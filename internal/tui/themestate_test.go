package tui

import (
	"path/filepath"
	"testing"
)

func TestThemeStateRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "theme.json")
	if got := loadThemeState(path); got != "" {
		t.Fatalf("loadThemeState on a missing file = %q, want \"\"", got)
	}
	saveThemeState(path, "dracula-dark")
	if got := loadThemeState(path); got != "dracula-dark" {
		t.Fatalf("loadThemeState after save = %q, want %q", got, "dracula-dark")
	}
	saveThemeState(path, "nord-light")
	if got := loadThemeState(path); got != "nord-light" {
		t.Fatalf("loadThemeState after overwrite = %q, want %q", got, "nord-light")
	}
}

func TestResolveStartupTheme(t *testing.T) {
	path := filepath.Join(t.TempDir(), "theme.json")

	// Nothing configured, nothing saved yet: the built-in default.
	if got := ResolveStartupTheme("", path); got != "gocode-dark" {
		t.Fatalf("ResolveStartupTheme with no config/state = %q, want gocode-dark", got)
	}

	// A saved pick wins over the built-in default.
	saveThemeState(path, "tokyonight-dark")
	if got := ResolveStartupTheme("", path); got != "tokyonight-dark" {
		t.Fatalf("ResolveStartupTheme with a saved pick = %q, want tokyonight-dark", got)
	}

	// The project/global config's theme wins over the saved pick, matching
	// TS's `config.theme ?? kv.get("theme", "opencode")`.
	if got := ResolveStartupTheme("dracula-light", path); got != "dracula-light" {
		t.Fatalf("ResolveStartupTheme with config.theme set = %q, want dracula-light", got)
	}
}

// TestSetThemePersistsAcrossRestart guards the actual feature: picking a
// theme (setTheme, the theme dialog's only path to it) has to survive the
// app being closed and reopened, via ResolveStartupTheme reading back what
// New wrote to a.themeStatePath.
func TestSetThemePersistsAcrossRestart(t *testing.T) {
	app := newTestApp(t, "http://example.invalid")
	app.setTheme(themeResolve("catppuccin-light"))

	if got := ResolveStartupTheme("", app.themeStatePath); got != "catppuccin-light" {
		t.Fatalf("ResolveStartupTheme after setTheme = %q, want catppuccin-light", got)
	}
}

// TestThemesOverlayCancelRestoresAndPersists guards dialog-theme-list.tsx's
// onCleanup: escaping the theme picker without confirming puts the
// pre-dialog theme back — both in a.theme (already covered indirectly by
// how live preview works) and in the persisted state, which a plain
// a.theme revert wouldn't touch on its own.
func TestThemesOverlayCancelRestoresAndPersists(t *testing.T) {
	app := newTestApp(t, "http://example.invalid")
	initial := app.theme.Name

	app.themesOverlay()
	if app.overlay == nil || app.overlay.onMove == nil || app.overlay.onCancel == nil {
		t.Fatal("themesOverlay did not wire onMove/onCancel")
	}
	// Simulate browsing to a different theme.
	app.overlay.onMove(overlayItem{value: "gruvbox-light"})
	if app.theme.Name != "gruvbox-light" {
		t.Fatalf("theme after onMove = %q, want gruvbox-light", app.theme.Name)
	}
	if got := loadThemeState(app.themeStatePath); got != "gruvbox-light" {
		t.Fatalf("persisted theme after onMove = %q, want gruvbox-light (live preview persists too, matching TS)", got)
	}

	// Escape: onCancel should put the original theme back, in-memory and
	// on disk.
	app.overlay.onCancel()
	if app.theme.Name != initial {
		t.Fatalf("theme after onCancel = %q, want %q", app.theme.Name, initial)
	}
	if got := loadThemeState(app.themeStatePath); got != initial {
		t.Fatalf("persisted theme after onCancel = %q, want %q", got, initial)
	}
}
