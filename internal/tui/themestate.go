package tui

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/langazov/gocode-go/internal/global"
)

// This file ports the persisted half of context/theme.tsx's active theme:
// `kv.set("theme", theme)`/`kv.get("theme", "opencode")`, a small piece of
// UI state the app itself writes whenever the user picks a theme — kept
// separate from config.Theme (config.go, sourced from the project/global
// config file) exactly like TS keeps it separate from `config.theme`, so
// browsing themes never rewrites a user-authored config file. TS's own
// precedence, `config.theme ?? kv.get("theme", "opencode")`, is
// ResolveStartupTheme below.

// themeStateFile is a sibling of prompt-history.jsonl (prompthistory.go),
// same directory, same best-effort read/write conventions.
const themeStateFile = "theme.json"

// ThemeStatePath is where the theme dialog's picks are persisted — exported
// so the CLI entry points (cmd_tui.go, cmd_run.go) can pass it back into
// ResolveStartupTheme without needing to know the filename themselves.
func ThemeStatePath() string {
	return filepath.Join(global.Resolve().State, themeStateFile)
}

type themeState struct {
	Theme string `json:"theme"`
}

// loadThemeState reads the last theme picked via the theme dialog,
// best-effort: a missing or corrupt file yields "", same as history.tsx's
// own catch-and-ignore reads.
func loadThemeState(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var state themeState
	if err := json.Unmarshal(data, &state); err != nil {
		return ""
	}
	return state.Theme
}

// saveThemeState persists name, best-effort — a failed write (e.g. a
// read-only state dir) shouldn't interrupt using the app.
func saveThemeState(path, name string) {
	if path == "" || name == "" {
		return
	}
	data, err := json.Marshal(themeState{Theme: name})
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	_ = os.WriteFile(path, data, 0o644)
}

// ResolveStartupTheme is theme/context.tsx's `config.theme ?? kv.get("theme",
// "opencode")` for the CLI entry points (cmd_tui.go, cmd_run.go): configTheme
// is the project/global config file's theme (config.Config.Theme), which
// wins when set since it's an explicit, checked-in choice; otherwise this
// falls back to whatever the theme dialog last persisted, then to gocode's
// own default. statePath is normally filepath.Join(global.Resolve().State,
// themeStateFile).
func ResolveStartupTheme(configTheme, statePath string) string {
	if configTheme != "" {
		return configTheme
	}
	if saved := loadThemeState(statePath); saved != "" {
		return saved
	}
	return "gocode-dark"
}
