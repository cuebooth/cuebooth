package chat

import (
	"context"
	"errors"
	"testing"
	"time"
)

// Restream selects scopes per application, so an application registered without
// chat read access yields a credential that authorizes nothing chat can use.
// Accepting it produces a loop: authorize, 401, "needs reconnecting", authorize
// again.
func TestExchangeWithoutTheChatScopeIsRejected(t *testing.T) {
	cases := []struct {
		name  string
		scope string
	}{
		{"no chat permission", "profile.default.read channels.default.read stream.default.read"},
		{"chat but not readable", "profile.default.read chat.default.write"},
		{"another resource entirely", "profile.default.read"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := newFakeRestream()
			fake.scope = tc.scope
			r, _, _ := newTestProvider(t, fake)

			err := authorize(t, r, "good-code")
			if !errors.Is(err, ErrMissingScope) {
				t.Fatalf("Complete error = %v, want ErrMissingScope", err)
			}
			if r.Authorized() {
				t.Error("provider adopted a credential that cannot mint a chat URL")
			}
		})
	}
}

// Restream grants chat read as "chat.default.read" while its dashboard labels
// the same permission "chat.read". Matching either string exactly rejects the
// other, which throws away a credential that works.
func TestExchangeWithTheChatScopeIsAccepted(t *testing.T) {
	cases := []struct {
		name  string
		scope string
	}{
		{"as a live grant reports it", "profile.default.read chat.default.read stream.default.read"},
		{"as the dashboard labels it", "profile.read channels.read chat.read stream.read"},
		{"chat permission alone", "chat.default.read"},
		{"a tier this code has not seen", "chat.premium.read"},
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
				t.Errorf("provider rejected a credential carrying chat read (%q)", tc.scope)
			}
		})
	}
}

// A platform that names no scopes says nothing about what was granted, and
// treating silence as refusal would lock out an operator over a missing field —
// the same policy the refresh lifetime already follows.
func TestExchangeWithNoScopeReportedIsAccepted(t *testing.T) {
	fake := newFakeRestream()
	fake.scope = ""
	r, _, _ := newTestProvider(t, fake)

	if err := authorize(t, r, "good-code"); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if !r.Authorized() {
		t.Error("an unreported scope set was treated as a refusal")
	}
}

// An absent access-token lifetime would leave every token instantly stale, so
// each mint would rotate the credential — the opposite of the policy applied to
// an absent refresh lifetime one field over.
func TestAbsentAccessLifetimeDoesNotRotateOnEveryMint(t *testing.T) {
	fake := newFakeRestream()
	fake.accessExpiresIn = 0
	r, _, _ := newTestProvider(t, fake)
	if err := authorize(t, r, "good-code"); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	for range 5 {
		if _, err := r.URL(context.Background()); err != nil {
			t.Fatalf("URL: %v", err)
		}
	}

	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.tokenCalls != 1 {
		t.Errorf("token endpoint called %d times over 5 mints, want 1 (the exchange only)", fake.tokenCalls)
	}
}

// The assumed lifetime must still expire, or a token really past its hour would
// never be refreshed.
func TestAssumedAccessLifetimeStillExpires(t *testing.T) {
	fake := newFakeRestream()
	fake.accessExpiresIn = 0
	r, clock, _ := newTestProvider(t, fake)
	if err := authorize(t, r, "good-code"); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if _, err := r.URL(context.Background()); err != nil {
		t.Fatalf("URL: %v", err)
	}

	clock.advance(assumedAccessLifetime + time.Minute)
	if _, err := r.URL(context.Background()); err != nil {
		t.Fatalf("URL after the assumed lifetime: %v", err)
	}

	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.tokenCalls != 2 {
		t.Errorf("token endpoint called %d times, want 2 (exchange + one refresh)", fake.tokenCalls)
	}
}
