package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "cuebooth.toml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("writing config: %v", err)
	}
	return path
}

func chatConfig(publicURL string) string {
	return `
[server]
listen = "127.0.0.1:7878"

[companion]
base_url = "http://localhost:8000"

[chat]
provider = "restream"
client_id = "id"
client_secret = "secret"
public_url = ` + publicURL + `
`
}

// One address is the ordinary case, so a bare string stays valid rather than
// making every deployment write a one-element array.
func TestPublicURLAcceptsASingleString(t *testing.T) {
	cfg, err := Load(writeConfig(t, chatConfig(`"http://production-pc:7878"`)))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.Chat.PublicURL; len(got) != 1 || got[0] != "http://production-pc:7878" {
		t.Fatalf("PublicURL = %v, want the one address", got)
	}
	if got, want := cfg.Chat.RedirectURI(), "http://production-pc:7878/chat/auth/callback"; got != want {
		t.Errorf("RedirectURI = %q, want %q", got, want)
	}
}

// A server on a tailnet is usually also reachable by LAN address, and on
// localhost at the production PC itself. Chat answers on all of them.
func TestPublicURLAcceptsSeveralAddresses(t *testing.T) {
	cfg, err := Load(writeConfig(t, chatConfig(
		`["http://pc.tailnet.ts.net:7878", "http://192.168.1.50:7878", "http://localhost:7878"]`)))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	wantHosts := []string{"pc.tailnet.ts.net:7878", "192.168.1.50:7878", "localhost:7878"}
	got := cfg.Chat.PublicURL.Hosts()
	if len(got) != len(wantHosts) {
		t.Fatalf("Hosts() = %v, want %v", got, wantHosts)
	}
	for i, want := range wantHosts {
		if got[i] != want {
			t.Errorf("Hosts()[%d] = %q, want %q", i, got[i], want)
		}
	}

	// The platform matches the redirect exactly, so only the first can be it.
	if got, want := cfg.Chat.RedirectURI(), "http://pc.tailnet.ts.net:7878/chat/auth/callback"; got != want {
		t.Errorf("RedirectURI = %q, want the first address, %q", got, want)
	}
}

// Hosts decides which requests chat will answer, so an entry it cannot read a
// host from must be dropped rather than contributed as an empty string — which
// would match a request that arrived without a Host of its own.
func TestHostsDropsEntriesWithNoHost(t *testing.T) {
	got := URLList{"http://pc:7878", "http://", "::not-a-url", "http://lan:7878"}.Hosts()

	want := []string{"pc:7878", "lan:7878"}
	if len(got) != len(want) {
		t.Fatalf("Hosts() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("Hosts()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
	for _, h := range got {
		if h == "" {
			t.Error("Hosts() contributed an empty host")
		}
	}
}

// Every entry decides which requests chat answers, so a broken one further down
// the list must fail loudly rather than quietly widening nothing.
func TestPublicURLValidatesEveryEntry(t *testing.T) {
	cases := []struct {
		name  string
		value string
		want  string
	}{
		{"second has no scheme", `["http://pc:7878", "192.168.1.50:7878"]`, "chat.public_url[1]"},
		{"second has a path", `["http://pc:7878", "http://lan:7878/cuebooth"]`, "chat.public_url[1]"},
		{"second is empty", `["http://pc:7878", ""]`, "chat.public_url[1]"},
		{"empty array", `[]`, "chat.public_url is required"},
		{"not a string", `[1]`, "want a string"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Load(writeConfig(t, chatConfig(tc.value)))
			if err == nil {
				t.Fatalf("Load accepted %s", tc.value)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %q, want it to name %q", err, tc.want)
			}
		})
	}
}

// A single bad value still reports without an index, because there is no list
// for the reader to look along.
func TestASinglePublicURLIsNamedWithoutAnIndex(t *testing.T) {
	_, err := Load(writeConfig(t, chatConfig(`"production-pc:7878"`)))
	if err == nil {
		t.Fatal("Load accepted an address with no scheme")
	}
	if strings.Contains(err.Error(), "[0]") {
		t.Errorf("error = %q, want no index for a single value", err)
	}
}
