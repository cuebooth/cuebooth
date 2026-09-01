package chat

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// tokens is the credential pair a provider persists across restarts.
type tokens struct {
	AccessToken   string    `json:"access_token"`
	RefreshToken  string    `json:"refresh_token"`
	AccessExpiry  time.Time `json:"access_expiry"`
	RefreshExpiry time.Time `json:"refresh_expiry"`
}

// valid reports whether the pair can still produce an access token: the refresh
// token is what survives a restart, and once it lapses only a fresh browser
// authorization recovers.
func (t tokens) valid(now time.Time) bool {
	return t.RefreshToken != "" && (t.RefreshExpiry.IsZero() || now.Before(t.RefreshExpiry))
}

// tokenStore persists tokens to a file.
//
// Restream invalidates the previous pair on every refresh, so the stored copy
// is state rather than configuration: a lost write costs the operator a
// re-authorization. Save therefore writes a temporary file in the same
// directory and renames it over the target, so an interrupted write leaves the
// prior credential intact instead of a truncated file.
type tokenStore struct {
	path string
}

// load returns the stored pair. A missing file is not an error — it is how a
// server that has never been authorized starts.
func (s *tokenStore) load() (tokens, error) {
	if s == nil || s.path == "" {
		return tokens{}, nil
	}
	data, err := os.ReadFile(s.path)
	if errors.Is(err, fs.ErrNotExist) {
		return tokens{}, nil
	}
	if err != nil {
		return tokens{}, fmt.Errorf("read chat token file %s: %w", s.path, err)
	}
	var t tokens
	if err := json.Unmarshal(data, &t); err != nil {
		return tokens{}, fmt.Errorf("parse chat token file %s: %w", s.path, err)
	}
	return t, nil
}

// save writes t durably. The file holds a live credential, so it is created
// 0600 and never widened.
func (s *tokenStore) save(t tokens) error {
	if s == nil || s.path == "" {
		return nil
	}
	data, err := json.MarshalIndent(t, "", "  ")
	if err != nil {
		return fmt.Errorf("encode chat tokens: %w", err)
	}

	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create chat token directory %s: %w", dir, err)
	}

	// The temporary file shares a directory with the target so the rename is
	// within one filesystem, which is what makes it atomic.
	tmp, err := os.CreateTemp(dir, filepath.Base(s.path)+".tmp*")
	if err != nil {
		return fmt.Errorf("create chat token temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("chmod chat token temp file: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write chat token temp file: %w", err)
	}
	// Sync before the rename: a rename that reaches disk ahead of the contents
	// would publish an empty file as the credential.
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("sync chat token temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close chat token temp file: %w", err)
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		return fmt.Errorf("replace chat token file %s: %w", s.path, err)
	}
	return nil
}
