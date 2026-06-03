package api

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/cuebooth/cuebooth/server/internal/companion"
)

// fakeSat is a stand-in for *companion.Satellite that lets a test drive the
// surface manager's callbacks and observe presses.
type fakeSat struct {
	rows, cols, bm int
	onKey          func(companion.SatelliteKey)
	onLayout       func(rows, cols, bm int)
	onClear        func()
	presses        []struct {
		key     int
		pressed bool
	}
	pressErr error
}

func (f *fakeSat) Layout() (int, int, int)               { return f.rows, f.cols, f.bm }
func (f *fakeSat) OnKey(fn func(companion.SatelliteKey)) { f.onKey = fn }
func (f *fakeSat) OnLayout(fn func(int, int, int))       { f.onLayout = fn }
func (f *fakeSat) OnClear(fn func())                     { f.onClear = fn }
func (f *fakeSat) Run(context.Context)                   {}
func (f *fakeSat) Press(key int, pressed bool) error {
	f.presses = append(f.presses, struct {
		key     int
		pressed bool
	}{key, pressed})
	return f.pressErr
}

// newTestClient builds a clientConn that captures enqueued frames in its send
// buffer without a real WebSocket connection.
func newTestClient() *clientConn {
	return &clientConn{
		send:   make(chan []byte, 256),
		done:   make(chan struct{}),
		topics: allTopicsSet(),
	}
}

func drainFrames(c *clientConn) []map[string]any {
	var out []map[string]any
	for {
		select {
		case raw := <-c.send:
			var m map[string]any
			if err := json.Unmarshal(raw, &m); err == nil {
				out = append(out, m)
			}
		default:
			return out
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

func TestSurfaceManagerPress(t *testing.T) {
	sat := &fakeSat{rows: 4, cols: 8, bm: 72}
	m := newSurfaceManager(sat, newHub())
	if err := m.press(7, true); err != nil {
		t.Fatalf("press: %v", err)
	}
	if len(sat.presses) != 1 || sat.presses[0].key != 7 || !sat.presses[0].pressed {
		t.Errorf("press not routed: %+v", sat.presses)
	}
}

func TestSurfaceManagerPressOutOfRange(t *testing.T) {
	sat := &fakeSat{rows: 4, cols: 8, bm: 72} // 32 keys
	m := newSurfaceManager(sat, newHub())
	_ = m.press(32, true) // first invalid index
	_ = m.press(-1, true)
	if len(sat.presses) != 0 {
		t.Errorf("out-of-range presses should be dropped, got %+v", sat.presses)
	}
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

	if len(sat.presses) != 1 || sat.presses[0].key != 5 || sat.presses[0].pressed {
		t.Errorf("expected a single release of key 5, got %+v", sat.presses)
	}
	// Idempotent: nothing left to release on a second call.
	c.releaseHeldSurfaceKeys()
	if len(sat.presses) != 1 {
		t.Errorf("second release should be a no-op, got %+v", sat.presses)
	}
}
