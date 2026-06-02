package state

import (
	"encoding/json"
	"fmt"
	"sync"
)

// Store is the authoritative, concurrency-safe state container. It assigns a
// monotonic revision on every change and produces sparse deltas for broadcast.
type Store struct {
	mu  sync.RWMutex
	st  State
	cur map[string]any // JSON-object form of st, kept in sync for diffing
	rev int
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

// Update applies mutate to the state under lock, then computes the delta. If the
// mutation changed anything, the revision is bumped and a patch is returned.
func (s *Store) Update(mutate func(*State)) (Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	mutate(&s.st)

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
	return Result{Changed: true, Rev: s.rev, Patch: patch}, nil
}

// Snapshot returns the current revision and a full state object limited to the
// given topics (the topics a client is subscribed to). The returned map is a
// fresh copy safe for the caller to marshal.
func (s *Store) Snapshot(topics map[string]bool) (rev int, data map[string]any) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := map[string]any{}
	for k, v := range s.cur {
		if topics[k] {
			out[k] = v
		}
	}
	return s.rev, out
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
