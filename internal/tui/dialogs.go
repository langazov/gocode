package tui

import (
	"context"
	"fmt"
	"image/color"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/anomalyco/opencode-go/internal/tui/client"
)

// Dialog panel widths, from the size prop in ui/dialog.tsx.
const (
	dialogMedium = 60
	dialogLarge  = 88
	dialogXLarge = 116
)

// overlayKind discriminates the active dialog.
type overlayKind int

const (
	overlayNone overlayKind = iota
	overlayList
	overlayInput
	overlayHelp
	overlayStatus
	overlayAlert
	overlayConfirm
)

// overlayItem is one row of a list dialog, mirroring DialogSelectOption:
// label is the title, hint the muted description, footer the right-aligned
// annotation, category the group header, and value the stable id matched
// against overlay.current for the ● current-item marker.
type overlayItem struct {
	label    string
	hint     string
	value    string
	category string
	footer   string
	action   func() tea.Msg
}

// dialogAction is a footer action (DialogSelect actions): a title plus the
// keybind that triggers it on the selected item.
type dialogAction struct {
	title     string
	keys      string
	onTrigger func(item overlayItem) tea.Cmd
}

// overlay is the shared dialog surface. List dialogs mirror DialogSelect: a
// bold title with an esc hint, a filter row, grouped selectable rows with a
// primary-highlighted selection, and footer actions. Input dialogs mirror
// DialogPrompt.
type overlay struct {
	kind     overlayKind
	title    string
	size     int // panel width; 0 = medium
	items    []overlayItem
	all      []overlayItem // unfiltered, for filter restore
	filter   string
	selected int
	current  string // value of the current item, marked with ●
	actions  []dialogAction
	armValue string // armed two-press confirmation (session delete)
	armKeys  string // keybind shown in the armed confirmation label
	onMove   func(item overlayItem)
	input    string // for overlayInput
	onSubmit func(string) tea.Msg

	// placeholder is the filter input's placeholder (DialogSelect's
	// placeholder prop, "Search" when unset).
	placeholder string
	// hideFilter suppresses the filter row, mirroring renderFilter={false}.
	hideFilter bool
	// locked disables selection, filtering and activation while leaving the
	// panel on screen — DialogSelect's locked prop, used with emptyBody to
	// show a load failure in place of the list.
	locked bool
	// emptyTitle/emptyBody replace the "No results found" fallback, the
	// port of DialogSelect's emptyView.
	emptyTitle string
	emptyBody  string

	// message is the body paragraph of an alert or confirm dialog.
	message string
	// cancelLabel overrides the left button's text on a confirm dialog
	// (DialogConfirm's label prop); empty renders "Cancel".
	cancelLabel string
	// confirmActive tracks which confirm button is highlighted; it starts on
	// confirm, matching DialogConfirm's initial active state.
	confirmActive bool
	onConfirm     func() tea.Msg
	onCancel      func() tea.Msg
}

// openAlert mirrors DialogAlert.show: a titled message with a single ok
// button, dismissed by enter or escape.
func (a *App) openAlert(title, message string, onConfirm func() tea.Msg) {
	a.overlay = &overlay{kind: overlayAlert, title: title, message: message, onConfirm: onConfirm}
}

// openConfirm mirrors DialogConfirm.show. cancelLabel overrides the left
// button's text; onCancel also runs when the dialog is dismissed with escape,
// matching the TS promise resolving via the dialog's onClose.
func (a *App) openConfirm(title, message, cancelLabel string, onConfirm, onCancel func() tea.Msg) {
	a.overlay = &overlay{
		kind:          overlayConfirm,
		title:         title,
		message:       message,
		cancelLabel:   cancelLabel,
		confirmActive: true,
		onConfirm:     onConfirm,
		onCancel:      onCancel,
	}
}

func (a *App) openList(title string, items []overlayItem) {
	a.overlay = &overlay{kind: overlayList, title: title, items: items, all: items}
}

func (a *App) openInput(title, placeholder string, onSubmit func(string) tea.Msg) {
	a.overlay = &overlay{kind: overlayInput, title: title, input: placeholder, onSubmit: onSubmit}
}

func (a *App) closeOverlay() {
	a.overlay = nil
}

func (o *overlay) applyFilter() {
	if o.filter == "" {
		o.items = o.all
		return
	}
	needle := strings.ToLower(o.filter)
	out := make([]overlayItem, 0, len(o.all))
	for _, item := range o.all {
		if strings.Contains(strings.ToLower(item.label), needle) ||
			strings.Contains(strings.ToLower(item.hint), needle) ||
			strings.Contains(strings.ToLower(item.category), needle) ||
			strings.Contains(strings.ToLower(item.value), needle) {
			out = append(out, item)
		}
	}
	o.items = out
	if o.selected >= len(o.items) {
		o.selected = len(o.items) - 1
	}
	if o.selected < 0 {
		o.selected = 0
	}
}

func (o *overlay) selectedItem() (overlayItem, bool) {
	if o.selected < 0 || o.selected >= len(o.items) {
		return overlayItem{}, false
	}
	return o.items[o.selected], true
}

// moveSelection moves the list selection with wraparound, disarming any
// pending confirmation and notifying onMove (live theme preview).
func (a *App) moveSelection(o *overlay, delta int) {
	if len(o.items) == 0 {
		return
	}
	o.selected += delta
	if o.selected < 0 {
		o.selected = len(o.items) - 1
	}
	if o.selected >= len(o.items) {
		o.selected = 0
	}
	o.armValue = ""
	if o.onMove != nil {
		o.onMove(o.items[o.selected])
	}
}

func (a *App) handleOverlayKey(key string) tea.Cmd {
	o := a.overlay
	if o == nil {
		return nil
	}
	// Inside a dialog the dialog owns the keyboard: ctrl+c closes it like
	// escape instead of quitting the app (Dialog keybinds in the original).
	if key == "ctrl+c" {
		a.closeOverlay()
		return nil
	}
	switch o.kind {
	case overlayHelp, overlayStatus:
		if key == "esc" || key == "enter" || key == "q" {
			a.closeOverlay()
		}
		return nil
	case overlayAlert:
		// Both keys dismiss: DialogAlert.show settles its promise from the
		// ok binding and from the dialog's onClose alike, so escape runs the
		// same continuation enter does.
		if key == "esc" || key == "enter" {
			return a.resolveOverlay(o.onConfirm)
		}
		return nil
	case overlayConfirm:
		switch key {
		case "esc":
			// DialogConfirm.show resolves undefined on close, running
			// neither branch.
			a.closeOverlay()
			return nil
		case "left", "right":
			o.confirmActive = !o.confirmActive
			return nil
		case "enter":
			if o.confirmActive {
				return a.resolveOverlay(o.onConfirm)
			}
			return a.resolveOverlay(o.onCancel)
		}
		return nil
	case overlayInput:
		switch key {
		case "esc":
			a.closeOverlay()
			return nil
		case "enter":
			overlay := *o
			a.closeOverlay()
			value := strings.TrimSpace(overlay.input)
			if value == "" || overlay.onSubmit == nil {
				return nil
			}
			return staticMsg(overlay.onSubmit(value))
		case "backspace":
			if run := []rune(o.input); len(run) > 0 {
				o.input = string(run[:len(run)-1])
			}
			return nil
		}
		if len(key) == 1 {
			o.input += key
		}
		return nil
	}

	// overlayList
	if o.locked {
		// DialogSelect's locked prop guards filtering, movement and
		// selection alike; only the dialog's own escape still applies.
		if key == "esc" {
			a.closeOverlay()
		}
		return nil
	}
	for _, action := range o.actions {
		if action.keys != "" && key == action.keys {
			if item, ok := o.selectedItem(); ok {
				return action.onTrigger(item)
			}
			return nil
		}
	}
	switch key {
	case "esc":
		a.closeOverlay()
		return nil
	case "up", "k":
		a.moveSelection(o, -1)
		return nil
	case "down", "j":
		a.moveSelection(o, 1)
		return nil
	case "backspace":
		if run := []rune(o.filter); len(run) > 0 {
			o.filter = string(run[:len(run)-1])
			o.applyFilter()
		}
		return nil
	case "enter":
		item, ok := o.selectedItem()
		if !ok {
			return nil
		}
		return a.activateItem(item)
	}
	if len(key) == 1 {
		o.filter += key
		o.applyFilter()
	}
	return nil
}

// resolveOverlay closes the dialog and dispatches the chosen branch, the
// shared tail of the alert and confirm button handlers.
func (a *App) resolveOverlay(branch func() tea.Msg) tea.Cmd {
	a.closeOverlay()
	if branch == nil {
		return nil
	}
	if result := branch(); result != nil {
		if cmd, ok := result.(tea.Cmd); ok {
			return cmd
		}
		return staticMsg(result)
	}
	return nil
}

// activateItem runs the selected item's action, mirroring DialogSelect's
// enter/onSelect: closes the dialog first, then dispatches whatever the
// action returns. Shared by the enter key and a mouse click/release on the
// row (see mouse.go's overlayMouseTarget/handleClick).
func (a *App) activateItem(item overlayItem) tea.Cmd {
	action := item.action
	a.closeOverlay()
	if action == nil {
		return nil
	}
	if result := action(); result != nil {
		if cmd, ok := result.(tea.Cmd); ok {
			return cmd
		}
		return staticMsg(result)
	}
	return nil
}

// moveSelectionTo jumps the list selection to an absolute index (mouse hover
// preselect / press), sharing moveSelection's disarm+onMove notification.
func (a *App) moveSelectionTo(o *overlay, index int) {
	if index < 0 || index >= len(o.items) {
		return
	}
	o.selected = index
	o.armValue = ""
	if o.onMove != nil {
		o.onMove(o.items[o.selected])
	}
}

// onPanel styles dialog text: explicit panel background so composed segments
// keep the tint after each segment's reset sequence.
func (a *App) onPanel(fg color.Color, bold bool) lipgloss.Style {
	s := lipgloss.NewStyle().Foreground(fg).Background(a.theme.BackgroundPanel)
	if bold {
		s = s.Bold(true)
	}
	return s
}

// dialogHeader is the shared title row: bold title left, muted keybind hint
// right. Select dialogs pad 4; prompt/help/status dialogs pad 2.
func (a *App) dialogHeader(pad int, title, hint string, w int) string {
	styled := a.onPanel(a.theme.Text, true).Render(title)
	esc := a.onPanel(a.theme.TextMuted, false).Render(hint)
	gap := w - 2*pad - lipgloss.Width(styled) - lipgloss.Width(esc)
	if gap < 1 {
		gap = 1
	}
	return strings.Repeat(" ", pad) + styled + strings.Repeat(" ", gap) + esc
}

// escHintRange mirrors dialogHeader's own layout math to report the column
// span its right-aligned hint occupies, so a mouse click there can be
// recognized as the TS "esc" label's onMouseUp (dialog-select.tsx / dialog.tsx).
func (a *App) escHintRange(pad int, title, hint string, w int) (start, end int) {
	styled := a.onPanel(a.theme.Text, true).Render(title)
	esc := a.onPanel(a.theme.TextMuted, false).Render(hint)
	gap := w - 2*pad - lipgloss.Width(styled) - lipgloss.Width(esc)
	if gap < 1 {
		gap = 1
	}
	start = pad + lipgloss.Width(styled) + gap
	return start, start + lipgloss.Width(esc)
}

// wrapWords wraps text to width on spaces so continuation lines keep their
// indent inside the panel.
func wrapWords(text string, width int) []string {
	var lines []string
	line := ""
	for _, word := range strings.Fields(text) {
		switch {
		case line == "":
			line = word
		case lipgloss.Width(line)+1+lipgloss.Width(word) <= width:
			line += " " + word
		default:
			lines = append(lines, line)
			line = word
		}
	}
	if line != "" {
		lines = append(lines, line)
	}
	return lines
}

// viewOverlay composites the dialog panel over the underlying route at
// height/4, centered — the Dialog backdrop in ui/dialog.tsx.
func (a *App) viewOverlay() string {
	panel, _ := a.overlayPanel()
	return a.frame(a.compositeOverlay(a.underlay(), panel))
}

func (a *App) underlay() string {
	if a.view == viewHome {
		return a.viewHome()
	}
	return a.viewChat()
}

// overlayHits maps the panel's rendered lines/columns back to what's
// interactive there, built by the same calls that produce the panel content
// so a mouse hit test always matches what's actually on screen. rowItem[i]
// is the item index selectable by panel line i, or -1.
type overlayHits struct {
	rowItem          []int
	escRow           int
	escStart, escEnd int
	actionRow        int
	actions          []actionHit
	// buttonRow/buttons locate the ok / cancel+confirm buttons of an alert
	// or confirm dialog, whose onMouseUp handlers they reproduce.
	buttonRow int
	buttons   []actionHit
}

type actionHit struct {
	start, end, index int
}

func newOverlayHits() *overlayHits {
	return &overlayHits{escRow: -1, actionRow: -1, buttonRow: -1}
}

// shiftRows accounts for n lines prepended ahead of everything already
// recorded (the panel's own PaddingTop).
func (h *overlayHits) shiftRows(n int) {
	prefix := make([]int, n)
	for i := range prefix {
		prefix[i] = -1
	}
	h.rowItem = append(prefix, h.rowItem...)
	if h.escRow >= 0 {
		h.escRow += n
	}
	if h.actionRow >= 0 {
		h.actionRow += n
	}
	if h.buttonRow >= 0 {
		h.buttonRow += n
	}
}

// overlayPanel renders the active dialog panel, alongside the hit map mouse
// handling needs (see mouse.go's overlayMouseTarget).
func (a *App) overlayPanel() (string, *overlayHits) {
	o := a.overlay
	size := o.size
	if size == 0 {
		size = dialogMedium
	}
	w := size
	if w > a.width-2 {
		w = a.width - 2
	}
	var content string
	hits := newOverlayHits()
	switch o.kind {
	case overlayHelp:
		content = a.helpOverlay(w)
		hits.escRow = 0
		hits.escStart, hits.escEnd = a.escHintRange(2, "Help", "esc/enter", w)
	case overlayStatus:
		content = a.statusOverlay(w)
		hits.escRow = 0
		hits.escStart, hits.escEnd = a.escHintRange(2, "Status", "esc", w)
	case overlayInput:
		content = a.inputOverlay(w)
		hits.escRow = 0
		hits.escStart, hits.escEnd = a.escHintRange(2, o.title, "esc", w)
	case overlayAlert:
		content, hits.buttonRow, hits.buttons = a.alertOverlay(w)
		hits.escRow = 0
		hits.escStart, hits.escEnd = a.escHintRange(2, o.title, "esc", w)
	case overlayConfirm:
		content, hits.buttonRow, hits.buttons = a.confirmOverlay(w)
		hits.escRow = 0
		hits.escStart, hits.escEnd = a.escHintRange(2, o.title, "esc", w)
	default:
		lines := a.listOverlay(o, w, hits)
		content = strings.Join(lines, "\n")
	}
	// The panel is a borderless backgroundPanel block with paddingTop 1,
	// exactly like the Dialog container in the original.
	style := lipgloss.NewStyle().
		Width(w).
		Background(a.theme.BackgroundPanel).
		PaddingTop(1)
	hits.shiftRows(1)
	return style.Render(content), hits
}

// overlayOrigin is the panel's top-left screen cell within compositeOverlay's
// layout, shared with mouse hit-testing so both agree on where the panel is.
func (a *App) overlayOrigin(panelW int) (top, left int) {
	return a.height / 4, (a.width - panelW) / 2
}

// compositeOverlay splices the panel into the base render. The backdrop dim
// of the original (black at ~59% alpha) has no lipgloss equivalent, so the
// base stays undimmed around the panel.
func (a *App) compositeOverlay(base, panel string) string {
	top, left := a.overlayOrigin(lipgloss.Width(panel))
	return a.spliceAt(base, panel, top, left)
}

// spliceAt splices panel into base at the given absolute screen row/col,
// the shared ANSI-safe technique behind compositeOverlay and the toast
// panel's top-right placement (feature.go's compositeToast).
func (a *App) spliceAt(base, panel string, top, left int) string {
	baseLines := strings.Split(base, "\n")
	for i, line := range baseLines {
		baseLines[i] = padPlain(line, a.width)
	}
	for len(baseLines) < a.height {
		baseLines = append(baseLines, strings.Repeat(" ", a.width))
	}
	panelLines := strings.Split(panel, "\n")
	panelW := lipgloss.Width(panel)
	for i, pline := range panelLines {
		row := top + i
		if row < 0 || row >= len(baseLines) {
			continue
		}
		prefix := sliceCells(baseLines[row], 0, left)
		suffix := sliceCells(baseLines[row], left+panelW, a.width)
		baseLines[row] = prefix + pline + suffix
	}
	return strings.Join(baseLines, "\n")
}

func padPlain(line string, width int) string {
	gap := width - lipgloss.Width(line)
	if gap <= 0 {
		return line
	}
	return line + strings.Repeat(" ", gap)
}

// sliceCells returns the ANSI-safe substring of line between cell columns
// start (inclusive) and end (exclusive), keeping escape sequences that apply
// to cells inside the window.
func sliceCells(line string, start, end int) string {
	if start == 0 && lipgloss.Width(line) <= end {
		return line
	}
	var out, pending strings.Builder
	cells := 0
	inEscape := false
	for _, r := range line {
		if inEscape {
			pending.WriteRune(r)
			if r == 'm' {
				inEscape = false
			}
			continue
		}
		if r == 0x1b {
			inEscape = true
			pending.WriteRune(r)
			continue
		}
		if cells >= start && cells < end {
			out.WriteString(pending.String())
			out.WriteRune(r)
		}
		pending.Reset()
		cells++
		if cells >= end {
			break
		}
	}
	return out.String()
}

// listOverlay renders a DialogSelect: header, filter, grouped rows, and the
// footer actions, separated by blank lines (the gap=1/paddingBottom=1 box).
func (a *App) listOverlay(o *overlay, w int, hits *overlayHits) []string {
	lines := []string{a.dialogHeader(4, o.title, "esc", w)}
	hits.rowItem = append(hits.rowItem, -1)
	hits.escRow = 0
	hits.escStart, hits.escEnd = a.escHintRange(4, o.title, "esc", w)
	// The filter input sits under the title inside the same padded header
	// box (paddingTop 1), then the parent's gap separates it from the list.
	if !o.hideFilter {
		lines = append(lines, "", a.filterRow(o, w))
		hits.rowItem = append(hits.rowItem, -1, -1)
	}
	lines = append(lines, "")
	hits.rowItem = append(hits.rowItem, -1)
	if len(o.items) == 0 {
		lines = append(lines, "")
		hits.rowItem = append(hits.rowItem, -1)
		for _, line := range a.emptyView(o, w) {
			lines = append(lines, line)
			hits.rowItem = append(hits.rowItem, -1)
		}
	} else {
		bodyLines, bodyHits := a.listBody(o, w-2)
		lines = append(lines, bodyLines...)
		hits.rowItem = append(hits.rowItem, bodyHits...)
	}
	if len(o.actions) > 0 {
		lines = append(lines, "")
		hits.rowItem = append(hits.rowItem, -1)
		actionLine, spans := a.actionRow(o, w)
		hits.actionRow = len(lines)
		hits.actions = spans
		lines = append(lines, actionLine)
		hits.rowItem = append(hits.rowItem, -1)
	}
	lines = append(lines, "")
	hits.rowItem = append(hits.rowItem, -1)
	return lines
}

// listBody renders the grouped option rows, windowed like the scrollbox
// capped at terminal height/2 - 6, alongside a parallel itemIndex-per-line
// slice (-1 for separators/category headers) for mouse hit-testing.
func (a *App) listBody(o *overlay, width int) ([]string, []int) {
	type row struct {
		text      string
		selected  bool
		itemIndex int
	}
	var rows []row
	category := ""
	for i, item := range o.items {
		if item.category != "" && item.category != category {
			if category != "" {
				rows = append(rows, row{itemIndex: -1})
			}
			rows = append(rows, row{
				text: strings.Repeat(" ", 3) +
					a.onPanel(a.theme.Accent, true).Render(item.category),
				itemIndex: -1,
			})
		}
		category = item.category
		rows = append(rows, row{text: a.listRow(o, item, i, width), selected: i == o.selected, itemIndex: i})
	}

	maxRows := a.height/2 - 6
	if maxRows < 3 {
		maxRows = 3
	}
	window := rows
	if len(rows) > maxRows {
		selected := 0
		for i, r := range rows {
			if r.selected {
				selected = i
			}
		}
		start := selected - maxRows/2
		if start < 0 {
			start = 0
		}
		if start > len(rows)-maxRows {
			start = len(rows) - maxRows
		}
		window = rows[start : start+maxRows]
	}
	texts := make([]string, len(window))
	indexes := make([]int, len(window))
	for i, r := range window {
		texts[i] = r.text
		indexes[i] = r.itemIndex
	}
	return texts, indexes
}

// listRow renders one DialogSelect option: left padding 1 for the current
// item (with a ● gutter) or 3 otherwise, title in text color (primary for
// the current item), muted hint, right-aligned footer, and the selected row
// highlighted with a primary background and bold background-colored text.
func (a *App) listRow(o *overlay, item overlayItem, index, width int) string {
	active := index == o.selected
	armed := o.armValue != "" && o.armValue == item.value
	current := o.current != "" && item.value == o.current

	bg := a.theme.BackgroundPanel
	if active {
		bg = a.theme.Primary
		if armed {
			bg = a.theme.Error
		}
	}
	segment := func(fg color.Color, bold bool, text string) string {
		s := lipgloss.NewStyle().Foreground(fg).Background(bg)
		if bold {
			s = s.Bold(true)
		}
		return s.Render(text)
	}
	titleFg := a.theme.Text
	if active {
		titleFg = a.theme.Background
	} else if current {
		titleFg = a.theme.Primary
	}
	hintFg := a.theme.TextMuted
	if active {
		hintFg = a.theme.Background
	}

	label := item.label
	if armed {
		label = "Press " + o.armKeys + " again to confirm"
	}
	const padLeftDefault, padRight = 3, 3
	padLeft := padLeftDefault
	if current {
		padLeft = 1
	}
	bullet := 0
	if current {
		bullet = 2 // "● "
	}

	hint := item.hint
	footerW := lipgloss.Width(item.footer)
	if hint != "" && lipgloss.Width(label)+1+lipgloss.Width(hint) > width-padLeft-padRight-bullet-footerW-1 {
		hint = ""
	}
	if lipgloss.Width(label) > width-padLeft-padRight-bullet-footerW-hintWidth(hint)-1 {
		label = truncateRunes(label, width-padLeft-padRight-bullet-footerW-hintWidth(hint)-1)
	}
	fillWidth := width - padLeft - bullet - lipgloss.Width(label) - hintWidth(hint) - footerW - padRight
	if fillWidth < 0 {
		fillWidth = 0
	}

	var b strings.Builder
	b.WriteString(strings.Repeat(" ", padLeft))
	if current {
		b.WriteString(segment(titleFg, false, "● "))
	}
	b.WriteString(segment(titleFg, active, label))
	if hint != "" {
		b.WriteString(segment(hintFg, false, " "+hint))
	}
	b.WriteString(lipgloss.NewStyle().Background(bg).Render(strings.Repeat(" ", fillWidth)))
	if item.footer != "" {
		b.WriteString(segment(hintFg, false, item.footer))
	}
	b.WriteString(strings.Repeat(" ", padRight))
	return b.String()
}

// hintWidth returns 1 + visible width for a non-empty hint (the leading
// space), 0 otherwise.
func hintWidth(hint string) int {
	if hint == "" {
		return 0
	}
	return 1 + lipgloss.Width(hint)
}

// actionRow renders the footer actions: "title keys" pairs separated by two
// spaces (DialogSelect's FooterAction).
// actionRow renders the footer actions and, alongside, the column span each
// one occupies (a leading 4-space pad, then "title keys" segments joined by
// two spaces) for a mouse click to resolve back to an action index.
func (a *App) actionRow(o *overlay, w int) (string, []actionHit) {
	parts := make([]string, 0, len(o.actions))
	spans := make([]actionHit, 0, len(o.actions))
	col := 4
	for i, action := range o.actions {
		if i > 0 {
			col += 2 // the "  " separator
		}
		parts = append(parts,
			a.onPanel(a.theme.Text, false).Render(action.title)+" "+
				a.onPanel(a.theme.TextMuted, false).Render(action.keys))
		width := lipgloss.Width(action.title) + 1 + lipgloss.Width(action.keys)
		spans = append(spans, actionHit{start: col, end: col + width, index: i})
		col += width
	}
	return strings.Repeat(" ", 4) + strings.Join(parts, "  "), spans
}

// inputOverlay mirrors DialogPrompt: bold title with esc, a three-row
// textarea showing the value and cursor, and an enter hint.
func (a *App) inputOverlay(w int) string {
	pad := strings.Repeat(" ", 2)
	value := a.onPanel(a.theme.Text, false).Render(a.overlay.input)
	cursor := lipgloss.NewStyle().
		Foreground(a.theme.BackgroundPanel).
		Background(a.theme.Text).
		Render(" ")
	lines := []string{
		a.dialogHeader(2, a.overlay.title, "esc", w),
		"",
		pad + value + cursor,
		"",
		"",
		"",
		pad + a.onPanel(a.theme.Text, false).Render("enter") + " " +
			a.onPanel(a.theme.TextMuted, false).Render("submit"),
		"",
	}
	return strings.Join(lines, "\n")
}

// helpOverlay mirrors ui/dialog-help.tsx: a short paragraph and a right
// aligned ok button in the primary color.
func (a *App) helpOverlay(w int) string {
	pad := strings.Repeat(" ", 2)
	ok := lipgloss.NewStyle().
		Foreground(a.theme.Background).
		Background(a.theme.Primary).
		Render("   ok   ")
	align := w - 4 - lipgloss.Width(ok)
	if align < 1 {
		align = 1
	}
	lines := []string{a.dialogHeader(2, "Help", "esc/enter", w), ""}
	for _, line := range wrapWords(
		"Press ctrl+p to see all available actions and commands in any context.", w-4) {
		lines = append(lines, pad+a.onPanel(a.theme.TextMuted, false).Render(line))
	}
	// The message box's paddingBottom and the parent box's gap are two
	// separate rows between the paragraph and the button.
	return strings.Join(append(lines,
		"", "",
		pad+strings.Repeat(" ", align)+ok,
		"",
	), "\n")
}

// statusOverlay mirrors component/dialog-status.tsx: MCP servers, then the
// formatter and plugin sections with their empty-state fallbacks.
func (a *App) statusOverlay(w int) string {
	pad := strings.Repeat(" ", 2)
	lines := []string{a.dialogHeader(2, "Status", "esc", w), ""}
	if len(a.mcpServers) > 0 {
		lines = append(lines, pad+
			a.onPanel(a.theme.Text, false).Render(fmt.Sprintf("%d MCP Servers", len(a.mcpServers))))
		for _, server := range a.mcpServers {
			dot := lipgloss.NewStyle().Foreground(mcpDotColor(a.theme, server.Status)).Render("•")
			lines = append(lines, pad+
				dot+" "+
				a.onPanel(a.theme.Text, true).Render(server.Name)+" "+
				a.onPanel(a.theme.TextMuted, false).Render(mcpStatusLabel(server)))
		}
	} else {
		lines = append(lines, pad+a.onPanel(a.theme.Text, false).Render("No MCP Servers"))
	}
	lines = append(lines,
		"",
		pad+a.onPanel(a.theme.Text, false).Render("No Formatters"),
		"",
		pad+a.onPanel(a.theme.Text, false).Render("No Plugins"),
		"")
	return strings.Join(lines, "\n")
}

// --- dialog content builders -----------------------------------------------

func (a *App) sessionsOverlay() {
	items := make([]overlayItem, 0, len(a.sessions))
	today := time.Now().Format("Mon Jan 2 2006")
	currentID := ""
	if a.active != nil {
		currentID = a.active.ID
	}
	for i := range a.sessions {
		session := a.sessions[i]
		category := ""
		if session.TimeUpdated > 0 {
			category = time.UnixMilli(session.TimeUpdated).Format("Mon Jan 2 2006")
			if category == today {
				category = "Today"
			}
		}
		footer := ""
		if session.Directory != "" {
			footer = truncateRunes(filepath.Base(session.Directory), 20)
		}
		sessionRef := session
		items = append(items, overlayItem{
			label:    sessionTitleOf(session),
			value:    session.ID,
			category: category,
			footer:   footer,
			action: func() tea.Msg {
				a.active = &sessionRef
				a.view = viewChat
				a.timeline = nil
				a.scrollOffset = 0
				return reloadMsg{}
			},
		})
	}
	a.openList("Sessions", items)
	o := a.overlay
	o.size = dialogLarge
	o.current = currentID
	o.actions = []dialogAction{
		{title: "delete", keys: "ctrl+d", onTrigger: a.deleteSessionAction},
		{title: "rename", keys: "ctrl+r", onTrigger: a.renameSessionAction},
	}
}

// deleteSessionAction mirrors the sessions dialog's two-press delete: the
// first press arms the row, the second deletes and refreshes the list.
func (a *App) deleteSessionAction(item overlayItem) tea.Cmd {
	o := a.overlay
	if o == nil {
		return nil
	}
	if o.armValue != item.value {
		o.armValue = item.value
		o.armKeys = "ctrl+d"
		return nil
	}
	o.armValue = ""
	c := a.client
	id := item.value
	if a.active != nil && a.active.ID == id {
		a.active = nil
		a.view = viewHome
		a.timeline = nil
	}
	return func() tea.Msg {
		if err := c.Delete(a.ctx, id); err != nil {
			return statusMsg{text: "delete failed: " + err.Error()}
		}
		sessions, err := c.Sessions(a.ctx)
		if err != nil {
			return statusMsg{text: "failed to load sessions: " + err.Error()}
		}
		return sessionsMsg{sessions: sessions}
	}
}

// renameSessionAction swaps the sessions dialog for the rename prompt
// (DialogSessionRename).
func (a *App) renameSessionAction(item overlayItem) tea.Cmd {
	sessionID := item.value
	title := ""
	for _, session := range a.sessions {
		if session.ID == sessionID {
			title = sessionTitleOf(session)
		}
	}
	a.openInput("Rename Session", title, func(value string) tea.Msg {
		if err := a.client.Rename(a.ctx, sessionID, value); err != nil {
			return statusMsg{text: "rename failed: " + err.Error()}
		}
		if a.active != nil && a.active.ID == sessionID {
			a.active.Title = value
		}
		for i := range a.sessions {
			if a.sessions[i].ID == sessionID {
				a.sessions[i].Title = value
			}
		}
		return statusMsg{text: "renamed"}
	})
	return nil
}

func (a *App) modelsOverlay() tea.Cmd {
	c := a.client
	return func() tea.Msg {
		models, err := c.Models(a.ctx)
		if err != nil {
			return statusMsg{text: "failed to load models: " + err.Error()}
		}
		items := make([]overlayItem, 0, len(models))
		for _, model := range models {
			provider := a.providerName(model.ProviderID)
			label := model.ProviderID + "/" + model.ID
			title := model.Name
			if title == "" {
				title = label
			}
			model := model
			items = append(items, overlayItem{
				label:    title,
				value:    label,
				category: provider,
				action: func() tea.Msg {
					if a.active == nil {
						return statusMsg{text: "open a session first"}
					}
					if err := a.client.SetModel(a.ctx, a.active.ID, model.ProviderID, model.ID); err != nil {
						return statusMsg{text: "model switch failed: " + err.Error()}
					}
					a.activeModel = label
					return statusMsg{text: "model: " + label}
				},
			})
		}
		a.openList("Select model", items)
		o := a.overlay
		o.current = a.currentModelLabel()
		return nil
	}
}

func (a *App) agentsOverlay() tea.Cmd {
	c := a.client
	return func() tea.Msg {
		agents, err := c.Agents(a.ctx)
		if err != nil {
			return statusMsg{text: "failed to load agents: " + err.Error()}
		}
		items := make([]overlayItem, 0, len(agents))
		for _, agent := range agents {
			agent := agent
			items = append(items, overlayItem{
				label: agent.ID,
				hint:  agent.Description,
				value: agent.ID,
				action: func() tea.Msg {
					if a.active == nil {
						return statusMsg{text: "open a session first"}
					}
					if err := a.client.SetAgent(a.ctx, a.active.ID, agent.ID); err != nil {
						return statusMsg{text: "agent switch failed: " + err.Error()}
					}
					a.activeAgent = agent.ID
					return statusMsg{text: "agent: " + agent.ID}
				},
			})
		}
		a.openList("Select agent", items)
		a.overlay.current = a.activeAgentOr("build")
		return nil
	}
}

func (a *App) themesOverlay() {
	themes := []string{"opencode-dark", "opencode-light"}
	items := make([]overlayItem, 0, len(themes))
	for _, name := range themes {
		name := name
		items = append(items, overlayItem{
			label: name,
			value: name,
			action: func() tea.Msg {
				return statusMsg{text: "theme: " + name}
			},
		})
	}
	a.openList("Themes", items)
	o := a.overlay
	o.current = a.theme.Name
	o.onMove = func(item overlayItem) {
		a.theme = themeResolve(item.value) // live preview like DialogThemeList
	}
}

// commandsRegistry lists the palette commands, mirroring the TS command set
// with its categories and keybind footers.
func (a *App) commandsRegistry() []overlayItem {
	c := a.client
	return []overlayItem{
		{label: "session.new", hint: "New session", category: "Session", footer: "ctrl+x n", action: func() tea.Msg { return reloadMsg{} }},
		{label: "session.list", hint: "List sessions", category: "Session", footer: "ctrl+x l", action: func() tea.Msg {
			a.sessionsOverlay()
			return nil
		}},
		{label: "session.interrupt", hint: "Interrupt", category: "Session", footer: "esc", action: func() tea.Msg {
			if a.active != nil && a.busy {
				_ = c.Interrupt(a.ctx, a.active.ID)
				a.busy = false
			}
			return nil
		}},
		{label: "session.rename", hint: "Rename session", category: "Session", footer: "ctrl+r", action: func() tea.Msg {
			if a.active == nil {
				return statusMsg{text: "open a session first"}
			}
			a.renameSessionAction(overlayItem{value: a.active.ID, label: a.sessionTitle()})
			return nil
		}},
		{label: "session.delete", hint: "Delete session", category: "Session", footer: "ctrl+d", action: func() tea.Msg {
			if a.active == nil {
				return statusMsg{text: "open a session first"}
			}
			// The original only exposes session.delete as an action inside
			// the session list, where an armed second press confirms it.
			// Reaching it from the palette has no list row to arm, so it
			// confirms through DialogConfirm instead of deleting outright.
			id := a.active.ID
			title := a.sessionTitle()
			a.openConfirm("Delete Session",
				fmt.Sprintf("Are you sure you want to delete %q?", title), "",
				func() tea.Msg {
					a.active = nil
					a.view = viewHome
					a.timeline = nil
					go func() { _ = c.Delete(context.Background(), id) }()
					return reloadMsg{}
				}, nil)
			return nil
		}},
		{label: "session.compact", hint: "Compact context", category: "Session", footer: "ctrl+x c", action: func() tea.Msg {
			return statusMsg{text: "compaction runs automatically near the context limit"}
		}},
		{label: "session.timeline", hint: "Jump to message", category: "Session", footer: "ctrl+x g", action: func() tea.Msg {
			a.openList("Timeline", a.timelineOverlayItems())
			a.overlay.size = dialogLarge
			return nil
		}},
		{label: "model.list", hint: "Choose model", category: "Model", footer: "ctrl+x m", action: func() tea.Msg {
			a.modelsOverlay()
			return nil
		}},
		{label: "agent.list", hint: "Choose agent", category: "Agent", footer: "ctrl+x a", action: func() tea.Msg {
			a.agentsOverlay()
			return nil
		}},
		{label: "theme.list", hint: "Choose theme", category: "Theme", footer: "ctrl+x t", action: func() tea.Msg {
			a.themesOverlay()
			return nil
		}},
		{label: "sidebar.toggle", hint: "Toggle sidebar", category: "View", footer: "ctrl+x b", action: func() tea.Msg {
			a.sidebar = !a.sidebar
			return nil
		}},
		{label: "timestamps.toggle", hint: "Toggle timestamps", category: "View", action: func() tea.Msg {
			a.timestamps = !a.timestamps
			return nil
		}},
		{label: "thinking.toggle", hint: thinkingToggleHint(a.thinkingMode), category: "Session", action: func() tea.Msg {
			a.thinkingMode = nextThinkingMode(a.thinkingMode)
			return nil
		}},
		{label: "help.show", hint: "Keybinds", category: "System", action: func() tea.Msg {
			a.overlay = &overlay{kind: overlayHelp, title: "Help"}
			return nil
		}},
		{label: "status.view", hint: "Session status", category: "System", footer: "ctrl+x s", action: func() tea.Msg {
			a.overlay = &overlay{kind: overlayStatus, title: "Status"}
			return nil
		}},
		{label: "app.exit", hint: "Quit", category: "System", footer: "ctrl+c", action: func() tea.Msg { return quitMsg{} }},
	}
}

// commandPalette opens the ctrl+p dialog.
func (a *App) commandPalette() {
	a.openList("Commands", a.commandsRegistry())
}

// fileMentions lists workspace files for @ completion.
func fileMentions(query string) []overlayItem {
	root, err := os.Getwd()
	if err != nil {
		return nil
	}
	var items []overlayItem
	count := 0
	filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if entry.IsDir() {
			name := entry.Name()
			if name == ".git" || name == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		if count >= 400 {
			return filepath.SkipAll
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		count++
		if query != "" && !strings.Contains(strings.ToLower(rel), strings.ToLower(query)) {
			return nil
		}
		items = append(items, overlayItem{label: rel, value: rel})
		if len(items) >= 20 {
			return filepath.SkipAll
		}
		return nil
	})
	sort.Slice(items, func(i, j int) bool { return items[i].label < items[j].label })
	return items
}

var _ = client.Model{}
