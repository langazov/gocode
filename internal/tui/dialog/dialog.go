// Package dialog implements the TUI's dialog surfaces on top of
// charm.land/huh/v2.
//
// Every dialog in the interface is a huh.Form embedded in a Shell — the
// wrapper that owns the panel chrome (title + esc hint, footer action bar)
// and the interaction contract documented in
// documentation/recomendations/TUI_RECOMENDATIONS.md §9. huh supplies the
// form lifecycle (fields, focus, validation, accessible mode); two custom
// fields — List and Note — supply the spec's rendering (DialogSelect's row
// geometry, DialogAlert's button row) that stock huh fields cannot express.
//
// The package deliberately imports neither the App nor anything that knows
// about it: items and hooks are plain values, so the TUI wires behavior and
// this package owns geometry.
package dialog

import (
	"strings"

	tea "charm.land/bubbletea/v2"
)

// Panel widths, from the size prop in ui/dialog.tsx.
const (
	Medium = 60
	Large  = 88
	XLarge = 116
)

// Item is one row of a list dialog, mirroring DialogSelectOption: label is
// the title, hint the muted description, footer the right-aligned annotation,
// category the group header, and value the stable id matched against
// Shell.Current for the ● current-item marker. The palette's rows carry their
// dotted command name ("session.new") in Value, the slot the original's
// command.name fills.
type Item struct {
	Label string
	// Slash is the name this item answers to after a "/", and SlashAliases
	// any additional ones. The interface command's own Value is a dotted
	// internal name ("session.new") that nobody types; the original gives
	// each one an explicit slashName ("new") plus aliases ("clear"), and
	// matching on that alone means "/new" resolves to nothing.
	Slash        string
	SlashAliases []string
	Hint         string
	Value        string
	Category     string
	Footer       string
	// Gutter is a glyph drawn in the bullet column (DialogSelectOption's
	// `gutter` slot), used for the connect dialog's ✓ on providers that
	// already have a credential. The current-item bullet wins over it.
	Gutter string
	// GutterOK colors the gutter glyph with the success color rather than
	// the title color, matching `<text fg={theme.success}>✓</text>`.
	GutterOK bool
	Action   func() tea.Msg
	// ArgAction handles "/name args" for an interface command that takes
	// them. Interface commands are otherwise argument-free — runSlashCommand
	// parses the arguments off and drops them — so a nil ArgAction is every
	// command that existed before this field, and they keep behaving
	// identically.
	ArgAction func(string) tea.Msg
	// Suggested and Hidden mirror command-palette.tsx's flags. A hidden
	// command stays slash-resolvable but never shows in the palette or the
	// "/" popup (isVisiblePaletteCommand); a suggested one is repeated under
	// a "Suggested" header while the palette filter is empty.
	Suggested bool
	Hidden    bool
}

// MatchesSlash reports whether an interface command answers to a "/" name.
//
// The dotted command name in Value is matched too, so "/session.new" keeps
// working for anyone who learned it, and a namespace prefix still resolves
// ("/help" would reach "help.show" even without its slash name).
func (i Item) MatchesSlash(name string) bool {
	if name == "" {
		return false
	}
	if i.Slash == name || i.Value == name {
		return true
	}
	for _, alias := range i.SlashAliases {
		if alias == name {
			return true
		}
	}
	return strings.HasPrefix(i.Value, name+".")
}

// Action is a footer action (DialogSelect actions): a title plus the keybind
// that triggers it on the selected item.
type Action struct {
	Title string
	Keys  string
	// Right places the action in the footer's right-aligned group
	// (DialogSelect's `side: "right"`); the default group is left-aligned.
	Right bool
	// Standalone marks an action that does not operate on the selected row —
	// "new", for one. Ordinary actions are skipped when nothing is selected,
	// which for a create action would disable it in exactly the state that
	// needs it most: the empty list. A standalone action is handed a zero
	// Item instead.
	Standalone bool
	OnTrigger  func(item Item) tea.Cmd
}

// Kind discriminates the dialog's shape. List, Input, Alert and Confirm are
// huh-backed forms; the read-only panels are a Note field under the same
// shell, which is why they share the enum rather than growing a second one.
type Kind int

const (
	KindList Kind = iota
	KindInput
	KindHelp
	KindStatus
	KindStats
	KindAlert
	KindConfirm
)

// Hits maps the panel's rendered lines back to what is interactive there,
// built by the same pass that produces the panel content so a mouse hit test
// always matches what is actually on screen. RowItem[i] is the item index
// selectable by panel line i, or -1.
type Hits struct {
	RowItem          []int
	EscRow           int
	EscStart, EscEnd int
	ActionRow        int
	Actions          []Span
	// ButtonRow/Buttons locate the ok / cancel+confirm buttons of an alert
	// or confirm dialog, whose onMouseUp handlers they reproduce.
	ButtonRow int
	Buttons   []Span
}

// Span is one clickable column range on a recorded row.
type Span struct {
	Start, End, Index int
}

// NewHits returns a hit map with its sentinel rows unset.
func NewHits() *Hits {
	return &Hits{EscRow: -1, ActionRow: -1, ButtonRow: -1}
}

// ShiftRows accounts for n lines prepended ahead of everything already
// recorded (the panel's own PaddingTop).
func (h *Hits) ShiftRows(n int) {
	prefix := make([]int, n)
	for i := range prefix {
		prefix[i] = -1
	}
	h.RowItem = append(prefix, h.RowItem...)
	if h.EscRow >= 0 {
		h.EscRow += n
	}
	if h.ActionRow >= 0 {
		h.ActionRow += n
	}
	if h.ButtonRow >= 0 {
		h.ButtonRow += n
	}
}

// TargetKind classifies what an absolute screen cell lands on within the
// open dialog.
type TargetKind int

const (
	TargetBackdrop TargetKind = iota // outside the panel: Dialog's backdrop
	TargetPanel                      // inside the panel, nothing interactive there
	TargetItem
	TargetEsc
	TargetAction
	TargetButton // an alert's ok, or a confirm's cancel/confirm
)

// Target is a resolved mouse target: the kind plus the indexes it carries.
type Target struct {
	Kind   TargetKind
	Item   int
	Action int
	Button int
}

// StaticMsg lifts a message into a command, the tail every action dispatch
// shares (nil means "nothing to run").
func StaticMsg(msg tea.Msg) tea.Cmd {
	if msg == nil {
		return nil
	}
	if cmd, ok := msg.(tea.Cmd); ok {
		return cmd
	}
	return func() tea.Msg { return msg }
}
