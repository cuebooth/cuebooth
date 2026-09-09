package api

import (
	"context"
	"encoding/json"
	"errors"
	"html/template"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/cuebooth/cuebooth/server/internal/chat"
	"github.com/cuebooth/cuebooth/server/internal/config"
	"github.com/cuebooth/cuebooth/server/internal/state"
)

// Chat routes. These sit outside the WebSocket protocol because both ends of
// the OAuth handshake are browser navigations, and because the minted chat URL
// carries a credential: fetching it on demand keeps it out of every state
// snapshot broadcast to every client (docs/protocol.md §11).
const (
	chatURLPath  = "/chat/url"
	chatAuthPath = "/chat/auth/start"
)

// chatCallbackPath is defined by config, which also builds the redirect URI
// Restream is given. Registering a different string here would send the
// operator's browser back to a route this server does not serve.
const chatCallbackPath = config.ChatCallbackPath

// chatMintDeadline bounds one /chat/url request. A mint can involve a refresh
// and the webchat call behind it, twice if the first token is refused, so
// without a ceiling the answer could outlast any client waiting for it. A token
// exchange already in flight is unaffected: it carries its own detached
// deadline, so a rotation is still read back after this gives up.
const chatMintDeadline = 40 * time.Second

// chatCompleteDeadline bounds one callback the same way. Without it the handler
// waits on the exchange mutex for however long the queue ahead of it takes, and
// then on a POST that deliberately ignores client cancellation.
const chatCompleteDeadline = 40 * time.Second

// chatCallbackSlots bounds callbacks completing at once. Each holds a goroutine,
// its inbound connection, and an authorization_code POST carrying the operator's
// client credentials — so an unbounded queue is an unauthenticated amplifier
// onto the operator's own Restream application.
const chatCallbackSlots = 4

// Starting an authorization allocates a pending state in a bounded map, and the
// map evicts to stay bounded. Without a rate the eviction is the weapon: enough
// starts inside the seconds an operator spends at the consent screen and their
// own state is gone by the time they come back, leaving them with a failure
// page that tells them to try again, forever. A person starts one authorization
// and occasionally retries it.
const (
	chatStartBurst = 5
	chatStartEvery = 12 * time.Second
)

// chatHostAllowed reports whether a request arrived on one of the addresses
// public_url names.
//
// Nothing else in this server inspects Host, and the WebSocket's same-origin
// policy cannot stand in for it: that compares Origin against Host, both of
// which the requesting page supplies, so a page that has made this server's
// address into its own name satisfies it and is same-origin by the browser's
// own rules. Against an API that presses buttons in a room, the network is a
// fair boundary. Against a route that hands out a credential which keeps
// working after the attacker has gone home, it is not.
func (s *Server) chatHostAllowed(r *http.Request) bool {
	hosts := s.cfg.Chat.PublicURL.Hosts()
	if len(hosts) == 0 {
		return true
	}
	for _, host := range hosts {
		if strings.EqualFold(host, r.Host) {
			return true
		}
	}
	return false
}

// chatSiteAllowed rejects a request a browser has told us came from another
// site. An absent header is allowed: a native client, curl, or an older browser
// sends none, and chatHostAllowed is what covers those.
func chatSiteAllowed(r *http.Request) bool {
	return r.Header.Get("Sec-Fetch-Site") != "cross-site"
}

// chatGuard applies both to one request, answering it when either refuses.
func (s *Server) chatGuard(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return false
	}
	if !chatSiteAllowed(r) {
		http.Error(w, "cross-site requests are not accepted here", http.StatusForbidden)
		return false
	}
	if !s.chatHostAllowed(r) {
		http.Error(w, "not the configured chat address", http.StatusForbidden)
		return false
	}
	return true
}

// serveChatURL mints a chat URL for a client about to display chat.
//
// The client calls this each time it needs one rather than caching: the token
// inside the URL is the platform's to expire, and minting another is a cheap
// server-side refresh instead of an operator re-authorizing.
func (s *Server) serveChatURL(w http.ResponseWriter, r *http.Request) {
	if !s.chatGuard(w, r) {
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), chatMintDeadline)
	defer cancel()

	chatURL, err := s.chat.URL(ctx)
	switch {
	case errors.Is(err, chat.ErrNeedsAuth):
		// Republished because a credential can be revoked between snapshots: a
		// client still showing "ready" from an older one would otherwise render
		// a platform error with no route back to authorization.
		s.publishChatStatus()
		// The start path is returned rather than a minted login URL so that a
		// client polling an unauthorized server doesn't accumulate one pending
		// authorization state per request.
		writeJSON(w, http.StatusConflict, map[string]any{
			"status":     string(chat.StatusNeedsAuth),
			"auth_start": chatAuthPath,
		})
	case err != nil:
		s.logger.Error("could not mint chat url", "provider", s.chat.Name(), "err", err)
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": "chat provider unavailable"})
	default:
		// Republished on success too: a refusal cooldown that has since lapsed
		// leaves the last published status at needs_auth, which would keep the
		// connect prompt in front of an operator whose chat is working.
		s.publishChatStatus()
		writeJSON(w, http.StatusOK, map[string]any{"url": chatURL})
	}
}

// serveChatAuthStart sends the operator's browser to the platform's authorize
// dialog.
func (s *Server) serveChatAuthStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !chatSiteAllowed(r) {
		http.Error(w, "cross-site requests are not accepted here", http.StatusForbidden)
		return
	}

	// The callback lands on the configured public URL, so a start that arrived
	// anywhere else is sent there first rather than refused: it keeps the whole
	// handshake on one address, and surfaces a public_url the operator's browser
	// cannot reach at the point they are standing in front of it. After the
	// redirect the Host matches, so this cannot loop.
	if target, ok := s.chatAuthPublicRedirect(r); ok {
		http.Redirect(w, r, target, http.StatusFound)
		return
	}

	// Rate-limited only once the address is settled, so a redirect toward
	// public_url does not spend the operator's budget.
	if !s.chatStarts.allow(time.Now()) {
		w.Header().Set("Retry-After", "12")
		http.Error(w, "too many authorization attempts; wait a moment and try again",
			http.StatusTooManyRequests)
		return
	}

	loginURL, err := s.chat.LoginURL()
	if err != nil {
		s.logger.Error("could not build chat login url", "provider", s.chat.Name(), "err", err)
		http.Error(w, "could not start chat authorization", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, loginURL, http.StatusFound)
}

// serveChatAuthCallback completes authorization from the platform's redirect.
// It renders a page rather than JSON because the operator's browser lands here.
func (s *Server) serveChatAuthCallback(w http.ResponseWriter, r *http.Request) {
	if !s.chatGuard(w, r) {
		return
	}

	// One slot per exchange in flight. Restream redirects one browser here, so
	// a queue is something else.
	select {
	case s.chatCallbacks <- struct{}{}:
		defer func() { <-s.chatCallbacks }()
	default:
		http.Error(w, "too many authorizations in flight", http.StatusTooManyRequests)
		return
	}

	code := r.URL.Query().Get("code")
	if code == "" {
		// Restream redirects back with no parameters when the operator declines.
		s.renderChatCallback(w, http.StatusOK, "Authorization cancelled",
			"CueBooth was not granted access. You can close this tab and try again from the client.")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), chatCompleteDeadline)
	defer cancel()

	if err := s.chat.Complete(ctx, code, r.URL.Query().Get("state")); err != nil {
		s.logger.Error("chat authorization failed", "provider", s.chat.Name(), "err", err)
		// A missing scope is the one failure the operator can act on directly, so
		// the page names it rather than sending them round the same loop.
		switch {
		case errors.Is(err, chat.ErrMissingScope):
			s.renderChatCallback(w, http.StatusBadRequest, "Missing permission",
				"The account signed in, but the application was not granted permission to read chat. "+
					"Add the chat.read scope to it at developers.restream.io, then try again.")
		default:
			s.renderChatCallback(w, http.StatusBadRequest, "Authorization failed",
				"CueBooth could not complete the sign-in. Close this tab and start again from the client.")
		}
		return
	}

	s.publishChatStatus()
	s.renderChatCallback(w, http.StatusOK, "Chat connected",
		"CueBooth can now show your stream chat. You can close this tab.")
}

// publishChatStatus records what the client should render for the chat panel.
// Called at startup and whenever authorization changes, so a client that was
// showing the connect prompt switches over without reconnecting.
func (s *Server) publishChatStatus() {
	status, ok := chat.StatusOf(s.chat)
	if !ok {
		return
	}
	if _, err := s.store.Update(func(st *state.State) {
		if st.Stream == nil {
			st.Stream = &state.StreamState{}
		}
		st.Stream.Chat = &state.ChatState{Provider: s.chat.Name(), Status: string(status)}
	}); err != nil {
		s.logger.Error("could not publish chat status", "err", err)
	}
}

// chatAuthPublicRedirect reports where to send a start request that arrived at
// an address other than the configured public URL, and whether to send it.
// It sends them to the first, not merely to one that is allowed: the platform
// redirects back to exactly the URI derived from that one, so a handshake begun
// anywhere else would end somewhere the browser may not be.
func (s *Server) chatAuthPublicRedirect(r *http.Request) (string, bool) {
	primary := s.cfg.Chat.PublicURL.Primary()
	if primary == "" {
		return "", false
	}
	if u, err := url.Parse(primary); err == nil && strings.EqualFold(u.Host, r.Host) {
		return "", false
	}
	return primary + chatAuthPath, true
}

func writeJSON(w http.ResponseWriter, code int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	// The body carries a URL with a credential in it, so no intermediary should
	// hold a copy.
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	// The payloads here are small maps of strings built in this file, so an
	// encode failure means the connection is already gone.
	_ = json.NewEncoder(w).Encode(payload)
}

// chatCallbackPage is what the operator's browser shows after the redirect. It
// is self-contained because the server has no static asset route.
var chatCallbackPage = template.Must(template.New("chat-callback").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Title}} — CueBooth</title>
<style>
  body { font-family: system-ui, sans-serif; background: #11151b; color: #e3e7ec;
         display: flex; min-height: 100vh; margin: 0; align-items: center; justify-content: center; }
  main { max-width: 26rem; padding: 2rem; text-align: center; }
  h1 { font-size: 1.4rem; margin: 0 0 0.75rem; }
  p { color: #9aa4b0; line-height: 1.6; margin: 0; }
</style>
</head>
<body><main><h1>{{.Title}}</h1><p>{{.Message}}</p></main></body>
</html>
`))

func (s *Server) renderChatCallback(w http.ResponseWriter, code int, title, message string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// This page's own URL carries the authorization code, so it is kept out of
	// shared caches and out of any Referer the page could ever send.
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.WriteHeader(code)
	if err := chatCallbackPage.Execute(w, struct{ Title, Message string }{title, message}); err != nil {
		s.logger.Error("could not render chat callback page", "err", err)
	}
}
