package api

import (
	"context"
	"errors"
	"net"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// TestGracefulShutdownClosesConnections verifies that cancelling the server
// context closes both a /ws and a /ws/meters connection and that serve returns
// (no leaked handler goroutines) — exercising closeAll, meter-context
// propagation, and waitConns together.
func TestGracefulShutdownClosesConnections(t *testing.T) {
	srv := NewServer(testConfig(), &fakePresser{}, WithServerID("shutdown-test"))

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()

	ctx, cancel := context.WithCancel(context.Background())
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.serve(ctx, ln) }()

	dialCtx, dialCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer dialCancel()
	base := "ws://" + addr
	wsConn, _, err := websocket.Dial(dialCtx, base+"/ws", nil)
	if err != nil {
		t.Fatalf("dial /ws: %v", err)
	}
	metersConn, _, err := websocket.Dial(dialCtx, base+"/ws/meters", nil)
	if err != nil {
		t.Fatalf("dial /ws/meters: %v", err)
	}

	// Drain the /ws hello+state so the connection is fully established.
	_, _, _ = wsConn.Read(dialCtx)

	// Trigger shutdown.
	cancel()

	// serve must return promptly.
	select {
	case err := <-serveErr:
		if err != nil {
			t.Errorf("serve returned error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("serve did not return after context cancel")
	}

	// Both connections must observe the close (server closed them). Drain any
	// still-buffered frames (e.g. the /ws initial state snapshot) until a read
	// returns a non-timeout error — that's the close.
	expectClosed(t, wsConn, "/ws")
	expectClosed(t, metersConn, "/ws/meters")
}

// expectClosed reads frames until the connection is closed by the peer, failing
// if it instead stays open until the deadline.
func expectClosed(t *testing.T, conn *websocket.Conn, name string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	for {
		if _, _, err := conn.Read(ctx); err != nil {
			if ctx.Err() != nil {
				t.Errorf("%s connection was not closed on shutdown (timed out)", name)
			}
			return
		}
		// A buffered frame arrived; keep draining toward the close.
	}
}

// TestMetersEndpointAccepts confirms the reserved meter endpoint accepts a
// connection and holds it open (no immediate close).
func TestMetersEndpointAccepts(t *testing.T) {
	srv := NewServer(testConfig(), &fakePresser{})
	hs := httptest.NewServer(srv.Handler())
	t.Cleanup(hs.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(hs.URL, "http")+"/ws/meters", nil)
	if err != nil {
		t.Fatalf("dial /ws/meters: %v", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "")

	// No frame should arrive (Phase 1 has no meter source) and the server must
	// hold the socket open — so the read should hit our deadline, not return
	// early with a frame or a server-side close.
	rctx, rcancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer rcancel()
	_, _, err = conn.Read(rctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("expected the meter read to time out (socket held open), got %v", err)
	}
}
