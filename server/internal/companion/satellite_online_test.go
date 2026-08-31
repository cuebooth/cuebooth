package companion

import (
	"context"
	"encoding/base64"
	"os"
	"sync"
	"testing"
	"time"
)

// This test drives the real Satellite client against a real Companion over TCP,
// covering what a fake cannot: that Companion accepts our ADD-DEVICE, that the
// bitmaps it renders are the size and encoding the Flutter client decodes, and
// that presses are accepted on the live connection. It is skipped unless
// COMPANION_SATELLITE_ADDR points at a reachable Companion, so `go test ./...`
// stays hermetic.
//
// Both a developer and CI run it through the same entry point:
//
//	scripts/companion-live-test.sh v3.4.1
//
// which starts the pinned Companion image, waits for the port, exports the
// address, and runs this test. See .github/workflows/companion-live.yml.
func liveAddr(t *testing.T) string {
	t.Helper()
	addr := os.Getenv("COMPANION_SATELLITE_ADDR")
	if addr == "" {
		t.Skip("COMPANION_SATELLITE_ADDR not set; skipping live Companion test")
	}
	return addr
}

// collector accumulates the callbacks a Satellite fires so the test can wait for
// the whole surface to arrive without racing on partially-filled state.
type collector struct {
	mu       sync.Mutex
	keys     map[int]SatelliteKey
	layouts  int
	rows     int
	cols     int
	bitmapSz int
	ready    chan struct{}
	want     int
	closed   bool
	updates  int
}

func newCollector(want int) *collector {
	return &collector{keys: map[int]SatelliteKey{}, ready: make(chan struct{}), want: want}
}

func (c *collector) onLayout(rows, cols, bitmapSize int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.layouts++
	c.rows, c.cols, c.bitmapSz = rows, cols, bitmapSize
}

func (c *collector) onKey(k SatelliteKey) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.keys[k.Key] = k
	c.updates++
	if !c.closed && len(c.keys) >= c.want {
		c.closed = true
		close(c.ready)
	}
}

func (c *collector) updateCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.updates
}

func (c *collector) snapshot() (keys map[int]SatelliteKey, rows, cols, bitmapSz, layouts int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make(map[int]SatelliteKey, len(c.keys))
	for k, v := range c.keys {
		out[k] = v
	}
	return out, c.rows, c.cols, c.bitmapSz, c.layouts
}

func TestOnlineSurfaceRegistersAndStreamsKeys(t *testing.T) {
	addr := liveAddr(t)
	version := os.Getenv("COMPANION_VERSION")

	const rows, cols = 4, 8
	total := rows * cols
	col := newCollector(total)

	sat := NewSatellite(SatelliteConfig{
		Addr:       addr,
		DeviceID:   "cuebooth-livetest",
		Rows:       rows,
		Cols:       cols,
		BitmapSize: 72,
	})
	sat.OnLayout(col.onLayout)
	sat.OnKey(col.onKey)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); sat.Run(ctx) }()

	select {
	case <-col.ready:
	case <-time.After(60 * time.Second):
		keys, _, _, _, layouts := col.snapshot()
		t.Fatalf("companion %s at %s: got %d/%d keys and %d layouts before timeout",
			version, addr, len(keys), total, layouts)
	}

	keys, gotRows, gotCols, gotBitmap, layouts := col.snapshot()
	if layouts == 0 {
		t.Error("no layout callback fired")
	}
	if gotRows != rows || gotCols != cols || gotBitmap != 72 {
		t.Errorf("layout = %dx%d bitmap=%d, want %dx%d bitmap=72", gotRows, gotCols, gotBitmap, rows, cols)
	}
	if len(keys) != total {
		t.Errorf("received %d distinct keys, want %d", len(keys), total)
	}

	// Every rendered key must decode to the raw RGB buffer the Flutter client
	// converts to RGBA; a change in size or encoding breaks rendering silently.
	for idx, k := range keys {
		if k.BitmapBase64 == "" {
			t.Errorf("key %d: no bitmap", idx)
			continue
		}
		raw, err := base64.StdEncoding.DecodeString(k.BitmapBase64)
		if err != nil {
			t.Errorf("key %d: bitmap not base64: %v", idx, err)
			continue
		}
		if len(raw) != bitmapBytes {
			t.Errorf("key %d: bitmap %d bytes, want %d (72x72 RGB)", idx, len(raw), bitmapBytes)
		}
		switch k.Type {
		case "BUTTON", "PAGEUP", "PAGEDOWN", "PAGENUM":
		default:
			t.Errorf("key %d: unexpected TYPE %q", idx, k.Type)
		}
	}

	// Press reports that the line was queued on a live session — it does not
	// prove Companion parsed it, since the acknowledgement (KEY-PRESS OK) isn't
	// surfaced to callers. An unconfigured button produces no other observable
	// effect: Companion sends no KEY-STATE echo for it.
	if err := sat.Press(1, true); err != nil {
		t.Fatalf("press down: %v", err)
	}
	time.Sleep(150 * time.Millisecond)
	if err := sat.Press(1, false); err != nil {
		t.Fatalf("press up: %v", err)
	}

	// A page-navigation key is the exception: pressing it makes Companion change
	// page and re-render every key, so the effect is visible over this
	// connection. Whether the surface has one is a per-surface Companion setting,
	// so this runs when it does and is skipped when it doesn't.
	pageKey := -1
	for idx, k := range keys {
		if k.Type == "PAGEUP" || k.Type == "PAGEDOWN" {
			pageKey = idx
			break
		}
	}
	if pageKey >= 0 {
		before := col.updateCount()
		if err := sat.Press(pageKey, true); err != nil {
			t.Fatalf("press page key down: %v", err)
		}
		time.Sleep(150 * time.Millisecond)
		if err := sat.Press(pageKey, false); err != nil {
			t.Fatalf("press page key up: %v", err)
		}
		deadline := time.After(30 * time.Second)
		for col.updateCount() < before+total {
			select {
			case <-deadline:
				t.Errorf("companion %s: pressing key %d (%s) produced %d key updates, want %d — the press was not acted on",
					version, pageKey, keys[pageKey].Type, col.updateCount()-before, total)
				deadline = nil
			case <-time.After(100 * time.Millisecond):
			}
			if deadline == nil {
				break
			}
		}
	} else {
		t.Logf("companion %s: surface has no page-navigation key; skipping the press-effect assertion", version)
	}

	// Presses still succeed well past the 2s keepalive interval, so the session
	// outlived at least one PING round trip rather than being dropped as idle.
	// This does not prove Companion's PONG was processed, only that neither side
	// tore the connection down.
	time.Sleep(3 * time.Second)
	if err := sat.Press(1, true); err != nil {
		t.Errorf("press after keepalive interval (connection dropped?): %v", err)
	}
	_ = sat.Press(1, false)

	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Error("Run did not return after context cancellation")
	}
}
