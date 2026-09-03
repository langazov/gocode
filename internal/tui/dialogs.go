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
	"github.com/langazov/gocode-go/internal/tui/client"
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
	label string
	// slash is the name this item answers to after a "/", and slashAliases
	// any additional ones. The interface command's own label is a dotted
	// internal name ("session.new") that nobody types; the original gives each
	// one an explicit slashName ("new") plus aliases ("clear"), and matching
	// on the label alone means "/new" resolves to nothing.
	slash        string
	slashAliases []string
	hint         string
	value        string
	category     string
	footer       string
	// gutter is a glyph drawn in the bullet column (DialogSelectOption's
	// `gutter` slot), used for the connect dialog's ✓ on providers that
	// already have a credential. The current-item bullet wins over it.
	gutter string
	// gutterOK colors the gutter glyph with the success color rather than the
	// title color, matching `<text fg={theme.success}>✓</text>`.
	gutterOK bool
	action   func() tea.Msg
}

// matchesSlash reports whether an interface command answers to a "/" name.
//
// The dotted label is matched too, so "/session.new" keeps working for anyone
// who learned it, and a namespace prefix still resolves ("/help" would reach
// "help.show" even without its slash name).
func (i overlayItem) matchesSlash(name string) bool {
	if name == "" {
		return false
	}
	if i.slash == name || i.label == name {
		return true
	}
	for _, alias := range i.slashAliases {
		if alias == name {
			return true
		}
	}
	return strings.HasPrefix(i.label, name+".")
}

// dialogAction is a footer action (DialogSelect actions): a title plus the
// keybind that triggers it on the selected item.
type dialogAction struct {
	title string
	keys  string
	// right places the action in the footer's right-aligned group
	// (DialogSelect's `side: "right"`); the default group is left-aligned.
	right     bool
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
	// focusedAction is the footer action tab/shift+tab has focused, or -1.
	// DialogSelect's focusedAction signal: while one is focused the selected
	// row dims and enter triggers the action instead of the item.
	focusedAction int
	// scrollTop is the first visible body row, and centerScroll picks which
	// of scrollToSelection's two arms applies. move() (the arrow and page
	// keys) passes center=true and recenters the selection; moveTo() —
	// home/end and mouse hover — leaves it false and scrolls the minimum
	// needed to bring the row back into view.
	scrollTop    int
	centerScroll bool
	armValue     string // armed two-press confirmation (session delete)
	armKeys      string // keybind shown in the armed confirmation label
	onMove       func(item overlayItem)
	input        string // for overlayInput
	onSubmit     func(string) tea.Msg

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
	a.overlay = &overlay{kind: overlayList, title: title, items: items, all: items, focusedAction: -1}
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
// moveSelection is DialogSelect's move(): it wraps at both ends and recenters
// the scroll (moveTo's center=true arm).
func (a *App) moveSelection(o *overlay, delta int) {
	if len(o.items) == 0 {
		return
	}
	next := o.selected + delta
	if next < 0 {
		next = len(o.items) - 1
	}
	if next >= len(o.items) {
		next = 0
	}
	o.selected = next
	o.centerScroll = true
	o.focusedAction = -1 // moveTo() clears the focused action
	o.armValue = ""
	if o.onMove != nil {
		o.onMove(o.items[o.selected])
	}
}

// moveActionFocus is DialogSelect's moveAction(): tab enters the footer at the
// first action, shift+tab at the last, and stepping off either end releases
// focus back to the list rather than wrapping.
func (a *App) moveActionFocus(o *overlay, direction int) {
	if len(o.actions) == 0 {
		return
	}
	if o.focusedAction < 0 {
		if direction == 1 {
			o.focusedAction = 0
		} else {
			o.focusedAction = len(o.actions) - 1
		}
		return
	}
	next := o.focusedAction + direction
	if next < 0 || next >= len(o.actions) {
		o.focusedAction = -1
		return
	}
	o.focusedAction = next
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
		// DialogHelp binds return and escape; every dialog also closes on
		// escape/ctrl+c from the Dialog container. `q` was this port's own
		// invention.
		if key == "esc" || key == "enter" {
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
	// config/keybind.ts's dialog.select.* defaults. Note what is NOT here:
	// j/k. The filter input owns the keyboard in the original, so those are
	// ordinary characters to type — binding them to movement (as this port
	// did) made them impossible to search for.
	switch key {
	case "esc":
		a.closeOverlay()
		return nil
	case "up", "ctrl+p":
		a.moveSelection(o, -1)
		return nil
	case "down", "ctrl+n":
		a.moveSelection(o, 1)
		return nil
	case "pgup", "pageup":
		a.moveSelection(o, -10)
		return nil
	case "pgdown", "pagedown":
		a.moveSelection(o, 10)
		return nil
	case "home":
		a.moveSelectionTo(o, 0)
		return nil
	case "end":
		a.moveSelectionTo(o, len(o.items)-1)
		return nil
	case "tab":
		a.moveActionFocus(o, 1)
		return nil
	case "shift+tab":
		a.moveActionFocus(o, -1)
		return nil
	case "backspace":
		if run := []rune(o.filter); len(run) > 0 {
			o.filter = string(run[:len(run)-1])
			o.applyFilter()
		}
		return nil
	case "enter":
		// submit(): a focused footer action wins over the selected item.
		if o.focusedAction >= 0 && o.focusedAction < len(o.actions) {
			if item, ok := o.selectedItem(); ok {
				return o.actions[o.focusedAction].onTrigger(item)
			}
			return nil
		}
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
	a.closeOverlay()
	return runItemAction(item)
}

// runItemAction runs a registry item's action and returns the command it
// implies.
//
// An action's declared result is tea.Msg, but several of them return a
// tea.Cmd — the real implementations they delegate to (newSession,
// modelsOverlay, compactNow) are command-producing. Returning one as a message
// would leave it sitting in the update loop unexecuted, so it is unwrapped
// here. Both the palette and the inline "/" popup go through this, or one of
// them silently does nothing.
func runItemAction(item overlayItem) tea.Cmd {
	if item.action == nil {
		return nil
	}
	result := item.action()
	if result == nil {
		return nil
	}
	if cmd, ok := result.(tea.Cmd); ok {
		return cmd
	}
	return staticMsg(result)
}

// moveSelectionTo jumps the list selection to an absolute index (mouse hover
// preselect / press), sharing moveSelection's disarm+onMove notification.
// moveSelectionTo is DialogSelect's moveTo() with its default center=false:
// home/end and mouse hover scroll only as far as they must.
func (a *App) moveSelectionTo(o *overlay, index int) {
	if index < 0 || index >= len(o.items) {
		return
	}
	o.selected = index
	o.centerScroll = false
	o.focusedAction = -1
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
	// The outer frame's own padding cells sit outside the composited base, so
	// they carry the scrim's background explicitly — the backdrop covers the
	// whole terminal in the original, margins included.
	return a.dimFrame(a.compositeOverlay(a.underlay(), panel))
}

// dimFrame is frame() with the scrim's background under its padding.
func (a *App) dimFrame(content string) string {
	bg := dimChannels(a.theme.Background)
	return lipgloss.NewStyle().
		Background(lipgloss.Color(fmt.Sprintf("#%02x%02x%02x", bg[0], bg[1], bg[2]))).
		Padding(0, 1).
		MaxHeight(a.height).
		Render(content)
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

// compositeOverlay dims the base render and splices the panel onto it,
// reproducing the Dialog backdrop's black-at-59% scrim (see dim.go).
func (a *App) compositeOverlay(base, panel string) string {
	top, left := a.overlayOrigin(lipgloss.Width(panel))
	return a.spliceAt(a.dimBackdrop(base), panel, top, left)
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
	var out, pending, carry strings.Builder
	cells := 0
	inEscape := false
	started := false
	for _, r := range line {
		if inEscape {
			pending.WriteRune(r)
			if r != 'm' {
				continue
			}
			inEscape = false
			sequence := pending.String()
			pending.Reset()
			// carry is the style still in force at the current cell. An
			// earlier version kept only the escapes immediately preceding the
			// window's first cell, which silently dropped any style opened
			// further left — a line-level style (the dialog backdrop opens
			// one per line, see dim.go) was lost entirely for the slice after
			// the dialog panel, leaving those cells at terminal defaults.
			if sequence == "[m" || sequence == "[0m" {
				carry.Reset()
			} else {
				carry.WriteString(sequence)
			}
			if started {
				out.WriteString(sequence)
			}
			continue
		}
		if r == 0x1b {
			inEscape = true
			pending.WriteRune(r)
			continue
		}
		if cells >= start && cells < end {
			if !started {
				out.WriteString(carry.String())
				started = true
			}
			out.WriteRune(r)
		}
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
		// The scrollbox spans the full panel width; its own paddingLeft/Right
		// of 1 is taken inside listBody.
		bodyLines, bodyHits := a.listBody(o, w)
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
	// The scrollbox pads 1 on each side, outside the row boxes — so the
	// highlight stops one column short of the panel edge, and a category
	// header's own paddingLeft={3} lands at column 4.
	const scrollPad = 1
	inner := width - 2*scrollPad
	indent := strings.Repeat(" ", scrollPad)
	panel := lipgloss.NewStyle().Background(a.theme.BackgroundPanel)

	var rows []row
	category := ""
	for i, item := range o.items {
		if item.category != "" && item.category != category {
			if category != "" {
				rows = append(rows, row{itemIndex: -1})
			}
			rows = append(rows, row{
				text: indent + strings.Repeat(" ", 3) +
					a.onPanel(a.theme.Accent, true).Render(item.category),
				itemIndex: -1,
			})
		}
		category = item.category
		rows = append(rows, row{
			text:      indent + a.listRow(o, item, i, inner) + panel.Render(indent),
			selected:  i == o.selected,
			itemIndex: i,
		})
	}

	// maxHeight={height()} where height = min(rows, floor(h/2) - 6).
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
		top := o.scrollTop
		if o.centerScroll {
			// scrollBy(y - floor(height/2)): bring the row to the middle.
			top = selected - maxRows/2
		} else {
			// The default arm: only scroll far enough to bring the row back
			// inside the viewport.
			if top > len(rows)-maxRows {
				top = len(rows) - maxRows
			}
			if selected < top {
				top = selected
			}
			if selected >= top+maxRows {
				top = selected - maxRows + 1
			}
		}
		if top > len(rows)-maxRows {
			top = len(rows) - maxRows
		}
		if top < 0 {
			top = 0
		}
		o.scrollTop = top
		window = rows[top : top+maxRows]
	} else {
		o.scrollTop = 0
	}
	texts := make([]string, len(window))
	indexes := make([]int, len(window))
	for i, r := range window {
		texts[i] = r.text
		indexes[i] = r.itemIndex
	}
	return texts, indexes
}

// listRow renders one DialogSelect option row.
//
// The geometry is worth spelling out, because this port had it wrong by three
// columns. The row box is `paddingLeft={current||gutter ? 1 : 3}
// paddingRight={3} gap={1}`, and inside it the *title text has its own
// `paddingLeft={3}`*. So a current row spends its first three cells on
// "␣●␣" (pad, bullet, gap) and a plain row on three pad cells, and in
// both cases the title starts at column 6 -- the bullet occupies the gutter
// without shifting the title. This port previously emitted only the row
// padding, so every row sat three columns left of the original.
//
// The background belongs to the row *box*, so a highlighted row is filled
// edge to edge including both paddings; this port used to leave them
// unstyled, which cut three columns off each end of the highlight.
func (a *App) listRow(o *overlay, item overlayItem, index, width int) string {
	active := index == o.selected
	armed := o.armValue != "" && o.armValue == item.value
	current := o.current != "" && item.value == o.current
	// actionFocused(): while a footer action holds focus the selected row
	// steps back to backgroundElement and its text goes muted.
	muted := o.focusedAction >= 0

	bg := a.theme.BackgroundPanel
	if active {
		switch {
		case muted:
			bg = a.theme.BackgroundElement
		case armed:
			bg = a.theme.Error
		default:
			bg = a.theme.Primary
		}
	}
	segment := func(fg color.Color, bold bool, text string) string {
		s := lipgloss.NewStyle().Foreground(fg).Background(bg)
		if bold {
			s = s.Bold(true)
		}
		return s.Render(text)
	}
	fill := func(n int) string {
		if n <= 0 {
			return ""
		}
		return lipgloss.NewStyle().Background(bg).Render(strings.Repeat(" ", n))
	}

	// Option's text() memo, in its own order.
	titleFg := a.theme.Text
	switch {
	case active && !muted:
		titleFg = a.theme.SelectedListItemText
	case muted && (active || current):
		titleFg = a.theme.TextMuted
	case current:
		titleFg = a.theme.Primary
	}
	// The description span and the footer share one color rule.
	secondaryFg := a.theme.TextMuted
	if active && !muted {
		secondaryFg = a.theme.SelectedListItemText
	}

	label := item.label
	if armed {
		label = "Press " + o.armKeys + " again to confirm"
	}
	// Locale.truncate(title, titleWidth ?? 61) runs before any layout, so a
	// long title carries its ellipsis even in a dialog wide enough to hold it.
	label = truncateEllipsis(label, dialogTitleWidth)

	const gutter, padRight = 6, 3
	budget := width - gutter - padRight
	if item.footer != "" {
		// gap={1} to the flexShrink={0} footer box.
		budget -= 1 + lipgloss.Width(item.footer)
	}
	if budget < 0 {
		budget = 0
	}

	// The title and its description live in one `overflow="hidden"` text, so
	// they are clipped together rather than the description being dropped.
	var body strings.Builder
	used := 0
	if lipgloss.Width(label) > budget {
		label = truncateRunes(label, budget)
	}
	body.WriteString(segment(titleFg, active && !muted, label))
	used += lipgloss.Width(label)
	if item.hint != "" && used+1 < budget {
		hint := " " + item.hint
		if lipgloss.Width(hint) > budget-used {
			hint = truncateRunes(hint, budget-used)
		}
		body.WriteString(segment(secondaryFg, false, hint))
		used += lipgloss.Width(hint)
	}

	var b strings.Builder
	switch {
	case current:
		// paddingLeft 1, the bullet gutter, then the row's gap={1}.
		b.WriteString(fill(1))
		b.WriteString(segment(titleFg, false, "●"))
		b.WriteString(fill(1))
	case item.gutter != "":
		gutterFg := titleFg
		if item.gutterOK {
			gutterFg = a.theme.Success
		}
		b.WriteString(fill(1))
		b.WriteString(segment(gutterFg, false, item.gutter))
		b.WriteString(fill(1))
	default:
		b.WriteString(fill(3))
	}
	b.WriteString(fill(3)) // the title text's own paddingLeft
	b.WriteString(body.String())
	b.WriteString(fill(budget - used))
	if item.footer != "" {
		b.WriteString(fill(1))
		b.WriteString(segment(secondaryFg, false, item.footer))
	}
	b.WriteString(fill(padRight))
	return b.String()
}

// dialogTitleWidth is DialogSelectOption's `titleWidth ?? 61`.
const dialogTitleWidth = 61

// truncateEllipsis is util/locale.ts's truncate(): the first len-1 runes plus
// a single-cell ellipsis. It counts runes, not cells, exactly as the original
// counts UTF-16 code units -- this is a content rule, not a layout one.
func truncateEllipsis(value string, max int) string {
	runes := []rune(value)
	if len(runes) <= max || max < 1 {
		return value
	}
	return string(runes[:max-1]) + "…"
}

// actionRow renders DialogSelect's footer action bar:
//
//	<box paddingRight={2} paddingLeft={4} justifyContent="space-between">
//	  <box gap={2}> …left actions… </box>
//	  <box gap={2}> …right actions… </box>
//	</box>
//
// Each action is its own box, so a focused one is filled with the primary
// color across its whole "title label" span. It also reports the column span
// each action occupies, so a click resolves back to an action index.
func (a *App) actionRow(o *overlay, w int) (string, []actionHit) {
	const padLeft, padRight = 4, 2
	spans := make([]actionHit, 0, len(o.actions))

	render := func(index int, action dialogAction, col int) (string, int) {
		focused := index == o.focusedAction
		titleStyle := a.onPanel(a.theme.Text, false)
		keyStyle := a.onPanel(a.theme.TextMuted, false)
		if focused {
			titleStyle = lipgloss.NewStyle().
				Foreground(a.theme.SelectedListItemText).Background(a.theme.Primary).Bold(true)
			keyStyle = lipgloss.NewStyle().
				Foreground(a.theme.SelectedListItemText).Background(a.theme.Primary)
		}
		text := titleStyle.Render(action.title) + keyStyle.Render(" "+action.keys)
		width := lipgloss.Width(action.title) + 1 + lipgloss.Width(action.keys)
		spans = append(spans, actionHit{start: col, end: col + width, index: index})
		return text, width
	}

	group := func(indexes []int, col int) (string, int) {
		var out strings.Builder
		used := 0
		for n, index := range indexes {
			if n > 0 {
				out.WriteString(a.onPanel(a.theme.TextMuted, false).Render("  ")) // gap={2}
				used += 2
				col += 2
			}
			text, width := render(index, o.actions[index], col)
			out.WriteString(text)
			used += width
			col += width
		}
		return out.String(), used
	}

	var leftIdx, rightIdx []int
	for i, action := range o.actions {
		if action.right {
			rightIdx = append(rightIdx, i)
		} else {
			leftIdx = append(leftIdx, i)
		}
	}

	left, leftWidth := group(leftIdx, padLeft)
	rightWidth := 0
	for n, index := range rightIdx {
		if n > 0 {
			rightWidth += 2
		}
		rightWidth += lipgloss.Width(o.actions[index].title) + 1 + lipgloss.Width(o.actions[index].keys)
	}
	gap := w - padLeft - padRight - leftWidth - rightWidth
	if gap < 0 {
		gap = 0
	}
	right, _ := group(rightIdx, padLeft+leftWidth+gap)

	return strings.Repeat(" ", padLeft) + left + strings.Repeat(" ", gap) + right, spans
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

// agentsOverlay opens the agent dialog from the cached list and refreshes in
// the background, for the same reason modelsOverlay does.
func (a *App) agentsOverlay() tea.Cmd {
	a.openAgentDialog(a.agentList)
	return a.loadAgentListCmd()
}

func (a *App) openAgentDialog(agents []client.Agent) {
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
	if len(agents) == 0 {
		a.overlay.emptyTitle = "Loading agents"
		a.overlay.emptyBody = "Fetching the agent list..."
	}
}

// loadAgentListCmd refreshes the agent list.
func (a *App) loadAgentListCmd() tea.Cmd {
	c := a.client
	return func() tea.Msg {
		agents, err := c.Agents(a.ctx)
		if err != nil {
			return nil
		}
		return agentListMsg{agents: agents}
	}
}

// agentListMsg carries a refreshed agent list.
type agentListMsg struct{ agents []client.Agent }

func (a *App) themesOverlay() {
	themes := []string{"gocode-dark", "gocode-light"}
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
		a.invalidateRenderCache()
	}
}

// commandsRegistry lists the palette commands, mirroring the TS command set
// with its categories and keybind footers.
func (a *App) commandsRegistry() []overlayItem {
	c := a.client
	items := []overlayItem{
		{label: "session.new", slash: "new", slashAliases: []string{"clear"}, hint: "New session", category: "Session", footer: "ctrl+x n", action: func() tea.Msg {
			// The same call the ctrl+x n keybind makes. This used to return
			// reloadMsg, which only reloads the *open* session's messages and
			// is a no-op on the home screen — so the command did nothing.
			return a.newSession()
		}},
		{label: "session.list", slash: "sessions", slashAliases: []string{"resume", "continue"}, hint: "List sessions", category: "Session", footer: "ctrl+x l", action: func() tea.Msg {
			a.sessionsOverlay()
			return nil
		}},
		{label: "session.interrupt", slash: "interrupt", hint: "Interrupt", category: "Session", footer: "esc", action: func() tea.Msg {
			// Say why nothing happened. Every other command reports when it
			// cannot act; this one returned silently, which from a command
			// palette or a "/" prompt is indistinguishable from being broken.
			if a.active == nil {
				return statusMsg{text: "open a session first"}
			}
			if !a.busy {
				return statusMsg{text: "nothing is running"}
			}
			_ = c.Interrupt(a.ctx, a.active.ID)
			a.busy = false
			return statusMsg{text: "interrupted"}
		}},
		{label: "session.rename", slash: "rename", hint: "Rename session", category: "Session", footer: "ctrl+r", action: func() tea.Msg {
			if a.active == nil {
				return statusMsg{text: "open a session first"}
			}
			a.renameSessionAction(overlayItem{value: a.active.ID, label: a.sessionTitle()})
			return nil
		}},
		{label: "session.delete", slash: "delete", hint: "Delete session", category: "Session", footer: "ctrl+d", action: func() tea.Msg {
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
		{label: "session.compact", slash: "compact", hint: "Compact context", category: "Session", footer: "ctrl+x c", action: func() tea.Msg {
			// Was a placeholder message even though the server endpoint and
			// the ctrl+x c binding both exist.
			return a.compactNow()
		}},
		{label: "session.timeline", slash: "timeline", hint: "Jump to message", category: "Session", footer: "ctrl+x g", action: func() tea.Msg {
			a.openList("Timeline", a.timelineOverlayItems())
			a.overlay.size = dialogLarge
			return nil
		}},
		{label: "model.list", slash: "models", hint: "Choose model", category: "Model", footer: "ctrl+x m", action: func() tea.Msg {
			return a.modelsOverlay()
		}},
		{label: "agent.list", slash: "agents", hint: "Choose agent", category: "Agent", footer: "ctrl+x a", action: func() tea.Msg {
			return a.agentsOverlay()
		}},
		{label: "theme.list", slash: "themes", hint: "Choose theme", category: "Theme", footer: "ctrl+x t", action: func() tea.Msg {
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
			a.invalidateRenderCache()
			return nil
		}},
		{label: "help.show", slash: "help", hint: "Keybinds", category: "System", action: func() tea.Msg {
			a.overlay = &overlay{kind: overlayHelp, title: "Help"}
			return nil
		}},
		{label: "status.view", slash: "status", hint: "Session status", category: "System", footer: "ctrl+x s", action: func() tea.Msg {
			a.overlay = &overlay{kind: overlayStatus, title: "Status"}
			return nil
		}},
		{label: "app.exit", slash: "exit", slashAliases: []string{"quit", "q"}, hint: "Quit", category: "System", footer: "ctrl+c", action: func() tea.Msg { return quitMsg{} }},
	}
	// The sidebar footer's getting-started card is dismissed by clicking its
	// "✕" upstream. This port has no per-widget mouse targets inside the
	// sidebar panel (see link.go on why clickable regions have to be recorded
	// in absolute screen cells, which the panel does not know), so the
	// dismissal is exposed as a palette command instead — offered only while
	// the card is actually showing, like the "✕" itself.
	if !a.paidProviderAvailable() && !a.dismissedGettingStarted {
		items = append(items, overlayItem{
			label: "getting_started.dismiss", hint: "Dismiss getting started", category: "System",
			action: func() tea.Msg {
				a.dismissedGettingStarted = true
				return nil
			},
		})
	}
	return items
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
