package api

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/cuebooth/cuebooth/server/internal/companion"
)

// The surface is only useful if it is actually wired into the connection: the
// replay has to run for a client that dials /ws, and a surface-press frame has
// to reach the dispatcher. Both are exercised here over a real socket, because a
// test that calls sendInitial or handleSurfacePress directly still passes when
// the wiring in run() or handle() is gone — and the failure that would let
// through is total and silent: a grid that never renders, or buttons that do
// nothing.
func dialSurfaceServer(t *testing.T, sat satelliteSurface) (*websocket.Conn, context.Context) {
	t.Helper()
	srv := NewServer(testConfig(), &fakePresser{},
		WithServerID("test-server"), WithVersion("9.9.9"), WithSatellite(sat))
	hs := httptest.NewServer(srv.Handler())
	t.Cleanup(hs.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)

	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(hs.URL, "http")+"/ws", nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	conn.SetReadLimit(4 << 20) // bitmaps are large
	t.Cleanup(func() { conn.Close(websocket.StatusNormalClosure, "") })
	return conn, ctx
}

func TestSurfaceReachesAClientOverWebSocket(t *testing.T) {
	sat := &fakeSat{rows: 2, cols: 2, bm: 72}
	conn, ctx := dialSurfaceServer(t, sat)

	// Companion has already rendered the surface before this client connects,
	// which is the ordinary case: the operator opens the app mid-service.
	sat.onKey(companion.SatelliteKey{Key: 0, Type: "BUTTON", Color: "#112233", BitmapBase64: "QUJD"})
	sat.onKey(companion.SatelliteKey{Key: 3, Type: "BUTTON", Color: "#445566", BitmapBase64: "REVG"})

	var layout map[string]any
	keys := map[int]map[string]any{}
	deadline := time.Now().Add(5 * time.Second)
	for (layout == nil || len(keys) < 2) && time.Now().Before(deadline) {
		f := readFrame(t, ctx, conn)
		switch f["type"] {
		case typeSurfaceLayout:
			layout = f
		case typeSurfaceKey:
			keys[int(f["key"].(float64))] = f
		}
	}

	if layout == nil {
		t.Fatal("no surface-layout reached the client; the grid would never render")
	}
	if layout["rows"].(float64) != 2 || layout["cols"].(float64) != 2 || layout["bitmap_size"].(float64) != 72 {
		t.Errorf("layout dims: %+v", layout)
	}
	if len(keys) != 2 {
		t.Fatalf("got %d cached keys, want 2 — the replay did not reach the client", len(keys))
	}
	if got := keys[0]["bitmap"]; got != "QUJD" {
		t.Errorf("key 0 bitmap: got %v", got)
	}
	if got := keys[3]["color"]; got != "#445566" {
		t.Errorf("key 3 color: got %v", got)
	}

	// A live update after connect reaches the same client.
	sat.onKey(companion.SatelliteKey{Key: 1, Type: "BUTTON", Pressed: true, BitmapBase64: "R0hJ"})
	var live map[string]any
	for time.Now().Before(deadline) {
		f := readFrame(t, ctx, conn)
		if f["type"] == typeSurfaceKey && int(f["key"].(float64)) == 1 {
			live = f
			break
		}
	}
	if live == nil {
		t.Fatal("a live key update never reached the client")
	}
	if live["pressed"] != true {
		t.Errorf("live key: %+v", live)
	}
}

// A tablet that loses Wi-Fi mid-hold cannot send its own release: the session
// stops being ready before the grid tears down, so the client suppresses it.
// The server releasing held keys on disconnect is the only thing that stops
// Companion sitting latched on a hold-to-act button until the operator comes
// back and taps it again.
func TestHeldKeyIsReleasedWhenTheClientVanishes(t *testing.T) {
	sat := &fakeSat{rows: 2, cols: 2, bm: 72}
	conn, ctx := dialSurfaceServer(t, sat)

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if readFrame(t, ctx, conn)["type"] == typeSurfaceLayout {
			break
		}
	}

	writeFrame(t, ctx, conn, map[string]any{"type": typeSurfacePress, "key": 1, "pressed": true})
	for time.Now().Before(deadline) {
		if len(sat.pressLog()) > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if log := sat.pressLog(); len(log) != 1 || !log[0].pressed {
		t.Fatalf("the hold never reached Companion: %+v", log)
	}

	// The tablet goes away without releasing.
	conn.Close(websocket.StatusAbnormalClosure, "wifi dropped")

	var released bool
	for time.Now().Before(deadline) && !released {
		for _, p := range sat.pressLog() {
			if p.key == 1 && !p.pressed {
				released = true
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !released {
		t.Fatalf("a key held at disconnect was never released; Companion stays latched: %+v", sat.pressLog())
	}
}

func TestSurfacePressFromAClientReachesCompanion(t *testing.T) {
	sat := &fakeSat{rows: 2, cols: 2, bm: 72}
	conn, ctx := dialSurfaceServer(t, sat)

	// Wait for the surface so the connection is fully established before tapping.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if readFrame(t, ctx, conn)["type"] == typeSurfaceLayout {
			break
		}
	}

	writeFrame(t, ctx, conn, map[string]any{"type": typeSurfacePress, "key": 2, "pressed": true})
	writeFrame(t, ctx, conn, map[string]any{"type": typeSurfacePress, "key": 2, "pressed": false})

	// The dispatch is asynchronous; wait for the pair rather than racing it.
	var down, up bool
	for time.Now().Before(deadline) {
		for _, p := range sat.pressLog() {
			if p.key == 2 && p.pressed {
				down = true
			}
			if p.key == 2 && !p.pressed {
				up = true
			}
		}
		if down && up {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !down || !up {
		t.Fatalf("a tap sent over the socket did not reach Companion (down=%v up=%v, presses=%+v)",
			down, up, sat.pressLog())
	}
}
