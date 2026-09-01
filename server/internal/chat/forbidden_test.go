package chat

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"
)

// Restream answers insufficient_scope with 403, not 401. Classified as a
// platform outage it would leave the panel on "not responding" behind a Try
// again that can never succeed, with nothing in the UI reaching authorization —
// and the published status would stay ready, so no connect prompt either.
func TestForbiddenWebchatReportsNeedsAuth(t *testing.T) {
	fake := newFakeRestream()
	fake.webchatStatus = http.StatusForbidden
	r, _, _ := newTestProvider(t, fake)
	if err := authorize(t, r, "good-code"); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	if _, err := r.URL(context.Background()); !errors.Is(err, ErrNeedsAuth) {
		t.Fatalf("URL error = %v, want ErrNeedsAuth", err)
	}
	if r.Authorized() {
		t.Error("provider reports authorized while the platform forbids the request")
	}
}

// A 403 is not a token problem, so it must not spend a rotation trying to
// refresh its way out of one.
func TestForbiddenDoesNotRefresh(t *testing.T) {
	fake := newFakeRestream()
	fake.webchatStatus = http.StatusForbidden
	r, _, _ := newTestProvider(t, fake)
	if err := authorize(t, r, "good-code"); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	fake.mu.Lock()
	afterExchange := fake.tokenCalls
	fake.mu.Unlock()

	_, _ = r.URL(context.Background())

	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.tokenCalls != afterExchange {
		t.Errorf("token endpoint called %d times for a 403, want 0", fake.tokenCalls-afterExchange)
	}
}

// A 5xx is transient and must stay a platform error, or a bad minute at
// Restream would send the operator to a browser for nothing.
func TestServerErrorStaysATransientFailure(t *testing.T) {
	fake := newFakeRestream()
	fake.webchatStatus = http.StatusInternalServerError
	r, _, _ := newTestProvider(t, fake)
	if err := authorize(t, r, "good-code"); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	_, err := r.URL(context.Background())
	if err == nil {
		t.Fatal("URL succeeded against a failing platform")
	}
	if errors.Is(err, ErrNeedsAuth) {
		t.Error("a server-side failure was reported as needing authorization")
	}
	if !r.Authorized() {
		t.Error("a server-side failure marked the credential unusable")
	}
}

// An operator who authorizes while a doomed attempt is still in flight must not
// have the fresh credential held off by that attempt's answer.
func TestAuthorizationOutranksAnAttemptInFlight(t *testing.T) {
	fake := newFakeRestream()
	release := make(chan struct{})
	parked := make(chan struct{})
	var once sync.Once
	fake.beforeWebchat = func() {
		once.Do(func() { close(parked) })
		<-release
	}
	// The parked request carries a token that a later authorization supersedes,
	// and the platform refuses it as out of scope rather than merely stale.
	fake.forbidStaleToken = true
	r, _, _ := newTestProvider(t, fake)
	if err := authorize(t, r, "good-code"); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, _ = r.URL(context.Background())
	}()

	<-parked // the mint is in the webchat call, holding the older token

	// The operator completes a fresh authorization while that mint is parked.
	fake.mu.Lock()
	fake.beforeWebchat = nil
	fake.mu.Unlock()
	if err := authorize(t, r, "good-code"); err != nil {
		t.Fatalf("re-authorize: %v", err)
	}

	close(release)
	wg.Wait()

	if !r.Authorized() {
		t.Error("a stale attempt held off a credential authorized after it started")
	}
	if _, err := r.URL(context.Background()); err != nil {
		t.Errorf("URL after re-authorizing: %v", err)
	}
}

// Restream's token response separates scopes with spaces; their capture-the-code
// documentation describes the same value as comma-separated. Rejecting a
// credential that carries chat.read would tell the operator to add a scope the
// application already has.
func TestScopeListAcceptsBothSeparators(t *testing.T) {
	cases := []struct {
		name  string
		scope string
	}{
		{"spaces", "profile.read channels.read chat.read stream.read"},
		{"commas", "profile.read,channels.read,chat.read,stream.read"},
		{"commas and spaces", "profile.read, channels.read, chat.read"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := newFakeRestream()
			fake.scope = tc.scope
			r, _, _ := newTestProvider(t, fake)

			if err := authorize(t, r, "good-code"); err != nil {
				t.Fatalf("Complete: %v", err)
			}
			if !r.Authorized() {
				t.Error("a credential carrying chat.read was rejected")
			}
		})
	}
}

// The platform rotates on receipt, so a shutdown that exits mid-exchange loses
// the replacement pair and costs a re-authorization.
func TestDrainWaitsForATokenRequestInFlight(t *testing.T) {
	fake := newFakeRestream()
	release := make(chan struct{})
	started := make(chan struct{})
	var once sync.Once
	fake.beforeToken = func() {
		once.Do(func() { close(started) })
		<-release
	}
	r, _, tokenFile := newTestProvider(t, fake)

	loginURL, err := r.LoginURL()
	if err != nil {
		t.Fatalf("LoginURL: %v", err)
	}
	state := stateFrom(t, loginURL)

	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = r.Complete(context.Background(), "good-code", state)
	}()
	<-started // the request is counted and the handler is parked

	// Drain must still be waiting while the exchange is parked.
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		r.Drain(ctx)
	}()

	select {
	case <-drained:
		t.Fatal("Drain returned while a token request was still in flight")
	case <-time.After(50 * time.Millisecond):
	}

	close(release)
	<-done
	<-drained

	if stored := readStoredTokens(t, tokenFile); stored.RefreshToken == "" {
		t.Error("the exchange completed but nothing was stored")
	}
}

// Drain must not hang a shutdown past its budget.
func TestDrainGivesUpWhenItsContextEnds(t *testing.T) {
	fake := newFakeRestream()
	release := make(chan struct{})
	started := make(chan struct{})
	var once sync.Once
	fake.beforeToken = func() {
		once.Do(func() { close(started) })
		<-release
	}
	r, _, _ := newTestProvider(t, fake)
	// Released before the fake's server is torn down: httptest.Close waits for
	// outstanding handlers, and this one is parked.
	defer close(release)

	loginURL, err := r.LoginURL()
	if err != nil {
		t.Fatalf("LoginURL: %v", err)
	}
	state := stateFrom(t, loginURL)
	go func() { _ = r.Complete(context.Background(), "good-code", state) }()
	<-started

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	start := time.Now()
	r.Drain(ctx)
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("Drain took %v; it should give up with its context", elapsed)
	}
}
