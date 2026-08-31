package api

import (
	"sync"

	"github.com/coder/websocket"
)

// hub tracks connected /ws clients and fans out state deltas to them, filtered
// by each client's topic subscription (protocol.md §3 subscribe/unsubscribe).
type hub struct {
	mu      sync.RWMutex
	clients map[*clientConn]struct{}
}

func newHub() *hub {
	return &hub{clients: make(map[*clientConn]struct{})}
}

func (h *hub) add(c *clientConn) {
	h.mu.Lock()
	h.clients[c] = struct{}{}
	h.mu.Unlock()
}

func (h *hub) remove(c *clientConn) {
	h.mu.Lock()
	delete(h.clients, c)
	h.mu.Unlock()
}

// broadcastDelta sends a state-delta to every client whose subscription
// intersects the patch. The patch is scoped per client, so a client subscribed
// to only [camera] never sees obs changes. A client whose send buffer is full
// is dropped (its connection is failed); it reconnects and gets a fresh
// snapshot. rev is the global revision (protocol.md §4).
func (h *hub) broadcastDelta(rev int, patch map[string]any) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for c := range h.clients {
		scoped := c.scopePatch(patch)
		if scoped == nil {
			continue
		}
		frame := mustMarshal(stateDeltaFrame{Type: typeStateDelta, Rev: rev, Patch: scoped})
		c.enqueue(frame)
	}
}

// broadcastSurfaceKey sends one key's render to every connected client,
// unconditionally (not topic-scoped) — the surface is not a state topic
// (protocol.md §10). Unlike a state delta, a client that has fallen behind is
// not dropped: the frame supersedes whatever was queued for the same key.
func (h *hub) broadcastSurfaceKey(key, seq int, frame []byte) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for c := range h.clients {
		c.enqueueSurfaceKey(key, seq, frame)
	}
}

// broadcastSurfaceLayout sends a surface re-baseline to every connected client,
// discarding the key frames each still has queued (Companion re-pushes them).
func (h *hub) broadcastSurfaceLayout(seq int, frame []byte) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for c := range h.clients {
		_ = c.enqueueSurfaceLayout(seq, frame)
	}
}

// closeAll requests teardown of every client connection with the given reason.
// Used on graceful shutdown.
func (h *hub) closeAll(reason string) {
	h.mu.RLock()
	clients := make([]*clientConn, 0, len(h.clients))
	for c := range h.clients {
		clients = append(clients, c)
	}
	h.mu.RUnlock()
	for _, c := range clients {
		// Shutdown closes while the reader is parked in Read → abrupt (no
		// protocol-mandated close code for going-away).
		c.close(websocket.StatusGoingAway, reason, false)
	}
}
