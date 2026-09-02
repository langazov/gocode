package tui

// Spinners, ported from packages/tui/src/ui/spinner.ts and
// packages/tui/src/component/spinner.tsx.
//
// The original runs two different spinners at two different rates, and this
// port had collapsed them into one:
//
//   - `component/spinner.tsx`'s `<Spinner>` — the braille frames at 80ms,
//     used inline beside a running tool row.
//   - `ui/spinner.ts`'s Knight Rider scanner — an 8-cell `■`/`⬝` bar that
//     sweeps back and forth with a colored trail, at 40ms. This is the one in
//     the prompt's hint row (`spinnerDef()` in component/prompt/index.tsx
//     builds it with `style: "blocks"`, `inactiveFactor: 0.6`,
//     `minAlpha: 0.3`), i.e. the footer spinner.
//
// Both are driven by the same tick loop at the finer of the two rates, with
// the braille frames advancing every other tick (see spinnerGlyph).
//
// opentui's RGBA has a real alpha channel and composites the trail against
// whatever is behind it. Terminal cells do not, so every alpha here is applied
// with theme.Tint against the surface the spinner sits on — the same
// substitution theme.FadeColor documents for the rest of the port.

import (
	"image/color"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/anomalyco/opencode-go/internal/tui/theme"
)

// createFrames' defaults (width 8, trailSteps 6, holdStart 30, holdEnd 9) plus
// the two options the prompt passes explicitly.
const (
	spinnerWidth          = 8
	spinnerTrailSteps     = 6
	spinnerHoldStart      = 30
	spinnerHoldEnd        = 9
	spinnerInactiveFactor = 0.6
	spinnerMinAlpha       = 0.3
)

// spinnerCycle is createFrames' totalFrames: forward across the bar, hold at
// the end, back again, hold at the start.
const spinnerCycle = spinnerWidth + spinnerHoldEnd + (spinnerWidth - 1) + spinnerHoldStart

// The "blocks" style's two glyphs.
const (
	spinnerActiveGlyph   = "■"
	spinnerInactiveGlyph = "⬝"
)

// scannerState ports getScannerState's bidirectional branch — the only
// direction createFrames uses.
type scannerState struct {
	activePosition   int
	isHolding        bool
	holdProgress     int
	holdTotal        int
	movementProgress int
	movementTotal    int
	isMovingForward  bool
}

func spinnerScannerState(frameIndex, totalChars int) scannerState {
	forward := totalChars
	backward := totalChars - 1

	switch {
	case frameIndex < forward:
		return scannerState{
			activePosition:   frameIndex,
			movementProgress: frameIndex,
			movementTotal:    forward,
			isMovingForward:  true,
		}
	case frameIndex < forward+spinnerHoldEnd:
		return scannerState{
			activePosition:  totalChars - 1,
			isHolding:       true,
			holdProgress:    frameIndex - forward,
			holdTotal:       spinnerHoldEnd,
			isMovingForward: true,
		}
	case frameIndex < forward+spinnerHoldEnd+backward:
		index := frameIndex - forward - spinnerHoldEnd
		return scannerState{
			activePosition:   totalChars - 2 - index,
			movementProgress: index,
			movementTotal:    backward,
		}
	default:
		return scannerState{
			isHolding:    true,
			holdProgress: frameIndex - forward - spinnerHoldEnd - backward,
			holdTotal:    spinnerHoldStart,
		}
	}
}

// spinnerColorIndex ports calculateColorIndex: the trail runs *behind* the
// head, so the distance is measured against the direction of travel, and
// during a hold it keeps sliding so the whole bar decays instead of freezing.
// -1 means the cell is inactive.
func spinnerColorIndex(charIndex, trailLength int, state scannerState) int {
	distance := charIndex - state.activePosition
	if state.isMovingForward {
		distance = state.activePosition - charIndex
	}

	if state.isHolding {
		return distance + state.holdProgress
	}
	if distance > 0 && distance < trailLength {
		return distance
	}
	if distance == 0 {
		return 0
	}
	return -1
}

// spinnerFade ports createKnightRiderTrail's fadeFactor: inactive cells fade
// out across a hold and back in across a sweep, bottoming out at minAlpha.
func spinnerFade(state scannerState) float64 {
	switch {
	case state.isHolding && state.holdTotal > 0:
		progress := min(float64(state.holdProgress)/float64(state.holdTotal), 1)
		return max(spinnerMinAlpha, 1-progress*(1-spinnerMinAlpha))
	case !state.isHolding && state.movementTotal > 0:
		progress := min(float64(state.movementProgress)/float64(max(1, state.movementTotal-1)), 1)
		return spinnerMinAlpha + progress*(1-spinnerMinAlpha)
	default:
		return 1
	}
}

// spinnerTrailStep is one entry of deriveTrailColors: the color after its
// brightness bump, and the opacity that goes with it.
type spinnerTrailStep struct {
	color color.Color
	alpha float64
}

// deriveSpinnerTrail ports deriveTrailColors: the head at full strength, a
// slightly brighter but less opaque bloom behind it, then an exponential
// alpha decay down the tail.
func deriveSpinnerTrail(base color.Color, steps int) []spinnerTrailStep {
	r, g, b := colorChannels(base)
	out := make([]spinnerTrailStep, 0, steps)
	for i := range steps {
		alpha, brightness := 1.0, 1.0
		switch {
		case i == 0:
		case i == 1:
			alpha, brightness = 0.9, 1.15
		default:
			alpha = pow(0.65, i-1)
		}
		out = append(out, spinnerTrailStep{
			color: lipgloss.Color(rgbHex(
				min(255, r*brightness), min(255, g*brightness), min(255, b*brightness))),
			alpha: alpha,
		})
	}
	return out
}

// scannerSpinner renders the current frame of the Knight Rider bar against
// surface. Every cell is emitted with its own foreground, which is what makes
// the trail read as a gradient rather than a solid block.
func (a *App) scannerSpinner(base, surface color.Color) string {
	if !a.animationsEnabled {
		// component/spinner.tsx's `fallback={<text>⋯ …}` when animations are
		// off; the scanner has no static form of its own.
		return "⋯"
	}
	state := spinnerScannerState(a.spinnerFrame%spinnerCycle, spinnerWidth)
	trail := deriveSpinnerTrail(base, spinnerTrailSteps)
	fade := spinnerFade(state)
	inactive := theme.Tint(surface, base, spinnerInactiveFactor*fade)

	var out strings.Builder
	for charIndex := range spinnerWidth {
		glyph, fg := spinnerInactiveGlyph, inactive
		if index := spinnerColorIndex(charIndex, len(trail), state); index >= 0 && index < len(trail) {
			glyph = spinnerActiveGlyph
			fg = theme.Tint(surface, trail[index].color, trail[index].alpha)
		}
		out.WriteString(lipgloss.NewStyle().Foreground(fg).Background(surface).Render(glyph))
	}
	return out.String()
}

// colorChannels extracts 0-255 components, mirroring theme.channels (which is
// unexported there).
func colorChannels(c color.Color) (r, g, b float64) {
	ri, gi, bi, _ := c.RGBA()
	return float64(ri >> 8), float64(gi >> 8), float64(bi >> 8)
}

func rgbHex(r, g, b float64) string {
	clamp := func(v float64) int {
		i := int(v + 0.5)
		return min(255, max(0, i))
	}
	const hex = "0123456789abcdef"
	out := []byte("#000000")
	for i, v := range []int{clamp(r), clamp(g), clamp(b)} {
		out[1+i*2] = hex[v>>4]
		out[2+i*2] = hex[v&0xf]
	}
	return string(out)
}

// pow is math.Pow for a small non-negative integer exponent, kept local so the
// trail derivation reads as the loop it is upstream.
func pow(base float64, exp int) float64 {
	out := 1.0
	for range exp {
		out *= base
	}
	return out
}
