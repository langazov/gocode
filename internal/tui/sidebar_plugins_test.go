package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/langazov/gocode-go/internal/tui/client"
)

// The sidebar's Plugins section lists each loaded plugin with its tier and
// state — mirroring the MCP section's per-server dot rows.
func TestSidebarShowsLoadedPlugins(t *testing.T) {
	app := newTestApp(t, "http://example.invalid")
	app.width, app.height = 140, 40
	app.view = viewChat
	app.active = &client.Session{ID: "ses_1", Title: "Test"}
	app.sidebar = true
	app.plugins = []client.PluginStatus{
		{ID: "plugin-echo", Spec: "./examples/plugin-echo", Source: "process", State: "running",
			Hooks: []string{"chat.params"}, Tools: []string{"echo"}},
		{ID: "subagents", Spec: "subagents", Source: "native", State: "loaded"},
	}

	view := ansi.Strip(app.sidebarView())
	if !strings.Contains(view, "Plugins") {
		t.Fatalf("sidebar missing Plugins section, got:\n%s", view)
	}
	for _, want := range []string{
		"plugin-echo", "process · running",
		"subagents", "native · loaded",
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("sidebar plugin rows missing %q, got:\n%s", want, view)
		}
	}
}

// With no plugins loaded the section states that explicitly rather than
// disappearing — same empty-state rule as LSP's "no servers found".
func TestSidebarShowsEmptyPluginState(t *testing.T) {
	app := newTestApp(t, "http://example.invalid")
	app.width, app.height = 140, 40
	app.view = viewChat
	app.active = &client.Session{ID: "ses_1", Title: "Test"}
	app.sidebar = true

	view := ansi.Strip(app.sidebarView())
	if !strings.Contains(view, "No plugins loaded") {
		t.Fatalf("sidebar should say no plugins are loaded, got:\n%s", view)
	}
}

// The status overlay lists the loaded plugins instead of the old hardcoded
// "No Plugins" line.
func TestStatusOverlayListsPlugins(t *testing.T) {
	app := newTestApp(t, "http://example.invalid")
	app.plugins = []client.PluginStatus{
		{ID: "rag-plugin", Spec: "./cmd/rag-plugin", Source: "process", State: "running"},
	}

	view := ansi.Strip(app.statusOverlay(80))
	for _, want := range []string{"1 Plugins", "rag-plugin", "process · running"} {
		if !strings.Contains(view, want) {
			t.Fatalf("status overlay missing %q, got:\n%s", want, view)
		}
	}
}
