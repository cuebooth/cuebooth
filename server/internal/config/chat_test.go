package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestChatConfigValidate(t *testing.T) {
	complete := func(mutate func(*ChatConfig)) ChatConfig {
		c := ChatConfig{
			Provider:     "restream",
			ClientID:     "id",
			ClientSecret: "secret",
			PublicURL:    "http://production-pc:7878",
		}
		if mutate != nil {
			mutate(&c)
		}
		return c
	}

	cases := []struct {
		name string
		cfg  ChatConfig
		ok   bool
	}{
		{"disabled by default", ChatConfig{}, true},
		{"explicitly off", ChatConfig{Provider: "off"}, true},
		// Disabled means disabled: half-filled credentials on an off provider
		// are a config an operator is mid-way through writing, not an error.
		{"off with leftover fields", ChatConfig{Provider: "none", ClientID: "id"}, true},
		{"complete", complete(nil), true},
		{"https public url", complete(func(c *ChatConfig) { c.PublicURL = "https://cuebooth.example" }), true},
		{"trailing slash tolerated", complete(func(c *ChatConfig) { c.PublicURL = "http://pc:7878/" }), true},

		{"unknown provider", complete(func(c *ChatConfig) { c.Provider = "youtube" }), false},
		{"no client id", complete(func(c *ChatConfig) { c.ClientID = "" }), false},
		{"no client secret", complete(func(c *ChatConfig) { c.ClientSecret = "" }), false},
		{"no public url", complete(func(c *ChatConfig) { c.PublicURL = "" }), false},
		// A bare host has no scheme, so the redirect built from it would not be
		// an absolute URL and Restream would reject the exchange.
		{"public url without a scheme", complete(func(c *ChatConfig) { c.PublicURL = "production-pc:7878" }), false},
		{"public url with the wrong scheme", complete(func(c *ChatConfig) { c.PublicURL = "ws://pc:7878" }), false},
		{"public url with no host", complete(func(c *ChatConfig) { c.PublicURL = "http://" }), false},
		// The callback route is appended to this, so a path would send the
		// platform's redirect somewhere the server does not serve.
		{"public url with a path", complete(func(c *ChatConfig) { c.PublicURL = "http://pc:7878/cuebooth" }), false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.cfg.validate()
			if tc.ok && err != nil {
				t.Errorf("validate() = %v, want nil", err)
			}
			if !tc.ok && err == nil {
				t.Error("validate() = nil, want an error")
			}
		})
	}
}

func TestChatConfigDisabled(t *testing.T) {
	for _, provider := range []string{"", "off", "OFF", " none ", "disabled"} {
		if !(ChatConfig{Provider: provider}).Disabled() {
			t.Errorf("provider %q should disable chat", provider)
		}
	}
	if (ChatConfig{Provider: "restream"}).Disabled() {
		t.Error("restream should not be treated as disabled")
	}
}

// The redirect is derived rather than configured so it cannot drift from the
// route that actually serves it.
func TestChatRedirectURI(t *testing.T) {
	cases := []struct{ public, want string }{
		{"http://production-pc:7878", "http://production-pc:7878/chat/auth/callback"},
		{"http://production-pc:7878/", "http://production-pc:7878/chat/auth/callback"},
		{"https://cuebooth.example", "https://cuebooth.example/chat/auth/callback"},
	}
	for _, tc := range cases {
		got := ChatConfig{PublicURL: tc.public}.RedirectURI()
		if got != tc.want {
			t.Errorf("RedirectURI(%q) = %q, want %q", tc.public, got, tc.want)
		}
	}
}

// The secret is the one credential an operator may reasonably want out of the
// config file, so the environment has to be able to supply it.
func TestChatSecretFromEnvironment(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cuebooth.toml")
	body := `
[server]
listen = "127.0.0.1:7878"

[companion]
base_url = "http://localhost:8000"

[chat]
provider = "restream"
client_id = "id"
public_url = "http://production-pc:7878"
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	if _, err := Load(path); err == nil {
		t.Fatal("config without a secret loaded; nothing would have caught the omission")
	}

	t.Setenv(ChatSecretEnv, "secret-from-env")
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load with %s set: %v", ChatSecretEnv, err)
	}
	if cfg.Chat.ClientSecret != "secret-from-env" {
		t.Errorf("client secret = %q, want the environment's value", cfg.Chat.ClientSecret)
	}
}

// An SCM-launched service runs with cwd C:\Windows\System32, so a token_file
// resolved against the working directory would put the credential somewhere no
// operator backs up — and, on Windows, somewhere world-readable.
func TestChatTokenFileResolvesAgainstTheConfigDirectory(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cuebooth.toml")
	body := `
[server]
listen = "127.0.0.1:7878"

[companion]
base_url = "http://localhost:8000"

[chat]
provider = "restream"
client_id = "id"
client_secret = "secret"
public_url = "http://production-pc:7878"
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := filepath.Join(dir, "chat-token.json")
	if cfg.Chat.TokenFile != want {
		t.Errorf("token file = %q, want %q", cfg.Chat.TokenFile, want)
	}
}

// An absolute path is the operator's explicit choice and must be left alone.
// The path goes in a TOML literal string: a Windows temp directory contains
// backslashes, which a basic string would read as escape sequences.
func TestChatTokenFileAbsolutePathIsUnchanged(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cuebooth.toml")
	absolute := filepath.Join(t.TempDir(), "elsewhere", "token.json")
	body := `
[server]
listen = "127.0.0.1:7878"

[companion]
base_url = "http://localhost:8000"

[chat]
provider = "restream"
client_id = "id"
client_secret = "secret"
public_url = "http://production-pc:7878"
token_file = '` + absolute + `'
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Chat.TokenFile != absolute {
		t.Errorf("token file = %q, want the configured %q", cfg.Chat.TokenFile, absolute)
	}
}

// A secret in the file wins, so an operator who set both is not surprised by a
// stale environment variable from another deployment.
func TestChatSecretInFileWinsOverEnvironment(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cuebooth.toml")
	body := `
[server]
listen = "127.0.0.1:7878"

[companion]
base_url = "http://localhost:8000"

[chat]
provider = "restream"
client_id = "id"
client_secret = "secret-from-file"
public_url = "http://production-pc:7878"
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	t.Setenv(ChatSecretEnv, "secret-from-env")

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Chat.ClientSecret != "secret-from-file" {
		t.Errorf("client secret = %q, want the file's value", cfg.Chat.ClientSecret)
	}
}
