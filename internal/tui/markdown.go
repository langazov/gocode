package tui

import (
	"image/color"
	"strconv"
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
	return a.renderMarkdownStyled(text, width, a.markdownRenderer)
}

// renderMarkdownDim is renderMarkdown through the dimmed reasoning-body
// palette (see markdownRendererDim / glamourStyleConfig's dim flag).
func (a *App) renderMarkdownDim(text string, width int) string {
	return a.renderMarkdownStyled(text, width, a.markdownRendererDim)
}

// renderMarkdownStyled is renderMarkdown's shared body: render + trim, with
// the renderer picked by the caller.
func (a *App) renderMarkdownStyled(text string, width int, pick func(int) *glamour.TermRenderer) string {
	if width < 10 {
		width = 10
	}
	// Tabs are expanded on the *source*, before glamour sizes anything: a
	// tab is width-ambiguous downstream — lipgloss.Width and
	// ansi.StringWidth count it as 1 cell while JoinHorizontal's getLines
	// expands it to 4 before measuring — and chroma passes a code fence's
	// literal tabs straight through. Expanding after the render would leave
	// each tab-indented line wider than the width glamour already wrapped
	// and padded against; expanding before means the wrap decisions, the
	// padding, and every later measurement all agree.
	text = strings.ReplaceAll(text, "\t", "    ")
	r := pick(width)
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

// markdownRenderer returns the glamour renderer cached for width, building one
// only for a width not seen yet under the current theme.
//
// The cache is dropped wholesale on a theme change, and again once it holds
// more widths than the interface plausibly uses at once — a terminal dragged
// slowly wider walks through a new width every frame, and each entry pins
// chroma's registries.
func (a *App) markdownRenderer(width int) *glamour.TermRenderer {
	return a.markdownRendererStyled(width, glamourKeyNormal, glamourStyleConfig(a.theme, false))
}

// glamourKeyNormal and glamourKeyDim are the two cache keys a width can hold.
// Normal is assistantTextBlock's full-brightness pass; dim fades every color
// toward the background (see glamourStyleConfig's dim parameter) and is what
// reasoning bodies render through, so a think block reads as one uniformly
// recessive run of thought rather than full-brightness text sitting under an
// already-faded header.
const (
	glamourKeyNormal = ""
	glamourKeyDim    = "dim"
)

// markdownRendererDim is markdownRenderer for the reasoning body: same cached
// machinery, dimmed palette.
func (a *App) markdownRendererDim(width int) *glamour.TermRenderer {
	return a.markdownRendererStyled(width, glamourKeyDim, glamourStyleConfig(a.theme, true))
}

// markdownRendererStyled is markdownRenderer's shared body: cache lookup by
// (variant, width), construction, insertion.
func (a *App) markdownRendererStyled(width int, variant string, styles ansi.StyleConfig) *glamour.TermRenderer {
	if a.mdRendererTheme != a.theme.Name || len(a.mdRenderers) > 8 {
		a.mdRenderers = nil
	}
	if r, ok := a.mdRenderers[variant+":"+strconv.Itoa(width)]; ok {
		return r
	}
	r, err := glamour.NewTermRenderer(
		glamour.WithStyles(styles),
		glamour.WithWordWrap(width),
		// Truecolor to match every other truecolor hex color in this style
		// (and the rest of the app) — chroma's own default formatter
		// downsamples to 256-color ANSI otherwise.
		glamour.WithChromaFormatter("terminal16m"),
	)
	if err != nil {
		return nil
	}
	if a.mdRenderers == nil {
		a.mdRenderers = map[string]*glamour.TermRenderer{}
	}
	a.mdRenderers[variant+":"+strconv.Itoa(width)], a.mdRendererTheme = r, a.theme.Name
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
// entirely. monokai/github are the closest bundled matches to this app's
// Dark()/Light() palettes (ported from the TS TUI's default "opencode"
// theme, see Dark()/Light()'s doc comment) — a near-black background with
// orange/purple accents for dark, a white background with blue/red accents
// for light — though, being pre-built styles rather than a literal mapping
// of theme colors onto chroma's roles, neither is an exact match.
func codeBlockTheme(t theme.Theme) string {
	if t.Dark {
		return "monokai"
	}
	return "github"
}

// glamourStyleConfig builds a glamour ansi.StyleConfig from the app's own
// theme colors rather than one of glamour's bundled prose styles, so
// markdown blends into the rest of the UI — including this port's custom
// light theme, which has no bundled glamour equivalent. Document/BlockQuote
// carry no Margin/BlockPrefix/BlockSuffix: this port's own layout
// (assistantTextBlock's indent+leading blank line) already handles the
// insetting glamour's bundled styles would otherwise add on top.
//
// dim builds the reasoning-body variant: every palette color is faded toward
// the background by ThinkingOpacity — exactly the fade reasoningBlock's
// header applies once its body shows — so the body cannot outshine its own
// dimmed header. Structural styling (prefixes, indents, underline/bold/
// italic) passes through untouched; only chroma code fences speak at full
// brightness.
func glamourStyleConfig(t theme.Theme, dim bool) ansi.StyleConfig {
	c := t.Colors
	str := func(s string) *string { return &s }
	hex := func(col color.Color) *string { s := theme.Hex(col); return &s }
	// dhex fades when dim is set, else delegates to hex. Body text keeps a
	// hair more presence (0.75 alpha) than the rest so paragraphs stay
	// readable rather than dissolving into the page.
	dhex := func(col color.Color) *string {
		if !dim {
			return hex(col)
		}
		alpha := t.ThinkingOpacity
		if col == c.Text {
			alpha = 0.75
		}
		s := theme.Hex(theme.FadeColor(t.Background, col, alpha))
		return &s
	}
	yes := func() *bool { b := true; return &b }
	one := func() *uint { v := uint(1); return &v }

	return ansi.StyleConfig{
		Document: ansi.StyleBlock{StylePrimitive: ansi.StylePrimitive{Color: dhex(c.Text)}},
		BlockQuote: ansi.StyleBlock{
			StylePrimitive: ansi.StylePrimitive{Color: dhex(c.TextMuted)},
			Indent:         one(),
			IndentToken:    str("│ "),
		},
		List: ansi.StyleList{StyleBlock: ansi.StyleBlock{StylePrimitive: ansi.StylePrimitive{Color: dhex(c.Text)}}},

		Heading: ansi.StyleBlock{StylePrimitive: ansi.StylePrimitive{Color: dhex(c.Primary), Bold: yes()}},
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
		HorizontalRule: ansi.StylePrimitive{Color: dhex(c.BorderActive), Format: "\n" + strings.Repeat("─", 40) + "\n"},

		Item:        ansi.StylePrimitive{BlockPrefix: "• "},
		Enumeration: ansi.StylePrimitive{BlockPrefix: ". ", Color: dhex(c.Primary)},

		Link:      ansi.StylePrimitive{Color: dhex(c.Primary), Underline: yes()},
		LinkText:  ansi.StylePrimitive{Color: dhex(c.Accent)},
		Image:     ansi.StylePrimitive{Color: dhex(c.Primary), Underline: yes()},
		ImageText: ansi.StylePrimitive{Color: dhex(c.Accent)},

		// TS's markup.raw.inline scope (theme/index.ts's getSyntaxRules) sets
		// background to theme.background, not backgroundElement: on an opaque
		// background that's a no-op (inline code just renders as plain
		// colored text, no highlight box), which is what this mirrors —
		// backgroundElement here painted a visible box behind every inline
		// code span that the original theme never draws.
		Code: ansi.StyleBlock{StylePrimitive: ansi.StylePrimitive{
			Color:           dhex(c.Accent),
			BackgroundColor: dhex(c.Background),
		}},
		CodeBlock: ansi.StyleCodeBlock{
			StyleBlock: ansi.StyleBlock{StylePrimitive: ansi.StylePrimitive{Color: dhex(c.Text)}},
			Theme:      codeBlockTheme(t),
		},
	}
}
