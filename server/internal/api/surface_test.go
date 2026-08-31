package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"

	"github.com/cuebooth/cuebooth/server/internal/companion"
)

// satPress is one press the fake recorded.
type satPress struct {
	key     int
	pressed bool
}

// fakeSat is a stand-in for *companion.Satellite that lets a test drive the
// surface manager's callbacks and observe presses. Press is called from the
// connection's read goroutine in the end-to-end tests, so the log is guarded;
// read it with pressLog.
type fakeSat struct {
	rows, cols, bm int
	onKey          func(companion.SatelliteKey)
	onLayout       func(rows, cols, bm int)
	onClear        func()

	mu       sync.Mutex
	presses  []satPress
	pressErr error
}

func (f *fakeSat) Layout() (int, int, int)               { return f.rows, f.cols, f.bm }
func (f *fakeSat) OnKey(fn func(companion.SatelliteKey)) { f.onKey = fn }
func (f *fakeSat) OnLayout(fn func(int, int, int))       { f.onLayout = fn }
func (f *fakeSat) OnClear(fn func())                     { f.onClear = fn }
func (f *fakeSat) Run(context.Context)                   {}
func (f *fakeSat) Press(key int, pressed bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.presses = append(f.presses, satPress{key, pressed})
	return f.pressErr
}

// pressLog returns a copy of the presses recorded so far.
func (f *fakeSat) pressLog() []satPress {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]satPress(nil), f.presses...)
}

// failPresses makes subsequent presses fail, as a dropped satellite would.
func (f *fakeSat) failPresses(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pressErr = err
}

// newTestClient builds a clientConn that captures enqueued frames in its send
// queue without a real WebSocket connection.
func newTestClient() *clientConn {
	return &clientConn{
		send:   newSendQueue(),
		done:   make(chan struct{}),
		topics: allTopicsSet(),
	}
}

func drainFrames(c *clientConn) []map[string]any {
	var out []map[string]any
	for {
		raw, ok := c.send.pop()
		if !ok {
			return out
		}
		var m map[string]any
		if err := json.Unmarshal(raw, &m); err == nil {
			out = append(out, m)
		}
	}
}

func TestSurfaceManagerOnKeyBroadcast(t *testing.T) {
	sat := &fakeSat{rows: 4, cols: 8, bm: 72}
	hub := newHub()
	newSurfaceManager(sat, hub) // registers callbacks on sat

	c := newTestClient()
	hub.add(c)

	sat.onKey(companion.SatelliteKey{Key: 10, Type: "BUTTON", Pressed: true, Color: "#abcdef", BitmapBase64: "QQ=="})

	frames := drainFrames(c)
	if len(frames) != 1 {
		t.Fatalf("got %d frames, want 1", len(frames))
	}
	f := frames[0]
	if f["type"] != typeSurfaceKey {
		t.Errorf("type: got %v", f["type"])
	}
	// key 10 on an 8-wide grid → row 1, col 2.
	if f["key"].(float64) != 10 || f["row"].(float64) != 1 || f["col"].(float64) != 2 {
		t.Errorf("key/row/col: got %v/%v/%v", f["key"], f["row"], f["col"])
	}
	if f["pressed"] != true || f["color"] != "#abcdef" || f["bitmap"] != "QQ==" {
		t.Errorf("fields: got %+v", f)
	}
	if f["seq"].(float64) != 1 {
		t.Errorf("seq: got %v, want 1", f["seq"])
	}
}

func TestSurfaceManagerSendInitial(t *testing.T) {
	sat := &fakeSat{rows: 2, cols: 4, bm: 72}
	hub := newHub()
	m := newSurfaceManager(sat, hub)

	// Two keys arrive before a client connects; they should be cached.
	sat.onKey(companion.SatelliteKey{Key: 0, Type: "BUTTON", BitmapBase64: "AA=="})
	sat.onKey(companion.SatelliteKey{Key: 1, Type: "BUTTON", BitmapBase64: "BB=="})

	c := newTestClient()
	m.sendInitial(c)

	frames := drainFrames(c)
	// Expect a layout frame plus one per cached key.
	if len(frames) != 3 {
		t.Fatalf("got %d frames, want 3 (layout + 2 keys)", len(frames))
	}
	if frames[0]["type"] != typeSurfaceLayout {
		t.Errorf("first frame type: got %v, want surface-layout", frames[0]["type"])
	}
	if frames[0]["rows"].(float64) != 2 || frames[0]["cols"].(float64) != 4 || frames[0]["bitmap_size"].(float64) != 72 {
		t.Errorf("layout dims: got %+v", frames[0])
	}
	keyFrames := 0
	for _, f := range frames[1:] {
		if f["type"] == typeSurfaceKey {
			keyFrames++
		}
	}
	if keyFrames != 2 {
		t.Errorf("got %d key frames, want 2", keyFrames)
	}
}

func TestSurfaceManagerOnLayoutClearsCache(t *testing.T) {
	sat := &fakeSat{rows: 4, cols: 8, bm: 72}
	hub := newHub()
	m := newSurfaceManager(sat, hub)
	sat.onKey(companion.SatelliteKey{Key: 0, Type: "BUTTON", BitmapBase64: "AA=="})

	// A re-register pushes a fresh layout; the cache must clear so a new client
	// isn't sent pre-reconnect bitmaps.
	sat.onLayout(2, 2, 96)

	c := newTestClient()
	m.sendInitial(c)
	frames := drainFrames(c)
	if len(frames) != 1 || frames[0]["type"] != typeSurfaceLayout {
		t.Fatalf("after layout reset, expected only a layout frame, got %d: %+v", len(frames), frames)
	}
	if frames[0]["rows"].(float64) != 2 || frames[0]["bitmap_size"].(float64) != 96 {
		t.Errorf("layout not updated: %+v", frames[0])
	}
}

// KEYS-CLEAR blanks the surface, so connected clients have to be told: leaving
// them on the last render diverges from a client that connects afterwards, and a
// tap on a stale button still reaches Companion.
func TestSurfaceManagerClearRebaselinesClients(t *testing.T) {
	sat := &fakeSat{rows: 2, cols: 2, bm: 72}
	hub := newHub()
	m := newSurfaceManager(sat, hub)
	sat.onKey(companion.SatelliteKey{Key: 0, Type: "BUTTON", BitmapBase64: "AA=="})

	c := newTestClient()
	hub.add(c)
	drainFrames(c)

	sat.onClear()

	var layouts int
	for _, f := range drainFrames(c) {
		if f["type"] == typeSurfaceLayout {
			layouts++
			if got := f["seq"].(float64); got != 1 {
				t.Errorf("clear layout seq = %v, want 1", got)
			}
		}
	}
	if layouts != 1 {
		t.Errorf("KEYS-CLEAR broadcast %d layout frames, want 1", layouts)
	}

	// The cache is emptied too, so a later client isn't replayed blanked keys.
	c2 := newTestClient()
	m.sendInitial(c2)
	for _, f := range drainFrames(c2) {
		if f["type"] == typeSurfaceKey {
			t.Errorf("a cleared key was replayed: %+v", f)
		}
	}
}

// The clear has to carry the sequence it was taken at, or the send queue cannot
// tell which queued renders it supersedes and a client that hasn't drained yet
// still receives — and paints — the buttons Companion just blanked.
func TestSurfaceManagerClearDropsRendersStillQueued(t *testing.T) {
	sat := &fakeSat{rows: 2, cols: 2, bm: 72}
	hub := newHub()
	newSurfaceManager(sat, hub)

	c := newTestClient()
	hub.add(c)
	sat.onKey(companion.SatelliteKey{Key: 0, Type: "BUTTON", BitmapBase64: "AA=="})
	sat.onKey(companion.SatelliteKey{Key: 1, Type: "BUTTON", BitmapBase64: "BB=="})

	sat.onClear() // nothing has drained yet: both renders are still queued

	frames := drainFrames(c)
	if len(frames) != 1 || frames[0]["type"] != typeSurfaceLayout {
		t.Fatalf("expected the clear to leave only a layout queued, got %+v", frames)
	}
}

// The same for a re-registration, which is the common case: Companion restarts
// mid-service and re-pushes the whole grid, and the renders queued to a tablet
// that hasn't drained are ~670KB the layout supersedes.
func TestSurfaceManagerLayoutDropsRendersStillQueued(t *testing.T) {
	sat := &fakeSat{rows: 2, cols: 2, bm: 72}
	hub := newHub()
	newSurfaceManager(sat, hub)

	c := newTestClient()
	hub.add(c)
	sat.onKey(companion.SatelliteKey{Key: 0, Type: "BUTTON", BitmapBase64: "AA=="})
	sat.onKey(companion.SatelliteKey{Key: 1, Type: "BUTTON", BitmapBase64: "BB=="})

	sat.onLayout(2, 2, 72)

	frames := drainFrames(c)
	if len(frames) != 1 || frames[0]["type"] != typeSurfaceLayout {
		t.Fatalf("expected the re-registration to leave only a layout queued, got %+v", frames)
	}
}

// And the replay's own layout. With renders already queued to this client, a
// layout that supersedes none of them lands behind them instead of in front:
// the client paints those buttons and the layout then drops them, leaving an
// empty grid until Companion pushes again.
func TestSendInitialLayoutLeadsTheReplay(t *testing.T) {
	sat := &fakeSat{rows: 2, cols: 2, bm: 72}
	hub := newHub()
	m := newSurfaceManager(sat, hub)

	c := newTestClient()
	hub.add(c)
	sat.onKey(companion.SatelliteKey{Key: 0, Type: "BUTTON", BitmapBase64: "AA=="})
	sat.onKey(companion.SatelliteKey{Key: 1, Type: "BUTTON", BitmapBase64: "BB=="})

	m.sendInitial(c)

	frames := drainFrames(c)
	if len(frames) == 0 || frames[0]["type"] != typeSurfaceLayout {
		t.Fatalf("the replay's layout must lead, got %+v", frames)
	}
}

func TestSurfaceManagerPress(t *testing.T) {
	sat := &fakeSat{rows: 4, cols: 8, bm: 72}
	m := newSurfaceManager(sat, newHub())
	if err := m.press(7, true); err != nil {
		t.Fatalf("press: %v", err)
	}
	if len(sat.pressLog()) != 1 || sat.pressLog()[0].key != 7 || !sat.pressLog()[0].pressed {
		t.Errorf("press not routed: %+v", sat.pressLog())
	}
}

func TestSurfaceManagerPressOutOfRange(t *testing.T) {
	sat := &fakeSat{rows: 4, cols: 8, bm: 72} // 32 keys
	m := newSurfaceManager(sat, newHub())
	_ = m.press(32, true) // first invalid index
	_ = m.press(-1, true)
	if len(sat.pressLog()) != 0 {
		t.Errorf("out-of-range presses should be dropped, got %+v", sat.pressLog())
	}
}

func TestSurfaceManagerInBounds(t *testing.T) {
	sat := &fakeSat{rows: 4, cols: 8, bm: 72} // 32 keys
	m := newSurfaceManager(sat, newHub())
	for _, k := range []int{0, 31} {
		if !m.inBounds(k) {
			t.Errorf("key %d should be in bounds", k)
		}
	}
	for _, k := range []int{-1, 32, 1000} {
		if m.inBounds(k) {
			t.Errorf("key %d should be out of bounds", k)
		}
	}
}

func TestSurfaceManagerSendInitialReplaysALargeGrid(t *testing.T) {
	// A surface far larger than sendBuffer replays in full: surface frames are
	// not counted against the state backlog, so the replay can neither overflow
	// it nor drop a healthy client. 8x8 = 64 keys → 65 frames.
	sat := &fakeSat{rows: 8, cols: 8, bm: 72}
	m := newSurfaceManager(sat, newHub())
	for i := 0; i < 64; i++ {
		sat.onKey(companion.SatelliteKey{Key: i, Type: "BUTTON", BitmapBase64: "AA=="})
	}

	c := newTestClient()
	m.sendInitial(c)

	frames := drainFrames(c)
	if len(frames) != 65 {
		t.Fatalf("replay delivered %d frames, want 65 (1 layout + 64 keys)", len(frames))
	}
	layouts := 0
	for _, f := range frames {
		if f["type"] == typeSurfaceLayout {
			layouts++
		}
	}
	if layouts != 1 {
		t.Errorf("expected exactly 1 layout frame, got %d", layouts)
	}
}

// Companion is the only source of key indices, but a key outside the grid we
// registered has no cell to render into and would sit in the cache forever.
func TestSurfaceManagerIgnoresOutOfGridKey(t *testing.T) {
	sat := &fakeSat{rows: 2, cols: 2, bm: 72} // 4 keys
	hub := newHub()
	m := newSurfaceManager(sat, hub)
	c := newTestClient()
	hub.add(c)

	sat.onKey(companion.SatelliteKey{Key: 99, Type: "BUTTON", BitmapBase64: "AA=="})

	if frames := drainFrames(c); len(frames) != 0 {
		t.Errorf("out-of-grid key was broadcast: %+v", frames)
	}
	c2 := newTestClient()
	m.sendInitial(c2)
	for _, f := range drainFrames(c2) {
		if f["type"] == typeSurfaceKey {
			t.Errorf("out-of-grid key was cached and replayed: %+v", f)
		}
	}
}

// The layout carries the sequence it was snapshotted at so a client can keep a
// key update that overtook it (protocol.md §10).
func TestSurfaceLayoutCarriesSeq(t *testing.T) {
	sat := &fakeSat{rows: 2, cols: 2, bm: 72}
	hub := newHub()
	m := newSurfaceManager(sat, hub)
	sat.onKey(companion.SatelliteKey{Key: 0, Type: "BUTTON"})
	sat.onKey(companion.SatelliteKey{Key: 1, Type: "BUTTON"})

	c := newTestClient()
	m.sendInitial(c)
	frames := drainFrames(c)
	if len(frames) == 0 || frames[0]["type"] != typeSurfaceLayout {
		t.Fatalf("expected a layout frame first, got %+v", frames)
	}
	if got := frames[0]["seq"].(float64); got != 2 {
		t.Errorf("layout seq = %v, want 2 (the two keys applied so far)", got)
	}

	// A re-registration broadcast carries the sequence reached so far, so a
	// client can tell which of its keys the layout supersedes.
	hub.add(c)
	drainFrames(c)
	sat.onKey(companion.SatelliteKey{Key: 2, Type: "BUTTON"}) // seq 3
	drainFrames(c)
	sat.onLayout(2, 2, 72)

	var sawLayout bool
	for _, f := range drainFrames(c) {
		if f["type"] != typeSurfaceLayout {
			continue
		}
		sawLayout = true
		if got := f["seq"].(float64); got != 3 {
			t.Errorf("broadcast layout seq = %v, want 3", got)
		}
	}
	if !sawLayout {
		t.Error("re-registration broadcast no layout frame")
	}
}

// handleSurfacePress is the whole client-facing entry point for a tap, so it is
// driven here with real frames rather than by calling the pieces it delegates to.
func TestHandleSurfacePress(t *testing.T) {
	newConn := func(m *surfaceManager) *clientConn {
		c := newTestClient()
		c.server = &Server{surface: m, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
		return c
	}
	press := func(c *clientConn, body string) {
		c.handleSurfacePress([]byte(body))
	}

	t.Run("a frame missing key or pressed is an error, not a press", func(t *testing.T) {
		sat := &fakeSat{rows: 2, cols: 2, bm: 72}
		m := newSurfaceManager(sat, newHub())
		c := newConn(m)
		press(c, `{"type":"surface-press","pressed":true}`)
		press(c, `{"type":"surface-press","key":1}`)
		press(c, `{"type":"surface-press"`)
		if len(sat.pressLog()) != 0 {
			t.Errorf("malformed frames reached Companion: %+v", sat.pressLog())
		}
		var errors int
		for _, f := range drainFrames(c) {
			if f["type"] == typeError {
				errors++
			}
		}
		if errors != 3 {
			t.Errorf("got %d error frames, want 3", errors)
		}
	})

	t.Run("an out-of-grid key is dropped and not tracked", func(t *testing.T) {
		sat := &fakeSat{rows: 2, cols: 2, bm: 72} // keys 0..3
		m := newSurfaceManager(sat, newHub())
		c := newConn(m)
		press(c, `{"type":"surface-press","key":99,"pressed":true}`)
		if len(sat.pressLog()) != 0 {
			t.Errorf("out-of-grid press forwarded: %+v", sat.pressLog())
		}
		c.releaseHeldSurfaceKeys()
		if len(sat.pressLog()) != 0 {
			t.Errorf("out-of-grid key was tracked as held: %+v", sat.pressLog())
		}
	})

	t.Run("an in-grid press is forwarded and held until released", func(t *testing.T) {
		sat := &fakeSat{rows: 2, cols: 2, bm: 72}
		m := newSurfaceManager(sat, newHub())
		c := newConn(m)
		press(c, `{"type":"surface-press","key":2,"pressed":true}`)
		if len(sat.pressLog()) != 1 || sat.pressLog()[0].key != 2 || !sat.pressLog()[0].pressed {
			t.Fatalf("press not forwarded: %+v", sat.pressLog())
		}
		press(c, `{"type":"surface-press","key":2,"pressed":false}`)
		c.releaseHeldSurfaceKeys()
		if len(sat.pressLog()) != 2 {
			t.Errorf("a delivered release should clear the hold, got %+v", sat.pressLog())
		}
	})

	t.Run("a release that fails to reach Companion stays held", func(t *testing.T) {
		sat := &fakeSat{rows: 2, cols: 2, bm: 72}
		m := newSurfaceManager(sat, newHub())
		c := newConn(m)
		press(c, `{"type":"surface-press","key":1,"pressed":true}`)

		// The satellite drops between the press and the release.
		sat.failPresses(errors.New("satellite not connected"))
		press(c, `{"type":"surface-press","key":1,"pressed":false}`)
		sat.failPresses(nil)

		// The disconnect fallback must still release it, or Companion is left
		// holding a button whose release never arrived.
		c.releaseHeldSurfaceKeys()
		var released bool
		for _, p := range sat.pressLog() {
			if p.key == 1 && !p.pressed {
				released = true
			}
		}
		if !released {
			t.Errorf("an undelivered release left the key untracked: %+v", sat.pressLog())
		}
	})

	t.Run("no surface configured warns instead of pressing", func(t *testing.T) {
		c := newTestClient()
		c.server = &Server{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
		press(c, `{"type":"surface-press","key":0,"pressed":true}`)
		var warned bool
		for _, f := range drainFrames(c) {
			if f["type"] == typeEvent && f["severity"] == "warn" {
				warned = true
			}
		}
		if !warned {
			t.Error("pressing with no surface configured produced no warning")
		}
	})
}

func TestReleaseHeldSurfaceKeysOnDisconnect(t *testing.T) {
	sat := &fakeSat{rows: 4, cols: 8, bm: 72}
	m := newSurfaceManager(sat, newHub())
	c := newTestClient()
	c.server = &Server{surface: m}

	c.trackSurfaceHold(3, true)
	c.trackSurfaceHold(5, true)
	c.trackSurfaceHold(3, false) // 3 released normally; only 5 remains held

	c.releaseHeldSurfaceKeys()

	if len(sat.pressLog()) != 1 || sat.pressLog()[0].key != 5 || sat.pressLog()[0].pressed {
		t.Errorf("expected a single release of key 5, got %+v", sat.pressLog())
	}
	// Idempotent: nothing left to release on a second call.
	c.releaseHeldSurfaceKeys()
	if len(sat.pressLog()) != 1 {
		t.Errorf("second release should be a no-op, got %+v", sat.pressLog())
	}
}
