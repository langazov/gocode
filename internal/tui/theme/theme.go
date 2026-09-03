// Package theme ports the TUI theme surface (packages/tui/src/theme): the
// resolved color keys used across the interface, with dark/light defaults
// generated from the terminal palette like the TypeScript generateSystem.
package theme

import (
	"fmt"
	"image/color"

	"charm.land/lipgloss/v2"
)

// Colors mirrors ThemeResolved's core keys. lipgloss v2's Color is a
// constructor function returning the stdlib color.Color interface (not a
// distinct named type like v1's lipgloss.Color), so these fields are typed
// against the interface directly.
type Colors struct {
	Primary           color.Color
	Secondary         color.Color
	Accent            color.Color
	Info              color.Color
	Error             color.Color
	Warning           color.Color
	Success           color.Color
	Text              color.Color
	TextMuted         color.Color
	Background        color.Color
	BackgroundPanel   color.Color
	BackgroundElement color.Color
	Border            color.Color
	BorderActive      color.Color
	BorderSubtle      color.Color
	// SelectedListItemText is the foreground of text drawn on a Primary
	// fill (dialog buttons, the which-key active tab). theme/index.ts makes
	// it optional and falls back to background, which Normalize reproduces.
	SelectedListItemText color.Color
	// BackgroundMenu is the surface behind menu-style panels. Optional in
	// theme/index.ts with a backgroundElement fallback.
	BackgroundMenu color.Color
}

// Normalize applies theme/index.ts's fallbacks for the two optional keys, so
// a palette that leaves them unset resolves exactly as the original does.
func (c Colors) Normalize() Colors {
	if c.SelectedListItemText == nil {
		c.SelectedListItemText = c.Background
	}
	if c.BackgroundMenu == nil {
		c.BackgroundMenu = c.BackgroundElement
	}
	return c
}

// Theme is a named palette with the resolved keys the UI reads.
type Theme struct {
	Name string
	Dark bool
	Colors
	// ThinkingOpacity mirrors ThemeResolved.thinkingOpacity: the alpha a
	// reasoning header's warning color fades to once its body is showing
	// (theme/index.ts defaults this to 0.6 when a theme doesn't set it;
	// none of the built-in dark/light palettes here do either).
	ThinkingOpacity float64
}

// Dark and Light mirror packages/tui/src/theme/assets/opencode.json — the
// TS TUI's actual default ("opencode") theme, resolved for each mode — so
// this port's default palette matches upstream instead of an unrelated
// hand-picked scheme. Values are copied straight from that file's defs:
// primary/accent/etc. come from its darkStep9/darkAccent/... refs, and the
// background/panel/element/border ramp comes from its darkStep1/2/3/6/7/8.
func Dark() Theme {
	return Theme{
		Name:            "gocode-dark",
		Dark:            true,
		ThinkingOpacity: 0.6,
		Colors: Colors{
			Primary:           lipgloss.Color("#fab283"),
			Secondary:         lipgloss.Color("#5c9cf5"),
			Accent:            lipgloss.Color("#9d7cd8"),
			Info:              lipgloss.Color("#56b6c2"),
			Error:             lipgloss.Color("#e06c75"),
			Warning:           lipgloss.Color("#f5a742"),
			Success:           lipgloss.Color("#7fd88f"),
			Text:              lipgloss.Color("#eeeeee"),
			TextMuted:         lipgloss.Color("#808080"),
			Background:        lipgloss.Color("#0a0a0a"),
			BackgroundPanel:   lipgloss.Color("#141414"),
			BackgroundElement: lipgloss.Color("#1e1e1e"),
			Border:            lipgloss.Color("#484848"),
			BorderActive:      lipgloss.Color("#606060"),
			BorderSubtle:      lipgloss.Color("#3c3c3c"),
		},
	}
}

func Light() Theme {
	return Theme{
		Name:            "gocode-light",
		Dark:            false,
		ThinkingOpacity: 0.6,
		Colors: Colors{
			Primary:           lipgloss.Color("#3b7dd8"),
			Secondary:         lipgloss.Color("#7b5bb6"),
			Accent:            lipgloss.Color("#d68c27"),
			Info:              lipgloss.Color("#318795"),
			Error:             lipgloss.Color("#d1383d"),
			Warning:           lipgloss.Color("#d68c27"),
			Success:           lipgloss.Color("#3d9a57"),
			Text:              lipgloss.Color("#1a1a1a"),
			TextMuted:         lipgloss.Color("#8a8a8a"),
			Background:        lipgloss.Color("#ffffff"),
			BackgroundPanel:   lipgloss.Color("#fafafa"),
			BackgroundElement: lipgloss.Color("#f5f5f5"),
			Border:            lipgloss.Color("#b8b8b8"),
			BorderActive:      lipgloss.Color("#a0a0a0"),
			BorderSubtle:      lipgloss.Color("#d4d4d4"),
		},
	}
}

// Tint linearly interpolates from base toward overlay by alpha (0..1), ported
// from theme/index.ts's tint(base, overlay, alpha). Terminal cells have no
// alpha channel, so this is also how the port renders TS's RGBA-alpha
// fadeColor(color, alpha): FadeColor blends the color into the surface it
// sits on (background at alpha 0, full color at alpha 1) to reproduce the
// same visual fade without real compositing.
func Tint(base, overlay color.Color, alpha float64) color.Color {
	br, bg, bb := channels(base)
	or, og, ob := channels(overlay)
	r := br + (or-br)*alpha
	g := bg + (og-bg)*alpha
	b := bb + (ob-bb)*alpha
	return lipgloss.Color(rgbToHex(round255(r), round255(g), round255(b)))
}

// FadeColor is the terminal-safe equivalent of signal.ts/prompt's
// fadeColor(color, alpha): color blended toward background by alpha.
func FadeColor(background, c color.Color, alpha float64) color.Color {
	return Tint(background, c, alpha)
}

// channels extracts 0-255 RGB components from a color.Color. RGBA() returns
// alpha-premultiplied 16-bit channels; every color in this package comes from
// lipgloss.Color(hex) which is always fully opaque, so >>8 recovers the
// original 8-bit component without needing to divide out alpha.
func channels(c color.Color) (r, g, b float64) {
	ri, gi, bi, _ := c.RGBA()
	return float64(ri >> 8), float64(gi >> 8), float64(bi >> 8)
}

func rgbToHex(r, g, b int) string {
	return fmt.Sprintf("#%02x%02x%02x", clamp255(r), clamp255(g), clamp255(b))
}

// Hex renders a color.Color back to a "#rrggbb" string, for handing theme
// colors to libraries (e.g. glamour's ansi.StyleConfig) that want a literal
// hex string rather than a color.Color.
func Hex(c color.Color) string {
	r, g, b := channels(c)
	return rgbToHex(round255(r), round255(g), round255(b))
}

func round255(v float64) int {
	if v < 0 {
		return 0
	}
	return int(v + 0.5)
}

func clamp255(v int) int {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return v
}

// Resolve picks a theme by name: "gocode-dark"/"gocode-light" (or the bare
// "dark"/"light") are this port's hand-ported default (see Dark/Light);
// anything else is looked up in the bundled catalog (see catalog.go, and
// Names for the full list); an unrecognized name falls back to dark, same
// as an empty one.
func Resolve(name string) Theme {
	switch name {
	case "gocode-light", "light":
		t := Light()
		t.Colors = t.Colors.Normalize()
		return t
	case "gocode-dark", "dark":
		t := Dark()
		t.Colors = t.Colors.Normalize()
		return t
	}
	if t, ok := catalog[name]; ok {
		return t
	}
	t := Dark()
	t.Colors = t.Colors.Normalize()
	return t
}
