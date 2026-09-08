package tui

import (
	"image/color"
	"strings"

	"charm.land/lipgloss/v2"
)

// components.go provides reusable lipgloss style builders for the common
// panel, row, button, and hint patterns that appear across the TUI's view
// functions. These are the building blocks the views compose from, reducing
// the scattered inline lipgloss.NewStyle() calls and making the layout intent
// declarative.

// splitBorderPanel returns the style used by every single-left-border panel
// in the timeline: a ┃ left border in the given foreground, backgroundPanel
// fill, uniform padding, and a border-box width.
//
// width is the intended total rendered width (border + padding + content),
// matching lipgloss v2's true border-box semantics. Callers should pass
// withLeftBorder(contentAndPaddingWidth) to add the border column.
func (a *App) splitBorderPanel(width int, fg color.Color) lipgloss.Style {
	return lipgloss.NewStyle().
		Border(splitBorder(), false, false, false, true).
		BorderForeground(fg).
		Background(a.theme.BackgroundPanel).
		PaddingTop(1).
		PaddingBottom(1).
		PaddingLeft(2).
		Width(width)
}

// splitBorderPanelCustom is splitBorderPanel with custom horizontal padding
// (used by permissionBanner and questionBanner which use PaddingLeft(1) and
// PaddingRight(3)).
func (a *App) splitBorderPanelCustom(width int, fg color.Color, padLeft, padRight int) lipgloss.Style {
	return lipgloss.NewStyle().
		Border(splitBorder(), false, false, false, true).
		BorderForeground(fg).
		Background(a.theme.BackgroundPanel).
		PaddingTop(1).
		PaddingBottom(1).
		PaddingLeft(padLeft).
		PaddingRight(padRight).
		Width(width)
}

// errPanel is splitBorderPanel colored for an error block.
func (a *App) errPanel(width int) lipgloss.Style {
	return a.splitBorderPanel(width, a.theme.Error)
}

// userPanel is splitBorderPanel colored for a user message block.
func (a *App) userPanel(width int) lipgloss.Style {
	return a.splitBorderPanel(width, a.theme.Primary)
}

// toolPanel is splitBorderPanel with the background-matching border used by
// blockToolStyle — the border is invisible (only there for the corner shape).
func (a *App) toolPanel(width int) lipgloss.Style {
	return a.splitBorderPanel(width, a.theme.Background)
}

// permissionButton renders a permission/question banner option button:
// selected fills with warning (permission) or primary (question) and uses
// the theme's selected-item foreground; unselected sits on backgroundElement.
func (a *App) permissionButton(label string, selected bool, activeBg color.Color) string {
	fg := a.theme.TextMuted
	bg := a.theme.BackgroundElement
	if selected {
		fg = a.theme.SelectedListItemText
		bg = activeBg
	}
	return lipgloss.NewStyle().Foreground(fg).Background(bg).Render(" " + label + " ")
}

// hintPair renders one "key label" hint pair: the key in text color, the
// label in muted, separated by a space. This is the most repeated pattern in
// the codebase — status bars, footers, dialog headers, and banners all use
// it.
func (a *App) hintPair(key, label string) string {
	return a.styles().Text.Render(key) + " " + a.styles().Muted.Render(label)
}

// splitRow lays out a justifyContent="space-between" row: left content
// left-aligned, right content right-aligned, bare spaces between.
//
// lipgloss has no space-between primitive (JoinHorizontal concatenates,
// PlaceHorizontal aligns the whole block), so this pattern is named once
// here instead of being re-derived at every call site.
//
// minWidthGap is the smallest gap allowed; when the two sides do not fit,
// the gap collapses to it rather than going negative. Callers that need to
// truncate or stack instead handle that themselves before calling.
func splitRow(width int, left, right string, minWidthGap int) string {
	gap := width - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < minWidthGap {
		gap = minWidthGap
	}
	return left + strings.Repeat(" ", gap) + right
}

// onPanelText renders text on the panel background, line by line — safe to
// embed inside a backgroundPanel fill (see renderLines' doc comment in
// render.go for why a single multi-line Render is not).
func (a *App) onPanelText(text string) string {
	return renderLines(a.onPanel(a.theme.Text, false), text)
}

// onPanelMuted renders muted text on the panel background, line by line.
func (a *App) onPanelMuted(text string) string {
	return renderLines(a.onPanel(a.theme.TextMuted, false), text)
}

// withLeftBorder converts a "content+padding width, with the single left
// border column rendered outside it" total into the total border-box width
// lipgloss v2's Style.Width expects. v2's Width() is true border-box (the
// declared value IS the total rendered size, border included), so reaching
// the on-screen total every single-left-border panel was tuned against needs
// the border column added back into the argument. One left border column,
// hence +1.
func withLeftBorder(contentAndPadding int) int {
	return contentAndPadding + 1
}
