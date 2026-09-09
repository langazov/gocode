package tui

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/langazov/gocode-go/internal/global"
)

// diffstate.go ports the persisted preferences of the TS diff viewer's KV
// entries (packages/tui/src/feature-plugins/system/diff-viewer.tsx):
//
//	KV_SHOW_FILE_TREE = "diff_viewer_show_file_tree"   (default true)
//	KV_SINGLE_PATCH   = "diff_viewer_single_patch"      (default false)
//	KV_VIEW           = "diff_viewer_view"              (split | unified)
//
// It follows themestate.go's shape exactly: a small JSON file in the state
// directory, best-effort read/write, kept separate from config so flipping a
// viewer preference never rewrites a user-authored config file.
//
// Divergence: TS's `diff_style: "stacked"` config key (which forces unified)
// is not ported — the Go config schema (internal/config/config.go) has no
// such key, and inventing one for a single viewer is not warranted.

// diffStateFile is a sibling of theme.json and prompt-history.jsonl.
const diffStateFile = "diffstate.json"

// DiffStatePath is where the diff viewer's preferences are persisted,
// exported so CLI entry points can pass it through like ThemeStatePath.
func DiffStatePath() string {
	return filepath.Join(global.Resolve().State, diffStateFile)
}

type diffState struct {
	// FileTree is KV_SHOW_FILE_TREE. TS defaults true
	// (`kv.get(KV_SHOW_FILE_TREE, true) !== false`).
	FileTree *bool `json:"fileTree,omitempty"`
	// SinglePatch is KV_SINGLE_PATCH. TS defaults false
	// (`kv.get(KV_SINGLE_PATCH, false) === true`).
	SinglePatch bool `json:"singlePatch,omitempty"`
	// View is KV_VIEW: "split" or "unified", unset until the user picks one
	// (storedView returns undefined for anything else).
	View string `json:"view,omitempty"`
}

// loadDiffState reads the viewer's persisted preferences, best-effort: a
// missing or corrupt file yields the zero value, like themeState's
// catch-and-ignore read.
func loadDiffState(path string) diffState {
	data, err := os.ReadFile(path)
	if err != nil {
		return diffState{}
	}
	var state diffState
	if err := json.Unmarshal(data, &state); err != nil {
		return diffState{}
	}
	return state
}

// saveDiffState persists the state, best-effort — a failed write must never
// interrupt using the viewer.
func saveDiffState(path string, state diffState) {
	if path == "" {
		return
	}
	data, err := json.Marshal(state)
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	_ = os.WriteFile(path, data, 0o644)
}

// showFileTreeDefault applies TS's `kv.get(KV_SHOW_FILE_TREE, true) !== false`:
// an explicit false hides it; anything else (including never-set) shows it.
func (s diffState) showFileTreeDefault() bool {
	return s.FileTree == nil || *s.FileTree
}

// storedView applies TS's storedView: only "split" and "unified" count;
// anything else is unset and the width-derived default applies.
func (s diffState) storedView() (string, bool) {
	if s.View == "split" || s.View == "unified" {
		return s.View, true
	}
	return "", false
}
