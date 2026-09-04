package tui

import (
	"fmt"
	"sort"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/langazov/gocode-go/internal/config"
	"github.com/langazov/gocode-go/internal/tui/client"
)

// The /plugins dialog: the loaded plugin list with per-plugin enable state,
// persisted back to the global config when the dialog closes.
//
// It is an ordinary DialogSelect, built the way themesOverlay builds the
// theme picker: openList for the rows, the shared overlay hooks for the
// behavior that is this dialog's own. That buys the filter input, the
// scroll window, the selection highlight, the footer action bar and mouse
// hit-testing for free, and — more to the point — makes every close path
// (esc, ctrl+c, a click on the esc hint, a click on the backdrop) run the
// same code, which here is the save. The bespoke renderer this replaced had
// none of that: a long plugin list overflowed the panel, and a click
// anywhere outside a row closed the dialog and dropped the edits.
//
// The overlay hooks it uses:
//
//	onCancel   save the working copy and close — the close path for every
//	           dismissal, exactly where themesOverlay puts its revert.
//	onActivate enter (and a click on a row) toggles instead of selecting,
//	           so the dialog stays open while several plugins are flipped.
//	actions    ctrl+t toggle, → details; ← back from the detail level.
//	           Footer actions bind ctrl keys and arrows, never bare letters:
//	           the filter input owns those (the same rule that keeps j/k
//	           typable in every other list dialog).
//
// "Enabled" means the config's `plugin` array names the plugin — the same
// rule the loader applies and `make disable-plugin` edits. Disabling here
// removes the entry (the change takes effect on restart, when the config is
// re-read and the loader runs again); enabling re-adds it. The dialog edits
// a working copy and saves once on close, so a half-finished edit is never
// written.

// pluginDialogState is the /plugins dialog's working copy. The server does
// not know the config, so the enable state is filled in from the App's own
// copy at dialog open; the loaded list only names what is running.
type pluginDialogState struct {
	// enabled holds the working copy of the enable state, keyed by config
	// ref (the same string the config's `plugin` array stores).
	enabled map[string]bool
	// dirty records that the working copy diverged from the config and the
	// close path must save.
	dirty bool
	// detail is the plugin id being drilled into, "" at the list level.
	detail string
}

// clientPluginRow is the flattened view of one plugin the dialog works from —
// the server's PluginStatus plus the resolved enable state, or, for one that
// is installed but unconfigured, the discovery that found it.
type clientPluginRow struct {
	id     string
	spec   string
	source string
	state  string
	// path is where an installed-but-unconfigured plugin was found; empty for
	// a configured one, whose spec already says where it lives.
	path    string
	hooks   []string
	tools   []string
	enabled bool
}

// pluginsOverlay opens the dialog and rescans for installed plugins.
//
// It renders from the App's cached lists so the panel appears immediately,
// and returns the refresh whose reply reopens the rows (see
// refreshPluginDialog) — the pattern the model and skill dialogs use. The
// rescan matters here: the loaded set is fixed at boot, but a plugin can be
// installed while the app runs, and this dialog is where you would go to turn
// it on.
func (a *App) pluginsOverlay() tea.Cmd {
	a.openPluginDialog(a.pluginConfig, a.plugins, a.pluginsAvailable)
	return a.loadPluginsCmd()
}

// openPluginDialog builds the working copy and opens the list. serverPlugins
// is the loaded list from the most recent GET /api/plugin, cfg the configured
// refs — which is what "enabled" means — and available what the scan found on
// disk without the config naming it. A configured ref with no loaded instance
// still shows: disabled or failed-to-load should be visible, not missing.
func (a *App) openPluginDialog(cfg []config.PluginSpec, serverPlugins []client.PluginStatus, available []client.PluginAvailable) {
	state := &pluginDialogState{enabled: map[string]bool{}}
	for _, row := range pluginRows(cfg, serverPlugins, available) {
		state.enabled[row.spec] = row.enabled
	}
	a.pluginDialog = state
	a.openPluginList()
}

// refreshPluginDialog folds a plugin list that landed after the dialog opened
// into the open panel: rows the scan just found are seeded with their config
// state, and rows the user already toggled keep the toggle.
func (a *App) refreshPluginDialog() {
	state := a.pluginDialog
	if state == nil || a.overlay == nil || state.detail != "" {
		return
	}
	for _, row := range pluginRows(a.pluginConfig, a.plugins, a.pluginsAvailable) {
		if _, edited := state.enabled[row.spec]; !edited {
			state.enabled[row.spec] = row.enabled
		}
	}
	a.rebuildPluginItems()
}

// openPluginList opens (or returns to) the dialog's list level.
func (a *App) openPluginList() {
	state := a.pluginDialog
	if state == nil {
		return
	}
	state.detail = ""
	a.openList("Plugins", a.pluginItems())
	o := a.overlay
	o.size = dialogLarge
	o.placeholder = "Search plugins..."
	o.onActivate = func(item overlayItem) tea.Cmd {
		a.togglePlugin(item)
		return nil
	}
	o.onCancel = a.savePluginsOnClose(state)
	o.actions = []dialogAction{
		{title: "toggle", keys: "ctrl+t", onTrigger: func(item overlayItem) tea.Cmd {
			a.togglePlugin(item)
			return nil
		}},
		{title: "details", keys: "right", onTrigger: func(item overlayItem) tea.Cmd {
			a.drillPlugin(item)
			return nil
		}},
	}
	if len(o.items) == 0 {
		o.emptyTitle = "No plugins"
		o.emptyBody = "Nothing is configured in the `plugin` array and no native plugin is loaded."
	}
}

// pluginItems renders the list rows from the working state. Enabled plugins
// carry the ✓ gutter the connect dialog uses for a provider that already
// holds a credential; the hint repeats the word so the filter can find it.
func (a *App) pluginItems() []overlayItem {
	state := a.pluginDialog
	if state == nil {
		return nil
	}
	rows := a.pluginRows()
	items := make([]overlayItem, 0, len(rows))
	for _, row := range rows {
		gutter := ""
		if state.enabled[row.spec] {
			gutter = "✓"
		}
		status := row.state
		if row.source != "" {
			status = row.source + " · " + row.state
		}
		items = append(items, overlayItem{
			label:    row.id,
			hint:     enabledWord(state.enabled[row.spec]) + " — " + status,
			value:    row.spec,
			gutter:   gutter,
			gutterOK: true,
		})
	}
	return items
}

// rebuildPluginItems refills the list rows after a toggle, keeping whatever
// the filter input holds — applyFilter also clamps the selection.
func (a *App) rebuildPluginItems() {
	o := a.overlay
	if o == nil || a.pluginDialog == nil || a.pluginDialog.detail != "" {
		return
	}
	selected := o.selected
	o.all = a.pluginItems()
	o.applyFilter()
	if selected < len(o.items) {
		o.selected = selected
	}
}

// togglePlugin flips a plugin's enable state in the working copy.
func (a *App) togglePlugin(item overlayItem) {
	state := a.pluginDialog
	if state == nil || item.value == "" {
		return
	}
	state.enabled[item.value] = !state.enabled[item.value]
	state.dirty = true
	a.rebuildPluginItems()
}

// drillPlugin opens the detail level for a plugin: the same list surface,
// its rows replaced by the plugin's identity, state and registrations.
func (a *App) drillPlugin(item overlayItem) {
	state := a.pluginDialog
	if state == nil || item.label == "" {
		return
	}
	row, ok := pluginRow(a.pluginRows(), item.label)
	if !ok {
		return
	}
	state.detail = row.id
	a.openList("Plugins / "+row.id, pluginDetailItems(row, state.enabled[row.spec]))
	o := a.overlay
	o.size = dialogLarge
	// A detail row is information, not a choice: enter must not fall through
	// to activateItem, which closes the dialog.
	o.onActivate = func(overlayItem) tea.Cmd { return nil }
	o.onCancel = a.savePluginsOnClose(state)
	o.actions = []dialogAction{
		{title: "back", keys: "left", onTrigger: func(overlayItem) tea.Cmd {
			a.openPluginList()
			return nil
		}},
	}
}

// pluginDetailItems is the drill-in body: identity and state as label/value
// rows, then what the plugin registered, grouped under its own heading. The
// field names are padded to a common width so the values line up — listRow
// lays a row out as title, one space, hint, with no column of its own.
func pluginDetailItems(row clientPluginRow, enabled bool) []overlayItem {
	var items []overlayItem
	field := func(name, value string) {
		// A plugin that never loaded has no source and no registrations; an
		// empty row would only say so twice.
		if value == "" {
			return
		}
		items = append(items, overlayItem{
			label: name + strings.Repeat(" ", 8-len(name)), hint: value, value: name,
		})
	}
	yesNo := "no"
	if enabled {
		yesNo = "yes"
	}
	field("spec", row.spec)
	field("source", row.source)
	field("state", row.state)
	field("enabled", yesNo)
	if row.path != row.spec {
		field("path", row.path)
	}
	switch {
	case row.state == "not loaded":
		field("warning", "configured but not loaded — check the path, or restart")
	case row.state == "installed" && !enabled:
		field("warning", "not in the config's plugin array — enable it, then restart")
	}
	for _, hook := range sortStrings(row.hooks) {
		items = append(items, overlayItem{label: hook, value: "hook:" + hook, category: "hooks"})
	}
	for _, tool := range sortStrings(row.tools) {
		items = append(items, overlayItem{label: tool, value: "tool:" + tool, category: "tools"})
	}
	return items
}

// savePluginsOnClose is the dialog's onCancel: every dismissal — esc, ctrl+c,
// the esc hint, the backdrop — saves the working copy and drops it. The state
// is captured rather than read back off the App, because resolveOverlay closes
// the dialog before running this.
func (a *App) savePluginsOnClose(state *pluginDialogState) func() tea.Msg {
	return func() tea.Msg {
		a.pluginDialog = nil
		return a.savePlugins(state)
	}
}

// savePlugins persists the working enable set to the global config. It writes
// only entries the dialog knows about, merged with any configured ref the
// loaded list never surfaced (e.g. a spec whose options bag the dialog does
// not render) — those pass through untouched, options and all.
func (a *App) savePlugins(state *pluginDialogState) tea.Cmd {
	if state == nil || !state.dirty {
		return nil
	}
	rows := a.pluginRows()
	// Start from the configured specs: anything not shown in the dialog (a
	// duplicate ref, a ref shadowed by an earlier merge) survives unchanged.
	shown := map[string]bool{}
	for _, row := range rows {
		shown[row.spec] = true
	}
	var specs []config.PluginSpec
	for _, spec := range a.pluginConfig {
		if !shown[spec.Ref] {
			specs = append(specs, spec)
		}
	}
	for _, row := range rows {
		if state.enabled[row.spec] {
			specs = append(specs, config.PluginSpec{Ref: row.spec})
		}
	}
	if err := config.SavePlugins(specs); err != nil {
		return staticMsg(statusMsg{text: "failed to save plugins: " + err.Error(), isErr: true})
	}
	return staticMsg(statusMsg{text: fmt.Sprintf("plugins saved (%d enabled) — restart to apply", len(specs))})
}

// pluginRows merges three views of the same thing, in that order: the
// config's plugin array, the loaded plugin list, and what the scan found
// installed. The config is the enable truth; the server list supplies the
// live state.
//
// Configured refs that never loaded render as disabled with a "not loaded"
// state, so a typo in gocode.json is visible from the dialog. Installed
// plugins the config does not name render last, disabled, with "installed"
// for a state — enabling one writes the ref the scan reports, which is the
// bare name under the global install root and the path anywhere else.
func pluginRows(cfg []config.PluginSpec, serverPlugins []client.PluginStatus, available []client.PluginAvailable) []clientPluginRow {
	configured := map[string]bool{}
	for _, spec := range cfg {
		configured[spec.Ref] = true
	}
	loadedBySpec := map[string]client.PluginStatus{}
	for _, p := range serverPlugins {
		loadedBySpec[p.Spec] = p
	}

	rows := make([]clientPluginRow, 0, len(cfg)+len(serverPlugins))
	seen := map[string]bool{}
	add := func(spec string, loaded client.PluginStatus, have bool) {
		if seen[spec] {
			return
		}
		seen[spec] = true
		row := clientPluginRow{spec: spec, enabled: configured[spec]}
		if have {
			row.id = loaded.ID
			row.source = loaded.Source
			row.state = loaded.State
			row.hooks = loaded.Hooks
			row.tools = loaded.Tools
		} else {
			row.id = spec
			row.state = "not loaded"
		}
		rows = append(rows, row)
	}
	for _, spec := range cfg {
		loaded, ok := loadedBySpec[spec.Ref]
		add(spec.Ref, loaded, ok)
	}
	// Native plugins have no config entry; they show enabled by definition.
	for _, p := range serverPlugins {
		if _, ok := seen[p.Spec]; !ok && p.Source == "native" {
			add(p.Spec, p, true)
		}
	}
	// Installed but unconfigured, last: the scan already excluded anything
	// the config names, and seen guards a name reached both ways.
	for _, entry := range available {
		if seen[entry.Ref] {
			continue
		}
		seen[entry.Ref] = true
		rows = append(rows, clientPluginRow{
			id:    entry.Name,
			spec:  entry.Ref,
			state: "installed",
			path:  entry.Path,
		})
	}
	return rows
}

// pluginRows is pluginRows over the App's three cached lists.
func (a *App) pluginRows() []clientPluginRow {
	return pluginRows(a.pluginConfig, a.plugins, a.pluginsAvailable)
}

// pluginRow looks one merged row up by the id the list rows carry as label.
func pluginRow(rows []clientPluginRow, id string) (clientPluginRow, bool) {
	for _, row := range rows {
		if row.id == id {
			return row, true
		}
	}
	return clientPluginRow{}, false
}

func enabledWord(enable bool) string {
	if enable {
		return "enabled"
	}
	return "disabled"
}

// sortStrings copies before sorting, so the detail view never reorders the
// cached lists the sidebar reads.
func sortStrings(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}
