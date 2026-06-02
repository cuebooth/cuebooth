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
	"sync"
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

	// conns tracks live WebSocket handler goroutines (both /ws and /ws/meters)
	// so shutdown can wait for them — http.Server.Shutdown does not wait for
	// hijacked connections.
	conns sync.WaitGroup
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

	// Broadcast every state change through the hub. The observer runs under the
	// Store lock, so deltas are emitted in strict revision order regardless of
	// which goroutine (command handler or poller) triggered the change.
	s.store.SetObserver(func(r state.Result) {
		s.hub.broadcastDelta(r.Rev, r.Patch)
	})

	s.poller = state.NewPoller(s.store, s.pollInterval, s.logger, s.sources...)

	s.mux = http.NewServeMux()
	s.mux.HandleFunc("/ws", s.serveWS)
	s.mux.HandleFunc("/ws/meters", s.serveMeters)
	return s
}

// Handler exposes the HTTP handler for tests (httptest) and embedding.
func (s *Server) Handler() http.Handler { return s.mux }

// applyState mutates the store; the Store observer (set in NewServer) broadcasts
// the resulting delta in revision order. This is the single funnel for
// command-driven state changes.
func (s *Server) applyState(mutate func(*state.State)) {
	if _, err := s.store.Update(mutate); err != nil {
		s.logger.Error("state update failed", "err", err)
	}
}

// Run starts the state poller and serves the API on cfg.Server.Listen until ctx
// is cancelled, then shuts down gracefully.
func (s *Server) Run(ctx context.Context) error {
	ln, err := net.Listen("tcp", s.cfg.Server.Listen)
	if err != nil {
		return err
	}
	return s.serve(ctx, ln)
}

// serve runs the poller and HTTP server on an already-bound listener until ctx
// is cancelled, then shuts down gracefully. Split from Run so tests can supply
// their own listener and observe its address.
func (s *Server) serve(ctx context.Context, ln net.Listener) error {
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
		sctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		// Stop accepting new connections first (Shutdown closes the listener but
		// does NOT wait for hijacked WebSocket handlers), then close the live
		// clients gracefully. Meter connections unblock on their own because
		// their request context descends from this (cancelled) ctx via
		// BaseContext. Finally wait for all handler goroutines to finish so
		// close frames are actually flushed before Run returns.
		err := s.httpServer.Shutdown(sctx)
		s.hub.closeAll("server shutting down")
		s.waitConns(sctx)
		return err
	case err := <-errc:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

// waitConns blocks until all WebSocket handler goroutines have returned or ctx
// expires.
func (s *Server) waitConns(ctx context.Context) {
	done := make(chan struct{})
	go func() { s.conns.Wait(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
		s.logger.Warn("timed out waiting for websocket connections to close")
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
	s.conns.Add(1)
	defer s.conns.Done()
	newClientConn(s, conn).run(r.Context())
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
	s.conns.Add(1)
	defer s.conns.Done()
	defer conn.CloseNow()
	// r.Context() descends from the server's base context (set in Run), so it is
	// cancelled on shutdown — unblocking this read and letting the goroutine exit
	// rather than leaking until the client disconnects. Phase 2 (CB-021) pushes
	// `meters` frames here; for now the read just keeps the socket open and
	// detects client-side close.
	ctx := r.Context()
	for {
		if _, _, err := conn.Read(ctx); err != nil {
			return
		}
	}
}
