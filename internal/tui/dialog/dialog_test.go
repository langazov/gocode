package dialog

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

func testPalette() Palette {
	return Palette{
		Primary:              lipgloss.Color("#fab283"),
		Accent:               lipgloss.Color("#9d7cd8"),
		Error:                lipgloss.Color("#e06c75"),
		Success:              lipgloss.Color("#7fd88f"),
		Text:                 lipgloss.Color("#eeeeee"),
		TextMuted:            lipgloss.Color("#808080"),
		BackgroundPanel:      lipgloss.Color("#141414"),
		BackgroundElement:    lipgloss.Color("#1e1e1e"),
		SelectedListItemText: lipgloss.Color("#0a0a0a"),
	}
}

func newShell(t *testing.T, s *Shell) *Shell {
	t.Helper()
	s.SetGeometry(100, 34, testPalette())
	return s
}

// The spike gate: a custom huh.Field renders inside a huh.Form under the
// shell, with the spec's row geometry intact.
func TestListFieldRendersInsideHuhForm(t *testing.T) {
	s := newShell(t, NewList("Commands", []Item{
		{Label: "alpha", Value: "a"},
		{Label: "beta", Value: "b", Category: "Group"},
	}))
	panel, _ := s.Panel()
	if panel == "" {
		t.Fatal("empty panel")
	}
	plain := ansi.Strip(panel)
	if !strings.Contains(plain, "alpha") || !strings.Contains(plain, "beta") {
		t.Fatalf("rows missing: %q", plain)
	}
	if !strings.Contains(plain, "Group") {
		t.Fatalf("category header missing: %q", plain)
	}
	// The embedded form is live and holds the field, and it is wired to the
	// same width/theme the shell renders with.
	if s.form == nil {
		t.Fatal("no form embedded")
	}
	if s.list.r.width != s.Width() {
		t.Fatalf("field width %d should track the panel width %d", s.list.r.width, s.Width())
	}
}

func TestKeyRouting(t *testing.T) {
	s := newShell(t, NewList("L", []Item{
		{Label: "one", Value: "1"},
		{Label: "two", Value: "2"},
		{Label: "three", Value: "3"},
	}))
	s.Key("down")
	if v := s.SelectedValue(); v != "2" {
		t.Fatalf("down should move to the second row, got %q", v)
	}
	// j and k are characters, not pager keys.
	s.Key("j")
	s.Key("k")
	if f := s.Filter(); f != "jk" {
		t.Fatalf("j/k should type into the filter, got %q", f)
	}
	if len(s.Items()) != 0 {
		t.Fatalf("a filter matching nothing empties the list, got %d rows", len(s.Items()))
	}
	s.Key("backspace")
	s.Key("backspace")
	if len(s.Items()) != 3 {
		t.Fatalf("backspace should restore the rows, got %d", len(s.Items()))
	}
}

func TestEnterDispatchesItemAction(t *testing.T) {
	ran := false
	s := newShell(t, NewList("L", []Item{
		{Label: "one", Value: "1", Action: func() tea.Msg { ran = true; return nil }},
	}))
	cmd := s.Key("enter")
	if ran {
		t.Fatal("the action must not run before the close lands (CloseThenMsg)")
	}
	if cmd == nil {
		t.Fatal("enter should return the close-then-run command")
	}
	// Run it: the close marker resolves first, and the App-side handler
	// (App.wrapDialogCmd) would run the thunk. Simulate that half.
	msg := cmd()
	ct, ok := msg.(CloseThenMsg)
	if !ok {
		t.Fatalf("enter should carry CloseThenMsg, got %T", msg)
	}
	if ct.Then != nil {
		if inner := ct.Then(); inner != nil {
			inner()
		}
	}
	if !ran {
		t.Fatal("the thunk should have run the action")
	}
}

func TestFooterActionFocusAndArming(t *testing.T) {
	s := newShell(t, NewList("L", []Item{{Label: "one", Value: "1"}}))
	s.SetActions([]Action{{Title: "del", Keys: "ctrl+d"}})
	s.Key("tab")
	if s.list.focusedAction != 0 {
		t.Fatalf("tab should focus the action, got %d", s.list.focusedAction)
	}
	s.Key("down")
	if s.list.focusedAction != -1 {
		t.Fatal("moving the selection should release action focus")
	}
	// The armed row relabels itself.
	s.Arm("1", "ctrl+d")
	panel, _ := s.Panel()
	if !strings.Contains(panel, "Press ctrl+d again to confirm") {
		t.Fatalf("armed row should carry the confirm label: %q", panel)
	}
}

func TestConfirmAndAlertLayout(t *testing.T) {
	s := newShell(t, NewAlert("Retry", "the model refused", nil))
	panel, hits := s.Panel()
	if hits.ButtonRow < 0 || len(hits.Buttons) != 1 {
		t.Fatalf("alert should record one button span, got %+v", hits)
	}
	if !strings.Contains(panel, "Retry") {
		t.Fatal("alert title missing")
	}

	c := newShell(t, NewConfirm("Delete", "sure?", "", nil, nil))
	panel = mustPanel(t, c)
	if !strings.Contains(panel, "Cancel") || !strings.Contains(panel, "Confirm") {
		t.Fatalf("confirm buttons missing: %q", panel)
	}
	c.Key("left")
	if c.confirmActive {
		t.Fatal("left should move off confirm")
	}
}

func mustPanel(t *testing.T, s *Shell) string {
	t.Helper()
	p, _ := s.Panel()
	return p
}

func TestNoteScrolling(t *testing.T) {
	lines := make([]string, 40)
	for i := range lines {
		lines[i] = strings.Repeat("x", 5)
	}
	s := newShell(t, NewNote(KindStats, func(p Palette, w int) []string { return lines },
		func(height int) int { return 10 }, nil))
	s.Key("down")
	s.Key("down")
	panel, _ := s.Panel()
	_ = panel
	// scrollTop advanced two rows and stays clamped by the render.
	if s.note.scrollTop != 2 {
		t.Fatalf("scrollTop = %d, want 2", s.note.scrollTop)
	}
	s.ScrollEnd()
	s.Panel()
	// The indicator costs a row, so the last window starts one earlier than
	// the raw budget suggests: budget-1 rows visible of len(lines).
	if s.note.scrollTop > len(lines)-9 {
		t.Fatalf("end should clamp near the bottom, got %d", s.note.scrollTop)
	}
}
