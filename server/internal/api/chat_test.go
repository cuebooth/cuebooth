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

	"github.com/cuebooth/cuebooth/server/internal/chat"
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
}

func (f *fakeChat) Name() string     { return "fake" }
func (f *fakeChat) Authorized() bool { return f.authorized }

func (f *fakeChat) URL(context.Context) (string, error) {
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

func TestChatCallbackRejectsAFailedExchange(t *testing.T) {
	provider := &fakeChat{completeErr: errors.New("state is unknown or expired")}
	srv, hs, client := chatTestServer(t, provider)

	_, code := getText(t, client, hs.URL+chatCallbackPath+"?code=abc&state=forged")
	if code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", code)
	}
	if got := chatStatusInState(t, srv); got != string(chat.StatusNeedsAuth) {
		t.Errorf("status = %q after a failed exchange, want needs_auth", got)
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
