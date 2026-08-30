// Package modelstate persists the last-used model per directory, mirroring
// the original opencode behavior: after switching models in the interface,
// restarts resume with the last-used model instead of the config default.
package modelstate

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"

	"github.com/anomalyco/opencode-go/internal/global"
)

type Ref struct {
	ProviderID string `json:"providerID"`
	ModelID    string `json:"modelID"`
}

var mu sync.Mutex

func file() string {
	return filepath.Join(global.Resolve().Data, "model-state.json")
}

// normalize resolves symlinks so the state key is stable regardless of how
// the directory was reached (/var vs /private/var on macOS, tmp symlinks).
func normalize(dir string) string {
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		return resolved
	}
	return dir
}

// Load returns the last-used model for a directory, if any.
func Load(dir string) (Ref, bool) {
	dir = normalize(dir)
	mu.Lock()
	defer mu.Unlock()
	data, err := os.ReadFile(file())
	if err != nil {
		return Ref{}, false
	}
	var state map[string]Ref
	if err := json.Unmarshal(data, &state); err != nil {
		return Ref{}, false
	}
	ref, ok := state[dir]
	return ref, ok
}

// Save records the last-used model for a directory (atomic write).
func Save(dir string, ref Ref) error {
	dir = normalize(dir)
	mu.Lock()
	defer mu.Unlock()
	var state map[string]Ref
	if data, err := os.ReadFile(file()); err == nil {
		json.Unmarshal(data, &state)
	}
	if state == nil {
		state = map[string]Ref{}
	}
	state[dir] = ref
	encoded, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(file()), 0o755); err != nil {
		return err
	}
	tmp := file() + ".tmp"
	if err := os.WriteFile(tmp, encoded, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, file())
}
