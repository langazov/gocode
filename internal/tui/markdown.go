package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// This file is a lightweight Markdown-to-ANSI renderer standing in for TS's
// opentui <markdown> component (used for assistant TextPart, index.tsx
// ~1687-1706), which renders through a full CommonMark parser with real
// syntax highlighting. This hand-rolled version isn't byte-for-byte
// identical — no nested emphasis, no syntax highlighting inside fences, no
// tables — but converts the constructs LLM responses use most (headers,
// fenced code, bold/italic/inline-code, lists, blockquotes, rules) instead
// of showing them as literal "**"/"#"/backtick characters.
//
// Word-wrap decisions are made on the raw markdown source width (markers
// included) rather than the true rendered width, so wrapped lines can land
// a few columns short of the target width when a span's markers are wider
// than its rendered form (e.g. "**bold**" counts 8 columns for 4 rendered
// ones) — safe (never overflows), just not perfectly filled.

// renderMarkdown converts text to ANSI-styled, word-wrapped lines within
// width.
func (a *App) renderMarkdown(text string, width int) string {
	if width < 10 {
		width = 10
	}
	var out []string
	var paragraph []string
	flushParagraph := func() {
		if len(paragraph) == 0 {
			return
		}
		joined := strings.Join(paragraph, " ")
		for _, l := range strings.Split(wrapText(joined, width), "\n") {
			out = append(out, a.renderMarkdownInline(l))
		}
		paragraph = nil
	}

	inFence := false
	fenceLang := ""
	var fenceLines []string
	flushFence := func() {
		if fenceLang != "" {
			out = append(out, a.styles().Muted.Render(fenceLang))
		}
		style := lipgloss.NewStyle().Foreground(a.theme.Text).Background(a.theme.BackgroundElement)
		for _, l := range fenceLines {
			// Pad every line to the same width so the highlighted background
			// is one stable rectangle — clipping alone left each line's
			// highlight only as wide as its own text, so the block's right
			// edge visibly danced from line to line (and while streaming,
			// over time) instead of forming a solid box.
			out = append(out, style.Render(padToWidth(l, width)))
		}
		fenceLines = nil
		fenceLang = ""
	}

	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if lang, ok := parseFence(trimmed); ok {
			if inFence {
				flushFence()
				inFence = false
			} else {
				flushParagraph()
				inFence = true
				fenceLang = lang
			}
			continue
		}
		if inFence {
			fenceLines = append(fenceLines, line)
			continue
		}
		switch {
		case trimmed == "":
			flushParagraph()
		case isHorizontalRule(trimmed):
			flushParagraph()
			out = append(out, a.styles().Muted.Render(strings.Repeat("─", width)))
		case isHeaderLine(trimmed):
			flushParagraph()
			_, content := parseHeader(trimmed)
			out = append(out, lipgloss.NewStyle().Bold(true).Foreground(a.theme.Primary).Render(content))
		case isBlockquoteLine(trimmed):
			flushParagraph()
			content := strings.TrimPrefix(strings.TrimPrefix(trimmed, ">"), " ")
			bar := a.styles().Muted.Render("│ ")
			for _, l := range strings.Split(wrapText(content, width-2), "\n") {
				out = append(out, bar+a.renderMarkdownInline(l))
			}
		case isListLine(trimmed):
			flushParagraph()
			marker, content := parseListItem(trimmed)
			bulletWidth := lipgloss.Width(marker) + 1
			lines := strings.Split(wrapText(content, width-bulletWidth), "\n")
			for i, l := range lines {
				prefix := strings.Repeat(" ", bulletWidth)
				if i == 0 {
					prefix = a.styles().Text.Render(marker) + " "
				}
				out = append(out, prefix+a.renderMarkdownInline(l))
			}
		default:
			paragraph = append(paragraph, trimmed)
		}
	}
	if inFence {
		flushFence()
	}
	flushParagraph()
	return strings.Join(out, "\n")
}

// renderMarkdownInline converts inline spans (code/bold/italic) to styled
// text, rendering plain runs through the same theme.Text color the old
// plain-wrapped rendering used — each styled span self-resets (lipgloss
// always closes with a full reset), so plain runs need their own explicit
// color rather than relying on an outer wrapper, matching the "each styled
// segment resets the enclosing span" pattern already noted in dialogs.go.
func (a *App) renderMarkdownInline(s string) string {
	var out, plain strings.Builder
	flush := func() {
		if plain.Len() == 0 {
			return
		}
		out.WriteString(a.styles().Text.Render(plain.String()))
		plain.Reset()
	}
	runes := []rune(s)
	i := 0
	for i < len(runes) {
		switch {
		case runes[i] == '`':
			if end := indexRune(runes, i+1, '`'); end != -1 {
				flush()
				out.WriteString(lipgloss.NewStyle().Foreground(a.theme.Text).Background(a.theme.BackgroundElement).
					Render(string(runes[i+1 : end])))
				i = end + 1
				continue
			}
		case hasPrefixAt(runes, i, "**"):
			if end := indexSeq(runes, i+2, "**"); end > i+2 {
				flush()
				out.WriteString(lipgloss.NewStyle().Bold(true).Foreground(a.theme.Text).Render(string(runes[i+2 : end])))
				i = end + 2
				continue
			}
		case hasPrefixAt(runes, i, "__"):
			if end := indexSeq(runes, i+2, "__"); end > i+2 {
				flush()
				out.WriteString(lipgloss.NewStyle().Bold(true).Foreground(a.theme.Text).Render(string(runes[i+2 : end])))
				i = end + 2
				continue
			}
		case runes[i] == '*':
			if end := indexRune(runes, i+1, '*'); end > i+1 {
				flush()
				out.WriteString(lipgloss.NewStyle().Italic(true).Foreground(a.theme.Text).Render(string(runes[i+1 : end])))
				i = end + 1
				continue
			}
		case runes[i] == '_':
			if end := indexRune(runes, i+1, '_'); end > i+1 {
				flush()
				out.WriteString(lipgloss.NewStyle().Italic(true).Foreground(a.theme.Text).Render(string(runes[i+1 : end])))
				i = end + 1
				continue
			}
		}
		plain.WriteRune(runes[i])
		i++
	}
	flush()
	return out.String()
}

func parseFence(trimmed string) (lang string, ok bool) {
	if !strings.HasPrefix(trimmed, "```") {
		return "", false
	}
	return strings.TrimSpace(strings.TrimPrefix(trimmed, "```")), true
}

func isHorizontalRule(trimmed string) bool {
	if len(trimmed) < 3 {
		return false
	}
	c := trimmed[0]
	if c != '-' && c != '*' && c != '_' {
		return false
	}
	for i := 0; i < len(trimmed); i++ {
		if trimmed[i] != c {
			return false
		}
	}
	return true
}

func isHeaderLine(trimmed string) bool {
	i := 0
	for i < len(trimmed) && trimmed[i] == '#' {
		i++
	}
	if i == 0 || i > 6 {
		return false
	}
	return i == len(trimmed) || trimmed[i] == ' '
}

func parseHeader(trimmed string) (level int, content string) {
	i := 0
	for i < len(trimmed) && trimmed[i] == '#' {
		i++
	}
	return i, strings.TrimSpace(trimmed[i:])
}

func isBlockquoteLine(trimmed string) bool {
	return trimmed == ">" || strings.HasPrefix(trimmed, "> ")
}

func isListLine(trimmed string) bool {
	if strings.HasPrefix(trimmed, "- ") || strings.HasPrefix(trimmed, "* ") || strings.HasPrefix(trimmed, "+ ") {
		return true
	}
	i := 0
	for i < len(trimmed) && trimmed[i] >= '0' && trimmed[i] <= '9' {
		i++
	}
	return i > 0 && strings.HasPrefix(trimmed[i:], ". ")
}

// parseListItem returns the rendered bullet ("•" for -/*/+, "N." for
// ordered) and the item's remaining text.
func parseListItem(trimmed string) (marker, content string) {
	if strings.HasPrefix(trimmed, "- ") || strings.HasPrefix(trimmed, "* ") || strings.HasPrefix(trimmed, "+ ") {
		return "•", trimmed[2:]
	}
	i := 0
	for i < len(trimmed) && trimmed[i] >= '0' && trimmed[i] <= '9' {
		i++
	}
	return trimmed[:i+1], trimmed[i+2:]
}

func clipToWidth(line string, width int) string {
	if lipgloss.Width(line) <= width {
		return line
	}
	return truncateRunes(line, width)
}

// padToWidth clips like clipToWidth but also right-pads shorter lines with
// spaces, so a background style applied to the result always paints the
// full width instead of only as far as the line's own text.
func padToWidth(line string, width int) string {
	line = clipToWidth(line, width)
	if gap := width - lipgloss.Width(line); gap > 0 {
		line += strings.Repeat(" ", gap)
	}
	return line
}

func hasPrefixAt(runes []rune, i int, s string) bool {
	sr := []rune(s)
	if i+len(sr) > len(runes) {
		return false
	}
	for k, r := range sr {
		if runes[i+k] != r {
			return false
		}
	}
	return true
}

func indexRune(runes []rune, from int, target rune) int {
	for i := from; i < len(runes); i++ {
		if runes[i] == target {
			return i
		}
	}
	return -1
}

func indexSeq(runes []rune, from int, seq string) int {
	sr := []rune(seq)
	for i := from; i+len(sr) <= len(runes); i++ {
		match := true
		for k, r := range sr {
			if runes[i+k] != r {
				match = false
				break
			}
		}
		if match {
			return i
		}
	}
	return -1
}
