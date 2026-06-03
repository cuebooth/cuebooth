// Package state holds the server's authoritative view of the live system and
// turns changes into the snapshots and deltas the WebSocket API broadcasts to
// clients.
//
// The shape of State mirrors the `state` frame in docs/protocol.md §4. The Store
// is the single source of truth: callers mutate it through Update, which assigns
// a monotonic revision and computes a sparse JSON-Merge-Patch delta (protocol.md
// §4 `state-delta`) describing exactly what changed. Command handlers update it
// optimistically; background pollers (Companion now, direct OSC in Phase 2) feed
// it through the same Update path, so new sources plug in without reworking the
// broadcast machinery.
package state

// State is the unified system state. Each top-level field is a protocol topic
// (audio, camera, obs, slides, stream) and is a pointer/map so an unpopulated
// topic is omitted from the wire entirely until a source fills it. Phase 1
// populates camera and obs (via command handling and Companion polling); audio
// arrives with direct OSC in Phase 2, slides with the sidecar in Phase 4.
type State struct {
	Audio  *AudioState        `json:"audio,omitempty"`
	Camera map[string]*Camera `json:"camera,omitempty"`
	OBS    *OBSState          `json:"obs,omitempty"`
	Slides *SlidesState       `json:"slides,omitempty"`
	Stream *StreamState       `json:"stream,omitempty"`
}

// Camera is one camera's absolute, normalized position plus its last-recalled
// preset. preset is "" (not null) when no preset is active — protocol.md §4.
type Camera struct {
	Preset string  `json:"preset"`
	Pan    float64 `json:"pan"`
	Tilt   float64 `json:"tilt"`
	Zoom   float64 `json:"zoom"`
}

// OBSState carries scene/streaming/recording status. The streaming and
// recording booleans are always present once obs exists (no omitempty) so the
// client never has to distinguish "false" from "absent".
type OBSState struct {
	Scene         string `json:"scene"`
	Streaming     bool   `json:"streaming"`
	Recording     bool   `json:"recording"`
	UptimeSeconds int    `json:"uptime_seconds,omitempty"`
}

// SlidesState is fed by the PowerPoint sidecar (Phase 4). pending_actions is
// always an array, never null.
type SlidesState struct {
	Current        int      `json:"current"`
	Total          int      `json:"total"`
	Title          string   `json:"title,omitempty"`
	PendingActions []string `json:"pending_actions"`
}

// StreamState carries streaming-platform metadata (distinct from obs.streaming;
// see protocol.md §3).
type StreamState struct {
	Platform string `json:"platform,omitempty"`
	Viewers  int    `json:"viewers,omitempty"`
}

// AudioState is populated by the direct-OSC audio engine in Phase 2.
type AudioState struct {
	Channels map[string]*AudioChannel `json:"channels,omitempty"`
	DCA      map[string]*AudioDCA     `json:"dca,omitempty"`
}

// AudioChannel is a mixer channel's modeled state.
type AudioChannel struct {
	Mute    bool    `json:"mute"`
	LevelDB float64 `json:"level_db"`
	GainDB  float64 `json:"gain_db"`
}

// AudioDCA is a DCA group's modeled state (no gain — see protocol.md §5).
type AudioDCA struct {
	Mute    bool    `json:"mute"`
	LevelDB float64 `json:"level_db"`
}

// Topics are the valid subscription topic names (protocol.md §3). They match
// the top-level JSON keys of State.
var Topics = []string{"audio", "camera", "obs", "slides", "stream"}

// normalize enforces wire invariants on the state before it is serialized for a
// snapshot or delta. Currently it guarantees slides.pending_actions is always an
// array, never null (protocol.md §4) — a nil slice would otherwise marshal to
// `null`, which the delta rules reserve for deletion. Called by Store.Update
// after every mutation, so the invariant holds regardless of how Slides is built.
func (s *State) normalize() {
	if s.Slides != nil && s.Slides.PendingActions == nil {
		s.Slides.PendingActions = []string{}
	}
}

// CameraOrNew returns the named camera, creating a zero entry (and the map) if
// it doesn't exist yet. Intended for use inside an Update mutator.
func (s *State) CameraOrNew(id string) *Camera {
	if s.Camera == nil {
		s.Camera = make(map[string]*Camera)
	}
	c, ok := s.Camera[id]
	if !ok {
		c = &Camera{}
		s.Camera[id] = c
	}
	return c
}

// OBSOrNew returns the OBS state, creating it if absent. Intended for use inside
// an Update mutator.
func (s *State) OBSOrNew() *OBSState {
	if s.OBS == nil {
		s.OBS = &OBSState{}
	}
	return s.OBS
}
