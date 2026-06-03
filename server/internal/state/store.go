package state

import (
	"encoding/json"
	"fmt"
	"sync"
)

// Store is the authoritative, concurrency-safe state container. It assigns a
// monotonic revision on every change and produces sparse deltas for broadcast.
type Store struct {
	mu       sync.RWMutex
	st       State
	cur      map[string]any // JSON-object form of st, kept in sync for diffing
	rev      int
	observer func(Result) // notified, under lock, on every change
}

// Result reports the outcome of an Update.
type Result struct {
	// Changed is true if the mutation altered the state.
	Changed bool
	// Rev is the revision after the update (unchanged if Changed is false).
	Rev int
	// Patch is the sparse delta to broadcast (nil if Changed is false).
	Patch map[string]any
}

// NewStore returns an empty Store at revision 0.
func NewStore() *Store {
	return &Store{cur: map[string]any{}}
}

// SetObserver registers a function notified of every change. It is invoked from
// Update while the Store lock is held (see Update), so it must not call back
// into the Store. It exists so a delta can be broadcast atomically with its
// revision assignment.
func (s *Store) SetObserver(fn func(Result)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.observer = fn
}

// Update applies mutate to the state under lock, then computes the delta. If the
// mutation changed anything, the revision is bumped, the observer is notified,
// and a patch is returned.
//
// The observer is called while the lock is still held so that revision
// assignment and notification are atomic: two concurrent writers can never
// broadcast their deltas out of revision order (which a client would misread as
// a dropped frame and re-sync over). The observer must therefore not re-enter
// the Store; the broadcast path only touches the hub and client send channels.
// Lock order: Store.mu -> hub.mu -> client.mu.
func (s *Store) Update(mutate func(*State)) (Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	mutate(&s.st)
	s.st.normalize()

	next, err := toMap(&s.st)
	if err != nil {
		return Result{}, err
	}
	patch := diff(s.cur, next)
	if patch == nil {
		return Result{Changed: false, Rev: s.rev}, nil
	}
	s.cur = next
	s.rev++
	res := Result{Changed: true, Rev: s.rev, Patch: patch}
	if s.observer != nil {
		s.observer(res)
	}
	return res, nil
}

// Snapshot returns the current revision and a full state object limited to the
// given topics (the topics a client is subscribed to). The returned map is a
// fresh copy safe for the caller to marshal.
func (s *Store) Snapshot(topics map[string]bool) (rev int, data map[string]any) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.rev, FilterTopics(s.cur, topics)
}

// SnapshotInto runs fn under the read lock with the current revision and a
// state object scoped to topics. Holding the lock across fn lets a caller
// enqueue the snapshot (and, on connect, register for broadcasts) atomically
// with respect to Update's observer — so a client never receives a higher-rev
// delta ahead of, or in place of, its snapshot frame. fn must not call back
// into the Store or block.
func (s *Store) SnapshotInto(topics map[string]bool, fn func(rev int, data map[string]any)) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	fn(s.rev, FilterTopics(s.cur, topics))
}

// Rev returns the current revision.
func (s *Store) Rev() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.rev
}

// toMap renders a State to its JSON-object form (the representation used for
// diffing and snapshots).
func toMap(st *State) (map[string]any, error) {
	b, err := json.Marshal(st)
	if err != nil {
		return nil, fmt.Errorf("state: marshal: %w", err)
	}
	m := map[string]any{}
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("state: unmarshal: %w", err)
	}
	return m, nil
}
