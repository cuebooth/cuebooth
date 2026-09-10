package chat

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestTokenStoreRoundTrips(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token.json")
	store := &tokenStore{path: path}

	want := tokens{
		AccessToken:   "access-1",
		RefreshToken:  "refresh-1",
		AccessExpiry:  time.Date(2026, 9, 1, 13, 0, 0, 0, time.UTC),
		RefreshExpiry: time.Date(2027, 9, 1, 12, 0, 0, 0, time.UTC),
	}
	if err := store.save(want); err != nil {
		t.Fatalf("save: %v", err)
	}

	got, err := store.load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got != want {
		t.Errorf("loaded %+v, want %+v", got, want)
	}
}

// A server that has never been authorized starts with no file, which is a
// normal state rather than a failure to boot on.
func TestTokenStoreMissingFileLoadsEmpty(t *testing.T) {
	store := &tokenStore{path: filepath.Join(t.TempDir(), "absent.json")}

	got, err := store.load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got.RefreshToken != "" {
		t.Errorf("loaded %+v from a missing file, want the zero value", got)
	}
}

func TestTokenStoreReplacesTheExistingCredential(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token.json")
	store := &tokenStore{path: path}

	if err := store.save(tokens{RefreshToken: "refresh-1"}); err != nil {
		t.Fatalf("first save: %v", err)
	}
	if err := store.save(tokens{RefreshToken: "refresh-2"}); err != nil {
		t.Fatalf("second save: %v", err)
	}

	got, err := store.load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got.RefreshToken != "refresh-2" {
		t.Errorf("refresh token = %q, want refresh-2", got.RefreshToken)
	}

	// The rename must not leave the temporary file behind, or the directory
	// fills with copies of a live credential.
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	if len(entries) != 1 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("directory holds %v, want only the token file", names)
	}
}

// A save that fails partway must not leave its temporary file behind: it holds
// a live credential, and the successful path never exercises that cleanup
// because the rename consumes the file.
func TestTokenStoreCleansUpAfterAFailedSave(t *testing.T) {
	dir := t.TempDir()
	// A directory where the token file belongs makes the rename fail with the
	// temporary file already written.
	target := filepath.Join(dir, "token.json")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	store := &tokenStore{path: target}

	if err := store.save(tokens{RefreshToken: "refresh-1"}); err == nil {
		t.Fatal("save reported success despite an impossible rename")
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	for _, e := range entries {
		if e.Name() != "token.json" {
			t.Errorf("save left %q behind, which holds a credential", e.Name())
		}
	}
}

// The file holds a live credential, so it must not be group- or world-readable.
func TestTokenStoreFileIsPrivate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix permission bits are not meaningful on windows")
	}
	path := filepath.Join(t.TempDir(), "token.json")
	store := &tokenStore{path: path}

	if err := store.save(tokens{RefreshToken: "refresh-1"}); err != nil {
		t.Fatalf("save: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("token file mode = %o, want 600", perm)
	}
}

func TestTokenStoreCreatesItsDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "dir", "token.json")
	store := &tokenStore{path: path}

	if err := store.save(tokens{RefreshToken: "refresh-1"}); err != nil {
		t.Fatalf("save: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("token file was not created: %v", err)
	}
}

// An empty path is how "keep authorization in memory only" is expressed, so it
// must be a no-op on both halves rather than an error.
func TestTokenStoreWithoutPathIsInert(t *testing.T) {
	var store *tokenStore

	if err := store.save(tokens{RefreshToken: "refresh-1"}); err != nil {
		t.Errorf("save on a nil store: %v", err)
	}
	got, err := store.load()
	if err != nil {
		t.Errorf("load on a nil store: %v", err)
	}
	if got.RefreshToken != "" {
		t.Errorf("loaded %+v from a nil store", got)
	}
}

func TestTokenStoreRejectsAnUnreadableFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token.json")
	if err := os.WriteFile(path, []byte("not json"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	store := &tokenStore{path: path}

	// Silently starting unauthorized would hide a corrupted credential behind a
	// re-login prompt the operator cannot explain.
	if _, err := store.load(); err == nil {
		t.Error("load accepted a file that is not valid JSON")
	}
}

func TestTokenValidity(t *testing.T) {
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		tok  tokens
		want bool
	}{
		{"no refresh token", tokens{}, false},
		{"unknown expiry", tokens{RefreshToken: "r"}, true},
		{"expiry ahead", tokens{RefreshToken: "r", RefreshExpiry: now.Add(time.Hour)}, true},
		{"expiry passed", tokens{RefreshToken: "r", RefreshExpiry: now.Add(-time.Hour)}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.tok.valid(now); got != tc.want {
				t.Errorf("valid = %v, want %v", got, tc.want)
			}
		})
	}
}
