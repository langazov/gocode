package tui

import (
	"strings"
	"testing"
)

// What reaches handleOverlayKey is a key *name* from tea.KeyMsg.String(), not
// the character typed. Testing it with len(key) == 1 was wrong twice: the
// space bar names itself "space" (five bytes), and len counts bytes, so every
// multi-byte character was dropped too.
func TestTypedText(t *testing.T) {
	typed := map[string]string{
		"space": " ",
		"a":     "a",
		"A":     "A",
		"-":     "-",
		"é":     "é",
		"世":     "世",
		"€":     "€",
	}
	for key, want := range typed {
		got, ok := typedText(key)
		if !ok || got != want {
			t.Errorf("typedText(%q) = (%q, %v), want (%q, true)", key, got, ok, want)
		}
	}

	// Key names and chords contribute no text. Every one of these is several
	// runes long, which is what makes the rune-count test safe.
	for _, key := range []string{
		"enter", "shift+enter", "esc", "tab", "shift+tab", "backspace", "delete",
		"up", "down", "left", "right", "home", "end", "pgup", "pgdown",
		"ctrl+a", "ctrl+c", "ctrl+d", "f1",
	} {
		if got, ok := typedText(key); ok {
			t.Errorf("typedText(%q) = (%q, true), want no text", key, got)
		}
	}
}

// The bug as the user hits it: a memory is a sentence, and without spaces you
// cannot write one.
func TestInputDialogAcceptsSpaces(t *testing.T) {
	app, state := memoryTestApp(t)
	openMemoryDialog(t, app)
	driveCmd(t, app, app.handleOverlayKey("ctrl+a"))

	for _, key := range []string{"r", "u", "n", "space", "m", "a", "k", "e"} {
		driveCmd(t, app, app.handleOverlayKey(key))
	}
	if app.overlay.input != "run make" {
		t.Fatalf("input = %q, want %q", app.overlay.input, "run make")
	}

	applyCmd(t, app, app.handleOverlayKey("enter"))
	saved := state.snapshot()
	if len(saved) != 1 || saved[0].Content != "run make" {
		t.Errorf("saved = %+v, want the spaced content", saved)
	}
}

func TestInputDialogAcceptsNonASCII(t *testing.T) {
	app, _ := memoryTestApp(t)
	openMemoryDialog(t, app)
	driveCmd(t, app, app.handleOverlayKey("ctrl+a"))

	for _, key := range []string{"c", "a", "f", "é", "space", "世", "界"} {
		driveCmd(t, app, app.handleOverlayKey(key))
	}
	if app.overlay.input != "café 世界" {
		t.Errorf("input = %q, want %q", app.overlay.input, "café 世界")
	}
}

// enter submits, so a newline needs its own chord.
func TestInputDialogShiftEnterInsertsNewline(t *testing.T) {
	app, state := memoryTestApp(t)
	openMemoryDialog(t, app)
	driveCmd(t, app, app.handleOverlayKey("ctrl+a"))

	for _, key := range []string{"a", "shift+enter", "b"} {
		driveCmd(t, app, app.handleOverlayKey(key))
	}
	if app.overlay.input != "a\nb" {
		t.Fatalf("input = %q, want %q", app.overlay.input, "a\nb")
	}

	// The panel must render the extra line rather than smuggling a raw
	// newline into a composited row, which would tear the dialog.
	app.width, app.height = 100, 30
	panel := app.inputOverlay(dialogMedium)
	rows := strings.Split(panel, "\n")
	var withA, withB bool
	for _, row := range rows {
		if strings.Contains(row, "a") && !strings.Contains(row, "b") {
			withA = true
		}
		if strings.Contains(row, "b") {
			withB = true
		}
	}
	if !withA || !withB {
		t.Errorf("multi-line input did not render on separate rows:\n%s", panel)
	}
	if !strings.Contains(panel, "shift+enter") {
		t.Errorf("the newline chord is not advertised:\n%s", panel)
	}

	applyCmd(t, app, app.handleOverlayKey("enter"))
	saved := state.snapshot()
	if len(saved) != 1 || saved[0].Content != "a\nb" {
		t.Errorf("saved = %+v, want the newline preserved", saved)
	}
}

// A single-line entry must keep the height the dialog has always had, so the
// growth only happens when there is something to grow for.
func TestInputDialogHeightStableForSingleLine(t *testing.T) {
	app, _ := memoryTestApp(t)
	openMemoryDialog(t, app)
	driveCmd(t, app, app.handleOverlayKey("ctrl+a"))
	app.overlay.input = "one line"
	single := len(strings.Split(app.inputOverlay(dialogMedium), "\n"))

	app.overlay.input = "one\ntwo\nthree\nfour\nfive"
	taller := len(strings.Split(app.inputOverlay(dialogMedium), "\n"))

	if single != 8 {
		t.Errorf("single-line panel is %d rows, want the original 8", single)
	}
	if taller <= single {
		t.Errorf("a 5-line entry rendered %d rows, not more than the %d of one line", taller, single)
	}
}

// The same decoding backs the list filter, so searching for a multi-word item
// was broken in every dialog, not just this one.
func TestListFilterAcceptsSpaces(t *testing.T) {
	app, _ := memoryTestApp(t, sampleMemories()...)
	o := openMemoryDialog(t, app)

	for _, key := range []string{"r", "u", "n", "space", "m", "a", "k", "e"} {
		driveCmd(t, app, app.handleOverlayKey(key))
	}
	if o.filter != "run make" {
		t.Fatalf("filter = %q, want %q", o.filter, "run make")
	}
	if len(o.items) != 1 {
		t.Fatalf("filter matched %d rows, want the one containing the phrase", len(o.items))
	}
	if !strings.Contains(o.items[0].label, "Run make check") {
		t.Errorf("matched the wrong row: %q", o.items[0].label)
	}
}
