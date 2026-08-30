package tui

import (
	"strings"
	"testing"
)

// Note: lipgloss's default renderer auto-detects "no color" under `go test`
// (stdout isn't a TTY) and silently no-ops every Bold()/Foreground() call,
// so these tests can't assert on actual SGR codes without forcing a color
// profile globally — which also changes bubbles/textarea's cursor
// rendering and breaks unrelated tests (tried, reverted). They instead
// assert on the structural transform: markdown markers stripped, content
// preserved, layout (bullets, fences, quotes, rules) correct.

func testMDApp() *App {
	return &App{width: 100, height: 30, theme: themeResolve("opencode-dark")}
}

func TestMarkdownHeaderIsBoldAndStripsHashes(t *testing.T) {
	app := testMDApp()
	got := app.renderMarkdown("## Section Title", 80)
	if strings.Contains(got, "#") {
		t.Fatalf("header hashes should be stripped, got %q", got)
	}
	if !strings.Contains(got, "Section Title") {
		t.Fatalf("header text missing, got %q", got)
	}
}

func TestMarkdownBoldAndItalicAreStyledAndMarkersStripped(t *testing.T) {
	app := testMDApp()
	got := app.renderMarkdown("this is **bold** and *italic* text", 80)
	if strings.Contains(got, "*") {
		t.Fatalf("markdown emphasis markers should be stripped, got %q", got)
	}
	if !strings.Contains(got, "bold") || !strings.Contains(got, "italic") {
		t.Fatalf("emphasis text missing, got %q", got)
	}
}

func TestMarkdownInlineCodeIsStyledAndBackticksStripped(t *testing.T) {
	app := testMDApp()
	got := app.renderMarkdown("run `go test ./...` now", 80)
	if strings.Contains(got, "`") {
		t.Fatalf("backticks should be stripped, got %q", got)
	}
	if !strings.Contains(got, "go test ./...") {
		t.Fatalf("inline code text missing, got %q", got)
	}
}

func TestMarkdownUnorderedListUsesBullet(t *testing.T) {
	app := testMDApp()
	got := app.renderMarkdown("- first\n- second", 80)
	if !strings.Contains(got, "•") {
		t.Fatalf("unordered list should render a bullet, got %q", got)
	}
	if !strings.Contains(got, "first") || !strings.Contains(got, "second") {
		t.Fatalf("list items missing, got %q", got)
	}
}

func TestMarkdownOrderedListKeepsNumber(t *testing.T) {
	app := testMDApp()
	got := app.renderMarkdown("1. one\n2. two", 80)
	if !strings.Contains(got, "1.") || !strings.Contains(got, "2.") {
		t.Fatalf("ordered list numbers missing, got %q", got)
	}
}

func TestMarkdownFencedCodeBlockKeepsContentVerbatim(t *testing.T) {
	app := testMDApp()
	got := app.renderMarkdown("```go\nfmt.Println(\"hi\")\n```", 80)
	if strings.Contains(got, "```") {
		t.Fatalf("fence markers should not appear in output, got %q", got)
	}
	if !strings.Contains(got, `fmt.Println("hi")`) {
		t.Fatalf("code content missing, got %q", got)
	}
	if !strings.Contains(got, "go") {
		t.Fatalf("fence language label missing, got %q", got)
	}
}

// TestMarkdownFenceLinesAreUniformWidth guards against a regression where
// each fence line's highlighted background only spanned as far as its own
// text (via clipToWidth alone), so the code block's right edge visibly
// "danced" line to line — and, while streaming, over time as lines grew —
// instead of forming one stable rectangle.
func TestMarkdownFenceLinesAreUniformWidth(t *testing.T) {
	app := testMDApp()
	got := app.renderMarkdown("```\nshort\na much longer line of code here\nx\n```", 40)
	for _, line := range strings.Split(got, "\n") {
		if strings.TrimSpace(line) == "" {
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
	got := app.renderMarkdown("```\n# not a header\n*not italic*\n```", 80)
	if !strings.Contains(got, "# not a header") {
		t.Fatalf("fenced '#' should stay literal, got %q", got)
	}
	if !strings.Contains(got, "*not italic*") {
		t.Fatalf("fenced '*' should stay literal, got %q", got)
	}
}

func TestMarkdownBlockquotePrefixed(t *testing.T) {
	app := testMDApp()
	got := app.renderMarkdown("> quoted text", 80)
	if !strings.Contains(got, "│") {
		t.Fatalf("blockquote should have a │ prefix, got %q", got)
	}
	if !strings.Contains(got, "quoted text") {
		t.Fatalf("blockquote text missing, got %q", got)
	}
}

func TestMarkdownHorizontalRule(t *testing.T) {
	app := testMDApp()
	got := app.renderMarkdown("above\n\n---\n\nbelow", 40)
	if !strings.Contains(got, strings.Repeat("─", 40)) {
		t.Fatalf("horizontal rule missing, got %q", got)
	}
}

func TestMarkdownPlainTextUnaffected(t *testing.T) {
	app := testMDApp()
	got := app.renderMarkdown("just a normal sentence with no markdown", 80)
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
