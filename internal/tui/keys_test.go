package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/anomalyco/opencode-go/internal/tui/client"
)

func key(app *App, name string) tea.Cmd {
	return app.handleKey(tea.KeyPressMsg{Text: name, Code: []rune(name)[0]})
}

// --- bindings this port invented and upstream does not have ----------------

// help_show defaults to "none" in config/keybind.ts. Binding it to "?" also
// meant "?" could not be typed as the first character of a prompt.
func TestQuestionMarkTypesInsteadOfOpeningHelp(t *testing.T) {
	app := newTestApp(t, "http://example.invalid")
	app.view = viewChat
	app.active = &client.Session{ID: "ses_1"}

	key(app, "?")
	if app.overlay != nil {
		t.Fatal("? has no default binding upstream and must not open a dialog")
	}
	if app.input.Value() != "?" {
		t.Fatalf("? should reach the prompt, got %q", app.input.Value())
	}
}

// session_fork defaults to "none"; <leader>f was this port's own invention.
func TestLeaderFIsNotBound(t *testing.T) {
	app := newTestApp(t, "http://example.invalid")
	app.leaderArmed = true
	key(app, "f")
	if app.overlay != nil {
		t.Fatal("<leader>f has no upstream binding")
	}
}

// Dialogs close on escape (and enter, for help) — not on `q`.
func TestDialogsDoNotCloseOnQ(t *testing.T) {
	app := newTestApp(t, "http://example.invalid")
	app.overlay = &overlay{kind: overlayHelp, title: "Help"}
	app.handleOverlayKey("q")
	if app.overlay == nil {
		t.Fatal("q is not a dialog binding upstream")
	}
	app.handleOverlayKey("esc")
	if app.overlay != nil {
		t.Fatal("escape closes the dialog")
	}
}

// --- bindings that were missing ---------------------------------------------

// agent_cycle / agent_cycle_reverse: tab and shift+tab. The hint row has
// always advertised "tab agents" with nothing bound to it.
func TestTabCyclesAgents(t *testing.T) {
	app := newTestApp(t, "http://example.invalid")
	app.agents2 = []client.Agent{{ID: "build"}, {ID: "plan"}, {ID: "general"}}
	app.activeAgent = "build"

	app.cycleAgent(1)
	if app.activeAgent != "plan" {
		t.Fatalf("tab should advance the agent, got %q", app.activeAgent)
	}
	app.cycleAgent(-1)
	if app.activeAgent != "build" {
		t.Fatalf("shift+tab should go back, got %q", app.activeAgent)
	}
	app.cycleAgent(-1)
	if app.activeAgent != "general" {
		t.Fatalf("cycling wraps, got %q", app.activeAgent)
	}
}

// session_child_first is <leader>down.
func TestLeaderDownOpensChildSessions(t *testing.T) {
	api, server := newMockAPI(t)
	_ = api
	app := newTestApp(t, server.URL)
	app.active = &client.Session{ID: "ses_1"}
	app.leaderArmed = true
	driveCmd(t, app, app.handleKey(tea.KeyPressMsg{Code: tea.KeyDown}))
	if app.overlay == nil || app.overlay.kind != overlayList {
		t.Fatal("<leader>down should open the child session list")
	}
}

// tips_toggle is <leader>h.
func TestLeaderHTogglesTips(t *testing.T) {
	app := newTestApp(t, "http://example.invalid")
	app.width, app.height = 100, 40
	if strings.Contains(ansi.Strip(app.viewHome()), "Tip") == false {
		t.Skip("no tip rendered in this build")
	}
	app.leaderArmed = true
	key(app, "h")
	if !app.tipsHidden {
		t.Fatal("<leader>h should hide the tips")
	}
	if strings.Contains(ansi.Strip(app.viewHome()), "Tip") {
		t.Fatal("the tip row should be gone once toggled off")
	}
}

// The messages_* scroll family.
func TestMessageScrollBindings(t *testing.T) {
	app := newTestApp(t, "http://example.invalid")
	app.view = viewChat
	app.active = &client.Session{ID: "ses_1"}
	for i := range 60 {
		app.timeline = append(app.timeline, settledAssistant(t, "m"+string(rune('a'+i%26))+string(rune('a'+i/26)), "line"))
	}
	page := app.viewportHeight()

	for _, tc := range []struct {
		key  string
		want int
	}{
		{"ctrl+alt+b", page},
		{"ctrl+alt+u", page / 2},
		{"ctrl+alt+y", 1},
	} {
		app.scrollOffset = 0
		app.handleKey(tea.KeyPressMsg{Text: tc.key})
		if app.scrollOffset != tc.want {
			t.Fatalf("%s scrolled %d lines, want %d", tc.key, app.scrollOffset, tc.want)
		}
	}

	// messages_first / messages_last.
	app.scrollOffset = 0
	app.handleKey(tea.KeyPressMsg{Text: "ctrl+g"})
	if app.scrollOffset != app.maxScrollOffset() || app.scrollOffset == 0 {
		t.Fatalf("ctrl+g should jump to the first message, got %d", app.scrollOffset)
	}
	app.handleKey(tea.KeyPressMsg{Text: "ctrl+alt+g"})
	if app.scrollOffset != 0 {
		t.Fatalf("ctrl+alt+g should return to the last message, got %d", app.scrollOffset)
	}
}

// Scrolling clamps to the content above the viewport instead of running away.
func TestScrollClampsToTheContent(t *testing.T) {
	app := newTestApp(t, "http://example.invalid")
	app.view = viewChat
	app.active = &client.Session{ID: "ses_1"}
	for range 20 {
		app.handleKey(tea.KeyPressMsg{Text: "ctrl+alt+b"})
	}
	if app.scrollOffset != 0 {
		t.Fatalf("an empty timeline has nowhere to scroll, got %d", app.scrollOffset)
	}
}

// input_newline: shift+return, ctrl+return, alt+return, ctrl+j.
func TestNewlineAliasesInsertIntoThePrompt(t *testing.T) {
	for _, name := range []string{"shift+enter", "ctrl+enter", "alt+enter", "ctrl+j"} {
		app := newTestApp(t, "http://example.invalid")
		app.view = viewChat
		app.active = &client.Session{ID: "ses_1"}
		app.input.SetValue("one")
		app.handleKey(tea.KeyPressMsg{Text: name})
		if !strings.Contains(app.input.Value(), "\n") {
			t.Fatalf("%s should insert a newline, got %q", name, app.input.Value())
		}
	}
}
