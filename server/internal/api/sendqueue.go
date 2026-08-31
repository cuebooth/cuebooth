package api

import (
	"container/list"
	"sync"
)

// frameClass selects the back-pressure policy the send queue applies to a
// frame. State and command traffic is a stream — every frame matters, so an
// overflow can only be answered by failing the connection. Surface traffic is a
// projection: each frame is the current render of one key, so a newer frame
// makes the one it replaces dead weight (protocol.md §10).
type frameClass int

const (
	classOther frameClass = iota
	classSurfaceKey
	classSurfaceLayout
)

type queuedFrame struct {
	data []byte
	kind frameClass
	// key is the surface key index for classSurfaceKey, unused otherwise.
	key int
	// seq is the surface sequence the frame was taken at (protocol.md §10),
	// unused for classOther.
	seq int
}

// sendQueue is a connection's outbound frame queue: an ordered list plus an
// index into it, so a surface frame can supersede the one it replaces instead
// of queueing behind it.
//
// Ordering is FIFO across every class. Coalescing never reorders what remains:
// a key frame is replaced where it already sits, and the frames a layout
// supersedes are removed rather than moved.
//
// Non-surface frames are capped at sendBuffer; overflowing it fails the
// connection. Surface frames need no cap: the queue holds at most one per key
// index plus one layout, and key indices are gated by surfaceManager.inBounds
// before they reach here, so the ceiling is one full surface. A socket that has
// genuinely stopped draining is still failed, by writeFrame's deadline.
type sendQueue struct {
	mu     sync.Mutex
	frames *list.List            // of queuedFrame, oldest at the front
	keys   map[int]*list.Element // newest queued surface-key frame, per key index
	layout *list.Element         // the queued surface-layout frame, if any
	other  int                   // queued frames of classOther, against the sendBuffer cap

	// wake signals the write loop, buffered so a producer never blocks and no
	// signal is lost: a poke that finds it full leaves a wake the write loop has
	// yet to consume, which covers the frame just queued.
	wake chan struct{}
}

func newSendQueue() *sendQueue {
	return &sendQueue{
		frames: list.New(),
		keys:   make(map[int]*list.Element),
		wake:   make(chan struct{}, 1),
	}
}

// pushOther queues a state or command frame, reporting false if the connection
// has exceeded its backlog and must be failed.
func (q *sendQueue) pushOther(data []byte) bool {
	q.mu.Lock()
	if q.other >= sendBuffer {
		q.mu.Unlock()
		return false
	}
	q.frames.PushBack(queuedFrame{data: data, kind: classOther})
	q.other++
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
	frame := queuedFrame{data: data, kind: classSurfaceKey, key: key, seq: seq}
	if e, ok := q.keys[key]; ok {
		if e.Value.(queuedFrame).seq >= seq {
			q.mu.Unlock()
			return
		}
		// Replaced in place, so the newer render keeps the older one's position
		// and still follows any layout queued ahead of it.
		e.Value = frame
	} else {
		q.keys[key] = q.frames.PushBack(frame)
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
	if q.layout != nil && q.layout.Value.(queuedFrame).seq > seq {
		q.mu.Unlock()
		return
	}
	for key, e := range q.keys {
		if e.Value.(queuedFrame).seq <= seq {
			q.frames.Remove(e)
			delete(q.keys, key)
		}
	}
	if q.layout != nil {
		q.frames.Remove(q.layout)
	}
	q.layout = q.frames.PushBack(queuedFrame{data: data, kind: classSurfaceLayout, seq: seq})
	q.mu.Unlock()
	q.poke()
}

// pop removes and returns the oldest queued frame, reporting false if the queue
// is empty.
func (q *sendQueue) pop() ([]byte, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	e := q.frames.Front()
	if e == nil {
		return nil, false
	}
	f := q.frames.Remove(e).(queuedFrame)
	switch f.kind {
	case classSurfaceKey:
		if q.keys[f.key] == e {
			delete(q.keys, f.key)
		}
	case classSurfaceLayout:
		if q.layout == e {
			q.layout = nil
		}
	default:
		q.other--
	}
	return f.data, true
}

// depth is the number of frames currently queued.
func (q *sendQueue) depth() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.frames.Len()
}

// poke signals the write loop that work is waiting, without blocking.
func (q *sendQueue) poke() {
	select {
	case q.wake <- struct{}{}:
	default:
	}
}
