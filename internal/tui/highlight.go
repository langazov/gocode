package tui

import (
	"fmt"
	"image/color"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/formatters"
	"github.com/alecthomas/chroma/v2/lexers"
	chromastyles "github.com/alecthomas/chroma/v2/styles"
	"github.com/charmbracelet/x/ansi"
)

// Syntax highlighting for the file contents a read/write tool block shows.
//
// This goes through chroma directly rather than through glamour (which is
// what highlights fenced code inside an assistant message): glamour renders a
// whole markdown document, with its own margins, wrapping and code-block
// chrome, none of which belongs inside blockToolStyle's panel. The lexer,
// style and formatter underneath are the same ones glamour would reach for,
// so a Go file looks the same whether the model pasted it into a fence or the
// read tool returned it.
//
// The style follows codeBlockTheme — monokai on a dark theme, github on a
// light one — for the same reason it does there.

// maxHighlightBytes bounds what gets tokenised. A file large enough to exceed
// it renders plain: chroma is linear but not free, and this runs inside the
// per-frame timeline render (cached per message, but a cache miss is a frame).
const maxHighlightBytes = 256 << 10

// fileHighlighter returns a function that colors a chunk of the named file's
// contents, or nil when chroma has no lexer for the extension — in which case
// the caller shows the text plain rather than guessing at a language.
//
// Matching is on the file name alone (chroma keys its lexers by glob, so
// ".go", "Makefile" and "*.tsx" all resolve here). Content sniffing is
// deliberately not a fallback: it is slower, and on a one-line collapsed
// preview it has almost nothing to go on.
func (a *App) fileHighlighter(path string) func(string) string {
	lexer := lexers.Match(filepath.Base(path))
	if lexer == nil {
		return nil
	}
	// Coalesce merges runs of same-type tokens, which cuts the number of SGR
	// sequences the formatter emits for a line of code roughly in half.
	lexer = chroma.Coalesce(lexer)
	style := chromastyles.Get(codeBlockTheme(a.theme))
	if style == nil {
		return nil
	}
	// terminal16m to match markdownRenderer's WithChromaFormatter: the rest of
	// the interface is truecolor, and chroma's default formatter downsamples
	// to 256 colors.
	formatter := formatters.Get("terminal16m")
	if formatter == nil {
		return nil
	}
	return func(code string) string {
		if len(code) > maxHighlightBytes {
			return code
		}
		iterator, err := lexer.Tokenise(nil, code)
		if err != nil {
			return code
		}
		var out strings.Builder
		if err := formatter.Format(&out, style, iterator); err != nil {
			return code
		}
		// Tokenising appends a trailing newline the input did not have; it
		// would show up as an extra blank row inside the panel.
		return strings.TrimSuffix(out.String(), "\n")
	}
}

// lineNumberPrefix matches the "12: " gutter the read tool puts on every line
// it returns (internal/tool/builtins/read.go's readFile). A blank source line
// comes back as just "12: ", and trailing whitespace does not always survive
// the round trip, so the space after the colon is optional at end of line.
var lineNumberPrefix = regexp.MustCompile(`^(\d+):( |$)`)

// splitLineNumbers separates read's gutter from the code under it, reporting
// false when the text is not in that shape (a write's content, a directory
// listing, a file whose own lines happen to start with digits).
//
// The gutter has to come off before tokenising: "42: func main() {" is not
// Go, and a lexer handed it gives up and colors the whole line as an error.
func splitLineNumbers(text string) (numbers []string, code string, ok bool) {
	lines := strings.Split(text, "\n")
	numbers = make([]string, len(lines))
	stripped := make([]string, len(lines))
	for i, line := range lines {
		match := lineNumberPrefix.FindStringSubmatch(line)
		if match == nil {
			// A blank line inside a numbered block is not a counter-example;
			// anything else is.
			if strings.TrimSpace(line) == "" {
				stripped[i] = line
				continue
			}
			return nil, text, false
		}
		if _, err := strconv.Atoi(match[1]); err != nil {
			return nil, text, false
		}
		// Normalised so the gutter is one width down the whole block, even
		// where the source line was blank and the space went missing.
		numbers[i] = match[1] + ": "
		stripped[i] = line[len(match[0]):]
	}
	return numbers, strings.Join(stripped, "\n"), true
}

// keepBackground re-arms bg after every full SGR reset in text.
//
// chroma ends each token with ESC[0m, which resets *every* attribute — the
// background blockToolStyle painted behind the block included. Without this
// the highlighted region shows the page background through the panel, in a
// ragged band that follows the shape of the code. lipgloss cannot fix it from
// the outside: the outer Render has already emitted those cells by the time
// its own fill would apply (see renderLines for the same problem in its
// padding form).
func keepBackground(text string, bg color.Color) string {
	if bg == nil || !strings.Contains(text, "\x1b[0m") {
		return text
	}
	r, g, b, _ := bg.RGBA()
	fill := fmt.Sprintf("\x1b[48;2;%d;%d;%dm", r>>8, g>>8, b>>8)
	return strings.ReplaceAll(text, "\x1b[0m", "\x1b[0m"+fill)
}

// bodyRenderer turns the collapsible half of a BlockTool into display rows.
// Tool output and file contents want opposite treatment, which is why this is
// a parameter: see wrappedBody and codeBody.
type bodyRenderer func(text string, width int) []string

// wrappedBody is the treatment shell output gets: word-wrapped to the panel,
// in the plain text color.
func (a *App) wrappedBody(text string, width int) []string {
	return strings.Split(a.onPanelText(wrapText(text, width)), "\n")
}

// markdownExtensions are the suffixes that get prose treatment rather than
// syntax highlighting. Lowercased before the lookup.
var markdownExtensions = map[string]bool{
	".md": true, ".markdown": true, ".mdown": true, ".mkd": true, ".mdx": true,
}

// isMarkdownPath reports whether the file should render as prose.
func isMarkdownPath(path string) bool {
	return markdownExtensions[strings.ToLower(filepath.Ext(path))]
}

// markdownBody is the treatment a markdown file gets: rendered as prose
// through glamour — headings, lists, emphasis, tables and highlighted fences —
// rather than shown as its own source.
//
// This is the same renderer an assistant message goes through, so a README
// looks the same whether the model quoted it or the read tool returned it.
//
// Read's line numbers come off and do not come back: glamour reflows the text,
// so a source line no longer corresponds to a row and a gutter would be
// numbering the wrong things. That is the trade — a markdown file is being
// shown for what it says, and a file being read for its line numbers is one
// you want as source anyway.
func (a *App) markdownBody(text string, width int) []string {
	if _, stripped, numbered := splitLineNumbers(text); numbered {
		text = stripped
	}
	rendered := a.renderMarkdown(text, width)
	lines := strings.Split(rendered, "\n")
	for i, line := range lines {
		// glamour wraps to width, but a table or a long unbroken URL can
		// still overhang; the panel has a fixed interior.
		lines[i] = keepBackground(ansi.Truncate(line, width, ""), a.theme.BackgroundPanel)
	}
	return lines
}

// codeBody is the treatment file contents get: one source line per row,
// syntax-highlighted, over-long lines truncated rather than wrapped.
//
// Wrapping is wrong for code twice over. wrapText splits on strings.Fields,
// which collapses every run of whitespace — so indentation, the thing that
// carries the structure of the file, disappears. And even a whitespace-
// preserving wrap turns one source line into several rows, which breaks the
// line numbering read puts in the gutter. Truncating keeps every row on the
// line it belongs to; the file is on disk if the tail matters.
func (a *App) codeBody(path string) bodyRenderer {
	highlight := a.fileHighlighter(path)
	return func(text string, width int) []string {
		numbers, code, numbered := splitLineNumbers(text)
		// A tab is width-ambiguous (see renderMarkdownStyled's note on the
		// same problem): expand before highlighting so the lexer, the
		// truncation below, and JoinHorizontal's measurement all agree, and
		// an indented source line cannot push the chat column wide.
		code = strings.ReplaceAll(code, "\t", "    ")
		if highlight != nil {
			code = highlight(code)
		}
		muted := a.styles().Muted
		lines := strings.Split(code, "\n")
		for i, line := range lines {
			room := width
			prefix := ""
			if numbered && i < len(numbers) && numbers[i] != "" {
				room = max(width-lipgloss.Width(numbers[i]), 1)
				prefix = muted.Render(numbers[i])
			}
			lines[i] = keepBackground(prefix+ansi.Truncate(line, room, muted.Render("…")), a.theme.BackgroundPanel)
		}
		return lines
	}
}

// fileBody picks how a file's contents are shown: markdown renders as prose,
// everything else as syntax-highlighted source.
func (a *App) fileBody(path string) bodyRenderer {
	if isMarkdownPath(path) {
		return a.markdownBody
	}
	return a.codeBody(path)
}
