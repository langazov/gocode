package dialog

import (
	"image/color"
	"strings"

	"charm.land/lipgloss/v2"
)

// This file owns the chrome every dialog kind shares. Before it existed each
// kind carried its own insets, its own footer idiom and its own button
// padding — a list header started at column 4 and an alert header at column
// 2, a list said "close esc" while the stats panel said "esc close", an
// alert padded its button by 3 and a confirm padded its pair by 1. The
// differences were not expressive of anything, so they read as sloppiness.
//
// Everything visible now composes from the constants and helpers here, and
// the layout of every kind is the same three-part frame
// (TUI_RECOMENDATIONS §9.1):
//
//	header  — bold title, right-aligned esc hint
//	rule    — a faint ─ across the content column
//	body    — the kind's own content
//	rule
//	footer  — the action bar, or the hint row naming the live keys
//
// so a dialog is recognizable as one before its content is read.
const (
	// PadX is the content inset shared by every dialog kind: the header
	// title, the rules, category headers, body text and the footer all
	// start at this column and end PadX short of the panel's right edge.
	PadX = 3

	// rowPad is the list scrollbox's own inset. A row box spans
	// [rowPad, w-rowPad), so the selection fill stops short of the panel
	// edge and the scrollbar occupies the column just outside it.
	rowPad = 2

	// gutterCol is where a row's ● / ✓ glyph lands, and rowTextCol where
	// its title starts: the glyph sits in the PadX lane the header and the
	// category labels share, and the title clears it by one cell.
	gutterCol  = PadX
	rowTextCol = PadX + 2
)

// rule is the faint horizontal line under a dialog's header and above its
// footer. It is drawn in a color mixed toward the panel background rather
// than in BackgroundElement: on a light theme those two tokens are one
// shade apart (#fafafa panel, #f5f5f5 element) and the line would vanish.
func rule(p Palette, w int) string {
	span := w - 2*PadX
	if span < 1 {
		return ""
	}
	return pad(p, PadX) +
		lipgloss.NewStyle().
			Foreground(mix(p.TextMuted, p.BackgroundPanel, 0.68)).
			Background(p.BackgroundPanel).
			Render(strings.Repeat("─", span))
}

// pad renders n cells of panel background. Bare spaces would reset the tint
// mid-row wherever a styled segment precedes them.
func pad(p Palette, n int) string {
	if n <= 0 {
		return ""
	}
	return lipgloss.NewStyle().Background(p.BackgroundPanel).Render(strings.Repeat(" ", n))
}

// mix blends fg toward bg by t (0 = fg, 1 = bg). The dialog renderer has no
// alpha channel to work with (§3.2), so every "faint" color is a blend
// computed against the panel it is painted on.
func mix(fg, bg color.Color, t float64) color.Color {
	if fg == nil {
		return bg
	}
	if bg == nil {
		return fg
	}
	fr, fgc, fb, _ := fg.RGBA()
	br, bgc, bb, _ := bg.RGBA()
	blend := func(a, b uint32) uint8 {
		v := float64(a>>8)*(1-t) + float64(b>>8)*t
		return uint8(v + 0.5)
	}
	return color.RGBA{R: blend(fr, br), G: blend(fgc, bgc), B: blend(fb, bb), A: 0xff}
}

// keyHint renders one footer affordance in the dialog's single hint idiom:
// what it does in Text, the key that does it in muted text. Every dialog
// kind reads the same way round — "close esc", never "esc close".
func keyHint(p Palette, label, keys string) string {
	out := onPanel(p, p.Text, false).Render(label)
	if keys != "" {
		out += onPanel(p, p.TextMuted, false).Render(" " + keys)
	}
	return out
}

// hintWidth is keyHint's rendered width, needed before the row is composed.
func hintWidth(label, keys string) int {
	w := lipgloss.Width(label)
	if keys != "" {
		w += 1 + lipgloss.Width(keys)
	}
	return w
}

// hintRow lays out a footer hint row: left-aligned affordances, then the
// right-aligned group (always including the way out), inside the shared
// content column.
func hintRow(p Palette, w int, left, right []string) string {
	span := w - 2*PadX
	body := splitRowOn(p, span, joinHints(p, left), joinHints(p, right), 1)
	return pad(p, PadX) + body
}

func joinHints(p Palette, parts []string) string {
	return strings.Join(parts, onPanel(p, p.TextMuted, false).Render("  "))
}

// scrollbar reports the column of glyphs that sits outside the list's row
// boxes: a thumb sized to the fraction of rows on screen, over a track
// mixed toward the panel. rows is the window height, total the row count
// and top the first visible row.
func scrollbar(p Palette, rows, total, top int) []string {
	out := make([]string, rows)
	thumbStyle := lipgloss.NewStyle().Foreground(p.TextMuted).Background(p.BackgroundPanel)
	trackStyle := lipgloss.NewStyle().
		Foreground(mix(p.TextMuted, p.BackgroundPanel, 0.74)).
		Background(p.BackgroundPanel)
	if total <= rows {
		for i := range out {
			out[i] = pad(p, 1)
		}
		return out
	}
	size := max(1, rows*rows/total)
	span := rows - size
	start := 0
	if span > 0 {
		start = (top*span + (total-rows)/2) / (total - rows)
		start = min(max(start, 0), span)
	}
	// The thumb and the track differ in shape as well as in color, so the
	// bar still reads as a position on a theme whose muted text sits close
	// to its panel background.
	for i := range out {
		if i >= start && i < start+size {
			out[i] = thumbStyle.Render("▐")
		} else {
			out[i] = trackStyle.Render("│")
		}
	}
	return out
}

// buttonPad is the horizontal padding inside every dialog button. One
// value, so an alert's single button and a confirm's pair are the same
// shape.
const buttonPad = 2
