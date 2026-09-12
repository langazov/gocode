package sync

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// BundleVersion is the only bundle format version this client understands.
const BundleVersion = 1

// Key names inside Bundle.Files. The global config and theme pick have
// fixed keys; project configs are keyed "project:<remote-url>|<relpath>".
const (
	KeyGlobal = "global"
	KeyTheme  = "theme"
)

// Bundle is the plaintext inside the envelope: every settings file this
// machine contributes to (or received from) the sync.
type Bundle struct {
	Version int               `json:"version"`
	Files   map[string]string `json:"files"`
}

// NewBundle returns an empty v1 bundle.
func NewBundle() *Bundle {
	return &Bundle{Version: BundleVersion, Files: map[string]string{}}
}

// ParseBundle decodes and validates a bundle from decrypted plaintext.
func ParseBundle(plaintext string) (*Bundle, error) {
	var bundle Bundle
	if err := json.Unmarshal([]byte(plaintext), &bundle); err != nil {
		return nil, fmt.Errorf("sync: bundle is not valid JSON: %w", err)
	}
	if bundle.Version != BundleVersion {
		return nil, fmt.Errorf("sync: bundle version %d is not supported", bundle.Version)
	}
	if bundle.Files == nil {
		bundle.Files = map[string]string{}
	}
	return &bundle, nil
}

// Marshal renders the bundle for sealing, with stable key order so the same
// files always produce the same plaintext (and therefore identical test
// vectors).
func (b *Bundle) Marshal() (string, error) {
	encoded, err := json.Marshal(b)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

// ProjectKey builds a project entry key from the git origin URL and the
// config file's path relative to the worktree root.
func ProjectKey(remoteURL, relPath string) string {
	return "project:" + remoteURL + "|" + relPath
}

// ParseProjectKey splits a project key back into remote URL and relative path.
func ParseProjectKey(key string) (remoteURL, relPath string, ok bool) {
	rest, found := strings.CutPrefix(key, "project:")
	if !found {
		return "", "", false
	}
	// The remote URL cannot contain '|' (git remotes are URL/path shaped),
	// so the first separator ends it.
	url, rel, found := strings.Cut(rest, "|")
	if !found || url == "" || rel == "" {
		return "", "", false
	}
	return url, rel, true
}

// GlobalConfigCandidates are the global config filenames in loader merge
// order; the first that exists is the synced one. Same list as
// internal/configedit.
var GlobalConfigCandidates = []string{"config.json", "gocode.json", "gocode.jsonc"}

// GlobalConfigDefault is created when no global config exists yet.
const GlobalConfigDefault = "gocode.json"

// themeFileName matches tui/themestate.go's file (the path is computed here
// rather than imported, so this package never depends on the TUI).
const themeFileName = "theme.json"

// Paths is the set of locations a bundle reads and writes. It is a struct
// (not pulled live from global.Resolve) so callers and tests can point it
// at scratch directories.
type Paths struct {
	ConfigDir string // global config directory
	StateDir  string // TUI state directory (theme.json)
	DataDir   string // staging area for not-yet-cloned projects
}

// GlobalConfigPath returns the existing global config path, or the default
// path to create when none exists (found=false).
func (p Paths) GlobalConfigPath() (path string, found bool) {
	for _, name := range GlobalConfigCandidates {
		candidate := filepath.Join(p.ConfigDir, name)
		if _, err := os.Stat(candidate); err == nil {
			return candidate, true
		}
	}
	return filepath.Join(p.ConfigDir, GlobalConfigDefault), false
}

// ThemePath returns the TUI theme state file path.
func (p Paths) ThemePath() string {
	return filepath.Join(p.StateDir, themeFileName)
}

// ProjectStagingDir is where project configs with no local match wait.
func (p Paths) ProjectStagingDir() string {
	return filepath.Join(p.DataDir, "sync", "projects")
}

// writeFileAtomic replaces path atomically: a temp file in the same
// directory, then rename, so an interrupted write can never truncate the
// user's config. Same convention as configedit.
func writeFileAtomic(path, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".sync-*")
	if err != nil {
		return err
	}
	name := temp.Name()
	defer os.Remove(name)
	if _, err := temp.WriteString(content); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(name, 0o644); err != nil {
		return err
	}
	return os.Rename(name, path)
}
