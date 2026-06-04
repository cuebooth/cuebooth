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
	// closeGrace bounds a graceful close handshake so a peer that never echoes
	// can't stall a connection's teardown.
	closeGrace = 2 * time.Second
)

// clientConn is one connected /ws client.
//
// Lifecycle and close model. The connection ends when close() is called (from
// any goroutine); it records the close code/reason once and closes the done
// channel. The actual socket close happens in run(), after the read, write, and
// ping goroutines have all exited — so it is the sole accessor and there is no
// concurrent reader/writer to fight.
//
// Two close paths, because coder/websocket's graceful Close acquires the read
// mutex to await the peer's echo:
//
//   - Graceful (delivers the WebSocket close code): used for closes detected by
//     the read goroutine itself — a fatal protocol error in handle (malformed
//     frame → code 1007, protocol.md §2). The read mutex is free at that point
//     (the reader is in handle, not Read), and the read loop then stops, so
//     run() can complete a graceful Close that transmits the code.
//   - Abrupt (CloseNow, no code): used for closes initiated while the reader is
//     parked in Read — server shutdown, slow-consumer drop, ping timeout. These
//     CloseNow immediately to unblock the blocked Read; none has a protocol-
//     mandated close code (going-away is advisory).
type clientConn struct {
	server *Server
	conn   *websocket.Conn
	send   chan []byte
	done   chan struct{}

	mu          sync.Mutex
	topics      map[string]bool
	closing     bool
	closeCode   websocket.StatusCode
	closeReason string
	graceful    bool
	// heldSurfaceKeys are surface keys this client has pressed-down but not yet
	// released, so they can be released on disconnect (see releaseHeldSurfaceKeys).
	heldSurfaceKeys map[int]bool
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
		c.close(websocket.StatusPolicyViolation, "client send buffer full", false)
	}
}

// enqueueBlocking queues a frame, blocking until there's room rather than
// dropping the client on a full buffer. It reports false if the connection is
// torn down before the frame is queued. Unlike enqueue (the slow-consumer drop
// path used by the hub broadcast, which must never block while holding hub.mu),
// this applies backpressure and so is ONLY safe to call from the connection's
// own run goroutine — never from the hub. It is used for the initial surface
// replay, an unbounded-by-config burst (rows*cols key frames) that would
// otherwise overflow the send buffer on a large grid before the write loop
// drains it. The wait is bounded: a stalled socket trips writeFrame's deadline,
// which closes the connection and fires done.
func (c *clientConn) enqueueBlocking(frame []byte) bool {
	select {
	case c.send <- frame:
		return true
	case <-c.done:
		return false
	}
}

// close requests teardown with a close code and reason. The first call wins and
// closes done; run() performs the actual socket close once the loops exit. When
// graceful is false (the reader may be parked in Read) it also calls CloseNow
// immediately to unblock that Read. Safe to call repeatedly and from any
// goroutine.
//
// graceful must only be true when the caller knows the read goroutine is not
// parked in Read (i.e. it's called from within handle), so the eventual
// graceful Close can acquire the read mutex.
//
// Lock order: close acquires only c.mu and never hub.mu. This is required
// because the hub calls enqueue (which can call close) while holding hub.mu;
// taking hub.mu here would deadlock. CloseNow touches only the socket, not the
// hub. Unregistration from the hub happens later, in run's deferred hub.remove.
func (c *clientConn) close(code websocket.StatusCode, reason string, graceful bool) {
	c.mu.Lock()
	first := !c.closing
	if first {
		c.closing = true
		c.closeCode = code
		c.closeReason = reason
		c.graceful = graceful
		close(c.done)
	}
	c.mu.Unlock()
	if first && !graceful {
		// Unblock a reader parked in Read; the graceful path leaves the socket
		// open for run() to close with a code.
		c.conn.CloseNow()
	}
}

func (c *clientConn) closeInfo() (websocket.StatusCode, string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closeCode, c.closeReason, c.graceful
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

// run drives the connection: it queues hello and the initial state snapshot
// (the only enqueues before the write loop starts, so they must fit the buffer
// — both are single frames), registers with the hub, starts the read/write/ping
// loops, then replays the current Companion surface under backpressure before
// serving the connection until it ends. parentCtx is the server/request
// context; its cancellation (server shutdown) triggers a close.
func (c *clientConn) run(parentCtx context.Context) {
	// dispatchCtx bounds command-handler work (e.g. Companion calls) to the
	// connection's and the server's lifetime.
	dispatchCtx, cancelDispatch := context.WithCancel(parentCtx)
	defer cancelDispatch()

	// Queue hello, then the initial state snapshot, before the write loop runs.
	// Registering with the hub inside SnapshotInto (under the store read lock)
	// makes the snapshot read, its enqueue, and registration atomic with respect
	// to Update's broadcast: the client receives deltas exactly from the snapshot
	// revision onward — no gap, and nothing reordered ahead of the snapshot.
	c.enqueue(mustMarshal(helloFrame{
		Type:          typeHello,
		Proto:         ProtoVersion,
		ServerVersion: c.server.version,
		ServerID:      c.server.serverID,
	}))
	c.sendSnapshot(func() { c.server.hub.add(c) })
	defer c.server.hub.remove(c)

	// Bridge server shutdown to a close, and stop in-flight command work once
	// the connection is closing.
	go func() {
		select {
		case <-parentCtx.Done():
			c.close(websocket.StatusGoingAway, "server shutting down", false)
		case <-c.done:
		}
		cancelDispatch()
	}()

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); c.writeLoop() }()
	go func() { defer wg.Done(); c.pingLoop() }()

	// The client has joined the hub (so no surface update is missed) and the
	// write loop is now draining, so replay the current surface under
	// backpressure: sendInitial enqueues a layout frame plus one per cached key
	// (rows*cols frames — unbounded by config), which would overflow the send
	// buffer and drop a healthy client on a large grid if queued before the loop
	// ran. Each surface-key carries a monotonic seq, so the client reconciles
	// these cached frames with any live updates that raced the join (protocol.md
	// §10).
	if c.server.surface != nil {
		c.server.surface.sendInitial(c)
	}
	// Release any keys still held when the client goes away, so a press whose
	// release never arrived (connection dropped mid-press) doesn't leave
	// Companion latched with the button down.
	defer c.releaseHeldSurfaceKeys()

	c.readLoop(dispatchCtx) // blocks until the connection is closed
	c.close(websocket.StatusNormalClosure, "connection closed", false)
	wg.Wait()

	// The read, write, and ping loops have all exited, so this goroutine is the
	// sole accessor of the socket. A graceful Close can now acquire the read
	// mutex; an abrupt close just tears down.
	code, reason, graceful := c.closeInfo()
	if graceful {
		c.gracefulClose(code, reason)
	} else {
		_ = c.conn.CloseNow()
	}
	c.server.logger.Debug("client connection closed", "reason", reason)
}

// gracefulClose performs the WebSocket close handshake (which transmits the
// close code), bounded by closeGrace. Only called from run() after every loop
// has exited, so the handshake's internal read can acquire the now-free read
// mutex; if the peer never echoes, CloseNow forces teardown.
func (c *clientConn) gracefulClose(code websocket.StatusCode, reason string) {
	done := make(chan struct{})
	go func() { _ = c.conn.Close(code, reason); close(done) }()
	select {
	case <-done:
	case <-time.After(closeGrace):
		_ = c.conn.CloseNow()
		<-done
	}
}

// writeLoop is the sole writer of data frames. On close it flushes anything
// still queued (so a final frame — e.g. a protocol error — reaches the client
// before the close handshake) and returns; run() closes the socket.
func (c *clientConn) writeLoop() {
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
		c.close(websocket.StatusAbnormalClosure, "write failed", false)
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
				c.close(websocket.StatusGoingAway, "keepalive failed", false)
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
		// If handle requested a graceful close (e.g. a malformed frame), stop
		// reading so the read mutex is free for run()'s close handshake — and so
		// we don't call Read again on a connection that's being torn down.
		select {
		case <-c.done:
			return
		default:
		}
	}
}

func (c *clientConn) handle(ctx context.Context, data []byte) {
	var env envelope
	if err := json.Unmarshal(data, &env); err != nil {
		// protocol.md §2: malformed frame → error frame, then close with code
		// 1007. This runs on the read goroutine (not parked in Read), so a
		// graceful close can transmit the code; the error frame is flushed by the
		// write loop before the close handshake.
		c.enqueue(mustMarshal(errorFrame{Type: typeError, Code: codeProtocol, Message: "malformed JSON frame"}))
		c.close(websocket.StatusInvalidFramePayloadData, "malformed frame", true)
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
		c.sendSnapshot(nil)
	case typeSurfacePress:
		c.handleSurfacePress(data)
	case typePing:
		var f pingFrame
		if err := json.Unmarshal(data, &f); err != nil || f.ID == "" {
			// protocol.md §3: ping carries a required id. Without it we can't
			// produce a correlatable pong, so it's a protocol error.
			c.enqueue(mustMarshal(errorFrame{Type: typeError, Code: codeProtocol, Message: "ping requires id"}))
			return
		}
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

// handleSurfacePress routes a surface key press to Companion via the satellite.
// A surface press has no id and is not ack'd/nak'd; a failure (e.g. the
// satellite isn't connected) surfaces as a warn event so the operator sees it.
func (c *clientConn) handleSurfacePress(data []byte) {
	var f surfacePressFrame
	if err := json.Unmarshal(data, &f); err != nil || f.Key == nil || f.Pressed == nil {
		c.enqueue(mustMarshal(errorFrame{Type: typeError, Code: codeProtocol, Message: "surface-press requires key and pressed"}))
		return
	}
	if c.server.surface == nil {
		c.enqueue(mustMarshal(eventFrame{Type: typeEvent, Severity: "warn", Source: "surface", Message: "no Companion surface configured"}))
		return
	}
	// Drop an out-of-range key before recording or forwarding it, so a client
	// can't grow heldSurfaceKeys unbounded by spamming large indices (the held
	// set is then bounded by the grid size).
	if !c.server.surface.inBounds(*f.Key) {
		return
	}
	// Record the hold first (on the client's intent), so a release is sent on
	// disconnect even if this press's delivery is uncertain.
	c.trackSurfaceHold(*f.Key, *f.Pressed)
	if err := c.server.surface.press(*f.Key, *f.Pressed); err != nil {
		c.server.logger.Warn("surface press failed", "key", *f.Key, "err", err)
		c.enqueue(mustMarshal(eventFrame{Type: typeEvent, Severity: "warn", Source: "surface", Message: "Companion surface unavailable"}))
	}
}

// trackSurfaceHold records (pressed) or clears (released) a surface key the
// client is holding, for release on disconnect.
func (c *clientConn) trackSurfaceHold(key int, pressed bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if pressed {
		if c.heldSurfaceKeys == nil {
			c.heldSurfaceKeys = make(map[int]bool)
		}
		c.heldSurfaceKeys[key] = true
	} else {
		delete(c.heldSurfaceKeys, key)
	}
}

// releaseHeldSurfaceKeys releases any surface keys still held at disconnect, so a
// press whose release never arrived doesn't leave Companion latched down.
func (c *clientConn) releaseHeldSurfaceKeys() {
	if c.server.surface == nil {
		return
	}
	c.mu.Lock()
	held := c.heldSurfaceKeys
	c.heldSurfaceKeys = nil
	c.mu.Unlock()
	for key := range held {
		_ = c.server.surface.press(key, false)
	}
}

func (c *clientConn) handleSubscription(data []byte, subscribe bool) {
	verb := "unsubscribe"
	if subscribe {
		verb = "subscribe"
	}
	var f subscribeFrame
	if err := json.Unmarshal(data, &f); err != nil {
		c.enqueue(mustMarshal(errorFrame{Type: typeError, Code: codeProtocol, Message: "malformed " + verb + " frame"}))
		return
	}
	for _, t := range f.Topics {
		if !validTopic(t) {
			c.enqueue(mustMarshal(errorFrame{Type: typeError, Code: codeUnknownTopic, Message: "unknown topic: " + t}))
			return // reject the whole frame; subscription unchanged
		}
	}

	// Re-baseline atomically, the same way the connect path does: leave the hub
	// so no delta is broadcast to this client mid-change, apply the subscription
	// mutation, then snapshot and re-add under the store lock. That way deltas
	// resume exactly from the snapshot's revision and none is queued ahead of it
	// — the snapshot is the client's new baseline (protocol.md §4). hub.remove
	// and hub.add are idempotent, so run's deferred hub.remove still applies.
	c.server.hub.remove(c)
	c.mu.Lock()
	for _, t := range f.Topics {
		if subscribe {
			c.topics[t] = true
		} else {
			delete(c.topics, t)
		}
	}
	c.mu.Unlock()
	c.sendSnapshot(func() { c.server.hub.add(c) })
}

// sendSnapshot enqueues a `state` frame scoped to the client's current
// subscription. The snapshot read and its enqueue happen together under the
// store lock (SnapshotInto), so a concurrent Update can't slip a higher-rev
// delta ahead of the snapshot. If andUnderLock is non-nil it runs under the
// same lock right after the enqueue — used at connect time to register with the
// hub atomically with the initial snapshot.
func (c *clientConn) sendSnapshot(andUnderLock func()) {
	topics := c.topicsSnapshot()
	c.server.store.SnapshotInto(topics, func(rev int, data map[string]any) {
		c.enqueue(mustMarshal(stateFrame{Type: typeState, Rev: rev, Data: data}))
		if andUnderLock != nil {
			andUnderLock()
		}
	})
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
