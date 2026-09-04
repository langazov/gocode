package chunk

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, root, rel, content string) {
	t.Helper()
	path := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestWalkSplitsIntoOverlappingWindows(t *testing.T) {
	root := t.TempDir()
	var lines []string
	for i := 1; i <= 25; i++ {
		lines = append(lines, "line"+itoa(i))
	}
	writeFile(t, root, "a.go", strings.Join(lines, "\n"))

	chunks, err := Walk(context.Background(), root, Options{Lines: 10, Overlap: 2})
	if err != nil {
		t.Fatal(err)
	}
	// step = 8: windows [1,10] [9,18] [17,25]
	want := [][2]int{{1, 10}, {9, 18}, {17, 25}}
	if len(chunks) != len(want) {
		t.Fatalf("got %d chunks, want %d: %+v", len(chunks), len(want), chunks)
	}
	for i, w := range want {
		if chunks[i].StartLine != w[0] || chunks[i].EndLine != w[1] {
			t.Errorf("chunk %d: got [%d,%d], want [%d,%d]", i, chunks[i].StartLine, chunks[i].EndLine, w[0], w[1])
		}
	}
	// Overlap: the last 2 lines of chunk 0 equal the first 2 of chunk 1.
	c0 := strings.Split(chunks[0].Content, "\n")
	c1 := strings.Split(chunks[1].Content, "\n")
	if c0[len(c0)-2] != c1[0] || c0[len(c0)-1] != c1[1] {
		t.Errorf("overlap mismatch: chunk0 tail %v, chunk1 head %v", c0[len(c0)-2:], c1[:2])
	}
}

func TestWalkIncludeExclude(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "src/main.go", "package main\n")
	writeFile(t, root, "vendor/lib/thing.go", "package lib\n")
	writeFile(t, root, "README.md", "# hi\n")

	chunks, err := Walk(context.Background(), root, Options{
		Include: []string{"**/*.go"},
		Exclude: []string{"vendor/**"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) != 1 {
		t.Fatalf("got %d chunks, want 1: %+v", len(chunks), chunks)
	}
	if chunks[0].Path != "src/main.go" {
		t.Errorf("got path %q, want src/main.go", chunks[0].Path)
	}
}

func TestWalkSkipsBinaryAndGitDir(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "bin.dat", "\x00\x01\x02binary")
	writeFile(t, root, ".git/HEAD", "ref: refs/heads/main\n")
	writeFile(t, root, "ok.txt", "hello\n")

	chunks, err := Walk(context.Background(), root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) != 1 || chunks[0].Path != "ok.txt" {
		t.Fatalf("got %+v, want only ok.txt", chunks)
	}
}

func TestChunkIDsStableAcrossRuns(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "a.txt", "one\ntwo\nthree\n")

	first, err := Walk(context.Background(), root, Options{Lines: 10, Overlap: 2})
	if err != nil {
		t.Fatal(err)
	}
	second, err := Walk(context.Background(), root, Options{Lines: 10, Overlap: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 1 || len(second) != 1 {
		t.Fatalf("expected single chunk, got %d and %d", len(first), len(second))
	}
	if first[0].ID != second[0].ID {
		t.Errorf("chunk ID not stable: %s vs %s", first[0].ID, second[0].ID)
	}
	if first[0].ContentHash != second[0].ContentHash {
		t.Errorf("content hash not stable")
	}
}

func TestContentHashChangesWithContentSameLineCount(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "a.txt", "one\ntwo\n")
	before, err := Walk(context.Background(), root, Options{Lines: 10, Overlap: 2})
	if err != nil {
		t.Fatal(err)
	}
	// Same line count and window boundaries, different content: the chunk ID
	// (addressed on path+line range) must stay stable while the content hash
	// (used to decide whether re-embedding is needed) must change.
	writeFile(t, root, "a.txt", "one\nTWO-EDITED\n")
	after, err := Walk(context.Background(), root, Options{Lines: 10, Overlap: 2})
	if err != nil {
		t.Fatal(err)
	}
	if before[0].ID != after[0].ID {
		t.Fatalf("chunk ID should stay stable when the window boundaries don't move: %s vs %s", before[0].ID, after[0].ID)
	}
	if before[0].ContentHash == after[0].ContentHash {
		t.Errorf("content hash should change when content changes")
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	digits := []byte{}
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}
