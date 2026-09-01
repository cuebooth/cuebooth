package chat

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// This test drives the real Restream API, covering what a fake cannot: that our
// OAuth application is registered with the scope the webchat endpoint needs,
// that a stored refresh token still works, and that the URL Restream hands back
// is the embeddable one the client renders.
//
// It is skipped unless credentials are present, so `go test ./...` stays
// hermetic. To run it, register an application at developers.restream.io/apps
// with the chat.read scope, authorize a server once, and point these at the
// resulting client credentials and token file:
//
//	RESTREAM_CLIENT_ID=... RESTREAM_CLIENT_SECRET=... \
//	RESTREAM_TOKEN_FILE=/path/to/chat-token.json \
//	  go test ./internal/chat/ -run TestOnline -v
//
// The token file is rewritten in place, because a successful refresh
// invalidates the pair that produced it — point this at a copy, not at the
// credential a running server owns.
func liveProvider(t *testing.T) *Restream {
	t.Helper()

	id := os.Getenv("RESTREAM_CLIENT_ID")
	secret := os.Getenv("RESTREAM_CLIENT_SECRET")
	tokenFile := os.Getenv("RESTREAM_TOKEN_FILE")
	if id == "" || secret == "" || tokenFile == "" {
		t.Skip("RESTREAM_CLIENT_ID, RESTREAM_CLIENT_SECRET, and RESTREAM_TOKEN_FILE not all set; skipping live Restream test")
	}

	r, err := NewRestream(RestreamConfig{
		ClientID:     id,
		ClientSecret: secret,
		RedirectURI:  "http://localhost:7878/chat/auth/callback",
		TokenFile:    tokenFile,
	})
	if err != nil {
		t.Fatalf("NewRestream: %v", err)
	}
	if !r.Authorized() {
		t.Fatalf("no usable credential in %s; authorize a server against this application first", tokenFile)
	}
	return r
}

// The webchat URL is the whole feature: if Restream stops returning an
// embeddable chat.restream.io URL, or the application loses the chat.read
// scope, the client has nothing to render.
func TestOnlineWebchatURLIsEmbeddable(t *testing.T) {
	r := liveProvider(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	url, err := r.URL(ctx)
	if err != nil {
		t.Fatalf("URL: %v", err)
	}
	if !strings.HasPrefix(url, "https://chat.restream.io/embed") {
		// Reported by shape, not value: the URL carries a live token, and a test
		// log outlives the terminal it scrolls past.
		t.Errorf("webchat url does not start with https://chat.restream.io/embed (got %d chars)", len(url))
	}
	// Without a token the page has nothing to authenticate as, so an embed URL
	// that carries none would render a signed-out chat.
	if !strings.Contains(url, "token=") && !strings.Contains(url, "guestToken=") {
		t.Error("webchat url carries no token parameter")
	}
}

// Refreshing is what makes chat survive without an operator, and it is the one
// path a fake cannot prove: only Restream can confirm that a rotated token is
// accepted and its predecessor retired.
func TestOnlineRefreshRotatesTheStoredCredential(t *testing.T) {
	r := liveProvider(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	r.mu.Lock()
	before := r.tok.RefreshToken
	r.mu.Unlock()

	// Force a refresh rather than waiting out the access token's hour.
	r.mu.Lock()
	r.tok.AccessExpiry = time.Time{}
	r.mu.Unlock()

	if _, err := r.URL(ctx); err != nil {
		t.Fatalf("URL after forcing a refresh: %v", err)
	}

	r.mu.Lock()
	after := r.tok.RefreshToken
	r.mu.Unlock()

	if after == before {
		t.Error("refresh token is unchanged; Restream is no longer rotating it, which this design assumes")
	}
	stored, err := r.store.load()
	if err != nil {
		t.Fatalf("load stored tokens: %v", err)
	}
	if stored.RefreshToken != after {
		t.Error("the stored refresh token is not the rotated one; a restart would need re-authorization")
	}
}
