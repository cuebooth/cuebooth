package state

import "reflect"

// diff computes a sparse JSON-Merge-Patch (RFC 7386-style) describing how to get
// from old to new, matching the `state-delta` rules in protocol.md §4:
//   - objects are merged recursively; only changed keys appear,
//   - a key present in old but absent in new maps to null (deletion),
//   - arrays (and scalars) are replaced wholesale when they differ.
//
// Both inputs are the JSON-object forms of a State (maps with float64 numbers,
// as produced by encoding/json round-tripping). It returns nil when nothing
// changed.
func diff(old, new map[string]any) map[string]any {
	patch := map[string]any{}

	for k, nv := range new {
		ov, existed := old[k]
		if !existed {
			patch[k] = nv
			continue
		}
		nm, nIsMap := nv.(map[string]any)
		om, oIsMap := ov.(map[string]any)
		if nIsMap && oIsMap {
			if sub := diff(om, nm); len(sub) > 0 {
				patch[k] = sub
			}
			continue
		}
		if !reflect.DeepEqual(ov, nv) {
			patch[k] = nv
		}
	}

	// Keys removed in new become explicit nulls (deletions).
	for k := range old {
		if _, ok := new[k]; !ok {
			patch[k] = nil
		}
	}

	if len(patch) == 0 {
		return nil
	}
	return patch
}

// scopePatch returns the subset of a top-level patch limited to the given
// topics (protocol subscription filtering). Returns nil if nothing remains.
func scopePatch(patch map[string]any, topics map[string]bool) map[string]any {
	out := map[string]any{}
	for k, v := range patch {
		if topics[k] {
			out[k] = v
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
