package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/langazov/gocode-go/internal/tui/client"
)

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
	got, ref := app.toolRow(client.Message{}, "t1", "read", state)
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
	got, ref := app.toolRow(client.Message{}, "t1", "read", state)
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
	got, ref := app.toolRow(client.Message{}, "t1", "write", state)
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
	open, _ := app.toolRow(client.Message{}, "t1", "read", state("/tmp/a.go"))
	shut, _ := app.toolRow(client.Message{}, "t2", "read", state("/tmp/b.go"))
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
	got, ref := app.toolRow(client.Message{}, "t1", "read", state)
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
	got, _ := app.toolRow(client.Message{}, "t1", "read", state)
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
	got, _ := app.toolRow(client.Message{}, "t1", "edit", state)
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
