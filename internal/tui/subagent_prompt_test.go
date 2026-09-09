package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/langazov/gocode-go/internal/tui/client"
)

// This file covers the subagent view's prompt handling: a child session
// mounts no prompt (routes/session/index.tsx's `visible` memo gates the
// `<Show>` around the Prompt), so none of the prompt's keys — history
// recall, slash/mention completion, submit, plain typing — may fire there,
// and the keys the SubagentFooter advertises (up/left/right) belong to
// session.parent / session.child.* navigation instead.
//
// The regression this guards: up in a subagent used to walk the prompt
// history (the empty-input guard this port invented let historyKey run
// first on an empty box), so the user who switched into a child with
// /subagent could not get back to the parent with the arrows.

// subagentApp builds an app viewing a child session with one history entry,
// the exact state in which the bug was reported: pressing up recalled
// "earlier prompt" instead of opening the parent.
func subagentApp(t *testing.T) *App {
	t.Helper()
	_, server := newMockAPI(t)
	app := newTestApp(t, server.URL)
	app.width, app.height = 160, 40
	app.view = viewChat
	app.active = &client.Session{ID: "ses_2", ParentID: "ses_1", Title: "@explore subagent"}
	app.subagentSiblings = []client.Session{
		{ID: "ses_1a", ParentID: "ses_1", TimeCreated: 100},
		{ID: "ses_2", ParentID: "ses_1", TimeCreated: 200},
	}
	app.history.Append("earlier prompt")
	return app
}

func TestSubagentViewHasNoPrompt(t *testing.T) {
	app := subagentApp(t)

	if app.promptEnabled() {
		t.Fatal("a subagent's session must not mount the prompt (upstream's visible memo)")
	}

	// The rendered view carries the subagent footer, not the prompt box or
	// its hint row: upstream replaces the one with the other.
	view := ansi.Strip(app.viewChat())
	for _, want := range []string{"Explore", "Parent", "Prev", "Next"} {
		if !strings.Contains(view, want) {
			t.Fatalf("subagent view missing %q:\n%s", want, view)
		}
	}
	for _, unwanted := range []string{"Ask anything", "interrupt", "tab agents"} {
		if strings.Contains(view, unwanted) {
			t.Fatalf("subagent view must not render the prompt block, found %q:\n%s", unwanted, view)
		}
	}
}

// The reported bug: up in a subagent walked the prompt history instead of
// opening the parent session.
func TestUpInSubagentOpensParentNotHistory(t *testing.T) {
	app := subagentApp(t)

	cmd := app.handleKey(tea.KeyPressMsg{Code: tea.KeyUp})
	if got := app.input.Value(); got != "" {
		t.Fatalf("up must not recall prompt history in a subagent, input = %q", got)
	}
	// openParentSession fetches the parent over HTTP before posting
	// sessionOpenedMsg, so the message only exists once the fetch lands.
	msg := driveFirst(t, cmd)
	opened, ok := msg.(sessionOpenedMsg)
	if !ok {
		t.Fatalf("up should open the parent session, got %#v", msg)
	}
	if opened.session == nil || opened.session.ID != "ses_1" {
		t.Fatalf("up should open the parent ses_1, got %#v", opened.session)
	}
}

func TestLeftRightInSubagentCycleSiblings(t *testing.T) {
	app := subagentApp(t)

	msg := driveFirst(t, app.handleKey(tea.KeyPressMsg{Code: tea.KeyLeft}))
	opened, ok := msg.(sessionOpenedMsg)
	if !ok || opened.session == nil || opened.session.ID != "ses_1a" {
		t.Fatalf("left should move to the previous sibling, got %#v", msg)
	}

	app.active = &client.Session{ID: "ses_1a", ParentID: "ses_1"}
	msg = driveFirst(t, app.handleKey(tea.KeyPressMsg{Code: tea.KeyRight}))
	opened, ok = msg.(sessionOpenedMsg)
	if !ok || opened.session == nil || opened.session.ID != "ses_2" {
		t.Fatalf("right should move to the next sibling, got %#v", msg)
	}
}

// Typing, submit, and the completion triggers are prompt bindings; none of
// them may act while the prompt is unmounted.
func TestSubagentViewIgnoresPromptKeys(t *testing.T) {
	app := subagentApp(t)

	app.handleKey(tea.KeyPressMsg{Text: "h", Code: 'h'})
	if got := app.input.Value(); got != "" {
		t.Fatalf("typing must not reach the editor in a subagent, input = %q", got)
	}

	// "/" and "@" open completion popups upstream — only at the prompt.
	app.handleKey(tea.KeyPressMsg{Text: "/", Code: '/'})
	if app.autocomplete.visible() {
		t.Fatal("/ must not open slash completion without a prompt")
	}
	if got := app.input.Value(); got != "" {
		t.Fatalf("/ must not insert into the editor, input = %q", got)
	}

	app.handleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if got := app.input.Value(); got != "" {
		t.Fatalf("enter must not submit from a subagent view, input = %q", got)
	}
}

// Arrow-driven cursor movement is also the textarea's own key handling:
// with no textarea mounted, up/down/left/right fall to route bindings and
// everything else is dropped — the input's cursor must not move.
func TestSubagentViewDropsEditorCursorKeys(t *testing.T) {
	app := subagentApp(t)

	for _, code := range []rune{tea.KeyDown, tea.KeyLeft, tea.KeyRight} {
		app.handleKey(tea.KeyPressMsg{Code: code})
	}
	if app.input.Line() != 0 || app.input.Column() != 0 {
		t.Fatalf("cursor keys must not move the unmounted editor, at line %d column %d",
			app.input.Line(), app.input.Column())
	}
}

// The parent session keeps its prompt and history behavior — the fix must
// not leak into the normal view.
func TestParentViewKeepsHistoryRecall(t *testing.T) {
	app := newTestApp(t, "http://example.invalid")
	app.width, app.height = 160, 40
	app.view = viewChat
	app.active = &client.Session{ID: "ses_1", Title: "root session"}
	app.history.Append("earlier prompt")

	if !app.promptEnabled() {
		t.Fatal("a root session's prompt is mounted")
	}

	app.handleKey(tea.KeyPressMsg{Code: tea.KeyUp})
	if got := app.input.Value(); got != "earlier prompt" {
		t.Fatalf("up should recall prompt history on a root session, input = %q", got)
	}
}

// driveFirst runs cmd and returns the first non-batch message it produces,
// so a test can assert on the one message a keypress actually dispatched.
func driveFirst(t *testing.T, cmd tea.Cmd) tea.Msg {
	t.Helper()
	if cmd == nil {
		return nil
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, inner := range batch {
			if inner == nil {
				continue
			}
			if result := inner(); result != nil {
				return result
			}
		}
		return nil
	}
	return msg
}
