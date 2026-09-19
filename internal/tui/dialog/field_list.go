package dialog

import (
	"image/color"
	"io"
	"strings"
	"unicode/utf8"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// listField is the DialogSelect port: a filterable, grouped, scrollable list
// rendered with the exact row geometry of ui/dialog-select.tsx (see
// TUI_RECOMENDATIONS §9.2). It implements huh.Field so a huh.Form can own
// its lifecycle, but every visible aspect — the row box paddings, the ●
// gutter, the category headers, the window arithmetic — is this port's own,
// because stock huh fields cannot express any of it.
type listField struct {
	title  string
	items  []Item
	all    []Item // unfiltered, for filter restore
	filter string

	selected int
	current  string // value of the current item, marked with ●
	actions  []Action

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

	armValue string // armed two-press confirmation (session delete)
	armKeys  string // keybind shown in the armed confirmation label

	onMove func(item Item)
	// onActivate replaces enter's (and a row click's) default close-then-run
	// with a handler that leaves the dialog open. A picker selects one thing
	// and is done; the plugins dialog toggles a row and stays put so several
	// can be flipped in one visit.
	onActivate func(item Item) tea.Cmd
	onSubmit   func(item Item) tea.Cmd

	// placeholder is the filter input's placeholder (DialogSelect's
	// placeholder prop, "Search" when unset).
	placeholder string
	// hideFilter suppresses the filter row, mirroring renderFilter={false}.
	hideFilter bool
	// locked disables selection, filtering and activation while leaving the
	// panel on screen — DialogSelect's locked prop, used with EmptyBody to
	// show a load failure in place of the list.
	locked bool
	// emptyTitle/emptyBody replace the "No results found" fallback, the
	// port of DialogSelect's emptyView.
	emptyTitle string
	emptyBody  string

	// render carries the geometry + palette the renderer needs; the field is
	// the renderer, so it lives here rather than in a separate model.
	r renderer
}

// renderer is everything the field needs to paint: the width and height the
// shell measured for the panel, and the theme tokens the rows color with.
// The shell supplies it on every render (Render(width, height, theme)), so a
// live theme swap is picked up without rebuilding the form.
type renderer struct {
	width  int
	height int
	theme  Palette
}

// Palette is the subset of the TUI theme the dialog renderer reads. It is a
// distinct type (not the tui theme.Theme) so this package stays import-clean
// of the App's own packages.
type Palette struct {
	Primary, Accent, Error, Success    color.Color
	Text, TextMuted                    color.Color
	BackgroundPanel, BackgroundElement color.Color
	SelectedListItemText               color.Color
}

// List builds a list field over items. The title is rendered by the shell's
// header, not the field, but it is kept here because DialogSelect owns both
// and the field needs it for nothing else — see Shell.Title.
func newList(items []Item) *listField {
	return &listField{
		items:         items,
		all:           items,
		focusedAction: -1,
	}
}

// --- huh.Field --------------------------------------------------------------

func (l *listField) Init() tea.Cmd { return nil }

func (l *listField) Update(msg tea.Msg) (huh.Model, tea.Cmd) {
	// The shell intercepts every key before the form sees it (Shell.Key);
	// this method exists to satisfy the interface and to keep the form's
	// bookkeeping (theme propagation, width) in one place.
	return l, nil
}

func (l *listField) View() string {
	lines, _ := l.layout()
	return strings.Join(lines, "\n")
}

func (l *listField) Focus() tea.Cmd          { return nil }
func (l *listField) Blur() tea.Cmd           { return nil }
func (l *listField) Error() error            { return nil }
func (l *listField) Skip() bool              { return false }
func (l *listField) Zoom() bool              { return false }
func (l *listField) KeyBinds() []key.Binding { return nil }
func (l *listField) GetKey() string          { return "list" }
func (l *listField) GetValue() any           { return l.selectedValueAny() }

func (l *listField) selectedValueAny() any {
	item, ok := l.SelectedItem()
	if !ok {
		return ""
	}
	return item.Value
}

func (l *listField) Run() error                                   { return nil }
func (l *listField) RunAccessible(w io.Writer, r io.Reader) error { return nil }

func (l *listField) WithTheme(theme huh.Theme) huh.Field { return l }
func (l *listField) WithKeyMap(km *huh.KeyMap) huh.Field { return l }

func (l *listField) WithWidth(width int) huh.Field {
	l.r.width = width
	return l
}

func (l *listField) WithHeight(height int) huh.Field {
	l.r.height = height
	return l
}

func (l *listField) WithPosition(p huh.FieldPosition) huh.Field { return l }

// --- state ------------------------------------------------------------------

// SelectedItem returns the row under the cursor, if any.
func (l *listField) SelectedItem() (Item, bool) {
	if l.selected < 0 || l.selected >= len(l.items) {
		return Item{}, false
	}
	return l.items[l.selected], true
}

// SelectedValue returns the selected row's stable id, "" when none.
func (l *listField) SelectedValue() string {
	if item, ok := l.SelectedItem(); ok {
		return item.Value
	}
	return ""
}

// ApplyFilter recomputes the visible rows from All and Filter.
//
// The palette's Suggested mirrors carry a "suggested:"-prefixed value, and
// command-palette.tsx drops them the moment a filter is active
// (`if (ref?.filter) return options()`) — the commands themselves stay
// reachable in their real categories below.
func (l *listField) ApplyFilter() {
	if l.filter == "" {
		l.items = l.all
		return
	}
	needle := strings.ToLower(l.filter)
	out := make([]Item, 0, len(l.all))
	for _, item := range l.all {
		if strings.HasPrefix(item.Value, "suggested:") {
			continue
		}
		if strings.Contains(strings.ToLower(item.Label), needle) ||
			strings.Contains(strings.ToLower(item.Hint), needle) ||
			strings.Contains(strings.ToLower(item.Category), needle) ||
			strings.Contains(strings.ToLower(item.Value), needle) {
			out = append(out, item)
		}
	}
	l.items = out
	if l.selected >= len(l.items) {
		l.selected = len(l.items) - 1
	}
	if l.selected < 0 {
		l.selected = 0
	}
}

// Move moves the selection with wraparound, disarming any pending
// confirmation and notifying OnMove (live theme preview).
//
// It is DialogSelect's move(): it wraps at both ends and recenters the
// scroll (moveTo's center=true arm).
func (l *listField) Move(delta int) {
	if len(l.items) == 0 {
		return
	}
	next := l.selected + delta
	if next < 0 {
		next = len(l.items) - 1
	}
	if next >= len(l.items) {
		next = 0
	}
	l.selected = next
	l.centerScroll = true
	l.focusedAction = -1 // moveTo() clears the focused action
	l.armValue = ""
	if l.onMove != nil {
		l.onMove(l.items[l.selected])
	}
}

// MoveTo jumps the selection to an absolute index (mouse hover preselect /
// press), sharing Move's disarm+onMove notification.
//
// It is DialogSelect's moveTo() with its default center=false: home/end and
// mouse hover scroll only as far as they must.
func (l *listField) MoveTo(index int) {
	if index < 0 || index >= len(l.items) {
		return
	}
	l.selected = index
	l.centerScroll = false
	l.focusedAction = -1
	l.armValue = ""
	if l.onMove != nil {
		l.onMove(l.items[l.selected])
	}
}

// MoveActionFocus is DialogSelect's moveAction(): tab enters the footer at
// the first action, shift+tab at the last, and stepping off either end
// releases focus back to the list rather than wrapping.
func (l *listField) MoveActionFocus(direction int) {
	if len(l.actions) == 0 {
		return
	}
	if l.focusedAction < 0 {
		if direction == 1 {
			l.focusedAction = 0
		} else {
			l.focusedAction = len(l.actions) - 1
		}
		return
	}
	next := l.focusedAction + direction
	if next < 0 || next >= len(l.actions) {
		l.focusedAction = -1
		return
	}
	l.focusedAction = next
}

// Activate runs the selected item's action, mirroring DialogSelect's
// enter/onSelect: closes the dialog first, then dispatches whatever the
// action returns. Shared by the enter key and a mouse click/release on the
// row (see Shell.MouseTarget).
//
// A dialog that stays open on activation (onActivate, see the field) takes
// over both paths, so keyboard and mouse cannot disagree about whether the
// panel closes.
//
// The close runs before the action — several actions (the variant hand-off,
// sessionOpenedMsg) open their own dialog or assume none is standing, and
// Go's argument evaluation would otherwise run item.Action() inside the
// Batch(...) call before any command executes. Sequence preserves the order.
func (l *listField) Activate(item Item) tea.Cmd {
	if l.onActivate != nil {
		return l.onActivate(item)
	}
	// The action must not run until after the close lands (see CloseThenMsg),
	// so it is wrapped rather than evaluated here.
	return CloseThen(func() tea.Cmd { return runItemAction(item) })
}

// runItemAction runs a registry item's action and returns the command it
// implies.
//
// An action's declared result is tea.Msg, but several of them return a
// tea.Cmd — the real implementations they delegate to (newSession,
// modelsOverlay, compactNow) are command-producing. Returning one as a
// message would leave it sitting in the update loop unexecuted, so it is
// unwrapped here. Both the palette and the inline "/" popup go through this,
// or one of them silently does nothing.
func runItemAction(item Item) tea.Cmd {
	if item.Action == nil {
		return nil
	}
	return StaticMsg(item.Action())
}

// RunItemActionWithArgs dispatches "/name args". A command that declares no
// argAction ignores the arguments, which is how every interface command
// behaved before argAction existed.
func RunItemActionWithArgs(item Item, arguments string) tea.Cmd {
	if arguments != "" && item.ArgAction != nil {
		return StaticMsg(item.ArgAction(arguments))
	}
	return runItemAction(item)
}

// --- rendering ----------------------------------------------------------------

// layout renders header, filter, body, actions — the same line sequence
// listOverlay produced — alongside the per-line item index map.
func (l *listField) layout() ([]string, *Hits) {
	w := l.r.width
	hits := NewHits()
	p := l.r.theme

	// header / rule / body / rule / footer — the frame every dialog kind
	// shares (§9.1). The rule under the header is what tells the eye where
	// the panel's chrome ends and its content begins, on a surface that has
	// no border to say so.
	lines := []string{header(p, l.title, "esc", w), rule(p, w)}
	hits.RowItem = append(hits.RowItem, -1, -1)
	hits.EscRow = 0
	hits.EscStart, hits.EscEnd = escHintRange(p, l.title, "esc", w)
	if !l.hideFilter {
		lines = append(lines, l.filterRow())
		hits.RowItem = append(hits.RowItem, -1)
	}
	lines = append(lines, "")
	hits.RowItem = append(hits.RowItem, -1)
	if len(l.items) == 0 {
		for _, line := range l.emptyView() {
			lines = append(lines, line)
			hits.RowItem = append(hits.RowItem, -1)
		}
	} else {
		bodyLines, bodyHits := l.body()
		lines = append(lines, bodyLines...)
		hits.RowItem = append(hits.RowItem, bodyHits...)
	}
	if len(l.actions) > 0 {
		lines = append(lines, rule(p, w))
		hits.RowItem = append(hits.RowItem, -1)
		actionLine, spans := l.actionRow(w)
		hits.ActionRow = len(lines)
		hits.Actions = spans
		lines = append(lines, actionLine)
		hits.RowItem = append(hits.RowItem, -1)
	}
	lines = append(lines, "")
	hits.RowItem = append(hits.RowItem, -1)
	return lines, hits
}

// filterRow renders the filter input. It is not a focusable field (§9.3):
// what you type goes into it directly, so it shows what it is — a ⌕ in the
// row's gutter lane and the text starting in the same column as every row
// title below it — rather than pretending to be a form control.
func (l *listField) filterRow() string {
	p := l.r.theme
	icon := onPanel(p, mix(p.TextMuted, p.BackgroundPanel, 0.3), false).Render("⌕")
	cursor := lipgloss.NewStyle().Foreground(p.BackgroundPanel).Background(p.Primary)
	lead := pad(p, PadX) + icon + pad(p, rowTextCol-PadX-1)
	if l.filter != "" {
		// The typed text is content, not annotation: it carries the Text
		// color, and the placeholder below keeps the muted one.
		return lead + onPanel(p, p.Text, false).Render(l.filter) + cursor.Render(" ")
	}
	placeholder := l.placeholder
	if placeholder == "" {
		placeholder = "Search"
	}
	runes := []rune(placeholder)
	return lead + cursor.Render(string(runes[0])) +
		onPanel(p, p.TextMuted, false).Render(string(runes[1:]))
}

// emptyView renders the list's empty state: the "No results found" fallback,
// or the emptyView a caller supplied instead.
//
// The title is red only when the list is locked — the state §9.5 reserves
// for a load that failed. "No memories" is not an error, and coloring it
// like one told the user something untrue about their own machine.
func (l *listField) emptyView() []string {
	p := l.r.theme
	w := l.r.width
	if l.emptyTitle == "" && l.emptyBody == "" {
		return []string{pad(p, PadX) + onPanel(p, p.TextMuted, false).Render("No results found")}
	}
	titleFg := p.Text
	if l.locked {
		titleFg = p.Error
	}
	var lines []string
	if l.emptyTitle != "" {
		lines = append(lines, pad(p, PadX)+onPanel(p, titleFg, true).Render(l.emptyTitle), "")
	}
	for _, line := range WrapWords(l.emptyBody, w-2*PadX) {
		lines = append(lines, pad(p, PadX)+onPanel(p, p.TextMuted, false).Render(line))
	}
	return lines
}

// body renders the grouped option rows, windowed like the scrollbox capped
// at terminal height/2 - 6, alongside a parallel itemIndex-per-line slice
// (-1 for separators/category headers) for mouse hit-testing.
//
// Every row is composed to exactly w-rowPad cells so the scrollbar column
// appended to it lands in the same place on every line — including the
// category headers and the gaps between groups, which is what makes the bar
// read as one continuous track rather than a dotted one.
func (l *listField) body() ([]string, []int) {
	p := l.r.theme
	width := l.r.width
	inner := width - 2*rowPad
	labelCol := l.hintColumn(inner - gutterSpan - rowPadRight)
	type row struct {
		text      string
		selected  bool
		itemIndex int
	}

	var rows []row
	category := ""
	for i, item := range l.items {
		if item.Category != "" && item.Category != category {
			if category != "" {
				rows = append(rows, row{text: pad(p, width-rowPad), itemIndex: -1})
			}
			rows = append(rows, row{text: l.categoryRow(item.Category), itemIndex: -1})
		}
		category = item.Category
		rows = append(rows, row{
			text:      pad(p, rowPad) + l.listRow(item, i, inner, labelCol),
			selected:  i == l.selected,
			itemIndex: i,
		})
	}

	// maxHeight={height()} where height = min(rows, floor(h/2) - 6).
	maxRows := l.r.height/2 - 6
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
		top := l.scrollTop
		if l.centerScroll {
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
		l.scrollTop = top
		window = rows[top : top+maxRows]
	} else {
		l.scrollTop = 0
	}
	bars := scrollbar(p, len(window), len(rows), l.scrollTop)
	texts := make([]string, len(window))
	indexes := make([]int, len(window))
	for i, r := range window {
		texts[i] = r.text + bars[i] + pad(p, rowPad-1)
		indexes[i] = r.itemIndex
	}
	return texts, indexes
}

// categoryRow renders a group header. It is uppercased and set in the
// accent color so it reads as a label for the rows beneath it rather than
// as another row, and it sits in the gutter lane — one step left of the
// titles it groups.
func (l *listField) categoryRow(name string) string {
	p := l.r.theme
	span := l.r.width - rowPad - PadX
	label := TruncateRunes(strings.ToUpper(name), max(1, span))
	return pad(p, PadX) + onPanel(p, p.Accent, true).Render(label) +
		pad(p, span-lipgloss.Width(label))
}

// hintColumn is the column every row's description starts at. Descriptions
// used to begin one space after their own title, so a group of rows ended
// up with its hints scattered across the panel; aligning them turns the
// hints into a second column the eye can scan. The column is capped at half
// the row so one long title cannot push every description off the panel.
func (l *listField) hintColumn(budget int) int {
	longest, hinted := 0, false
	for _, item := range l.items {
		if item.Hint == "" {
			continue
		}
		hinted = true
		if w := lipgloss.Width(TruncateEllipsis(item.Label, titleWidth)); w > longest {
			longest = w
		}
	}
	if !hinted {
		return 0
	}
	return min(longest, max(0, budget/2))
}

// titleWidth is DialogSelectOption's `titleWidth ?? 61`.
const titleWidth = 61

// Row geometry inside the row box: one cell of padding, the ● / ✓ gutter,
// one cell of gap, then the title — so the glyph lands in the same column
// as the header's title and the category labels (PadX), and the row titles
// clear it by two.
const (
	gutterSpan   = rowTextCol - rowPad
	rowPadRight  = 2
	hintSeparate = " · "
)

// listRow renders one option row.
//
// The background belongs to the row *box*, so a highlighted row is filled
// edge to edge including both paddings; the scrollbar column body() appends
// sits outside it.
func (l *listField) listRow(item Item, index, width, labelCol int) string {
	p := l.r.theme
	active := index == l.selected
	armed := l.armValue != "" && l.armValue == item.Value
	current := l.current != "" && item.Value == l.current
	// actionFocused(): while a footer action holds focus the selected row
	// steps back to backgroundElement and its text goes muted.
	muted := l.focusedAction >= 0

	bg := p.BackgroundPanel
	if active {
		switch {
		case muted:
			bg = p.BackgroundElement
		case armed:
			bg = p.Error
		default:
			bg = p.Primary
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
	titleFg := p.Text
	switch {
	case active && !muted:
		titleFg = p.SelectedListItemText
	case muted && (active || current):
		titleFg = p.TextMuted
	case current:
		titleFg = p.Primary
	}
	// The description span and the footer share one color rule.
	secondaryFg := p.TextMuted
	if active && !muted {
		secondaryFg = p.SelectedListItemText
	}
	// The separator is a notch fainter than the description it introduces,
	// so it reads as punctuation instead of as content.
	separatorFg := mix(p.TextMuted, p.BackgroundPanel, 0.45)
	if active {
		separatorFg = secondaryFg
	}

	label := item.Label
	if armed {
		label = "Press " + l.armKeys + " again to confirm"
	}
	// Locale.truncate(title, titleWidth ?? 61) runs before any layout, so a
	// long title carries its ellipsis even in a dialog wide enough to hold it.
	label = TruncateEllipsis(label, titleWidth)

	budget := width - gutterSpan - rowPadRight
	if item.Footer != "" {
		// gap={1} to the flexShrink={0} footer box.
		budget -= 1 + lipgloss.Width(item.Footer)
	}
	if budget < 0 {
		budget = 0
	}

	// The title and its description live in one clipped span, so they are
	// truncated together rather than the description being dropped.
	var body strings.Builder
	used := 0
	if lipgloss.Width(label) > budget {
		label = TruncateRunes(label, budget)
	}
	body.WriteString(segment(titleFg, active && !muted, label))
	used += lipgloss.Width(label)
	if item.Hint != "" && !armed {
		lead := 0
		if labelCol > used {
			lead = labelCol - used
		}
		separator := strings.Repeat(" ", lead) + hintSeparate
		if used+lipgloss.Width(separator) < budget {
			body.WriteString(segment(separatorFg, false, separator))
			used += lipgloss.Width(separator)
			hint := item.Hint
			if lipgloss.Width(hint) > budget-used {
				hint = TruncateRunes(hint, budget-used)
			}
			body.WriteString(segment(secondaryFg, false, hint))
			used += lipgloss.Width(hint)
		}
	}

	var b strings.Builder
	switch {
	case current:
		b.WriteString(fill(1))
		b.WriteString(segment(titleFg, false, "●"))
		b.WriteString(fill(gutterSpan - 2))
	case item.Gutter != "":
		gutterFg := titleFg
		if item.GutterOK {
			gutterFg = p.Success
		}
		b.WriteString(fill(1))
		b.WriteString(segment(gutterFg, false, item.Gutter))
		b.WriteString(fill(gutterSpan - 2))
	default:
		b.WriteString(fill(gutterSpan))
	}
	b.WriteString(body.String())
	b.WriteString(fill(budget - used))
	if item.Footer != "" {
		b.WriteString(fill(1))
		b.WriteString(segment(secondaryFg, false, item.Footer))
	}
	b.WriteString(fill(rowPadRight))
	return b.String()
}

// actionRow renders the footer action bar: the left-aligned group, then the
// right-aligned one, inside the shared content column.
//
// Every action carries a cell of padding on each side whether or not it is
// focused, so the primary fill a focused action wears has room to breathe
// and nothing shifts when focus moves. It also reports the column span each
// action occupies, so a click resolves back to an action index.
func (l *listField) actionRow(w int) (string, []Span) {
	p := l.r.theme
	// The actions' own padding cell means their text still starts at PadX.
	const edge = PadX - 1
	spans := make([]Span, 0, len(l.actions))

	render := func(index int, action Action, col int) (string, int) {
		width := 2 + hintWidth(action.Title, action.Keys)
		var text string
		if index == l.focusedAction {
			fill := lipgloss.NewStyle().
				Foreground(p.SelectedListItemText).Background(p.Primary)
			text = fill.Bold(true).Render(" "+action.Title) +
				fill.Render(keySuffix(action.Keys)+" ")
		} else {
			text = pad(p, 1) + keyHint(p, action.Title, action.Keys) + pad(p, 1)
		}
		spans = append(spans, Span{Start: col, End: col + width, Index: index})
		return text, width
	}

	group := func(indexes []int, col int) (string, int) {
		var out strings.Builder
		used := 0
		for n, index := range indexes {
			if n > 0 {
				out.WriteString(pad(p, 1))
				used++
				col++
			}
			text, width := render(index, l.actions[index], col)
			out.WriteString(text)
			used += width
			col += width
		}
		return out.String(), used
	}

	var leftIdx, rightIdx []int
	for i, action := range l.actions {
		if action.Right {
			rightIdx = append(rightIdx, i)
		} else {
			leftIdx = append(leftIdx, i)
		}
	}

	left, leftWidth := group(leftIdx, edge)
	rightWidth := 0
	for n, index := range rightIdx {
		if n > 0 {
			rightWidth++
		}
		rightWidth += 2 + hintWidth(l.actions[index].Title, l.actions[index].Keys)
	}
	gap := w - 2*edge - leftWidth - rightWidth
	if gap < 0 {
		gap = 0
	}
	right, _ := group(rightIdx, edge+leftWidth+gap)

	return pad(p, edge) + left + pad(p, gap) + right + pad(p, edge), spans
}

// keySuffix is the " keys" tail of an action label, empty for an action
// that has no keybind of its own.
func keySuffix(keys string) string {
	if keys == "" {
		return ""
	}
	return " " + keys
}

// --- shared chrome helpers ----------------------------------------------------

// onPanel styles dialog text: explicit panel background so composed segments
// keep the tint after each segment's reset sequence.
func onPanel(p Palette, fg color.Color, bold bool) lipgloss.Style {
	s := lipgloss.NewStyle().Foreground(fg).Background(p.BackgroundPanel)
	if bold {
		s = s.Bold(true)
	}
	return s
}

// header is the shared title row: bold title left, muted keybind hint right.
// Every kind pads by PadX — a list header used to start at column 4 and an
// alert header at column 2, which made two dialogs opened seconds apart look
// like two different programs.
func header(p Palette, title, hint string, w int) string {
	styled := onPanel(p, p.Text, true).Render(TruncateEllipsis(title, max(1, w-2*PadX-lipgloss.Width(hint)-1)))
	esc := onPanel(p, p.TextMuted, false).Render(hint)
	return pad(p, PadX) + splitRowOn(p, w-2*PadX, styled, esc, 1)
}

// escHintRange mirrors header's own layout math to report the column span its
// right-aligned hint occupies, so a mouse click there can be recognized as
// the TS "esc" label's onMouseUp (dialog-select.tsx / dialog.tsx).
func escHintRange(p Palette, title, hint string, w int) (start, end int) {
	styled := TruncateEllipsis(title, max(1, w-2*PadX-lipgloss.Width(hint)-1))
	gap := w - 2*PadX - lipgloss.Width(styled) - lipgloss.Width(hint)
	if gap < 1 {
		gap = 1
	}
	start = PadX + lipgloss.Width(styled) + gap
	return start, start + lipgloss.Width(hint)
}

// splitRowOn is the space-between row — left, minimum gap, right — with the
// gap painted in the panel background: both halves are styled spans, so a
// run of bare spaces between them would reset the tint across the middle of
// the row.
func splitRowOn(p Palette, width int, left, right string, minWidthGap int) string {
	gap := width - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < minWidthGap {
		gap = minWidthGap
	}
	return left + pad(p, gap) + right
}

// WrapWords wraps text to width on spaces so continuation lines keep their
// indent inside the panel. A single token wider than the line (long flag,
// URL, pasted regex) is chunked in place rather than left to overflow.
func WrapWords(text string, width int) []string {
	if width < 1 {
		width = 1
	}
	var lines []string
	line := ""
	for _, word := range strings.Fields(text) {
		for lipgloss.Width(word) > width {
			head, tail := chunkToWidth(word, width)
			if line != "" {
				lines = append(lines, line)
				line = ""
			}
			lines = append(lines, head)
			word = tail
		}
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

// chunkToWidth splits a single unbreakable token at the width boundary,
// returning the head and the remainder.
func chunkToWidth(value string, width int) (head, tail string) {
	if width < 1 {
		return "", value
	}
	runes := []rune(value)
	cells := 0
	for i, r := range runes {
		w := runeWidth(r)
		if cells+w > width {
			return string(runes[:i]), string(runes[i:])
		}
		cells += w
	}
	return value, ""
}

// runeWidth returns the display width of one rune (1 for combining marks is
// good enough here; chunkToWidth only ever sees plain word characters).
func runeWidth(r rune) int {
	if utf8.RuneSelf <= r && ansi.WcWidth.StringWidth(string(r)) > 1 {
		return 2
	}
	return 1
}

// TruncateEllipsis is util/locale.ts's truncate(): the first len-1 runes plus
// a single-cell ellipsis. It counts runes, not cells, exactly as the original
// counts UTF-16 code units — this is a content rule, not a layout one.
func TruncateEllipsis(value string, max int) string {
	runes := []rune(value)
	if len(runes) <= max || max < 1 {
		return value
	}
	return string(runes[:max-1]) + "…"
}

// TruncateRunes cuts to at most max display cells, preferring a rune
// boundary.
func TruncateRunes(value string, max int) string {
	if max < 1 {
		return ""
	}
	if lipgloss.Width(value) <= max {
		return value
	}
	runes := []rune(value)
	cells := 0
	for i, r := range runes {
		w := runeWidth(r)
		if cells+w > max {
			return string(runes[:i])
		}
		cells += w
	}
	return value
}
