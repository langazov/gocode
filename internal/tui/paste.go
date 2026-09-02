package tui

import (
	"fmt"
	"sort"
	"strings"

	tea "charm.land/bubbletea/v2"
)

// This file ports the paste handling in packages/tui/src/component/prompt/
// index.tsx: pasteInputText, pasteText and expandTrackedPastedText.
//
// Bracketed paste arrives as tea.PasteMsg, which App.update did not handle at
// all — the message fell through the switch and the pasted text was dropped.

// pasteSummaryLines and pasteSummaryChars are the thresholds from
// pasteInputText: a paste of at least three lines, or longer than 150
// characters, is collapsed to a placeholder rather than dumped into the
// editor.
const (
	pasteSummaryLines = 3
	pasteSummaryChars = 150
)

// pastedText is one collapsed paste: the placeholder standing in for it in the
// editor, and the real content to restore on submit.
type pastedText struct {
	// placeholder is the exact text inserted into the editor, e.g.
	// "[Pasted ~12 lines]".
	placeholder string
	// content is what the placeholder stands for.
	content string
}

// handlePaste inserts pasted text, porting pasteInputText.
func (a *App) handlePaste(msg tea.PasteMsg) tea.Cmd {
	text := normalizePastedText(msg.Content)
	if text == "" {
		return nil
	}

	// A pasted path to an image or PDF attaches the file rather than inserting
	// the path, which is what a file manager or screenshot tool puts on the
	// clipboard. Checked before the size rules, matching pasteInputText.
	if file, ok := readLocalAttachment(pastedFilepath(text)); ok {
		if file.Text != "" {
			// SVG is markup: the original inlines it as text so the model can
			// read it without vision.
			a.pastes = append(a.pastes, pastedText{
				placeholder: "[SVG: " + file.Name + "]",
				content:     file.Text,
			})
			a.input.InsertString("[SVG: " + file.Name + "] ")
			return nil
		}
		a.attachPaste(file)
		return nil
	}

	// A large paste is collapsed to a one-line placeholder so it does not bury
	// the prompt. The real content is restored when the message is sent.
	if summary, ok := pasteSummary(text); ok {
		a.pastes = append(a.pastes, pastedText{placeholder: summary, content: text})
		a.input.InsertString(summary + " ")
		return nil
	}

	a.input.InsertString(text)
	return nil
}

// normalizePastedText ports the CRLF handling in pasteInputText. Windows
// ConPTY and some terminals send CR-only line endings inside a bracketed
// paste, which would otherwise land as a single line with stray carriage
// returns in it.
func normalizePastedText(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	return strings.ReplaceAll(text, "\r", "\n")
}

// pasteSummary returns the placeholder for a paste large enough to collapse,
// porting the `lineCount >= 3 || length > 150` test. The line count is of the
// trimmed content, matching pastedContent in the original.
func pasteSummary(text string) (string, bool) {
	trimmed := strings.TrimSpace(text)
	lines := strings.Count(trimmed, "\n") + 1
	if lines < pasteSummaryLines && len(trimmed) <= pasteSummaryChars {
		return "", false
	}
	return fmt.Sprintf("[Pasted ~%d lines]", lines), true
}

// expandPastes restores collapsed pastes in the submitted text, porting
// expandTrackedPastedText.
//
// Placeholders are matched by their text rather than by tracked offsets: this
// port has no extmarks, so an edit elsewhere in the prompt would invalidate a
// stored range. Matching on the text survives editing, at the cost of expanding
// a placeholder the user typed by hand — which reads the same way, and is what
// they would have got by pasting it.
//
// A placeholder the user deleted simply does not match, so its content is
// dropped, which is the behavior the original's extmark removal produces.
func expandPastes(text string, pastes []pastedText) string {
	if len(pastes) == 0 {
		return text
	}
	// Longest placeholder first: "[Pasted ~1 lines]" is a prefix of nothing
	// here, but ordering by length keeps a shorter placeholder from matching
	// inside a longer one if the format ever changes.
	ordered := append([]pastedText(nil), pastes...)
	sort.SliceStable(ordered, func(i, j int) bool {
		return len(ordered[i].placeholder) > len(ordered[j].placeholder)
	})
	for _, paste := range ordered {
		// Replace one occurrence per recorded paste, so pasting the same
		// content twice expands both.
		text = strings.Replace(text, paste.placeholder, paste.content, 1)
	}
	return text
}

// takePastes returns the recorded pastes and clears them, for a submit.
func (a *App) takePastes() []pastedText {
	pastes := a.pastes
	a.pastes = nil
	return pastes
}
