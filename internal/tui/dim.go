package tui

// The dialog backdrop.
//
// ui/dialog.tsx draws its scrim as a full-screen box at
// `RGBA.fromInts(0, 0, 0, 150)` — black at 59% — which opentui composites over
// whatever is behind it. lipgloss has no compositing step, and this port had
// recorded the dim as impossible and left the content behind a dialog at full
// brightness. It is not impossible: the frame being covered is a string of
// SGR-colored cells, so the same blend can be applied by rewriting those
// colors before the panel is spliced in.
//
// What that buys is the actual look of a modal — the dialog reads as lifted off
// a receded page instead of sitting in the middle of an equally bright one.
//
// Two cases have to be handled. Cells with an explicit color carry it in the
// escape sequence and are blended directly. Cells with no color at all inherit
// the terminal's defaults, which cannot be read back — so each line is opened
// with the theme's own background and text colors, pre-blended, which is what
// those defaults are meant to be.

import (
	"fmt"
	"image/color"
	"strconv"
	"strings"
)

// backdropAlpha is the scrim's alpha, 150/255.
const backdropAlpha = 150.0 / 255.0

// dimBackdrop blends every color in content toward black by backdropAlpha.
func (a *App) dimBackdrop(content string) string {
	fg := dimChannels(a.theme.Text)
	bg := dimChannels(a.theme.Background)
	defaults := fmt.Sprintf("\x1b[38;2;%d;%d;%d;48;2;%d;%d;%dm",
		fg[0], fg[1], fg[2], bg[0], bg[1], bg[2])

	lines := strings.Split(content, "\n")
	for i, line := range lines {
		lines[i] = defaults + dimSGR(line, defaults) + "\x1b[m"
	}
	return strings.Join(lines, "\n")
}

// dimSGR rewrites every color in one line's escape sequences. A reset inside
// the line returns to the dimmed defaults rather than the terminal's, so a
// styled span does not punch a bright hole in the scrim when it ends.
func dimSGR(line, defaults string) string {
	var out strings.Builder
	out.Grow(len(line))

	for {
		start := strings.Index(line, "\x1b[")
		if start < 0 {
			out.WriteString(line)
			return out.String()
		}
		end := strings.IndexByte(line[start:], 'm')
		if end < 0 {
			// Not an SGR sequence (or truncated): leave the rest alone.
			out.WriteString(line)
			return out.String()
		}
		end += start
		out.WriteString(line[:start])
		out.WriteString(dimSGRParams(line[start+2:end], defaults))
		line = line[end+1:]
	}
}

// dimSGRParams rewrites the parameter list of one SGR sequence.
func dimSGRParams(params, defaults string) string {
	if params == "" || params == "0" {
		// A bare reset: fall back to the dimmed defaults, not the terminal's.
		return "\x1b[m" + defaults
	}
	fields := strings.Split(params, ";")
	var out []string
	for i := 0; i < len(fields); i++ {
		field := fields[i]
		switch field {
		case "38", "48":
			rgb, next, ok := readColor(fields, i)
			if !ok {
				out = append(out, field)
				continue
			}
			dimmed := dimRGB(rgb)
			out = append(out, field, "2",
				strconv.Itoa(dimmed[0]), strconv.Itoa(dimmed[1]), strconv.Itoa(dimmed[2]))
			i = next
		case "0":
			// An embedded reset: re-apply the dimmed defaults after it.
			out = append(out, "0")
			return "\x1b[" + strings.Join(out, ";") + "m" + defaults +
				dimSGRParams(strings.Join(fields[i+1:], ";"), defaults)
		case "39":
			out = append(out, "39")
		case "49":
			out = append(out, "49")
		default:
			out = append(out, field)
		}
	}
	return "\x1b[" + strings.Join(out, ";") + "m"
}

// readColor parses the extended-color arguments following a 38/48 at index i,
// returning the RGB it denotes and the index of its last consumed field.
func readColor(fields []string, i int) (rgb [3]int, last int, ok bool) {
	if i+1 >= len(fields) {
		return rgb, i, false
	}
	switch fields[i+1] {
	case "2":
		if i+4 >= len(fields) {
			return rgb, i, false
		}
		for n := range 3 {
			value, err := strconv.Atoi(fields[i+2+n])
			if err != nil {
				return rgb, i, false
			}
			rgb[n] = value
		}
		return rgb, i + 4, true
	case "5":
		if i+2 >= len(fields) {
			return rgb, i, false
		}
		index, err := strconv.Atoi(fields[i+2])
		if err != nil {
			return rgb, i, false
		}
		return xterm256(index), i + 2, true
	}
	return rgb, i, false
}

// xterm256 maps a 256-color index to RGB: the 16 system colors, the 6x6x6
// cube, then the 24-step grayscale ramp.
func xterm256(index int) [3]int {
	switch {
	case index < 0 || index > 255:
		return [3]int{0, 0, 0}
	case index < 16:
		return systemColors[index]
	case index < 232:
		n := index - 16
		level := func(v int) int {
			if v == 0 {
				return 0
			}
			return 55 + v*40
		}
		return [3]int{level(n / 36), level((n / 6) % 6), level(n % 6)}
	default:
		v := 8 + (index-232)*10
		return [3]int{v, v, v}
	}
}

var systemColors = [16][3]int{
	{0, 0, 0}, {128, 0, 0}, {0, 128, 0}, {128, 128, 0},
	{0, 0, 128}, {128, 0, 128}, {0, 128, 128}, {192, 192, 192},
	{128, 128, 128}, {255, 0, 0}, {0, 255, 0}, {255, 255, 0},
	{0, 0, 255}, {255, 0, 255}, {0, 255, 255}, {255, 255, 255},
}

// dimRGB blends one color toward black by backdropAlpha. Black is the overlay,
// so the blend collapses to a scale of the source channels.
func dimRGB(rgb [3]int) [3]int {
	scale := 1 - backdropAlpha
	for i, v := range rgb {
		rgb[i] = int(float64(v)*scale + 0.5)
	}
	return rgb
}

// dimChannels is dimRGB for a color.Color.
func dimChannels(c color.Color) [3]int {
	r, g, b, _ := c.RGBA()
	return dimRGB([3]int{int(r >> 8), int(g >> 8), int(b >> 8)})
}
