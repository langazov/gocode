package tui

import (
	"encoding/json"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/langazov/gocode-go/internal/tui/client"
)

// This file covers batch scoping: every task call in one assistant message
// shares that message's ID as its batchID (published via ExecContext.SetMeta),
// and the subagent view's arrows and the children overlay's groups are scoped
// by it. Opening a task from one fan-out must never cycle into another.

// batchTimeline builds a parent timeline with two fan-out messages: batch A
// (msgA) launched a1+a2, batch B (msgB) launched b1.
func batchTimeline() []client.Message {
	return []client.Message{
		{ID: "msg_u", SessionID: "ses_p", Type: "user", Seq: 0, Data: json.RawMessage(`{"text":"go"}`)},
		{ID: "msgA", SessionID: "ses_p", Type: "assistant", Seq: 1, Data: json.RawMessage(
			`{"agent":"build","content":[` +
				`{"type":"tool","id":"call_a1","name":"task","state":{"status":"completed","metadata":{"sessionID":"ses_a1","batchID":"msgA"}}},` +
				`{"type":"tool","id":"call_a2","name":"task","state":{"status":"completed","metadata":{"sessionID":"ses_a2","batchID":"msgA"}}}` +
				`],"finish":"tool_use"}`)},
		{ID: "msgB", SessionID: "ses_p", Type: "assistant", Seq: 2, Data: json.RawMessage(
			`{"agent":"build","content":[` +
				`{"type":"tool","id":"call_b1","name":"task","state":{"status":"completed","metadata":{"sessionID":"ses_b1","batchID":"msgB"}}}` +
				`],"finish":"tool_use"}`)},
	}
}

// batchApp opens a subagent of ses_p with the sibling set and parent timeline
// seeded, so batchSiblings() has everything it needs without HTTP.
func batchApp(t *testing.T, openChild string) *App {
	t.Helper()
	_, server := newMockAPI(t)
	app := newTestApp(t, server.URL)
	app.width, app.height = 160, 40
	app.view = viewChat
	app.active = &client.Session{ID: openChild, ParentID: "ses_p", Title: "@general subagent"}
	app.subagentSiblings = []client.Session{
		{ID: "ses_a1", ParentID: "ses_p", TimeCreated: 100},
		{ID: "ses_a2", ParentID: "ses_p", TimeCreated: 200},
		{ID: "ses_b1", ParentID: "ses_p", TimeCreated: 300},
	}
	app.parentMessages = batchTimeline()
	return app
}

// taskBatchOf must read the published batchID off the task part, falling
// back to the owning message for parts written before the key existed.
func TestTaskBatchOf(t *testing.T) {
	timeline := batchTimeline()
	for child, want := range map[string]string{
		"ses_a1": "msgA", "ses_a2": "msgA", "ses_b1": "msgB",
	} {
		if got := taskBatchOf(timeline, child); got != want {
			t.Fatalf("taskBatchOf(%s) = %q, want %q", child, got, want)
		}
	}
	if got := taskBatchOf(timeline, "ses_fork"); got != "" {
		t.Fatalf("an unlinked child has no batch, got %q", got)
	}
}

// batchSiblings narrows to the open child's batch, and a missing parent
// timeline falls back to the full sibling list rather than breaking arrows.
func TestBatchSiblingsScopesArrows(t *testing.T) {
	app := batchApp(t, "ses_a2")
	scoped := app.batchSiblings()
	if len(scoped) != 2 || scoped[0].ID != "ses_a1" || scoped[1].ID != "ses_a2" {
		ids := make([]string, len(scoped))
		for i, s := range scoped {
			ids[i] = s.ID
		}
		t.Fatalf("batch A's arrows must cover only ses_a1+ses_a2, got %v", ids)
	}

	// A child from the other batch sees only its own.
	app.active = &client.Session{ID: "ses_b1", ParentID: "ses_p", Title: "@general subagent"}
	scoped = app.batchSiblings()
	if len(scoped) != 1 || scoped[0].ID != "ses_b1" {
		t.Fatalf("batch B's arrows must cover only ses_b1, got %d sessions", len(scoped))
	}

	// No parent timeline: the full sibling list is the fallback.
	app.parentMessages = nil
	if got := len(app.batchSiblings()); got != 3 {
		t.Fatalf("without batch info the full sibling list applies, got %d sessions", got)
	}
}

// The footer's (n of N) counts the batch, not every sibling, and left/right
// cycle within the batch — never into another one.
func TestArrowsCycleWithinBatchOnly(t *testing.T) {
	app := batchApp(t, "ses_a1")

	// Footer position: ses_a1 is 1 of 2 within batch A (not 1 of 3).
	view := ansi.Strip(app.viewChat())
	if !strings.Contains(view, "(1 of 2)") {
		t.Fatalf("the subagent footer should count within the batch, missing (1 of 2):\n%s", view)
	}

	// right: a1 → a2 (same batch), then wraps within the batch, never b1.
	msg := driveFirst(t, app.handleKey(tea.KeyPressMsg{Code: tea.KeyRight}))
	if opened, ok := msg.(sessionOpenedMsg); !ok || opened.session == nil || opened.session.ID != "ses_a2" {
		t.Fatalf("right should move to the batch sibling ses_a2, got %#v", msg)
	}
	app.active = &client.Session{ID: "ses_a2", ParentID: "ses_p", Title: "@general subagent"}
	msg = driveFirst(t, app.handleKey(tea.KeyPressMsg{Code: tea.KeyRight}))
	if opened, ok := msg.(sessionOpenedMsg); !ok || opened.session == nil || opened.session.ID != "ses_a1" {
		t.Fatalf("right at the batch edge should wrap to ses_a1, not cross batches, got %#v", msg)
	}
}

// The children overlay groups running subagents by batch so two fan-outs
// read as two groups.
func TestChildrenOverlayGroupsByBatch(t *testing.T) {
	api, server := newMockAPI(t)
	app := newTestApp(t, server.URL)
	openSession(t, app)
	app.timeline = batchTimeline()

	running := newSessionNode("ses_a1")
	running.Busy = true
	running2 := newSessionNode("ses_a2")
	running2.Busy = true
	other := newSessionNode("ses_b1")
	other.Busy = true
	app.Update(snapshotMsg{snapshot: Snapshot{Sessions: map[string]*SessionNode{
		"ses_a1": running, "ses_a2": running2, "ses_b1": other,
	}}})
	api.children = []client.Session{
		{ID: "ses_a1", Title: "alpha (@general subagent)", Directory: "/tmp", Version: "1", TimeCreated: 100},
		{ID: "ses_a2", Title: "beta (@general subagent)", Directory: "/tmp", Version: "1", TimeCreated: 200},
		{ID: "ses_b1", Title: "gamma (@general subagent)", Directory: "/tmp", Version: "1", TimeCreated: 300},
	}

	driveCmd(t, app, app.childrenOverlay())
	if app.overlay == nil {
		t.Fatal("expected the children overlay to open")
	}
	view := ansi.Strip(app.View())
	for _, want := range []string{"Batch 1", "Batch 2", "alpha", "beta", "gamma"} {
		if !strings.Contains(view, want) {
			t.Fatalf("overlay missing %q:\n%s", want, view)
		}
	}
	// alpha and beta share Batch 1's header; gamma is alone in Batch 2.
	if strings.Count(view, "Batch 1") != 1 || strings.Count(view, "Batch 2") != 1 {
		t.Fatalf("each batch header should render once:\n%s", view)
	}
}
