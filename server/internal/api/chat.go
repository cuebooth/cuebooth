package api

import (
	"encoding/json"
	"errors"
	"html/template"
	"net"
	"net/http"

	"github.com/cuebooth/cuebooth/server/internal/chat"
	"github.com/cuebooth/cuebooth/server/internal/state"
)

// Chat routes. These sit outside the WebSocket protocol because both ends of
// the OAuth handshake are browser navigations, and because the minted chat URL
// carries a credential: fetching it on demand keeps it out of every state
// snapshot broadcast to every client (docs/protocol.md §11).
const (
	chatURLPath      = "/chat/url"
	chatAuthPath     = "/chat/auth/start"
	chatCallbackPath = "/chat/auth/callback"
)

// serveChatURL mints a chat URL for a client about to display chat.
//
// The client calls this each time it needs one rather than caching: the token
// inside the URL is the platform's to expire, and minting another is a cheap
// server-side refresh instead of an operator re-authorizing.
func (s *Server) serveChatURL(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	url, err := s.chat.URL(r.Context())
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
		writeJSON(w, http.StatusOK, map[string]any{"url": url})
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

	loginURL, err := s.chat.LoginURL(requesterAddr(r))
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
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	code := r.URL.Query().Get("code")
	if code == "" {
		// Restream redirects back with no parameters when the operator declines.
		s.renderChatCallback(w, http.StatusOK, "Authorization cancelled",
			"CueBooth was not granted access. You can close this tab and try again from the client.")
		return
	}

	if err := s.chat.Complete(r.Context(), code, r.URL.Query().Get("state"), requesterAddr(r)); err != nil {
		s.logger.Error("chat authorization failed", "provider", s.chat.Name(), "err", err)
		s.renderChatCallback(w, http.StatusBadRequest, "Authorization failed",
			"CueBooth could not complete the sign-in. Close this tab and start again from the client.")
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

// requesterAddr is the host an HTTP request came from, without its port. The
// port changes between the two legs of the OAuth handshake even from the same
// browser, so only the host can bind them.
func requesterAddr(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
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
	w.WriteHeader(code)
	if err := chatCallbackPage.Execute(w, struct{ Title, Message string }{title, message}); err != nil {
		s.logger.Error("could not render chat callback page", "err", err)
	}
}
