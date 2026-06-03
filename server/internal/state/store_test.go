package state

import (
	"fmt"
	"reflect"
	"sync"
	"testing"
)

func allTopics() map[string]bool {
	m := map[string]bool{}
	for _, t := range Topics {
		m[t] = true
	}
	return m
}

func TestUpdateAssignsRevAndDelta(t *testing.T) {
	s := NewStore()
	if s.Rev() != 0 {
		t.Fatalf("initial rev = %d, want 0", s.Rev())
	}

	res, err := s.Update(func(st *State) {
		st.OBSOrNew().Scene = "camera-only"
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if !res.Changed || res.Rev != 1 {
		t.Fatalf("res = %+v, want changed rev 1", res)
	}
	obs, ok := res.Patch["obs"].(map[string]any)
	if !ok {
		t.Fatalf("patch.obs missing: %v", res.Patch)
	}
	if obs["scene"] != "camera-only" {
		t.Errorf("patch obs.scene = %v, want camera-only", obs["scene"])
	}
	// Booleans must be present once obs exists.
	if obs["streaming"] != false || obs["recording"] != false {
		t.Errorf("expected streaming/recording present and false, got %v", obs)
	}
}

func TestUpdateNoChangeNoRev(t *testing.T) {
	s := NewStore()
	if _, err := s.Update(func(st *State) { st.OBSOrNew().Scene = "x" }); err != nil {
		t.Fatal(err)
	}
	res, err := s.Update(func(st *State) { st.OBSOrNew().Scene = "x" }) // same value
	if err != nil {
		t.Fatal(err)
	}
	if res.Changed {
		t.Errorf("expected no change, got %+v", res)
	}
	if s.Rev() != 1 {
		t.Errorf("rev = %d, want 1 (unchanged)", s.Rev())
	}
}

func TestDeltaOnlyChangedFields(t *testing.T) {
	s := NewStore()
	s.Update(func(st *State) {
		o := st.OBSOrNew()
		o.Scene = "a"
		o.Streaming = false
	})
	res, _ := s.Update(func(st *State) {
		st.OBSOrNew().Streaming = true // only streaming changes
	})
	obs := res.Patch["obs"].(map[string]any)
	if _, ok := obs["scene"]; ok {
		t.Errorf("unchanged scene must not appear in delta: %v", obs)
	}
	if obs["streaming"] != true {
		t.Errorf("delta should carry streaming=true, got %v", obs)
	}
}

func TestSnapshotScopedToTopics(t *testing.T) {
	s := NewStore()
	s.Update(func(st *State) {
		st.OBSOrNew().Scene = "a"
		st.CameraOrNew("main").Preset = "choir"
	})

	rev, data := s.Snapshot(map[string]bool{"camera": true})
	if rev != 1 {
		t.Errorf("rev = %d, want 1", rev)
	}
	if _, ok := data["obs"]; ok {
		t.Errorf("obs should be excluded by camera-only subscription: %v", data)
	}
	cam, ok := data["camera"].(map[string]any)
	if !ok {
		t.Fatalf("camera missing: %v", data)
	}
	main := cam["main"].(map[string]any)
	if main["preset"] != "choir" {
		t.Errorf("camera.main.preset = %v, want choir", main["preset"])
	}
}

func TestDeletionBecomesNull(t *testing.T) {
	// diff is exercised directly for the deletion rule.
	old := map[string]any{"obs": map[string]any{"scene": "a"}}
	next := map[string]any{}
	patch := diff(old, next)
	if patch["obs"] != nil {
		t.Errorf("removed key should map to nil, got %v", patch["obs"])
	}
	if _, ok := patch["obs"]; !ok {
		t.Errorf("deletion key must be present: %v", patch)
	}
}

// TestConcurrentUpdatesObservedInRevOrder verifies the observer fires in strict
// monotonic revision order even under concurrent writers — the property the
// hub relies on to broadcast deltas without spurious rev gaps.
func TestConcurrentUpdatesObservedInRevOrder(t *testing.T) {
	s := NewStore()
	var mu sync.Mutex
	var revs []int
	s.SetObserver(func(r Result) {
		mu.Lock()
		revs = append(revs, r.Rev)
		mu.Unlock()
	})

	const n = 50
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, _ = s.Update(func(st *State) { st.CameraOrNew("main").Preset = fmt.Sprintf("p%d", i) })
		}(i)
	}
	wg.Wait()

	// Some updates may be no-ops (same preset value already set), but every
	// observed rev must be strictly increasing — never reordered.
	for i := 1; i < len(revs); i++ {
		if revs[i] <= revs[i-1] {
			t.Fatalf("observed revs out of order at %d: %v", i, revs)
		}
	}
	if len(revs) == 0 || revs[len(revs)-1] != s.Rev() {
		t.Errorf("last observed rev %v != store rev %d", revs, s.Rev())
	}
}

// TestSlidesPendingActionsNeverNull guards the protocol invariant that
// slides.pending_actions is always an array, never null — even when a caller
// sets SlidesState with a nil PendingActions slice.
func TestSlidesPendingActionsNeverNull(t *testing.T) {
	s := NewStore()
	res, err := s.Update(func(st *State) {
		st.Slides = &SlidesState{Current: 1, Total: 3} // PendingActions left nil
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	slides, ok := res.Patch["slides"].(map[string]any)
	if !ok {
		t.Fatalf("slides missing from patch: %v", res.Patch)
	}
	pa, ok := slides["pending_actions"]
	if !ok {
		t.Fatal("pending_actions absent")
	}
	arr, ok := pa.([]any)
	if !ok || len(arr) != 0 {
		t.Errorf("pending_actions = %#v, want empty array (not null)", pa)
	}
}

func TestFilterTopics(t *testing.T) {
	patch := map[string]any{"obs": map[string]any{"scene": "a"}, "camera": map[string]any{}}
	got := FilterTopics(patch, map[string]bool{"camera": true})
	want := map[string]any{"camera": map[string]any{}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("FilterTopics = %v, want %v", got, want)
	}
	if FilterTopics(patch, map[string]bool{"slides": true}) != nil {
		t.Errorf("non-intersecting scope should be nil")
	}
}
