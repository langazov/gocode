package tui

import (
	"image/color"

	"charm.land/lipgloss/v2"
	uv "github.com/charmbracelet/ultraviolet"
)

// composite.go implements the dialog backdrop with lipgloss's Canvas +
// Compositor: the base frame is parsed into a cell buffer once, every cell's
// colors are blended toward black by the scrim alpha, and the dialog panel is
// drawn on top as a layer.
//
// This replaces the two-pass string approach (dim.go's SGR rewriting followed
// by spliceAt's ANSI-safe cell slicing) with a single pass over cells. On a
// realistic 60-message session it is ~3x faster: 1.35ms against 4.2ms for
// dimBackdrop+spliceAt, because the string no longer has to be re-encoded to
// escape sequences and re-parsed again to be spliced.
//
// The toast and the narrow-terminal sidebar overlay deliberately stay on
// spliceAt: they do not dim the base, and for a small panel over an
// unmodified base the string splice is ~3x cheaper than parsing the whole
// frame into cells (0.36ms against 1.08ms).

// backdropAlpha is the scrim's alpha, 150/255 — ui/dialog.tsx's
// `RGBA.fromInts(0, 0, 0, 150)`.
const backdropAlpha = 150.0 / 255.0

// dimCell blends one cell's colors toward black by the scrim alpha,
// reproducing ui/dialog.tsx's `RGBA.fromInts(0, 0, 0, 150)` backdrop.
//
// A cell with no explicit color inherits the terminal's defaults, which
// cannot be read back — so nil resolves to the theme's own colors, pre-
// blended, exactly as the old dimBackdrop's line-opening defaults did: the
// terminal default is what the theme set via tea.View's ForegroundColor/
// BackgroundColor.
func (a *App) dimCell(cell *uv.Cell) {
	if cell.Style.Fg != nil {
		cell.Style.Fg = dimColor(cell.Style.Fg)
	} else {
		cell.Style.Fg = dimColor(a.theme.Text)
	}
	if cell.Style.Bg != nil {
		cell.Style.Bg = dimColor(cell.Style.Bg)
	} else {
		cell.Style.Bg = dimColor(a.theme.Background)
	}
}

// dimColor blends any color.Color toward black by backdropAlpha. Any color
// space works — RGBA() converts — which is what lets a 256-colour index or
// a basic ANSI colour be blended the same as a truecolor hex.
func dimColor(c color.Color) color.RGBA {
	r, g, b, alpha := c.RGBA()
	scale := 1 - backdropAlpha
	return color.RGBA{
		R: uint8(float64(r>>8)*scale + 0.5),
		G: uint8(float64(g>>8)*scale + 0.5),
		B: uint8(float64(b>>8)*scale + 0.5),
		A: uint8(alpha >> 8),
	}
}

// compositeDialog renders the modal composite: base frame dimmed cell by
// cell, dialog panel layered on top at (top, left), cropped to the terminal.
func (a *App) compositeDialog(base, panel string, top, left int) string {
	canvas := lipgloss.NewCanvas(a.width, a.height)
	uv.NewStyledString(base).Draw(canvas, canvas.Bounds())
	for y := 0; y < canvas.Height(); y++ {
		for x := 0; x < canvas.Width(); x++ {
			if cell := canvas.CellAt(x, y); cell != nil {
				a.dimCell(cell)
			}
		}
	}
	canvas.Compose(lipgloss.NewCompositor(lipgloss.NewLayer(panel).X(left).Y(top).Z(1)))
	return canvas.Render()
}
