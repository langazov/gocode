package signin

import (
	"context"
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/langazov/gocode-go/internal/tui/theme"
)

type recorder struct {
	calls    int
	register bool
	creds    Credentials
	result   Result
	err      error
}

func (r *recorder) submit(_ context.Context, register bool, c Credentials) (Result, error) {
	r.calls++
	r.register, r.creds = register, c
	return r.result, r.err
}

func newTestModel(opts Options, rec *recorder) *model {
	opts.Theme = theme.Dark()
	opts.Submit = rec.submit
	return newModel(context.Background(), opts)
}

func keyMsg(k string) tea.KeyPressMsg {
	switch k {
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "tab":
		return tea.KeyPressMsg{Code: tea.KeyTab}
	case "shift+tab":
		return tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	case "ctrl+c":
		return tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}
	}
	r := []rune(k)[0]
	return tea.KeyPressMsg{Code: r, Text: k}
}

// press sends keys, discarding commands (cursor blinks sleep).
func press(m *model, keys ...string) tea.Cmd {
	var last tea.Cmd
	for _, k := range keys {
		_, last = m.Update(keyMsg(k))
	}
	return last
}

func typeText(m *model, text string) {
	for _, r := range text {
		m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
}

// settle runs a submit command — a batch of the spinner's first tick and
// the Submit call, neither of which sleeps — and feeds the result back.
func settle(t *testing.T, m *model, cmd tea.Cmd) {
	t.Helper()
	if cmd == nil {
		t.Fatal("expected a submit command")
	}
	var run func(tea.Cmd)
	run = func(c tea.Cmd) {
		switch msg := c().(type) {
		case tea.BatchMsg:
			for _, sub := range msg {
				if sub != nil {
					run(sub)
				}
			}
		case submitDoneMsg:
			m.Update(msg)
		}
	}
	run(cmd)
}

func TestRegisterFromMenu(t *testing.T) {
	rec := &recorder{result: Result{Name: "alice", Email: "alice@example.com", KeyPrefix: "gk_ab12", Path: "/tmp/gocoder.json"}}
	m := newTestModel(Options{AllowSkip: true}, rec)

	press(m, "enter") // "Create an account" is first
	if m.screen != ScreenRegister {
		t.Fatalf("screen = %v, want register", m.screen)
	}
	typeText(m, "alice@example.com")
	press(m, "tab", "tab") // leave the display name to its default
	typeText(m, "correct-horse")
	press(m, "tab")
	typeText(m, "correct-horse")
	settle(t, m, press(m, "enter"))

	if rec.calls != 1 || !rec.register {
		t.Fatalf("submit calls=%d register=%v", rec.calls, rec.register)
	}
	if rec.creds != (Credentials{Email: "alice@example.com", DisplayName: "alice", Password: "correct-horse"}) {
		t.Fatalf("creds = %+v", rec.creds)
	}
	if m.screen != screenDone || !strings.Contains(m.render(), "Welcome, alice.") {
		t.Fatalf("screen = %v, render:\n%s", m.screen, m.render())
	}
	press(m, "enter")
	if !m.quitting || m.outcome.Status != StatusSignedIn || m.outcome.Result.KeyPrefix != "gk_ab12" {
		t.Fatalf("outcome = %+v", m.outcome)
	}
	if !strings.Contains(m.doneLine(), "Signed in to gocoder.org as alice <alice@example.com>") {
		t.Fatalf("done line = %q", m.doneLine())
	}
}

func TestRegisterValidationBlocksSubmit(t *testing.T) {
	rec := &recorder{}
	m := newTestModel(Options{Start: ScreenRegister}, rec)

	typeText(m, "alice@example.com")
	press(m, "tab", "tab")
	typeText(m, "correct-horse")
	press(m, "tab")
	typeText(m, "correct-hose")
	press(m, "enter")

	if rec.calls != 0 || m.busy {
		t.Fatalf("submitted despite a mismatch: calls=%d busy=%v", rec.calls, m.busy)
	}
	if m.errField != 3 || m.focus != 3 || !strings.Contains(m.render(), "passwords don't match") {
		t.Fatalf("errField=%d focus=%d render:\n%s", m.errField, m.focus, m.render())
	}
	// Fixing the field clears the complaint.
	typeText(m, "x")
	if m.formErr != "" {
		t.Fatalf("error not cleared on edit: %q", m.formErr)
	}
}

func TestInvalidEmailFocusesEmail(t *testing.T) {
	m := newTestModel(Options{Start: ScreenLogin}, &recorder{})
	typeText(m, "not-an-email")
	press(m, "tab")
	typeText(m, "whatever1")
	press(m, "enter")
	if m.errField != 0 || m.focus != 0 {
		t.Fatalf("errField=%d focus=%d", m.errField, m.focus)
	}
}

func TestSubmitErrorIsShownAndReported(t *testing.T) {
	rec := &recorder{err: errors.New("invalid email or password")}
	m := newTestModel(Options{AllowSkip: true}, rec)

	press(m, "down", "enter") // "Log in"
	typeText(m, "alice@example.com")
	press(m, "tab")
	typeText(m, "wrong-password")
	settle(t, m, press(m, "enter"))

	if m.screen != ScreenLogin || m.busy || !strings.Contains(m.render(), "invalid email or password") {
		t.Fatalf("screen=%v busy=%v render:\n%s", m.screen, m.busy, m.render())
	}
	press(m, "esc") // back to the menu, email remembered
	if m.screen != ScreenMenu || m.email != "alice@example.com" {
		t.Fatalf("screen=%v email=%q", m.screen, m.email)
	}
	press(m, "esc") // skip
	if m.outcome.Status != StatusSkipped || m.outcome.LastErr == nil {
		t.Fatalf("outcome = %+v", m.outcome)
	}
}

func TestDirectFormEscCancels(t *testing.T) {
	m := newTestModel(Options{Start: ScreenLogin}, &recorder{})
	press(m, "esc")
	if !m.quitting || m.outcome.Status != StatusCancelled {
		t.Fatalf("outcome = %+v", m.outcome)
	}
}

func TestCtrlCCancelsAnywhere(t *testing.T) {
	m := newTestModel(Options{AllowSkip: true}, &recorder{})
	press(m, "enter")
	typeText(m, "a")
	press(m, "ctrl+c")
	if m.outcome.Status != StatusCancelled {
		t.Fatalf("outcome = %+v", m.outcome)
	}
}

func TestPasswordsNeverRenderInPlainText(t *testing.T) {
	m := newTestModel(Options{Start: ScreenLogin}, &recorder{})
	typeText(m, "alice@example.com")
	press(m, "tab")
	typeText(m, "hunter2secret")
	if out := m.render(); strings.Contains(out, "hunter2secret") || !strings.Contains(out, "•••") {
		t.Fatalf("password leaked or not masked:\n%s", out)
	}
}

func TestMenuWithoutSkip(t *testing.T) {
	m := newTestModel(Options{}, &recorder{})
	if len(m.menuItems()) != 2 || strings.Contains(m.render(), "Skip for now") {
		t.Fatalf("skip offered when not allowed:\n%s", m.render())
	}
	press(m, "esc")
	if m.outcome.Status != StatusCancelled {
		t.Fatalf("outcome = %+v", m.outcome)
	}
}

func TestStrength(t *testing.T) {
	for _, tc := range []struct {
		password string
		score    int
	}{
		{"short", 0},
		{"alllowercase", 2},
		{"Mixedcase", 2},
		{"Mixedcase12", 3},
		{"Much-Longer-Passphrase-42", 4},
	} {
		if got, _ := strength(tc.password); got != tc.score {
			t.Errorf("strength(%q) = %d, want %d", tc.password, got, tc.score)
		}
	}
}
