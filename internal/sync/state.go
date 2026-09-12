package sync

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// ErrNoKey reports that no derived sync key is stored on this machine —
// the sign-in that derives it has not happened here yet.
var ErrNoKey = fmt.Errorf("sync: no encryption key stored; sign in to derive it")

// stateFileName is where the manager keeps its bookkeeping, a sibling of
// theme.json in the state directory.
const stateFileName = "sync.json"

// State records the last point where local files and the server agreed.
// Both loops (watch and poll) consult it to avoid ping-ponging: after a
// pull writes files, the recorded hashes equal the new content, so the
// watcher does not push back what the poller just applied.
type State struct {
	// Enabled gates the loops; nil means enabled (default on when signed in).
	Enabled *bool `json:"enabled,omitempty"`
	// LastRevision is the server revision this machine last synced.
	LastRevision int64 `json:"lastRevision"`
	// LastSyncAt is when the last successful push or pull completed.
	LastSyncAt time.Time `json:"lastSyncAt"`
	// Hashes are sha256 (hex) of each file's content at last sync, keyed
	// like bundle keys ("global", "theme", "project:...").
	Hashes map[string]string `json:"hashes"`
	// Salt and Iter mirror the envelope's KDF parameters so a new seal can
	// derive the same key as the stored envelope without refetching it.
	Salt string `json:"salt,omitempty"`
	Iter int    `json:"iter,omitempty"`
	// Key holds the derived sync key (base64 std). It is as sensitive as
	// the gocoder.org API key it sits next to; the file is written 0600.
	// Empty when the user has not signed in since the key format landed.
	Key string `json:"key,omitempty"`
	// NeedsRelogin is set when decryption failed — the stored key no longer
	// matches the account's password (changed on the website). Sync pauses
	// until the next sign-in re-derives it.
	NeedsRelogin bool `json:"needsRelogin,omitempty"`
}

// StatePath returns the sync state file location.
func StatePath(stateDir string) string {
	return filepath.Join(stateDir, stateFileName)
}

// LoadState reads the sync state; a missing file is a fresh start, not an
// error.
func LoadState(stateDir string) (*State, error) {
	data, err := os.ReadFile(StatePath(stateDir))
	if errors.Is(err, fs.ErrNotExist) {
		return &State{Hashes: map[string]string{}}, nil
	}
	if err != nil {
		return nil, err
	}
	var state State
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("%s: %w", StatePath(stateDir), err)
	}
	if state.Hashes == nil {
		state.Hashes = map[string]string{}
	}
	return &state, nil
}

// SaveState writes the state atomically, owner-only: it carries the derived
// sync key.
func SaveState(stateDir string, state *State) error {
	path := StatePath(stateDir)
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".sync-*")
	if err != nil {
		return err
	}
	name := temp.Name()
	defer os.Remove(name)
	if err := temp.Chmod(0o600); err != nil {
		temp.Close()
		return err
	}
	if _, err := temp.Write(append(data, '\n')); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

// IsEnabled reports whether sync loops should run. GOCODE_SYNC=off wins
// over everything; then the state file's enabled flag; default on.
func (s *State) IsEnabled() bool {
	if os.Getenv("GOCODE_SYNC") == "off" {
		return false
	}
	if s == nil || s.Enabled == nil {
		return true
	}
	return *s.Enabled
}
