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
