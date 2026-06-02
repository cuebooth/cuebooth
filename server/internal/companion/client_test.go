package companion

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func newTestClient(t *testing.T, h http.HandlerFunc, opts ...Option) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	// Fast, deterministic retries for tests.
	opts = append([]Option{WithRetries(2, time.Millisecond)}, opts...)
	c, err := New(srv.URL, opts...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

func TestPressVerbs(t *testing.T) {
	cases := []struct {
		name     string
		call     func(*Client, context.Context) error
		wantPath string
	}{
		{"press", func(c *Client, ctx context.Context) error { return c.Press(ctx, Location{7, 0, 2}) }, "/api/location/7/0/2/press"},
		{"down", func(c *Client, ctx context.Context) error { return c.PressDown(ctx, Location{1, 3, 2}) }, "/api/location/1/3/2/down"},
		{"up", func(c *Client, ctx context.Context) error { return c.PressUp(ctx, Location{1, 3, 2}) }, "/api/location/1/3/2/up"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var gotPath, gotMethod string
			c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				gotPath, gotMethod = r.URL.Path, r.Method
				w.WriteHeader(http.StatusOK)
			})
			if err := tc.call(c, context.Background()); err != nil {
				t.Fatalf("call: %v", err)
			}
			if gotMethod != http.MethodPost {
				t.Errorf("method = %q, want POST", gotMethod)
			}
			if gotPath != tc.wantPath {
				t.Errorf("path = %q, want %q", gotPath, tc.wantPath)
			}
		})
	}
}

func TestGetVariable(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/custom-variable/mute_choir/value" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if r.Method != http.MethodGet {
			t.Errorf("method = %q, want GET", r.Method)
		}
		_, _ = io.WriteString(w, "1")
	})
	got, err := c.GetVariable(context.Background(), "mute_choir")
	if err != nil {
		t.Fatalf("GetVariable: %v", err)
	}
	if got != "1" {
		t.Errorf("value = %q, want %q", got, "1")
	}
}

func TestSetVariable(t *testing.T) {
	var gotBody, gotCT string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %q, want POST", r.Method)
		}
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		gotCT = r.Header.Get("Content-Type")
		w.WriteHeader(http.StatusOK)
	})
	if err := c.SetVariable(context.Background(), "cue", "intro"); err != nil {
		t.Fatalf("SetVariable: %v", err)
	}
	if gotBody != "intro" {
		t.Errorf("body = %q, want %q", gotBody, "intro")
	}
	if gotCT != "text/plain" {
		t.Errorf("content-type = %q, want text/plain", gotCT)
	}
}

func TestGetModuleVariable(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/variable/obs/scene/value" {
			t.Errorf("path = %q", r.URL.Path)
		}
		_, _ = io.WriteString(w, "Camera + Slides")
	})
	got, err := c.GetModuleVariable(context.Background(), "obs", "scene")
	if err != nil {
		t.Fatalf("GetModuleVariable: %v", err)
	}
	if got != "Camera + Slides" {
		t.Errorf("value = %q", got)
	}
}

func TestRetryOnServerError(t *testing.T) {
	var calls atomic.Int32
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		// Fail twice with 503, then succeed — within the default 2 retries.
		if calls.Add(1) <= 2 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	if err := c.Press(context.Background(), Location{1, 0, 0}); err != nil {
		t.Fatalf("Press: %v", err)
	}
	if got := calls.Load(); got != 3 {
		t.Errorf("calls = %d, want 3 (2 failures + 1 success)", got)
	}
}

func TestRetryOn429(t *testing.T) {
	var calls atomic.Int32
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		// 429 is the only non-5xx status that retries — exercise that branch.
		if calls.Add(1) <= 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	if err := c.Press(context.Background(), Location{1, 0, 0}); err != nil {
		t.Fatalf("Press: %v", err)
	}
	if got := calls.Load(); got != 2 {
		t.Errorf("calls = %d, want 2 (429 then success)", got)
	}
}

func TestNoRetryOnClientError(t *testing.T) {
	var calls atomic.Int32
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Error(w, "no such button", http.StatusNotFound)
	})
	if err := c.Press(context.Background(), Location{1, 0, 0}); err == nil {
		t.Fatal("expected error on 404")
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("calls = %d, want 1 (4xx must not be retried)", got)
	}
}

func TestRetriesExhausted(t *testing.T) {
	var calls atomic.Int32
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusBadGateway)
	})
	if err := c.Press(context.Background(), Location{1, 0, 0}); err == nil {
		t.Fatal("expected error after exhausting retries")
	}
	if got := calls.Load(); got != 3 {
		t.Errorf("calls = %d, want 3 (1 initial + 2 retries)", got)
	}
}

func TestContextCancellation(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable) // always transient
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled
	if err := c.Press(ctx, Location{1, 0, 0}); err == nil {
		t.Fatal("expected error with cancelled context")
	}
}

// rtFunc adapts a function to an http.RoundTripper so tests can inject
// transport-level failures (which an httptest.Server can't easily produce).
type rtFunc func(*http.Request) (*http.Response, error)

func (f rtFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("body read boom") }

func clientWithTransport(t *testing.T, rt http.RoundTripper, opts ...Option) *Client {
	t.Helper()
	opts = append([]Option{WithRetries(2, time.Millisecond), WithHTTPClient(&http.Client{Transport: rt})}, opts...)
	c, err := New("http://companion.test", opts...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

// A non-idempotent POST must NOT be retried on a transport-level error: the
// request may already have actuated the button before the connection dropped.
func TestTransportErrorNotRetriedForPOST(t *testing.T) {
	var calls atomic.Int32
	c := clientWithTransport(t, rtFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return nil, errors.New("connection reset")
	}))
	if err := c.Press(context.Background(), Location{1, 0, 0}); err == nil {
		t.Fatal("expected error")
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("calls = %d, want 1 (POST transport error must not be retried)", got)
	}
}

// An idempotent GET is safe to retry on a transport error.
func TestTransportErrorRetriedForGET(t *testing.T) {
	var calls atomic.Int32
	c := clientWithTransport(t, rtFunc(func(*http.Request) (*http.Response, error) {
		if calls.Add(1) <= 2 {
			return nil, errors.New("connection reset")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("v"))}, nil
	}))
	got, err := c.GetVariable(context.Background(), "x")
	if err != nil {
		t.Fatalf("GetVariable: %v", err)
	}
	if got != "v" {
		t.Errorf("value = %q, want v", got)
	}
	if n := calls.Load(); n != 3 {
		t.Errorf("calls = %d, want 3 (GET transport error retried)", n)
	}
}

// A body-read failure on a successful (2xx) POST must be treated as success —
// the action already happened and the body is unused; re-sending would double-
// actuate.
func TestPostBodyReadErrorTreatedAsSuccess(t *testing.T) {
	var calls atomic.Int32
	c := clientWithTransport(t, rtFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return &http.Response{StatusCode: 200, Body: io.NopCloser(errReader{})}, nil
	}))
	if err := c.SetVariable(context.Background(), "cue", "intro"); err != nil {
		t.Fatalf("POST with unreadable 2xx body should succeed: %v", err)
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("calls = %d, want 1 (successful POST must not be re-sent)", got)
	}
}

// Cancelling the context mid-backoff returns promptly without another attempt.
func TestCancellationDuringBackoff(t *testing.T) {
	var calls atomic.Int32
	c := clientWithTransport(t, rtFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return &http.Response{StatusCode: 503, Body: io.NopCloser(strings.NewReader(""))}, nil
	}), WithRetries(5, 500*time.Millisecond))
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(20 * time.Millisecond); cancel() }()

	start := time.Now()
	if err := c.Press(ctx, Location{1, 0, 0}); err == nil {
		t.Fatal("expected error after cancellation")
	}
	if elapsed := time.Since(start); elapsed > 300*time.Millisecond {
		t.Errorf("took %v; a cancel during backoff should return promptly", elapsed)
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("calls = %d, want 1 (backoff cancelled before the 2nd attempt)", got)
	}
}

func TestNewValidation(t *testing.T) {
	for _, bad := range []string{"", "://nope", "ftp://host", "http://"} {
		if _, err := New(bad); err == nil {
			t.Errorf("New(%q) = nil error, want error", bad)
		}
	}
	if _, err := New("http://localhost:8000/"); err != nil {
		t.Errorf("New with trailing slash: %v", err)
	}
}
