package tui

import (
	"strings"

	"charm.land/lipgloss/v2"
)

// buttonPad is the horizontal padding inside a dialog button. DialogAlert and
// DialogHelp pad their single ok button by 3; DialogConfirm pads its pair by 1.
const (
	alertButtonPad   = 3
	confirmButtonPad = 1
)

// button renders one dialog button: padded label text on the primary fill when
// active, or on the panel in muted text when not (dialog-confirm.tsx).
func (a *App) button(label string, active bool) string {
	padded := strings.Repeat(" ", confirmButtonPad) + label + strings.Repeat(" ", confirmButtonPad)
	if active {
		return lipgloss.NewStyle().
			Foreground(a.theme.SelectedListItemText).
			Background(a.theme.Primary).
			Render(padded)
	}
	return a.onPanel(a.theme.TextMuted, false).Render(padded)
}

// buttonRow right-aligns rendered buttons inside a panel padded by pad on both
// sides (justifyContent="flex-end"), and reports the column span each one
// occupies so a click can be routed back to it.
func (a *App) buttonRow(pad, w int, buttons []string) (string, []actionHit) {
	total := 0
	for _, b := range buttons {
		total += lipgloss.Width(b)
	}
	align := w - 2*pad - total
	if align < 0 {
		align = 0
	}
	col := pad + align
	spans := make([]actionHit, 0, len(buttons))
	var row strings.Builder
	row.WriteString(strings.Repeat(" ", col))
	for i, b := range buttons {
		width := lipgloss.Width(b)
		spans = append(spans, actionHit{start: col, end: col + width, index: i})
		col += width
		row.WriteString(b)
	}
	return row.String(), spans
}

// messageBlock renders a dialog's body paragraph: muted, wrapped to the panel
// width, followed by the box's own paddingBottom row.
func (a *App) messageBlock(pad, w int, message string) []string {
	indent := strings.Repeat(" ", pad)
	var lines []string
	for _, line := range wrapWords(message, w-2*pad) {
		lines = append(lines, indent+a.onPanel(a.theme.TextMuted, false).Render(line))
	}
	if len(lines) == 0 {
		lines = append(lines, "")
	}
	return append(lines, "")
}

// alertOverlay mirrors ui/dialog-alert.tsx: a bold title with an esc hint, a
// muted message, and a single right-aligned ok button on the primary fill.
func (a *App) alertOverlay(w int) (content string, buttonRow int, spans []actionHit) {
	o := a.overlay
	lines := []string{a.dialogHeader(2, o.title, "esc", w), ""}
	lines = append(lines, a.messageBlock(2, w, o.message)...)
	lines = append(lines, "")
	ok := lipgloss.NewStyle().
		Foreground(a.theme.SelectedListItemText).
		Background(a.theme.Primary).
		Render(strings.Repeat(" ", alertButtonPad) + "ok" + strings.Repeat(" ", alertButtonPad))
	row, spans := a.buttonRow(2, w, []string{ok})
	buttonRow = len(lines)
	lines = append(lines, row, "")
	return strings.Join(lines, "\n"), buttonRow, spans
}

// confirmOverlay mirrors ui/dialog-confirm.tsx: the alert layout with a Cancel
// and a Confirm button, the active one filled with the primary color. Buttons
// render in cancel-then-confirm order, and left/right move between them.
func (a *App) confirmOverlay(w int) (content string, buttonRow int, spans []actionHit) {
	o := a.overlay
	lines := []string{a.dialogHeader(2, o.title, "esc", w), ""}
	lines = append(lines, a.messageBlock(2, w, o.message)...)
	lines = append(lines, "")
	cancel := o.cancelLabel
	if cancel == "" {
		cancel = "cancel"
	}
	row, spans := a.buttonRow(2, w, []string{
		a.button(titlecase(cancel), !o.confirmActive),
		a.button(titlecase("confirm"), o.confirmActive),
	})
	buttonRow = len(lines)
	lines = append(lines, row, "")
	return strings.Join(lines, "\n"), buttonRow, spans
}

// filterRow renders DialogSelect's filter input: the typed text in muted
// text with a primary-colored block cursor after it, or the placeholder with
// the cursor resting on its first cell while the field is empty.
func (a *App) filterRow(o *overlay, w int) string {
	indent := strings.Repeat(" ", 4)
	cursor := lipgloss.NewStyle().Foreground(a.theme.BackgroundPanel).Background(a.theme.Primary)
	if o.filter != "" {
		return indent + a.onPanel(a.theme.TextMuted, false).Render(o.filter) + cursor.Render(" ")
	}
	placeholder := o.placeholder
	if placeholder == "" {
		placeholder = "Search"
	}
	runes := []rune(placeholder)
	return indent + cursor.Render(string(runes[0])) +
		a.onPanel(a.theme.TextMuted, false).Render(string(runes[1:]))
}

// emptyView renders the list's empty state: DialogSelect's "No results found"
// fallback, or the emptyView a caller supplied instead (dialog-skill.tsx uses
// one to report a failed load in place of the list).
func (a *App) emptyView(o *overlay, w int) []string {
	indent := strings.Repeat(" ", 4)
	if o.emptyTitle == "" && o.emptyBody == "" {
		return []string{indent + a.onPanel(a.theme.TextMuted, false).Render("No results found")}
	}
	var lines []string
	if o.emptyTitle != "" {
		lines = append(lines, indent+a.onPanel(a.theme.Error, true).Render(o.emptyTitle))
	}
	for _, line := range wrapWords(o.emptyBody, w-8) {
		lines = append(lines, indent+a.onPanel(a.theme.TextMuted, false).Render(line))
	}
	return lines
}
