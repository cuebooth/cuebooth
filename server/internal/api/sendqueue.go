package api

import (
	"container/list"
	"sync"
)

// surfaceFrame is one queued surface frame: a key's render, or a re-baseline
// when layout is set. seq is the surface sequence it was taken at
// (protocol.md §10).
type surfaceFrame struct {
	data   []byte
	key    int
	seq    int
	layout bool
}

// sendQueue is a connection's outbound frame queue, split into two lanes
// because state and surface traffic are different shapes.
//
// State and command traffic is a stream — every frame matters, so an overflow
// can only be answered by failing the connection, and the lane is capped at
// sendBuffer. Surface traffic is a projection: each frame is the current render
// of one key, so a newer frame makes the one it replaces dead weight. That lane
// coalesces instead of overflowing, and needs no cap — it holds at most one
// frame per key index plus one layout, and key indices are gated by
// surfaceManager.inBounds before they reach here, so its ceiling is one full
// surface.
//
// pop drains the state lane first. Nothing orders the two against each other:
// protocol.md §10 requires the surface to follow the initial `state` snapshot,
// which this preserves because the snapshot is a state frame, and §4's
// ack-then-delta ordering is between two frames of the same lane. Draining in
// this order keeps an ack from waiting behind a page change — 32 frames and
// ~670KB at the default grid — on the congested link where that is most felt.
// The state lane cannot starve the surface: it is capped, and only a command or
// an event puts anything in it.
//
// Within each lane the order is FIFO, and coalescing never disturbs it: a key
// frame is replaced where it already sits, and the frames a layout supersedes
// are removed rather than moved.
type sendQueue struct {
	mu      sync.Mutex
	state   *list.List            // of []byte, oldest at the front
	surface *list.List            // of surfaceFrame, oldest at the front
	keys    map[int]*list.Element // newest queued key frame, per key index
	layout  *list.Element         // the queued layout frame, if any

	// wake signals the write loop, buffered so a producer never blocks and no
	// signal is lost: a poke that finds it full leaves a wake the write loop has
	// yet to consume, which covers the frame just queued.
	wake chan struct{}
}

func newSendQueue() *sendQueue {
	return &sendQueue{
		state:   list.New(),
		surface: list.New(),
		keys:    make(map[int]*list.Element),
		wake:    make(chan struct{}, 1),
	}
}

// pushOther queues a state or command frame, reporting false if the connection
// has exceeded its backlog and must be failed.
func (q *sendQueue) pushOther(data []byte) bool {
	q.mu.Lock()
	if q.state.Len() >= sendBuffer {
		q.mu.Unlock()
		return false
	}
	q.state.PushBack(data)
	q.mu.Unlock()
	q.poke()
	return true
}

// pushSurfaceKey queues one key's render at surface sequence seq, superseding
// whatever was queued for that key. Highest seq wins, not latest arrival: the
// cached replay and a live update for the same key can reach the queue out of
// sequence, and the client would discard the older one anyway (protocol.md §10).
func (q *sendQueue) pushSurfaceKey(key, seq int, data []byte) {
	q.mu.Lock()
	frame := surfaceFrame{data: data, key: key, seq: seq}
	if e, ok := q.keys[key]; ok {
		if e.Value.(surfaceFrame).seq >= seq {
			q.mu.Unlock()
			return
		}
		// Replaced in place, so the newer render keeps the older one's position
		// and still follows any layout queued ahead of it.
		e.Value = frame
	} else {
		q.keys[key] = q.surface.PushBack(frame)
	}
	q.mu.Unlock()
	q.poke()
}

// pushSurfaceLayout queues a surface re-baseline taken at surface sequence seq,
// dropping the queued key frames it supersedes: those at or below seq, which is
// the client's own rule (protocol.md §10). A key above seq is a render the
// layout does not cover and survives it.
//
// Of two layouts only the newer is kept, the incoming one on a tie. Ties are the
// ordinary case, not a corner: the sequence advances only on key updates, so a
// re-baseline broadcast while a replay is in flight carries the same one, and
// the later push is the current view of the surface.
func (q *sendQueue) pushSurfaceLayout(seq int, data []byte) {
	q.mu.Lock()
	if q.layout != nil && q.layout.Value.(surfaceFrame).seq > seq {
		q.mu.Unlock()
		return
	}
	for key, e := range q.keys {
		if e.Value.(surfaceFrame).seq <= seq {
			q.surface.Remove(e)
			delete(q.keys, key)
		}
	}
	if q.layout != nil {
		q.surface.Remove(q.layout)
	}
	q.layout = q.surface.PushBack(surfaceFrame{data: data, seq: seq, layout: true})
	q.mu.Unlock()
	q.poke()
}

// pop removes and returns the next frame to write — the oldest state frame if
// there is one, else the oldest surface frame — reporting false if the queue is
// empty.
func (q *sendQueue) pop() ([]byte, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if e := q.state.Front(); e != nil {
		return q.state.Remove(e).([]byte), true
	}
	e := q.surface.Front()
	if e == nil {
		return nil, false
	}
	f := q.surface.Remove(e).(surfaceFrame)
	if f.layout {
		if q.layout == e {
			q.layout = nil
		}
	} else if q.keys[f.key] == e {
		delete(q.keys, f.key)
	}
	return f.data, true
}

// depth is the number of frames currently queued.
func (q *sendQueue) depth() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.state.Len() + q.surface.Len()
}

// poke signals the write loop that work is waiting, without blocking.
func (q *sendQueue) poke() {
	select {
	case q.wake <- struct{}{}:
	default:
	}
}
