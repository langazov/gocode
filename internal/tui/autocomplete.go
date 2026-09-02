package tui

import (
	"image/color"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// Inline completion for "/" and "@", porting
// packages/tui/src/component/prompt/autocomplete.tsx.
//
// The original is not a dialog. It is an absolutely-positioned box anchored
// directly above the prompt — same left edge, same width, at most ten rows,
// with a split left border and the menu background:
//
//	position="absolute" top={position().y - height()} left={position().x}
//	width={position().width} zIndex={100} {...SplitBorder}
//	height={Math.min(10, count, Math.max(1, props.anchor().y))}
//
// It also does not have a filter field of its own: what you type keeps going
// into the prompt, and the list narrows against the text after the trigger.
// This port first reused the centred modal dialog surface, which has a title,
// a search row and a footer, and reads as a different component entirely.

// autocompleteMaxRows is the `Math.min(10, ...)` cap on the popup's height.
const autocompleteMaxRows = 10

type autocompleteKind int

const (
	autocompleteNone autocompleteKind = iota
	autocompleteSlash
	autocompleteMention
)

// autocompleteItem is one row.
type autocompleteItem struct {
	// display is the text shown first, including the trigger ("/new").
	display string
	// description follows it in muted text.
	description string
	// value is what the row inserts.
	value string
	// action runs when the row is chosen. Typed as tea.Cmd rather than a bare
	// message so an interface command's own command can be returned directly:
	// asserting an `any` holding a func() tea.Msg back to the named type
	// tea.Cmd fails, and the action ends up wrapped as a message instead of
	// being run.
	action func() tea.Cmd
}

// autocompleteState is the popup's state. The zero value is closed.
type autocompleteState struct {
	kind     autocompleteKind
	all      []autocompleteItem
	items    []autocompleteItem
	selected int
	// offset is the first visible row, so a long list scrolls.
	offset int
	// trigger is the index in the prompt of the "/" or "@" that opened it.
	trigger int
}

func (s *autocompleteState) visible() bool { return s.kind != autocompleteNone }

func (s *autocompleteState) close() { *s = autocompleteState{} }

// height ports the height memo: at most ten rows, at least one so the empty
// state has somewhere to render.
func (s *autocompleteState) height() int {
	count := len(s.items)
	if count == 0 {
		count = 1
	}
	if count > autocompleteMaxRows {
		return autocompleteMaxRows
	}
	return count
}

// move changes the selection, keeping the visible window around it.
func (s *autocompleteState) move(delta int) {
	if len(s.items) == 0 {
		return
	}
	s.selected += delta
	if s.selected < 0 {
		s.selected = len(s.items) - 1
	}
	if s.selected >= len(s.items) {
		s.selected = 0
	}
	height := s.height()
	if s.selected < s.offset {
		s.offset = s.selected
	}
	if s.selected >= s.offset+height {
		s.offset = s.selected - height + 1
	}
	if s.offset < 0 {
		s.offset = 0
	}
}

// filter narrows the list to rows matching the query, and is what makes typing
// in the prompt drive the popup.
//
// Matching is on the display text and the description, which is what the
// original does for "/" (it adds `description` to the fuzzy keys only for
// slash commands, since for "@" it surfaced unrelated files).
func (s *autocompleteState) filter(query string) {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		s.items = s.all
	} else {
		out := make([]autocompleteItem, 0, len(s.all))
		for _, item := range s.all {
			haystack := strings.ToLower(item.display)
			if s.kind == autocompleteSlash {
				haystack += " " + strings.ToLower(item.description)
			}
			if strings.Contains(haystack, query) {
				out = append(out, item)
			}
		}
		s.items = out
	}
	s.selected, s.offset = 0, 0
}

// autocompleteView renders the popup, or "" when it is closed.
//
// width is the prompt box's width, so the popup lines up with it exactly.
func (a *App) autocompleteView(width int) string {
	state := &a.autocomplete
	if !state.visible() {
		return ""
	}

	background := a.theme.BackgroundMenu
	if background == nil {
		background = a.theme.BackgroundElement
	}
	// The same left-border treatment the prompt box below it uses, so the two
	// read as one stack rather than two unrelated panels.
	inner := width - 1
	if inner < 4 {
		inner = 4
	}

	var rows []string
	if len(state.items) == 0 {
		rows = append(rows, lipgloss.NewStyle().
			Foreground(a.theme.TextMuted).
			Background(background).
			Width(inner).
			Render(" No matching items"))
	} else {
		height := state.height()
		for i := state.offset; i < len(state.items) && i < state.offset+height; i++ {
			rows = append(rows, a.autocompleteRow(state.items[i], i == state.selected, inner, background))
		}
	}

	return lipgloss.NewStyle().
		Border(lipgloss.Border{Left: "┃"}, false, false, false, true).
		BorderForeground(a.theme.Border).
		Background(background).
		Width(borderBoxWidth(width)).
		Render(strings.Join(rows, "\n"))
}

// autocompleteRow renders one option: the name, then its description in muted
// text, with the selected row filled in the primary color.
func (a *App) autocompleteRow(item autocompleteItem, selected bool, width int, background color.Color) string {
	nameFg, descFg := a.theme.Text, a.theme.TextMuted
	rowBackground := background
	if selected {
		rowBackground = a.theme.Primary
		nameFg, descFg = a.theme.SelectedListItemText, a.theme.SelectedListItemText
	}

	// paddingLeft/paddingRight of 1 in the original.
	segment := func(fg color.Color, text string) string {
		return lipgloss.NewStyle().Foreground(fg).Background(rowBackground).Render(text)
	}
	var builder strings.Builder
	builder.WriteString(segment(nameFg, " "+item.display))
	used := lipgloss.Width(item.display) + 1

	if item.description != "" && used+2 < width {
		description := " " + strings.TrimSpace(item.description)
		if lipgloss.Width(description) > width-used-1 {
			description = truncateRunes(description, width-used-1)
		}
		builder.WriteString(segment(descFg, description))
		used += lipgloss.Width(description)
	}
	if pad := width - used; pad > 0 {
		builder.WriteString(lipgloss.NewStyle().Background(rowBackground).Render(strings.Repeat(" ", pad)))
	}
	return builder.String()
}
