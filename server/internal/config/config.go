// Package config loads and validates the cuebooth-server TOML configuration.
//
// The config maps logical action names used by the slide DSL and the Flutter
// client to concrete Companion button coordinates and/or direct OSC commands.
// See docs/design.md §3.4 (Slide Rule Format) for the design rationale and example.
package config

import (
	"fmt"
	"log/slog"
	"os"

	"github.com/BurntSushi/toml"
)

// Config is the root of the cuebooth.toml schema. Preset mappings nest under
// [presets.<kind>.<name>] (see presets.go). Sections for later phases (mixer,
// cameras, obs, visible-channel lists) are sketched in the example config but
// not yet decoded here; see the Undecoded handling in Load.
type Config struct {
	Server    ServerConfig    `toml:"server"`
	Companion CompanionConfig `toml:"companion"`
	// Presets maps logical action names (used in slide rules and by the client)
	// to Companion buttons or direct OSC commands. See presets.go.
	Presets PresetsConfig `toml:"presets"`
}

// ServerConfig holds settings for the WebSocket API the Flutter client
// connects to.
type ServerConfig struct {
	// Listen is the host:port the WebSocket API binds to. Use 0.0.0.0:port
	// to accept connections from any interface (LAN + Tailscale).
	Listen string `toml:"listen"`
}

// CompanionConfig holds the connection details for the Bitfocus Companion
// HTTP API the server delegates most hardware control to.
type CompanionConfig struct {
	// BaseURL is the root URL of the Companion HTTP API, typically
	// http://localhost:8000 when Companion runs on the same PC.
	BaseURL string `toml:"base_url"`
}

// Load reads, parses, and validates the configuration file at path.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}

	cfg := defaults()
	md, err := toml.Decode(string(data), cfg)
	if err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}

	// Warn (rather than reject) on unrecognized keys so a typo like "listenn"
	// surfaces instead of silently falling back to the default. We only warn
	// for keys we'd expect to decode — top-level scalars and keys inside tables
	// we actually map (see decodedTables). The example and deployment configs
	// advertise forthcoming sections ([mixer], [cameras.*], [obs], [[audio.visible]])
	// that aren't wired into the struct yet; warning on those would flood a
	// docs-following operator with noise on every startup.
	for _, key := range md.Undecoded() {
		if !warnableKey(key) {
			continue
		}
		slog.Warn("ignoring unknown config key", "path", path, "key", key.String())
	}

	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("invalid config %s: %w", path, err)
	}

	return cfg, nil
}

// decodedTables are the top-level [tables] currently mapped into Config.
// Keep this in sync as new sections are wired up; warnableKey uses it to
// suppress warnings for not-yet-implemented sections.
var decodedTables = map[string]bool{
	"server":    true,
	"companion": true,
	"presets":   true,
}

// warnableKey reports whether an undecoded TOML key is worth warning about:
// top-level scalars (e.g. a misspelled root key) and keys nested under a table
// we actually decode (e.g. [server] listenn). Keys under tables we don't decode
// yet are expected forward-compat sections, so they're ignored silently.
func warnableKey(key toml.Key) bool {
	if len(key) <= 1 {
		return true
	}
	return decodedTables[key[0]]
}

func defaults() *Config {
	return &Config{
		Server:    ServerConfig{Listen: "0.0.0.0:7878"},
		Companion: CompanionConfig{BaseURL: "http://localhost:8000"},
	}
}

func (c *Config) validate() error {
	if c.Server.Listen == "" {
		return fmt.Errorf("server.listen is required")
	}
	if c.Companion.BaseURL == "" {
		return fmt.Errorf("companion.base_url is required")
	}
	if err := c.Presets.validate(); err != nil {
		return err
	}
	return nil
}
