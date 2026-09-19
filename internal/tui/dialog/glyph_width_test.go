package dialog

import (
	"testing"

	"charm.land/lipgloss/v2"
)

// Every glyph the dialog chrome introduces must be single-width, or the
// panel tears on the terminals that disagree about it (§5).
func TestChromeGlyphsAreSingleWidth(t *testing.T) {
	for _, g := range []string{"⌕", "─", "·", "▐", "│", "●", "✓", "↑", "↓"} {
		if w := lipgloss.Width(g); w != 1 {
			t.Errorf("%q measures %d cells, want 1", g, w)
		}
	}
}
