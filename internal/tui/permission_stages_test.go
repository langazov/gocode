package tui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/langazov/gocode-go/internal/tui/client"
)

// captureReplyServer answers the reply routes, recording what was posted so a
// test can assert the exact reply (and message) the banner sent.
func captureReplyServer(t *testing.T) (*httptest.Server, *[]map[string]any) {
	t.Helper()
	posted := &[]map[string]any{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		*posted = append(*posted, body)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(server.Close)
	return server, posted
}

// TestAlwaysRequiresConfirmation pins the always-confirmation stage: pressing
// enter on "Allow always" must not post anything until the Confirm stage is
// accepted, and esc returns to the option bar without granting. This is the
// P6 surface fix — a durable grant whose scope is shown before it commits.
func TestAlwaysRequiresConfirmation(t *testing.T) {
	server, posted := captureReplyServer(t)
	app := newTestApp(t, server.URL)
	app.permission = &client.PermissionRequest{
		ID:        "perm_1",
		SessionID: "ses_1",
		Action:    "edit",
		Resources: []string{"a.go"},
		Save:      []string{"*"},
	}
	app.permissionChoice = 1 // Allow always

	// Enter on "Allow always" opens the confirmation, posts nothing.
	drive(t, app, tea.KeyPressMsg{Code: tea.KeyEnter})
	if app.permissionStage != permissionStageConfirmAlways {
		t.Fatalf("stage = %v, want confirm-always", app.permissionStage)
	}
	if len(*posted) != 0 {
		t.Fatalf("enter on always posted %d replies, want 0", len(*posted))
	}
	// The confirmation names the exact pattern that would be saved, derived
	// from Save rather than the resource.
	banner := app.permissionConfirmAlwaysBanner()
	if !ansiContains(banner, "- *") {
		t.Fatalf("confirmation does not show the save pattern:\n%s", ansiStrip(banner))
	}
	// esc retreats without granting.
	drive(t, app, tea.KeyPressMsg{Code: tea.KeyEscape})
	if app.permissionStage != permissionStageOptions {
		t.Fatalf("esc did not return to options: %v", app.permissionStage)
	}
	if len(*posted) != 0 {
		t.Fatalf("esc posted %d replies, want 0", len(*posted))
	}
	// Confirming posts exactly one always.
	drive(t, app, tea.KeyPressMsg{Code: tea.KeyEnter}) // -> stage confirm
	drive(t, app, tea.KeyPressMsg{Code: tea.KeyEnter}) // -> confirm selected
	if len(*posted) != 1 {
		t.Fatalf("confirm posted %d replies, want 1", len(*posted))
	}
	if reply := (*posted)[0]["reply"]; reply != "always" {
		t.Fatalf("reply = %v, want always", reply)
	}
}

// TestRejectReasonFlow pins the reject stage: the typed reason is posted as
// the message, and enter rejects even when nothing was typed.
func TestRejectReasonFlow(t *testing.T) {
	server, posted := captureReplyServer(t)
	app := newTestApp(t, server.URL)
	app.permission = &client.PermissionRequest{
		ID: "perm_1", SessionID: "ses_1", Action: "bash", Resources: []string{"rm -rf /tmp/x"},
	}

	drive(t, app, tea.KeyPressMsg{Code: 'n'}) // reject -> stage reason
	if app.permissionStage != permissionStageRejectReason {
		t.Fatalf("stage = %v, want reject-reason", app.permissionStage)
	}
	for _, r := range "use a temp dir" {
		drive(t, app, tea.KeyPressMsg{Text: string(r), Code: r})
	}
	drive(t, app, tea.KeyPressMsg{Code: tea.KeyEnter})
	if len(*posted) != 1 {
		t.Fatalf("posted %d replies, want 1", len(*posted))
	}
	if reply := (*posted)[0]["reply"]; reply != "reject" {
		t.Fatalf("reply = %v, want reject", reply)
	}
	if message := (*posted)[0]["message"]; message != "use a temp dir" {
		t.Fatalf("message = %v, want the typed reason", message)
	}
}

// TestRejectEscapeSkipsReason pins esc inside the reason stage: it rejects
// immediately without a reason, never returns to the option bar (the user has
// already said no; making them say it twice is friction on a refusal).
func TestRejectEscapeSkipsReason(t *testing.T) {
	server, posted := captureReplyServer(t)
	app := newTestApp(t, server.URL)
	app.permission = &client.PermissionRequest{
		ID: "perm_1", SessionID: "ses_1", Action: "bash", Resources: []string{"ls"},
	}
	drive(t, app, tea.KeyPressMsg{Code: tea.KeyEscape})
	if app.permissionStage != permissionStageRejectReason {
		t.Fatalf("esc at options should open the reason stage, got %v", app.permissionStage)
	}
	drive(t, app, tea.KeyPressMsg{Code: tea.KeyEscape})
	if len(*posted) != 1 {
		t.Fatalf("posted %d replies, want 1", len(*posted))
	}
	if reply := (*posted)[0]["reply"]; reply != "reject" {
		t.Fatalf("reply = %v, want reject", reply)
	}
	if message, ok := (*posted)[0]["message"]; ok && message != "" {
		t.Fatalf("message = %v, want none", message)
	}
}

// TestOnceStillAnswersDirectly pins that only always and reject grow a stage:
// once is exactly as quick as before.
func TestOnceStillAnswersDirectly(t *testing.T) {
	server, posted := captureReplyServer(t)
	app := newTestApp(t, server.URL)
	app.permission = &client.PermissionRequest{
		ID: "perm_1", SessionID: "ses_1", Action: "read", Resources: []string{"a.go"},
	}
	drive(t, app, tea.KeyPressMsg{Code: 'y'})
	if len(*posted) != 1 {
		t.Fatalf("posted %d replies, want 1", len(*posted))
	}
	if reply := (*posted)[0]["reply"]; reply != "once" {
		t.Fatalf("reply = %v, want once", reply)
	}
}

// ansiStrip removes ANSI escapes so assertions do not depend on where styling
// was applied.
func ansiStrip(s string) string {
	var out strings.Builder
	inEscape := false
	for _, r := range s {
		switch {
		case r == '\x1b':
			inEscape = true
		case inEscape:
			if r == 'm' {
				inEscape = false
			}
		default:
			out.WriteRune(r)
		}
	}
	return out.String()
}

// ansiContains reports whether the plain text of s contains substr.
func ansiContains(s, substr string) bool {
	return strings.Contains(ansiStrip(s), substr)
}
