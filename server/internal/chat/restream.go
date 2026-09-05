package chat

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"
)

// ErrNeedsAuth reports that no usable credential is held, so an operator has to
// authorize through LoginURL before chat can be shown.
var ErrNeedsAuth = errors.New("chat provider is not authorized")

// errInvalidGrant reports that the platform rejected the grant itself — a
// refresh token it has retired or an operator has revoked. Only a fresh
// authorization recovers, so it is handled apart from transport failures, which
// a later attempt may well survive. OAuth 2.0 requires a 400 for this case
// (RFC 6749 §5.2), which is what distinguishes it on the wire.
var errInvalidGrant = errors.New("restream rejected the grant")

// errUnauthorized reports that an access token was refused. It can mean the
// token was retired before the expiry Restream stated, which one refresh
// resolves.
var errUnauthorized = errors.New("restream refused the access token")

// errForbidden reports a token the platform accepts but will not serve this
// endpoint with — its insufficient_scope. A refresh reissues the same
// permissions, so no unattended retry changes the answer.
var errForbidden = errors.New("restream refused the request as out of scope")

const (
	// defaultRestreamAPI is Restream's API root. Tests point this at an httptest
	// server; the login, token, and webchat endpoints all hang off it.
	defaultRestreamAPI = "https://api.restream.io"

	// refreshSkew renews an access token before it actually lapses, so a request
	// that is already in flight cannot cross the expiry boundary.
	refreshSkew = 2 * time.Minute

	// loginStateTTL bounds how long a started-but-unfinished browser
	// authorization stays acceptable, capping how long a leaked state parameter
	// is worth anything.
	loginStateTTL = 10 * time.Minute

	// maxPendingLogins bounds authorizations started but never finished. The
	// route that creates them is an unauthenticated GET, so without a ceiling
	// any page the operator's browser visits could grow this map for the whole
	// loginStateTTL. An operator has one browser and one tab open at a time;
	// this leaves room for retries and abandoned attempts.
	maxPendingLogins = 64

	// maxTokenBody caps how much of a token or webchat response is read. These
	// are small JSON documents; the limit keeps a misdirected endpoint from
	// streaming into memory.
	maxTokenBody = 1 << 20

	// tokenRequestTimeout bounds a token exchange or refresh. It replaces the
	// caller's deadline rather than narrowing it, because the rotation must be
	// read back even when the client that triggered it has gone. The HTTP client
	// carries no Timeout of its own, which would cap this one invisibly.
	tokenRequestTimeout = 20 * time.Second

	// assumedAccessLifetime stands in when the platform states no access-token
	// lifetime. It matches Restream's documented hour.
	assumedAccessLifetime = time.Hour

	// webchatTimeout bounds a webchat URL fetch. Unlike a token request this one
	// may be abandoned freely — nothing is consumed by asking.
	webchatTimeout = 10 * time.Second

	// drainPollInterval is how often shutdown re-checks for token requests still
	// in flight. Short enough not to add noticeable delay to a clean stop.
	drainPollInterval = 20 * time.Millisecond

	// refusalCooldown is how long a token the platform refused immediately after
	// a refresh is treated as unusable. Nothing the server does unattended will
	// change that answer, so retrying only spends another rotation.
	refusalCooldown = 5 * time.Minute

	// webchatScope is the permission the webchat endpoint requires. Restream
	// selects scopes per application rather than per authorization request, so an
	// application registered without it yields a credential that can never mint a
	// chat URL.
	webchatScope = "chat.read"

	// insufficientScopeName is what Restream reports when a token is valid but
	// the application lacks the permission. Anything else answering 403 is
	// something in front of them, which a later attempt may survive.
	insufficientScopeName = "insufficient_scope"

	// invalidGrantName is the error Restream reports when the grant itself is
	// rejected — a retired or revoked refresh token. Other 400s (a rotated
	// client secret, a bad minute on their gateway) name something else and must
	// not cost a year-long credential.
	invalidGrantName = "invalid_grant"
)

// RestreamConfig is what an operator supplies to enable Restream chat.
type RestreamConfig struct {
	ClientID     string
	ClientSecret string
	// RedirectURI must exactly match one registered on the Restream application;
	// Restream rejects the code exchange otherwise. It points back at this
	// server's callback route.
	RedirectURI string
	// TokenFile is where the rotating credential is persisted. Empty keeps
	// authorization in memory only, which costs a re-login on every restart.
	TokenFile string
}

// Restream is the Restream chat provider: it holds the OAuth credential and
// mints the embeddable chat URL clients render.
//
// Restream rotates the refresh token on every use — the previous pair is
// invalidated the moment a refresh succeeds — so the newly issued pair is
// persisted immediately rather than at shutdown.
type Restream struct {
	clientID     string
	clientSecret string
	redirectURI  string
	apiBase      string
	http         *http.Client
	logger       *slog.Logger
	now          func() time.Time
	store        *tokenStore

	// refreshMu serializes token refreshes. Restream invalidates the old pair
	// the moment one refresh succeeds, so two concurrent refreshes would spend
	// the same refresh token and the loser would strand the credential.
	refreshMu sync.Mutex

	mu sync.Mutex
	// tok is the live credential; pending maps the state parameter of each
	// started-but-unfinished authorization to its deadline.
	tok     tokens
	pending map[string]time.Time
	// refusedUntil holds off further attempts after the platform refused a
	// freshly refreshed token, so a panel left open on a misconfigured
	// application does not rotate the credential once per state change.
	refusedUntil time.Time
	// gen counts authorizations completed. An attempt carries the value it
	// started with, so its answer cannot hold off a credential authorized since.
	gen uint64

	// tokenCalls counts token requests in flight. They must be read back to
	// capture the rotation the platform has already performed, so shutdown waits
	// for them rather than exiting mid-exchange.
	tokenCalls int
}

// RestreamOption adjusts a Restream provider at construction.
type RestreamOption func(*Restream)

// WithRestreamHTTPClient replaces the HTTP client used for Restream calls.
func WithRestreamHTTPClient(c *http.Client) RestreamOption {
	return func(r *Restream) {
		if c != nil {
			r.http = c
		}
	}
}

// WithRestreamAPIBase overrides Restream's API root.
func WithRestreamAPIBase(base string) RestreamOption {
	return func(r *Restream) {
		if base != "" {
			r.apiBase = strings.TrimRight(base, "/")
		}
	}
}

// WithRestreamLogger sets the logger (default slog.Default()).
func WithRestreamLogger(l *slog.Logger) RestreamOption {
	return func(r *Restream) {
		if l != nil {
			r.logger = l
		}
	}
}

// WithRestreamClock replaces the clock, for tests that drive token expiry.
func WithRestreamClock(now func() time.Time) RestreamOption {
	return func(r *Restream) {
		if now != nil {
			r.now = now
		}
	}
}

// NewRestream builds a Restream provider and loads any credential already on
// disk, so a restart resumes without an operator present.
func NewRestream(cfg RestreamConfig, opts ...RestreamOption) (*Restream, error) {
	if cfg.ClientID == "" || cfg.ClientSecret == "" {
		return nil, errors.New("restream chat requires client_id and client_secret")
	}
	if cfg.RedirectURI == "" {
		return nil, errors.New("restream chat requires a redirect_uri")
	}

	r := &Restream{
		clientID:     cfg.ClientID,
		clientSecret: cfg.ClientSecret,
		redirectURI:  cfg.RedirectURI,
		apiBase:      defaultRestreamAPI,
		// No client-level Timeout: each call sets its own deadline, and a shared
		// one would silently cap the token request's, which must outlive the
		// caller so a rotation is read back.
		http:    &http.Client{},
		logger:  slog.Default(),
		now:     time.Now,
		pending: make(map[string]time.Time),
	}
	for _, opt := range opts {
		opt(r)
	}
	if cfg.TokenFile != "" {
		r.store = &tokenStore{path: cfg.TokenFile}
	} else {
		r.logger.Warn("no chat token_file configured; chat will need authorizing again after every restart")
	}

	tok, err := r.store.load()
	if err != nil {
		// Chat is one panel; the operator still needs the button surface, the
		// slide status, and everything else. Refusing to start would take the
		// whole control surface down over an accessory's state file — and under
		// the Windows SCM there is no console to explain why. Start
		// unauthorized instead, which puts a Connect button in front of them.
		r.logger.Error("could not read the stored chat credential; chat will need authorizing again",
			"err", err, "path", cfg.TokenFile)
		tok = tokens{}
	}
	r.tok = tok
	return r, nil
}

// Name identifies the platform on the wire.
func (r *Restream) Name() string { return "restream" }

// Authorized reports whether a credential is held that could currently mint a
// chat URL. A token the platform has just refused counts as unusable for the
// cooldown, so the status clients render matches what a request would get.
func (r *Restream) Authorized() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	return r.tok.valid(now) && !now.Before(r.refusedUntil)
}

// LoginURL builds Restream's authorize dialog URL and records the state
// parameter Complete will require back. Restream takes no scope parameter —
// scopes are selected per application in their dashboard, and chat.read is the
// one the webchat endpoint needs.
func (r *Restream) LoginURL() (string, error) {
	state, err := randomState()
	if err != nil {
		return "", err
	}

	now := r.now()
	r.mu.Lock()
	r.prunePendingLocked(now)
	for len(r.pending) >= maxPendingLogins {
		r.evictOldestPendingLocked()
	}
	r.pending[state] = now.Add(loginStateTTL)
	r.mu.Unlock()

	q := url.Values{}
	q.Set("response_type", "code")
	q.Set("client_id", r.clientID)
	q.Set("redirect_uri", r.redirectURI)
	q.Set("state", state)
	return r.apiBase + "/login?" + q.Encode(), nil
}

// Complete exchanges an authorization code for a token pair. state must be one
// LoginURL issued and has not been used: it is consumed here whether or not the
// exchange succeeds, so a replayed callback cannot re-run the exchange.
func (r *Restream) Complete(ctx context.Context, code, state string) error {
	if code == "" {
		return errors.New("restream callback carried no authorization code")
	}

	now := r.now()
	r.mu.Lock()
	r.prunePendingLocked(now)
	deadline, ok := r.pending[state]
	delete(r.pending, state)
	r.mu.Unlock()

	if !ok || now.After(deadline) {
		return errors.New("restream callback state is unknown or expired")
	}

	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("redirect_uri", r.redirectURI)
	form.Set("code", code)

	// Held for the same reason a refresh holds it: an exchange landing beside an
	// in-flight refresh could have its freshly authorized pair overwritten by
	// the older one.
	r.refreshMu.Lock()
	defer r.refreshMu.Unlock()

	tok, err := r.postToken(ctx, form)
	if err != nil {
		// A rejected code says nothing about the credential already held, so an
		// operator who mistypes their way through a re-authorization does not
		// lose a working one.
		return err
	}
	// Restream names the granted scopes in the exchange response. Without
	// chat.read the credential authorizes nothing this feature can use, and
	// every later attempt would 401 — a loop that only re-registering the
	// application escapes, which the operator has to be told.
	if tok.Scope != "" && !slices.Contains(scopeList(tok.Scope), webchatScope) {
		return fmt.Errorf("%w: granted %q, needs %s", ErrMissingScope, tok.Scope, webchatScope)
	}
	r.adopt(tok, true)
	return nil
}

// URL mints a chat URL to display. The token embedded in it is Restream's to
// expire, so this is called each time a client needs one rather than cached.
func (r *Restream) URL(ctx context.Context) (string, error) {
	// Captured before the attempt so a credential authorized while this one was
	// in flight is not held off by its stale answer.
	gen := r.generation()

	chatURL, used, err := r.mint(ctx, "")
	if errors.Is(err, errUnauthorized) {
		// Restream retired the access token before the expiry it stated —
		// a revoked session, or a reset on their side. One refresh separates
		// that from a credential that is genuinely spent. The retry retires the
		// token this attempt actually used, so callers that were refused
		// together share one rotation rather than taking one each.
		chatURL, _, err = r.mint(ctx, used)
	}
	if errors.Is(err, errUnauthorized) || errors.Is(err, errForbidden) {
		// Either a token minted moments ago was still refused, or the platform
		// accepted it and refused the permission. Neither improves unattended —
		// most often the application was registered without chat.read. Held off
		// for a while so a panel left open cannot rotate the credential once per
		// attempt, and reported as needing authorization so the operator has a
		// route to it.
		r.logger.Error("restream will not serve chat with this credential; check the application's chat.read scope", "err", err)
		r.holdOff(gen)
		return "", ErrNeedsAuth
	}
	return chatURL, err
}

// holdOff suppresses further attempts for a while, unless the credential has
// been replaced since gen was taken.
func (r *Restream) holdOff(gen uint64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.gen != gen {
		return
	}
	r.refusedUntil = r.now().Add(refusalCooldown)
}

func (r *Restream) generation() uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.gen
}

// mint fetches a chat URL and reports the access token it used. A retry needs
// that token in order to retire exactly the one that was refused, so callers
// refused together share one rotation rather than taking one each.
//
// retire names a token the caller already saw refused; it forces a refresh only
// while that token is still the one held.
func (r *Restream) mint(ctx context.Context, retire string) (chatURL, used string, err error) {
	token, err := r.accessToken(ctx, retire)
	if err != nil {
		return "", "", err
	}

	ctx, cancel := context.WithTimeout(ctx, webchatTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.apiBase+"/v2/user/webchat/url", nil)
	if err != nil {
		return "", token, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")

	resp, err := r.http.Do(req)
	if err != nil {
		return "", token, fmt.Errorf("restream webchat url: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxTokenBody))
	if err != nil {
		return "", token, fmt.Errorf("restream webchat url: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		name, message := restreamError(body)
		switch resp.StatusCode {
		case http.StatusUnauthorized:
			return "", token, fmt.Errorf("%w: %s", errUnauthorized, message)
		case http.StatusForbidden:
			// Only the platform's own insufficient_scope means the permission is
			// wrong; a proxy or rate limiter in front of it also answers 403, and
			// telling an operator to fix a scope that is already correct sends
			// them round a loop nothing can end.
			if name == insufficientScopeName {
				return "", token, fmt.Errorf("%w: %s", errForbidden, message)
			}
			return "", token, fmt.Errorf("restream webchat url: %s: %s", resp.Status, message)
		default:
			return "", token, fmt.Errorf("restream webchat url: %s: %s", resp.Status, message)
		}
	}

	var payload struct {
		WebchatURL string `json:"webchatUrl"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return "", token, fmt.Errorf("restream webchat url: parse response: %w", err)
	}
	if payload.WebchatURL == "" {
		return "", token, errors.New("restream webchat url: response carried no webchatUrl")
	}
	return payload.WebchatURL, token, nil
}

// accessToken returns a usable bearer token, refreshing when the held one is
// spent or close enough to expiry that a request could outlive it.
func (r *Restream) accessToken(ctx context.Context, retire string) (string, error) {
	r.refreshMu.Lock()
	defer r.refreshMu.Unlock()

	// Re-read under refreshMu: a caller that waited here may find the token it
	// would have fetched already refreshed by the caller ahead of it.
	r.mu.Lock()
	tok := r.tok
	r.mu.Unlock()

	now := r.now()
	if !tok.valid(now) || now.Before(r.refusalDeadline()) {
		return "", ErrNeedsAuth
	}
	// retire names a token the caller saw refused. If it is no longer the one
	// held, a caller ahead of this one already replaced it, and refreshing again
	// would spend a rotation per concurrent failure.
	forced := retire != "" && retire == tok.AccessToken
	if !forced && tok.AccessToken != "" && now.Add(refreshSkew).Before(tok.AccessExpiry) {
		return tok.AccessToken, nil
	}

	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", tok.RefreshToken)

	fresh, err := r.postToken(ctx, form)
	if err != nil {
		if errors.Is(err, errInvalidGrant) {
			// The stored pair is dead — revoked, or rotated away by another
			// process holding a copy. Keeping it would leave the provider
			// reporting itself ready while every mint failed, stranding the
			// operator on an error with no route back to authorization.
			//
			// Logged here because ErrNeedsAuth carries no detail to the client,
			// and this is the only place the platform says why.
			r.logger.Error("restream rejected the stored credential; chat needs authorizing again", "err", err)
			r.discard()
			return "", ErrNeedsAuth
		}
		return "", err
	}
	r.adopt(fresh, false)
	return fresh.AccessToken, nil
}

// Drain waits for token requests already issued, so a rotation the platform has
// performed is read back and persisted before the process exits. It returns
// when they finish or ctx is done.
func (r *Restream) Drain(ctx context.Context) {
	tick := time.NewTicker(drainPollInterval)
	defer tick.Stop()
	for {
		r.mu.Lock()
		inFlight := r.tokenCalls
		r.mu.Unlock()
		if inFlight == 0 {
			return
		}
		select {
		case <-ctx.Done():
			r.logger.Warn("exiting with a chat token request in flight; chat may need authorizing again")
			return
		case <-tick.C:
		}
	}
}

func (r *Restream) refusalDeadline() time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.refusedUntil
}

// discard drops a credential the platform has rejected, so Authorized stops
// claiming chat is ready.
func (r *Restream) discard() {
	r.mu.Lock()
	r.tok = tokens{}
	r.mu.Unlock()

	if err := r.store.save(tokens{}); err != nil {
		r.logger.Error("could not clear the rejected chat credential", "err", err)
	}
}

// adopt takes a newly issued pair as the live credential and persists it.
//
// The write happens before the token is handed back because the exchange that
// produced it has already invalidated its predecessor: from here on, the copy
// on disk is the only thing that survives a restart. A failed write is logged
// rather than propagated — the process still holds a working token, and failing
// chat outright would turn a storage problem into an outage.
func (r *Restream) adopt(t tokens, authorized bool) {
	r.mu.Lock()
	r.tok = t
	// A fresh credential is exactly what a refusal was waiting for.
	r.refusedUntil = time.Time{}
	if authorized {
		// Bumped with the credential, not after it: an attempt that started
		// earlier must not see the new token and the old generation.
		r.gen++
	}
	r.mu.Unlock()

	if err := r.store.save(t); err != nil {
		r.logger.Error("could not persist restream credential; a restart will need re-authorization", "err", err)
	}
}

// postToken runs one call against Restream's token endpoint. The client
// credentials go in a Basic Auth header rather than the body, which is what
// Restream recommends so they cannot end up in an intermediary's logs.
//
// The caller's cancellation is deliberately dropped. Restream rotates the pair
// on receipt, so abandoning the request does not abandon the rotation: the
// credential is spent either way, and only reading the response recovers the
// replacement. A client that closes its connection mid-refresh would otherwise
// leave the stored token dead.
func (r *Restream) postToken(ctx context.Context, form url.Values) (tokens, error) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), tokenRequestTimeout)
	defer cancel()

	r.mu.Lock()
	r.tokenCalls++
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		r.tokenCalls--
		r.mu.Unlock()
	}()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.apiBase+"/oauth/token", strings.NewReader(form.Encode()))
	if err != nil {
		return tokens{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.SetBasicAuth(r.clientID, r.clientSecret)

	resp, err := r.http.Do(req)
	if err != nil {
		return tokens{}, fmt.Errorf("restream token request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxTokenBody))
	if err != nil {
		return tokens{}, fmt.Errorf("restream token request: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		name, message := restreamError(body)
		// Only the grant being rejected justifies destroying the stored
		// credential, so the platform's own error name decides it rather than
		// the status code alone.
		if name == invalidGrantName && resp.StatusCode == http.StatusBadRequest {
			return tokens{}, fmt.Errorf("%w: %s", errInvalidGrant, message)
		}
		return tokens{}, fmt.Errorf("restream token request: %s: %s", resp.Status, message)
	}

	var payload struct {
		AccessToken      string `json:"access_token"`
		RefreshToken     string `json:"refresh_token"`
		ExpiresIn        int    `json:"expires_in"`
		RefreshExpiresIn int    `json:"refreshTokenExpiresIn"`
		Scope            string `json:"scope"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return tokens{}, fmt.Errorf("restream token request: parse response: %w", err)
	}
	if payload.AccessToken == "" || payload.RefreshToken == "" {
		return tokens{}, errors.New("restream token request: response was missing a token")
	}

	now := r.now()
	tok := tokens{
		AccessToken:  payload.AccessToken,
		RefreshToken: payload.RefreshToken,
		AccessExpiry: now.Add(time.Duration(payload.ExpiresIn) * time.Second),
		Scope:        payload.Scope,
	}
	// An absent lifetime would leave the token instantly stale, refreshing on
	// every mint — and each rotation is another chance to lose the credential.
	// Restream documents an hour; assume it, and let a 401 correct the guess.
	if payload.ExpiresIn <= 0 {
		tok.AccessExpiry = now.Add(assumedAccessLifetime)
	}
	// Restream documents a one-year refresh lifetime and restarts that clock on
	// every refresh, but only sends it in the camelCase field. Leaving the
	// expiry zero when it is absent means "unknown", which tokens.valid treats
	// as usable rather than locking the operator out on a missing field.
	if payload.RefreshExpiresIn > 0 {
		tok.RefreshExpiry = now.Add(time.Duration(payload.RefreshExpiresIn) * time.Second)
	}
	return tok, nil
}

// prunePendingLocked drops authorization states that were never completed.
// Callers hold r.mu.
func (r *Restream) prunePendingLocked(now time.Time) {
	for state, deadline := range r.pending {
		if now.After(deadline) {
			delete(r.pending, state)
		}
	}
}

// evictOldestPendingLocked drops the authorization closest to expiry, keeping
// the map bounded when starts arrive faster than they are completed. Callers
// hold r.mu.
func (r *Restream) evictOldestPendingLocked() {
	var oldest string
	var deadline time.Time
	for state, d := range r.pending {
		if oldest == "" || d.Before(deadline) {
			oldest, deadline = state, d
		}
	}
	delete(r.pending, oldest)
}

// restreamError pulls the error name and message out of Restream's envelope.
// The name is empty when the body does not match it, which keeps an
// unrecognized error off the destructive path.
func restreamError(body []byte) (name, message string) {
	var envelope struct {
		Error struct {
			Name    string `json:"name"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &envelope); err == nil && envelope.Error.Message != "" {
		return envelope.Error.Name, envelope.Error.Message
	}
	if len(body) > 200 {
		body = body[:200]
	}
	return "", strings.TrimSpace(string(body))
}

// scopeList splits a granted-scope string. Restream's token response separates
// them with spaces, and their capture-the-code documentation describes the same
// value as comma-separated, so both are accepted.
func scopeList(scope string) []string {
	return strings.FieldsFunc(scope, func(r rune) bool {
		return r == ' ' || r == ',' || r == '\t' || r == '\n'
	})
}

func randomState() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate oauth state: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}
