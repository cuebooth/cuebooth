package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/cuebooth/cuebooth/server/internal/companion"
)

// drainQueue pops everything queued and returns the frames as strings.
func drainQueue(q *sendQueue) []string {
	var out []string
	for {
		data, ok := q.pop()
		if !ok {
			return out
		}
		out = append(out, string(data))
	}
}

// State frames go first so an ack does not wait behind a page change's worth of
// bitmaps; within each lane the order is the order they were queued.
func TestSendQueueServesStateFramesFirst(t *testing.T) {
	q := newSendQueue()
	q.pushOther([]byte("a"))
	q.pushSurfaceLayout(0, []byte("layout"))
	q.pushSurfaceKey(1, 1, []byte("k1"))
	q.pushOther([]byte("b"))
	q.pushSurfaceKey(2, 2, []byte("k2"))

	got := strings.Join(drainQueue(q), ",")
	if want := "a,b,layout,k1,k2"; got != want {
		t.Errorf("order = %q, want %q", got, want)
	}
}

// The guarantee that ordering has to preserve (protocol.md §10): the surface
// follows the initial `state` snapshot. Draining state first can only
// strengthen it — but a client must never see a surface frame before `hello`.
func TestSendQueueKeepsHelloAndSnapshotAheadOfTheSurface(t *testing.T) {
	q := newSendQueue()
	// The connect sequence, with live key updates racing the hub registration.
	q.pushOther([]byte("hello"))
	q.pushSurfaceKey(0, 1, []byte("k0"))
	q.pushOther([]byte("state"))
	q.pushSurfaceKey(1, 2, []byte("k1"))

	if got, want := strings.Join(drainQueue(q), ","), "hello,state,k0,k1"; got != want {
		t.Errorf("order = %q, want %q", got, want)
	}
}

// The point of the queue: a key's newest render replaces the one it supersedes
// instead of queueing behind it, so a client that can't keep up falls behind on
// button images rather than growing an unbounded backlog.
func TestSendQueueCoalescesPerKey(t *testing.T) {
	q := newSendQueue()
	for i := range 5000 {
		q.pushSurfaceKey(i%4, i+1, fmt.Appendf(nil, "k%d-v%d", i%4, i))
	}
	if got := q.depth(); got != 4 {
		t.Fatalf("depth after 5000 updates across 4 keys = %d, want 4", got)
	}
	// Each key holds its newest render: the last four pushes were i = 4996..4999.
	if got, want := strings.Join(drainQueue(q), ","), "k0-v4996,k1-v4997,k2-v4998,k3-v4999"; got != want {
		t.Errorf("queued frames = %q, want %q", got, want)
	}
}

// Coalescing must not reorder what is left, or a key frame could overtake the
// layout that supersedes it.
func TestSendQueueCoalescingKeepsPosition(t *testing.T) {
	q := newSendQueue()
	q.pushSurfaceKey(1, 1, []byte("k1-old"))
	q.pushSurfaceKey(2, 2, []byte("k2"))
	q.pushSurfaceKey(3, 3, []byte("k3"))
	q.pushSurfaceKey(1, 4, []byte("k1-new")) // supersedes k1-old in place

	got := strings.Join(drainQueue(q), ",")
	if want := "k1-new,k2,k3"; got != want {
		t.Errorf("order = %q, want %q", got, want)
	}
}

// A layout re-baselines the surface and Companion re-pushes every key behind it,
// so the queued renders it covers describe a grid that no longer exists.
func TestSendQueueLayoutSupersedesTheKeysItCovers(t *testing.T) {
	q := newSendQueue()
	q.pushSurfaceLayout(1, []byte("layout-old"))
	q.pushSurfaceKey(1, 2, []byte("k1"))
	q.pushOther([]byte("a"))
	q.pushSurfaceKey(2, 3, []byte("k2"))
	// Key 3 sits exactly at the incoming layout's sequence. That is the ordinary
	// production case — a broadcast layout carries m.seq and so does the newest
	// key queued behind it — and it is the boundary the whole rule turns on.
	q.pushSurfaceKey(3, 4, []byte("k3"))
	q.pushSurfaceLayout(4, []byte("layout-new"))

	// Only the state frame and the newest layout survive: a layout says nothing
	// about state traffic.
	if got, want := strings.Join(drainQueue(q), ","), "a,layout-new"; got != want {
		t.Errorf("order = %q, want %q", got, want)
	}

	// Keys pushed after the layout are kept — they are the re-push.
	q.pushSurfaceLayout(4, []byte("layout"))
	q.pushSurfaceKey(1, 5, []byte("k1-fresh"))
	if got, want := strings.Join(drainQueue(q), ","), "layout,k1-fresh"; got != want {
		t.Errorf("post-layout keys = %q, want %q", got, want)
	}
}

// The counterpart, and the rule the layout's seq exists for (protocol.md §10): a
// key above the layout's sequence is a render the layout does not cover, so it
// survives, and an older copy of that key loses to it rather than overwriting
// it. The queue enforces this on its own terms — its callers currently keep
// seq order, but the client's rule is stated over sequences, not arrivals.
func TestSendQueueLayoutKeepsKeysNewerThanItself(t *testing.T) {
	q := newSendQueue()
	q.pushSurfaceKey(1, 7, []byte("k1-live"))
	q.pushSurfaceKey(2, 8, []byte("k2-live"))
	q.pushSurfaceLayout(5, []byte("layout"))
	q.pushSurfaceKey(1, 3, []byte("k1-stale"))

	if got, want := strings.Join(drainQueue(q), ","), "k1-live,k2-live,layout"; got != want {
		t.Errorf("frames = %q, want %q", got, want)
	}
}

// An older layout arriving behind a newer one is itself superseded.
func TestSendQueueOlderLayoutIsDropped(t *testing.T) {
	q := newSendQueue()
	q.pushSurfaceLayout(9, []byte("layout-new"))
	q.pushSurfaceLayout(5, []byte("layout-stale"))
	if got, want := strings.Join(drainQueue(q), ","), "layout-new"; got != want {
		t.Errorf("frames = %q, want %q", got, want)
	}
}

// Two layouts at the same sequence is the ordinary case, not a corner: the
// sequence advances only on key updates, so onLayout and onClear broadcast at
// whatever it already was. The later push is the current view of the surface, so
// it must win — pinning the direction, which a >= comparison would reverse.
func TestSendQueueLayoutTieKeepsTheIncomingOne(t *testing.T) {
	q := newSendQueue()
	q.pushSurfaceLayout(5, []byte("layout-first"))
	q.pushSurfaceLayout(5, []byte("layout-second"))
	if got, want := strings.Join(drainQueue(q), ","), "layout-second"; got != want {
		t.Errorf("frames = %q, want %q", got, want)
	}
}

// A KEYS-CLEAR landing mid-replay must not leave the replay's remaining keys
// queued behind the layout that blanks them: the client drops its keys on a
// layout and applies whatever follows, so those frames would repaint the page
// Companion just cleared — and after a genuine blank nothing re-pushes to
// correct it. The operator taps a button that is no longer there.
func TestSendInitialIsAtomicAgainstAReBaseline(t *testing.T) {
	const rows, cols = 8, 16 // 128 keys: a replay long enough to interleave
	for range 30 {
		sat := &fakeSat{rows: rows, cols: cols, bm: 72}
		hub := newHub()
		m := newSurfaceManager(sat, hub)
		for k := range rows * cols {
			sat.onKey(companion.SatelliteKey{Key: k, Type: "BUTTON", BitmapBase64: "AA=="})
		}
		c := newTestClient()
		hub.add(c)

		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); m.sendInitial(c) }()
		go func() { defer wg.Done(); sat.onClear() }()
		wg.Wait()

		frames := drainFrames(c)
		lastLayout, layoutSeq := -1, 0
		for i, f := range frames {
			if f["type"] == typeSurfaceLayout {
				lastLayout, layoutSeq = i, int(f["seq"].(float64))
			}
		}
		if lastLayout < 0 {
			t.Fatal("no layout was delivered")
		}
		for _, f := range frames[lastLayout+1:] {
			if f["type"] != typeSurfaceKey {
				continue
			}
			if seq := int(f["seq"].(float64)); seq <= layoutSeq {
				t.Fatalf("key %v at seq %d follows a layout at seq %d that supersedes it",
					f["key"], seq, layoutSeq)
			}
		}
	}
}

// A client joins the hub before its surface is replayed, so the first surface
// frame it sees may be a live surface-key. It has no bitmap size until a
// surface-layout arrives, so it keeps that frame's seq but not its image — safe
// only while the layout behind it supersedes the key (protocol.md §10, drop keys
// at or below the layout's seq). One that outlived the layout would hold a seq
// that suppresses the replay's copy, leaving the button color-only.
func TestAKeyRacingTheReplayCannotOutliveItsLayout(t *testing.T) {
	const rows, cols = 8, 16 // 128 keys: a replay long enough to interleave
	const raced, updates = 3, 64
	newest := fmt.Sprintf("cmVuZGVy%d", updates-1)
	for range 50 {
		sat := &fakeSat{rows: rows, cols: cols, bm: 72}
		hub := newHub()
		m := newSurfaceManager(sat, hub)
		for k := range rows * cols {
			sat.onKey(companion.SatelliteKey{Key: k, Type: "BUTTON", BitmapBase64: "b2xk"})
		}

		c := newTestClient()
		hub.add(c)
		stop := drainConcurrently(c)

		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); m.sendInitial(c) }()
		go func() {
			defer wg.Done()
			for i := range updates {
				sat.onKey(companion.SatelliteKey{
					Key:          raced,
					Type:         "BUTTON",
					BitmapBase64: fmt.Sprintf("cmVuZGVy%d", i),
				})
			}
		}()
		wg.Wait()
		frames := stop()

		layout := -1
		for i, f := range frames {
			if f["type"] == typeSurfaceLayout {
				layout = i
				break
			}
		}
		if layout < 0 {
			t.Fatal("no layout was delivered")
		}
		layoutSeq := int(frames[layout]["seq"].(float64))
		for _, f := range frames[:layout] {
			if f["type"] != typeSurfaceKey {
				continue
			}
			if seq := int(f["seq"].(float64)); seq > layoutSeq {
				t.Fatalf("key %v at seq %d reached the client ahead of the layout at seq %d, which does not supersede it",
					f["key"], seq, layoutSeq)
			}
		}
		// The newest render still arrives afterwards, so the button isn't left
		// waiting on Companion's next re-render for an image.
		last := ""
		for _, f := range frames[layout+1:] {
			if f["type"] == typeSurfaceKey && int(f["key"].(float64)) == raced {
				last, _ = f["bitmap"].(string)
			}
		}
		if last != newest {
			t.Fatalf("raced key's bitmap after the layout = %q, want %q", last, newest)
		}
	}
}

// State and command traffic is a stream, not a projection: an overflow can only
// be answered by failing the connection, so the cap has to still bite.
func TestSendQueueOtherOverflows(t *testing.T) {
	q := newSendQueue()
	for i := range sendBuffer {
		if !q.pushOther([]byte("x")) {
			t.Fatalf("push %d of %d rejected below the cap", i, sendBuffer)
		}
	}
	if q.pushOther([]byte("x")) {
		t.Fatal("push past sendBuffer should be rejected")
	}
	// Draining frees the budget again.
	if _, ok := q.pop(); !ok {
		t.Fatal("pop returned nothing from a full queue")
	}
	if !q.pushOther([]byte("x")) {
		t.Error("push should be accepted once a slot is drained")
	}
}

// Surface frames must not consume the state budget, or a page change would
// still be able to fail a connection for reasons that have nothing to do with
// state traffic.
func TestSendQueueSurfaceDoesNotConsumeOtherBudget(t *testing.T) {
	q := newSendQueue()
	q.pushSurfaceLayout(0, []byte("layout"))
	for i := range 10_000 {
		q.pushSurfaceKey(i%256, i+1, []byte("k"))
	}
	for i := range sendBuffer {
		if !q.pushOther([]byte("x")) {
			t.Fatalf("state frame %d rejected after a surface flood", i)
		}
	}
}

// pop has to unindex the frame it removes, or the next push for that key would
// try to supersede an element that is no longer in the list.
func TestSendQueuePopUnindexes(t *testing.T) {
	q := newSendQueue()
	q.pushSurfaceKey(1, 1, []byte("k1-first"))
	if _, ok := q.pop(); !ok {
		t.Fatal("pop returned nothing")
	}
	q.pushSurfaceKey(1, 2, []byte("k1-second"))
	if got, want := strings.Join(drainQueue(q), ","), "k1-second"; got != want {
		t.Errorf("frames = %q, want %q", got, want)
	}
	if got := q.depth(); got != 0 {
		t.Errorf("depth after draining = %d, want 0", got)
	}
}

// The wake channel has to be buffered. A push happens while the write loop is
// between its empty pop and its select on wake as often as not, and an
// unbuffered channel has no receiver at that moment, so poke's non-blocking
// send would take its default arm and lose the signal. A lone frame — an ack, or
// the error frame before a close — would then sit queued until unrelated traffic
// woke the loop.
func TestSendQueuePushLeavesAWakeBehind(t *testing.T) {
	for _, tc := range []struct {
		name string
		push func(*sendQueue)
	}{
		{"other", func(q *sendQueue) { q.pushOther([]byte("x")) }},
		{"surface key", func(q *sendQueue) { q.pushSurfaceKey(0, 1, []byte("k")) }},
		{"surface layout", func(q *sendQueue) { q.pushSurfaceLayout(1, []byte("l")) }},
	} {
		q := newSendQueue()
		tc.push(q)
		select {
		case <-q.wake:
		default:
			t.Errorf("%s: pushed with no write loop parked and left no wake behind", tc.name)
		}
	}
}

func TestSendQueuePokeIsNonBlocking(t *testing.T) {
	q := newSendQueue()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range 100 {
			q.pushOther([]byte("x"))
			q.pop()
		}
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("a producer blocked with no write loop consuming wake")
	}
}

// stalledClient is a clientConn with a real socket (so close() can tear it down)
// whose write loop is deliberately never started, which is the state a client on
// a congested link is in: frames accumulate in the queue with nothing draining
// them. The peer end is returned so a test can read what was actually written.
func stalledClient(t *testing.T, srv *Server) (*clientConn, *websocket.Conn) {
	t.Helper()
	conns := make(chan *websocket.Conn, 1)
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Errorf("accept: %v", err)
			return
		}
		conns <- c
		<-r.Context().Done()
	}))
	t.Cleanup(hs.Close)

	client, _, err := websocket.Dial(t.Context(), "ws"+strings.TrimPrefix(hs.URL, "http"), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { client.CloseNow() })

	server := <-conns
	t.Cleanup(func() { server.CloseNow() })
	return &clientConn{server: srv, conn: server, send: newSendQueue(), done: make(chan struct{}), topics: allTopicsSet()}, client
}

func isClosed(c *clientConn) bool {
	select {
	case <-c.done:
		return true
	default:
		return false
	}
}

// The behaviour CB-088 is about. Companion re-renders every key on a page
// change, and an operator's tablet at the back of a hall may not drain that as
// fast as it arrives. Before, the backlog overflowed and the client was dropped
// mid-event — and then replayed the whole surface on reconnect. Now it
// coalesces.
func TestSurfaceFloodDoesNotDropASlowClient(t *testing.T) {
	sat := &fakeSat{rows: 4, cols: 8, bm: 72} // 32 keys
	hub := newHub()
	newSurfaceManager(sat, hub)

	c, _ := stalledClient(t, &Server{})
	hub.add(c)

	// Fifty page changes' worth of re-renders with nothing draining them.
	for range 50 {
		for key := range 32 {
			sat.onKey(companion.SatelliteKey{Key: key, Type: "BUTTON", BitmapBase64: "AA=="})
		}
	}

	if isClosed(c) {
		t.Fatal("a slow client was dropped by surface traffic")
	}
	if got := c.send.depth(); got != 32 {
		t.Errorf("queue depth = %d after 1600 key updates, want 32 (one per key)", got)
	}
	// What it holds is the newest render of each key, not the oldest.
	for _, f := range drainFrames(c) {
		if got := f["seq"].(float64); got <= 1568 { // the first 49 rounds are seq 1..1568
			t.Errorf("key %v holds seq %v, want a frame from the final round", f["key"], got)
		}
	}
}

// The drop policy still has to apply to state traffic, where every frame counts
// and falling behind can only be answered by re-snapshotting.
func TestStateFloodStillDropsASlowClient(t *testing.T) {
	c, _ := stalledClient(t, &Server{})
	for range sendBuffer + 1 {
		c.enqueue([]byte(`{"type":"state-delta"}`))
	}
	if !isClosed(c) {
		t.Fatal("a state-delta backlog past the cap should fail the connection")
	}
	code, reason, _ := c.closeInfo()
	if code != websocket.StatusPolicyViolation || reason != "client send buffer full" {
		t.Errorf("close = %v %q, want %v %q", code, reason,
			websocket.StatusPolicyViolation, "client send buffer full")
	}
}

// A frame queued as the connection is torn down still has to reach the peer —
// protocol.md §2 requires the `error` frame for a malformed client frame to be
// delivered before the close handshake. The write loop reaches this drain only
// when a frame is queued between its last empty pop and its select on done, a
// window no test can schedule, so the drain is exercised directly.
func TestFlushRemainingDeliversFramesQueuedAtClose(t *testing.T) {
	c, peer := stalledClient(t, &Server{})
	c.enqueue(mustMarshal(errorFrame{Type: typeError, Code: codeProtocol, Message: "malformed JSON frame"}))
	c.send.pushSurfaceKey(0, 1, []byte(`{"type":"surface-key","key":0}`))

	c.flushRemaining()

	for _, want := range []string{typeError, typeSurfaceKey} {
		rctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
		_, data, err := peer.Read(rctx)
		cancel()
		if err != nil {
			t.Fatalf("reading the %s frame: %v", want, err)
		}
		var f map[string]any
		if err := json.Unmarshal(data, &f); err != nil {
			t.Fatalf("unmarshal %q: %v", data, err)
		}
		if f["type"] != want {
			t.Errorf("frame type = %v, want %s", f["type"], want)
		}
	}
	if got := c.send.depth(); got != 0 {
		t.Errorf("queue depth after the flush = %d, want 0", got)
	}
}
