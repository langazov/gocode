package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// TestSlashNewCreatesASession is the regression for "typing /new and pressing
// enter is not working".
//
// The registry's session.new action returned reloadMsg, which only reloads an
// already-open session's messages and is a no-op on the home screen. It never
// created anything. The palette entry had been written as a listing and never
// wired to newSession, the way the ctrl+x n binding is.
func TestSlashNewCreatesASession(t *testing.T) {
	api, server := newMockAPI(t)
	app := newTestApp(t, server.URL)
	before := api.created

	cmd := app.runSlashCommand("new")
	if cmd == nil {
		t.Fatal("/new produced no command")
	}
	drive(t, app, cmd())

	if api.created == before {
		t.Fatalf("/new created no session (POST /api/session called %d times, was %d)", api.created, before)
	}
}

// TestSlashCommandsAllDoSomething is the systematic form of "other commands
// are not working either": every command reachable by "/" must produce an
// observable effect. A command that returns nothing from the home screen is
// indistinguishable from a broken one.
func TestSlashCommandsAllDoSomething(t *testing.T) {
	_, server := newMockAPI(t)

	// Commands that legitimately do nothing without an open session say so
	// with a status message, which counts as an effect.
	for _, name := range []string{
		"new", "sessions", "models", "agents", "themes", "help", "status",
		"compact", "timeline", "rename", "delete", "interrupt",
	} {
		t.Run(name, func(t *testing.T) {
			app := newTestApp(t, server.URL)
			app.width, app.height = 120, 40
			before := snapshotEffects(app)

			cmd := app.runSlashCommand(name)
			if cmd == nil {
				// No command is only acceptable if the registry item changed
				// state directly.
				if snapshotEffects(app) == before {
					t.Fatalf("/%s did nothing at all", name)
				}
				return
			}
			msg := cmd()
			// Update, not drive: driving would also run the follow-up ticks
			// (a toast expiry is a 3s Tick), which runCmds executes for real
			// and would add seconds per command for no extra coverage.
			app.Update(msg)

			if snapshotEffects(app) == before && msg == nil {
				t.Fatalf("/%s produced no observable effect", name)
			}
		})
	}
}

// snapshotEffects captures the interface state a command could plausibly
// change, so "did anything happen" can be asked without enumerating each
// command's specific outcome.
func snapshotEffects(app *App) string {
	var builder strings.Builder
	if app.overlay != nil {
		builder.WriteString("overlay:" + app.overlay.title + ";")
	}
	if app.active != nil {
		builder.WriteString("session:" + app.active.ID + ";")
	}
	builder.WriteString("view:" + itoa(app.view) + ";")
	builder.WriteString("status:" + app.statusMsg + ";")
	builder.WriteString("sidebar:" + boolText(app.sidebar) + ";")
	return builder.String()
}

func boolText(value bool) string {
	if value {
		return "1"
	}
	return "0"
}

// TestSlashCompactIsNotAPlaceholder: /compact used to answer with a message
// saying compaction happens automatically, even though the endpoint and the
// ctrl+x c binding both exist.
func TestSlashCompactIsNotAPlaceholder(t *testing.T) {
	_, server := newMockAPI(t)
	app := newTestApp(t, server.URL)
	openSession(t, app)

	cmd := app.runSlashCommand("compact")
	if cmd == nil {
		t.Fatal("/compact produced no command")
	}
	if status, ok := cmd().(statusMsg); ok && strings.Contains(status.text, "automatically") {
		t.Errorf("/compact is still a placeholder: %q", status.text)
	}
}

// TestSlashModelsOpensTheDialog covers the commands whose action returns a
// command that has to be run for the effect to land.
func TestSlashModelsOpensTheDialog(t *testing.T) {
	_, server := newMockAPI(t)
	app := newTestApp(t, server.URL)
	app.width, app.height = 120, 40

	cmd := app.runSlashCommand("models")
	if app.overlay == nil {
		t.Fatal("/models did not open a dialog")
	}
	if app.overlay.title != "Select model" {
		t.Errorf("dialog title = %q", app.overlay.title)
	}
	if cmd != nil {
		drive(t, app, cmd())
	}
}

// TestEnterThroughThePopupRunsTheCommand is the reported gesture end to end:
// type the name, press enter, and the command runs.
func TestEnterThroughThePopupRunsTheCommand(t *testing.T) {
	api, server := newMockAPI(t)
	app := newTestApp(t, server.URL)
	app.width, app.height = 120, 40
	before := api.created

	for _, r := range "/new" {
		drive(t, app, tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	if !app.autocomplete.visible() {
		t.Fatal("the popup should be open")
	}
	drive(t, app, tea.KeyPressMsg{Code: tea.KeyEnter})

	if api.created == before {
		t.Error("enter through the popup did not create a session")
	}
}
