package state

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestPollerAppliesAndReportsChange(t *testing.T) {
	store := NewStore()
	var changes atomic.Int32
	var scene atomic.Value
	scene.Store("a")

	src := SourceFunc(func(ctx context.Context) (func(*State), error) {
		v := scene.Load().(string)
		return func(st *State) { st.OBSOrNew().Scene = v }, nil
	})

	// Changes are observed via the Store observer (how the server broadcasts).
	store.SetObserver(func(r Result) { changes.Add(1) })
	p := NewPoller(store, 5*time.Millisecond, nil, src)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { p.Run(ctx); close(done) }()

	// First tick is immediate: scene becomes "a" (1 change).
	waitFor(t, func() bool { return changes.Load() >= 1 })
	// Same value on subsequent ticks must NOT produce more changes.
	time.Sleep(30 * time.Millisecond)
	if got := changes.Load(); got != 1 {
		t.Errorf("changes = %d, want 1 (no change should not re-fire)", got)
	}
	// Flip the value: next tick should fire again.
	scene.Store("b")
	waitFor(t, func() bool { return changes.Load() >= 2 })

	cancel()
	<-done
	if _, m := store.Snapshot(allTopics()); m["obs"].(map[string]any)["scene"] != "b" {
		t.Errorf("final scene = %v, want b", m["obs"])
	}
}

func TestPollerSourceErrorIsTolerated(t *testing.T) {
	store := NewStore()
	var ticks atomic.Int32
	src := SourceFunc(func(ctx context.Context) (func(*State), error) {
		ticks.Add(1)
		return nil, errors.New("companion unreachable")
	})
	p := NewPoller(store, 5*time.Millisecond, nil, src)
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	p.Run(ctx) // must not panic or exit early on source errors
	if ticks.Load() < 2 {
		t.Errorf("expected multiple ticks despite errors, got %d", ticks.Load())
	}
	if store.Rev() != 0 {
		t.Errorf("rev = %d, want 0 (failed polls leave state intact)", store.Rev())
	}
}

func TestPollerNoSourcesWaitsForShutdown(t *testing.T) {
	store := NewStore()
	p := NewPoller(store, time.Millisecond, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { p.Run(ctx); close(done) }()
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Poller.Run with no sources did not return on cancel")
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("condition not met before deadline")
}
