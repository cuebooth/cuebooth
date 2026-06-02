package api

import (
	"context"
	"encoding/json"
	"slices"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/cuebooth/cuebooth/server/internal/state"
)

const (
	// sendBuffer bounds per-client outbound queue depth. A client that can't
	// keep up past this is dropped and must reconnect (and re-snapshot).
	sendBuffer = 64
	// writeTimeout bounds a single frame write.
	writeTimeout = 5 * time.Second
	// pingInterval / pingTimeout drive server→client keepalive (protocol.md §1).
	pingInterval = 20 * time.Second
	pingTimeout  = 10 * time.Second
	// readLimit caps an inbound frame; client frames are tiny.
	readLimit = 1 << 20
)

// clientConn is one connected /ws client.
//
// Lifecycle: the write goroutine is the sole writer and tears the connection
// down. A close is requested by calling close(), which records a reason (for
// logging) and closes the done channel (once); the write loop then flushes any
// queued frames and calls conn.CloseNow.
//
// Teardown uses CloseNow (abrupt) rather than the graceful close handshake:
// coder/websocket's Close acquires the read mutex to await the peer's echo,
// which the read goroutine holds while blocked in Read, so a graceful Close
// would stall for the library's 5s read-lock timeout. The semantic reason for a
// close is instead conveyed at the application layer (an `error` or `nak`
// frame) before teardown. NOTE: this means the WebSocket close code in
// protocol.md §2 (1007 for malformed) is not delivered as a close code — see
// the package TODO. The `error` frame (code "protocol") still is.
type clientConn struct {
	server *Server
	conn   *websocket.Conn
	send   chan []byte
	done   chan struct{}

	mu          sync.Mutex
	topics      map[string]bool
	closing     bool
	closeReason string
}

func newClientConn(s *Server, conn *websocket.Conn) *clientConn {
	return &clientConn{
		server: s,
		conn:   conn,
		send:   make(chan []byte, sendBuffer),
		done:   make(chan struct{}),
		topics: allTopicsSet(),
	}
}

func allTopicsSet() map[string]bool {
	m := make(map[string]bool, len(state.Topics))
	for _, t := range state.Topics {
		m[t] = true
	}
	return m
}

func validTopic(t string) bool { return slices.Contains(state.Topics, t) }

// enqueue queues a pre-marshalled frame for the write loop. If the buffer is
// full the client is too slow: fail it so it reconnects and re-syncs.
//
// enqueue is called by the hub while it holds hub.mu (broadcastDelta), so the
// close it may trigger MUST NOT acquire hub.mu — see the lock-order note on close.
func (c *clientConn) enqueue(frame []byte) {
	select {
	case c.send <- frame:
	default:
		c.close("client send buffer full")
	}
}

// close requests teardown with a reason (for logging). The first call wins and
// closes done; the write loop performs the actual CloseNow. Safe to call
// repeatedly and from any goroutine.
//
// Lock order: close acquires only c.mu and never hub.mu. This is required
// because the hub calls enqueue (which can call close) while holding hub.mu;
// taking hub.mu here would deadlock. Unregistration from the hub happens later,
// in run's deferred hub.remove, not here.
func (c *clientConn) close(reason string) {
	c.mu.Lock()
	if !c.closing {
		c.closing = true
		c.closeReason = reason
		close(c.done)
	}
	c.mu.Unlock()
}

func (c *clientConn) reason() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closeReason
}

// topicsSnapshot returns a copy of the current subscription set.
func (c *clientConn) topicsSnapshot() map[string]bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make(map[string]bool, len(c.topics))
	for k, v := range c.topics {
		out[k] = v
	}
	return out
}

// scopePatch returns the part of a delta patch this client is subscribed to, or
// nil if none. Called by the hub.
func (c *clientConn) scopePatch(patch map[string]any) map[string]any {
	c.mu.Lock()
	defer c.mu.Unlock()
	return state.FilterTopics(patch, c.topics)
}

// run drives the connection: it sends hello and the initial snapshot directly
// (the only writes before the write loop starts, so there's a single writer),
// registers with the hub, then runs the read/write/ping loops until the
// connection ends. parentCtx is the server/request context; its cancellation
// (server shutdown) triggers a close.
func (c *clientConn) run(parentCtx context.Context) {
	// dispatchCtx bounds command-handler work (e.g. Companion calls) to the
	// connection's and the server's lifetime.
	dispatchCtx, cancelDispatch := context.WithCancel(parentCtx)
	defer cancelDispatch()

	hello := mustMarshal(helloFrame{
		Type:          typeHello,
		Proto:         ProtoVersion,
		ServerVersion: c.server.version,
		ServerID:      c.server.serverID,
	})
	if err := c.writeDirect(hello); err != nil {
		c.conn.CloseNow()
		return
	}
	if err := c.writeDirect(c.snapshotFrame()); err != nil {
		c.conn.CloseNow()
		return
	}

	c.server.hub.add(c)
	defer c.server.hub.remove(c)

	// Bridge server shutdown to a close, and stop in-flight command work once
	// the connection is closing.
	go func() {
		select {
		case <-parentCtx.Done():
			c.close("server shutting down")
		case <-c.done:
		}
		cancelDispatch()
	}()

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); c.writeLoop() }()
	go func() { defer wg.Done(); c.pingLoop() }()

	c.readLoop(dispatchCtx) // blocks until the connection is closed
	c.close("connection closed")
	wg.Wait()
	c.server.logger.Debug("client connection closed", "reason", c.reason())
}

// writeDirect writes a frame synchronously. Only used before the write loop
// starts, so it never races another writer.
func (c *clientConn) writeDirect(frame []byte) error {
	wctx, cancel := context.WithTimeout(context.Background(), writeTimeout)
	defer cancel()
	return c.conn.Write(wctx, websocket.MessageText, frame)
}

// writeLoop is the sole writer of data frames and tears the connection down. On
// close it flushes anything still queued (so a final frame — e.g. a protocol
// error — reaches the client) and then closes the socket.
func (c *clientConn) writeLoop() {
	defer c.conn.CloseNow()
	for {
		select {
		case frame := <-c.send:
			if !c.writeFrame(frame) {
				return
			}
		case <-c.done:
			c.flushRemaining()
			return
		}
	}
}

// writeFrame writes one frame, returning false (and requesting close) on error.
// It uses a standalone deadline rather than the connection context so a
// shutdown never aborts a frame mid-write.
func (c *clientConn) writeFrame(frame []byte) bool {
	wctx, cancel := context.WithTimeout(context.Background(), writeTimeout)
	defer cancel()
	if err := c.conn.Write(wctx, websocket.MessageText, frame); err != nil {
		c.close("write failed")
		return false
	}
	return true
}

// flushRemaining best-effort drains and writes any frames still queued at
// close, stopping at the first write error or an empty queue.
func (c *clientConn) flushRemaining() {
	for {
		select {
		case frame := <-c.send:
			if !c.writeFrame(frame) {
				return
			}
		default:
			return
		}
	}
}

func (c *clientConn) pingLoop() {
	t := time.NewTicker(pingInterval)
	defer t.Stop()
	for {
		select {
		case <-c.done:
			return
		case <-t.C:
			pctx, cancel := context.WithTimeout(context.Background(), pingTimeout)
			err := c.conn.Ping(pctx)
			cancel()
			if err != nil {
				c.close("keepalive failed")
				return
			}
		}
	}
}

func (c *clientConn) readLoop(ctx context.Context) {
	c.conn.SetReadLimit(readLimit)
	for {
		// Read with a background context: a cancelled Read context fails the
		// connection in coder/websocket, which would race the write loop. The
		// write loop's CloseNow is what unblocks this Read on teardown.
		typ, data, err := c.conn.Read(context.Background())
		if err != nil {
			return
		}
		if typ != websocket.MessageText {
			continue // v1 has no binary frames
		}
		c.handle(ctx, data)
	}
}

func (c *clientConn) handle(ctx context.Context, data []byte) {
	var env envelope
	if err := json.Unmarshal(data, &env); err != nil {
		// protocol.md §2: malformed frame → error frame then close. The error
		// frame is flushed by the write loop before teardown.
		c.enqueue(mustMarshal(errorFrame{Type: typeError, Code: codeProtocol, Message: "malformed JSON frame"}))
		c.close("malformed frame")
		return
	}

	switch env.Type {
	case typeCmd:
		c.handleCmd(ctx, data)
	case typeSubscribe:
		c.handleSubscription(data, true)
	case typeUnsubscribe:
		c.handleSubscription(data, false)
	case typeGetState:
		c.enqueue(c.snapshotFrame())
	case typePing:
		var f pingFrame
		_ = json.Unmarshal(data, &f)
		c.enqueue(mustMarshal(pongFrame{Type: typePong, ID: f.ID}))
	default:
		// Unknown type MUST be ignored (protocol.md §2/§7).
	}
}

func (c *clientConn) handleCmd(ctx context.Context, data []byte) {
	var f cmdFrame
	if err := json.Unmarshal(data, &f); err != nil || f.ID == "" || f.Target == "" {
		// Without an id we can't correlate a nak, so this is a protocol error.
		c.enqueue(mustMarshal(errorFrame{Type: typeError, Code: codeProtocol, Message: "cmd requires id and target"}))
		return
	}

	mutate, cerr := c.server.dispatcher.Dispatch(ctx, f)
	if cerr != nil {
		c.enqueue(mustMarshal(nakFrame{Type: typeNak, ID: f.ID, Error: wireError{Code: cerr.code, Message: cerr.message}}))
		return
	}
	// Ack first, then broadcast any resulting delta (protocol.md §4).
	c.enqueue(mustMarshal(ackFrame{Type: typeAck, ID: f.ID}))
	if mutate != nil {
		c.server.applyState(mutate)
	}
}

func (c *clientConn) handleSubscription(data []byte, subscribe bool) {
	var f subscribeFrame
	if err := json.Unmarshal(data, &f); err != nil {
		c.enqueue(mustMarshal(errorFrame{Type: typeError, Code: codeProtocol, Message: "malformed subscribe frame"}))
		return
	}
	for _, t := range f.Topics {
		if !validTopic(t) {
			c.enqueue(mustMarshal(errorFrame{Type: typeError, Code: codeUnknownTopic, Message: "unknown topic: " + t}))
			return // reject the whole frame; subscription unchanged
		}
	}

	c.mu.Lock()
	for _, t := range f.Topics {
		if subscribe {
			c.topics[t] = true
		} else {
			delete(c.topics, t)
		}
	}
	c.mu.Unlock()

	// A subscription change is followed by a fresh state snapshot (protocol.md §4).
	c.enqueue(c.snapshotFrame())
}

// snapshotFrame builds a `state` frame scoped to the client's current subscription.
func (c *clientConn) snapshotFrame() []byte {
	rev, data := c.server.store.Snapshot(c.topicsSnapshot())
	return mustMarshal(stateFrame{Type: typeState, Rev: rev, Data: data})
}

// mustMarshal marshals a frame that is statically known to be encodable. A
// failure indicates a programmer error, not a runtime condition.
func mustMarshal(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic("api: marshal frame: " + err.Error())
	}
	return b
}
