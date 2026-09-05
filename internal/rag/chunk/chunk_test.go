package chunk

import (
	"context"
	"os"
	"path/filepath"
	"slices"
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

func TestWalkHonorsGitignore(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, ".gitignore", "node_modules/\n*.log\n")
	writeFile(t, root, "node_modules/lib/pkg.js", "module.exports = {}\n")
	writeFile(t, root, "debug.log", "boom\n")
	writeFile(t, root, "src/main.go", "package main\n")

	chunks, err := Walk(context.Background(), root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) != 1 || chunks[0].Path != "src/main.go" {
		t.Fatalf("got %+v, want only src/main.go", chunks)
	}
}

func TestWalkHonorsIgnoreFile(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, ".ignore", "scratch/\n")
	writeFile(t, root, "scratch/temp.txt", "throwaway\n")
	writeFile(t, root, "keep.txt", "hello\n")

	chunks, err := Walk(context.Background(), root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) != 1 || chunks[0].Path != "keep.txt" {
		t.Fatalf("got %+v, want only keep.txt", chunks)
	}
}

func TestWalkNestedGitignoreOverridesParentViaNegation(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, ".gitignore", "*.md\n")
	writeFile(t, root, "docs/.gitignore", "!keep.md\n")
	writeFile(t, root, "docs/keep.md", "kept\n")
	writeFile(t, root, "docs/drop.md", "dropped\n")
	writeFile(t, root, "top.md", "dropped too\n")

	chunks, err := Walk(context.Background(), root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) != 1 || chunks[0].Path != "docs/keep.md" {
		t.Fatalf("got %+v, want only docs/keep.md", chunks)
	}
}

func TestWalkGitignoreDirectoryCannotBeResurrectedFromInside(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, ".gitignore", "build/\n")
	writeFile(t, root, "build/.gitignore", "!keep.txt\n")
	writeFile(t, root, "build/keep.txt", "should still be skipped\n")

	chunks, err := Walk(context.Background(), root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) != 0 {
		t.Fatalf("got %+v, want none: an excluded directory's own .gitignore is never consulted", chunks)
	}
}

func TestWalkDisableGitignore(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, ".gitignore", "debug.txt\n")
	writeFile(t, root, "debug.txt", "boom\n")

	chunks, err := Walk(context.Background(), root, Options{DisableGitignore: true})
	if err != nil {
		t.Fatal(err)
	}
	var gotLog bool
	for _, c := range chunks {
		if c.Path == "debug.txt" {
			gotLog = true
		}
	}
	if !gotLog {
		t.Fatalf("got %+v, want debug.txt present (gitignore disabled)", chunks)
	}
}

func TestWalkScopedSubdirectoryHonorsAncestorGitignore(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, ".gitignore", "node_modules/\n")
	writeFile(t, root, "packages/app/node_modules/dep/index.js", "module.exports = {}\n")
	writeFile(t, root, "packages/app/main.go", "package main\n")

	sub := filepath.Join(root, "packages", "app")
	chunks, err := Walk(context.Background(), sub, Options{PathPrefix: "packages/app", IgnoreBase: root})
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) != 1 || chunks[0].Path != "packages/app/main.go" {
		t.Fatalf("got %+v, want only packages/app/main.go", chunks)
	}
}

func TestWalkGitignoreDoesNotExcludeExplicitRootItself(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, ".gitignore", "packages/\n")
	writeFile(t, root, "packages/app/main.go", "package main\n")

	sub := filepath.Join(root, "packages")
	chunks, err := Walk(context.Background(), sub, Options{PathPrefix: "packages", IgnoreBase: root})
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) != 1 || chunks[0].Path != "packages/app/main.go" {
		t.Fatalf("got %+v, want packages/app/main.go: an explicitly requested root shouldn't self-exclude", chunks)
	}
}

func TestWalkSkipsKnownBinaryExtensionsWithoutReadingContent(t *testing.T) {
	root := t.TempDir()
	// No NUL bytes in these "binaries" — looksBinary's content sniff alone
	// wouldn't catch them; the extension check must. Include is set
	// explicitly (bypassing the default text allowlist) so this exercises
	// isBinaryExt itself, not the allowlist that would exclude them anyway.
	writeFile(t, root, "logo.png", "not actually binary bytes but still a .png\n")
	writeFile(t, root, "archive.zip", "also not real zip bytes\n")
	writeFile(t, root, "main.go", "package main\n")

	chunks, err := Walk(context.Background(), root, Options{Include: []string{"**/*"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) != 1 || chunks[0].Path != "main.go" {
		t.Fatalf("got %+v, want only main.go", chunks)
	}
}

func TestWalkDefaultOnlyIndexesKnownTextFiles(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "src/main.go", "package main\n")
	writeFile(t, root, "docs/guide.md", "# Guide\n")
	writeFile(t, root, "icons/logo.svg", "<svg></svg>\n")
	writeFile(t, root, "bun.lock", "{}\n")
	writeFile(t, root, "data.bin", "\x00not text either\n")

	chunks, err := Walk(context.Background(), root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, c := range chunks {
		got = append(got, c.Path)
	}
	if len(chunks) != 2 || !slices.Contains(got, "src/main.go") || !slices.Contains(got, "docs/guide.md") {
		t.Fatalf("got %v, want only src/main.go and docs/guide.md: .svg and .lock aren't code or docs", got)
	}
}

func TestWalkCapsLargeJSONFilesBelowGeneralMaxFileBytes(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "package.json", `{"name":"ok"}`+"\n")
	writeFile(t, root, "fixtures/huge.json", strings.Repeat("x", 100*1024))

	chunks, err := Walk(context.Background(), root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) != 1 || chunks[0].Path != "package.json" {
		t.Fatalf("got %+v, want only package.json: a 100KB JSON fixture should be skipped even though it's under the 1MB general cap", chunks)
	}
}

func TestWalkIncludeExplicitlyBypassesDefaultAllowlist(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "icons/logo.svg", "<svg></svg>\n")
	writeFile(t, root, "src/main.go", "package main\n")

	chunks, err := Walk(context.Background(), root, Options{Include: []string{"**/*.svg"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) != 1 || chunks[0].Path != "icons/logo.svg" {
		t.Fatalf("got %+v, want only icons/logo.svg: an explicit Include should override the default text-only allowlist", chunks)
	}
}

func TestWalkDefaultIncludesWellKnownExtensionlessFiles(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "Dockerfile", "FROM scratch\n")
	writeFile(t, root, "Makefile", "build:\n\techo hi\n")

	chunks, err := Walk(context.Background(), root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, c := range chunks {
		got = append(got, c.Path)
	}
	if len(chunks) != 2 || !slices.Contains(got, "Dockerfile") || !slices.Contains(got, "Makefile") {
		t.Fatalf("got %v, want Dockerfile and Makefile", got)
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
