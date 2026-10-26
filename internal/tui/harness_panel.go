package tui

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/langazov/gocode-go/internal/tui/dialog"
)

// A harness read-only panel (a stage, a comparison) is mostly diffs and
// prose, and the help panel's one muted, clipped style reads badly for
// both: a diff needs its +/- lines told apart, an editor's notes need to
// wrap. These panels render their own rows instead, from tagged lines.

type panelTone int

const (
	toneBody    panelTone = iota // prose, wrapped, in Text
	toneHeading                  // a section title, bold
	toneMuted                    // metadata, clipped
	toneAdd                      // a diff's added line
	toneDel                      // a diff's removed line
	toneHunk                     // a diff's @@ line
	toneError                    // what went wrong, wrapped, in Error
	toneNote                     // metadata that must be read whole, wrapped, muted
)

type panelLine struct {
	Text string
	Tone panelTone
}

// openHarnessPanel shows tagged lines in a scrolling read-only panel.
func (a *App) openHarnessPanel(title string, lines []panelLine) {
	body := func(p dialog.Palette, w int) []string { return renderPanelLines(p, w, lines) }
	a.mount(dialog.NewNote(dialog.KindStatus, body, a.statsBudget, nil)).WithHelpLines(title, nil)
}

func renderPanelLines(p dialog.Palette, w int, lines []panelLine) []string {
	width := max(8, w-2*dialog.PadX)
	pad := lipgloss.NewStyle().Background(p.BackgroundPanel).Render(strings.Repeat(" ", dialog.PadX))
	style := func(tone panelTone) lipgloss.Style {
		s := lipgloss.NewStyle().Background(p.BackgroundPanel)
		switch tone {
		case toneHeading:
			return s.Foreground(p.Text).Bold(true)
		case toneMuted, toneNote:
			return s.Foreground(p.TextMuted)
		case toneAdd:
			return s.Foreground(p.Success)
		case toneDel, toneError:
			return s.Foreground(p.Error)
		case toneHunk:
			return s.Foreground(p.Primary)
		}
		return s.Foreground(p.Text)
	}
	var out []string
	for _, line := range lines {
		text := panelSafe(line.Text)
		rows := []string{ansi.Truncate(text, width, "…")}
		if (line.Tone == toneBody || line.Tone == toneError || line.Tone == toneNote) && text != "" {
			rows = dialog.WrapWords(text, width)
		}
		for _, row := range rows {
			out = append(out, pad+style(line.Tone).Render(row))
		}
	}
	return out
}

// panelSafe makes evidence text safe to splice into a panel row. A stage's
// diff and deliverable are whatever the rollout wrote — CRLF files, an
// agent's colored test output, a stray bell — and raw, a carriage return or
// an escape sequence moves the terminal's cursor behind the renderer's back:
// the screen stops matching what the renderer believes is on it, and the
// panel's cells outlive closing it. Escape sequences are stripped, other
// control characters dropped, tabs expanded.
func panelSafe(text string) string {
	text = ansi.Strip(text)
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) {
			return -1
		}
		return r
	}, strings.ReplaceAll(text, "\t", "    "))
}

// diffLines tags a unified diff's lines by what they are.
func diffLines(diff string) []panelLine {
	var out []panelLine
	for _, line := range splitPanelLines(strings.TrimRight(diff, "\n")) {
		tone := toneMuted
		switch {
		case strings.HasPrefix(line, "+++"), strings.HasPrefix(line, "---"):
		case strings.HasPrefix(line, "+"):
			tone = toneAdd
		case strings.HasPrefix(line, "-"):
			tone = toneDel
		case strings.HasPrefix(line, "@@"):
			tone = toneHunk
		}
		out = append(out, panelLine{Text: line, Tone: tone})
	}
	return out
}

// proseLines tags text as wrapped body lines, one per source line.
func proseLines(text string, tone panelTone) []panelLine {
	var out []panelLine
	for _, line := range splitPanelLines(strings.TrimRight(text, "\n")) {
		out = append(out, panelLine{Text: line, Tone: tone})
	}
	return out
}

// panelText is the panel's plain text, for tests and copying.
func panelText(lines []panelLine) []string {
	out := make([]string, len(lines))
	for i, line := range lines {
		out[i] = line.Text
	}
	return out
}
