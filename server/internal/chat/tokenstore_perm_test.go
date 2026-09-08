package chat

import (
	"os"
	"path/filepath"
	"testing"
)

// The refresh token is a year-long credential for the operator's chat account,
// and the file holding it is 0600. That protects its contents; it says nothing
// about who can list the directory or replace the file wholesale.
//
// MkdirAll leaves an existing directory's mode alone, and the default path puts
// the token beside the config file — a directory the installer created, not the
// server. So the mode the server asks for was, in the common case, never
// applied to anything.
func TestSaveNarrowsAnExistingTokenDirectory(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("running as root; directory modes do not constrain this process")
	}

	dir := filepath.Join(t.TempDir(), "config")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("seeding the directory: %v", err)
	}
	// Guard against a umask that already narrowed it, or this proves nothing.
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatalf("chmod: %v", err)
	}

	store := &tokenStore{path: filepath.Join(dir, "chat-token.json")}
	if err := store.save(tokens{AccessToken: "a", RefreshToken: "r"}); err != nil {
		t.Fatalf("save: %v", err)
	}

	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o700 {
		t.Errorf("directory mode = %#o after save, want 0700 — anyone on the box can list "+
			"and replace the credential", got)
	}

	file, err := os.Stat(store.path)
	if err != nil {
		t.Fatalf("stat token file: %v", err)
	}
	if got := file.Mode().Perm(); got != 0o600 {
		t.Errorf("token file mode = %#o, want 0600", got)
	}
}
