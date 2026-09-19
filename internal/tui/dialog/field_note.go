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

	// total/above/below are what the last render measured: the full row
	// count and how many rows the window hides on each side, which the
	// scrollbar and the footer's "N more" counts both read.
	total, above, below int

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
	body, scrollable := n.window(p, w)

	// The gap under the rule is the one every kind takes between its header
	// and its content (a list spends it between the filter and the rows).
	lines := []string{header(p, n.title(), "esc", w), rule(p, w), ""}
	hits.RowItem = append(hits.RowItem, -1, -1, -1)
	hits.EscRow = 0
	hits.EscStart, hits.EscEnd = escHintRange(p, n.title(), "esc", w)

	// A read-only panel scrolls the same way a list does, so it gets the
	// same scrollbar column rather than a second idiom of its own.
	bars := scrollbar(p, len(body), n.total, n.scrollTop)
	for i, line := range body {
		lines = append(lines, bodyRow(p, w, line, bars[i]))
		hits.RowItem = append(hits.RowItem, -1)
	}
	lines = append(lines, rule(p, w))
	hits.RowItem = append(hits.RowItem, -1)
	if n.hints != nil {
		lines = append(lines, n.hints(p, w, scrollable))
	} else {
		above, below := n.hidden()
		lines = append(lines, PanelHints(p, w, scrollable, above, below))
	}
	hits.RowItem = append(hits.RowItem, -1)
	lines = append(lines, "")
	hits.RowItem = append(hits.RowItem, -1)
	return lines, hits
}

// hidden reports how many body rows are above and below the window, for the
// footer's "N more" counts.
func (n *noteField) hidden() (above, below int) {
	return n.above, n.below
}

// bodyRow pads a caller-supplied content line out to the scrollbar column
// and appends it. A line already wider than the column keeps its own width
// — truncating a pre-styled string here would cut an escape sequence in
// half — and simply goes without the bar on that row.
func bodyRow(p Palette, w int, line, bar string) string {
	span := w - rowPad
	used := lipgloss.Width(line)
	if used > span {
		return line
	}
	return line + pad(p, span-used) + bar + pad(p, rowPad-1)
}

// PanelHints is the read-only panels' footer: the scroll keys only while
// there is something to scroll, what the window is hiding, and the way out.
// It is the one hint row help, status and stats all use, so the three read
// identically.
func PanelHints(p Palette, w int, scrollable bool, above, below int) string {
	var left []string
	if scrollable {
		left = append(left, keyHint(p, "scroll", "↑↓"), keyHint(p, "page", "pgup/pgdn"))
		var more []string
		if above > 0 {
			more = append(more, "↑ "+itoa(above)+" more")
		}
		if below > 0 {
			more = append(more, "↓ "+itoa(below)+" more")
		}
		if len(more) > 0 {
			left = append(left, onPanel(p, p.TextMuted, false).Render(strings.Join(more, " ")))
		}
	}
	return hintRow(p, w, left, []string{keyHint(p, "close", "esc")})
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

// itoa renders a small non-negative count without pulling strconv into the
// renderer's hot path.
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

// content is the panel's full body, before any windowing: the help
// paragraph, or the lines the App's callback renders.
func (n *noteField) content(p Palette, w int) []string {
	if n.kind == KindHelp {
		return n.helpBody(p, w)
	}
	if n.body == nil {
		return nil
	}
	return n.body(p, w)
}

// window renders the body and clamps it to the panel's scroll budget.
//
// Help, status and stats all go through it. They used to differ — only the
// stats panel could scroll, so a status panel with a dozen plugins or a
// shortcut sheet longer than the terminal simply ran off the bottom of the
// screen, and the keys that would have moved it did nothing.
func (n *noteField) window(p Palette, w int) ([]string, bool) {
	all := n.content(p, w)
	rows := len(all)
	budget := rows
	if n.scrollBudget != nil {
		budget = max(1, n.scrollBudget(n.r.height))
	}
	n.total = rows
	if rows <= budget {
		n.scrollTop, n.above, n.below = 0, 0, 0
		return all, false
	}
	start := min(max(n.scrollTop, 0), rows-budget)
	n.scrollTop = start
	n.above, n.below = start, rows-start-budget
	return all[start : start+budget], true
}

// helpBody is the help panel's paragraph, or the pre-rendered rows a caller
// supplied instead (the diff viewer's shortcut sheet).
//
// It carries no ok button any more. The one it had was never wired into the
// hit map, so it was a button that could not be clicked; the panel's footer
// names the key that does close it, the way every other dialog does.
func (n *noteField) helpBody(p Palette, w int) []string {
	lines := []string{}
	if len(n.helpLines) > 0 {
		for _, row := range n.helpLines {
			lines = append(lines, pad(p, PadX)+onPanel(p, p.TextMuted, false).Render(
				truncateToWidth(row, max(4, w-2*PadX))))
		}
	} else {
		for _, line := range WrapWords(
			"Press ctrl+p to see all available actions and commands in any context.", w-2*PadX) {
			lines = append(lines, pad(p, PadX)+onPanel(p, p.TextMuted, false).Render(line))
		}
	}
	return lines
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

// button renders one dialog button: a padded label on the primary fill when
// it is the active one, on BackgroundElement when it is not.
//
// Both kinds of button are the same shape now. An alert's single ok used to
// pad by 3 and a confirm's pair by 1, so the two dialogs presented buttons
// of visibly different sizes for the same job; and an inactive button was
// bare muted text, which read as a label rather than as the other thing you
// could press.
func button(p Palette, label string, active bool) string {
	padded := strings.Repeat(" ", buttonPad) + label + strings.Repeat(" ", buttonPad)
	if active {
		return lipgloss.NewStyle().
			Foreground(p.SelectedListItemText).
			Background(p.Primary).
			Bold(true).
			Render(padded)
	}
	return lipgloss.NewStyle().
		Foreground(p.TextMuted).
		Background(p.BackgroundElement).
		Render(padded)
}

// buttonRow right-aligns rendered buttons inside the shared content column,
// with a cell between them, and reports the column span each one occupies
// so a click can be routed back to it.
func buttonRow(p Palette, w int, buttons []string) (string, []Span) {
	total := 0
	for i, b := range buttons {
		if i > 0 {
			total++
		}
		total += lipgloss.Width(b)
	}
	align := w - 2*PadX - total
	if align < 0 {
		align = 0
	}
	col := PadX + align
	spans := make([]Span, 0, len(buttons))
	var row strings.Builder
	row.WriteString(pad(p, col))
	for i, b := range buttons {
		if i > 0 {
			row.WriteString(pad(p, 1))
			col++
		}
		width := lipgloss.Width(b)
		spans = append(spans, Span{Start: col, End: col + width, Index: i})
		col += width
		row.WriteString(b)
	}
	row.WriteString(pad(p, max(0, w-col)))
	return row.String(), spans
}

// messageBlock renders a dialog's body paragraph: muted, wrapped to the
// shared content column.
func messageBlock(p Palette, w int, message string) []string {
	var lines []string
	for _, line := range WrapWords(message, w-2*PadX) {
		lines = append(lines, pad(p, PadX)+onPanel(p, p.TextMuted, false).Render(line))
	}
	if len(lines) == 0 {
		lines = append(lines, "")
	}
	return lines
}

// alertBody is the one-button acknowledgement: the shared header and rules
// around a muted message, with a single right-aligned Ok.
func alertBody(p Palette, title, message string, w int) (content string, buttonRowIdx int, spans []Span) {
	lines := []string{header(p, title, "esc", w), rule(p, w), ""}
	lines = append(lines, messageBlock(p, w, message)...)
	lines = append(lines, "", rule(p, w))
	row, spans := buttonRow(p, w, []string{button(p, "Ok", true)})
	buttonRowIdx = len(lines)
	lines = append(lines, row, "")
	return strings.Join(lines, "\n"), buttonRowIdx, spans
}

// confirmBody is the alert layout with a Cancel and a Confirm button, the
// active one filled with the primary color. Buttons render in
// cancel-then-confirm order, and left/right move between them.
func confirmBody(p Palette, title, message, cancelLabel string, confirmActive bool, w int) (content string, buttonRowIdx int, spans []Span) {
	lines := []string{header(p, title, "esc", w), rule(p, w), ""}
	lines = append(lines, messageBlock(p, w, message)...)
	lines = append(lines, "", rule(p, w))
	if cancelLabel == "" {
		cancelLabel = "cancel"
	}
	row, spans := buttonRow(p, w, []string{
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
	cursor := lipgloss.NewStyle().
		Foreground(p.BackgroundPanel).
		Background(p.Primary).
		Render(" ")

	// The value is rendered a line at a time so a multi-line entry does not
	// smuggle a raw newline into the middle of a composited row. The cursor
	// sits after the last line, and carries the same Primary block the list
	// dialog's filter uses — one caret for the whole interface.
	var value []string
	if f.value == "" && f.placeholder != "" {
		value = append(value, pad(p, PadX)+cursor+
			onPanel(p, p.TextMuted, false).Render(
				TruncateRunes(f.placeholder, max(1, w-2*PadX-1))))
	} else {
		entry := strings.Split(f.value, "\n")
		for i, line := range entry {
			rendered := pad(p, PadX) + onPanel(p, p.Text, false).Render(line)
			if i == len(entry)-1 {
				rendered += cursor
			}
			value = append(value, rendered)
		}
	}

	lines := []string{header(p, f.title, "esc", w), rule(p, w), ""}
	lines = append(lines, value...)
	// Filler rows keep a single-line dialog the height it has always been;
	// a taller entry eats into them before the panel grows.
	for i := len(value); i < 3; i++ {
		lines = append(lines, "")
	}
	lines = append(lines,
		rule(p, w),
		hintRow(p, w,
			[]string{keyHint(p, "submit", "enter"), keyHint(p, "newline", "shift+enter")},
			[]string{keyHint(p, "cancel", "esc")}),
		"",
	)
	hits := NewHits()
	hits.RowItem = make([]int, len(lines))
	for i := range hits.RowItem {
		hits.RowItem[i] = -1
	}
	hits.EscRow = 0
	hits.EscStart, hits.EscEnd = escHintRange(p, f.title, "esc", w)
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
