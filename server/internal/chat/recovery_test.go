package chat

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A revoked credential must send the operator back to authorization. Reporting
// it as a transport failure would leave the client showing "the platform is not
// responding" behind a Try again that can never succeed, with nothing in the UI
// reaching the authorization route.
func TestRevokedCredentialReportsNeedsAuthAndClearsItself(t *testing.T) {
	fake := newFakeRestream()
	r, clock, tokenFile := newTestProvider(t, fake)
	if err := authorize(t, r, "good-code"); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	// The operator revokes CueBooth in Restream's dashboard.
	fake.mu.Lock()
	fake.revoked = true
	fake.mu.Unlock()
	clock.advance(2 * time.Hour)

	if _, err := r.URL(context.Background()); !errors.Is(err, ErrNeedsAuth) {
		t.Fatalf("URL error = %v, want ErrNeedsAuth", err)
	}
	if r.Authorized() {
		t.Error("provider still reports authorized after the platform rejected its token")
	}

	// The dead pair must not survive a restart, or the server comes back up
	// claiming chat is ready and failing on every mint.
	stored := readStoredTokens(t, tokenFile)
	if stored.RefreshToken != "" {
		t.Errorf("stored refresh token = %q, want it cleared", stored.RefreshToken)
	}
	revived, err := NewRestream(RestreamConfig{
		ClientID: "client-id", ClientSecret: "client-secret",
		RedirectURI: "http://x/cb", TokenFile: tokenFile,
	})
	if err != nil {
		t.Fatalf("NewRestream after restart: %v", err)
	}
	if revived.Authorized() {
		t.Error("a restarted provider reports authorized from a cleared credential")
	}
}

// A mistyped or expired authorization code says nothing about the credential
// already held, so re-authorizing badly must not cost a working one.
func TestFailedReauthorizationKeepsTheExistingCredential(t *testing.T) {
	fake := newFakeRestream()
	r, _, _ := newTestProvider(t, fake)
	if err := authorize(t, r, "good-code"); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	if err := authorize(t, r, "wrong-code"); err == nil {
		t.Fatal("Complete accepted a bad code")
	}

	if !r.Authorized() {
		t.Error("a failed re-authorization discarded the working credential")
	}
	if _, err := r.URL(context.Background()); err != nil {
		t.Errorf("URL after a failed re-authorization: %v", err)
	}
}

// The route that mints these is an unauthenticated GET, so an unbounded map is
// a memory cost that any page the operator's browser visits could impose.
func TestPendingAuthorizationsAreBounded(t *testing.T) {
	r, _, _ := newTestProvider(t, newFakeRestream())

	firstURL, err := r.LoginURL()
	if err != nil {
		t.Fatalf("LoginURL: %v", err)
	}
	first := stateFrom(t, firstURL)

	for range maxPendingLogins * 4 {
		if _, err := r.LoginURL(); err != nil {
			t.Fatalf("LoginURL: %v", err)
		}
	}

	r.mu.Lock()
	pending := len(r.pending)
	r.mu.Unlock()
	if pending > maxPendingLogins {
		t.Errorf("pending authorizations = %d, want at most %d", pending, maxPendingLogins)
	}

	// Eviction takes the oldest, so the earliest start is gone while one made
	// after the flood still completes.
	if err := r.Complete(context.Background(), "good-code", first); err == nil {
		t.Error("the oldest pending authorization survived a flood past the cap")
	}
	if err := authorize(t, r, "good-code"); err != nil {
		t.Errorf("a fresh authorization could not complete after a flood: %v", err)
	}
}

// An access token Restream retires early is recoverable without an operator:
// one refresh distinguishes it from a credential that is genuinely spent.
func TestEarlyRetiredAccessTokenIsRefreshedOnce(t *testing.T) {
	fake := newFakeRestream()
	r, _, _ := newTestProvider(t, fake)
	if err := authorize(t, r, "good-code"); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	fake.mu.Lock()
	fake.unauthorizeWebchatOnce = true
	fake.mu.Unlock()

	got, err := r.URL(context.Background())
	if err != nil {
		t.Fatalf("URL: %v", err)
	}
	if want := "https://chat.restream.io/embed?token=access-2"; got != want {
		t.Errorf("URL = %q, want %q — the retry should mint from a refreshed token", got, want)
	}
}

// Chat is one panel. A credential file that cannot be read must not take the
// button surface, slide status, and everything else down with it — under the
// Windows SCM there is not even a console to say why.
func TestCorruptTokenFileStartsUnauthorizedRatherThanFailing(t *testing.T) {
	tokenFile := filepath.Join(t.TempDir(), "chat-token.json")
	partial := []byte("{\"access_token\":\"a\",\"refresh_tok")
	if err := os.WriteFile(tokenFile, partial, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	r, err := NewRestream(RestreamConfig{
		ClientID: "client-id", ClientSecret: "client-secret",
		RedirectURI: "http://x/cb", TokenFile: tokenFile,
	})
	if err != nil {
		t.Fatalf("NewRestream refused to start on an unreadable credential: %v", err)
	}
	if r.Authorized() {
		t.Error("provider reports authorized from an unparseable credential file")
	}
}
