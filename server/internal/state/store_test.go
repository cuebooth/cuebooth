package state

import (
	"reflect"
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

func TestScopePatch(t *testing.T) {
	patch := map[string]any{"obs": map[string]any{"scene": "a"}, "camera": map[string]any{}}
	got := scopePatch(patch, map[string]bool{"camera": true})
	want := map[string]any{"camera": map[string]any{}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("scopePatch = %v, want %v", got, want)
	}
	if scopePatch(patch, map[string]bool{"slides": true}) != nil {
		t.Errorf("non-intersecting scope should be nil")
	}
}
