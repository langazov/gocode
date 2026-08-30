package tui

import (
	"encoding/base64"
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/x/ansi"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// This file ports packages/tui/src mouse support to Bubble Tea's cell-mouse
// model: app.tsx's copy-on-select backdrop handler (util/selection.ts),
// dialog.tsx's backdrop/esc dismiss, and dialog-select.tsx's hover-preselect
// + click-to-activate list rows and footer actions. opentui tracks a real
// per-renderable text selection with visual highlight; Bubble Tea has no such
// primitive, so textSelection/applySelectionHighlight rebuild the same
// effect by operating on the plain rendered frame string, using
// charmbracelet/x/ansi to slice/wrap styled lines without corrupting escape
// sequences (the same technique lipgloss and Bubble Tea's own viewport use
// internally).

const wheelScrollLines = 3

// textSelection is a drag-to-select range in absolute screen cells,
// standing in for opentui's Renderer.getSelection().
type textSelection struct {
	active               bool
	anchorRow, anchorCol int
	row, col             int
}

func (s *textSelection) begin(row, col int) {
	*s = textSelection{active: true, anchorRow: row, anchorCol: col, row: row, col: col}
}

func (s *textSelection) extend(row, col int) {
	if !s.active {
		return
	}
	s.row, s.col = row, col
}

func (s *textSelection) clear() { *s = textSelection{} }

// hasRange reports a real (non-empty) selection, mirroring
// `selection.getSelectedText()` being non-empty.
func (s *textSelection) hasRange() bool {
	return s.anchorRow != s.row || s.anchorCol != s.col
}

// ordered returns the two corners top-to-bottom/left-to-right regardless of
// drag direction.
func (s *textSelection) ordered() (startRow, startCol, endRow, endCol int) {
	if s.anchorRow < s.row || (s.anchorRow == s.row && s.anchorCol <= s.col) {
		return s.anchorRow, s.anchorCol, s.row, s.col
	}
	return s.row, s.col, s.anchorRow, s.anchorCol
}

// handleMouse dispatches a tea.MouseMsg, mirroring the three opentui handlers
// this port cares about: wheel scroll, drag-select, and dialog click/hover.
// bubbletea v2 splits what v1 encoded as a single MouseMsg + Action field
// into four concrete message types; the message's own type now says what
// v1's Action field used to.
func (a *App) handleMouse(msg tea.MouseMsg) tea.Cmd {
	if a.quitting || a.width == 0 {
		return nil
	}
	switch msg := msg.(type) {
	case tea.MouseWheelMsg:
		return a.handleWheel(msg.Mouse())
	case tea.MouseClickMsg:
		return a.handleMousePress(msg.Mouse())
	case tea.MouseMotionMsg:
		return a.handleMouseMotion(msg.Mouse())
	case tea.MouseReleaseMsg:
		return a.handleMouseRelease(msg.Mouse())
	}
	return nil
}

// handleWheel scrolls the open dialog's list (mirroring nothing in TS
// directly — opentui's scrollbox handles wheel natively — but the same
// affordance belongs here since Bubble Tea has no such native scrollbox) or,
// with no dialog open, the chat timeline (pgup/pgdown's finer-grained mouse
// equivalent).
func (a *App) handleWheel(msg tea.Mouse) tea.Cmd {
	up := msg.Button == tea.MouseWheelUp
	if a.overlay != nil {
		if len(a.overlay.items) == 0 {
			return nil
		}
		delta := 1
		if up {
			delta = -1
		}
		a.moveSelection(a.overlay, delta)
		return nil
	}
	if a.view != viewChat {
		return nil
	}
	if up {
		a.scrollOffset += wheelScrollLines
		return nil
	}
	a.scrollOffset -= wheelScrollLines
	if a.scrollOffset < 0 {
		a.scrollOffset = 0
	}
	return nil
}

// handleMousePress starts a drag-selection and, over an open dialog's list,
// preselects the row under the cursor (dialog-select.tsx's onMouseDown moveTo).
func (a *App) handleMousePress(msg tea.Mouse) tea.Cmd {
	if msg.Button != tea.MouseLeft {
		return nil
	}
	a.selection.begin(msg.Y, msg.X)
	if a.overlay != nil {
		if target := a.overlayMouseTarget(msg.Y, msg.X); target.kind == overlayTargetItem {
			a.moveSelectionTo(a.overlay, target.item)
		}
	}
	return nil
}

// handleMouseMotion extends an active drag-selection, or — with no button
// held — preselects the dialog row under the cursor (onMouseOver moveTo).
func (a *App) handleMouseMotion(msg tea.Mouse) tea.Cmd {
	if a.selection.active {
		a.selection.extend(msg.Y, msg.X)
		return nil
	}
	if a.overlay != nil {
		if target := a.overlayMouseTarget(msg.Y, msg.X); target.kind == overlayTargetItem {
			a.moveSelectionTo(a.overlay, target.item)
		}
	}
	return nil
}

// handleMouseRelease ends a drag: a real range copies to the clipboard
// (util/selection.ts's copy) and consumes the click; otherwise it's a plain
// click, dispatched to whatever is under the cursor.
func (a *App) handleMouseRelease(msg tea.Mouse) tea.Cmd {
	dragged := a.selection.active && a.selection.hasRange()
	a.selection.active = false
	if dragged {
		cmd := a.copySelectionCmd()
		a.selection.clear()
		return cmd
	}
	a.selection.clear()
	if msg.Button != tea.MouseLeft {
		return nil
	}
	return a.handleClick(msg.X, msg.Y)
}

// handleClick mirrors dialog.tsx's backdrop/esc dismiss and dialog-select.tsx's
// onMouseUp row/action activation, and — in the chat view — ReasoningHeader's
// onMouseUp toggle (see reasoningClickTarget).
func (a *App) handleClick(x, y int) tea.Cmd {
	if a.overlay == nil {
		if href := a.linkAt(y, x); href != "" {
			_ = openURL(href)
			return nil
		}
		if a.view == viewChat {
			if id, ok := a.reasoningClickTarget(y); ok {
				a.expandedReasoning[id] = !a.expandedReasoning[id]
			}
		}
		return nil
	}
	o := a.overlay
	switch target := a.overlayMouseTarget(y, x); target.kind {
	case overlayTargetBackdrop, overlayTargetEsc:
		a.closeOverlay()
	case overlayTargetItem:
		return a.activateItem(o.items[target.item])
	case overlayTargetAction:
		if sel, ok := o.selectedItem(); ok {
			return o.actions[target.action].onTrigger(sel)
		}
	}
	return nil
}

// reasoningClickTarget resolves an absolute screen row against the reasoning
// header rows viewChat's last render cached (chatReasoningRows/
// chatWindowPad/chatWindowStart), mirroring ReasoningPart's per-instance
// `<box onMouseUp={toggle}>` — column is ignored (the header's own text is
// short and left-aligned, and TS's toggle also doesn't require expanding
// the whole width). Only meaningful when thinkingMode is "hide": a click
// while "show" toggles a per-part flag reasoningBlock never reads (every
// block already renders open), matching TS's `toggle()` no-op when
// `!inMinimal()`.
func (a *App) reasoningClickTarget(row int) (id string, ok bool) {
	if a.thinkingMode == "show" {
		return "", false
	}
	i := row - a.chatWindowPad
	if i < 0 {
		return "", false
	}
	id, found := a.chatReasoningRows[a.chatWindowStart+i]
	return id, found
}

// overlayTargetKind classifies what an absolute screen cell lands on within
// the open dialog.
type overlayTargetKind int

const (
	overlayTargetBackdrop overlayTargetKind = iota // outside the panel: Dialog's backdrop
	overlayTargetPanel                             // inside the panel, nothing interactive there
	overlayTargetItem
	overlayTargetEsc
	overlayTargetAction
)

type overlayTarget struct {
	kind   overlayTargetKind
	item   int
	action int
}

// overlayMouseTarget resolves an absolute screen (row, col) against the
// currently open dialog, using the exact same panel render + hit map that
// produced what's on screen (see overlayPanel/overlayOrigin in dialogs.go).
func (a *App) overlayMouseTarget(row, col int) overlayTarget {
	panel, hits := a.overlayPanel()
	panelW := lipgloss.Width(panel)
	top, left := a.overlayOrigin(panelW)
	localRow := row - top
	localCol := col - left
	panelLines := strings.Count(panel, "\n") + 1
	if localRow < 0 || localRow >= panelLines || localCol < 0 || localCol >= panelW {
		return overlayTarget{kind: overlayTargetBackdrop}
	}
	if hits.escRow == localRow && localCol >= hits.escStart && localCol < hits.escEnd {
		return overlayTarget{kind: overlayTargetEsc}
	}
	if hits.actionRow == localRow {
		for _, span := range hits.actions {
			if localCol >= span.start && localCol < span.end {
				return overlayTarget{kind: overlayTargetAction, action: span.index}
			}
		}
	}
	if localRow >= 0 && localRow < len(hits.rowItem) {
		if idx := hits.rowItem[localRow]; idx >= 0 && a.overlay != nil && idx < len(a.overlay.items) {
			return overlayTarget{kind: overlayTargetItem, item: idx}
		}
	}
	return overlayTarget{kind: overlayTargetPanel}
}

// currentFrame renders exactly what's on screen right now (sans selection
// highlight), the coordinate space mouse events and selection extraction
// both operate in.
func (a *App) currentFrame() string {
	if a.overlay != nil {
		return a.viewOverlay()
	}
	if a.view == viewHome {
		return a.compositeToast(a.viewHome())
	}
	return a.compositeToast(a.viewChat())
}

// applySelectionHighlight reverse-videos the selected cell range on top of
// an already-rendered frame, standing in for opentui's real text-selection
// paint. The middle slice is ANSI-stripped before the reverse-video wrap
// (rather than left in place, as a naive prefix/wrap/suffix composition
// would do): glamour's chroma-highlighted code emits a full SGR reset after
// nearly every token, and any such reset appearing inside the wrapped
// span — trivially reachable once a selection spans more than one syntax
// token — cancels the outer reverse-video attribute right there, so only
// the first token would visibly highlight instead of the whole span.
// Stripping first guarantees one uniform highlighted block regardless of
// how many colored runs the original text was made of.
func (a *App) applySelectionHighlight(content string) string {
	if !a.selection.hasRange() {
		return content
	}
	startRow, startCol, endRow, endCol := a.selection.ordered()
	lines := strings.Split(content, "\n")
	for row := max(startRow, 0); row <= endRow && row < len(lines); row++ {
		line := lines[row]
		width := ansi.StringWidth(line)
		colStart, colEnd := selectionCols(row, startRow, startCol, endRow, endCol, width)
		if colStart >= colEnd {
			continue
		}
		prefix := ansi.Cut(line, 0, colStart)
		middle := ansi.Strip(ansi.Cut(line, colStart, colEnd))
		suffix := ansi.Cut(line, colEnd, width)
		lines[row] = prefix + "\x1b[7m" + middle + "\x1b[27m" + suffix
	}
	return strings.Join(lines, "\n")
}

// selectedText extracts the plain (ANSI-stripped) text under the current
// selection from what's on screen right now, mirroring opentui's
// Selection.copy reading the renderer's selection buffer.
func (a *App) selectedText() string {
	if !a.selection.hasRange() {
		return ""
	}
	return extractSelection(a.currentFrame(), a.selection)
}

// extractSelection is selectedText's pure half, split out so the extraction
// math is testable without rendering a full frame.
func extractSelection(content string, sel textSelection) string {
	lines := strings.Split(content, "\n")
	startRow, startCol, endRow, endCol := sel.ordered()
	var out []string
	for row := max(startRow, 0); row <= endRow && row < len(lines); row++ {
		line := lines[row]
		width := ansi.StringWidth(line)
		colStart, colEnd := selectionCols(row, startRow, startCol, endRow, endCol, width)
		if colStart >= colEnd {
			out = append(out, "")
			continue
		}
		out = append(out, strings.TrimRight(ansi.Strip(ansi.Cut(line, colStart, colEnd)), " "))
	}
	return strings.TrimRight(strings.Join(out, "\n"), "\n")
}

// selectionCols clamps the selection's column span for one line to that
// line's visible width, opening the full line for any row strictly between
// the selection's start/end rows.
func selectionCols(row, startRow, startCol, endRow, endCol, width int) (colStart, colEnd int) {
	colStart, colEnd = 0, width
	if row == startRow {
		colStart = startCol
	}
	if row == endRow {
		colEnd = endCol + 1 // inclusive of the cell the cursor is on
	}
	if colStart < 0 {
		colStart = 0
	}
	if colEnd > width {
		colEnd = width
	}
	return colStart, colEnd
}

// copySelectionCmd writes the selected text to the terminal clipboard via
// OSC52 (the same mechanism as feature.go's copyTranscript) and toasts,
// mirroring util/selection.ts's copy().
func (a *App) copySelectionCmd() tea.Cmd {
	text := a.selectedText()
	if strings.TrimSpace(text) == "" {
		return nil
	}
	encoded := base64.StdEncoding.EncodeToString([]byte(text))
	fmt.Fprintf(os.Stdout, "\033]52;c;%s\a", encoded)
	return a.showToast("Copied to clipboard", false)
}
