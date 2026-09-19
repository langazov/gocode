package dialog

import (
	"io"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2"
	"charm.land/lipgloss/v2"
)

// noteField is the read-only panel behind the help, status and stats
// dialogs: a body of pre-rendered lines under the shell's header, with an
// optional scroll window (§9.7: only the body scrolls, header and hints are
// fixed). It implements huh.Field so the same Shell/form plumbing drives it.
type noteField struct {
	kind Kind

	// body renders the panel's content lines at the panel width. It runs on
	// every render so a stats refresh or a theme swap is picked up live.
	body func(p Palette, width int) []string

	// hints renders the keybind hint row (stats names its scroll keys only
	// while there is something to scroll).
	hints func(p Palette, width int, scrollable bool) string

	// scrollBudget reports how many body rows fit on screen; the stats
	// panel's balanced-margin arithmetic lives in the callback.
	scrollBudget func(height int) int

	// helpLines overrides the help overlay's paragraph with caller-supplied
	// rows (the diff viewer's shortcut sheet). Empty renders the default
	// one-liner — same dialog kind, different content. helpTitle names that
	// panel ("Diff shortcuts" instead of "Help").
	helpLines []string
	helpTitle string

	scrollTop int
	locked    bool

	r renderer
}

func newNote(kind Kind) *noteField {
	return &noteField{kind: kind}
}

func (n *noteField) Init() tea.Cmd { return nil }

func (n *noteField) Update(msg tea.Msg) (huh.Model, tea.Cmd) { return n, nil }

func (n *noteField) View() string {
	lines, _ := n.layout()
	return strings.Join(lines, "\n")
}

func (n *noteField) layout() ([]string, *Hits) {
	p := n.r.theme
	w := n.r.width
	hits := NewHits()
	var body []string
	scrollable := false
	switch n.kind {
	case KindHelp:
		body = n.helpBody(p, w)
	case KindStatus:
		body = n.statusBody(p, w)
	default:
		body, scrollable = n.scrollableBody(p, w)
	}
	lines := []string{header(p, 2, n.title(), "esc", w), ""}
	hits.RowItem = append(hits.RowItem, -1, -1)
	hits.EscRow = 0
	hits.EscStart, hits.EscEnd = escHintRange(p, 2, n.title(), "esc", w)
	lines = append(lines, body...)
	for range body {
		hits.RowItem = append(hits.RowItem, -1)
	}
	// The stats panel's hint row names the keys that are actually live —
	// scroll keys only while there is something to scroll, esc always.
	if n.hints != nil {
		lines = append(lines, n.hints(p, w, scrollable))
		hits.RowItem = append(hits.RowItem, -1)
	}
	return lines, hits
}

// title names the panel for the header row.
func (n *noteField) title() string {
	if n.helpTitle != "" {
		return n.helpTitle
	}
	switch n.kind {
	case KindHelp:
		return "Help"
	case KindStatus:
		return "Status"
	case KindStats:
		return "Stats"
	}
	return ""
}

// ScrollUp/ScrollDown move the body window by rows; only the read-only
// panels with a scroll budget consume them.
func (n *noteField) ScrollUp(rows int) {
	n.scrollTop -= rows
	if n.scrollTop < 0 {
		n.scrollTop = 0
	}
}

func (n *noteField) ScrollDown(rows int) { n.scrollTop += rows }

// ScrollEnd parks scrollTop at a sentinel clamped during the render, the
// same trick handleOverlayKey's "end" arm used.
func (n *noteField) ScrollEnd() { n.scrollTop = 1 << 30 }

// scrollableBody renders a scrollable panel body (stats), windowing the
// callback's lines to the budget and clamping scrollTop at the true maximum.
func (n *noteField) scrollableBody(p Palette, w int) ([]string, bool) {
	if n.body == nil {
		return nil, false
	}
	all := n.body(p, w)
	rows := len(all)
	budget := len(all)
	if n.scrollBudget != nil {
		budget = n.scrollBudget(n.r.height)
	}
	scrollable := rows > budget
	if scrollable {
		// The "more lines" indicator costs a row.
		budget = max(1, budget-1)
		scrollable = rows > budget
	}
	start := 0
	if scrollable {
		start = min(max(n.scrollTop, 0), rows-budget)
	}
	n.scrollTop = start
	if !scrollable {
		return append(append([]string(nil), all...), ""), false
	}
	out := append([]string(nil), all[start:start+budget]...)
	// The blank row separates the footer from the body the same way it
	// separates every section above it.
	out = append(out, "", n.moreIndicator(p, w, start, budget, rows))
	return out, true
}

// moreIndicator reports what the window is hiding, in the timeline's own
// "↑ N more lines" wording.
func (n *noteField) moreIndicator(p Palette, inner, start, rows, total int) string {
	var parts []string
	if start > 0 {
		parts = append(parts, "↑ "+itoa(start)+" more")
	}
	if end := start + rows; end < total {
		parts = append(parts, "↓ "+itoa(total-end)+" more")
	}
	return strings.Repeat(" ", 2) +
		onPanel(p, p.TextMuted, false).Render(
			TruncateRunes(strings.Join(parts, "   "), inner))
}

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	var digits []byte
	for v > 0 {
		digits = append([]byte{byte('0' + v%10)}, digits...)
		v /= 10
	}
	return string(digits)
}

// helpBody mirrors ui/dialog-help.tsx: a short paragraph and a right
// aligned ok button in the primary color. helpLines replaces the paragraph
// with pre-rendered rows (the diff viewer's shortcut sheet).
func (n *noteField) helpBody(p Palette, w int) []string {
	pad := strings.Repeat(" ", 2)
	ok := lipgloss.NewStyle().
		Foreground(p.Text).
		Background(p.Primary).
		Render("   ok   ")
	lines := []string{}
	if len(n.helpLines) > 0 {
		for _, row := range n.helpLines {
			lines = append(lines, pad+onPanel(p, p.TextMuted, false).Render(
				truncateToWidth(row, max(4, w-4))))
		}
	} else {
		for _, line := range WrapWords(
			"Press ctrl+p to see all available actions and commands in any context.", w-4) {
			lines = append(lines, pad+onPanel(p, p.TextMuted, false).Render(line))
		}
	}
	// The message box's paddingBottom and the parent box's gap are two
	// separate rows between the paragraph and the button.
	return append(lines,
		"",
		"",
		pad+lipgloss.PlaceHorizontal(w-4, lipgloss.Right, ok),
		"",
	)
}

// statusBody mirrors component/dialog-status.tsx: MCP servers, then the
// formatter and plugin sections with their empty-state fallbacks.
func (n *noteField) statusBody(p Palette, w int) []string {
	if n.body == nil {
		return nil
	}
	return n.body(p, w)
}

// truncateToWidth keeps a caller-supplied row inside the panel's content
// column (an overlong line does not wrap — it would tear the row the
// compositor splices it into).
func truncateToWidth(row string, width int) string {
	if len(row) <= width {
		return row
	}
	var out strings.Builder
	cells := 0
	for _, r := range row {
		rw := runeWidth(r)
		if cells+rw > width-1 {
			break
		}
		out.WriteRune(r)
		cells += rw
	}
	return out.String() + "…"
}

// --- huh.Field --------------------------------------------------------------

func (n *noteField) Focus() tea.Cmd          { return nil }
func (n *noteField) Blur() tea.Cmd           { return nil }
func (n *noteField) Error() error            { return nil }
func (n *noteField) Skip() bool              { return false }
func (n *noteField) Zoom() bool              { return false }
func (n *noteField) KeyBinds() []key.Binding { return nil }
func (n *noteField) GetKey() string          { return "note" }
func (n *noteField) GetValue() any           { return nil }

func (n *noteField) Run() error                                   { return nil }
func (n *noteField) RunAccessible(w io.Writer, r io.Reader) error { return nil }

func (n *noteField) WithTheme(theme huh.Theme) huh.Field { return n }
func (n *noteField) WithKeyMap(km *huh.KeyMap) huh.Field { return n }
func (n *noteField) WithWidth(width int) huh.Field {
	n.r.width = width
	return n
}
func (n *noteField) WithHeight(height int) huh.Field {
	n.r.height = height
	return n
}
func (n *noteField) WithPosition(p huh.FieldPosition) huh.Field { return n }

// --- alert / confirm ----------------------------------------------------------

// buttonPad is the horizontal padding inside a dialog button. DialogAlert and
// DialogHelp pad their single ok button by 3; DialogConfirm pads its pair by 1.
const (
	alertButtonPad   = 3
	confirmButtonPad = 1
)

// button renders one dialog button: padded label text on the primary fill when
// active, or on the panel in muted text when not (dialog-confirm.tsx).
func button(p Palette, label string, active bool) string {
	padded := strings.Repeat(" ", confirmButtonPad) + label + strings.Repeat(" ", confirmButtonPad)
	if active {
		return lipgloss.NewStyle().
			Foreground(p.SelectedListItemText).
			Background(p.Primary).
			Render(padded)
	}
	return onPanel(p, p.TextMuted, false).Render(padded)
}

// buttonRow right-aligns rendered buttons inside a panel padded by pad on both
// sides (justifyContent="flex-end"), and reports the column span each one
// occupies so a click can be routed back to it.
func buttonRow(p Palette, pad, w int, buttons []string) (string, []Span) {
	total := 0
	for _, b := range buttons {
		total += lipgloss.Width(b)
	}
	align := w - 2*pad - total
	if align < 0 {
		align = 0
	}
	col := pad + align
	spans := make([]Span, 0, len(buttons))
	var row strings.Builder
	row.WriteString(strings.Repeat(" ", col))
	for i, b := range buttons {
		width := lipgloss.Width(b)
		spans = append(spans, Span{Start: col, End: col + width, Index: i})
		col += width
		row.WriteString(b)
	}
	return row.String(), spans
}

// messageBlock renders a dialog's body paragraph: muted, wrapped to the panel
// width, followed by the box's own paddingBottom row.
func messageBlock(p Palette, pad, w int, message string) []string {
	indent := strings.Repeat(" ", pad)
	var lines []string
	for _, line := range WrapWords(message, w-2*pad) {
		lines = append(lines, indent+onPanel(p, p.TextMuted, false).Render(line))
	}
	if len(lines) == 0 {
		lines = append(lines, "")
	}
	return append(lines, "")
}

// alertBody mirrors ui/dialog-alert.tsx: a bold title with an esc hint, a
// muted message, and a single right-aligned ok button on the primary fill.
func alertBody(p Palette, title, message string, w int) (content string, buttonRowIdx int, spans []Span) {
	lines := []string{header(p, 2, title, "esc", w), ""}
	lines = append(lines, messageBlock(p, 2, w, message)...)
	lines = append(lines, "")
	ok := lipgloss.NewStyle().
		Foreground(p.SelectedListItemText).
		Background(p.Primary).
		Render(strings.Repeat(" ", alertButtonPad) + "ok" + strings.Repeat(" ", alertButtonPad))
	row, spans := buttonRow(p, 2, w, []string{ok})
	buttonRowIdx = len(lines)
	lines = append(lines, row, "")
	return strings.Join(lines, "\n"), buttonRowIdx, spans
}

// confirmBody mirrors ui/dialog-confirm.tsx: the alert layout with a Cancel
// and a Confirm button, the active one filled with the primary color. Buttons
// render in cancel-then-confirm order, and left/right move between them.
func confirmBody(p Palette, title, message, cancelLabel string, confirmActive bool, w int) (content string, buttonRowIdx int, spans []Span) {
	lines := []string{header(p, 2, title, "esc", w), ""}
	lines = append(lines, messageBlock(p, 2, w, message)...)
	lines = append(lines, "")
	if cancelLabel == "" {
		cancelLabel = "cancel"
	}
	row, spans := buttonRow(p, 2, w, []string{
		button(p, titlecaseLabel(cancelLabel), !confirmActive),
		button(p, titlecaseLabel("confirm"), confirmActive),
	})
	buttonRowIdx = len(lines)
	lines = append(lines, row, "")
	return strings.Join(lines, "\n"), buttonRowIdx, spans
}

// titlecaseLabel uppercases the first rune, the old titlecase helper's whole
// job for the button labels.
func titlecaseLabel(value string) string {
	runes := []rune(value)
	if len(runes) == 0 {
		return value
	}
	if runes[0] >= 'a' && runes[0] <= 'z' {
		runes[0] = runes[0] - 'a' + 'A'
	}
	return string(runes)
}

// --- input ---------------------------------------------------------------------

// inputField is the DialogPrompt port: a bare editable value with a block
// cursor, rendered line by line (a raw newline spliced into a composited row
// tears the panel).
type inputField struct {
	title       string
	value       string
	placeholder string
	onSubmit    func(string) tea.Msg
	cursorEnd   bool
	r           renderer
}

func newInput(title, placeholder string, value string) *inputField {
	return &inputField{title: title, placeholder: placeholder, value: value}
}

func (f *inputField) Init() tea.Cmd { return nil }

func (f *inputField) Update(msg tea.Msg) (huh.Model, tea.Cmd) { return f, nil }

func (f *inputField) View() string {
	lines, _ := f.layout()
	return strings.Join(lines, "\n")
}

func (f *inputField) layout() ([]string, *Hits) {
	p := f.r.theme
	w := f.r.width
	pad := strings.Repeat(" ", 2)
	cursor := lipgloss.NewStyle().
		Foreground(p.BackgroundPanel).
		Background(p.Text).
		Render(" ")

	// The value is rendered a line at a time so a multi-line entry does not
	// smuggle a raw newline into the middle of a composited row. The cursor
	// sits after the last line.
	entry := strings.Split(f.value, "\n")
	value := make([]string, 0, len(entry))
	for i, line := range entry {
		rendered := pad + onPanel(p, p.Text, false).Render(line)
		if i == len(entry)-1 {
			rendered += cursor
		}
		value = append(value, rendered)
	}

	lines := []string{header(p, 2, f.title, "esc", w), ""}
	lines = append(lines, value...)
	// Three filler rows keep a single-line dialog the height it has always
	// been; a taller entry eats into them before the panel grows.
	for i := len(value); i < 4; i++ {
		lines = append(lines, "")
	}
	lines = append(lines,
		pad+onPanel(p, p.Text, false).Render("enter")+" "+
			onPanel(p, p.TextMuted, false).Render("submit")+"  "+
			onPanel(p, p.Text, false).Render("shift+enter")+" "+
			onPanel(p, p.TextMuted, false).Render("newline"),
		"",
	)
	hits := NewHits()
	hits.RowItem = make([]int, len(lines))
	for i := range hits.RowItem {
		hits.RowItem[i] = -1
	}
	hits.EscRow = 0
	hits.EscStart, hits.EscEnd = escHintRange(p, 2, f.title, "esc", w)
	return lines, hits
}

// Type appends a literal character (typedText's contract: a key name that is
// one rune, or "space").
func (f *inputField) Type(text string) { f.value += text }

// Backspace drops the last rune.
func (f *inputField) Backspace() {
	if run := []rune(f.value); len(run) > 0 {
		f.value = string(run[:len(run)-1])
	}
}

// Newline appends a line break (enter submits, so a newline needs its own
// chord; the renderer grows the panel to match).
func (f *inputField) Newline() { f.value += "\n" }

// Value returns the current entry.
func (f *inputField) Value() string { return f.value }

// Submit trims and dispatches the value; empty or missing handlers do
// nothing, exactly as overlayInput's enter arm always did.
func (f *inputField) Submit() tea.Cmd {
	value := strings.TrimSpace(f.value)
	if value == "" || f.onSubmit == nil {
		return nil
	}
	return StaticMsg(f.onSubmit(value))
}

func (f *inputField) Focus() tea.Cmd          { return nil }
func (f *inputField) Blur() tea.Cmd           { return nil }
func (f *inputField) Error() error            { return nil }
func (f *inputField) Skip() bool              { return false }
func (f *inputField) Zoom() bool              { return false }
func (f *inputField) KeyBinds() []key.Binding { return nil }
func (f *inputField) GetKey() string          { return "input" }
func (f *inputField) GetValue() any           { return f.value }

func (f *inputField) Run() error                                   { return nil }
func (f *inputField) RunAccessible(w io.Writer, r io.Reader) error { return nil }

func (f *inputField) WithTheme(theme huh.Theme) huh.Field { return f }
func (f *inputField) WithKeyMap(km *huh.KeyMap) huh.Field { return f }
func (f *inputField) WithWidth(width int) huh.Field {
	f.r.width = width
	return f
}
func (f *inputField) WithHeight(height int) huh.Field {
	f.r.height = height
	return f
}
func (f *inputField) WithPosition(p huh.FieldPosition) huh.Field { return f }
