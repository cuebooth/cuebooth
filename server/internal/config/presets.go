package config

import (
	"errors"
	"fmt"
	"slices"

	"github.com/cuebooth/cuebooth/server/internal/companion"
)

// ErrUnknownPreset is returned by the Resolve* methods when a referenced preset
// name is not defined in the config. Callers distinguish it (e.g. to emit an
// `unknown_preset` nak per protocol.md §8) with errors.Is.
var ErrUnknownPreset = errors.New("unknown preset")

// PresetsConfig maps the logical preset names used in slide rules and by the
// client to concrete actions — either a Companion button to press or a direct
// OSC command. See docs/design.md §3.4 and docs/sample-deployment.md §6.
type PresetsConfig struct {
	// Camera presets are namespaced by camera id: Camera[<camera-id>][<name>].
	// Matches [presets.camera.<id>.<name>] in TOML.
	Camera map[string]map[string]ActionRef `toml:"camera"`
	// Scene presets recall an OBS scene. Matches [presets.scene.<name>].
	Scene map[string]ActionRef `toml:"scene"`
	// Audio holds mute/unmute presets, keyed by channel-or-DCA name. Matches
	// [presets.audio.mute.<name>] and [presets.audio.unmute.<name>].
	Audio AudioPresets `toml:"audio"`
	// Streaming maps the OBS stream start/stop verbs to Companion buttons.
	// Keys: "start", "stop". Matches [presets.streaming.start|stop].
	Streaming map[string]ActionRef `toml:"streaming"`
	// Recording maps the OBS recording start/stop verbs. Keys: "start", "stop".
	Recording map[string]ActionRef `toml:"recording"`
	// Slides maps slide advance/retreat verbs. Keys: "next", "prev". (Phase 1
	// drives slides through Companion; Phase 4+ adds the sidecar + HID paths.)
	Slides map[string]ActionRef `toml:"slides"`
}

// AudioPresets groups the mute and unmute preset tables.
type AudioPresets struct {
	Mute   map[string]ActionRef `toml:"mute"`
	Unmute map[string]ActionRef `toml:"unmute"`
}

// ActionRef is a single preset entry. Exactly one routing path must be set: a
// Companion button coordinate, or a direct OSC command (with its value). The
// two are mutually exclusive.
type ActionRef struct {
	// CompanionButton is a "page/row/column" coordinate (e.g. "7/0/2").
	CompanionButton string `toml:"companion_button"`
	// OSCCommand is an OSC address (e.g. "/ch/01/mix/on"). When set, OSCValue
	// must also be set.
	OSCCommand string   `toml:"osc_command"`
	OSCValue   OSCValue `toml:"osc_value"`
}

// Action is a resolved preset: a concrete thing to do. Exactly one of Button or
// OSCCommand is populated, mirroring the ActionRef it came from.
type Action struct {
	// Button is the Companion button to press, when Companion-routed.
	Button *companion.Location
	// OSCCommand and OSCValue are populated when OSC-routed.
	OSCCommand string
	OSCValue   float64
}

// IsCompanion reports whether the action presses a Companion button (vs. direct OSC).
func (a Action) IsCompanion() bool { return a.Button != nil }

// OSCValue is a TOML number (integer or float) for an OSC argument. It exists
// so config authors can write `osc_value = 0` (an integer literal, as in the
// sample deployment) rather than being forced to `0.0`; both decode here.
type OSCValue struct {
	Value float64
	Set   bool
}

// UnmarshalTOML accepts either a TOML integer or float for an OSC value.
func (v *OSCValue) UnmarshalTOML(data any) error {
	switch n := data.(type) {
	case int64:
		v.Value = float64(n)
	case float64:
		v.Value = n
	default:
		return fmt.Errorf("osc_value must be a number, got %T", data)
	}
	v.Set = true
	return nil
}

// validate checks every preset entry for a well-formed, unambiguous routing.
func (p *PresetsConfig) validate() error {
	for cam, names := range p.Camera {
		for name, ref := range names {
			if err := ref.validate(); err != nil {
				return fmt.Errorf("[presets.camera.%s.%s]: %w", cam, name, err)
			}
		}
	}
	for name, ref := range p.Scene {
		if err := ref.validate(); err != nil {
			return fmt.Errorf("[presets.scene.%s]: %w", name, err)
		}
	}
	for name, ref := range p.Audio.Mute {
		if err := ref.validate(); err != nil {
			return fmt.Errorf("[presets.audio.mute.%s]: %w", name, err)
		}
	}
	for name, ref := range p.Audio.Unmute {
		if err := ref.validate(); err != nil {
			return fmt.Errorf("[presets.audio.unmute.%s]: %w", name, err)
		}
	}
	for _, vp := range []struct {
		section string
		m       map[string]ActionRef
		allowed []string
	}{
		{"streaming", p.Streaming, []string{"start", "stop"}},
		{"recording", p.Recording, []string{"start", "stop"}},
		{"slides", p.Slides, []string{"next", "prev"}},
	} {
		for name, ref := range vp.m {
			if !slices.Contains(vp.allowed, name) {
				return fmt.Errorf("[presets.%s.%s]: unknown verb (allowed: %v)", vp.section, name, vp.allowed)
			}
			if err := ref.validate(); err != nil {
				return fmt.Errorf("[presets.%s.%s]: %w", vp.section, name, err)
			}
		}
	}
	return nil
}

func (r ActionRef) validate() error {
	hasButton := r.CompanionButton != ""
	hasOSC := r.OSCCommand != ""
	switch {
	case hasButton && hasOSC:
		return errors.New("set either companion_button or osc_command, not both")
	case !hasButton && !hasOSC:
		return errors.New("must set companion_button or osc_command")
	case hasButton:
		if _, err := companion.ParseLocation(r.CompanionButton); err != nil {
			return err
		}
	case hasOSC:
		if !r.OSCValue.Set {
			return errors.New("osc_command requires osc_value")
		}
	}
	return nil
}

// resolve turns a validated ActionRef into a concrete Action.
func (r ActionRef) resolve() (Action, error) {
	if r.CompanionButton != "" {
		loc, err := companion.ParseLocation(r.CompanionButton)
		if err != nil {
			return Action{}, err
		}
		return Action{Button: &loc}, nil
	}
	return Action{OSCCommand: r.OSCCommand, OSCValue: r.OSCValue.Value}, nil
}

// ResolveCameraPreset resolves a camera preset by camera id and preset name.
func (c *Config) ResolveCameraPreset(cameraID, name string) (Action, error) {
	names, ok := c.Presets.Camera[cameraID]
	if !ok {
		return Action{}, fmt.Errorf("%w: camera %q has no presets", ErrUnknownPreset, cameraID)
	}
	ref, ok := names[name]
	if !ok {
		return Action{}, fmt.Errorf("%w: camera %q preset %q", ErrUnknownPreset, cameraID, name)
	}
	return ref.resolve()
}

// ResolveScene resolves an OBS scene preset by name.
func (c *Config) ResolveScene(name string) (Action, error) {
	return resolveNamed(c.Presets.Scene, "scene", name)
}

// ResolveAudioMute resolves a mute preset by channel-or-DCA name.
func (c *Config) ResolveAudioMute(name string) (Action, error) {
	return resolveNamed(c.Presets.Audio.Mute, "audio mute", name)
}

// ResolveAudioUnmute resolves an unmute preset by channel-or-DCA name.
func (c *Config) ResolveAudioUnmute(name string) (Action, error) {
	return resolveNamed(c.Presets.Audio.Unmute, "audio unmute", name)
}

// ResolveStreaming resolves a stream verb ("start" or "stop").
func (c *Config) ResolveStreaming(verb string) (Action, error) {
	return resolveNamed(c.Presets.Streaming, "streaming", verb)
}

// ResolveRecording resolves a recording verb ("start" or "stop").
func (c *Config) ResolveRecording(verb string) (Action, error) {
	return resolveNamed(c.Presets.Recording, "recording", verb)
}

// ResolveSlides resolves a slide verb ("next" or "prev").
func (c *Config) ResolveSlides(verb string) (Action, error) {
	return resolveNamed(c.Presets.Slides, "slides", verb)
}

// resolveNamed looks up a preset by name in a flat preset map.
func resolveNamed(m map[string]ActionRef, kind, name string) (Action, error) {
	ref, ok := m[name]
	if !ok {
		return Action{}, fmt.Errorf("%w: %s %q", ErrUnknownPreset, kind, name)
	}
	return ref.resolve()
}
