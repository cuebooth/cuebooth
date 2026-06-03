package companion

import (
	"bufio"
	"context"
	"errors"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestTokenizeSatellite(t *testing.T) {
	got := tokenizeSatellite(`ADD-DEVICE DEVICEID=x PRODUCT_NAME="CueBooth Client" BITMAPS=72`)
	want := []string{"ADD-DEVICE", "DEVICEID=x", "PRODUCT_NAME=CueBooth Client", "BITMAPS=72"}
	if len(got) != len(want) {
		t.Fatalf("token count: got %d (%q), want %d", len(got), got, len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("token %d: got %q, want %q", i, got[i], want[i])
		}
	}
}

func TestTokenizeSatelliteEscaping(t *testing.T) {
	// A backslash escapes the next char, including a quote inside a quoted value.
	got := tokenizeSatellite(`CMD A="x \"q\" y" B=c\\d`)
	want := []string{"CMD", `A=x "q" y`, `B=c\d`}
	if len(got) != len(want) {
		t.Fatalf("token count: got %d (%q), want %d", len(got), got, len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("token %d: got %q, want %q", i, got[i], want[i])
		}
	}
}

func TestParseSatelliteLine(t *testing.T) {
	cmd, args := parseSatelliteLine(`KEY-STATE DEVICEID=cuebooth KEY=5 TYPE=BUTTON PRESSED=1 COLOR=#00ff00 BITMAP=AAEC`)
	if cmd != "KEY-STATE" {
		t.Fatalf("cmd: got %q", cmd)
	}
	for k, want := range map[string]string{
		"DEVICEID": "cuebooth", "KEY": "5", "TYPE": "BUTTON",
		"PRESSED": "1", "COLOR": "#00ff00", "BITMAP": "AAEC",
	} {
		if args[k] != want {
			t.Errorf("arg %s: got %q, want %q", k, args[k], want)
		}
	}
}

func TestParseWireBool(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want bool
	}{{"1", true}, {"true", true}, {"True", true}, {"0", false}, {"false", false}, {"", false}} {
		if got := parseWireBool(tc.in); got != tc.want {
			t.Errorf("parseWireBool(%q): got %v, want %v", tc.in, got, tc.want)
		}
	}
}

// fakeCompanion is one end of an in-memory pipe standing in for Companion's
// satellite listener.
type fakeCompanion struct {
	conn net.Conn
	r    *bufio.Reader
}

func (f *fakeCompanion) readLine(t *testing.T) string {
	t.Helper()
	_ = f.conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	line, err := f.r.ReadString('\n')
	if err != nil {
		t.Fatalf("fakeCompanion read: %v", err)
	}
	return strings.TrimRight(line, "\r\n")
}

func (f *fakeCompanion) writeLine(t *testing.T, s string) {
	t.Helper()
	if _, err := f.conn.Write([]byte(s + "\n")); err != nil {
		t.Fatalf("fakeCompanion write: %v", err)
	}
}

// newSatelliteWithPipe wires a Satellite to an in-memory fake Companion. The
// dialer hands over the pipe once; subsequent reconnects fail fast so the test
// controls a single session.
func newSatelliteWithPipe(t *testing.T, cfg SatelliteConfig) (*Satellite, *fakeCompanion) {
	t.Helper()
	srvEnd, devEnd := net.Pipe()
	fake := &fakeCompanion{conn: srvEnd, r: bufio.NewReader(srvEnd)}

	var once sync.Once
	dial := func(ctx context.Context) (net.Conn, error) {
		var c net.Conn
		err := errors.New("no more connections")
		once.Do(func() { c, err = devEnd, nil })
		return c, err
	}
	sat := NewSatellite(cfg, WithSatelliteDialer(dial))
	t.Cleanup(func() { srvEnd.Close(); devEnd.Close() })
	return sat, fake
}

func TestSatelliteRegisterAndKeyState(t *testing.T) {
	keys := make(chan SatelliteKey, 4)
	layouts := make(chan [3]int, 1)

	sat, fake := newSatelliteWithPipe(t, SatelliteConfig{
		DeviceID: "cuebooth", Rows: 4, Cols: 8, BitmapSize: 72,
	})
	sat.OnKey(func(k SatelliteKey) { keys <- k })
	sat.OnLayout(func(rows, cols, bm int) { layouts <- [3]int{rows, cols, bm} })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go sat.Run(ctx)

	// The surface registers itself first.
	got := fake.readLine(t)
	want := `ADD-DEVICE DEVICEID=cuebooth PRODUCT_NAME="CueBooth" KEYS_TOTAL=32 KEYS_PER_ROW=8 BITMAPS=72 COLORS=hex`
	if got != want {
		t.Fatalf("ADD-DEVICE:\n got %q\nwant %q", got, want)
	}

	if l := <-layouts; l != [3]int{4, 8, 72} {
		t.Errorf("layout: got %v, want [4 8 72]", l)
	}

	// Companion pushes a key.
	fake.writeLine(t, "KEY-STATE DEVICEID=cuebooth KEY=9 TYPE=BUTTON PRESSED=1 COLOR=#ff0000 BITMAP=QUJD")
	select {
	case k := <-keys:
		if k.Key != 9 || k.Type != "BUTTON" || !k.Pressed || k.Color != "#ff0000" || k.BitmapBase64 != "QUJD" {
			t.Errorf("key: got %+v", k)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for OnKey")
	}
}

func TestSatellitePingPong(t *testing.T) {
	sat, fake := newSatelliteWithPipe(t, SatelliteConfig{DeviceID: "cuebooth"})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go sat.Run(ctx)

	_ = fake.readLine(t) // ADD-DEVICE
	fake.writeLine(t, "PING token123")
	if got := fake.readLine(t); got != "PONG token123" {
		t.Errorf("PONG: got %q, want %q", got, "PONG token123")
	}
}

func TestSatellitePress(t *testing.T) {
	sat, fake := newSatelliteWithPipe(t, SatelliteConfig{DeviceID: "cuebooth"})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go sat.Run(ctx)

	_ = fake.readLine(t) // ADD-DEVICE; after this the write conn is established

	if err := sat.Press(5, true); err != nil {
		t.Fatalf("Press: %v", err)
	}
	if got := fake.readLine(t); got != "KEY-PRESS DEVICEID=cuebooth KEY=5 PRESSED=true" {
		t.Errorf("KEY-PRESS down: got %q", got)
	}
	if err := sat.Press(5, false); err != nil {
		t.Fatalf("Press up: %v", err)
	}
	if got := fake.readLine(t); got != "KEY-PRESS DEVICEID=cuebooth KEY=5 PRESSED=false" {
		t.Errorf("KEY-PRESS up: got %q", got)
	}
}

func TestSatellitePressNotConnected(t *testing.T) {
	// No Run(), so no connection is established.
	sat := NewSatellite(SatelliteConfig{DeviceID: "cuebooth"})
	if err := sat.Press(0, true); !errors.Is(err, ErrSatelliteNotConnected) {
		t.Errorf("Press without connection: got %v, want ErrSatelliteNotConnected", err)
	}
}

// TestSatelliteConcurrentPressDuringTeardown guards the send-on-closed-channel
// race: many presses fire while the session tears down (ctx cancel). enqueue's
// send and teardown's close share the lock, so this must not panic. Run under
// -race in CI to catch a regression.
func TestSatelliteConcurrentPressDuringTeardown(t *testing.T) {
	srvEnd, devEnd := net.Pipe()
	var once sync.Once
	dial := func(ctx context.Context) (net.Conn, error) {
		var c net.Conn
		err := errors.New("no more connections")
		once.Do(func() { c, err = devEnd, nil })
		return c, err
	}
	sat := NewSatellite(SatelliteConfig{DeviceID: "x"}, WithSatelliteDialer(dial))
	// Drain the Companion end so the writer never blocks on the pipe.
	go func() {
		b := make([]byte, 4096)
		for {
			if _, err := srvEnd.Read(b); err != nil {
				return
			}
		}
	}()

	ctx, cancel := context.WithCancel(context.Background())
	go sat.Run(ctx)

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				_ = sat.Press(j%32, j%2 == 0)
			}
		}()
	}
	time.Sleep(10 * time.Millisecond) // let the session connect, then tear down mid-press
	cancel()
	wg.Wait()
	srvEnd.Close()
	devEnd.Close()
	// Reaching here without a panic is the assertion.
}

func TestNewSatelliteDefaults(t *testing.T) {
	sat := NewSatellite(SatelliteConfig{})
	rows, cols, bm := sat.Layout()
	if rows != DefaultSatRows || cols != DefaultSatCols || bm != DefaultSatBitmapSize {
		t.Errorf("defaults: got %d×%d bm=%d", rows, cols, bm)
	}
	if sat.cfg.Addr != DefaultSatelliteAddr || sat.cfg.DeviceID != defaultDeviceID {
		t.Errorf("addr/device defaults: got %q / %q", sat.cfg.Addr, sat.cfg.DeviceID)
	}
}
