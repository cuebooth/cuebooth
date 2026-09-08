package api

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/cuebooth/cuebooth/server/internal/config"
)

const boundaryPublic = "http://production-pc.tailnet.test:7878"

func boundaryServer(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()
	cfg := testConfig()
	cfg.Chat = config.ChatConfig{
		Provider: "restream", ClientID: "id", ClientSecret: "s",
		PublicURL: boundaryPublic,
	}
	srv := NewServer(cfg, &fakePresser{}, WithChat(&fakeChat{
		authorized: true,
		url:        "https://chat.restream.io/embed?token=SECRET",
		loginURL:   "https://api.restream.io/login?state=xyz",
	}))
	hs := httptest.NewServer(srv.Handler())
	t.Cleanup(hs.Close)
	return srv, hs
}

// ask sends one request with a chosen Host and Sec-Fetch-Site, following no
// redirects, which is how a browser's fetch and a rebound page both look.
func ask(t *testing.T, hs *httptest.Server, path, host, site string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, hs.URL+path, nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	if host != "" {
		req.Host = host
	}
	if site != "" {
		req.Header.Set("Sec-Fetch-Site", site)
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

// The WebSocket's same-origin policy compares Origin against Host, both of
// which the requesting page supplies — so a page that has made this server's
// address into its own name satisfies it and is same-origin by the browser's
// rules. That is a fair boundary for pressing buttons in a room. /chat/url
// returns a live credential for the operator's chat account, which keeps
// working from anywhere afterwards, so it answers only on the address the
// operator configured.
func TestChatURLAnswersOnlyOnTheConfiguredAddress(t *testing.T) {
	_, hs := boundaryServer(t)

	onPublic := ask(t, hs, chatURLPath, "production-pc.tailnet.test:7878", "same-origin")
	if onPublic.StatusCode != http.StatusOK {
		t.Fatalf("on the configured address: status = %d, want 200", onPublic.StatusCode)
	}

	rebound := ask(t, hs, chatURLPath, "evil.example:7878", "same-origin")
	if rebound.StatusCode != http.StatusForbidden {
		t.Errorf("on a rebound address: status = %d, want 403 — the credential is reachable",
			rebound.StatusCode)
	}
}

// A browser tells the server when a request came from another site. Nothing
// used to read it.
func TestChatRoutesRefuseCrossSiteRequests(t *testing.T) {
	_, hs := boundaryServer(t)

	for _, path := range []string{chatURLPath, chatAuthPath, chatCallbackPath} {
		resp := ask(t, hs, path, "production-pc.tailnet.test:7878", "cross-site")
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("GET %s cross-site = %d, want 403", path, resp.StatusCode)
		}
	}
}

// An absent header is a native client, curl, or an older browser. Rejecting
// those would break the client the feature exists for; the address check is
// what covers them.
func TestChatRoutesAllowRequestsWithNoFetchMetadata(t *testing.T) {
	_, hs := boundaryServer(t)

	resp := ask(t, hs, chatURLPath, "production-pc.tailnet.test:7878", "")
	if resp.StatusCode != http.StatusOK {
		t.Errorf("GET with no Sec-Fetch-Site = %d, want 200", resp.StatusCode)
	}
}

// Starting an authorization allocates a pending state in a bounded map that
// evicts to stay bounded, so a burst inside the seconds an operator spends at
// the consent screen takes their state with it — and the page they come back to
// tells them to try again, forever. A person starts one authorization.
func TestChatAuthStartIsRateLimited(t *testing.T) {
	srv, hs := boundaryServer(t)

	allowed := 0
	for i := range chatStartBurst * 4 {
		resp := ask(t, hs, chatAuthPath, "production-pc.tailnet.test:7878", "same-origin")
		switch resp.StatusCode {
		case http.StatusFound:
			allowed++
		case http.StatusTooManyRequests:
		default:
			t.Fatalf("request %d: status = %d, want 302 or 429", i, resp.StatusCode)
		}
	}
	if allowed > chatStartBurst {
		t.Errorf("%d starts allowed in a burst, want at most %d", allowed, chatStartBurst)
	}
	if allowed == 0 {
		t.Fatal("no start was allowed at all; the limiter is not letting an operator through")
	}

	// And the budget comes back, or an operator who retries twice is locked out
	// of their own chat until a restart.
	if !srv.chatStarts.allow(time.Now().Add(chatStartEvery * chatStartBurst)) {
		t.Error("the limiter never refills")
	}
}

// Each callback holds a goroutine, its connection, and an authorization_code
// POST carrying the operator's client credentials. Restream redirects one
// browser here; a queue is something else.
//
// Driven through the handler rather than through the channel it uses: a cap the
// handler no longer consults is not a cap, and that difference is invisible
// from the other side.
func TestChatCallbacksAreBounded(t *testing.T) {
	release := make(chan struct{})
	// Closed exactly once, whether the test finishes or fails early — the
	// parked callbacks are holding real goroutines either way.
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	provider := &fakeChat{authorized: true, completeBlock: release}

	cfg := testConfig()
	cfg.Chat = config.ChatConfig{
		Provider: "restream", ClientID: "id", ClientSecret: "s",
		PublicURL: boundaryPublic,
	}
	hs := httptest.NewServer(NewServer(cfg, &fakePresser{}, WithChat(provider)).Handler())
	t.Cleanup(hs.Close)
	t.Cleanup(unblock)

	// Fill every slot with a callback parked inside the exchange.
	parked := make(chan int, chatCallbackSlots)
	for range chatCallbackSlots {
		go func() {
			resp := callback(t, hs, "code")
			parked <- resp
		}()
	}
	waitForBlockedCallbacks(t, provider, chatCallbackSlots)

	// The next one must be refused rather than queued.
	if got := callback(t, hs, "code"); got != http.StatusTooManyRequests {
		t.Errorf("callback beyond the cap = %d, want 429", got)
	}

	unblock()
	for range chatCallbackSlots {
		<-parked
	}
}

// callback issues one authorization callback and returns its status.
func callback(t *testing.T, hs *httptest.Server, code string) int {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, hs.URL+chatCallbackPath+"?code="+code+"&state=s", nil)
	if err != nil {
		t.Errorf("NewRequest: %v", err)
		return 0
	}
	req.Host = "production-pc.tailnet.test:7878"
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Errorf("callback: %v", err)
		return 0
	}
	defer resp.Body.Close()
	return resp.StatusCode
}

// waitForBlockedCallbacks waits until n callbacks are parked inside Complete.
func waitForBlockedCallbacks(t *testing.T, provider *fakeChat, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if provider.reached() >= n {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("only %d callbacks reached the exchange, want %d", provider.reached(), n)
}

// The page the operator's browser lands on after the redirect: its own URL
// carries the authorization code.
func TestChatCallbackPageIsNotCachedAndSendsNoReferrer(t *testing.T) {
	_, hs := boundaryServer(t)

	resp := ask(t, hs, chatCallbackPath, "production-pc.tailnet.test:7878", "same-origin")
	if got := resp.Header.Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
	if got := resp.Header.Get("Referrer-Policy"); got != "no-referrer" {
		t.Errorf("Referrer-Policy = %q, want no-referrer", got)
	}
}

// The frame headers were set by the web UI's handler, so they covered what was
// mounted under "/" and nothing registered ahead of it — including the HTML
// page the callback renders.
func TestEveryRouteCarriesTheFrameHeaders(t *testing.T) {
	_, hs := boundaryServer(t)

	for _, path := range []string{"/", "/nope.js", chatURLPath, chatAuthPath, chatCallbackPath} {
		resp := ask(t, hs, path, "production-pc.tailnet.test:7878", "same-origin")
		for header, want := range map[string]string{
			"X-Frame-Options":        "DENY",
			"X-Content-Type-Options": "nosniff",
		} {
			if got := resp.Header.Get(header); got != want {
				t.Errorf("GET %s: %s = %q, want %q", path, header, got, want)
			}
		}
		if got := resp.Header.Get("Content-Security-Policy"); got != "frame-ancestors 'none'" {
			t.Errorf("GET %s: CSP = %q", path, got)
		}
	}
}

func TestRateLimiterRefillsOverTime(t *testing.T) {
	start := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	l := newRateLimiter(2, time.Minute)

	for i := range 2 {
		if !l.allow(start) {
			t.Fatalf("permit %d refused from a full bucket", i)
		}
	}
	if l.allow(start) {
		t.Error("a third permit was granted from a bucket of two")
	}
	if !l.allow(start.Add(time.Minute)) {
		t.Error("no permit after a full interval")
	}
	if l.allow(start.Add(time.Minute)) {
		t.Error("one interval granted more than one permit")
	}

	// A long idle period refills to the burst and no further, or the bucket
	// becomes a way to bank an unbounded flood.
	granted := 0
	for range 10 {
		if l.allow(start.Add(time.Hour)) {
			granted++
		}
	}
	if granted != 2 {
		t.Errorf("after an hour idle, %d permits granted, want the burst of 2", granted)
	}
}
