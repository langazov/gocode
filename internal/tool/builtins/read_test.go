package builtins

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Ported from packages/opencode/test/tool/read.test.ts, for the parts this
// port implements. Upstream additionally covers image reads, binary
// detection, a suggestion list for a missing path, and byte-budget
// truncation; this port truncates by line count and by line length only.

func readFixture(t *testing.T, name, content string) (*ReadTool, string) {
	t.Helper()
	resolver, root := newRoot(t)
	target := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return NewReadTool(resolver), root
}

func mustRead(t *testing.T, read *ReadTool, input map[string]any) string {
	t.Helper()
	out, err := read.Execute(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// Line numbers are the whole point of the format: they are what an edit or a
// diagnostic is later expressed against, so they must be absolute file lines,
// not offsets into the window that was returned.
func TestReadNumbersLinesFromTheFileNotTheWindow(t *testing.T) {
	read, _ := readFixture(t, "a.txt", "one\ntwo\nthree\nfour\nfive\n")

	out := mustRead(t, read, map[string]any{"path": "a.txt", "offset": 3, "limit": 2})
	if out != "3: three\n4: four" {
		t.Fatalf("got %q", out)
	}
}

func TestReadWindowing(t *testing.T) {
	var body strings.Builder
	for i := 1; i <= 10; i++ {
		fmt.Fprintf(&body, "line%d\n", i)
	}
	read, _ := readFixture(t, "a.txt", body.String())

	for _, tc := range []struct {
		name      string
		input     map[string]any
		wantFirst string
		wantLast  string
		wantLines int
	}{
		{"whole file by default", map[string]any{"path": "a.txt"}, "1: line1", "10: line10", 10},
		{"limit only", map[string]any{"path": "a.txt", "limit": 3}, "1: line1", "3: line3", 3},
		{"offset only", map[string]any{"path": "a.txt", "offset": 8}, "8: line8", "10: line10", 3},
		{"offset and limit", map[string]any{"path": "a.txt", "offset": 4, "limit": 2}, "4: line4", "5: line5", 2},
		// A window running off the end returns what exists rather than failing.
		{"limit past the end", map[string]any{"path": "a.txt", "offset": 9, "limit": 100}, "9: line9", "10: line10", 2},
		// Nonsense values fall back to the defaults instead of returning
		// nothing, which is what an off-by-one in the caller looks like.
		{"zero offset", map[string]any{"path": "a.txt", "offset": 0, "limit": 2}, "1: line1", "2: line2", 2},
		{"negative limit", map[string]any{"path": "a.txt", "limit": -5}, "1: line1", "10: line10", 10},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lines := strings.Split(mustRead(t, read, tc.input), "\n")
			if len(lines) != tc.wantLines {
				t.Fatalf("got %d lines, want %d: %q", len(lines), tc.wantLines, lines)
			}
			if lines[0] != tc.wantFirst {
				t.Errorf("first line = %q, want %q", lines[0], tc.wantFirst)
			}
			if lines[len(lines)-1] != tc.wantLast {
				t.Errorf("last line = %q, want %q", lines[len(lines)-1], tc.wantLast)
			}
		})
	}
}

// An offset past the end is a caller error worth reporting: returning an empty
// string reads as "the file is empty", and the model retries the same call.
func TestReadOffsetPastEndOfFileFails(t *testing.T) {
	read, _ := readFixture(t, "a.txt", "one\ntwo\n")
	if _, err := read.Execute(context.Background(), map[string]any{"path": "a.txt", "offset": 50}); err == nil {
		t.Fatal("expected an out-of-range error")
	}
}

// A minified bundle is one enormous line. Without a cap it would fill the
// context window on its own, so the line is cut and said to be cut.
func TestReadTruncatesOverlongLines(t *testing.T) {
	long := strings.Repeat("x", maxLineLength+500)
	read, _ := readFixture(t, "a.txt", "short\n"+long+"\nalso short\n")

	out := mustRead(t, read, map[string]any{"path": "a.txt"})
	lines := strings.Split(out, "\n")
	if len(lines) != 3 {
		t.Fatalf("expected 3 lines, got %d", len(lines))
	}
	if !strings.HasSuffix(lines[1], "(line truncated)") {
		t.Errorf("an overlong line must say it was truncated, got %q", lines[1][:60])
	}
	if strings.Contains(lines[1], strings.Repeat("x", maxLineLength+1)) {
		t.Error("the line was not actually cut")
	}
	// The lines around it are untouched.
	if lines[0] != "1: short" || lines[2] != "3: also short" {
		t.Errorf("neighbouring lines changed: %q, %q", lines[0], lines[2])
	}
}

// The default limit is what stops a large file arriving whole. Reading past it
// requires an explicit offset, which is the paging contract.
func TestReadStopsAtTheDefaultLimit(t *testing.T) {
	var body strings.Builder
	for i := 1; i <= defaultReadLimit+50; i++ {
		fmt.Fprintf(&body, "line%d\n", i)
	}
	read, _ := readFixture(t, "big.txt", body.String())

	lines := strings.Split(mustRead(t, read, map[string]any{"path": "big.txt"}), "\n")
	if len(lines) != defaultReadLimit {
		t.Fatalf("got %d lines, want the default limit of %d", len(lines), defaultReadLimit)
	}
	if !strings.HasPrefix(lines[len(lines)-1], fmt.Sprintf("%d: ", defaultReadLimit)) {
		t.Errorf("last line = %q", lines[len(lines)-1])
	}

	// The rest is reachable by paging.
	next := mustRead(t, read, map[string]any{"path": "big.txt", "offset": defaultReadLimit + 1})
	if !strings.HasPrefix(next, fmt.Sprintf("%d: line%d", defaultReadLimit+1, defaultReadLimit+1)) {
		t.Errorf("paging past the limit returned %q", next[:40])
	}
}

// A directory reads as its sorted entries, with directories marked, so the
// model can tell a directory from a file without a second call.
func TestReadDirectoryListing(t *testing.T) {
	resolver, root := newRoot(t)
	for _, name := range []string{"b.txt", "a.txt", "c.txt"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	read := NewReadTool(resolver)

	out := mustRead(t, read, map[string]any{"path": "."})
	if out != "a.txt\nb.txt\nc.txt\nsub/" {
		t.Fatalf("got %q, want sorted entries with the directory marked", out)
	}

	// Paging applies to directories too.
	if got := mustRead(t, read, map[string]any{"path": ".", "offset": 2, "limit": 2}); got != "b.txt\nc.txt" {
		t.Fatalf("directory paging returned %q", got)
	}
	if _, err := read.Execute(context.Background(), map[string]any{"path": ".", "offset": 99}); err == nil {
		t.Error("an offset past the last entry must fail")
	}
}

func TestReadRefusals(t *testing.T) {
	resolver, _ := newRoot(t)
	read := NewReadTool(resolver)

	if _, err := read.Execute(context.Background(), map[string]any{}); err == nil {
		t.Error("a read with no path must fail")
	}
	if _, err := read.Execute(context.Background(), map[string]any{"path": "missing.txt"}); err == nil {
		t.Error("reading a missing file must fail")
	}
	if _, err := read.Execute(context.Background(), map[string]any{"path": "../outside.txt"}); err == nil ||
		!strings.Contains(err.Error(), "escapes working directory") {
		t.Error("reading outside the root must be refused")
	}
}

// A file with no trailing newline still reports its last line, and an empty
// file reads as empty rather than erroring.
func TestReadEdgeCaseFiles(t *testing.T) {
	read, _ := readFixture(t, "a.txt", "only line, no newline")
	if got := mustRead(t, read, map[string]any{"path": "a.txt"}); got != "1: only line, no newline" {
		t.Errorf("got %q", got)
	}

	empty, _ := readFixture(t, "empty.txt", "")
	if got := mustRead(t, empty, map[string]any{"path": "empty.txt"}); got != "" {
		t.Errorf("an empty file should read as empty, got %q", got)
	}
}
