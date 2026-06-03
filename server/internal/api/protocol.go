package api

import "encoding/json"

// Wire protocol constants and frame types. The normative spec is
// docs/protocol.md; this file is its Go encoding for the v1 message set.

// ProtoVersion is the on-wire protocol version advertised in the hello frame
// (protocol.md §1 "Versioning"). Major version "1" must match between client
// and server.
const ProtoVersion = "1.0"

// Message type strings (the `type` envelope field, protocol.md §2).
const (
	typeHello       = "hello"
	typeState       = "state"
	typeStateDelta  = "state-delta"
	typeAck         = "ack"
	typeNak         = "nak"
	typePong        = "pong"
	typeError       = "error"
	typeEvent       = "event"
	typeCmd         = "cmd"
	typeSubscribe   = "subscribe"
	typeUnsubscribe = "unsubscribe"
	typeGetState    = "get_state"
	typePing        = "ping"

	// Surface frames carry the Companion Satellite surface (protocol.md §10): a
	// live, server-rendered button grid the client displays natively. They sit
	// outside the state/delta machinery because button bitmaps are large and
	// change frequently (clocks, feedback) — diffing them through the state
	// store would be wasteful.
	typeSurfaceLayout = "surface-layout" // server → client
	typeSurfaceKey    = "surface-key"    // server → client
	typeSurfacePress  = "surface-press"  // client → server
)

// Error codes used in nak.error.code and error.code (protocol.md §8).
const (
	codeProtocol          = "protocol"
	codeUnknownTarget     = "unknown_target"
	codeUnknownAction     = "unknown_action"
	codeUnknownTopic      = "unknown_topic"
	codeUnknownPreset     = "unknown_preset"
	codeUnknownChannel    = "unknown_channel"
	codeInvalidTargetKind = "invalid_target_kind"
	codeDeviceUnavailable = "device_unavailable"
	codeInternal          = "internal"
)

// envelope is the minimal shape used to peek at a frame's type before decoding
// it into the concrete struct.
type envelope struct {
	Type string `json:"type"`
}

// --- client → server ---

type cmdFrame struct {
	Type     string          `json:"type"`
	ID       string          `json:"id"`
	Target   string          `json:"target"`
	Action   string          `json:"action"`
	Value    json.RawMessage `json:"value"`
	CameraID string          `json:"camera_id"`
}

type subscribeFrame struct {
	Type   string   `json:"type"`
	Topics []string `json:"topics"`
}

type pingFrame struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

// surfacePressFrame is a client tapping a surface key (protocol.md §10). pressed
// is a pointer so an omitted field is rejected rather than silently treated as a
// key-up.
type surfacePressFrame struct {
	Type    string `json:"type"`
	Key     int    `json:"key"`
	Pressed *bool  `json:"pressed"`
}

// --- server → client ---

type helloFrame struct {
	Type          string `json:"type"`
	Proto         string `json:"proto"`
	ServerVersion string `json:"server_version"`
	ServerID      string `json:"server_id"`
}

type stateFrame struct {
	Type string         `json:"type"`
	Rev  int            `json:"rev"`
	Data map[string]any `json:"-"`
}

// MarshalJSON flattens the topic data alongside type/rev (protocol.md §4: the
// topics sit at the top level of the frame, not under a sub-object).
func (s stateFrame) MarshalJSON() ([]byte, error) {
	out := map[string]any{"type": s.Type, "rev": s.Rev}
	for k, v := range s.Data {
		out[k] = v
	}
	return json.Marshal(out)
}

type stateDeltaFrame struct {
	Type  string         `json:"type"`
	Rev   int            `json:"rev"`
	Patch map[string]any `json:"patch"`
}

type ackFrame struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

type wireError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type nakFrame struct {
	Type  string    `json:"type"`
	ID    string    `json:"id"`
	Error wireError `json:"error"`
}

type pongFrame struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

type errorFrame struct {
	Type    string `json:"type"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// eventFrame is an out-of-band advisory notification (protocol.md §4 `event`).
type eventFrame struct {
	Type     string `json:"type"`
	Severity string `json:"severity"`
	Source   string `json:"source,omitempty"`
	Message  string `json:"message"`
}

// surfaceLayoutFrame announces the surface grid dimensions (protocol.md §10).
// Sent on connect and whenever the surface re-registers with Companion.
type surfaceLayoutFrame struct {
	Type       string `json:"type"`
	Rows       int    `json:"rows"`
	Cols       int    `json:"cols"`
	BitmapSize int    `json:"bitmap_size"`
}

// surfaceKeyFrame is one key's current rendered state (protocol.md §10). It is
// sent for each cached key on connect and on every Companion KEY-STATE update.
// Bitmap is base64-encoded 8-bit RGB pixel data (BitmapSize²), forwarded
// verbatim from Companion; Color is "#rrggbb". Either may be empty.
type surfaceKeyFrame struct {
	Type string `json:"type"`
	Key  int    `json:"key"`
	// Seq is a monotonically increasing surface-update sequence number. A client
	// applies updates last-write-wins per key and ignores any frame whose seq is
	// not newer than the last it applied for that key, so the initial cached
	// frame and a concurrent live update can arrive in any order safely.
	Seq     int    `json:"seq"`
	Row     int    `json:"row"`
	Col     int    `json:"col"`
	KeyType string `json:"key_type"`
	Pressed bool   `json:"pressed"`
	Color   string `json:"color,omitempty"`
	Bitmap  string `json:"bitmap,omitempty"`
}
