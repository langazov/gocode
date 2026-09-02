package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// TestPasteInsertsText is the regression for "paste into prompt is not
// working": tea.PasteMsg fell through App.update's switch, so bracketed paste
// was dropped and nothing appeared in the prompt.
func TestPasteInsertsText(t *testing.T) {
	app := typingApp(t, 100, 40)
	app.Update(tea.PasteMsg{Content: "hello from the clipboard"})

	if got := app.input.Value(); got != "hello from the clipboard" {
		t.Errorf("prompt contains %q, want the pasted text", got)
	}
}

// TestPasteAtCursor: a paste lands where the cursor is, not at the end.
func TestPasteAtCursor(t *testing.T) {
	app := typingApp(t, 100, 40)
	typeText(app, "ab")
	app.input.SetCursorColumn(1)
	app.Update(tea.PasteMsg{Content: "XY"})

	if got := app.input.Value(); got != "aXYb" {
		t.Errorf("prompt is %q, want %q", got, "aXYb")
	}
}

// TestPasteNormalizesLineEndings ports the CRLF handling: Windows terminals
// and ConPTY send CR or CRLF inside a bracketed paste, which would otherwise
// arrive as one line with stray carriage returns embedded.
func TestPasteNormalizesLineEndings(t *testing.T) {
	cases := map[string]string{
		"a\r\nb\r\nc": "a\nb\nc",
		"a\rb\rc":     "a\nb\nc",
		"a\nb":        "a\nb",
	}
	for input, want := range cases {
		if got := normalizePastedText(input); got != want {
			t.Errorf("normalizePastedText(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestPasteSummaryThresholds(t *testing.T) {
	cases := []struct {
		name      string
		text      string
		collapsed bool
		want      string
	}{
		{"one short line", "hello", false, ""},
		{"two lines stay inline", "one\ntwo", false, ""},
		{"three lines collapse", "one\ntwo\nthree", true, "[Pasted ~3 lines]"},
		{"long single line collapses", strings.Repeat("x", 151), true, "[Pasted ~1 lines]"},
		{"exactly 150 chars stays inline", strings.Repeat("x", 150), false, ""},
		{"many lines", strings.Repeat("line\n", 41), true, "[Pasted ~41 lines]"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := pasteSummary(c.text)
			if ok != c.collapsed {
				t.Fatalf("pasteSummary collapsed=%v, want %v", ok, c.collapsed)
			}
			if ok && got != c.want {
				t.Errorf("placeholder = %q, want %q", got, c.want)
			}
		})
	}
}

// TestLargePasteShowsPlaceholder: a big paste must not bury the prompt, and
// the placeholder is what the user sees.
func TestLargePasteShowsPlaceholder(t *testing.T) {
	app := typingApp(t, 100, 40)
	var lines []string
	for i := 0; i < 40; i++ {
		lines = append(lines, fmt.Sprintf("line %d of the pasted block", i))
	}
	content := strings.Join(lines, "\n")
	app.Update(tea.PasteMsg{Content: content})

	value := app.input.Value()
	if !strings.Contains(value, "[Pasted ~40 lines]") {
		t.Errorf("prompt is %q, want the placeholder", value)
	}
	if strings.Contains(value, "line 7 of the pasted block") {
		t.Errorf("the prompt should hold the placeholder, not the content: %q", value)
	}
	// The prompt must stay one line tall rather than growing to 40.
	if got := app.input.Height(); got != 1 {
		t.Errorf("editor grew to %d rows for a collapsed paste, want 1", got)
	}
}

// TestLargePasteExpandsOnSubmit: the placeholder is a display convenience, so
// the real content has to be what is sent.
func TestLargePasteExpandsOnSubmit(t *testing.T) {
	content := strings.Repeat("payload line\n", 10)
	pastes := []pastedText{{placeholder: "[Pasted ~11 lines]", content: content}}

	got := expandPastes("look at this [Pasted ~11 lines] please", pastes)
	want := "look at this " + content + " please"
	if got != want {
		t.Errorf("expandPastes = %q, want %q", got, want)
	}
}

// TestExpandPastesHandlesSeveral: two pastes in one prompt each restore their
// own content, including when the same content was pasted twice.
func TestExpandPastesHandlesSeveral(t *testing.T) {
	pastes := []pastedText{
		{placeholder: "[Pasted ~3 lines]", content: "AAA"},
		{placeholder: "[Pasted ~5 lines]", content: "BBB"},
	}
	got := expandPastes("x [Pasted ~3 lines] y [Pasted ~5 lines] z", pastes)
	if want := "x AAA y BBB z"; got != want {
		t.Errorf("expandPastes = %q, want %q", got, want)
	}

	same := []pastedText{
		{placeholder: "[Pasted ~3 lines]", content: "AAA"},
		{placeholder: "[Pasted ~3 lines]", content: "AAA"},
	}
	got = expandPastes("[Pasted ~3 lines] and [Pasted ~3 lines]", same)
	if want := "AAA and AAA"; got != want {
		t.Errorf("two identical pastes: got %q, want %q", got, want)
	}
}

// TestExpandPastesDropsDeletedPlaceholder: deleting the placeholder must drop
// its content, matching the original removing the extmark with it.
func TestExpandPastesDropsDeletedPlaceholder(t *testing.T) {
	pastes := []pastedText{{placeholder: "[Pasted ~3 lines]", content: "AAA"}}
	if got := expandPastes("I changed my mind", pastes); got != "I changed my mind" {
		t.Errorf("expandPastes = %q, want the text unchanged", got)
	}
}

func TestExpandPastesWithNone(t *testing.T) {
	if got := expandPastes("plain text", nil); got != "plain text" {
		t.Errorf("expandPastes = %q, want it unchanged", got)
	}
}

// TestPasteRecordsNothingForSmallPaste: a short paste goes in literally, so
// there is nothing to expand later.
func TestPasteRecordsNothingForSmallPaste(t *testing.T) {
	app := typingApp(t, 100, 40)
	app.Update(tea.PasteMsg{Content: "short"})
	if len(app.pastes) != 0 {
		t.Errorf("recorded %d pastes for a short one, want none", len(app.pastes))
	}
}

// TestTakePastesClears: the record must not leak into the next message.
func TestTakePastesClears(t *testing.T) {
	app := typingApp(t, 100, 40)
	app.Update(tea.PasteMsg{Content: strings.Repeat("line\n", 10)})
	if len(app.pastes) != 1 {
		t.Fatalf("recorded %d pastes, want 1", len(app.pastes))
	}
	if got := app.takePastes(); len(got) != 1 {
		t.Fatalf("takePastes returned %d, want 1", len(got))
	}
	if len(app.pastes) != 0 {
		t.Errorf("takePastes left %d behind", len(app.pastes))
	}
}

// TestPasteThenTypeThenSubmit is the whole flow: paste a block, type around
// it, and check what would be sent.
func TestPasteThenTypeThenSubmit(t *testing.T) {
	app := typingApp(t, 100, 40)
	content := strings.Repeat("code line\n", 20)

	typeText(app, "review ")
	app.Update(tea.PasteMsg{Content: content})
	typeText(app, "thanks")

	value := app.input.Value()
	if !strings.HasPrefix(value, "review [Pasted ~") {
		t.Fatalf("prompt is %q", value)
	}
	expanded := expandPastes(value, app.takePastes())
	if !strings.Contains(expanded, "code line") {
		t.Errorf("submitted text lost the pasted content: %q", expanded)
	}
	if !strings.HasPrefix(expanded, "review ") || !strings.HasSuffix(expanded, "thanks") {
		t.Errorf("submitted text lost the typed parts: %q", expanded)
	}
	if strings.Contains(expanded, "[Pasted ~") {
		t.Errorf("the placeholder survived into the submitted text: %q", expanded)
	}
}

// TestMultilinePasteBelowThresholdGrowsTheBox: a two-line paste goes in
// literally, so the prompt must grow to show it.
func TestMultilinePasteBelowThresholdGrowsTheBox(t *testing.T) {
	app := typingApp(t, 100, 40)
	app.Update(tea.PasteMsg{Content: "first\nsecond"})

	if got := app.input.Value(); got != "first\nsecond" {
		t.Fatalf("prompt is %q", got)
	}
	if got := app.input.Height(); got != 2 {
		t.Errorf("editor is %d rows for a two-line paste, want 2", got)
	}
	box := app.promptBox(app.sessionPromptBoxWidth())
	for _, want := range []string{"first", "second"} {
		if !strings.Contains(box, want) {
			t.Errorf("%q is not visible in the prompt box:\n%s", want, box)
		}
	}
}

func TestEmptyPasteIsIgnored(t *testing.T) {
	app := typingApp(t, 100, 40)
	typeText(app, "keep")
	app.Update(tea.PasteMsg{Content: ""})
	if got := app.input.Value(); got != "keep" {
		t.Errorf("prompt is %q, want it unchanged", got)
	}
}
