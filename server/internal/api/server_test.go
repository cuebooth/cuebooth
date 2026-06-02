package api

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/cuebooth/cuebooth/server/internal/companion"
	"github.com/cuebooth/cuebooth/server/internal/config"
)

// fakePresser records button presses and can be made to fail.
type fakePresser struct {
	mu      sync.Mutex
	pressed []companion.Location
	err     error
}

func (f *fakePresser) Press(_ context.Context, loc companion.Location) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.pressed = append(f.pressed, loc)
	return nil
}

func (f *fakePresser) last() (companion.Location, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.pressed) == 0 {
		return companion.Location{}, false
	}
	return f.pressed[len(f.pressed)-1], true
}

func testConfig() *config.Config {
	return &config.Config{
		Server:    config.ServerConfig{Listen: "127.0.0.1:0"},
		Companion: config.CompanionConfig{BaseURL: "http://localhost:8000"},
		Presets: config.PresetsConfig{
			Camera: map[string]map[string]config.ActionRef{
				"main": {"choir": {CompanionButton: "1/3/2"}},
			},
			Scene: map[string]config.ActionRef{
				"camera-only": {CompanionButton: "7/0/1"},
			},
			Streaming: map[string]config.ActionRef{
				"start": {CompanionButton: "8/0/0"},
				"stop":  {CompanionButton: "8/0/1"},
			},
		},
	}
}

// dialTestServer starts the API on an httptest server and dials /ws.
func dialTestServer(t *testing.T, presser buttonPresser) (*websocket.Conn, context.Context) {
	t.Helper()
	srv := NewServer(testConfig(), presser, WithServerID("test-server"), WithVersion("9.9.9"))
	hs := httptest.NewServer(srv.Handler())
	t.Cleanup(hs.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)

	wsURL := "ws" + strings.TrimPrefix(hs.URL, "http") + "/ws"
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { conn.Close(websocket.StatusNormalClosure, "") })
	return conn, ctx
}

func readFrame(t *testing.T, ctx context.Context, conn *websocket.Conn) map[string]any {
	t.Helper()
	rctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	_, data, err := conn.Read(rctx)
	if err != nil {
		t.Fatalf("read frame: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("unmarshal frame %q: %v", data, err)
	}
	return m
}

func writeFrame(t *testing.T, ctx context.Context, conn *websocket.Conn, v any) {
	t.Helper()
	b, _ := json.Marshal(v)
	if err := conn.Write(ctx, websocket.MessageText, b); err != nil {
		t.Fatalf("write frame: %v", err)
	}
}

// expectPongNext proves no other frame was queued ahead of the pong: it sends a
// ping and asserts the very next frame is the matching pong. Because a single
// connection's frames are FIFO and a command's delta is broadcast synchronously
// before the next client frame is read, any spurious delta would arrive before
// this pong. (A timed-out Read can't be used to assert "nothing arrives" —
// cancelling a Read's context closes the connection in coder/websocket.)
func expectPongNext(t *testing.T, ctx context.Context, conn *websocket.Conn, id string) {
	t.Helper()
	writeFrame(t, ctx, conn, map[string]any{"type": "ping", "id": id})
	f := readFrame(t, ctx, conn)
	if f["type"] != typePong || f["id"] != id {
		t.Fatalf("expected pong %q with nothing ahead of it, got %v", id, f)
	}
}

func TestHelloThenInitialState(t *testing.T) {
	conn, ctx := dialTestServer(t, &fakePresser{})

	hello := readFrame(t, ctx, conn)
	if hello["type"] != typeHello {
		t.Fatalf("first frame type = %v, want hello", hello["type"])
	}
	if hello["proto"] != ProtoVersion {
		t.Errorf("proto = %v, want %s", hello["proto"], ProtoVersion)
	}
	if hello["server_version"] != "9.9.9" || hello["server_id"] != "test-server" {
		t.Errorf("hello identity = %v", hello)
	}

	st := readFrame(t, ctx, conn)
	if st["type"] != typeState {
		t.Fatalf("second frame type = %v, want state", st["type"])
	}
	if st["rev"].(float64) != 0 {
		t.Errorf("initial rev = %v, want 0", st["rev"])
	}
}

func TestCommandAckThenDelta(t *testing.T) {
	presser := &fakePresser{}
	conn, ctx := dialTestServer(t, presser)
	readFrame(t, ctx, conn) // hello
	readFrame(t, ctx, conn) // initial state

	writeFrame(t, ctx, conn, map[string]any{
		"type": "cmd", "id": "c1", "target": "camera", "action": "preset", "value": "choir",
	})

	ack := readFrame(t, ctx, conn)
	if ack["type"] != typeAck || ack["id"] != "c1" {
		t.Fatalf("expected ack for c1, got %v", ack)
	}
	delta := readFrame(t, ctx, conn)
	if delta["type"] != typeStateDelta {
		t.Fatalf("expected state-delta, got %v", delta)
	}
	patch := delta["patch"].(map[string]any)
	cam := patch["camera"].(map[string]any)["main"].(map[string]any)
	if cam["preset"] != "choir" {
		t.Errorf("delta camera.main.preset = %v, want choir", cam["preset"])
	}
	if loc, ok := presser.last(); !ok || loc.String() != "1/3/2" {
		t.Errorf("pressed = %v (ok=%v), want 1/3/2", loc, ok)
	}
}

func TestNakUnknownPreset(t *testing.T) {
	conn, ctx := dialTestServer(t, &fakePresser{})
	readFrame(t, ctx, conn)
	readFrame(t, ctx, conn)

	writeFrame(t, ctx, conn, map[string]any{
		"type": "cmd", "id": "c2", "target": "camera", "action": "preset", "value": "nope",
	})
	nak := readFrame(t, ctx, conn)
	if nak["type"] != typeNak || nak["id"] != "c2" {
		t.Fatalf("expected nak for c2, got %v", nak)
	}
	if nak["error"].(map[string]any)["code"] != codeUnknownPreset {
		t.Errorf("nak code = %v, want %s", nak["error"], codeUnknownPreset)
	}
}

func TestUnknownTargetNak(t *testing.T) {
	conn, ctx := dialTestServer(t, &fakePresser{})
	readFrame(t, ctx, conn)
	readFrame(t, ctx, conn)
	writeFrame(t, ctx, conn, map[string]any{"type": "cmd", "id": "x", "target": "teleporter", "action": "go"})
	nak := readFrame(t, ctx, conn)
	if nak["type"] != typeNak || nak["error"].(map[string]any)["code"] != codeUnknownTarget {
		t.Errorf("expected unknown_target nak, got %v", nak)
	}
}

func TestSubscribeUnknownTopic(t *testing.T) {
	conn, ctx := dialTestServer(t, &fakePresser{})
	readFrame(t, ctx, conn)
	readFrame(t, ctx, conn)
	writeFrame(t, ctx, conn, map[string]any{"type": "subscribe", "topics": []string{"bogus"}})
	e := readFrame(t, ctx, conn)
	if e["type"] != typeError || e["code"] != codeUnknownTopic {
		t.Errorf("expected unknown_topic error, got %v", e)
	}
}

func TestUnsubscribeFiltersDelta(t *testing.T) {
	conn, ctx := dialTestServer(t, &fakePresser{})
	readFrame(t, ctx, conn) // hello
	readFrame(t, ctx, conn) // initial state

	// Drop the obs topic; the server replies with a fresh snapshot.
	writeFrame(t, ctx, conn, map[string]any{"type": "unsubscribe", "topics": []string{"obs"}})
	if snap := readFrame(t, ctx, conn); snap["type"] != typeState {
		t.Fatalf("expected state snapshot after unsubscribe, got %v", snap)
	}

	// A scene change mutates obs — which we no longer watch, so only the ack
	// should arrive, no delta.
	writeFrame(t, ctx, conn, map[string]any{"type": "cmd", "id": "s1", "target": "scene", "action": "set", "value": "camera-only"})
	ack := readFrame(t, ctx, conn)
	if ack["type"] != typeAck {
		t.Fatalf("expected ack, got %v", ack)
	}
	expectPongNext(t, ctx, conn, "after-scene")
}

func TestPingPong(t *testing.T) {
	conn, ctx := dialTestServer(t, &fakePresser{})
	readFrame(t, ctx, conn)
	readFrame(t, ctx, conn)
	writeFrame(t, ctx, conn, map[string]any{"type": "ping", "id": "k1"})
	pong := readFrame(t, ctx, conn)
	if pong["type"] != typePong || pong["id"] != "k1" {
		t.Errorf("expected pong k1, got %v", pong)
	}
}

func TestGetStateReturnsSnapshot(t *testing.T) {
	conn, ctx := dialTestServer(t, &fakePresser{})
	readFrame(t, ctx, conn)
	readFrame(t, ctx, conn)

	writeFrame(t, ctx, conn, map[string]any{"type": "cmd", "id": "c1", "target": "streaming", "action": "start"})
	readFrame(t, ctx, conn) // ack
	readFrame(t, ctx, conn) // delta

	writeFrame(t, ctx, conn, map[string]any{"type": "get_state"})
	snap := readFrame(t, ctx, conn)
	if snap["type"] != typeState {
		t.Fatalf("expected state, got %v", snap)
	}
	if snap["rev"].(float64) != 1 {
		t.Errorf("snapshot rev = %v, want 1", snap["rev"])
	}
	obs := snap["obs"].(map[string]any)
	if obs["streaming"] != true {
		t.Errorf("snapshot obs.streaming = %v, want true", obs["streaming"])
	}
}

func TestUnknownTypeIgnored(t *testing.T) {
	conn, ctx := dialTestServer(t, &fakePresser{})
	readFrame(t, ctx, conn)
	readFrame(t, ctx, conn)
	// An unknown frame type must be ignored (no error, no close) and the
	// connection must remain usable — the next frame is the pong, proving the
	// unknown frame produced no response.
	writeFrame(t, ctx, conn, map[string]any{"type": "who-knows"})
	expectPongNext(t, ctx, conn, "after")
}
