package chat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeRestream stands in for Restream's OAuth and webchat endpoints, including
// the behaviour this package exists to cope with: every successful refresh
// invalidates the pair that produced it, so a stale refresh token is rejected.
type fakeRestream struct {
	mu sync.Mutex

	seq          int
	access       string
	refresh      string
	tokenCalls   int
	webchatCalls int

	// refreshExpiresIn is the documented one-year lifetime, in seconds; zero
	// omits the field so the absent-field path is exercisable.
	refreshExpiresIn int
	// accessExpiresIn defaults to Restream's documented hour.
	accessExpiresIn int
	// webchatStatus overrides the webchat response code when non-zero.
	webchatStatus int
	// revoked makes every refresh fail the way Restream reports a retired or
	// revoked token: a 400 invalid_grant.
	revoked bool
	// unauthorizeWebchatOnce answers the next webchat call 401, as Restream does
	// when an access token is retired before its stated expiry.
	unauthorizeWebchatOnce bool
	// tokenErrorName and tokenErrorMessage override what a rejected token
	// request reports, so a 400 that is not invalid_grant can be exercised.
	tokenErrorName    string
	tokenErrorMessage string
	// forbidStaleToken answers a request carrying anything but the current access
	// token with 403 rather than 401, standing in for a permission the platform
	// will not serve regardless of how fresh the token is.
	forbidStaleToken bool
	// beforeToken and beforeWebchat run before the handler answers, letting a
	// test park a request in flight. Read without the lock held so a hook can
	// block without stalling the whole fake.
	beforeToken   func()
	beforeWebchat func()
	// scope is what the token response reports granting. Defaults to a set
	// including chat.read; empty omits the field entirely.
	scope string
}

// testAddr stands in for the browser address that started an authorization; the
// callback must arrive from the same one.
const testAddr = "192.0.2.10"

func newFakeRestream() *fakeRestream {
	return &fakeRestream{refreshExpiresIn: 31536000, accessExpiresIn: 3600,
		scope: "profile.read channels.read chat.read stream.read"}
}

func (f *fakeRestream) server(t *testing.T) string {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/oauth/token", f.handleToken)
	mux.HandleFunc("/v2/user/webchat/url", f.handleWebchat)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv.URL
}

func (f *fakeRestream) handleToken(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	hook := f.beforeToken
	f.mu.Unlock()
	if hook != nil {
		hook()
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	f.tokenCalls++

	id, secret, ok := r.BasicAuth()
	if !ok || id != "client-id" || secret != "client-secret" {
		writeRestreamError(w, http.StatusUnauthorized, "invalid client credentials")
		return
	}
	if err := r.ParseForm(); err != nil {
		writeRestreamError(w, http.StatusBadRequest, "malformed body")
		return
	}

	switch r.Form.Get("grant_type") {
	case "authorization_code":
		if r.Form.Get("code") != "good-code" {
			writeRestreamError(w, http.StatusBadRequest, "Invalid grant: authorization code is invalid")
			return
		}
	case "refresh_token":
		if f.revoked || r.Form.Get("refresh_token") != f.refresh {
			name, msg := "invalid_grant", "Invalid grant: refresh token is invalid"
			if f.tokenErrorName != "" {
				name, msg = f.tokenErrorName, f.tokenErrorMessage
			}
			writeRestreamErrorNamed(w, http.StatusBadRequest, name, msg)
			return
		}
	default:
		writeRestreamError(w, http.StatusBadRequest, "unsupported grant_type")
		return
	}

	f.seq++
	f.access = fmt.Sprintf("access-%d", f.seq)
	f.refresh = fmt.Sprintf("refresh-%d", f.seq)

	payload := map[string]any{
		"access_token":  f.access,
		"refresh_token": f.refresh,
		"expires_in":    f.accessExpiresIn,
	}
	if f.scope != "" {
		payload["scope"] = f.scope
	}
	if f.refreshExpiresIn > 0 {
		payload["refreshTokenExpiresIn"] = f.refreshExpiresIn
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(payload)
}

func (f *fakeRestream) handleWebchat(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	hook := f.beforeWebchat
	f.mu.Unlock()
	if hook != nil {
		hook()
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	f.webchatCalls++

	if f.webchatStatus != 0 {
		writeRestreamError(w, f.webchatStatus, "Invalid token: access token is invalid")
		return
	}
	if f.unauthorizeWebchatOnce {
		f.unauthorizeWebchatOnce = false
		writeRestreamError(w, http.StatusUnauthorized, "Invalid token: access token is invalid")
		return
	}
	if r.Header.Get("Authorization") != "Bearer "+f.access {
		if f.forbidStaleToken {
			writeRestreamErrorNamed(w, http.StatusForbidden, "insufficient_scope",
				"Insufficient scope: authorized scope is insufficient")
			return
		}
		writeRestreamError(w, http.StatusUnauthorized, "Invalid token: access token is invalid")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{
		"webchatUrl": "https://chat.restream.io/embed?token=" + f.access,
	})
}

func writeRestreamError(w http.ResponseWriter, code int, msg string) {
	writeRestreamErrorNamed(w, code, "invalid_grant", msg)
}

func writeRestreamErrorNamed(w http.ResponseWriter, code int, name, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]any{"statusCode": code, "message": msg, "name": name},
	})
}

// testClock is a hand-wound clock so token expiry can be crossed without waiting.
type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *testClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *testClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// newTestProvider wires a provider to fake against a token file in t.TempDir.
func newTestProvider(t *testing.T, fake *fakeRestream) (*Restream, *testClock, string) {
	t.Helper()
	base := fake.server(t)
	clock := &testClock{t: time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)}
	tokenFile := filepath.Join(t.TempDir(), "chat-token.json")

	r, err := NewRestream(RestreamConfig{
		ClientID:     "client-id",
		ClientSecret: "client-secret",
		RedirectURI:  "http://production-pc:7878/chat/auth/callback",
		TokenFile:    tokenFile,
	}, WithRestreamAPIBase(base), WithRestreamClock(clock.now))
	if err != nil {
		t.Fatalf("NewRestream: %v", err)
	}
	return r, clock, tokenFile
}

// authorize runs the browser half of the handshake the way the API handlers do.
func authorize(t *testing.T, r *Restream, code string) error {
	t.Helper()
	loginURL, err := r.LoginURL()
	if err != nil {
		t.Fatalf("LoginURL: %v", err)
	}
	u, err := url.Parse(loginURL)
	if err != nil {
		t.Fatalf("parse login url: %v", err)
	}
	return r.Complete(context.Background(), code, u.Query().Get("state"))
}

// stateFrom pulls the state parameter out of a login URL.
func stateFrom(t *testing.T, loginURL string) string {
	t.Helper()
	u, err := url.Parse(loginURL)
	if err != nil {
		t.Fatalf("parse login url: %v", err)
	}
	return u.Query().Get("state")
}

func readStoredTokens(t *testing.T, path string) tokens {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read token file: %v", err)
	}
	var tok tokens
	if err := json.Unmarshal(data, &tok); err != nil {
		t.Fatalf("parse token file: %v", err)
	}
	return tok
}

func TestLoginURLCarriesTheDocumentedParameters(t *testing.T) {
	r, _, _ := newTestProvider(t, newFakeRestream())

	loginURL, err := r.LoginURL()
	if err != nil {
		t.Fatalf("LoginURL: %v", err)
	}
	u, err := url.Parse(loginURL)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	q := u.Query()
	if got := q.Get("response_type"); got != "code" {
		t.Errorf("response_type = %q, want code", got)
	}
	if got := q.Get("client_id"); got != "client-id" {
		t.Errorf("client_id = %q", got)
	}
	if got := q.Get("redirect_uri"); got != "http://production-pc:7878/chat/auth/callback" {
		t.Errorf("redirect_uri = %q", got)
	}
	if q.Get("state") == "" {
		t.Error("state parameter is empty; nothing would bind the callback to this request")
	}
}

func TestCompleteStoresTheCredential(t *testing.T) {
	fake := newFakeRestream()
	r, _, tokenFile := newTestProvider(t, fake)

	if r.Authorized() {
		t.Fatal("provider reports authorized before any exchange")
	}
	if err := authorize(t, r, "good-code"); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if !r.Authorized() {
		t.Error("provider is not authorized after a successful exchange")
	}

	stored := readStoredTokens(t, tokenFile)
	if stored.RefreshToken != "refresh-1" {
		t.Errorf("stored refresh token = %q, want refresh-1", stored.RefreshToken)
	}
	// A restart must resume without an operator, so the file is the thing that
	// has to carry the credential — not just the in-memory provider.
	revived, err := NewRestream(RestreamConfig{
		ClientID: "client-id", ClientSecret: "client-secret",
		RedirectURI: "http://x/cb", TokenFile: tokenFile,
	})
	if err != nil {
		t.Fatalf("NewRestream after restart: %v", err)
	}
	if !revived.Authorized() {
		t.Error("a provider restarted against the stored file is not authorized")
	}
}

func TestBadCodeLeavesProviderUnauthorized(t *testing.T) {
	r, _, _ := newTestProvider(t, newFakeRestream())

	err := authorize(t, r, "wrong-code")
	if err == nil {
		t.Fatal("Complete accepted a code the platform rejected")
	}
	if r.Authorized() {
		t.Error("provider reports authorized after a failed exchange")
	}
}

func TestURLWithoutCredentialReportsNeedsAuth(t *testing.T) {
	r, _, _ := newTestProvider(t, newFakeRestream())

	_, err := r.URL(context.Background())
	if !errors.Is(err, ErrNeedsAuth) {
		t.Fatalf("URL error = %v, want ErrNeedsAuth", err)
	}
}

func TestURLMintsFromTheStoredCredential(t *testing.T) {
	fake := newFakeRestream()
	r, _, _ := newTestProvider(t, fake)
	if err := authorize(t, r, "good-code"); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	got, err := r.URL(context.Background())
	if err != nil {
		t.Fatalf("URL: %v", err)
	}
	if want := "https://chat.restream.io/embed?token=access-1"; got != want {
		t.Errorf("URL = %q, want %q", got, want)
	}
}

// The rotation this guards is the whole reason the token file is treated as
// state: the pair that produced a refresh is dead afterwards, so if the new one
// were not written before it is used, a crash would cost a re-authorization.
func TestRefreshPersistsTheRotatedTokenBeforeUse(t *testing.T) {
	fake := newFakeRestream()
	r, clock, tokenFile := newTestProvider(t, fake)
	if err := authorize(t, r, "good-code"); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	// Past the access token's hour, so the next call has to refresh.
	clock.advance(2 * time.Hour)

	if _, err := r.URL(context.Background()); err != nil {
		t.Fatalf("URL after expiry: %v", err)
	}

	stored := readStoredTokens(t, tokenFile)
	if stored.RefreshToken != "refresh-2" {
		t.Fatalf("stored refresh token = %q, want the rotated refresh-2", stored.RefreshToken)
	}
	if stored.AccessToken != "access-2" {
		t.Errorf("stored access token = %q, want access-2", stored.AccessToken)
	}
	// The pair that produced the refresh must genuinely be dead, or the
	// assertion above says nothing about rotation.
	stale := &Restream{
		clientID: "client-id", clientSecret: "client-secret",
		apiBase: r.apiBase, http: r.http, logger: r.logger, now: clock.now,
		tok: tokens{RefreshToken: "refresh-1"},
	}
	if _, err := stale.URL(context.Background()); err == nil {
		t.Error("the rotated-away refresh token still worked; the platform fake is not rotating")
	}
}

func TestFreshAccessTokenIsReusedWithoutRefreshing(t *testing.T) {
	fake := newFakeRestream()
	r, _, _ := newTestProvider(t, fake)
	if err := authorize(t, r, "good-code"); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	for range 3 {
		if _, err := r.URL(context.Background()); err != nil {
			t.Fatalf("URL: %v", err)
		}
	}

	fake.mu.Lock()
	defer fake.mu.Unlock()
	// One call for the code exchange, none after: refreshing on every mint would
	// rotate the credential needlessly and multiply the chance of losing a write.
	if fake.tokenCalls != 1 {
		t.Errorf("token endpoint called %d times, want 1", fake.tokenCalls)
	}
	if fake.webchatCalls != 3 {
		t.Errorf("webchat endpoint called %d times, want 3", fake.webchatCalls)
	}
}

func TestRefreshHappensBeforeTheAccessTokenActuallyLapses(t *testing.T) {
	fake := newFakeRestream()
	r, clock, _ := newTestProvider(t, fake)
	if err := authorize(t, r, "good-code"); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	// Inside the skew window: still valid, but close enough that a request could
	// outlive it.
	clock.advance(time.Hour - refreshSkew/2)
	if _, err := r.URL(context.Background()); err != nil {
		t.Fatalf("URL: %v", err)
	}

	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.tokenCalls != 2 {
		t.Errorf("token endpoint called %d times, want 2 (exchange + pre-emptive refresh)", fake.tokenCalls)
	}
}

// Two goroutines refreshing at once would each spend the same refresh token;
// the fake rejects the second, which is what real rotation does.
func TestConcurrentMintsRefreshOnlyOnce(t *testing.T) {
	fake := newFakeRestream()
	r, clock, _ := newTestProvider(t, fake)
	if err := authorize(t, r, "good-code"); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	clock.advance(2 * time.Hour)

	const callers = 8
	errs := make(chan error, callers)
	var start sync.WaitGroup
	start.Add(1)
	for range callers {
		go func() {
			start.Wait()
			_, err := r.URL(context.Background())
			errs <- err
		}()
	}
	start.Done()

	for range callers {
		if err := <-errs; err != nil {
			t.Fatalf("concurrent URL: %v", err)
		}
	}

	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.tokenCalls != 2 {
		t.Errorf("token endpoint called %d times, want 2 (exchange + one shared refresh)", fake.tokenCalls)
	}
}

func TestLapsedRefreshTokenReportsNeedsAuth(t *testing.T) {
	fake := newFakeRestream()
	r, clock, _ := newTestProvider(t, fake)
	if err := authorize(t, r, "good-code"); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	// Past the documented one-year refresh lifetime with no use in between.
	clock.advance(400 * 24 * time.Hour)

	if r.Authorized() {
		t.Error("provider reports authorized with a lapsed refresh token")
	}
	if _, err := r.URL(context.Background()); !errors.Is(err, ErrNeedsAuth) {
		t.Fatalf("URL error = %v, want ErrNeedsAuth", err)
	}
}

// Restream sends the refresh lifetime only in a camelCase field. Treating its
// absence as "expired" would lock an operator out on a field they never see.
func TestAbsentRefreshLifetimeIsTreatedAsUsable(t *testing.T) {
	fake := newFakeRestream()
	fake.refreshExpiresIn = 0
	r, clock, _ := newTestProvider(t, fake)
	if err := authorize(t, r, "good-code"); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	clock.advance(400 * 24 * time.Hour)
	if !r.Authorized() {
		t.Error("provider treated an unknown refresh expiry as lapsed")
	}
}

func TestCallbackStateIsSingleUse(t *testing.T) {
	r, _, _ := newTestProvider(t, newFakeRestream())

	loginURL, err := r.LoginURL()
	if err != nil {
		t.Fatalf("LoginURL: %v", err)
	}
	u, _ := url.Parse(loginURL)
	state := u.Query().Get("state")

	if err := r.Complete(context.Background(), "good-code", state); err != nil {
		t.Fatalf("first Complete: %v", err)
	}
	if err := r.Complete(context.Background(), "good-code", state); err == nil {
		t.Error("a replayed callback state was accepted a second time")
	}
}

func TestUnknownCallbackStateIsRejected(t *testing.T) {
	r, _, _ := newTestProvider(t, newFakeRestream())

	if err := r.Complete(context.Background(), "good-code", "not-a-state-we-issued"); err == nil {
		t.Fatal("Complete accepted a state it never issued")
	}
	if r.Authorized() {
		t.Error("provider became authorized from an unsolicited callback")
	}
}

func TestExpiredCallbackStateIsRejected(t *testing.T) {
	r, clock, _ := newTestProvider(t, newFakeRestream())

	loginURL, err := r.LoginURL()
	if err != nil {
		t.Fatalf("LoginURL: %v", err)
	}
	u, _ := url.Parse(loginURL)

	clock.advance(loginStateTTL + time.Minute)
	if err := r.Complete(context.Background(), "good-code", u.Query().Get("state")); err == nil {
		t.Error("Complete accepted a state past its lifetime")
	}
}

func TestCompleteRejectsAnEmptyCode(t *testing.T) {
	r, _, _ := newTestProvider(t, newFakeRestream())

	if err := r.Complete(context.Background(), "", "whatever"); err == nil {
		t.Error("Complete accepted an empty authorization code")
	}
}

func TestPlatformErrorMessageReachesTheCaller(t *testing.T) {
	fake := newFakeRestream()
	// A server-side failure rather than a 401: an access token the platform
	// refuses is classified as needing authorization, which deliberately
	// carries no platform detail to the caller.
	fake.webchatStatus = http.StatusInternalServerError
	r, _, _ := newTestProvider(t, fake)
	if err := authorize(t, r, "good-code"); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	_, err := r.URL(context.Background())
	if err == nil {
		t.Fatal("URL succeeded against a rejecting platform")
	}
	// The operator's log is the only place this failure is visible, so the
	// platform's own wording has to survive.
	if got := err.Error(); !strings.Contains(got, "access token is invalid") {
		t.Errorf("error = %q, want it to carry the platform message", got)
	}
}

func TestNewRestreamRequiresCredentials(t *testing.T) {
	cases := []struct {
		name string
		cfg  RestreamConfig
	}{
		{"no client id", RestreamConfig{ClientSecret: "s", RedirectURI: "http://x/cb"}},
		{"no client secret", RestreamConfig{ClientID: "i", RedirectURI: "http://x/cb"}},
		{"no redirect uri", RestreamConfig{ClientID: "i", ClientSecret: "s"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := NewRestream(tc.cfg); err == nil {
				t.Error("NewRestream accepted an incomplete configuration")
			}
		})
	}
}
