package dialog

import (
	"image/color"
	"strings"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2"
	"charm.land/lipgloss/v2"
)

// Shell is the dialog wrapper the App talks to: it owns the panel chrome
// (header with the esc hint, footer action bar), the keyboard contract, and
// the embedded huh.Form that owns the field lifecycle. One Shell is one open
// dialog.
//
// Why a Form at all, when the shell does its own key routing? Because huh's
// form supplies the parts this port should not re-implement: field focus and
// position bookkeeping, accessible mode plumbing, validation surfaces, and
// the group/selector machinery that future multi-field dialogs (the custom
// provider prompt, a memory editor) compose from. The Shell intercepts the
// keys the spec binds (esc, tab, enter, the action keys, the pager keys)
// before delegating to the form, so huh's own keymap never conflicts with
// the documented contract.
type Shell struct {
	Kind  Kind
	Title string
	// size is the panel width before clamping; 0 = Medium.
	size int

	form  *huh.Form
	list  *listField
	note  *noteField
	input *inputField

	// width/height are the terminal measurements the App refreshes on
	// WindowSizeMsg; the panel clamps to width-2.
	width, height int
	theme         Palette

	// message is the body paragraph of an alert or confirm dialog.
	message string
	// cancelLabel overrides the left button's text on a confirm dialog
	// (DialogConfirm's label prop); empty renders "Cancel".
	cancelLabel string
	// confirmActive tracks which confirm button is highlighted; it starts on
	// confirm, matching DialogConfirm's initial active state.
	confirmActive bool

	// onConfirm/onCancel are the alert/confirm branches. onCancel also runs
	// when a list dialog is dismissed (DialogConfirm's promise settling via
	// the dialog's onClose), which is where the theme dialog's revert and
	// the plugins dialog's save live.
	onConfirm func() tea.Msg
	onCancel  func() tea.Msg

	// onDismissed runs after the App has decided to close the dialog — the
	// hook close paths that need the shell's state (the plugins working
	// copy) read it from.
	hits *Hits
}

// NewList builds a list dialog (DialogSelect).
func NewList(title string, items []Item) *Shell {
	l := newList(items)
	l.title = title
	return &Shell{
		Kind:  KindList,
		Title: title,
		list:  l,
		form:  huh.NewForm(huh.NewGroup(l)),
	}
}

// NewInput builds a prompt dialog (DialogPrompt).
func NewInput(title, placeholder, value string) *Shell {
	f := newInput(title, placeholder, value)
	return &Shell{
		Kind:  KindInput,
		Title: title,
		input: f,
		form:  huh.NewForm(huh.NewGroup(f)),
	}
}

// NewAlert mirrors DialogAlert.show: a titled message with a single ok
// button, dismissed by enter or escape.
func NewAlert(title, message string, onConfirm func() tea.Msg) *Shell {
	return &Shell{
		Kind:      KindAlert,
		Title:     title,
		message:   message,
		onConfirm: onConfirm,
	}
}

// NewConfirm mirrors DialogConfirm.show. cancelLabel overrides the left
// button's text; onCancel also runs when the dialog is dismissed with escape,
// matching the TS promise resolving via the dialog's onClose.
func NewConfirm(title, message, cancelLabel string, onConfirm, onCancel func() tea.Msg) *Shell {
	return &Shell{
		Kind:          KindConfirm,
		Title:         title,
		message:       message,
		cancelLabel:   cancelLabel,
		confirmActive: true,
		onConfirm:     onConfirm,
		onCancel:      onCancel,
	}
}

// NewNote builds a read-only panel (help, status, stats). body renders the
// content lines; scrollBudget (optional) makes the body scrollable with the
// given row budget; hints renders the keybind row under a scrollable body.
func NewNote(kind Kind, body func(p Palette, width int) []string, scrollBudget func(height int) int, hints func(p Palette, width int, scrollable bool) string) *Shell {
	n := newNote(kind)
	n.body = body
	n.scrollBudget = scrollBudget
	n.hints = hints
	s := &Shell{Kind: kind, note: n}
	if kind != KindAlert {
		s.form = huh.NewForm(huh.NewGroup(n))
	}
	return s
}

// WithHelpLines overrides the help panel's paragraph (the diff viewer's
// shortcut sheet) and names the panel.
func (s *Shell) WithHelpLines(title string, lines []string) *Shell {
	if s.note != nil {
		s.note.helpLines = lines
		s.note.helpTitle = title
	}
	if title != "" {
		s.Title = title
	}
	return s
}

// SetGeometry records the terminal measurements and panel width. The App
// calls it on open and on every WindowSizeMsg, then again after a theme
// swap; the render is always made from the current values.
func (s *Shell) SetGeometry(width, height int, theme Palette) {
	s.width, s.height, s.theme = width, height, theme
	if s.form != nil {
		s.form.WithWidth(s.panelWidth())
		s.form.WithHeight(height)
	}
	if s.list != nil {
		s.list.r = renderer{width: s.panelWidth(), height: height, theme: theme}
	}
	if s.note != nil {
		s.note.r = renderer{width: s.panelWidth(), height: height, theme: theme}
	}
	if s.input != nil {
		s.input.r = renderer{width: s.panelWidth(), height: height, theme: theme}
	}
}

// panelWidth resolves the dialog's width: the size prop clamped to
// width-2, exactly as overlayPanel did.
func (s *Shell) panelWidth() int {
	size := s.size
	if size == 0 {
		size = Medium
	}
	w := size
	if s.width > 0 && w > s.width-2 {
		w = s.width - 2
	}
	return max(w, 8)
}

// Width reports the panel width the last render used (the App's compositor
// and mouse hit-testing read it).
func (s *Shell) Width() int { return s.panelWidth() }

// SetSize sets the panel width constant (Medium/Large/XLarge) and re-applies
// the geometry so the next render picks it up.
func (s *Shell) SetSize(size int) *Shell {
	s.size = size
	s.SetGeometry(s.width, s.height, s.theme)
	return s
}

// Size returns the configured size (0 = Medium).
func (s *Shell) Size() int { return s.size }

// Init satisfies the embedded-form lifecycle (the App forwards it when it
// opens the dialog; its only visible effect here is a window-size request
// the shell answers from its own geometry).
func (s *Shell) Init() tea.Cmd {
	if s.form != nil {
		return s.form.Init()
	}
	return nil
}

// --- list accessors -----------------------------------------------------------

// Items returns the visible rows (after filtering).
func (s *Shell) Items() []Item { return s.listItems() }

func (s *Shell) listItems() []Item {
	if s.list == nil {
		return nil
	}
	return s.list.items
}

// AllItems returns the unfiltered rows.
func (s *Shell) AllItems() []Item {
	if s.list == nil {
		return nil
	}
	return s.list.all
}

// SetAllItems replaces the row set and re-applies the filter, preserving the
// selected row's identity — the refresh-in-place path the catalog and
// plugin dialogs use.
func (s *Shell) SetAllItems(items []Item) {
	if s.list == nil {
		return
	}
	selected := s.list.SelectedValue()
	s.list.all = items
	s.list.ApplyFilter()
	for i, item := range s.list.items {
		if item.Value == selected {
			s.list.selected = i
			return
		}
	}
}

// SetAllItemsKeepIndex replaces the row set and keeps the cursor on the same
// index, the plugins dialog's toggle refresh.
func (s *Shell) SetAllItemsKeepIndex(items []Item) {
	if s.list == nil {
		return
	}
	selected := s.list.selected
	s.list.all = items
	s.list.ApplyFilter()
	if selected < len(s.list.items) {
		s.list.selected = selected
	}
}

// SelectedItem returns the row under the cursor.
func (s *Shell) SelectedItem() (Item, bool) {
	if s.list == nil {
		return Item{}, false
	}
	return s.list.SelectedItem()
}

// SelectedValue returns the selected row's stable id.
func (s *Shell) SelectedValue() string {
	if s.list == nil {
		return ""
	}
	return s.list.SelectedValue()
}

// Filter returns the filter input's text.
func (s *Shell) Filter() string {
	if s.list == nil {
		return ""
	}
	return s.list.filter
}

// SetFilter replaces the filter text and re-applies it.
func (s *Shell) SetFilter(filter string) *Shell {
	if s.list != nil {
		s.list.filter = filter
		s.list.ApplyFilter()
	}
	return s
}

// RestoreSelection reapplies a filter and moves the cursor back to the row it
// was on, so a background refresh does not move the selection.
func (s *Shell) RestoreSelection(filter, value string) {
	if s.list == nil {
		return
	}
	s.list.filter = filter
	s.list.ApplyFilter()
	for i, item := range s.list.items {
		if item.Value == value {
			s.list.selected = i
			return
		}
	}
}

// SetCurrent marks the row whose value matches with the ● gutter.
func (s *Shell) SetCurrent(value string) *Shell {
	if s.list != nil {
		s.list.current = value
	}
	return s
}

// Current returns the ●-marked value.
func (s *Shell) Current() string {
	if s.list == nil {
		return ""
	}
	return s.list.current
}

// SelectValue moves the cursor onto the row with the given value (the
// variant picker's "cursor and bullet start together" rule).
func (s *Shell) SelectValue(value string) {
	if s.list == nil {
		return
	}
	for i, item := range s.list.items {
		if item.Value == value {
			s.list.selected = i
			return
		}
	}
}

// SetActions sets the footer action bar.
func (s *Shell) SetActions(actions []Action) *Shell {
	if s.list != nil {
		s.list.actions = actions
	}
	return s
}

// FocusedAction reports which footer action holds focus, or -1.
func (s *Shell) FocusedAction() int {
	if s.list == nil {
		return -1
	}
	return s.list.focusedAction
}

// Actions returns the footer action bar.
func (s *Shell) Actions() []Action {
	if s.list == nil {
		return nil
	}
	return s.list.actions
}

// SetPlaceholder sets the filter input's placeholder.
func (s *Shell) SetPlaceholder(placeholder string) *Shell {
	if s.list != nil {
		s.list.placeholder = placeholder
	}
	return s
}

// Placeholder returns the filter input's placeholder.
func (s *Shell) Placeholder() string {
	if s.list == nil {
		return ""
	}
	return s.list.placeholder
}

// SetHideFilter suppresses the filter row (renderFilter={false}).
func (s *Shell) SetHideFilter(v bool) *Shell {
	if s.list != nil {
		s.list.hideFilter = v
	}
	return s
}

// HideFilter reports whether the filter row is suppressed.
func (s *Shell) HideFilter() bool {
	return s.list != nil && s.list.hideFilter
}

// SetLocked disables selection/filtering/activation while leaving the panel
// on screen (DialogSelect's locked prop).
func (s *Shell) SetLocked(v bool) *Shell {
	if s.list != nil {
		s.list.locked = v
	}
	return s
}

// Locked reports the locked state.
func (s *Shell) Locked() bool { return s.list != nil && s.list.locked }

// SetEmptyView overrides the "No results found" fallback.
func (s *Shell) SetEmptyView(title, body string) *Shell {
	if s.list != nil {
		s.list.emptyTitle, s.list.emptyBody = title, body
	}
	return s
}

// EmptyView returns the (title, body) pair.
func (s *Shell) EmptyView() (string, string) {
	if s.list == nil {
		return "", ""
	}
	return s.list.emptyTitle, s.list.emptyBody
}

// SetOnMove sets the live-preview hook: it fires as the selection moves.
func (s *Shell) SetOnMove(fn func(item Item)) *Shell {
	if s.list != nil {
		s.list.onMove = fn
	}
	return s
}

// SetOnActivate replaces enter's (and a row click's) default close-then-run
// with a handler that leaves the dialog open.
func (s *Shell) SetOnActivate(fn func(item Item) tea.Cmd) *Shell {
	if s.list != nil {
		s.list.onActivate = fn
	}
	return s
}

// OnActivate runs the activation hook, if any.
func (s *Shell) OnActivate(item Item) tea.Cmd {
	if s.list == nil || s.list.onActivate == nil {
		return nil
	}
	return s.list.onActivate(item)
}

// HasActivateHook reports whether an onActivate hook replaced the default
// close-then-run dispatch.
func (s *Shell) HasActivateHook() bool {
	return s.list != nil && s.list.onActivate != nil
}

// SetOnCancel sets the dismissal branch (theme revert, plugin save).
func (s *Shell) SetOnCancel(fn func() tea.Msg) *Shell {
	s.onCancel = fn
	return s
}

// OnMoveHook exposes the live-preview hook (tests and the theme dialog).
func (s *Shell) OnMoveHook() func(item Item) {
	if s.list == nil {
		return nil
	}
	return s.list.onMove
}

// OnCancel returns the dismissal branch (theme revert, plugin save).
func (s *Shell) OnCancel() func() tea.Msg { return s.onCancel }

// AlertConfirm exposes the alert dialog's onConfirm branch for the mouse
// path (both the button and a backdrop click resolve it, as escape does).
func (s *Shell) AlertConfirm() func() tea.Msg { return s.onConfirm }

// ConfirmBranch exposes the confirm dialog's onConfirm branch.
func (s *Shell) ConfirmBranch() func() tea.Msg { return s.onConfirm }

// SetOnSubmit wires the list's enter dispatch (used by the tests and by
// dialogs that close-then-run).
func (s *Shell) SetOnSubmit(fn func(item Item) tea.Cmd) *Shell {
	if s.list != nil {
		s.list.onSubmit = fn
	}
	return s
}

// OnInputSubmit sets the input dialog's submit branch.
func (s *Shell) OnInputSubmit(fn func(string) tea.Msg) *Shell {
	if s.input != nil {
		s.input.onSubmit = fn
	}
	return s
}

// Arm arms the two-press confirmation on a row (session/memory delete).
func (s *Shell) Arm(value, keys string) {
	if s.list != nil {
		s.list.armValue, s.list.armKeys = value, keys
	}
}

// Armed reports the armed row's value ("" when none).
func (s *Shell) Armed() string {
	if s.list == nil {
		return ""
	}
	return s.list.armValue
}

// Disarm clears the armed confirmation.
func (s *Shell) Disarm() {
	if s.list != nil {
		s.list.armValue = ""
	}
}

// Move/MoveTo/MoveActionFocus forward the list's navigation to the App's
// mouse handler.
func (s *Shell) Move(delta int) {
	if s.list != nil {
		s.list.Move(delta)
	}
}

func (s *Shell) MoveTo(index int) {
	if s.list != nil {
		s.list.MoveTo(index)
	}
}

func (s *Shell) ScrollUp(rows int) {
	if s.note != nil {
		s.note.ScrollUp(rows)
	}
}

func (s *Shell) ScrollDown(rows int) {
	if s.note != nil {
		s.note.ScrollDown(rows)
	}
}

func (s *Shell) ScrollEnd() {
	if s.note != nil {
		s.note.ScrollEnd()
	}
}

// HasList reports whether this shell is a list dialog (the wheel scrolls
// only lists).
func (s *Shell) HasList() bool { return s.list != nil }

// HasItems reports whether the visible list is non-empty.
func (s *Shell) HasItems() bool { return len(s.listItems()) > 0 }

// ActivateItem activates a row through the shell (mouse release path).
func (s *Shell) ActivateItem(item Item) tea.Cmd {
	if s.list == nil {
		return nil
	}
	return s.list.Activate(item)
}

// EmptyTitle/EmptyBody expose the empty-state pair for tests and refreshes.
func (s *Shell) EmptyTitle() string { return s.list.emptyTitle }
func (s *Shell) EmptyBody() string  { return s.list.emptyBody }

// SetEmptyTitle sets only the title of the empty state.
func (s *Shell) SetEmptyTitle(title string) *Shell {
	if s.list != nil {
		s.list.emptyTitle = title
	}
	return s
}

// SetEmptyBody sets only the body of the empty state.
func (s *Shell) SetEmptyBody(body string) *Shell {
	if s.list != nil {
		s.list.emptyBody = body
	}
	return s
}

// HelpLines returns the help panel's override rows.
func (s *Shell) HelpLines() []string {
	if s.note == nil {
		return nil
	}
	return s.note.helpLines
}

// ConfirmActive reports which confirm button is highlighted.
func (s *Shell) ConfirmActive() bool { return s.confirmActive }

// ScrollPos reports the panel's body scroll offset (tests and the stats
// refresh path).
func (s *Shell) ScrollPos() int {
	if s.list != nil {
		return s.list.scrollTop
	}
	if s.note != nil {
		return s.note.scrollTop
	}
	return 0
}

// SelectedIndexOf returns the row index carrying value, or -1.
func (s *Shell) SelectedIndexOf(value string) int {
	if s.list == nil {
		return -1
	}
	for i, item := range s.list.items {
		if item.Value == value {
			return i
		}
	}
	return -1
}

// SelectedIndex reports the cursor's row index, or -1 (tests only).
func (s *Shell) SelectedIndex() int {
	if s.list == nil {
		return -1
	}
	return s.list.selected
}

// TriggerAction fires a footer action on the selected row.
func (s *Shell) TriggerAction(index int) tea.Cmd {
	if s.list == nil || index < 0 || index >= len(s.list.actions) {
		return nil
	}
	action := s.list.actions[index]
	if action.Standalone {
		return action.OnTrigger(Item{})
	}
	if item, ok := s.list.SelectedItem(); ok {
		return action.OnTrigger(item)
	}
	return nil
}

// InputContent returns the input panel's content lines (the unit the old
// inputOverlay returned, without the shell's paddingTop).
func (s *Shell) InputContent() string {
	if s.input == nil {
		return ""
	}
	return s.input.View()
}

// SetInputValue replaces the input dialog's entry.
func (s *Shell) SetInputValue(value string) {
	if s.input != nil {
		s.input.value = value
	}
}

// Paste inserts pasted text into whatever the open dialog is editing: an
// input dialog's value, or a list dialog's filter. It reports whether the
// dialog took it.
//
// Without this a dialog was simply unpasteable. Bracketed paste arrives as a
// tea.PasteMsg, which the App routed to the prompt editor and dropped
// whenever a dialog was open — so the one field in the interface most likely
// to receive a paste, the provider dialog's API key, could only be typed by
// hand.
func (s *Shell) Paste(text string) bool {
	switch {
	case s.input != nil:
		s.input.Paste(text)
		return true
	case s.list != nil && !s.list.locked && !s.list.hideFilter:
		// A filter is one line: a pasted block collapses to spaces rather
		// than smuggling a newline into a row the compositor splices.
		s.list.filter += collapseLines(text)
		s.list.ApplyFilter()
		return true
	}
	return false
}

// collapseLines folds any line structure in pasted text into single spaces.
func collapseLines(text string) string {
	return strings.Join(strings.Fields(strings.ReplaceAll(text, "\n", " ")), " ")
}

// InputValue returns the input dialog's entry.
func (s *Shell) InputValue() string {
	if s.input == nil {
		return ""
	}
	return s.input.value
}

// --- rendering ------------------------------------------------------------------

// Panel renders the dialog panel, alongside the hit map mouse handling
// needs. The panel is a borderless backgroundPanel block with paddingTop 1,
// exactly like the Dialog container in the original.
func (s *Shell) Panel() (string, *Hits) {
	var content string
	hits := NewHits()
	switch s.Kind {
	case KindHelp, KindStatus, KindStats:
		var lines []string
		lines, hits = s.note.layout()
		content = strings.Join(lines, "\n")
	case KindInput:
		var lines []string
		lines, hits = s.input.layout()
		content = strings.Join(lines, "\n")
	case KindAlert:
		var row int
		var spans []Span
		content, row, spans = alertBody(s.theme, s.Title, s.message, s.panelWidth())
		hits.ButtonRow = row
		hits.Buttons = spans
		hits.EscRow = 0
		hits.EscStart, hits.EscEnd = escHintRange(s.theme, s.Title, "esc", s.panelWidth())
	case KindConfirm:
		var row int
		var spans []Span
		content, row, spans = confirmBody(s.theme, s.Title, s.message, s.cancelLabel, s.confirmActive, s.panelWidth())
		hits.ButtonRow = row
		hits.Buttons = spans
		hits.EscRow = 0
		hits.EscStart, hits.EscEnd = escHintRange(s.theme, s.Title, "esc", s.panelWidth())
	default:
		var lines []string
		lines, hits = s.list.layout()
		content = strings.Join(lines, "\n")
	}
	style := lipgloss.NewStyle().
		Width(s.panelWidth()).
		Background(s.theme.BackgroundPanel).
		PaddingTop(1)
	hits.ShiftRows(1)
	s.hits = hits
	return style.Render(content), hits
}

// LastHits returns the hit map the last render produced.
func (s *Shell) LastHits() *Hits {
	if s.hits == nil {
		return NewHits()
	}
	return s.hits
}

// Origin is the panel's top-left screen cell, shared with the App's
// compositor and mouse hit-testing so both agree on where the panel is.
func (s *Shell) Origin(panelWidth int) (top, left int) {
	return s.height / 4, (s.width - panelWidth) / 2
}

// MouseTarget resolves an absolute screen (row, col) against the panel the
// last render produced, using the same panel + hit map that painted what is
// on screen.
func (s *Shell) MouseTarget(row, col int) Target {
	panel, hits := s.Panel()
	panelW := lipgloss.Width(panel)
	top, left := s.Origin(panelW)
	localRow := row - top
	localCol := col - left
	panelLines := strings.Count(panel, "\n") + 1
	if localRow < 0 || localRow >= panelLines || localCol < 0 || localCol >= panelW {
		return Target{Kind: TargetBackdrop}
	}
	if hits.EscRow == localRow && localCol >= hits.EscStart && localCol < hits.EscEnd {
		return Target{Kind: TargetEsc}
	}
	if hits.ButtonRow == localRow {
		for _, span := range hits.Buttons {
			if localCol >= span.Start && localCol < span.End {
				return Target{Kind: TargetButton, Button: span.Index}
			}
		}
	}
	if hits.ActionRow == localRow {
		for _, span := range hits.Actions {
			if localCol >= span.Start && localCol < span.End {
				return Target{Kind: TargetAction, Action: span.Index}
			}
		}
	}
	if localRow >= 0 && localRow < len(hits.RowItem) {
		if idx := hits.RowItem[localRow]; idx >= 0 && idx < len(s.listItems()) {
			return Target{Kind: TargetItem, Item: idx}
		}
	}
	return Target{Kind: TargetPanel}
}

// --- keyboard -------------------------------------------------------------------

// Key dispatches one keypress inside the open dialog, returning the command
// it implies. This is the port of handleOverlayKey; the shell owns the
// keyboard completely while open.
func (s *Shell) Key(key string) tea.Cmd {
	// Inside a dialog the dialog owns the keyboard: ctrl+c closes it like
	// escape instead of quitting the app (Dialog keybinds in the original) —
	// "like escape" including its onCancel, so it cannot skip the theme
	// dialog's revert or the plugins dialog's save.
	if key == "ctrl+c" {
		return s.resolve(s.onCancel)
	}
	switch s.Kind {
	case KindHelp, KindStatus, KindStats:
		// The three read-only panels answer to one keymap: esc/enter close,
		// and the scroll keys move the body when it overflows. Only the
		// stats panel used to scroll, so the keys the other two printed in
		// their own footers did nothing.
		switch key {
		case "esc", "enter":
			return cmdClose
		case "up", "ctrl+p":
			s.ScrollUp(1)
		case "down", "ctrl+n":
			s.ScrollDown(1)
		case "pgup", "pageup":
			s.ScrollUp(10)
		case "pgdown", "pagedown":
			s.ScrollDown(10)
		case "home":
			s.note.scrollTop = 0
		case "end":
			s.ScrollEnd() // clamp at render time
		}
		return nil
	case KindAlert:
		// Both keys dismiss: DialogAlert.show settles its promise from the
		// ok binding and from the dialog's onClose alike, so escape runs the
		// same continuation enter does.
		if key == "esc" || key == "enter" {
			return s.resolve(s.onConfirm)
		}
		return nil
	case KindConfirm:
		switch key {
		case "esc":
			// DialogConfirm.show resolves undefined on close, running
			// neither branch.
			return cmdClose
		case "left", "right":
			s.confirmActive = !s.confirmActive
			return nil
		case "enter":
			if s.confirmActive {
				return s.resolve(s.onConfirm)
			}
			return s.resolve(s.onCancel)
		}
		return nil
	case KindInput:
		switch key {
		case "esc":
			return cmdClose
		case "enter":
			// The old DialogPrompt closed itself before running onSubmit —
			// memoryRefresh's reopen path keys off "no dialog is open".
			cmd := s.input.Submit()
			if cmd == nil {
				return cmdClose
			}
			return tea.Batch(cmdClose, cmd)
		case "shift+enter":
			// enter submits, so a newline needs its own chord. The renderer
			// grows the panel to match.
			s.input.Newline()
			return nil
		case "backspace":
			s.input.Backspace()
			return nil
		}
		if text, ok := typedText(key); ok {
			s.input.Type(text)
		}
		return nil
	}

	// KindList
	l := s.list
	if l.locked {
		// DialogSelect's locked prop guards filtering, movement and
		// selection alike; only the dialog's own escape still applies.
		if key == "esc" {
			return s.resolve(s.onCancel)
		}
		return nil
	}
	for _, action := range l.actions {
		if action.Keys != "" && key == action.Keys {
			if action.Standalone {
				return action.OnTrigger(Item{})
			}
			if item, ok := l.SelectedItem(); ok {
				return action.OnTrigger(item)
			}
			return nil
		}
	}
	// config/keybind.ts's dialog.select.* defaults. Note what is NOT here:
	// j/k. The filter input owns the keyboard in the original, so those are
	// ordinary characters to type — binding them to movement (as this port
	// once did) made them impossible to search for.
	switch key {
	case "esc":
		// onCancel lets a list dialog undo a live preview it applied as the
		// selection moved (themesOverlay's theme swap, mirroring
		// dialog-theme-list.tsx's onCleanup restoring theme.selected when
		// the dialog closes unconfirmed) — nil for every other list dialog,
		// where this is just the old plain close.
		return s.resolve(s.onCancel)
	case "up", "ctrl+p":
		l.Move(-1)
		return nil
	case "down", "ctrl+n":
		l.Move(1)
		return nil
	case "pgup", "pageup":
		l.Move(-10)
		return nil
	case "pgdown", "pagedown":
		l.Move(10)
		return nil
	case "home":
		l.MoveTo(0)
		return nil
	case "end":
		l.MoveTo(len(l.items) - 1)
		return nil
	case "tab":
		l.MoveActionFocus(1)
		return nil
	case "shift+tab":
		l.MoveActionFocus(-1)
		return nil
	case "backspace":
		if run := []rune(l.filter); len(run) > 0 {
			l.filter = string(run[:len(run)-1])
			l.ApplyFilter()
		}
		return nil
	case "enter":
		// submit(): a focused footer action wins over the selected item.
		if l.focusedAction >= 0 && l.focusedAction < len(l.actions) {
			return s.TriggerAction(l.focusedAction)
		}
		item, ok := l.SelectedItem()
		if !ok {
			return nil
		}
		return s.ActivateItem(item)
	}
	if text, ok := typedText(key); ok {
		l.filter += text
		l.ApplyFilter()
	}
	return nil
}

// resolve closes the dialog and dispatches the chosen branch, the shared
// tail of the alert and confirm button handlers. Closing is expressed as a
// closeDialog command the App runs (the shell cannot drop itself).
func (s *Shell) resolve(branch func() tea.Msg) tea.Cmd {
	cmd := Close()
	if branch == nil {
		return cmd
	}
	if result := branch(); result != nil {
		return tea.Batch(cmd, StaticMsg(result))
	}
	return cmd
}

// typedText reports the literal text a key contributes to an editable field,
// and whether it contributes any at all.
//
// The obvious test — len(key) == 1 — is wrong twice over, because what arrives
// here is a key *name* from tea.KeyMsg.String(), not the character typed:
//
//   - The space bar names itself "space", five bytes, so it was silently
//     dropped. Nothing with a space in it could be typed into a filter or an
//     input dialog.
//   - len() counts bytes, so every non-ASCII character was dropped too: "é" is
//     two bytes and "世" is three, neither of which is 1.
//
// Counting runes instead is safe here because every other key name is a word
// ("enter", "tab", "up") or a chord ("ctrl+a"), all of which are several runes
// long. A one-rune name is therefore always a literal character.
// TypedText exposes typedText for the tests that pin its contract.
func TypedText(key string) (string, bool) { return typedText(key) }

func typedText(key string) (string, bool) {
	if key == "space" {
		return " ", true
	}
	if utf8.RuneCountInString(key) == 1 {
		return key, true
	}
	return "", false
}

// CloseMsg is the message the App reads to drop the open dialog.
type CloseMsg struct{}

// CloseThenMsg tells the App: close the open dialog first, then run the
// carried command. It exists because Go evaluates tea.Batch/tea.Sequence
// arguments eagerly — item.Action() would run before any close could — and
// several actions (the variant hand-off, sessionOpenedMsg) open their own
// dialog or assume none is standing behind them. Then is a thunk for the
// same reason: building the command must not run the action either.
type CloseThenMsg struct{ Then func() tea.Cmd }

// CloseThen wraps a command thunk to run after the dialog closes.
func CloseThen(then func() tea.Cmd) tea.Cmd {
	return func() tea.Msg { return CloseThenMsg{Then: then} }
}

// Close returns the command that closes the dialog without running a branch.
func Close() tea.Cmd { return func() tea.Msg { return CloseMsg{} } }

// cmdClose is an alias kept for readability at the read-only-panel keybinds.
var cmdClose = Close()

// PaletteFrom is the App-side constructor for the renderer's palette, so the
// theme tokens map in exactly one place.
func PaletteFrom(primary, accent, errColor, success, text, textMuted, backgroundPanel, backgroundElement, selectedListItemText color.Color) Palette {
	return Palette{
		Primary:              primary,
		Accent:               accent,
		Error:                errColor,
		Success:              success,
		Text:                 text,
		TextMuted:            textMuted,
		BackgroundPanel:      backgroundPanel,
		BackgroundElement:    backgroundElement,
		SelectedListItemText: selectedListItemText,
	}
}
