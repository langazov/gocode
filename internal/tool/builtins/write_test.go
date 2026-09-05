package builtins

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Ported from packages/opencode/test/tool/write.test.ts, for the parts this
// port implements. Upstream additionally asserts a generated title and file
// permissions on sensitive content; neither exists here.

func writeFixture(t *testing.T) (*WriteTool, string) {
	t.Helper()
	resolver, root := newRoot(t)
	return NewWriteTool(resolver), root
}

// Content goes to disk byte for byte. A tool that trims, normalises line
// endings or appends a newline corrupts anything it is asked to write —
// which is why each of these compares bytes rather than lines.
func TestWriteContentIsExact(t *testing.T) {
	for _, tc := range []struct {
		name    string
		content string
	}{
		{"empty", ""},
		{"single line without a trailing newline", "no trailing newline"},
		{"multi-line", "one\ntwo\nthree\n"},
		{"CRLF is not normalised away", "one\r\ntwo\r\n"},
		{"JSON", "{\n  \"a\": 1,\n  \"b\": [2, 3]\n}\n"},
		{"leading and trailing whitespace is kept", "  padded  \n\n"},
		{"non-ASCII", "héllo → wörld 🎉\n"},
		{"NUL and other control bytes survive", "a\x00b\x01c\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			write, root := writeFixture(t)
			if _, err := write.Execute(context.Background(), map[string]any{
				"path": "out.txt", "content": tc.content,
			}); err != nil {
				t.Fatal(err)
			}
			if got := readRaw(t, filepath.Join(root, "out.txt")); got != tc.content {
				t.Fatalf("got %q, want %q", got, tc.content)
			}
		})
	}
}

func TestWriteCreatesParentDirectories(t *testing.T) {
	write, root := writeFixture(t)
	if _, err := write.Execute(context.Background(), map[string]any{
		"path": "a/b/c/deep.txt", "content": "nested",
	}); err != nil {
		t.Fatal(err)
	}
	if got := readRaw(t, filepath.Join(root, "a", "b", "c", "deep.txt")); got != "nested" {
		t.Fatalf("got %q", got)
	}
}

// The verb distinguishes the two cases for the reader of the transcript, and
// an overwrite replaces rather than appends or merges.
func TestWriteReportsCreatedVersusOverwritten(t *testing.T) {
	write, root := writeFixture(t)

	out, err := write.Execute(context.Background(), map[string]any{"path": "f.txt", "content": "first"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Created") {
		t.Errorf("a new file should report Created, got %q", out)
	}

	out, err = write.Execute(context.Background(), map[string]any{"path": "f.txt", "content": "second"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Wrote") {
		t.Errorf("an existing file should report Wrote, got %q", out)
	}
	// Shorter than the original: a truncating write, not an in-place patch
	// that would leave the tail of the old content behind.
	if got := readRaw(t, filepath.Join(root, "f.txt")); got != "second" {
		t.Fatalf("overwrite left %q", got)
	}
}

func TestWriteRefusals(t *testing.T) {
	write, root := writeFixture(t)

	if _, err := write.Execute(context.Background(), map[string]any{"content": "x"}); err == nil {
		t.Error("a write with no path must fail")
	}

	for _, escaping := range []string{"../escaped.txt", "/etc/passwd"} {
		if _, err := write.Execute(context.Background(), map[string]any{
			"path": escaping, "content": "nope",
		}); err == nil || !strings.Contains(err.Error(), "escapes working directory") {
			t.Errorf("writing %s must be refused, got %v", escaping, err)
		}
	}

	// A path whose parent exists as a *file* cannot be created; the tool has
	// to fail rather than report success on a write that never happened.
	if err := os.WriteFile(filepath.Join(root, "afile"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := write.Execute(context.Background(), map[string]any{
		"path": "afile/child.txt", "content": "nope",
	}); err == nil {
		t.Error("writing under a regular file must fail")
	}
}

// An absolute path inside the root is the same file as the relative one; a
// model that has read a file gets an absolute path back and writes with it.
func TestWriteAcceptsAbsolutePathsInsideTheRoot(t *testing.T) {
	write, root := writeFixture(t)
	target := filepath.Join(root, "abs.txt")
	if _, err := write.Execute(context.Background(), map[string]any{
		"path": target, "content": "by absolute path",
	}); err != nil {
		t.Fatal(err)
	}
	if got := readRaw(t, target); got != "by absolute path" {
		t.Fatalf("got %q", got)
	}
}
