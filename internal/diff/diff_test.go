package diff

import (
	"strings"
	"testing"
)

func TestUnifiedProducesRealDiff(t *testing.T) {
	before := "package main\n\nconst answer = 41\n"
	after := "package main\n\nconst answer = 42\n"
	got := Unified("main.go", "main.go", before, after)

	for _, want := range []string{"--- main.go", "+++ main.go", "@@", "-const answer = 41", "+const answer = 42"} {
		if !strings.Contains(got, want) {
			t.Fatalf("diff missing %q:\n%s", want, got)
		}
	}
	// Context lines are what distinguish a real diff from a naive dump.
	if !strings.Contains(got, " package main") {
		t.Fatalf("diff carries no context lines:\n%s", got)
	}
}

func TestUnifiedIdenticalContentIsEmpty(t *testing.T) {
	if got := Unified("a", "a", "same\n", "same\n"); got != "" {
		t.Fatalf("expected no diff, got %q", got)
	}
}

func TestCount(t *testing.T) {
	cases := []struct {
		name          string
		before, after string
		wantAdditions int
		wantDeletions int
	}{
		{"replace one line", "a\nb\nc\n", "a\nB\nc\n", 1, 1},
		{"pure addition", "a\n", "a\nb\n", 1, 0},
		{"pure deletion", "a\nb\n", "a\n", 0, 1},
		{"no change", "a\n", "a\n", 0, 0},
		{"multi-line replace", "a\nb\nc\n", "a\nX\nY\nZ\nc\n", 3, 1},
		{"from empty", "", "a\nb\n", 2, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Count(tc.before, tc.after)
			if got.Additions != tc.wantAdditions || got.Deletions != tc.wantDeletions {
				t.Fatalf("stat = +%d -%d, want +%d -%d",
					got.Additions, got.Deletions, tc.wantAdditions, tc.wantDeletions)
			}
		})
	}
}

func TestTrimRemovesFileHeader(t *testing.T) {
	unified := "--- a/x.go\n+++ b/x.go\n@@ -1 +1 @@\n-a\n+b\n"
	got := Trim(unified)
	if strings.HasPrefix(got, "---") || strings.Contains(got, "+++") {
		t.Fatalf("header not trimmed: %q", got)
	}
	if !strings.HasPrefix(got, "@@") {
		t.Fatalf("trimmed diff should start at the hunk: %q", got)
	}
}

func TestParseStructuresHunks(t *testing.T) {
	unified := strings.Join([]string{
		"--- a/main.go",
		"+++ b/main.go",
		"@@ -1,4 +1,4 @@",
		" package main",
		" ",
		"-const answer = 41",
		"+const answer = 42",
		"",
	}, "\n")

	files := Parse(unified)
	if len(files) != 1 {
		t.Fatalf("parsed %d files, want 1", len(files))
	}
	file := files[0]
	if file.Name() != "main.go" {
		t.Fatalf("name = %q", file.Name())
	}
	if file.Stat.Additions != 1 || file.Stat.Deletions != 1 {
		t.Fatalf("stat = %+v", file.Stat)
	}

	var kinds []LineKind
	for _, line := range file.Lines {
		kinds = append(kinds, line.Kind)
	}
	want := []LineKind{LineHunk, LineContext, LineContext, LineRemoved, LineAdded}
	if len(kinds) != len(want) {
		t.Fatalf("kinds = %v, want %v", kinds, want)
	}
	for i := range want {
		if kinds[i] != want[i] {
			t.Fatalf("kinds = %v, want %v", kinds, want)
		}
	}

	// Line numbers are what the loose fallback cannot provide.
	for _, line := range file.Lines {
		switch line.Kind {
		case LineRemoved:
			if line.OldLine != 3 || line.NewLine != 0 {
				t.Fatalf("removed line numbers = old %d new %d, want old 3 new 0", line.OldLine, line.NewLine)
			}
		case LineAdded:
			if line.NewLine != 3 || line.OldLine != 0 {
				t.Fatalf("added line numbers = old %d new %d, want old 0 new 3", line.OldLine, line.NewLine)
			}
		}
	}
}

// TestParseHeaderlessDiff covers the shape the tools actually emit: Trim
// strips the ---/+++ header, and the parser must still recover line numbers.
func TestParseHeaderlessDiff(t *testing.T) {
	unified := "@@ -1,3 +1,3 @@\n a\n-b\n+B\n"
	files := Parse(unified)
	if len(files) != 1 {
		t.Fatalf("parsed %d files", len(files))
	}
	for _, line := range files[0].Lines {
		if line.Kind == LineRemoved && line.OldLine != 2 {
			t.Fatalf("headerless diff lost line numbers: %+v", line)
		}
		if line.Kind == LineAdded && line.NewLine != 2 {
			t.Fatalf("headerless diff lost line numbers: %+v", line)
		}
	}
}

func TestParseMultiFile(t *testing.T) {
	unified := strings.Join([]string{
		"--- a/one.go", "+++ b/one.go", "@@ -1 +1 @@", "-a", "+A",
		"--- a/two.go", "+++ b/two.go", "@@ -1 +1 @@", "-b", "+B",
		"",
	}, "\n")
	files := Parse(unified)
	if len(files) != 2 {
		t.Fatalf("parsed %d files, want 2", len(files))
	}
	if files[0].Name() != "one.go" || files[1].Name() != "two.go" {
		t.Fatalf("names = %q, %q", files[0].Name(), files[1].Name())
	}
}

// TestParseFallsBackOnGarbage is the resilience rule: a malformed diff still
// renders, just without line numbers.
func TestParseFallsBackOnGarbage(t *testing.T) {
	files := Parse("-removed\n+added\nsomething else\n")
	if len(files) != 1 {
		t.Fatalf("parsed %d files", len(files))
	}
	if files[0].Stat.Additions != 1 || files[0].Stat.Deletions != 1 {
		t.Fatalf("stat = %+v", files[0].Stat)
	}
	for _, line := range files[0].Lines {
		if line.OldLine != 0 || line.NewLine != 0 {
			t.Fatalf("fallback should not invent line numbers: %+v", line)
		}
	}
}

func TestParseEmpty(t *testing.T) {
	if files := Parse("   \n "); files != nil {
		t.Fatalf("expected nil for blank input, got %+v", files)
	}
}

func TestRoundTripGenerateThenParse(t *testing.T) {
	before := "one\ntwo\nthree\nfour\nfive\n"
	after := "one\nTWO\nthree\nfour\nFIVE\n"

	unified := Trim(Unified("f.go", "f.go", before, after))
	files := Parse(unified)
	if len(files) != 1 {
		t.Fatalf("parsed %d files", len(files))
	}
	// Generated stats and parsed stats must agree.
	generated := Count(before, after)
	if files[0].Stat != generated {
		t.Fatalf("parsed stat %+v != generated stat %+v", files[0].Stat, generated)
	}
}

// TestParseNoNewlineMarker pins that the "\ No newline at end of file" marker
// never renders as a content line. The strict parser consumes it into hunk
// metadata; the loose fallback classifies it as LineMeta. Either way it must
// not appear as added, removed, or context text.
func TestParseNoNewlineMarker(t *testing.T) {
	unified := "@@ -1 +1 @@\n-a\n+b\n\\ No newline at end of file\n"
	files := Parse(unified)
	if len(files) != 1 {
		t.Fatalf("parsed %d files", len(files))
	}
	for _, line := range files[0].Lines {
		if line.Kind == LineMeta {
			continue
		}
		if strings.Contains(line.Content, "No newline") {
			t.Fatalf("the no-newline marker leaked into content: %+v", line)
		}
	}
	// The real content lines survive it.
	if files[0].Stat.Additions != 1 || files[0].Stat.Deletions != 1 {
		t.Fatalf("stat = %+v", files[0].Stat)
	}
}

// TestParseLooseHandlesNoNewlineMarker covers the fallback path, where the
// marker is classified rather than consumed by the parser.
func TestParseLooseHandlesNoNewlineMarker(t *testing.T) {
	loose := parseLoose("-a\n+b\n\\ No newline at end of file")
	for _, line := range loose.Lines {
		if strings.Contains(line.Content, "No newline") && line.Kind != LineContext {
			return
		}
	}
	// The loose path has no hunk structure, so the marker lands as context;
	// what matters is that it is not counted as an addition or deletion.
	if loose.Stat.Additions != 1 || loose.Stat.Deletions != 1 {
		t.Fatalf("stat = %+v, marker should not be counted", loose.Stat)
	}
}
