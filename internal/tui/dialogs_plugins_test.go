package tui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/langazov/gocode-go/internal/config"
	"github.com/langazov/gocode-go/internal/tui/client"
)

// pluginTestApp builds an app with two configured plugins and a matching
// loaded list, plus an isolated global config dir.
func pluginTestApp(t *testing.T) (*App, string) {
	t.Helper()
	dir := t.TempDir() // global config root; gocode/ is created on save
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("XDG_STATE_HOME", filepath.Join(t.TempDir(), "state"))

	app := newTestApp(t, "http://example.invalid")
	app.width, app.height = 100, 30
	app.view = viewChat
	app.active = &client.Session{ID: "ses_1", Title: "Test"}
	app.plugins = []client.PluginStatus{
		{ID: "rag-plugin", Spec: "/work/rag-plugin", Source: "process", State: "running",
			Tools: []string{"rag_index", "rag_search"}},
		{ID: "subagents", Spec: "subagents", Source: "native", State: "loaded",
			Hooks: []string{"chat.params"}},
	}
	app.pluginConfig = []config.PluginSpec{
		{Ref: "/work/rag-plugin"},
		{Ref: "subagents"},
	}
	return app, filepath.Join(dir, "gocode", "gocode.json")
}

func openPluginsDialog(t *testing.T, app *App) *overlay {
	t.Helper()
	// The returned command is the rescan; the tests that care about it drive
	// it themselves against a stub server.
	app.openPluginDialog(app.pluginConfig, app.plugins, app.pluginsAvailable)
	// The dialog is an ordinary DialogSelect, like the theme picker: no
	// bespoke overlay kind, so it inherits the filter, the scroll window and
	// the mouse hit map.
	if app.overlay == nil || app.overlay.kind != overlayList {
		t.Fatal("pluginsOverlay did not open a list dialog")
	}
	return app.overlay
}

// The dialog lists every configured plugin with its enable state.
func TestPluginsDialogListsPlugins(t *testing.T) {
	app, _ := pluginTestApp(t)
	o := openPluginsDialog(t, app)

	if len(o.items) != 2 {
		t.Fatalf("items = %d, want 2", len(o.items))
	}
	// Enabled rows carry the ✓ gutter the connect dialog uses.
	for _, item := range o.items {
		if item.gutter != "✓" || !item.gutterOK {
			t.Fatalf("enabled row %q should carry the success gutter, got %q", item.label, item.gutter)
		}
	}
	view := ansi.Strip(app.View())
	for _, want := range []string{"Plugins", "rag-plugin", "subagents", "enabled", "toggle", "details"} {
		if !strings.Contains(view, want) {
			t.Fatalf("dialog missing %q, got:\n%s", want, view)
		}
	}
}

// enter and ctrl+t toggle the selected plugin, on the working copy only, and
// leave the dialog open so several can be flipped in one visit.
func TestPluginsDialogToggleKeys(t *testing.T) {
	app, _ := pluginTestApp(t)
	o := openPluginsDialog(t, app)
	state := app.pluginDialog

	// Selected is rag-plugin (index 0): enter toggles it off.
	drive(t, app, tea.KeyPressMsg{Code: tea.KeyEnter})
	if state.enabled["/work/rag-plugin"] {
		t.Fatal("enter did not disable rag-plugin")
	}
	if app.overlay == nil {
		t.Fatal("toggling must leave the dialog open")
	}
	if !state.dirty {
		t.Fatal("a change must mark the dialog dirty")
	}
	if o.items[0].hint != "disabled — process · running" {
		t.Fatalf("row hint = %q, want disabled", o.items[0].hint)
	}
	if o.items[0].gutter != "" {
		t.Fatalf("a disabled row should have no ✓, got %q", o.items[0].gutter)
	}

	// enter toggles it back on.
	drive(t, app, tea.KeyPressMsg{Code: tea.KeyEnter})
	if !state.enabled["/work/rag-plugin"] {
		t.Fatal("enter did not re-enable rag-plugin")
	}

	// Down to subagents (native), then the footer action's ctrl+t.
	drive(t, app, tea.KeyPressMsg{Code: tea.KeyDown})
	drive(t, app, tea.KeyPressMsg{Code: 't', Mod: tea.ModCtrl})
	if state.enabled["subagents"] {
		t.Fatal("ctrl+t did not disable subagents")
	}
}

// Letters filter the list rather than acting on it — the rule every other
// list dialog follows, and the reason toggling is on ctrl+t and not on d/e.
func TestPluginsDialogFilters(t *testing.T) {
	app, _ := pluginTestApp(t)
	o := openPluginsDialog(t, app)

	for _, r := range "sub" {
		drive(t, app, tea.KeyPressMsg{Text: string(r), Code: r})
	}
	if o.filter != "sub" {
		t.Fatalf("filter = %q, want sub", o.filter)
	}
	if len(o.items) != 1 || o.items[0].label != "subagents" {
		t.Fatalf("filtered items = %+v, want just subagents", o.items)
	}
	if app.pluginDialog.dirty {
		t.Fatal("typing a filter must not change any enable state")
	}

	// A toggle rebuilds the rows without dropping the filter.
	drive(t, app, tea.KeyPressMsg{Code: tea.KeyEnter})
	if len(o.items) != 1 || o.items[0].hint != "disabled — native · loaded" {
		t.Fatalf("rebuilt filtered row = %+v", o.items)
	}
}

// right drills into the detail view; left returns.
func TestPluginsDialogDrillDown(t *testing.T) {
	app, _ := pluginTestApp(t)
	openPluginsDialog(t, app)

	drive(t, app, tea.KeyPressMsg{Code: tea.KeyRight})
	if app.pluginDialog.detail != "rag-plugin" {
		t.Fatalf("detail = %q, want rag-plugin", app.pluginDialog.detail)
	}
	view := ansi.Strip(app.View())
	for _, want := range []string{"spec", "/work/rag-plugin", "process", "running", "tools", "rag_index", "rag_search", "back"} {
		if !strings.Contains(view, want) {
			t.Fatalf("detail view missing %q, got:\n%s", want, view)
		}
	}

	// enter on an information row does nothing — it must not close the dialog.
	drive(t, app, tea.KeyPressMsg{Code: tea.KeyEnter})
	if app.overlay == nil {
		t.Fatal("enter on a detail row closed the dialog")
	}

	// left returns to the list.
	drive(t, app, tea.KeyPressMsg{Code: tea.KeyLeft})
	if app.pluginDialog.detail != "" {
		t.Fatal("left did not return to the list level")
	}
	if app.overlay.title != "Plugins" {
		t.Fatalf("title = %q, want Plugins", app.overlay.title)
	}

	// esc from the detail level closes (and saves), it does not step back.
	drive(t, app, tea.KeyPressMsg{Code: tea.KeyRight})
	drive(t, app, tea.KeyPressMsg{Code: tea.KeyEscape})
	if app.overlay != nil {
		t.Fatal("esc from detail did not close the dialog")
	}
}

// Closing the dialog after a change writes the enable set to the global
// config, preserving other keys; closing without changes writes nothing.
func TestPluginsDialogSavesOnClose(t *testing.T) {
	app, path := pluginTestApp(t)
	seedPluginConfig(t, path)

	// Disable subagents, then close. Update directly on the close: the save
	// produces a toast whose 5s tick drive would execute.
	openPluginsDialog(t, app)
	app.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	app.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	app.Update(tea.KeyPressMsg{Code: tea.KeyEscape})

	assertSavedPlugins(t, path)
	if app.overlay != nil {
		t.Fatal("dialog did not close after save")
	}
	if app.pluginDialog != nil {
		t.Fatal("the working copy outlived the dialog")
	}

	// Reopening reflects the saved state: subagents disabled. The app's
	// cached config must be reloaded the way a restart would, so the same
	// read the save performed sees the file just written.
	saved, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	app.pluginConfig = saved.Plugin
	app.openPluginDialog(app.pluginConfig, app.plugins, app.pluginsAvailable)
	if app.pluginDialog.enabled["subagents"] {
		t.Fatal("reopened dialog does not reflect the saved disable")
	}
}

// Dismissing with the mouse saves too: a click on the backdrop runs the same
// close handler escape does, instead of dropping the edits on the floor.
func TestPluginsDialogSavesOnBackdropClick(t *testing.T) {
	app, path := pluginTestApp(t)
	app.width, app.height = 100, 40
	seedPluginConfig(t, path)

	openPluginsDialog(t, app)
	app.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	app.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	app.Update(tea.MouseClickMsg{X: 0, Y: 0, Button: tea.MouseLeft})
	app.Update(tea.MouseReleaseMsg{X: 0, Y: 0, Button: tea.MouseLeft})

	if app.overlay != nil {
		t.Fatal("clicking the backdrop should close the dialog")
	}
	assertSavedPlugins(t, path)
}

// A plugin installed under a config directory's plugin folder but absent
// from the config shows up, disabled, after the dialog's own rescan lands.
func TestPluginsDialogShowsInstalledAsDisabled(t *testing.T) {
	app, _ := pluginTestApp(t)
	o := openPluginsDialog(t, app)

	// The scan reply arrives a moment after the panel opened.
	drive(t, app, pluginsMsg{
		plugins:    app.plugins,
		configured: []client.PluginSpec{{Ref: "/work/rag-plugin"}, {Ref: "subagents"}},
		available:  []client.PluginAvailable{{Name: "lint", Ref: "lint", Path: "/home/u/.config/gocode/plugin/lint"}},
	})

	if len(o.items) != 3 {
		t.Fatalf("items = %d, want 3 (two configured, one installed)", len(o.items))
	}
	found := o.items[2]
	if found.label != "lint" {
		t.Fatalf("installed plugin should come last, got %+v", o.items)
	}
	if found.hint != "disabled — installed" {
		t.Fatalf("installed row hint = %q, want disabled", found.hint)
	}
	if found.gutter != "" {
		t.Fatalf("an unconfigured plugin must not show the enabled ✓, got %q", found.gutter)
	}
	if app.pluginDialog.dirty {
		t.Fatal("discovering a plugin is not an edit")
	}
	if app.pluginDialog.enabled["lint"] {
		t.Fatal("an unconfigured plugin must start disabled")
	}
}

// Enabling a discovered plugin writes its ref into the config's plugin array,
// alongside what was already there.
func TestPluginsDialogEnablesInstalledPlugin(t *testing.T) {
	app, path := pluginTestApp(t)
	seedPluginConfig(t, path)
	app.pluginsAvailable = []client.PluginAvailable{
		{Name: "lint", Ref: "lint", Path: "/home/u/.config/gocode/plugin/lint"},
	}

	openPluginsDialog(t, app)
	app.Update(tea.KeyPressMsg{Code: tea.KeyEnd}) // the installed row, last
	app.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !app.pluginDialog.enabled["lint"] {
		t.Fatal("enter did not enable the installed plugin")
	}
	app.Update(tea.KeyPressMsg{Code: tea.KeyEscape})

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Plugin []string `json:"plugin"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("config not valid JSON after save: %v (%s)", err, raw)
	}
	want := []string{"/work/rag-plugin", "subagents", "lint"}
	if len(out.Plugin) != len(want) {
		t.Fatalf("plugin = %v, want %v", out.Plugin, want)
	}
	for i := range want {
		if out.Plugin[i] != want[i] {
			t.Fatalf("plugin = %v, want %v", out.Plugin, want)
		}
	}
}

// The detail view of a discovered plugin says where it was found and what to
// do about it.
func TestPluginsDialogInstalledDetail(t *testing.T) {
	app, _ := pluginTestApp(t)
	app.pluginsAvailable = []client.PluginAvailable{
		{Name: "lint", Ref: "lint", Path: "/home/u/.config/gocode/plugin/lint"},
	}
	openPluginsDialog(t, app)

	drive(t, app, tea.KeyPressMsg{Code: tea.KeyEnd})
	drive(t, app, tea.KeyPressMsg{Code: tea.KeyRight})
	view := ansi.Strip(app.View())
	for _, want := range []string{"Plugins / lint", "installed", "/home/u/.config/gocode/plugin/lint", "not in the config"} {
		if !strings.Contains(view, want) {
			t.Fatalf("detail view missing %q, got:\n%s", want, view)
		}
	}
}

// ctrl+c closes the dialog the way escape does, save included.
func TestPluginsDialogSavesOnCtrlC(t *testing.T) {
	app, path := pluginTestApp(t)
	seedPluginConfig(t, path)

	openPluginsDialog(t, app)
	app.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	app.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	app.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})

	if app.overlay != nil {
		t.Fatal("ctrl+c should close the dialog")
	}
	assertSavedPlugins(t, path)
}

// A click on a row toggles it, exactly as enter does, and keeps the panel up.
func TestPluginsDialogClickTogglesRow(t *testing.T) {
	app, _ := pluginTestApp(t)
	app.width, app.height = 100, 40
	openPluginsDialog(t, app)

	panel, hits := app.overlayPanel()
	top, left := app.overlayOrigin(lipgloss.Width(panel))
	row := rowForItem(hits, 1)

	drive(t, app, tea.MouseClickMsg{X: left + 5, Y: top + row, Button: tea.MouseLeft})
	drive(t, app, tea.MouseReleaseMsg{X: left + 5, Y: top + row, Button: tea.MouseLeft})

	if app.overlay == nil {
		t.Fatal("clicking a plugin row must not close the dialog")
	}
	if app.pluginDialog.enabled["subagents"] {
		t.Fatal("clicking the subagents row did not toggle it off")
	}
}

// A clean close (no edits) must not touch the config file.
func TestPluginsDialogNoSaveWithoutChanges(t *testing.T) {
	app, path := pluginTestApp(t)
	openPluginsDialog(t, app)
	drive(t, app, tea.KeyPressMsg{Code: tea.KeyEscape})
	if _, err := os.Stat(path); err == nil {
		t.Fatal("a clean close wrote a config file")
	}
}

// The slash command is registered.
func TestPluginsSlashCommand(t *testing.T) {
	app, _ := pluginTestApp(t)
	found := false
	for _, entry := range app.commandsRegistry() {
		if entry.slash == "plugins" {
			found = true
			drive(t, app, tea.KeyPressMsg{Code: tea.KeyEscape})
			break
		}
	}
	if !found {
		t.Fatal("no /plugins command in the registry")
	}
}

// seedPluginConfig writes a global config holding both plugins plus an
// unrelated key, so a save can be checked for preserving it.
func seedPluginConfig(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"theme":"gocode-dark","plugin":["/work/rag-plugin","subagents"]}`), 0o644); err != nil {
		t.Fatal(err)
	}
}

// assertSavedPlugins checks the config now names only rag-plugin, with every
// other key intact.
func assertSavedPlugins(t *testing.T, path string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Theme  string   `json:"theme"`
		Plugin []string `json:"plugin"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("config not valid JSON after save: %v (%s)", err, raw)
	}
	if out.Theme != "gocode-dark" {
		t.Errorf("theme = %q, want preserved", out.Theme)
	}
	if len(out.Plugin) != 1 || out.Plugin[0] != "/work/rag-plugin" {
		t.Errorf("plugin = %v, want just [/work/rag-plugin]", out.Plugin)
	}
}
