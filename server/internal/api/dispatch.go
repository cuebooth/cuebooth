package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/cuebooth/cuebooth/server/internal/companion"
	"github.com/cuebooth/cuebooth/server/internal/config"
	"github.com/cuebooth/cuebooth/server/internal/state"
)

// defaultCameraID is the key for the lone camera in a single-camera deployment
// (protocol.md §3/§4): a client that omits camera_id reads and writes it.
const defaultCameraID = "main"

// cmdError carries a protocol error code and message; the connection turns it
// into a nak (protocol.md §8).
type cmdError struct {
	code    string
	message string
}

func (e *cmdError) Error() string { return e.message }

func nakErr(code, format string, args ...any) *cmdError {
	return &cmdError{code: code, message: fmt.Sprintf(format, args...)}
}

// buttonPresser is the slice of the Companion client the dispatcher needs.
// *companion.Client satisfies it; tests substitute a fake.
type buttonPresser interface {
	Press(ctx context.Context, loc companion.Location) error
}

// Dispatcher executes a client command. On success it returns a state mutator
// to apply (nil if the command changes no modeled state — an ack-with-no-delta)
// and a nil error. On rejection it returns a non-nil *cmdError (→ nak).
//
// Returning the mutator rather than applying it lets the caller enqueue the ack
// before broadcasting the resulting delta (protocol.md §4: ack precedes delta).
type Dispatcher interface {
	Dispatch(ctx context.Context, c cmdFrame) (mutate func(*state.State), err *cmdError)
}

// companionDispatcher routes the v1 command set to Companion button presses.
// Direct-OSC/VISCA/OBS paths (audio faders, velocity PTZ, etc.) land in later
// phases and are rejected with device_unavailable for now.
type companionDispatcher struct {
	cfg  *config.Config
	comp buttonPresser
}

func newCompanionDispatcher(cfg *config.Config, comp buttonPresser) *companionDispatcher {
	return &companionDispatcher{cfg: cfg, comp: comp}
}

func (d *companionDispatcher) Dispatch(ctx context.Context, c cmdFrame) (func(*state.State), *cmdError) {
	switch c.Target {
	case "camera":
		return d.camera(ctx, c)
	case "scene":
		return d.scene(ctx, c)
	case "audio":
		return d.audio(ctx, c)
	case "streaming":
		return d.streamingOrRecording(ctx, c, false)
	case "recording":
		return d.streamingOrRecording(ctx, c, true)
	case "slides":
		return d.slides(ctx, c)
	case "power", "automation":
		// Valid v1 targets with no backend until Phase 7/8.
		return nil, nakErr(codeDeviceUnavailable, "target %q has no backend in this phase", c.Target)
	default:
		return nil, nakErr(codeUnknownTarget, "unknown target %q", c.Target)
	}
}

func (d *companionDispatcher) camera(ctx context.Context, c cmdFrame) (func(*state.State), *cmdError) {
	if c.Action != "preset" {
		// position/pan_tilt/zoom need direct VISCA (Phase 3).
		return nil, nakErr(codeDeviceUnavailable, "camera action %q lands in a later phase", c.Action)
	}
	name, err := valueString(c.Value)
	if err != nil {
		return nil, nakErr(codeProtocol, "camera preset value must be a string: %v", err)
	}
	camID := c.CameraID
	if camID == "" {
		camID = defaultCameraID
	}
	act, rerr := d.cfg.ResolveCameraPreset(camID, name)
	if rerr != nil {
		return nil, resolveErr(rerr)
	}
	if cerr := d.press(ctx, act); cerr != nil {
		return nil, cerr
	}
	return func(st *state.State) { st.CameraOrNew(camID).Preset = name }, nil
}

func (d *companionDispatcher) scene(ctx context.Context, c cmdFrame) (func(*state.State), *cmdError) {
	if c.Action != "set" {
		return nil, nakErr(codeUnknownAction, "unknown scene action %q", c.Action)
	}
	name, err := valueString(c.Value)
	if err != nil {
		return nil, nakErr(codeProtocol, "scene value must be a string: %v", err)
	}
	act, rerr := d.cfg.ResolveScene(name)
	if rerr != nil {
		return nil, resolveErr(rerr)
	}
	if cerr := d.press(ctx, act); cerr != nil {
		return nil, cerr
	}
	return func(st *state.State) { st.OBSOrNew().Scene = name }, nil
}

func (d *companionDispatcher) audio(ctx context.Context, c cmdFrame) (func(*state.State), *cmdError) {
	if c.Action != "set_mute" {
		// set_fader/set_gain/apply_profile/dca_member are direct-OSC, Phase 2.
		return nil, nakErr(codeDeviceUnavailable, "audio action %q lands with direct OSC in Phase 2", c.Action)
	}
	var v struct {
		ID   string `json:"id"`
		Mute bool   `json:"mute"`
	}
	if err := json.Unmarshal(c.Value, &v); err != nil || v.ID == "" {
		return nil, nakErr(codeProtocol, "set_mute value must be {id, mute}")
	}
	var (
		act  config.Action
		rerr error
	)
	if v.Mute {
		act, rerr = d.cfg.ResolveAudioMute(v.ID)
	} else {
		act, rerr = d.cfg.ResolveAudioUnmute(v.ID)
	}
	if rerr != nil {
		if errors.Is(rerr, config.ErrUnknownPreset) {
			return nil, nakErr(codeUnknownChannel, "no mute/unmute preset for %q", v.ID)
		}
		return nil, resolveErr(rerr)
	}
	if cerr := d.press(ctx, act); cerr != nil {
		return nil, cerr
	}
	// Audio state isn't modeled until Phase 2 (direct OSC), so no delta.
	return nil, nil
}

func (d *companionDispatcher) streamingOrRecording(ctx context.Context, c cmdFrame, recording bool) (func(*state.State), *cmdError) {
	if c.Action != "start" && c.Action != "stop" {
		return nil, nakErr(codeUnknownAction, "%s action must be start or stop, got %q", c.Target, c.Action)
	}
	var (
		act  config.Action
		rerr error
	)
	if recording {
		act, rerr = d.cfg.ResolveRecording(c.Action)
	} else {
		act, rerr = d.cfg.ResolveStreaming(c.Action)
	}
	if rerr != nil {
		return nil, resolveErr(rerr)
	}
	if cerr := d.press(ctx, act); cerr != nil {
		return nil, cerr
	}
	on := c.Action == "start"
	return func(st *state.State) {
		o := st.OBSOrNew()
		if recording {
			o.Recording = on
		} else {
			o.Streaming = on
		}
	}, nil
}

func (d *companionDispatcher) slides(ctx context.Context, c cmdFrame) (func(*state.State), *cmdError) {
	switch c.Action {
	case "next", "prev":
		act, rerr := d.cfg.ResolveSlides(c.Action)
		if rerr != nil {
			return nil, resolveErr(rerr)
		}
		// Slide state is reported by the sidecar (Phase 4), not modeled here.
		return nil, d.press(ctx, act)
	case "confirm_pending", "cancel_pending":
		// No pending-action system until Phase 4; a no-op confirm/cancel is a
		// valid ack-with-no-delta (protocol.md §3 `cmd`).
		return nil, nil
	default:
		return nil, nakErr(codeUnknownAction, "unknown slides action %q", c.Action)
	}
}

// press actuates a resolved action's Companion button, or rejects it if the
// action routes to direct OSC (not available until Phase 2).
func (d *companionDispatcher) press(ctx context.Context, act config.Action) *cmdError {
	if !act.IsCompanion() {
		return nakErr(codeDeviceUnavailable, "this preset routes to direct OSC, which lands in Phase 2")
	}
	if err := d.comp.Press(ctx, *act.Button); err != nil {
		return nakErr(codeDeviceUnavailable, "companion press failed: %v", err)
	}
	return nil
}

func valueString(raw json.RawMessage) (string, error) {
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", err
	}
	return s, nil
}

func resolveErr(err error) *cmdError {
	if errors.Is(err, config.ErrUnknownPreset) {
		return nakErr(codeUnknownPreset, "%v", err)
	}
	return nakErr(codeInternal, "%v", err)
}
