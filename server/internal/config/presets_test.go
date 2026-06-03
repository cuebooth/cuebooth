package config

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/BurntSushi/toml"
)

// sampleConfig mirrors docs/sample-deployment.md §6 (the worked example). It
// exercises every preset shape: camera/scene/audio, Companion-button routing,
// and direct-OSC routing with integer osc_value literals.
const sampleConfig = `
[server]
listen = "0.0.0.0:7878"

[companion]
base_url = "http://localhost:8000"

# Forward-compat sections that aren't decoded yet — must not break loading.
[mixer]
host = "10.0.0.50"
port = 10024

[cameras.main]
host = "10.0.1.10"
visca_port = 1259

[obs]
host = "127.0.0.1"
port = 4455

[presets.camera.main.podium]
companion_button = "1/1/1"

[presets.camera.main.choir]
companion_button = "1/3/2"

[presets.scene.camera-with-slides]
companion_button = "7/0/2"

[presets.audio.mute.non-choir]
companion_button = "4/1/1"

[presets.audio.unmute.podium]
osc_command = "/ch/04/mix/on"
osc_value = 1

[presets.audio.mute.podium]
osc_command = "/ch/04/mix/on"
osc_value = 0

[presets.streaming.start]
companion_button = "8/0/0"

[presets.streaming.stop]
companion_button = "8/0/1"

[presets.recording.start]
companion_button = "8/1/0"

[presets.slides.next]
companion_button = "7/3/6"

[presets.slides.prev]
companion_button = "7/3/5"

[[audio.visible]]
id = "podium"
label = "Podium"
osc_channel = "/ch/04"
`

func loadString(t *testing.T, s string) (*Config, error) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "cuebooth.toml")
	if err := os.WriteFile(path, []byte(s), 0o600); err != nil {
		t.Fatalf("write temp config: %v", err)
	}
	return Load(path)
}

func TestLoadSampleDeployment(t *testing.T) {
	cfg, err := loadString(t, sampleConfig)
	if err != nil {
		t.Fatalf("Load sample config: %v", err)
	}

	// Companion-button camera preset.
	act, err := cfg.ResolveCameraPreset("main", "choir")
	if err != nil {
		t.Fatalf("ResolveCameraPreset(main, choir): %v", err)
	}
	if !act.IsCompanion() || act.Button.String() != "1/3/2" {
		t.Errorf("choir = %+v, want button 1/3/2", act)
	}

	// Scene preset.
	act, err = cfg.ResolveScene("camera-with-slides")
	if err != nil {
		t.Fatalf("ResolveScene: %v", err)
	}
	if !act.IsCompanion() || act.Button.String() != "7/0/2" {
		t.Errorf("scene = %+v, want button 7/0/2", act)
	}

	// Companion-button audio mute.
	act, err = cfg.ResolveAudioMute("non-choir")
	if err != nil {
		t.Fatalf("ResolveAudioMute(non-choir): %v", err)
	}
	if !act.IsCompanion() || act.Button.String() != "4/1/1" {
		t.Errorf("mute non-choir = %+v, want button 4/1/1", act)
	}

	// Direct-OSC audio unmute with integer osc_value=1.
	act, err = cfg.ResolveAudioUnmute("podium")
	if err != nil {
		t.Fatalf("ResolveAudioUnmute(podium): %v", err)
	}
	if act.IsCompanion() {
		t.Errorf("unmute podium should be OSC-routed, got %+v", act)
	}
	if act.OSCCommand != "/ch/04/mix/on" || act.OSCValue != 1 {
		t.Errorf("unmute podium = %+v, want /ch/04/mix/on = 1", act)
	}

	// Direct-OSC audio mute with integer osc_value=0.
	act, err = cfg.ResolveAudioMute("podium")
	if err != nil {
		t.Fatalf("ResolveAudioMute(podium): %v", err)
	}
	if act.OSCValue != 0 || act.OSCCommand != "/ch/04/mix/on" {
		t.Errorf("mute podium = %+v, want /ch/04/mix/on = 0", act)
	}

	// Verb-keyed presets: streaming / recording / slides.
	verbCases := []struct {
		name   string
		fn     func() (Action, error)
		button string
	}{
		{"streaming start", func() (Action, error) { return cfg.ResolveStreaming("start") }, "8/0/0"},
		{"streaming stop", func() (Action, error) { return cfg.ResolveStreaming("stop") }, "8/0/1"},
		{"recording start", func() (Action, error) { return cfg.ResolveRecording("start") }, "8/1/0"},
		{"slides next", func() (Action, error) { return cfg.ResolveSlides("next") }, "7/3/6"},
		{"slides prev", func() (Action, error) { return cfg.ResolveSlides("prev") }, "7/3/5"},
	}
	for _, vc := range verbCases {
		got, err := vc.fn()
		if err != nil {
			t.Errorf("%s: %v", vc.name, err)
			continue
		}
		if !got.IsCompanion() || got.Button.String() != vc.button {
			t.Errorf("%s = %+v, want button %s", vc.name, got, vc.button)
		}
	}
}

func TestInvalidVerbPreset(t *testing.T) {
	base := "[server]\nlisten = \"x:1\"\n[companion]\nbase_url = \"http://localhost:8000\"\n"
	// "pause" is not a valid streaming verb.
	frag := "[presets.streaming.pause]\ncompanion_button = \"8/0/2\"\n"
	if _, err := loadString(t, base+frag); err == nil {
		t.Error("expected error for unknown streaming verb")
	}
}

func TestResolveUnknownPreset(t *testing.T) {
	cfg, err := loadString(t, sampleConfig)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	cases := []func() error{
		func() error { _, e := cfg.ResolveCameraPreset("main", "nope"); return e },
		func() error { _, e := cfg.ResolveCameraPreset("front", "choir"); return e }, // unknown camera
		func() error { _, e := cfg.ResolveScene("nope"); return e },
		func() error { _, e := cfg.ResolveAudioMute("nope"); return e },
		func() error { _, e := cfg.ResolveAudioUnmute("nope"); return e },
	}
	for i, fn := range cases {
		if err := fn(); !errors.Is(err, ErrUnknownPreset) {
			t.Errorf("case %d: err = %v, want ErrUnknownPreset", i, err)
		}
	}
}

func TestActionRefValidation(t *testing.T) {
	bad := map[string]string{
		"both routes": `
[presets.scene.x]
companion_button = "1/0/0"
osc_command = "/foo"
osc_value = 1`,
		"neither route": `
[presets.scene.x]
`,
		"bad coordinate": `
[presets.scene.x]
companion_button = "1/0"`,
		"osc without value": `
[presets.audio.mute.x]
osc_command = "/foo"`,
		"osc not an address": `
[presets.audio.mute.x]
osc_command = "ch01"
osc_value = 0`,
		"button with osc_value": `
[presets.scene.x]
companion_button = "1/0/0"
osc_value = 1`,
	}
	base := "[server]\nlisten = \"0.0.0.0:7878\"\n[companion]\nbase_url = \"http://localhost:8000\"\n"
	for name, frag := range bad {
		t.Run(name, func(t *testing.T) {
			if _, err := loadString(t, base+frag); err == nil {
				t.Errorf("expected validation error for %q", name)
			}
		})
	}
}

func TestOSCValueFloat(t *testing.T) {
	var ref ActionRef
	if _, err := toml.Decode(`osc_command = "/lr/mix/fader"`+"\n"+`osc_value = 0.75`, &ref); err != nil {
		t.Fatalf("decode float osc_value: %v", err)
	}
	if !ref.OSCValue.Set || ref.OSCValue.Value != 0.75 {
		t.Errorf("osc_value = %+v, want 0.75 set", ref.OSCValue)
	}
}

// TestExampleConfigLoads guards the shipped example config against drift: it
// must always load cleanly.
func TestExampleConfigLoads(t *testing.T) {
	path := filepath.Join("..", "..", "configs", "cuebooth.example.toml")
	if _, err := os.Stat(path); err != nil {
		t.Skipf("example config not found: %v", err)
	}
	if _, err := Load(path); err != nil {
		t.Fatalf("example config failed to load: %v", err)
	}
}
