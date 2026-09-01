// Package config loads and validates the cuebooth-server TOML configuration.
//
// The config maps logical action names used by the slide DSL and the Flutter
// client to concrete Companion button coordinates and/or direct OSC commands.
// See docs/design.md §3.4 (Slide Rule Format) for the design rationale and example.
package config

import (
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"strings"

	"github.com/BurntSushi/toml"
)

// Config is the root of the cuebooth.toml schema. Preset mappings nest under
// [presets.<kind>.<name>] (see presets.go). Sections for later phases (mixer,
// cameras, obs, visible-channel lists) are sketched in the example config but
// not yet decoded here; see the Undecoded handling in Load.
type Config struct {
	Server    ServerConfig    `toml:"server"`
	Companion CompanionConfig `toml:"companion"`
	// Chat configures the stream-chat panel in the client. See ChatConfig.
	Chat ChatConfig `toml:"chat"`
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
	// Satellite configures the surface CueBooth registers with Companion's
	// Satellite API so clients render Companion's own buttons. See SatelliteConfig.
	Satellite SatelliteConfig `toml:"satellite"`
}

// SatelliteConfig configures the Companion Satellite surface the server
// registers and relays to clients. Companion renders each button to a bitmap
// and pushes it over this connection; the client displays it natively, so the
// operator's button grid is whatever Companion is configured with — no
// client-side button definitions to maintain. The defaults match a Stream Deck
// XL layout (8 columns × 4 rows), which mirrors the operator's primary
// Companion page.
type SatelliteConfig struct {
	// Addr is the Companion satellite endpoint (host:port). Defaults to
	// localhost:16622. Set to "off" (or "disabled") to disable the surface.
	Addr string `toml:"addr"`
	// DeviceID is the stable surface identifier Companion keys per-surface
	// settings (assigned page, etc.) by. Defaults to "cuebooth".
	DeviceID string `toml:"device_id"`
	// Rows and Cols are the surface key grid dimensions (default 4 × 8).
	Rows int `toml:"rows"`
	Cols int `toml:"cols"`
	// BitmapSize is the button bitmap edge length in pixels, square (default 72).
	BitmapSize int `toml:"bitmap_size"`
}

// ChatConfig configures the stream-chat panel the client renders.
//
// The server, not the client, holds the platform credential: Restream's OAuth
// has no PKCE and its token exchange needs a client secret, which an app
// distributed to operators cannot keep. See internal/chat.
type ChatConfig struct {
	// Provider names the chat platform. "restream" is the only one implemented;
	// "off" (or empty) disables the panel.
	Provider string `toml:"provider"`
	// ClientID and ClientSecret identify the application registered at
	// developers.restream.io. The secret may be supplied through
	// CUEBOOTH_CHAT_CLIENT_SECRET instead, keeping it out of the config file.
	ClientID     string `toml:"client_id"`
	ClientSecret string `toml:"client_secret"`
	// PublicURL is the address operators reach this server at. The OAuth
	// redirect is derived from it so the two cannot drift apart; the result must
	// match a redirect URI registered on the Restream application.
	PublicURL string `toml:"public_url"`
	// TokenFile is where the rotating credential is stored. Restream invalidates
	// the previous token on every refresh, so this file is state: deleting it
	// costs a re-authorization.
	TokenFile string `toml:"token_file"`
}

// ChatSecretEnv supplies chat.client_secret when the config file omits it.
const ChatSecretEnv = "CUEBOOTH_CHAT_CLIENT_SECRET"

// chatCallbackPath is the route Restream redirects back to after an operator
// authorizes. It must match the handler registered in internal/api.
const chatCallbackPath = "/chat/auth/callback"

// Disabled reports whether the chat panel is turned off by config.
func (c ChatConfig) Disabled() bool {
	switch strings.ToLower(strings.TrimSpace(c.Provider)) {
	case "", "off", "disabled", "none":
		return true
	default:
		return false
	}
}

// RedirectURI is the OAuth callback registered with the chat platform.
func (c ChatConfig) RedirectURI() string {
	return strings.TrimRight(c.PublicURL, "/") + chatCallbackPath
}

// maxSatelliteKeys bounds how many keys a surface may have. A Companion page is
// 32 buttons and the largest rig anyone drives is a few Stream Deck XLs side by
// side; past a couple of hundred an operator cannot find the button they want
// under stage lighting, and the server caches and queues a bitmap for each one.
const maxSatelliteKeys = 256

// maxSatelliteBitmapSize bounds the button bitmap edge length. Companion renders
// at whatever size we ask for rather than clamping to a physical device's, so
// this bound is ours alone: a key frame carries bitmap_size² × 3 bytes of pixel
// data, squaring into the per-client buffer that maxSatelliteKeys multiplies.
// 256 clears the 120px of the largest physical surface.
const maxSatelliteBitmapSize = 256

// Disabled reports whether the satellite surface is turned off by config.
func (s SatelliteConfig) Disabled() bool {
	switch strings.ToLower(strings.TrimSpace(s.Addr)) {
	case "off", "disabled", "none":
		return true
	default:
		return false
	}
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

	// Resolved before validation so an operator who keeps the secret in the
	// environment isn't rejected for omitting it from the file.
	if cfg.Chat.ClientSecret == "" {
		cfg.Chat.ClientSecret = os.Getenv(ChatSecretEnv)
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
	"chat":      true,
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
		Server: ServerConfig{Listen: "0.0.0.0:7878"},
		Chat:   ChatConfig{TokenFile: "chat-token.json"},
		Companion: CompanionConfig{
			BaseURL: "http://localhost:8000",
			Satellite: SatelliteConfig{
				Addr:       "localhost:16622",
				DeviceID:   "cuebooth",
				Rows:       4,
				Cols:       8,
				BitmapSize: 72,
			},
		},
	}
}

func (c *Config) validate() error {
	if c.Server.Listen == "" {
		return fmt.Errorf("server.listen is required")
	}
	if c.Companion.BaseURL == "" {
		return fmt.Errorf("companion.base_url is required")
	}
	if err := c.Companion.Satellite.validate(); err != nil {
		return err
	}
	if err := c.Chat.validate(); err != nil {
		return err
	}
	if err := c.Presets.validate(); err != nil {
		return err
	}
	return nil
}

func (c ChatConfig) validate() error {
	if c.Disabled() {
		return nil
	}
	if strings.ToLower(strings.TrimSpace(c.Provider)) != "restream" {
		return fmt.Errorf("chat.provider %q is not supported (use \"restream\" or \"off\")", c.Provider)
	}
	if c.ClientID == "" {
		return fmt.Errorf("chat.client_id is required when chat is enabled")
	}
	if c.ClientSecret == "" {
		return fmt.Errorf("chat.client_secret is required when chat is enabled (or set %s)", ChatSecretEnv)
	}
	if c.PublicURL == "" {
		return fmt.Errorf("chat.public_url is required when chat is enabled")
	}
	// The redirect is built from public_url and must match one registered on the
	// Restream application, so a value that can't form an absolute URL would only
	// fail later, at the point an operator is standing in front of a browser.
	u, err := url.Parse(strings.TrimRight(c.PublicURL, "/"))
	if err != nil {
		return fmt.Errorf("chat.public_url is not a valid URL: %w", err)
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("chat.public_url must be an absolute http:// or https:// URL, e.g. http://production-pc:7878")
	}
	return nil
}

func (s SatelliteConfig) validate() error {
	if s.Disabled() {
		return nil
	}
	// device_id is emitted as an unquoted token in the satellite line protocol
	// (DEVICEID=<id>), so whitespace or '='/'"' would produce a malformed line
	// and Companion would silently not register the surface.
	if strings.ContainsAny(s.DeviceID, " \t\r\n\"=") {
		return fmt.Errorf("companion.satellite.device_id must not contain whitespace, '=', or '\"'")
	}
	// The grid size drives what we advertise to Companion and how much the
	// server buffers per client, so an implausible value (a typo'd extra digit)
	// is rejected rather than turned into a huge allocation and a Companion
	// asked to render that many bitmaps.
	if s.Rows < 0 || s.Cols < 0 {
		return fmt.Errorf("companion.satellite.rows and companion.satellite.cols must not be negative")
	}
	// Each dimension is checked before their product so a pair large enough to
	// overflow the multiplication cannot wrap into a small, passing key count.
	if s.Rows > maxSatelliteKeys || s.Cols > maxSatelliteKeys || s.Rows*s.Cols > maxSatelliteKeys {
		return fmt.Errorf("companion.satellite.rows × companion.satellite.cols must be at most %d keys", maxSatelliteKeys)
	}
	// Companion silently coerces a BITMAPS size below 5 to its 72px default, so a
	// 1–4 value would make us advertise the wrong dimensions to clients (corrupt
	// renders). Reject it; 0 selects the default.
	if s.BitmapSize < 0 || (s.BitmapSize > 0 && s.BitmapSize < 5) {
		return fmt.Errorf("companion.satellite.bitmap_size must be 0 (default) or >= 5")
	}
	if s.BitmapSize > maxSatelliteBitmapSize {
		return fmt.Errorf("companion.satellite.bitmap_size must be at most %d", maxSatelliteBitmapSize)
	}
	return nil
}
