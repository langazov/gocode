package tui

import (
	"image/color"
	"strings"

	"github.com/langazov/gocode-go/internal/tui/theme"

	"charm.land/glamour/v2"
	"charm.land/glamour/v2/ansi"
)

// This file renders assistant markdown through charmbracelet/glamour: real
// CommonMark parsing (goldmark) plus chroma syntax-highlighted code fences —
// replacing an earlier hand-rolled Markdown-to-ANSI pass that had no syntax
// highlighter at all and only covered headers, fenced code, bold/italic/
// inline-code, lists, blockquotes, and rules (no nested emphasis, no
// tables). glamour's own lipgloss v2 dependency was the reason that swap
// was deferred until this port's TUI moved to lipgloss v2 too (see
// specs/go-port-gaps.md's dated entries).

// renderMarkdown converts text to ANSI-styled, word-wrapped lines within
// width, via a glamour renderer cached per theme+width on the App (see
// markdownRenderer): constructing one loads chroma's lexer/style
// registries, too costly to redo on every streamed delta.
func (a *App) renderMarkdown(text string, width int) string {
	if width < 10 {
		width = 10
	}
	r := a.markdownRenderer(width)
	if r == nil {
		return text
	}
	out, err := r.Render(text)
	if err != nil {
		return text
	}
	// glamour's block renderer inserts real CommonMark block spacing
	// (paragraphs/headings/lists/fences each carry their own trailing
	// blank line); only the outermost leading/trailing blank lines are
	// trimmed here since assistantTextBlock already manages the single
	// leading blank line it needs between parts.
	return strings.Trim(out, "\n")
}

// markdownRenderer returns the glamour renderer cached for width, rebuilding
// it only when width or the active theme has actually changed.
func (a *App) markdownRenderer(width int) *glamour.TermRenderer {
	if a.mdRenderer != nil && a.mdRendererWidth == width && a.mdRendererTheme == a.theme.Name {
		return a.mdRenderer
	}
	r, err := glamour.NewTermRenderer(
		glamour.WithStyles(glamourStyleConfig(a.theme)),
		glamour.WithWordWrap(width),
		// Truecolor to match every other truecolor hex color in this style
		// (and the rest of the app) — chroma's own default formatter
		// downsamples to 256-color ANSI otherwise.
		glamour.WithChromaFormatter("terminal16m"),
	)
	if err != nil {
		return nil
	}
	a.mdRenderer, a.mdRendererWidth, a.mdRendererTheme = r, width, a.theme.Name
	return r
}

// codeBlockTheme picks one of chroma's own bundled syntax-highlighting
// styles for fenced code, rather than mapping the app's theme colors onto
// chroma's token roles directly: glamour registers a custom Chroma style
// once under a single fixed name ("charm", internal to the ansi package —
// see codeblock.go's chromaStyleTheme const) and skips re-registering it if
// that name is already taken, so a custom per-theme Chroma struct would
// silently keep whichever theme rendered a code block *first* for the rest
// of the process — breaking this app's live dark/light theme toggle. Using
// one of chroma's pre-registered named styles instead sidesteps that
// entirely, and tokyonight-night/tokyonight-day are literal, exact matches
// for this app's Dark()/Light() palettes (Dark() *is* Tokyo Night).
func codeBlockTheme(t theme.Theme) string {
	if t.Dark {
		return "tokyonight-night"
	}
	return "tokyonight-day"
}

// glamourStyleConfig builds a glamour ansi.StyleConfig from the app's own
// theme colors rather than one of glamour's bundled prose styles, so
// markdown blends into the rest of the UI — including this port's custom
// light theme, which has no bundled glamour equivalent. Document/BlockQuote
// carry no Margin/BlockPrefix/BlockSuffix: this port's own layout
// (assistantTextBlock's indent+leading blank line) already handles the
// insetting glamour's bundled styles would otherwise add on top.
func glamourStyleConfig(t theme.Theme) ansi.StyleConfig {
	c := t.Colors
	str := func(s string) *string { return &s }
	hex := func(col color.Color) *string { s := theme.Hex(col); return &s }
	yes := func() *bool { b := true; return &b }
	one := func() *uint { v := uint(1); return &v }

	return ansi.StyleConfig{
		Document: ansi.StyleBlock{StylePrimitive: ansi.StylePrimitive{Color: hex(c.Text)}},
		BlockQuote: ansi.StyleBlock{
			StylePrimitive: ansi.StylePrimitive{Color: hex(c.TextMuted)},
			Indent:         one(),
			IndentToken:    str("│ "),
		},
		List: ansi.StyleList{StyleBlock: ansi.StyleBlock{StylePrimitive: ansi.StylePrimitive{Color: hex(c.Text)}}},

		Heading: ansi.StyleBlock{StylePrimitive: ansi.StylePrimitive{Color: hex(c.Primary), Bold: yes()}},
		H1:      ansi.StyleBlock{StylePrimitive: ansi.StylePrimitive{Prefix: "# ", Bold: yes()}},
		H2:      ansi.StyleBlock{StylePrimitive: ansi.StylePrimitive{Prefix: "## "}},
		H3:      ansi.StyleBlock{StylePrimitive: ansi.StylePrimitive{Prefix: "### "}},
		H4:      ansi.StyleBlock{StylePrimitive: ansi.StylePrimitive{Prefix: "#### "}},
		H5:      ansi.StyleBlock{StylePrimitive: ansi.StylePrimitive{Prefix: "##### "}},
		H6:      ansi.StyleBlock{StylePrimitive: ansi.StylePrimitive{Prefix: "###### "}},

		Strikethrough: ansi.StylePrimitive{CrossedOut: yes()},
		Emph:          ansi.StylePrimitive{Italic: yes()},
		Strong:        ansi.StylePrimitive{Bold: yes()},
		// glamour's Format template only ever sees the (empty, for a rule)
		// token text — no width variable is available to it — so, like
		// every one of glamour's own bundled styles, this can't stretch
		// edge-to-edge the way the old hand-rolled renderer's
		// strings.Repeat("─", width) did; a fixed-width divider is the best
		// this mechanism supports.
		HorizontalRule: ansi.StylePrimitive{Color: hex(c.BorderActive), Format: "\n" + strings.Repeat("─", 40) + "\n"},

		Item:        ansi.StylePrimitive{BlockPrefix: "• "},
		Enumeration: ansi.StylePrimitive{BlockPrefix: ". ", Color: hex(c.Primary)},

		Link:      ansi.StylePrimitive{Color: hex(c.Primary), Underline: yes()},
		LinkText:  ansi.StylePrimitive{Color: hex(c.Accent)},
		Image:     ansi.StylePrimitive{Color: hex(c.Primary), Underline: yes()},
		ImageText: ansi.StylePrimitive{Color: hex(c.Accent)},

		Code: ansi.StyleBlock{StylePrimitive: ansi.StylePrimitive{
			Color:           hex(c.Accent),
			BackgroundColor: hex(c.BackgroundElement),
		}},
		CodeBlock: ansi.StyleCodeBlock{
			StyleBlock: ansi.StyleBlock{StylePrimitive: ansi.StylePrimitive{Color: hex(c.Text)}},
			Theme:      codeBlockTheme(t),
		},
	}
}
