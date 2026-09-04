package tui

import (
	"testing"

	"github.com/langazov/gocode-go/internal/tui/client"
)

// TestAgentSwitchedFollowsServerSideSwitch covers the footer lying about the
// active agent: plan_enter switches the session from inside a turn, so the
// change arrives on the event stream rather than from a key the user pressed.
func TestAgentSwitchedFollowsServerSideSwitch(t *testing.T) {
	app := newTestApp(t, "http://example.invalid")
	app.active = &client.Session{ID: "ses_1"}
	app.activeAgent = "build"

	state := newTree()
	if !state.apply(client.Event{
		Type:    "session.next.agent.switched",
		Session: "ses_1",
		Data:    map[string]any{"agent": "plan"},
	}) {
		t.Fatal("an agent switch must dirty the session")
	}
	effect := app.applySnapshot(state.snapshot(0))

	if app.activeAgent != "plan" {
		t.Fatalf("activeAgent = %q, want plan", app.activeAgent)
	}
	if !effect.timeline {
		t.Fatal("the switch is a timeline entry, so the history needs a refetch")
	}
	if got := app.activeAgentOr("build"); got != "plan" {
		t.Fatalf("the footer would still render %q", got)
	}
}

// A switch in a subagent's session must not move the parent's indicator.
func TestAgentSwitchedIgnoresOtherSessions(t *testing.T) {
	app := newTestApp(t, "http://example.invalid")
	app.active = &client.Session{ID: "ses_1"}
	app.activeAgent = "build"

	state := newTree()
	state.apply(client.Event{
		Type:    "session.next.agent.switched",
		Session: "ses_child",
		Data:    map[string]any{"agent": "plan"},
	})
	app.applySnapshot(state.snapshot(0))

	if app.activeAgent != "build" {
		t.Fatalf("activeAgent = %q, want build", app.activeAgent)
	}
}

// A repeat of the agent already in effect — the echo of the TUI's own Tab,
// which now round-trips through the bus — is not a change.
func TestAgentSwitchedIgnoresRepeat(t *testing.T) {
	state := newTree()
	event := client.Event{
		Type:    "session.next.agent.switched",
		Session: "ses_1",
		Data:    map[string]any{"agent": "plan"},
	}
	if !state.apply(event) {
		t.Fatal("the first switch is a change")
	}
	if state.apply(event) {
		t.Fatal("re-announcing the same agent is not a change")
	}
}
