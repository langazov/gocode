package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/langazov/gocode-go/internal/tui/client"
	"github.com/langazov/gocode-go/internal/tui/dialog"
)

// mcpTestApp builds an app with a cached management list of three servers:
// connected+enabled, needs-auth+enabled, disabled.
func mcpTestApp(t *testing.T) *App {
	t.Helper()
	app := newTestApp(t, "http://example.invalid")
	app.width, app.height = 100, 30
	app.view = viewChat
	app.active = &client.Session{ID: "ses_1", Title: "Test"}
	app.mcpInfo = []client.MCPServerInfo{
		{Name: "github", Type: "local", Command: []string{"gh-mcp"}, Enabled: true, Status: "connected", ToolCount: 12, EstTokens: 3400, CostLive: true},
		{Name: "linear", Type: "remote", URL: "https://mcp.linear.app/sse", Enabled: true, Status: "needs_auth"},
		{Name: "old", Type: "remote", URL: "https://old.example", Enabled: false, Status: "disabled"},
		{Name: "toggled", Type: "remote", URL: "https://t.example", Enabled: false, Status: "disabled", ToolCount: 8, EstTokens: 1200},
	}
	return app
}

func openMCPDialog(t *testing.T, app *App) *dialog.Shell {
	t.Helper()
	app.openMCPDialog(app.mcpInfo)
	if app.overlay == nil || app.overlay.Kind != dialog.KindList {
		t.Fatal("openMCPDialog did not open a list dialog")
	}
	return app.overlay
}

// The dialog lists every configured server with its enable state and
// status, the ✓ gutter marking enabled ones.
func TestMCPDialogListsServers(t *testing.T) {
	app := mcpTestApp(t)
	o := openMCPDialog(t, app)
	items := o.AllItems()
	if len(items) != 4 {
		t.Fatalf("items = %d, want 4", len(items))
	}
	if items[0].Label != "github" || items[0].Gutter != "✓" {
		t.Errorf("first row = %+v, want github with ✓ gutter", items[0])
	}
	if items[2].Label != "old" || items[2].Gutter != "" {
		t.Errorf("disabled row = %+v, want old with no gutter", items[2])
	}
	if !strings.Contains(items[0].Hint, "enabled") || !strings.Contains(items[0].Hint, "connected") {
		t.Errorf("first hint = %q, want enabled — connected", items[0].Hint)
	}
	if !strings.Contains(items[2].Hint, "disabled") {
		t.Errorf("third hint = %q, want disabled", items[2].Hint)
	}
}

// Each row right-aligns the server's context cost. A connected server shows
// what it costs now; one disabled mid-run shows its last-known cost with a
// "+" — what enabling it back would return; never-measured shows a dash.
func TestMCPDialogShowsContextCost(t *testing.T) {
	app := mcpTestApp(t)
	o := openMCPDialog(t, app)
	items := o.AllItems()
	var byName = map[string]string{}
	for _, item := range items {
		byName[item.Label] = item.Footer
	}
	if got, want := byName["github"], "12 tools · 3.4K"; got != want {
		t.Errorf("connected row = %q, want %q", got, want)
	}
	if got, want := byName["linear"], "—"; got != want {
		t.Errorf("needs-auth row = %q, want %q (never measured)", got, want)
	}
	if got, want := byName["old"], "—"; got != want {
		t.Errorf("booted-disabled row = %q, want %q (never measured)", got, want)
	}
	if got, want := byName["toggled"], "+8 tools · 1.2K"; got != want {
		t.Errorf("disabled-mid-run row = %q, want %q (last known, + prefix)", got, want)
	}

	// The details drill-in spells each state out.
	o.SelectValue("github")
	item, _ := o.SelectedItem()
	app.drillMCPItem(item)
	view, _ := app.overlay.Panel()
	if !strings.Contains(view, "3.4K") || !strings.Contains(view, "estimated") {
		t.Errorf("detail view missing the token estimate:\n%s", view)
	}
	// Back to the list, then into a disabled-mid-run server's details.
	app.openMCPDialog(app.mcpInfo)
	o.SelectValue("toggled")
	item, _ = o.SelectedItem()
	_ = app.drillMCPItem(item)
	view, _ = app.overlay.Panel()
	if !strings.Contains(view, "1.2K") || !strings.Contains(view, "would return on enable") {
		t.Errorf("detail view missing the last-known estimate:\n%s", view)
	}
}

// Toggling flips the cached enable state, updates the rows in place and
// returns a command (the mutation) — the dialog stays open.
func TestMCPToggleFlipsRow(t *testing.T) {
	app := mcpTestApp(t)
	o := openMCPDialog(t, app)
	o.SelectValue("github")
	item, ok := o.SelectedItem()
	if !ok {
		t.Fatal("no selected item")
	}
	cmd := app.toggleMCPServer(item)
	if cmd == nil {
		t.Fatal("toggle returned no command")
	}
	if app.mcpInfo[0].Enabled {
		t.Error("cached enable state not flipped")
	}
	items := o.AllItems()
	if items[0].Gutter != "" {
		t.Errorf("row gutter after disable = %q, want empty", items[0].Gutter)
	}
	// And the disabled one comes back on.
	o.SelectValue("old")
	item, _ = o.SelectedItem()
	if cmd := app.toggleMCPServer(item); cmd == nil {
		t.Fatal("toggle returned no command")
	}
	if !app.mcpInfo[2].Enabled {
		t.Error("enable state of 'old' not flipped")
	}
}

// Delete is two-press: the first arms the row, the second commits and
// removes the row from the list.
func TestMCPDeleteArmsThenRemoves(t *testing.T) {
	app := mcpTestApp(t)
	o := openMCPDialog(t, app)
	o.SelectValue("old")
	item, _ := o.SelectedItem()

	if cmd := app.deleteMCPServerAction(item); cmd != nil {
		t.Error("the first press should arm, not run a command")
	}
	if o.Armed() != "old" {
		t.Errorf("armValue = %q, want old", o.Armed())
	}
	if len(o.AllItems()) != 4 {
		t.Fatal("the armed press already deleted a row")
	}

	cmd := app.deleteMCPServerAction(item)
	if cmd == nil {
		t.Fatal("the second press returned no command")
	}
	if len(o.AllItems()) != 3 {
		t.Errorf("rows after delete = %d, want 3", len(o.AllItems()))
	}
	if len(app.mcpInfo) != 3 {
		t.Errorf("cached info after delete = %d, want 3", len(app.mcpInfo))
	}
}

// The details drill-in shows type, endpoint and state, and enter does
// nothing there (a detail row is not a choice).
func TestMCPDetailDrillIn(t *testing.T) {
	app := mcpTestApp(t)
	o := openMCPDialog(t, app)
	o.SelectValue("linear")
	item, _ := o.SelectedItem()
	if cmd := app.drillMCPItem(item); cmd != nil {
		t.Error("drill-in returned a command")
	}
	if app.mcpDetail != "linear" {
		t.Errorf("mcpDetail = %q, want linear", app.mcpDetail)
	}
	o = app.overlay // drill-in mounted a new shell
	view, _ := o.Panel()
	for _, want := range []string{"remote", "https://mcp.linear.app/sse", "enabled", "needs authentication"} {
		if !strings.Contains(view, want) {
			t.Errorf("detail view missing %q:\n%s", want, view)
		}
	}
	// Back restores the list level.
	if cmd := o.Actions()[0].OnTrigger(dialog.Item{}); cmd != nil {
		_ = cmd
	}
	if app.mcpDetail != "" {
		t.Errorf("mcpDetail after back = %q, want empty", app.mcpDetail)
	}
}

// A refresh landing while the dialog is open folds in: rows update in
// place, and an open detail view is rebuilt from the new info.
func TestMCPInfoRefreshFoldsIntoOpenDialog(t *testing.T) {
	app := mcpTestApp(t)
	o := openMCPDialog(t, app)

	// List level: fold in.
	app.updateForTest(mcpInfoMsg{servers: []client.MCPServerInfo{
		{Name: "github", Type: "local", Command: []string{"gh-mcp"}, Enabled: true, Status: "connected"},
	}})
	if got := len(o.AllItems()); got != 1 {
		t.Errorf("rows after fold-in = %d, want 1", got)
	}

	// Detail level: rebuilt.
	app.drillMCPItem(dialog.Item{Value: "github"})
	app.updateForTest(mcpInfoMsg{servers: []client.MCPServerInfo{
		{Name: "github", Type: "local", Command: []string{"gh-mcp"}, Enabled: false, Status: "disabled"},
	}})
	if app.mcpDetail != "github" {
		t.Fatalf("drill-in lost after refresh: %q", app.mcpDetail)
	}
	o = app.overlay // the refresh rebuilt the detail shell
	view, _ := o.Panel()
	if !strings.Contains(view, "disabled") {
		t.Errorf("detail view not rebuilt from the refresh:\n%s", view)
	}
}

// The empty state names the way in, not just the emptiness.
func TestMCPEmptyState(t *testing.T) {
	app := mcpTestApp(t)
	app.mcpInfo = nil
	o := openMCPDialog(t, app)
	if o.HasItems() {
		t.Fatal("expected an empty list")
	}
	title, body := o.EmptyView()
	if !strings.Contains(title, "No MCP servers") {
		t.Errorf("empty title = %q", title)
	}
	if !strings.Contains(body, "gocode mcp add") {
		t.Errorf("empty body = %q, want the add hint", body)
	}
}

// The command registry carries the dialog as /mcp.
func TestMCPCommandRegistered(t *testing.T) {
	app := mcpTestApp(t)
	for _, item := range app.commandsRegistry() {
		if item.Value == "mcp.list" {
			if item.Slash != "mcp" {
				t.Errorf("slash = %q, want mcp", item.Slash)
			}
			return
		}
	}
	t.Fatal("mcp.list not registered")
}

// updateForTest drives one message through Update without the tea runtime.
func (a *App) updateForTest(msg tea.Msg) {
	_ = a.Update(msg)
}
