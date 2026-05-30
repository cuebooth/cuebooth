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

// Config is the root of the cuebooth.toml schema. Sections are intentionally
// flat at the top level; preset mappings nest under [presets.<kind>.<name>]
// and are loaded lazily as Phase 1+ work lands.
type Config struct {
	Server    ServerConfig    `toml:"server"`
	Companion CompanionConfig `toml:"companion"`
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

	// Warn (rather than reject) on keys we don't recognize: a typo like
	// "listenn" would otherwise silently fall back to the default. We don't
	// hard-fail because the example config advertises forthcoming sections
	// ([mixer], [obs], [presets.*], ...) that aren't wired into the struct yet.
	for _, key := range md.Undecoded() {
		slog.Warn("ignoring unknown config key", "path", path, "key", key.String())
	}

	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("invalid config %s: %w", path, err)
	}

	return cfg, nil
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
	return nil
}
