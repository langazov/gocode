package tui

import (
	"fmt"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"

	"github.com/langazov/gocode-go/internal/tui/client"
)

// first drops toolRow's click-target return so a call can be inlined. plain
// (reasoning_test.go) strips the SGR sequences syntax highlighting puts
// through a block, so an assertion can talk about the text the user reads.
func first(block string, _ *toolOutputHeaderRef) string { return block }

// --- file tool rows: path + collapsible content ---------------------------

// The builtins name the argument "path"; the TypeScript tools this port came
// from call it "filePath". Reading only the latter left every file tool row
// stuck on its placeholder with the path sitting unread in the input.
func TestFileToolLabelsReadThePathArgument(t *testing.T) {
	cases := []struct {
		tool string
		key  string
		want string
	}{
		{"read", "path", "Read /tmp/a.go"},
		{"read", "filePath", "Read /tmp/a.go"},
		{"write", "path", "Write /tmp/a.go"},
		{"write", "filePath", "Write /tmp/a.go"},
		{"edit", "path", "Edit /tmp/a.go"},
		{"edit", "filePath", "Edit /tmp/a.go"},
	}
	for _, tc := range cases {
		t.Run(tc.tool+"/"+tc.key, func(t *testing.T) {
			_, label := toolLabel(tc.tool, map[string]any{tc.key: "/tmp/a.go"}, nil)
			if label != tc.want {
				t.Errorf("label = %q, want %q", label, tc.want)
			}
		})
	}
}

// The placeholders stay for the window before the arguments have arrived.
func TestFileToolLabelsFallBackWithoutAPath(t *testing.T) {
	for tool, want := range map[string]string{
		"read":  "Reading file...",
		"write": "Preparing write...",
		"edit":  "Preparing edit...",
	} {
		if _, label := toolLabel(tool, map[string]any{}, nil); label != want {
			t.Errorf("%s label = %q, want %q", tool, label, want)
		}
	}
}

// A path inside the project shows relative to it; anything else falls back to
// the home-abbreviated form.
func TestDisplayPath(t *testing.T) {
	app := &App{cwd: "/home/dev/project", homeDir: "/home/dev"}
	cases := map[string]string{
		"/home/dev/project/internal/tui/app.go": "internal/tui/app.go",
		"/home/dev/notes.md":                    "~/notes.md",
		"/etc/hosts":                            "/etc/hosts",
		"relative/path.go":                      "relative/path.go",
		"":                                      "",
	}
	for in, want := range cases {
		if got := app.displayPath(in); got != want {
			t.Errorf("displayPath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestReadBlockShowsPathAndCollapsedContent(t *testing.T) {
	app := &App{width: 100, height: 30, expandedToolOutput: map[string]bool{}}
	var lines []string
	for i := 1; i <= 20; i++ {
		lines = append(lines, fmt.Sprintf("%d: line %d", i, i))
	}
	state := &toolState{
		Status: "done",
		Input:  map[string]any{"path": "/tmp/a.go"},
		Output: strings.Join(lines, "\n"),
	}
	raw, ref := app.toolRow(client.Message{}, "t1", "read", state)
	got := plain(raw)
	if !strings.Contains(got, "Read /tmp/a.go") {
		t.Errorf("read block should name the file, got %q", got)
	}
	if !strings.Contains(got, "20 lines") {
		t.Errorf("read block should count the lines it returned, got %q", got)
	}
	if !strings.Contains(got, "1: line 1") {
		t.Errorf("read block should preview the first line, got %q", got)
	}
	if strings.Contains(got, "20: line 20") {
		t.Errorf("collapsed read block should hide the rest, got %q", got)
	}
	if !strings.Contains(got, "click to expand") {
		t.Errorf("read block should hint it can be expanded, got %q", got)
	}
	if ref == nil || ref.id != "t1" {
		t.Fatalf("collapsed read block needs a click target, got %+v", ref)
	}
}

func TestReadBlockExpandedShowsWholeFile(t *testing.T) {
	app := &App{width: 100, height: 30, expandedToolOutput: map[string]bool{"t1": true}}
	var lines []string
	for i := 1; i <= 200; i++ {
		lines = append(lines, fmt.Sprintf("%d: line %d", i, i))
	}
	state := &toolState{
		Status: "done",
		Input:  map[string]any{"path": "/tmp/a.go"},
		Output: strings.Join(lines, "\n"),
	}
	raw, ref := app.toolRow(client.Message{}, "t1", "read", state)
	got := plain(raw)
	if !strings.Contains(got, "200: line 200") {
		t.Error("expanded read block should show the whole file")
	}
	// No header row survives once open, so the whole block collapses it back.
	if ref == nil || ref.lineStart != 0 {
		t.Fatalf("expanded read block should be clickable throughout, got %+v", ref)
	}
}

// write's own output is a one-line "Wrote file successfully"; what is worth
// showing is the content the model sent.
func TestWriteBlockShowsTheContentItStored(t *testing.T) {
	app := &App{width: 100, height: 30, expandedToolOutput: map[string]bool{}}
	state := &toolState{
		Status: "done",
		Input: map[string]any{
			"path":    "/tmp/a.go",
			"content": "package main\n\nfunc main() {}\n",
		},
		Output: "Created file successfully: /tmp/a.go",
	}
	raw, ref := app.toolRow(client.Message{}, "t1", "write", state)
	got := plain(raw)
	if !strings.Contains(got, "Write /tmp/a.go") {
		t.Errorf("write block should name the file, got %q", got)
	}
	if !strings.Contains(got, "3 lines") {
		t.Errorf("write block should count the content's lines, got %q", got)
	}
	if !strings.Contains(got, "package main") {
		t.Errorf("write block should preview the content, got %q", got)
	}
	if strings.Contains(got, "func main") {
		t.Errorf("collapsed write block should hide the rest, got %q", got)
	}
	if ref == nil {
		t.Error("collapsed write block needs a click target")
	}
}

// Two calls in one message toggle independently: the state is keyed by the
// tool part's own id.
func TestFileBlocksExpandIndependently(t *testing.T) {
	app := &App{width: 100, height: 30, expandedToolOutput: map[string]bool{"t1": true}}
	state := func(path string) *toolState {
		return &toolState{
			Status: "done",
			Input:  map[string]any{"path": path},
			Output: "1: first\n2: second",
		}
	}
	open := plain(first(app.toolRow(client.Message{}, "t1", "read", state("/tmp/a.go"))))
	shut := plain(first(app.toolRow(client.Message{}, "t2", "read", state("/tmp/b.go"))))
	if !strings.Contains(open, "2: second") {
		t.Error("t1 was expanded and should show both lines")
	}
	if strings.Contains(shut, "2: second") {
		t.Error("t2 was not expanded and should stay collapsed")
	}
}

// While the tool is still running there is nothing to show, and the one-line
// row carries the spinner — but it names the file now.
func TestRunningFileToolKeepsTheSpinnerRow(t *testing.T) {
	app := &App{width: 100, height: 30, expandedToolOutput: map[string]bool{}}
	state := &toolState{Status: "running", Input: map[string]any{"path": "/tmp/a.go"}}
	raw, ref := app.toolRow(client.Message{}, "t1", "read", state)
	got := plain(raw)
	if strings.Contains(got, "click to expand") {
		t.Errorf("a running read has no content block, got %q", got)
	}
	if !strings.Contains(got, "Read /tmp/a.go") {
		t.Errorf("a running read should still name the file, got %q", got)
	}
	if ref != nil {
		t.Errorf("nothing to toggle while running, got %+v", ref)
	}
}

// A file tool whose input never arrived falls back to the one-line row rather
// than rendering an empty panel.
func TestFileBlockWithoutPathFallsBackToRow(t *testing.T) {
	app := &App{width: 100, height: 30, expandedToolOutput: map[string]bool{}}
	state := &toolState{Status: "done", Input: map[string]any{}, Output: "whatever"}
	raw, _ := app.toolRow(client.Message{}, "t1", "read", state)
	got := plain(raw)
	if !strings.Contains(got, "Reading file...") {
		t.Errorf("expected the placeholder row, got %q", got)
	}
}

func TestEditDiffBlockNamesTheFile(t *testing.T) {
	app := &App{width: 100, height: 30, theme: themeResolve("gocode-dark")}
	state := &toolState{
		Status: "done",
		Input:  map[string]any{"path": "/tmp/a.go"},
		Output: "```diff\n--- a/a.go\n+++ b/a.go\n@@ -1,1 +1,1 @@\n-old\n+new\n```",
	}
	raw, _ := app.toolRow(client.Message{}, "t1", "edit", state)
	got := plain(raw)
	if !strings.Contains(got, "Edit /tmp/a.go") {
		t.Errorf("edit diff block should name the file, got %q", got)
	}
}

// grep and glob take an optional path that narrows the search; which subtree
// was searched is part of the answer.
func TestSearchLabelsNameTheSubtree(t *testing.T) {
	_, label := toolLabel("grep", map[string]any{"pattern": "TODO", "path": "internal"}, nil)
	if label != `Grep "TODO" in internal` {
		t.Errorf("grep label = %q", label)
	}
	_, label = toolLabel("glob", map[string]any{"pattern": "*.go", "path": "cmd"}, nil)
	if label != `Glob "*.go" in cmd` {
		t.Errorf("glob label = %q", label)
	}
	_, label = toolLabel("grep", map[string]any{"pattern": "TODO"}, nil)
	if label != `Grep "TODO"` {
		t.Errorf("grep label without a path = %q", label)
	}
}

// --- syntax highlighting --------------------------------------------------

func TestFileHighlighterMatchesOnExtension(t *testing.T) {
	app := &App{theme: themeResolve("gocode-dark")}
	for _, path := range []string{"/tmp/a.go", "/tmp/a.py", "/tmp/a.tsx", "/tmp/Makefile", "/tmp/a.json"} {
		if app.fileHighlighter(path) == nil {
			t.Errorf("expected a lexer for %q", path)
		}
	}
	// Nothing chroma recognises: the caller shows the text plain rather than
	// guessing at a language.
	if app.fileHighlighter("/tmp/a.zzzznotalanguage") != nil {
		t.Error("expected no lexer for an unknown extension")
	}
}

func TestFileHighlighterColorsCodeAndKeepsItsText(t *testing.T) {
	app := &App{theme: themeResolve("gocode-dark")}
	highlight := app.fileHighlighter("/tmp/a.go")
	if highlight == nil {
		t.Fatal("no Go lexer")
	}
	const code = "package main\n\nfunc main() {}"
	got := highlight(code)
	if !strings.Contains(got, "\x1b[") {
		t.Error("highlighted code should carry SGR sequences")
	}
	if plain(got) != code {
		t.Errorf("highlighting changed the text: %q", plain(got))
	}
}

// The line numbers read puts in front of every line are not part of the
// source; a lexer handed "1: package main" gives up on the whole line.
func TestSplitLineNumbers(t *testing.T) {
	numbers, code, ok := splitLineNumbers("1: package main\n2:\n3: func main() {}")
	if !ok {
		t.Fatal("expected the read gutter to be recognised")
	}
	if code != "package main\n\nfunc main() {}" {
		t.Errorf("code = %q", code)
	}
	if numbers[0] != "1: " || numbers[2] != "3: " {
		t.Errorf("numbers = %q", numbers)
	}

	// write's content is not numbered, and neither is a directory listing.
	if _, _, ok := splitLineNumbers("package main\nfunc main() {}"); ok {
		t.Error("plain content should not be read as numbered")
	}
	if _, _, ok := splitLineNumbers("cmd/\ninternal/"); ok {
		t.Error("a directory listing should not be read as numbered")
	}
}

// Indentation is the structure of a file, and wrapText (which shell output
// uses) collapses it away with strings.Fields.
func TestCodeBodyKeepsIndentation(t *testing.T) {
	app := &App{theme: themeResolve("gocode-dark")}
	rows := app.codeBody("/tmp/a.go")("func main() {\n\tif x {\n\t\treturn\n\t}\n}", 60)
	if len(rows) != 5 {
		t.Fatalf("rows = %d, want one per source line", len(rows))
	}
	if !strings.HasPrefix(plain(rows[2]), "\t\treturn") {
		t.Errorf("indentation lost: %q", plain(rows[2]))
	}
}

// One source line stays one row: wrapping it would break the numbering and
// the alignment of everything under it.
func TestCodeBodyTruncatesRatherThanWraps(t *testing.T) {
	app := &App{theme: themeResolve("gocode-dark")}
	long := "1: " + strings.Repeat("x", 400)
	rows := app.codeBody("/tmp/a.go")(long, 40)
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	if width := lipgloss.Width(plain(rows[0])); width > 40 {
		t.Errorf("row width = %d, want it to fit in 40", width)
	}
	if !strings.HasPrefix(plain(rows[0]), "1: ") {
		t.Errorf("line number gutter lost: %q", plain(rows[0]))
	}
	if !strings.HasSuffix(plain(rows[0]), "…") {
		t.Errorf("truncation should be marked: %q", plain(rows[0]))
	}
}

// A file with no lexer still renders — plain, one line per row.
func TestCodeBodyWithoutALexer(t *testing.T) {
	app := &App{theme: themeResolve("gocode-dark")}
	rows := app.codeBody("/tmp/notes.zzzznotalanguage")("  indented\nplain", 60)
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(rows))
	}
	if plain(rows[0]) != "  indented" {
		t.Errorf("row = %q, want the text untouched", plain(rows[0]))
	}
}

// The read block highlights what it shows, and shows the file's own text.
func TestReadBlockHighlightsByExtension(t *testing.T) {
	app := &App{width: 100, height: 30, theme: themeResolve("gocode-dark"),
		expandedToolOutput: map[string]bool{"t1": true}}
	state := &toolState{
		Status: "done",
		Input:  map[string]any{"path": "/tmp/a.go"},
		Output: "1: package main\n2:\n3: func main() {}",
	}
	raw, _ := app.toolRow(client.Message{}, "t1", "read", state)
	if !strings.Contains(raw, "\x1b[38;2;") {
		t.Error("expanded read block should be syntax highlighted")
	}
	got := plain(raw)
	if !strings.Contains(got, "3: func main() {}") {
		t.Errorf("highlighting should not disturb the text, got %q", got)
	}
}

// --- markdown files -------------------------------------------------------

func TestIsMarkdownPath(t *testing.T) {
	for _, path := range []string{"/x/README.md", "/x/notes.markdown", "/x/a.MD", "/x/doc.mdx"} {
		if !isMarkdownPath(path) {
			t.Errorf("%q should render as prose", path)
		}
	}
	for _, path := range []string{"/x/main.go", "/x/notes.txt", "/x/mdfile", "/x/a.json"} {
		if isMarkdownPath(path) {
			t.Errorf("%q should not render as prose", path)
		}
	}
}

func TestMarkdownBodyRendersProse(t *testing.T) {
	app := &App{theme: themeResolve("gocode-dark")}
	rows := app.markdownBody("# Title\n\n- one\n- two\n\nSome **bold** text.", 60)
	got := plain(strings.Join(rows, "\n"))
	if !strings.Contains(got, "Title") {
		t.Errorf("heading text missing: %q", got)
	}
	if !strings.Contains(got, "•") {
		t.Errorf("list should render as bullets: %q", got)
	}
	if strings.Contains(got, "- one") {
		t.Errorf("raw list markers should be gone: %q", got)
	}
	if strings.Contains(got, "**bold**") {
		t.Errorf("emphasis markers should be gone: %q", got)
	}
}

// glamour reflows the text, so read's line numbers cannot survive — they would
// end up numbering rows that no longer match source lines.
func TestMarkdownBodyDropsReadLineNumbers(t *testing.T) {
	app := &App{theme: themeResolve("gocode-dark")}
	rows := app.markdownBody("1: # Title\n2: \n3: body text", 60)
	got := plain(strings.Join(rows, "\n"))
	if strings.Contains(got, "1:") || strings.Contains(got, "3:") {
		t.Errorf("line numbers should be gone: %q", got)
	}
	if !strings.Contains(got, "body text") {
		t.Errorf("content should survive: %q", got)
	}
}

// A markdown file reads as prose; anything else stays source.
func TestFileBodyPicksProseOnlyForMarkdown(t *testing.T) {
	app := &App{width: 100, height: 30, theme: themeResolve("gocode-dark"),
		expandedToolOutput: map[string]bool{"t1": true}}
	body := "1: # Title\n2: \n3: - a bullet"

	md := &toolState{Status: "done", Input: map[string]any{"path": "/x/NOTES.md"}, Output: body}
	raw, _ := app.toolRow(client.Message{}, "t1", "read", md)
	if got := plain(raw); !strings.Contains(got, "•") || strings.Contains(got, "3: ") {
		t.Errorf("a markdown read should render as prose, got %q", got)
	}

	code := &toolState{Status: "done", Input: map[string]any{"path": "/x/notes.go"}, Output: body}
	raw, _ = app.toolRow(client.Message{}, "t1", "read", code)
	if got := plain(raw); !strings.Contains(got, "3: ") {
		t.Errorf("a non-markdown read should stay source, got %q", got)
	}
}

// Writing a markdown file previews it the same way reading one does.
func TestWriteBlockRendersMarkdown(t *testing.T) {
	app := &App{width: 100, height: 30, theme: themeResolve("gocode-dark"),
		expandedToolOutput: map[string]bool{"t1": true}}
	state := &toolState{Status: "done", Input: map[string]any{
		"path": "/x/NOTES.md", "content": "# Title\n\n- a bullet\n"}}
	raw, _ := app.toolRow(client.Message{}, "t1", "write", state)
	if got := plain(raw); !strings.Contains(got, "•") {
		t.Errorf("a markdown write should render as prose, got %q", got)
	}
}

// More than one width is live on the same frame, so the renderer cache has to
// hold one per width — a single slot rebuilt a glamour renderer per call.
func TestMarkdownRendererCachesPerWidth(t *testing.T) {
	app := &App{theme: themeResolve("gocode-dark")}
	wide, narrow := app.markdownRenderer(80), app.markdownRenderer(40)
	if wide == nil || narrow == nil {
		t.Fatal("expected renderers")
	}
	if wide == narrow {
		t.Fatal("different widths need different renderers")
	}
	if again := app.markdownRenderer(80); again != wide {
		t.Error("the same width should hit the cache, not rebuild")
	}

	// A theme change invalidates everything: the style config is baked in.
	app.theme = themeResolve("gocode-light")
	if after := app.markdownRenderer(80); after == wide {
		t.Error("a theme change should rebuild the renderer")
	}
}
