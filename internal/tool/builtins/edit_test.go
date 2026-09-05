package builtins

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// These port the behaviours packages/opencode/test/tool/edit.test.ts asserts,
// for the parts this port implements. Two deliberate differences, both
// asserted below rather than left implicit:
//
//   - Upstream's edit creates a new file when oldString is empty. This port
//     refuses and points at the write tool (see TestEditRefusesToCreateFiles).
//   - Upstream has a chain of fuzzy replacers (block-anchor, whitespace
//     insensitive, and so on) whose tests have no counterpart here, because
//     this port matches exactly and nothing else. See specs/go-port-gaps.md.

// editFixture writes content to name under a fresh root and returns the tool
// and the absolute path, so a test reads back exactly what was written —
// bytes, not lines.
func editFixture(t *testing.T, name, content string) (*EditTool, string) {
	t.Helper()
	resolver, root := newRoot(t)
	target := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return NewEditTool(resolver), target
}

func readRaw(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// The line-ending matrix. A model writes LF because that is what it has seen
// most of; the file on disk may not. Every combination of what the file uses
// and what the arguments use has to come out as the file's own ending, or an
// edit rewrites every line of a CRLF file and the diff is unreadable.
func TestEditPreservesTheFilesLineEndings(t *testing.T) {
	for _, tc := range []struct {
		name      string
		file      string
		oldString string
		newString string
		want      string
	}{
		{
			name: "CRLF file, LF arguments",
			file: "foo\r\nbar\r\n", oldString: "foo\nbar", newString: "baz\nqux",
			want: "baz\r\nqux\r\n",
		},
		{
			name: "CRLF file, LF newString only",
			file: "foo\r\nbar\r\n", oldString: "foo\r\nbar", newString: "baz\nqux",
			want: "baz\r\nqux\r\n",
		},
		{
			name: "CRLF file, CRLF arguments",
			file: "foo\r\nbar\r\n", oldString: "foo\r\nbar", newString: "baz\r\nqux",
			want: "baz\r\nqux\r\n",
		},
		{
			name: "LF file, CRLF arguments",
			file: "foo\nbar\n", oldString: "foo\r\nbar", newString: "baz\r\nqux",
			want: "baz\nqux\n",
		},
		{
			name: "LF file, CRLF newString only",
			file: "foo\nbar\n", oldString: "foo\nbar", newString: "baz\r\nqux",
			want: "baz\nqux\n",
		},
		{
			name: "LF file, LF arguments",
			file: "foo\nbar\n", oldString: "foo\nbar", newString: "baz\nqux",
			want: "baz\nqux\n",
		},
		{
			name: "CRLF file, multi-line block",
			file: "a\r\nb\r\nc\r\nd\r\n", oldString: "b\nc", newString: "x\ny\nz",
			want: "a\r\nx\r\ny\r\nz\r\nd\r\n",
		},
		{
			name: "LF file, multi-line block",
			file: "a\nb\nc\nd\n", oldString: "b\r\nc", newString: "x\r\ny\r\nz",
			want: "a\nx\ny\nz\nd\n",
		},
		{
			// A single-line edit must not convert the rest of the file.
			name: "CRLF file, single-line edit leaves the rest alone",
			file: "keep\r\nchange\r\nkeep\r\n", oldString: "change", newString: "changed",
			want: "keep\r\nchanged\r\nkeep\r\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			edit, target := editFixture(t, "a.txt", tc.file)
			if _, err := edit.Execute(context.Background(), map[string]any{
				"path": target, "oldString": tc.oldString, "newString": tc.newString,
			}); err != nil {
				t.Fatal(err)
			}
			if got := readRaw(t, target); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// replaceAll goes through the same conversion, and every replacement has to
// land in the file's ending — not just the first.
func TestEditReplaceAllPreservesLineEndings(t *testing.T) {
	for _, tc := range []struct {
		name string
		file string
		want string
	}{
		{"CRLF", "x\r\ny\r\nsep\r\nx\r\ny\r\n", "A\r\nB\r\nsep\r\nA\r\nB\r\n"},
		{"LF", "x\ny\nsep\nx\ny\n", "A\nB\nsep\nA\nB\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			edit, target := editFixture(t, "a.txt", tc.file)
			// LF arguments against both, which is the case that matters.
			if _, err := edit.Execute(context.Background(), map[string]any{
				"path": target, "oldString": "x\ny", "newString": "A\nB", "replaceAll": true,
			}); err != nil {
				t.Fatal(err)
			}
			if got := readRaw(t, target); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestEditReplaceAllReplacesEveryOccurrence(t *testing.T) {
	edit, target := editFixture(t, "a.txt", "a\na\na\n")
	out, err := edit.Execute(context.Background(), map[string]any{
		"path": target, "oldString": "a", "newString": "b", "replaceAll": true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := readRaw(t, target); got != "b\nb\nb\n" {
		t.Fatalf("got %q, want all three replaced", got)
	}
	if !strings.Contains(out, "3") {
		t.Errorf("the output should report how many replacements were made, got %q", out)
	}
}

// A byte-order mark is invisible to the model, so it must survive an edit
// untouched — and it must not stop oldString matching the first visible line,
// which is where a BOM would otherwise hide.
func TestEditPreservesBOM(t *testing.T) {
	const bom = "\ufeff"

	t.Run("kept when editing a later line", func(t *testing.T) {
		edit, target := editFixture(t, "a.cs", bom+"using System;\nclass A {}\n")
		if _, err := edit.Execute(context.Background(), map[string]any{
			"path": target, "oldString": "class A {}", "newString": "class B {}",
		}); err != nil {
			t.Fatal(err)
		}
		if got := readRaw(t, target); got != bom+"using System;\nclass B {}\n" {
			t.Fatalf("got %q", got)
		}
	})

	t.Run("first visible line is still matchable", func(t *testing.T) {
		edit, target := editFixture(t, "a.cs", bom+"using System;\n")
		if _, err := edit.Execute(context.Background(), map[string]any{
			"path": target, "oldString": "using System;", "newString": "using Other;",
		}); err != nil {
			t.Fatalf("the BOM must not hide the first line: %v", err)
		}
		if got := readRaw(t, target); got != bom+"using Other;\n" {
			t.Fatalf("got %q", got)
		}
	})

	t.Run("BOM and CRLF together", func(t *testing.T) {
		edit, target := editFixture(t, "a.cs", bom+"using System;\r\nclass A {}\r\n")
		if _, err := edit.Execute(context.Background(), map[string]any{
			"path": target, "oldString": "class A {}", "newString": "class B {}",
		}); err != nil {
			t.Fatal(err)
		}
		if got := readRaw(t, target); got != bom+"using System;\r\nclass B {}\r\n" {
			t.Fatalf("got %q", got)
		}
	})
}

// Every refusal leaves the file byte-for-byte as it was. Asserting the error
// alone would pass a tool that wrote first and complained afterwards.
func TestEditRefusalsLeaveTheFileUnchanged(t *testing.T) {
	const original = "hello world\n"
	for _, tc := range []struct {
		name      string
		oldString string
		newString string
		want      string
	}{
		{"identical strings", "hello", "hello", "identical"},
		{"empty oldString", "", "anything", "must not be empty"},
		{"oldString not present", "goodbye", "hello", "Could not find oldString"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			edit, target := editFixture(t, "a.txt", original)
			_, err := edit.Execute(context.Background(), map[string]any{
				"path": target, "oldString": tc.oldString, "newString": tc.newString,
			})
			if err == nil {
				t.Fatal("expected an error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %q, want it to mention %q", err, tc.want)
			}
			if got := readRaw(t, target); got != original {
				t.Errorf("a refused edit rewrote the file: %q", got)
			}
		})
	}
}

// Ambiguity is refused rather than guessed at: replacing the first of several
// identical matches silently is how an edit lands in the wrong place.
func TestEditRefusesAmbiguousMatchWithoutReplaceAll(t *testing.T) {
	const original = "dup\ndup\n"
	edit, target := editFixture(t, "a.txt", original)
	_, err := edit.Execute(context.Background(), map[string]any{
		"path": target, "oldString": "dup", "newString": "one",
	})
	if err == nil || !strings.Contains(err.Error(), "multiple exact matches") {
		t.Fatalf("expected a multiple-match refusal, got %v", err)
	}
	if got := readRaw(t, target); got != original {
		t.Fatalf("a refused edit rewrote the file: %q", got)
	}
}

func TestEditRequiresAnExistingFile(t *testing.T) {
	resolver, root := newRoot(t)
	edit := NewEditTool(resolver)

	_, err := edit.Execute(context.Background(), map[string]any{
		"path": filepath.Join(root, "missing.txt"), "oldString": "a", "newString": "b",
	})
	if err == nil || !strings.Contains(err.Error(), "Unable to edit") {
		t.Fatalf("expected a missing-file error, got %v", err)
	}

	// A directory is not a file, and reading one has to fail rather than
	// produce an empty string that then "does not contain oldString".
	if err := os.Mkdir(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err = edit.Execute(context.Background(), map[string]any{
		"path": filepath.Join(root, "sub"), "oldString": "a", "newString": "b",
	})
	if err == nil {
		t.Fatal("editing a directory must fail")
	}
}

// A deliberate difference from upstream, pinned so it is a decision rather
// than a bug: upstream's edit creates a file when oldString is empty, this
// port sends the caller to write.
func TestEditRefusesToCreateFiles(t *testing.T) {
	resolver, root := newRoot(t)
	edit := NewEditTool(resolver)
	target := filepath.Join(root, "new.txt")

	_, err := edit.Execute(context.Background(), map[string]any{
		"path": target, "oldString": "", "newString": "content",
	})
	if err == nil || !strings.Contains(err.Error(), "write") {
		t.Fatalf("expected to be pointed at the write tool, got %v", err)
	}
	if _, statErr := os.Stat(target); statErr == nil {
		t.Fatal("edit created a file")
	}
}

func TestEditRefusesPathsOutsideTheRoot(t *testing.T) {
	resolver, _ := newRoot(t)
	edit := NewEditTool(resolver)
	_, err := edit.Execute(context.Background(), map[string]any{
		"path": "../escaped.txt", "oldString": "a", "newString": "b",
	})
	if err == nil || !strings.Contains(err.Error(), "escapes working directory") {
		t.Fatalf("expected the sandbox to refuse, got %v", err)
	}
}
