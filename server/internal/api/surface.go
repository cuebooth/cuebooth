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
	m.mu.Unlock()
	m.hub.broadcast(mustMarshal(surfaceLayoutFrame{
		Type:       typeSurfaceLayout,
		Rows:       rows,
		Cols:       cols,
		BitmapSize: bitmapSize,
	}))
}

// onKey caches a key's latest rendered state and broadcasts it to all clients.
func (m *surfaceManager) onKey(k companion.SatelliteKey) {
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
	m.hub.broadcast(mustMarshal(frame))
}

// onClear drops the cached key state (Companion asked the surface to blank,
// e.g. on a page change) so a client connecting mid-change isn't sent stale
// bitmaps. Live clients keep their last render until fresh KEY-STATEs arrive.
func (m *surfaceManager) onClear() {
	m.mu.Lock()
	m.keys = make(map[int]surfaceKeyFrame)
	m.mu.Unlock()
}

// sendInitial enqueues the current surface (layout + every cached key) to a
// single just-connected client. Called from the connection's run() so the
// client renders the surface as soon as it connects.
func (m *surfaceManager) sendInitial(c *clientConn) {
	m.mu.Lock()
	layout := surfaceLayoutFrame{
		Type:       typeSurfaceLayout,
		Rows:       m.rows,
		Cols:       m.cols,
		BitmapSize: m.bitmapSize,
	}
	frames := make([]surfaceKeyFrame, 0, len(m.keys))
	for _, f := range m.keys {
		frames = append(frames, f)
	}
	m.mu.Unlock()

	c.enqueue(mustMarshal(layout))
	for _, f := range frames {
		c.enqueue(mustMarshal(f))
	}
}

// inBounds reports whether key is a valid index for the current surface grid.
// Before any keys arrive (grid 0×0) nothing is in bounds.
func (m *surfaceManager) inBounds(key int) bool {
	m.mu.Lock()
	max := m.rows * m.cols
	m.mu.Unlock()
	return key >= 0 && key < max
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
