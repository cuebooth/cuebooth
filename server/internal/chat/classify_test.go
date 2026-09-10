package chat

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"
)

// Only the grant being rejected justifies destroying the credential. A rotated
// client secret also answers 400, and erasing a year-long refresh token over it
// would turn a config fix into a walk to a browser.
func TestOtherBadRequestsKeepTheCredential(t *testing.T) {
	fake := newFakeRestream()
	fake.tokenErrorName = "invalid_client"
	fake.tokenErrorMessage = "Invalid client: client is invalid"
	r, clock, tokenFile := newTestProvider(t, fake)
	if err := authorize(t, r, "good-code"); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	fake.mu.Lock()
	fake.revoked = true
	fake.mu.Unlock()
	clock.advance(2 * time.Hour)

	_, err := r.URL(context.Background())
	if err == nil {
		t.Fatal("URL succeeded against a rejecting platform")
	}
	if errors.Is(err, ErrNeedsAuth) {
		t.Error("a non-grant 400 was reported as needing authorization")
	}
	if !r.Authorized() {
		t.Error("a non-grant 400 discarded the credential")
	}
	if stored := readStoredTokens(t, tokenFile); stored.RefreshToken == "" {
		t.Error("a non-grant 400 cleared the stored credential")
	}
}

// The grant is only rejected when the platform says so with the status OAuth
// requires for it. A gateway echoing the name on a 502 must not cost a
// year-long credential.
func TestInvalidGrantOnAnotherStatusKeepsTheCredential(t *testing.T) {
	fake := newFakeRestream()
	fake.tokenErrorStatus = 502
	r, clock, tokenFile := newTestProvider(t, fake)
	if err := authorize(t, r, "good-code"); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	fake.mu.Lock()
	fake.revoked = true
	fake.mu.Unlock()
	clock.advance(2 * time.Hour)

	_, err := r.URL(context.Background())
	if err == nil {
		t.Fatal("URL succeeded against a rejecting platform")
	}
	if errors.Is(err, ErrNeedsAuth) {
		t.Error("an invalid_grant on a 502 was reported as needing authorization")
	}
	if !r.Authorized() {
		t.Error("an invalid_grant on a 502 discarded the credential")
	}
	if stored := readStoredTokens(t, tokenFile); stored.RefreshToken == "" {
		t.Error("an invalid_grant on a 502 cleared the stored credential")
	}
}

// Restream rotates on receipt, so abandoning a refresh does not abandon the
// rotation. If the caller's cancellation reached the token request, a client
// that dropped mid-refresh would leave the stored pair dead and cost the
// operator a browser re-authorization.
func TestRefreshSurvivesCallerCancellation(t *testing.T) {
	fake := newFakeRestream()
	r, clock, tokenFile := newTestProvider(t, fake)
	if err := authorize(t, r, "good-code"); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	clock.advance(2 * time.Hour)

	// A context already cancelled stands in for a client that went away the
	// instant the refresh began.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	// The mint itself fails — the webchat call uses the caller's context — but
	// the rotation behind it must have completed and been stored.
	_, _ = r.URL(ctx)

	stored := readStoredTokens(t, tokenFile)
	if stored.RefreshToken != "refresh-2" {
		t.Fatalf("stored refresh token = %q, want the rotated refresh-2", stored.RefreshToken)
	}
	if _, err := r.URL(context.Background()); err != nil {
		t.Errorf("URL on a healthy connection after a cancelled refresh: %v", err)
	}
}

// A token minted moments ago being refused means no unattended retry will help
// — most likely the application lacks the chat.read scope. Reporting it as a
// platform outage would leave the operator on a Try again that cannot work, and
// would rotate the credential on every attempt.
func TestPersistentlyRefusedTokenReportsNeedsAuth(t *testing.T) {
	fake := newFakeRestream()
	fake.webchatStatus = http.StatusUnauthorized
	r, _, _ := newTestProvider(t, fake)
	if err := authorize(t, r, "good-code"); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	if _, err := r.URL(context.Background()); !errors.Is(err, ErrNeedsAuth) {
		t.Fatalf("URL error = %v, want ErrNeedsAuth", err)
	}

	// Further attempts must spend nothing: the refusal cooldown answers them
	// without going near the token endpoint.
	fake.mu.Lock()
	after := fake.tokenCalls
	fake.mu.Unlock()

	for range 3 {
		_, _ = r.URL(context.Background())
	}

	fake.mu.Lock()
	defer fake.mu.Unlock()
	if grew := fake.tokenCalls - after; grew != 0 {
		t.Errorf("token endpoint called %d more times over 3 attempts, want 0", grew)
	}
}
