package api

import (
	"context"
	"sync"

	"github.com/cuebooth/cuebooth/server/internal/companion"
)

// satelliteSurface is the slice of *companion.Satellite the surface manager
// depends on. Tests substitute a fake.
type satelliteSurface interface {
	Layout() (rows, cols, bitmapSize int)
	Press(key int, pressed bool) error
	OnKey(func(companion.SatelliteKey))
	OnLayout(func(rows, cols, bitmapSize int))
	OnClear(func())
	Run(context.Context)
}

// surfaceManager bridges the Companion Satellite surface to connected clients.
// It caches the latest rendered state of every key so a newly connected client
// can be sent the full surface immediately, and fans Companion's KEY-STATE
// updates out through the hub as surface-key frames. Presses travel back to
// Companion via the satellite. Surface frames bypass the state/delta machinery
// (see protocol.md §10): button bitmaps are large and change often, so diffing
// them through the state store would be wasteful.
//
// onKey, onLayout and onClear are raised from the satellite's reader, never
// concurrently with one another, so each may broadcast after releasing mu.
// sendInitial is the only concurrent producer; it runs on a client's own read
// goroutine, at connect and again on each subscribe or unsubscribe.
type surfaceManager struct {
	sat satelliteSurface
	hub *hub

	mu         sync.Mutex
	rows       int
	cols       int
	bitmapSize int
	seq        int                     // monotonic surface-update sequence
	keys       map[int]surfaceKeyFrame // latest frame per key index
}

func newSurfaceManager(sat satelliteSurface, hub *hub) *surfaceManager {
	rows, cols, bitmapSize := sat.Layout()
	m := &surfaceManager{
		sat:        sat,
		hub:        hub,
		rows:       rows,
		cols:       cols,
		bitmapSize: bitmapSize,
		keys:       make(map[int]surfaceKeyFrame),
	}
	sat.OnLayout(m.onLayout)
	sat.OnKey(m.onKey)
	sat.OnClear(m.onClear)
	return m
}

// Run drives the underlying satellite connection until ctx is cancelled.
func (m *surfaceManager) Run(ctx context.Context) { m.sat.Run(ctx) }

// onLayout re-baselines the surface dimensions on (re)connect and clears the
// key cache, since Companion will re-push every key's state after registration.
func (m *surfaceManager) onLayout(rows, cols, bitmapSize int) {
	m.mu.Lock()
	m.rows, m.cols, m.bitmapSize = rows, cols, bitmapSize
	m.keys = make(map[int]surfaceKeyFrame)
	seq := m.seq
	m.mu.Unlock()
	m.hub.broadcastSurfaceLayout(seq, mustMarshal(surfaceLayoutFrame{
		Type:       typeSurfaceLayout,
		Rows:       rows,
		Cols:       cols,
		Seq:        seq,
		BitmapSize: bitmapSize,
	}))
}

// onKey caches a key's latest rendered state and broadcasts it to all clients.
// A key outside the grid we registered is dropped rather than cached: it would
// occupy a slot no client can render and grow the cache without bound.
func (m *surfaceManager) onKey(k companion.SatelliteKey) {
	if !m.inBounds(k.Key) {
		return
	}
	m.mu.Lock()
	cols := m.cols
	m.seq++
	frame := surfaceKeyFrame{
		Type:    typeSurfaceKey,
		Key:     k.Key,
		Seq:     m.seq,
		KeyType: k.Type,
		Pressed: k.Pressed,
		Color:   k.Color,
		Bitmap:  k.BitmapBase64,
	}
	if cols > 0 {
		frame.Row = k.Key / cols
		frame.Col = k.Key % cols
	}
	m.keys[k.Key] = frame
	m.mu.Unlock()
	m.hub.broadcastSurfaceKey(frame.Key, frame.Seq, mustMarshal(frame))
}

// onClear drops the cached key state (Companion asked the surface to blank) so a
// client connecting mid-change isn't sent stale bitmaps. Page changes don't reach
// here on the versions we've measured — Companion re-pushes every key instead —
// so this is the defensive path for versions that do send KEYS-CLEAR.
func (m *surfaceManager) onClear() {
	m.mu.Lock()
	m.keys = make(map[int]surfaceKeyFrame)
	layout := surfaceLayoutFrame{
		Type:       typeSurfaceLayout,
		Rows:       m.rows,
		Cols:       m.cols,
		Seq:        m.seq,
		BitmapSize: m.bitmapSize,
	}
	m.mu.Unlock()
	// Tell the clients too. Clearing only the cache would leave whoever is
	// already connected showing buttons Companion has blanked, while a client
	// connecting a moment later saw an empty grid — and a tap on one of those
	// stale buttons still reaches Companion, which now has something else there.
	m.hub.broadcastSurfaceLayout(layout.Seq, mustMarshal(layout))
}

// sendInitial replays the current surface (layout + every cached key) to a
// single just-connected client. The replay is one frame per key, which is what
// the connection's send queue is bounded at anyway, so it cannot overflow — and
// a live update landing mid-replay supersedes the cached frame for that key
// rather than queueing behind it.
//
// The lock covers the enqueues, not just the snapshot: a re-baseline landing
// between them would queue this snapshot's remaining keys *behind* the layout
// that supersedes them, and the client — which drops its keys on a layout and
// then applies whatever follows — would repaint a surface Companion has
// blanked. Holding it is affordable because no enqueue blocks; the satellite's
// callback goroutine waits only for the marshalling.
func (m *surfaceManager) sendInitial(c *clientConn) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c.enqueueSurfaceLayout(m.seq, mustMarshal(surfaceLayoutFrame{
		Type:       typeSurfaceLayout,
		Rows:       m.rows,
		Cols:       m.cols,
		Seq:        m.seq,
		BitmapSize: m.bitmapSize,
	}))
	for _, f := range m.keys {
		c.enqueueSurfaceKey(f.Key, f.Seq, mustMarshal(f))
	}
}

// inBounds reports whether key is a valid index for the current surface grid.
func (m *surfaceManager) inBounds(key int) bool {
	return key >= 0 && key < m.keyCount()
}

// keyCount is the number of keys on the current surface.
func (m *surfaceManager) keyCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.rows * m.cols
}

// press routes a client's key press to Companion. The boundary is also guarded
// here (defense in depth) since press is reachable from the held-key release
// path; callers should prefer gating on inBounds first.
func (m *surfaceManager) press(key int, pressed bool) error {
	if !m.inBounds(key) {
		return nil
	}
	return m.sat.Press(key, pressed)
}
