package builtins

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Ported from packages/opencode/test/tool/{glob,grep}.test.ts, for the parts
// this port implements. Upstream additionally asserts a token-budget
// truncation notice ("Results truncated. Consider using a more specific path
// or pattern.") that this port expresses as a plain result limit.

// searchTree lays out a small repository under a fresh root.
func searchTree(t *testing.T, files map[string]string) (Resolver, string) {
	t.Helper()
	resolver, root := newRoot(t)
	for rel, content := range files {
		full := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return resolver, root
}

// The empty result has to be a sentence, not an empty string: an empty tool
// result reads to the model as a failure and gets retried.
func TestSearchToolsReportEmptyResultsExplicitly(t *testing.T) {
	resolver, _ := searchTree(t, map[string]string{"a.go": "package main\n"})

	out, err := NewGlobTool(resolver).Execute(context.Background(), map[string]any{"pattern": "*.rs"})
	if err != nil {
		t.Fatal(err)
	}
	if out != "No files found" {
		t.Errorf("glob with no matches = %q", out)
	}

	out, err = NewGrepTool(resolver).Execute(context.Background(), map[string]any{"pattern": "nowhere"})
	if err != nil {
		t.Fatal(err)
	}
	if out != "No files found" {
		t.Errorf("grep with no matches = %q", out)
	}
}

// A search scoped to a subdirectory must not reach outside it, and must not be
// a way around the sandbox either.
func TestSearchHonoursThePathArgument(t *testing.T) {
	resolver, _ := searchTree(t, map[string]string{
		"src/a.go":    "needle\n",
		"vendor/b.go": "needle\n",
	})

	out, err := NewGrepTool(resolver).Execute(context.Background(), map[string]any{
		"pattern": "needle", "path": "src",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "a.go") || strings.Contains(out, "b.go") {
		t.Errorf("a scoped grep leaked outside its path: %q", out)
	}

	for name, exec := range map[string]func(context.Context, map[string]any) (string, error){
		"glob": NewGlobTool(resolver).Execute,
		"grep": NewGrepTool(resolver).Execute,
	} {
		if _, err := exec(context.Background(), map[string]any{"pattern": "*", "path": "../.."}); err == nil {
			t.Errorf("%s must refuse a path outside the root", name)
		}
	}
}

// The limit is what keeps a search over a large tree from returning the tree.
// Both tools have to stop at it rather than gather everything and slice.
func TestSearchRespectsTheResultLimit(t *testing.T) {
	files := map[string]string{}
	for i := range 40 {
		files[fmt.Sprintf("pkg/f%02d.go", i)] = "needle\n"
	}
	resolver, _ := searchTree(t, files)

	out, err := NewGlobTool(resolver).Execute(context.Background(), map[string]any{
		"pattern": "**/*.go", "limit": 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := len(strings.Split(strings.TrimSpace(out), "\n")); got != 5 {
		t.Errorf("glob returned %d paths, want the limit of 5:\n%s", got, out)
	}

	out, err = NewGrepTool(resolver).Execute(context.Background(), map[string]any{
		"pattern": "needle", "limit": 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	// The count in the header is what was returned, not a guess at the total:
	// upstream's "does not report an unknown total when results are
	// truncated" is the same requirement.
	if !strings.Contains(out, "Found 5 matches") {
		t.Errorf("grep should report the number it actually returned, got header %q",
			strings.SplitN(out, "\n", 2)[0])
	}
}

// Glob results are sorted, so two runs over the same tree agree — a search
// whose order comes from the filesystem makes every diff of a transcript noise.
func TestGlobResultsAreSorted(t *testing.T) {
	resolver, _ := searchTree(t, map[string]string{
		"z.go": "x\n", "a.go": "x\n", "m/n.go": "x\n",
	})
	glob := NewGlobTool(resolver)

	first, err := glob.Execute(context.Background(), map[string]any{"pattern": "**/*.go"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := glob.Execute(context.Background(), map[string]any{"pattern": "**/*.go"})
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("glob is not stable:\n%q\n%q", first, second)
	}
	lines := strings.Split(strings.TrimSpace(first), "\n")
	for i := 1; i < len(lines); i++ {
		if lines[i-1] > lines[i] {
			t.Fatalf("results are not sorted: %q before %q", lines[i-1], lines[i])
		}
	}
}

// grep reports where a match is, not just that there was one: the file and the
// line number are what the next read or edit is addressed to.
func TestGrepReportsFileAndLine(t *testing.T) {
	resolver, _ := searchTree(t, map[string]string{
		"a.go": "package main\n\nfunc target() {}\n",
	})
	out, err := NewGrepTool(resolver).Execute(context.Background(), map[string]any{"pattern": "func target"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "a.go") {
		t.Errorf("no file in the output: %q", out)
	}
	if !strings.Contains(out, "3") {
		t.Errorf("no line number in the output: %q", out)
	}
}

// The pattern is a regular expression, and an invalid one is a caller error
// rather than a silent no-match.
func TestGrepRejectsAnInvalidPattern(t *testing.T) {
	resolver, _ := searchTree(t, map[string]string{"a.go": "x\n"})
	if _, err := NewGrepTool(resolver).Execute(context.Background(), map[string]any{"pattern": "([unclosed"}); err == nil {
		t.Fatal("an invalid regular expression must be reported")
	}
}

func TestSearchToolsRequireAPattern(t *testing.T) {
	resolver, _ := searchTree(t, map[string]string{"a.go": "x\n"})
	if _, err := NewGlobTool(resolver).Execute(context.Background(), map[string]any{}); err == nil {
		t.Error("glob without a pattern must fail")
	}
	if _, err := NewGrepTool(resolver).Execute(context.Background(), map[string]any{}); err == nil {
		t.Error("grep without a pattern must fail")
	}
}
