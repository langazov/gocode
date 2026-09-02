package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// Note: lipgloss v2 (which glamour is built on) always emits real ANSI —
// unlike v1, which auto-detected "no color" under `go test` (stdout isn't a
// TTY) and silently no-op'd every styled Render call. These tests strip
// ANSI (ansi.Strip) before asserting on content/structure, since glamour's
// block renderer emits prefix/content/suffix as separate styled runs even
// when they share the same color, which would otherwise split a literal
// substring check across an escape sequence.

func testMDApp() *App {
	return &App{width: 100, height: 30, theme: themeResolve("opencode-dark")}
}

func TestMarkdownHeaderRendersWithLevelPrefix(t *testing.T) {
	app := testMDApp()
	got := ansi.Strip(app.renderMarkdown("## Section Title", 80))
	// glamour's own convention (used by every one of its bundled styles,
	// since a terminal can't vary font size) is to keep a literal "## "
	// level indicator rather than stripping it — this style config mirrors
	// that instead of fully hiding it like the old hand-rolled renderer did.
	if !strings.Contains(got, "## Section Title") {
		t.Fatalf("header should keep its level prefix and text, got %q", got)
	}
}

func TestMarkdownBoldAndItalicAreStyledAndMarkersStripped(t *testing.T) {
	app := testMDApp()
	got := ansi.Strip(app.renderMarkdown("this is **bold** and *italic* text", 80))
	if strings.Contains(got, "*") {
		t.Fatalf("markdown emphasis markers should be stripped, got %q", got)
	}
	if !strings.Contains(got, "bold") || !strings.Contains(got, "italic") {
		t.Fatalf("emphasis text missing, got %q", got)
	}
}

func TestMarkdownInlineCodeIsStyledAndBackticksStripped(t *testing.T) {
	app := testMDApp()
	got := ansi.Strip(app.renderMarkdown("run `go test ./...` now", 80))
	if strings.Contains(got, "`") {
		t.Fatalf("backticks should be stripped, got %q", got)
	}
	if !strings.Contains(got, "go test ./...") {
		t.Fatalf("inline code text missing, got %q", got)
	}
}

func TestMarkdownUnorderedListUsesBullet(t *testing.T) {
	app := testMDApp()
	got := ansi.Strip(app.renderMarkdown("- first\n- second", 80))
	if !strings.Contains(got, "•") {
		t.Fatalf("unordered list should render a bullet, got %q", got)
	}
	if !strings.Contains(got, "first") || !strings.Contains(got, "second") {
		t.Fatalf("list items missing, got %q", got)
	}
}

func TestMarkdownOrderedListKeepsNumber(t *testing.T) {
	app := testMDApp()
	got := ansi.Strip(app.renderMarkdown("1. one\n2. two", 80))
	if !strings.Contains(got, "1. one") || !strings.Contains(got, "2. two") {
		t.Fatalf("ordered list numbers missing, got %q", got)
	}
}

func TestMarkdownFencedCodeBlockKeepsContentVerbatim(t *testing.T) {
	app := testMDApp()
	got := ansi.Strip(app.renderMarkdown("```go\nfmt.Println(\"hi\")\n```", 80))
	if strings.Contains(got, "```") {
		t.Fatalf("fence markers should not appear in output, got %q", got)
	}
	if !strings.Contains(got, `fmt.Println("hi")`) {
		t.Fatalf("code content missing, got %q", got)
	}
}

// TestMarkdownFenceLinesAreUniformWidth guards the same property the old
// hand-rolled renderer's padToWidth enforced: every fenced-code line's
// highlighted background should span the same full width, forming one
// stable rectangle instead of each line's highlight only reaching as far as
// its own text.
func TestMarkdownFenceLinesAreUniformWidth(t *testing.T) {
	app := testMDApp()
	got := app.renderMarkdown("```\nshort\na much longer line of code here\nx\n```", 40)
	for _, line := range strings.Split(got, "\n") {
		if strings.TrimSpace(ansi.Strip(line)) == "" {
			continue
		}
		if w := visibleWidth(line); w != 40 {
			t.Fatalf("fence line width = %d, want the full 40 (padded, not ragged): %q", w, line)
		}
	}
}

func TestMarkdownFencedCodeBlockNotTreatedAsMarkdown(t *testing.T) {
	app := testMDApp()
	// Asterisks/hashes inside a fence must stay literal, not become emphasis
	// or headers.
	got := ansi.Strip(app.renderMarkdown("```\n# not a header\n*not italic*\n```", 80))
	if !strings.Contains(got, "# not a header") {
		t.Fatalf("fenced '#' should stay literal, got %q", got)
	}
	if !strings.Contains(got, "*not italic*") {
		t.Fatalf("fenced '*' should stay literal, got %q", got)
	}
}

func TestMarkdownBlockquotePrefixed(t *testing.T) {
	app := testMDApp()
	got := ansi.Strip(app.renderMarkdown("> quoted text", 80))
	if !strings.Contains(got, "│") {
		t.Fatalf("blockquote should have a │ prefix, got %q", got)
	}
	if !strings.Contains(got, "quoted text") {
		t.Fatalf("blockquote text missing, got %q", got)
	}
}

func TestMarkdownHorizontalRule(t *testing.T) {
	app := testMDApp()
	got := ansi.Strip(app.renderMarkdown("above\n\n---\n\nbelow", 40))
	// glamour's Format template has no width variable available to it (see
	// glamourStyleConfig's doc comment), so unlike the old hand-rolled
	// renderer this can't stretch edge-to-edge — just assert a rule of
	// dashes/box-drawing characters actually appears between the two words.
	if !strings.Contains(got, "above") || !strings.Contains(got, "below") {
		t.Fatalf("surrounding text missing, got %q", got)
	}
	if !strings.Contains(got, "─") {
		t.Fatalf("horizontal rule missing, got %q", got)
	}
}

func TestMarkdownPlainTextUnaffected(t *testing.T) {
	app := testMDApp()
	got := ansi.Strip(app.renderMarkdown("just a normal sentence with no markdown", 80))
	if !strings.Contains(got, "just a normal sentence with no markdown") {
		t.Fatalf("plain text should pass through, got %q", got)
	}
}

func TestMarkdownNeverOverflowsWidth(t *testing.T) {
	app := testMDApp()
	long := "this is a very long fenced code line that would overflow a narrow terminal width for sure"
	got := app.renderMarkdown("```\n"+long+"\n```", 20)
	for _, line := range strings.Split(got, "\n") {
		if w := visibleWidth(line); w > 20 {
			t.Fatalf("line exceeds width 20 (got %d): %q", w, line)
		}
	}
}

// visibleWidth strips SGR sequences to measure a line's printable width.
func visibleWidth(line string) int {
	var out strings.Builder
	inEscape := false
	for _, r := range line {
		if inEscape {
			if r == 'm' {
				inEscape = false
			}
			continue
		}
		if r == 0x1b {
			inEscape = true
			continue
		}
		out.WriteRune(r)
	}
	return len([]rune(out.String()))
}
