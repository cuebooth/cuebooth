package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/cuebooth/cuebooth/server/internal/chat"
	"github.com/cuebooth/cuebooth/server/internal/config"
)

// fakeChat is a chat.Provider whose every answer the test dictates.
type fakeChat struct {
	authorized  bool
	url         string
	urlErr      error
	loginURL    string
	loginErr    error
	completeErr error
	completes   int
	// urlDeadline records whether the context the handler passed carried one,
	// and how long it had left.
	urlHadDeadline bool
	urlBudget      time.Duration
}

func (f *fakeChat) Name() string     { return "fake" }
func (f *fakeChat) Authorized() bool { return f.authorized }

func (f *fakeChat) URL(ctx context.Context) (string, error) {
	if deadline, ok := ctx.Deadline(); ok {
		f.urlHadDeadline = true
		f.urlBudget = time.Until(deadline)
	}
	if f.urlErr != nil {
		return "", f.urlErr
	}
	if !f.authorized {
		return "", chat.ErrNeedsAuth
	}
	return f.url, nil
}

func (f *fakeChat) LoginURL() (string, error) {
	if f.loginErr != nil {
		return "", f.loginErr
	}
	return f.loginURL, nil
}

func (f *fakeChat) Complete(_ context.Context, code, state string) error {
	f.completes++
	if f.completeErr != nil {
		return f.completeErr
	}
	f.authorized = true
	return nil
}

// chatTestServer starts the API with p as its chat provider. A nil p leaves
// chat unconfigured, which is how a deployment without it runs.
func chatTestServer(t *testing.T, p chat.Provider) (*Server, *httptest.Server, *http.Client) {
	t.Helper()
	opts := []Option{WithServerID("test-server"), WithVersion("9.9.9")}
	if p != nil {
		opts = append(opts, WithChat(p))
	}
	srv := NewServer(testConfig(), &fakePresser{}, opts...)
	hs := httptest.NewServer(srv.Handler())
	t.Cleanup(hs.Close)

	// Redirects are the thing under test on the auth-start route, so they are
	// returned rather than followed.
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	return srv, hs, client
}

func getJSON(t *testing.T, client *http.Client, url string) (int, map[string]any) {
	t.Helper()
	resp, err := client.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	var payload map[string]any
	if len(body) > 0 {
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Fatalf("parse body %q: %v", body, err)
		}
	}
	return resp.StatusCode, payload
}

func TestChatURLReturnsTheMintedURL(t *testing.T) {
	provider := &fakeChat{authorized: true, url: "https://chat.restream.io/embed?token=abc"}
	_, hs, client := chatTestServer(t, provider)

	code, payload := getJSON(t, client, hs.URL+chatURLPath)
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	if got := payload["url"]; got != provider.url {
		t.Errorf("url = %v, want %q", got, provider.url)
	}
}

// An unauthorized server answers with the path that starts authorization, not a
// minted login URL: a client polling here would otherwise leave one pending
// authorization state behind per request.
func TestChatURLReportsNeedsAuthWithAStartPath(t *testing.T) {
	_, hs, client := chatTestServer(t, &fakeChat{})

	code, payload := getJSON(t, client, hs.URL+chatURLPath)
	if code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", code)
	}
	if got := payload["status"]; got != string(chat.StatusNeedsAuth) {
		t.Errorf("status field = %v, want %q", got, chat.StatusNeedsAuth)
	}
	if got := payload["auth_start"]; got != chatAuthPath {
		t.Errorf("auth_start = %v, want %q", got, chatAuthPath)
	}
}

// A credential can be revoked between snapshots. Without republishing, a client
// holding an older "ready" would render a platform error behind a Try again
// that can never succeed, with nothing in the UI reaching authorization.
func TestChatURLRepublishesNeedsAuthWhenTheCredentialDies(t *testing.T) {
	provider := &fakeChat{authorized: true, url: "https://chat.restream.io/embed?token=abc"}
	srv, hs, client := chatTestServer(t, provider)

	if got := chatStatusInState(t, srv); got != string(chat.StatusReady) {
		t.Fatalf("status = %q, want ready before revocation", got)
	}

	provider.authorized = false
	code, payload := getJSON(t, client, hs.URL+chatURLPath)

	if code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", code)
	}
	if got := payload["auth_start"]; got != chatAuthPath {
		t.Errorf("auth_start = %v, want %q", got, chatAuthPath)
	}
	if got := chatStatusInState(t, srv); got != string(chat.StatusNeedsAuth) {
		t.Errorf("published status = %q, want needs_auth", got)
	}
}

// A mint can involve a refresh and the webchat call behind it, twice if the
// first token is refused. Unbounded, the answer could outlast any client
// waiting for it, and the client would report a working server as unreachable.
func TestChatURLBoundsHowLongAMintMayTake(t *testing.T) {
	provider := &fakeChat{authorized: true, url: "https://chat.restream.io/embed?token=abc"}
	_, hs, client := chatTestServer(t, provider)

	if code, _ := getJSON(t, client, hs.URL+chatURLPath); code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}

	if !provider.urlHadDeadline {
		t.Fatal("the provider was called with no deadline; a hung mint would hang the request")
	}
	if provider.urlBudget > chatMintDeadline {
		t.Errorf("mint budget %v exceeds chatMintDeadline %v", provider.urlBudget, chatMintDeadline)
	}
}

func TestChatURLSurfacesProviderFailure(t *testing.T) {
	_, hs, client := chatTestServer(t, &fakeChat{authorized: true, urlErr: errors.New("platform down")})

	code, _ := getJSON(t, client, hs.URL+chatURLPath)
	if code != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", code)
	}
}

func TestChatAuthStartRedirectsToThePlatform(t *testing.T) {
	provider := &fakeChat{loginURL: "https://api.restream.io/login?state=xyz"}
	_, hs, client := chatTestServer(t, provider)

	resp, err := client.Get(hs.URL + chatAuthPath)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusFound {
		t.Fatalf("status = %d, want 302", resp.StatusCode)
	}
	if got := resp.Header.Get("Location"); got != provider.loginURL {
		t.Errorf("Location = %q, want %q", got, provider.loginURL)
	}
}

func TestChatCallbackCompletesAndPublishesReady(t *testing.T) {
	provider := &fakeChat{}
	srv, hs, client := chatTestServer(t, provider)

	if got := chatStatusInState(t, srv); got != string(chat.StatusNeedsAuth) {
		t.Fatalf("status before authorization = %q, want needs_auth", got)
	}

	resp, err := client.Get(hs.URL + chatCallbackPath + "?code=abc&state=xyz")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if provider.completes != 1 {
		t.Errorf("Complete called %d times, want 1", provider.completes)
	}
	// A client showing the connect prompt has to switch over without
	// reconnecting, which only happens if the callback republishes state.
	if got := chatStatusInState(t, srv); got != string(chat.StatusReady) {
		t.Errorf("status after authorization = %q, want ready", got)
	}
}

func TestChatCallbackWithoutCodeReportsCancellation(t *testing.T) {
	provider := &fakeChat{}
	_, hs, client := chatTestServer(t, provider)

	body, code := getText(t, client, hs.URL+chatCallbackPath)
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	if provider.completes != 0 {
		t.Errorf("Complete was called for a declined authorization")
	}
	if !strings.Contains(body, "cancelled") {
		t.Errorf("page did not say the authorization was cancelled: %q", body)
	}
}

// The provider starts authorized so the published status is `ready`: a handler
// that republished on the failure path would flip it, and starting from
// needs_auth could not tell the two behaviours apart.
func TestChatCallbackRejectsAFailedExchangeWithoutDisturbingState(t *testing.T) {
	provider := &fakeChat{
		authorized:  true,
		url:         "https://chat.restream.io/embed?token=abc",
		completeErr: errors.New("state is unknown or expired"),
	}
	srv, hs, client := chatTestServer(t, provider)

	if got := chatStatusInState(t, srv); got != string(chat.StatusReady) {
		t.Fatalf("status = %q before the callback, want ready", got)
	}

	_, code := getText(t, client, hs.URL+chatCallbackPath+"?code=abc&state=forged")
	if code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", code)
	}
	if got := chatStatusInState(t, srv); got != string(chat.StatusReady) {
		t.Errorf("status = %q after a failed exchange, want the unchanged ready", got)
	}
}

// The callback lands on the configured public URL, which must be one the
// platform has registered. A start that arrived at some other address — a LAN
// address where public_url names a VPN host — is sent there first.
func TestChatAuthStartRedirectsToThePublicURLFirst(t *testing.T) {
	provider := &fakeChat{loginURL: "https://api.restream.io/login?state=xyz"}
	cfg := testConfig()
	cfg.Chat = config.ChatConfig{
		Provider: "restream", ClientID: "id", ClientSecret: "s",
		PublicURL: "http://production-pc.tailnet.test:7878",
	}
	srv := NewServer(cfg, &fakePresser{}, WithChat(provider))
	hs := httptest.NewServer(srv.Handler())
	t.Cleanup(hs.Close)
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}

	resp, err := client.Get(hs.URL + chatAuthPath)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("status = %d, want 302", resp.StatusCode)
	}
	got := resp.Header.Get("Location")
	want := "http://production-pc.tailnet.test:7878" + chatAuthPath + "?" + chatAuthViaPublic + "=1"
	if got != want {
		t.Errorf("Location = %q, want %q", got, want)
	}

	// Arriving with the marker must not bounce again, whatever the Host, or a
	// proxy that rewrites it would loop the browser forever.
	marked, err := client.Get(hs.URL + chatAuthPath + "?" + chatAuthViaPublic + "=1")
	if err != nil {
		t.Fatalf("GET marked: %v", err)
	}
	marked.Body.Close()
	if loc := marked.Header.Get("Location"); loc != provider.loginURL {
		t.Errorf("marked request redirected to %q, want the platform login %q", loc, provider.loginURL)
	}
}

// A request that already arrived at the configured public URL must go straight
// to the platform. Without the host comparison every Connect tap would take an
// extra hop through public_url, which §11 warns may be unreachable from where
// the operator is standing.
func TestChatAuthStartDoesNotRedirectWhenAlreadyPublic(t *testing.T) {
	provider := &fakeChat{loginURL: "https://api.restream.io/login?state=xyz"}
	cfg := testConfig()
	srv := NewServer(cfg, &fakePresser{}, WithChat(provider))
	hs := httptest.NewServer(srv.Handler())
	t.Cleanup(hs.Close)
	// The address is only known once the server is listening, and the config is
	// held by pointer, so it can be pointed at itself here.
	cfg.Chat.PublicURL = hs.URL

	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	resp, err := client.Get(hs.URL + chatAuthPath)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	resp.Body.Close()
	if loc := resp.Header.Get("Location"); loc != provider.loginURL {
		t.Errorf("Location = %q, want the platform login %q", loc, provider.loginURL)
	}
}

// An unset public_url leaves nothing to compare against, so the handler must
// proceed rather than redirect to a bare path.
func TestChatAuthStartProceedsWithoutAPublicURL(t *testing.T) {
	provider := &fakeChat{loginURL: "https://api.restream.io/login?state=xyz"}
	_, hs, client := chatTestServer(t, provider)

	resp, err := client.Get(hs.URL + chatAuthPath)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	resp.Body.Close()
	if loc := resp.Header.Get("Location"); loc != provider.loginURL {
		t.Errorf("Location = %q, want the platform login %q", loc, provider.loginURL)
	}
}

func TestChatCallbackNamesAMissingScope(t *testing.T) {
	provider := &fakeChat{completeErr: chat.ErrMissingScope}
	_, hs, client := chatTestServer(t, provider)

	body, code := getText(t, client, hs.URL+chatCallbackPath+"?code=abc&state=xyz")
	if code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", code)
	}
	// Without naming the scope the operator re-authorizes forever against an
	// application that can never work.
	if !strings.Contains(body, "chat.read") {
		t.Errorf("page did not name the missing scope: %q", body)
	}
}

// A deployment without chat should not answer these routes at all, rather than
// exposing endpoints that can only fail.
func TestChatRoutesAreAbsentWithoutAProvider(t *testing.T) {
	srv, hs, client := chatTestServer(t, nil)

	for _, path := range []string{chatURLPath, chatAuthPath, chatCallbackPath} {
		resp, err := client.Get(hs.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", path, resp.StatusCode)
		}
	}

	_, data := srv.store.Snapshot(map[string]bool{"stream": true})
	if _, ok := data["stream"]; ok {
		t.Error("stream state was published with no chat provider configured")
	}
}

func TestChatRoutesRejectNonGET(t *testing.T) {
	_, hs, client := chatTestServer(t, &fakeChat{authorized: true, url: "https://example.test/chat"})

	for _, path := range []string{chatURLPath, chatAuthPath, chatCallbackPath} {
		resp, err := client.Post(hs.URL+path, "application/json", strings.NewReader("{}"))
		if err != nil {
			t.Fatalf("POST %s: %v", path, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusMethodNotAllowed {
			t.Errorf("POST %s = %d, want 405", path, resp.StatusCode)
		}
	}
}

func getText(t *testing.T, client *http.Client, url string) (string, int) {
	t.Helper()
	resp, err := client.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return string(body), resp.StatusCode
}

// chatStatusInState reads stream.chat.status out of a fresh snapshot.
func chatStatusInState(t *testing.T, srv *Server) string {
	t.Helper()
	_, data := srv.store.Snapshot(map[string]bool{"stream": true})
	stream, ok := data["stream"].(map[string]any)
	if !ok {
		t.Fatalf("stream missing from snapshot: %#v", data)
	}
	chatState, ok := stream["chat"].(map[string]any)
	if !ok {
		t.Fatalf("stream.chat missing from snapshot: %#v", stream)
	}
	status, _ := chatState["status"].(string)
	return status
}
