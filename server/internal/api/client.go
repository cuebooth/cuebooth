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
type clientConn struct {
	server *Server
	conn   *websocket.Conn
	send   chan []byte
	cancel context.CancelFunc

	mu          sync.Mutex
	topics      map[string]bool
	closeCode   websocket.StatusCode
	closeReason string
	closing     bool
}

func newClientConn(s *Server, conn *websocket.Conn, cancel context.CancelFunc) *clientConn {
	return &clientConn{
		server:      s,
		conn:        conn,
		send:        make(chan []byte, sendBuffer),
		cancel:      cancel,
		topics:      allTopicsSet(),
		closeCode:   websocket.StatusNormalClosure,
		closeReason: "",
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
		c.close(websocket.StatusPolicyViolation, "client too slow")
	}
}

// close records the close status (once) and cancels the connection's context;
// the write loop performs the actual close handshake.
//
// Lock order: close acquires only c.mu and never hub.mu. This is required
// because the hub calls enqueue (which can call close) while holding hub.mu;
// taking hub.mu here would deadlock. Unregistration from the hub happens later,
// in run's deferred hub.remove, not here.
func (c *clientConn) close(code websocket.StatusCode, reason string) {
	c.mu.Lock()
	if !c.closing {
		c.closing = true
		c.closeCode = code
		c.closeReason = reason
	}
	c.mu.Unlock()
	c.cancel()
}

func (c *clientConn) closeStatus() (websocket.StatusCode, string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closeCode, c.closeReason
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
// connection ends.
func (c *clientConn) run(ctx context.Context) {
	hello := mustMarshal(helloFrame{
		Type:          typeHello,
		Proto:         ProtoVersion,
		ServerVersion: c.server.version,
		ServerID:      c.server.serverID,
	})
	if err := c.writeDirect(ctx, hello); err != nil {
		c.conn.CloseNow()
		return
	}
	if err := c.writeDirect(ctx, c.snapshotFrame()); err != nil {
		c.conn.CloseNow()
		return
	}

	c.server.hub.add(c)
	defer c.server.hub.remove(c)

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); c.writeLoop(ctx) }()
	go func() { defer wg.Done(); c.pingLoop(ctx) }()

	c.readLoop(ctx) // blocks until the peer or context ends the connection
	c.cancel()
	wg.Wait()

	code, reason := c.closeStatus()
	_ = c.conn.Close(code, reason)
}

// writeDirect writes a frame synchronously. Only used before the write loop
// starts, so it never races another writer.
func (c *clientConn) writeDirect(ctx context.Context, frame []byte) error {
	wctx, cancel := context.WithTimeout(ctx, writeTimeout)
	defer cancel()
	return c.conn.Write(wctx, websocket.MessageText, frame)
}

func (c *clientConn) writeLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			// Flush anything already queued (e.g. a protocol `error` frame
			// enqueued just before close) before the connection is closed, so a
			// final frame isn't lost to the race between enqueue and cancel.
			c.flushRemaining()
			return
		case frame := <-c.send:
			wctx, cancel := context.WithTimeout(ctx, writeTimeout)
			err := c.conn.Write(wctx, websocket.MessageText, frame)
			cancel()
			if err != nil {
				c.cancel()
				return
			}
		}
	}
}

// flushRemaining best-effort drains and writes any frames still queued at
// shutdown. It uses a fresh context (ctx is already done here) bounded by
// writeTimeout per frame, and stops at the first write error or empty queue.
func (c *clientConn) flushRemaining() {
	for {
		select {
		case frame := <-c.send:
			wctx, cancel := context.WithTimeout(context.Background(), writeTimeout)
			err := c.conn.Write(wctx, websocket.MessageText, frame)
			cancel()
			if err != nil {
				return
			}
		default:
			return
		}
	}
}

func (c *clientConn) pingLoop(ctx context.Context) {
	t := time.NewTicker(pingInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			pctx, cancel := context.WithTimeout(ctx, pingTimeout)
			err := c.conn.Ping(pctx)
			cancel()
			if err != nil {
				c.cancel()
				return
			}
		}
	}
}

func (c *clientConn) readLoop(ctx context.Context) {
	c.conn.SetReadLimit(readLimit)
	for {
		typ, data, err := c.conn.Read(ctx)
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
		c.enqueue(mustMarshal(errorFrame{Type: typeError, Code: codeProtocol, Message: "malformed JSON frame"}))
		c.close(websocket.StatusInvalidFramePayloadData, "malformed frame")
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
