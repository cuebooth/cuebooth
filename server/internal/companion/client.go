package companion

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DefaultTimeout bounds a single HTTP attempt against Companion. Companion runs
// on the same machine, so requests are normally sub-millisecond; this only
// trips when Companion is wedged or gone.
const DefaultTimeout = 5 * time.Second

// Default retry behaviour for transient failures (network errors, 5xx, 429).
const (
	defaultMaxRetries = 2
	defaultBackoff    = 100 * time.Millisecond
)

// Client is a thin HTTP client for the Bitfocus Companion remote-control API
// (the "HTTP Remote Control" page of the Bitfocus Companion user guide).
//
// It is safe for concurrent use by multiple goroutines.
//
// Note on button state: Companion's HTTP API has no endpoint to read a button's
// toggle state or style — it only actuates buttons (press/down/up). Reading
// "is this mute on?" is therefore done by reading a Companion variable that the
// operator wires to the button's feedback (see GetVariable / GetModuleVariable),
// not by querying the button. This matches design.md §7.8.
type Client struct {
	baseURL    string
	httpClient *http.Client
	maxRetries int
	backoff    time.Duration
	logger     *slog.Logger
}

// Option configures a Client.
type Option func(*Client)

// WithHTTPClient sets the underlying *http.Client. If the client has no Timeout
// set, DefaultTimeout is applied to a clone so a stuck Companion can't block a
// caller indefinitely.
func WithHTTPClient(hc *http.Client) Option {
	return func(c *Client) {
		if hc != nil {
			c.httpClient = hc
		}
	}
}

// WithRetries sets how many times a transient failure is retried (in addition
// to the initial attempt) and the base backoff between attempts. The backoff
// grows linearly with the attempt number. A maxRetries of 0 disables retrying;
// negative values are clamped to 0.
//
// Not every failure is retried: a transport-level error (lost/refused
// connection) is retried only for idempotent reads (GET), since re-sending a
// button press or variable write could double-actuate. A 5xx/429 response —
// which means Companion received the request but did not perform the action —
// is retried for any method.
func WithRetries(maxRetries int, backoff time.Duration) Option {
	return func(c *Client) {
		if maxRetries < 0 {
			maxRetries = 0
		}
		c.maxRetries = maxRetries
		if backoff > 0 {
			c.backoff = backoff
		}
	}
}

// WithLogger sets the logger used for retry diagnostics. Defaults to
// slog.Default().
func WithLogger(l *slog.Logger) Option {
	return func(c *Client) {
		if l != nil {
			c.logger = l
		}
	}
}

// New creates a Client targeting the Companion HTTP API rooted at baseURL
// (e.g. "http://localhost:8000"). baseURL must be an absolute http(s) URL.
func New(baseURL string, opts ...Option) (*Client, error) {
	u, err := url.Parse(baseURL)
	if err != nil {
		return nil, fmt.Errorf("companion: parse base URL %q: %w", baseURL, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("companion: base URL %q must be http or https", baseURL)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("companion: base URL %q is missing a host", baseURL)
	}

	c := &Client{
		baseURL:    strings.TrimRight(baseURL, "/"),
		httpClient: &http.Client{Timeout: DefaultTimeout},
		maxRetries: defaultMaxRetries,
		backoff:    defaultBackoff,
		logger:     slog.Default(),
	}
	for _, opt := range opts {
		opt(c)
	}
	// Guard against a caller-supplied client with no timeout.
	if c.httpClient.Timeout == 0 {
		clone := *c.httpClient
		clone.Timeout = DefaultTimeout
		c.httpClient = &clone
	}
	return c, nil
}

// Press runs both the down and up actions of a button (a normal click).
func (c *Client) Press(ctx context.Context, loc Location) error {
	return c.actuate(ctx, loc, "press")
}

// PressDown runs the button's down actions and holds (no release). Pair with
// PressUp for hold/release gestures.
func (c *Client) PressDown(ctx context.Context, loc Location) error {
	return c.actuate(ctx, loc, "down")
}

// PressUp runs the button's up actions, releasing a held button.
func (c *Client) PressUp(ctx context.Context, loc Location) error {
	return c.actuate(ctx, loc, "up")
}

func (c *Client) actuate(ctx context.Context, loc Location, verb string) error {
	path := fmt.Sprintf("/api/location/%d/%d/%d/%s", loc.Page, loc.Row, loc.Column, verb)
	_, err := c.do(ctx, http.MethodPost, path, "", "")
	if err != nil {
		return fmt.Errorf("companion: %s button %s: %w", verb, loc, err)
	}
	return nil
}

// GetVariable reads the value of a Companion custom variable by name.
func (c *Client) GetVariable(ctx context.Context, name string) (string, error) {
	if name == "" {
		return "", errors.New("companion: variable name is empty")
	}
	path := fmt.Sprintf("/api/custom-variable/%s/value", url.PathEscape(name))
	body, err := c.do(ctx, http.MethodGet, path, "", "")
	if err != nil {
		return "", fmt.Errorf("companion: get variable %q: %w", name, err)
	}
	return string(body), nil
}

// SetVariable sets a Companion custom variable to value. The value is sent as a
// text/plain body so it round-trips verbatim regardless of its contents.
func (c *Client) SetVariable(ctx context.Context, name, value string) error {
	if name == "" {
		return errors.New("companion: variable name is empty")
	}
	path := fmt.Sprintf("/api/custom-variable/%s/value", url.PathEscape(name))
	if _, err := c.do(ctx, http.MethodPost, path, value, "text/plain"); err != nil {
		return fmt.Errorf("companion: set variable %q: %w", name, err)
	}
	return nil
}

// GetModuleVariable reads a module/connection variable, identified by the
// connection's label and the variable name (Companion path
// /api/variable/<label>/<name>/value). Module variables are how most live
// device state (OBS scene, streaming status, etc.) is exposed to the HTTP API.
func (c *Client) GetModuleVariable(ctx context.Context, connectionLabel, name string) (string, error) {
	if connectionLabel == "" || name == "" {
		return "", errors.New("companion: module variable requires a connection label and name")
	}
	path := fmt.Sprintf("/api/variable/%s/%s/value", url.PathEscape(connectionLabel), url.PathEscape(name))
	body, err := c.do(ctx, http.MethodGet, path, "", "")
	if err != nil {
		return "", fmt.Errorf("companion: get module variable %s:%s: %w", connectionLabel, name, err)
	}
	return string(body), nil
}

// do issues an HTTP request to the Companion API with bounded retries on
// transient failures. It returns the response body on a 2xx response.
func (c *Client) do(ctx context.Context, method, path, body, contentType string) ([]byte, error) {
	var lastErr error
	for attempt := 0; attempt <= c.maxRetries; attempt++ {
		if attempt > 0 {
			if err := sleep(ctx, c.backoff*time.Duration(attempt)); err != nil {
				return nil, err
			}
			c.logger.Debug("retrying companion request", "method", method, "path", path, "attempt", attempt)
		}

		respBody, retryable, err := c.attempt(ctx, method, path, body, contentType)
		if err == nil {
			return respBody, nil
		}
		lastErr = err
		// Don't burn a retry if the caller's context is already done, or the
		// error is permanent (e.g. a 4xx).
		if !retryable || ctx.Err() != nil {
			return nil, err
		}
	}
	return nil, lastErr
}

// attempt performs a single HTTP request. The returned bool reports whether the
// error (if any) is worth retrying.
func (c *Client) attempt(ctx context.Context, method, path, body, contentType string) ([]byte, bool, error) {
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return nil, false, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}

	// Only idempotent reads (GET) may be retried on an ambiguous failure. A
	// transport error or lost response on a POST might mean Companion already
	// pressed the button or applied the write, so re-sending could double-
	// actuate. (Trade-off: a POST that fails with connection-refused — which
	// definitely never reached Companion — also isn't retried; we surface the
	// error and favor never double-actuating. Reads, including state polling,
	// retry as before.)
	idempotent := method == http.MethodGet

	resp, err := c.httpClient.Do(req)
	if err != nil {
		// No response received: retry only idempotent requests. (do owns the
		// cancellation rule — it stops retrying once ctx is done regardless of
		// this flag.)
		return nil, idempotent, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		if !idempotent {
			// Success. The body is unused for actuation/writes, but drain it so
			// net/http can reuse the keep-alive connection across rapid presses
			// instead of opening a new socket each time. A read error here is
			// irrelevant — the action already happened — and must not re-send.
			_, _ = io.Copy(io.Discard, resp.Body)
			return nil, false, nil
		}
		respBody, readErr := io.ReadAll(resp.Body)
		if readErr != nil {
			return nil, true, fmt.Errorf("read response body: %w", readErr)
		}
		return respBody, false, nil
	}

	// Non-2xx: Companion responded, so the request reached it and completed
	// without performing the action. That makes 5xx/429 safe to retry for ANY
	// method — unlike a transport error (above), where a POST's fate is unknown,
	// a status response means no side effect occurred. (Companion's API is a
	// direct localhost endpoint with no intermediary that could emit a 5xx after
	// the action ran.) 5xx and 429 are transient; other 4xx are permanent (bad
	// request, unknown button/variable) and aren't retried.
	respBody, _ := io.ReadAll(resp.Body)
	retryable := resp.StatusCode >= 500 || resp.StatusCode == http.StatusTooManyRequests
	return nil, retryable, fmt.Errorf("unexpected status %s: %s", resp.Status, strings.TrimSpace(string(respBody)))
}

// sleep waits for d or until ctx is done, whichever comes first.
func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
