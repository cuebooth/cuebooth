package chat

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// Restream invalidates the previous pair when it issues a replacement, so the
// window between the response arriving and the new pair reaching disk is a
// window in which the stored credential is already dead. Drain exists to close
// it. Counting only the HTTP request leaves it open, and the loss is silent:
// nothing warns, the file still parses, and the next start reports itself
// authorized right up until the first mint fails.
func TestDrainWaitsForTheRotationToReachDisk(t *testing.T) {
	var (
		mu        sync.Mutex
		entered   = make(chan struct{})
		enterOnce sync.Once
		released  = make(chan struct{})
		rotated   string
	)

	// A platform that hands back a rotated pair only once the test lets it, so
	// the exchange can be parked exactly where the window used to be.
	platform := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		enterOnce.Do(func() { close(entered) })
		<-released
		mu.Lock()
		rotated = "refresh-after-rotation"
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "access-after-rotation",
			"refresh_token": "refresh-after-rotation",
			"expires_in":    3600,
			"scope":         webchatScope,
		})
	}))
	t.Cleanup(platform.Close)

	dir := t.TempDir()
	tokenFile := filepath.Join(dir, "chat-token.json")
	seed := tokens{
		AccessToken:   "stale-access",
		RefreshToken:  "refresh-before-rotation",
		AccessExpiry:  time.Now().Add(-time.Hour),
		RefreshExpiry: time.Now().Add(24 * time.Hour),
	}
	store := &tokenStore{path: tokenFile}
	if err := store.save(seed); err != nil {
		t.Fatalf("seeding the token file: %v", err)
	}

	r, err := NewRestream(RestreamConfig{
		ClientID: "id", ClientSecret: "secret",
		RedirectURI: "http://host/chat/auth/callback", TokenFile: tokenFile,
	}, WithRestreamAPIBase(platform.URL))
	if err != nil {
		t.Fatalf("NewRestream: %v", err)
	}

	// A mint that must refresh, parked inside the exchange.
	refreshErr := make(chan error, 1)
	go func() {
		_, err := r.accessToken(context.Background(), "")
		refreshErr <- err
	}()

	// Wait until the exchange is genuinely in flight, or the drain below would
	// race the goroutine starting and prove nothing.
	select {
	case <-entered:
	case err := <-refreshErr:
		t.Fatalf("no exchange was attempted (accessToken returned %v); the seeded credential is wrong", err)
	case <-time.After(10 * time.Second):
		t.Fatal("the exchange never reached the platform")
	}

	// Let it complete, then immediately drain — this is the shutdown racing the
	// write.
	close(released)
	drainCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	r.Drain(drainCtx)

	// Drain has returned. Whatever the platform rotated to must already be on
	// disk, because the process is entitled to exit now.
	mu.Lock()
	want := rotated
	mu.Unlock()
	if want == "" {
		t.Fatal("the platform never rotated; the test proved nothing")
	}

	raw, err := os.ReadFile(tokenFile)
	if err != nil {
		t.Fatalf("reading the token file after Drain: %v", err)
	}
	if !strings.Contains(string(raw), want) {
		t.Errorf("Drain returned before the rotated credential reached disk:\n"+
			"file holds %s\nwant it to hold %q", raw, want)
	}

	if err := <-refreshErr; err != nil {
		t.Errorf("the refresh itself failed: %v", err)
	}
}
