// Package api hosts the WebSocket API the Flutter client connects to.
//
// The server is authoritative for state; clients send commands and receive
// state broadcasts. The main channel (/ws) carries the command/state protocol;
// audio meters travel on a separate higher-frequency channel (/ws/meters,
// reserved here and filled in Phase 2) to avoid flooding it. See
// docs/design.md §3.6 and docs/protocol.md for the wire spec.
package api

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/coder/websocket"
	"github.com/cuebooth/cuebooth/server/internal/config"
	"github.com/cuebooth/cuebooth/server/internal/state"
)

// shutdownTimeout bounds graceful HTTP shutdown.
const shutdownTimeout = 5 * time.Second

// Server is the WebSocket API server.
type Server struct {
	cfg        *config.Config
	store      *state.Store
	hub        *hub
	dispatcher Dispatcher
	poller     *state.Poller
	mux        *http.ServeMux
	httpServer *http.Server
	logger     *slog.Logger
	version    string
	serverID   string

	pollInterval time.Duration
	sources      []state.Source
}

// Option configures a Server.
type Option func(*Server)

// WithLogger sets the logger (default slog.Default()).
func WithLogger(l *slog.Logger) Option {
	return func(s *Server) {
		if l != nil {
			s.logger = l
		}
	}
}

// WithVersion sets the server_version advertised in the hello frame.
func WithVersion(v string) Option {
	return func(s *Server) {
		if v != "" {
			s.version = v
		}
	}
}

// WithServerID sets the server_id advertised in the hello frame (default: the OS
// hostname).
func WithServerID(id string) Option {
	return func(s *Server) {
		if id != "" {
			s.serverID = id
		}
	}
}

// WithPollInterval sets the state poll cadence (default state.DefaultPollInterval).
func WithPollInterval(d time.Duration) Option {
	return func(s *Server) { s.pollInterval = d }
}

// WithSources registers background state sources (e.g. Companion variable polls)
// for the aggregator. With none registered, state is driven solely by command
// handlers (the Phase 1 default).
func WithSources(sources ...state.Source) Option {
	return func(s *Server) { s.sources = append(s.sources, sources...) }
}

// NewServer builds the API server. comp is the Companion button presser the
// command dispatcher routes to (typically *companion.Client).
func NewServer(cfg *config.Config, comp buttonPresser, opts ...Option) *Server {
	hostname, _ := os.Hostname()
	s := &Server{
		cfg:      cfg,
		store:    state.NewStore(),
		hub:      newHub(),
		logger:   slog.Default(),
		version:  "0.1.0",
		serverID: hostname,
	}
	s.dispatcher = newCompanionDispatcher(cfg, comp)
	for _, opt := range opts {
		opt(s)
	}

	s.poller = state.NewPoller(s.store, s.pollInterval, func(r state.Result) {
		s.hub.broadcastDelta(r.Rev, r.Patch)
	}, s.logger, s.sources...)

	s.mux = http.NewServeMux()
	s.mux.HandleFunc("/ws", s.serveWS)
	s.mux.HandleFunc("/ws/meters", s.serveMeters)
	return s
}

// Handler exposes the HTTP handler for tests (httptest) and embedding.
func (s *Server) Handler() http.Handler { return s.mux }

// applyState mutates the store and broadcasts the resulting delta, if any. It is
// the single funnel for command-driven state changes.
func (s *Server) applyState(mutate func(*state.State)) {
	res, err := s.store.Update(mutate)
	if err != nil {
		s.logger.Error("state update failed", "err", err)
		return
	}
	if res.Changed {
		s.hub.broadcastDelta(res.Rev, res.Patch)
	}
}

// Run starts the state poller and serves the API on cfg.Server.Listen until ctx
// is cancelled, then shuts down gracefully.
func (s *Server) Run(ctx context.Context) error {
	ln, err := net.Listen("tcp", s.cfg.Server.Listen)
	if err != nil {
		return err
	}

	go s.poller.Run(ctx)

	s.httpServer = &http.Server{
		Handler:     s.mux,
		BaseContext: func(net.Listener) context.Context { return ctx },
	}

	errc := make(chan error, 1)
	go func() { errc <- s.httpServer.Serve(ln) }()

	s.logger.Info("api server listening", "addr", ln.Addr().String())

	select {
	case <-ctx.Done():
		s.hub.closeAll(websocket.StatusGoingAway, "server shutting down")
		sctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		return s.httpServer.Shutdown(sctx)
	case err := <-errc:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

func (s *Server) serveWS(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		// v1 has no in-protocol auth and relies on network isolation (LAN +
		// Tailscale, design.md §3.7), and the client may connect from any origin
		// (native app, or web served elsewhere). Accept all origins.
		OriginPatterns: []string{"*"},
	})
	if err != nil {
		s.logger.Warn("websocket accept failed", "err", err)
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	newClientConn(s, conn, cancel).run(ctx)
}

// serveMeters is the reserved high-frequency meter endpoint (protocol.md §6).
// Phase 1 has no meter source; the endpoint accepts connections and holds them
// open so clients can establish it now. Phase 2 (CB-021) pushes `meters` frames
// here at ~10 Hz.
func (s *Server) serveMeters(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: []string{"*"}})
	if err != nil {
		s.logger.Warn("meters websocket accept failed", "err", err)
		return
	}
	defer conn.CloseNow()
	ctx := r.Context()
	for {
		if _, _, err := conn.Read(ctx); err != nil {
			return
		}
	}
}
