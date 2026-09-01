// Package chat integrates a live-chat platform so clients can display chat
// without holding platform credentials.
//
// A Provider owns whatever authorization its platform requires and mints a URL
// the client renders. Restream is the only implementation today; the interface
// is the seam that lets YouTube or Twitch be added without the client learning
// a second auth model (docs/design.md §3.5).
//
// Authorization sits on the server because Restream's OAuth offers no PKCE and
// its token exchange requires a client secret. Restream's own documentation
// directs integrations to keep that secret off user devices and refresh through
// a proxy the application provides, which is what this package is.
package chat

import "context"

// Status tells a client whether to render chat or prompt the operator to
// authorize it. It is carried in state as stream.chat.status (protocol.md §4).
type Status string

const (
	StatusNeedsAuth Status = "needs_auth"
	StatusReady     Status = "ready"
)

// Provider is one chat platform's server-side integration.
type Provider interface {
	// Name identifies the platform on the wire, e.g. "restream".
	Name() string

	// Authorized reports whether a credential is held that URL could mint from.
	// It performs no network IO, so the API server may call it while building
	// any state snapshot.
	Authorized() bool

	// URL returns a freshly minted, authenticated chat URL for display. Callers
	// must not cache it: the lifetime of the token it carries belongs to the
	// platform, and minting another is cheap.
	URL(ctx context.Context) (string, error)

	// LoginURL is where an operator authorizes this provider in a browser. The
	// returned URL carries a single-use state token that Complete requires back.
	// addr identifies who asked; the callback must come from the same place.
	LoginURL(addr string) (string, error)

	// Complete finishes authorization from the platform's redirect, persisting
	// whatever credential it yields. addr is where the callback arrived from.
	Complete(ctx context.Context, code, state, addr string) error
}

// StatusOf reports what a client should render for p. A nil Provider means no
// chat is configured, which is distinct from configured-but-unauthorized.
func StatusOf(p Provider) (Status, bool) {
	if p == nil {
		return "", false
	}
	if p.Authorized() {
		return StatusReady, true
	}
	return StatusNeedsAuth, true
}
