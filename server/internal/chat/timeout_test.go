package chat

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// A client-level Timeout would cap the token request's own deadline invisibly,
// aborting the read of a rotation the platform has already performed — the
// exact loss the detached context exists to prevent.
func TestTokenRequestIsNotCappedByAClientTimeout(t *testing.T) {
	r, _, _ := newTestProvider(t, newFakeRestream())

	if r.http.Timeout != 0 {
		t.Errorf("http client Timeout = %v, want 0 so the token request's %v deadline governs",
			r.http.Timeout, tokenRequestTimeout)
	}
	if webchatTimeout >= tokenRequestTimeout {
		t.Errorf("webchat timeout %v is not shorter than the token timeout %v", webchatTimeout, tokenRequestTimeout)
	}
}

// A token endpoint slower than a shared client timeout would have rotated the
// pair before the read was abandoned. With the deadline set per request, a
// response inside tokenRequestTimeout is read back and stored.
func TestSlowTokenResponseIsStillReadBack(t *testing.T) {
	fake := newFakeRestream()
	base := fake.server(t)

	// Sits in front of the fake, delaying only the token endpoint.
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path == "/oauth/token" {
			time.Sleep(1500 * time.Millisecond)
		}
		proxyTo(t, base, w, req)
	}))
	t.Cleanup(slow.Close)

	clock := &testClock{t: time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)}
	tokenFile := t.TempDir() + "/chat-token.json"
	r, err := NewRestream(RestreamConfig{
		ClientID: "client-id", ClientSecret: "client-secret",
		RedirectURI: "http://x/cb", TokenFile: tokenFile,
	}, WithRestreamAPIBase(slow.URL), WithRestreamClock(clock.now))
	if err != nil {
		t.Fatalf("NewRestream: %v", err)
	}

	if err := authorize(t, r, "good-code"); err != nil {
		t.Fatalf("Complete through a slow endpoint: %v", err)
	}
	if !r.Authorized() {
		t.Error("a slow but successful exchange left the provider unauthorized")
	}
	if stored := readStoredTokens(t, tokenFile); stored.RefreshToken == "" {
		t.Error("a slow but successful exchange stored no credential")
	}
}

// A panel left open on an application missing chat.read must not rotate the
// credential once per attempt: each rotation is another chance to lose it.
func TestRefusedTokenIsNotRetriedUntilTheCooldownPasses(t *testing.T) {
	fake := newFakeRestream()
	fake.webchatStatus = http.StatusUnauthorized
	r, clock, _ := newTestProvider(t, fake)
	if err := authorize(t, r, "good-code"); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	if _, err := r.URL(context.Background()); !errors.Is(err, ErrNeedsAuth) {
		t.Fatalf("URL error = %v, want ErrNeedsAuth", err)
	}
	fake.mu.Lock()
	afterFirst := fake.tokenCalls
	fake.mu.Unlock()

	for range 5 {
		if _, err := r.URL(context.Background()); !errors.Is(err, ErrNeedsAuth) {
			t.Fatalf("URL during cooldown = %v, want ErrNeedsAuth", err)
		}
	}

	fake.mu.Lock()
	during := fake.tokenCalls
	fake.mu.Unlock()
	if during != afterFirst {
		t.Errorf("token endpoint called %d more times during the cooldown, want 0", during-afterFirst)
	}

	// The status clients render has to agree with what a request would get.
	if r.Authorized() {
		t.Error("provider reports authorized while the platform is refusing its tokens")
	}

	// After the cooldown one more attempt is allowed, so a transient fault is
	// not latched forever.
	clock.advance(refusalCooldown + time.Minute)
	if !r.Authorized() {
		t.Error("provider is still unauthorized after the cooldown passed")
	}
	if _, err := r.URL(context.Background()); !errors.Is(err, ErrNeedsAuth) {
		t.Fatalf("URL after cooldown = %v, want ErrNeedsAuth", err)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.tokenCalls <= during {
		t.Error("no attempt was made after the cooldown passed")
	}
}

// A fresh authorization is what a refusal was waiting for.
func TestAuthorizingClearsARefusal(t *testing.T) {
	fake := newFakeRestream()
	fake.webchatStatus = http.StatusUnauthorized
	r, _, _ := newTestProvider(t, fake)
	if err := authorize(t, r, "good-code"); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if _, err := r.URL(context.Background()); !errors.Is(err, ErrNeedsAuth) {
		t.Fatalf("URL error = %v, want ErrNeedsAuth", err)
	}
	if r.Authorized() {
		t.Fatal("provider is authorized during a refusal")
	}

	fake.mu.Lock()
	fake.webchatStatus = 0
	fake.mu.Unlock()

	if err := authorize(t, r, "good-code"); err != nil {
		t.Fatalf("re-authorize: %v", err)
	}
	if !r.Authorized() {
		t.Error("a fresh authorization did not clear the refusal")
	}
	if _, err := r.URL(context.Background()); err != nil {
		t.Errorf("URL after re-authorizing: %v", err)
	}
}

func proxyTo(t *testing.T, base string, w http.ResponseWriter, req *http.Request) {
	t.Helper()
	outbound, err := http.NewRequest(req.Method, base+req.URL.Path, req.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	outbound.Header = req.Header.Clone()
	resp, err := http.DefaultClient.Do(outbound)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	for k, vs := range resp.Header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, io.LimitReader(resp.Body, maxTokenBody))
}
