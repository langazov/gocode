package theme

import (
	"testing"

	"charm.land/lipgloss/v2"
)

// mirrors theme/index.ts's tint(base, overlay, alpha): base at alpha 0,
// overlay at alpha 1, linear in between.
func TestTint(t *testing.T) {
	base := lipgloss.Color("#000000")
	overlay := lipgloss.Color("#ffffff")

	if got := Tint(base, overlay, 0); got != base {
		t.Fatalf("Tint(base, overlay, 0) = %v, want base %v", got, base)
	}
	if got := Tint(base, overlay, 1); got != overlay {
		t.Fatalf("Tint(base, overlay, 1) = %v, want overlay %v", got, overlay)
	}
	if got := Tint(base, overlay, 0.5); got != lipgloss.Color("#808080") {
		t.Fatalf("Tint(base, overlay, 0.5) = %v, want #808080", got)
	}
}

// FadeColor(background, color, alpha) is the terminal-safe stand-in for
// signal.ts prompt's fadeColor(color, alpha): invisible (== background) at
// alpha 0, the full color at alpha 1.
func TestFadeColor(t *testing.T) {
	bg := lipgloss.Color("#123456")
	color := lipgloss.Color("#abcdef")

	if got := FadeColor(bg, color, 0); got != bg {
		t.Fatalf("FadeColor(.., 0) = %v, want background %v", got, bg)
	}
	if got := FadeColor(bg, color, 1); got != color {
		t.Fatalf("FadeColor(.., 1) = %v, want full color %v", got, color)
	}
}
