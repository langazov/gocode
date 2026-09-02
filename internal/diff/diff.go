// Package diff generates and parses unified diffs.
//
// Generation uses github.com/aymanbagabas/go-udiff, the Go port of gopls'
// internal diff, which produces the same unified output the TypeScript tools
// return via createTwoFilesPatch. Parsing uses
// github.com/sourcegraph/go-diff, the canonical Go unified-diff parser, so
// the TUI can render a diff it received as text rather than scraping it.
//
// Diffs cross the wire as unified text, matching the TypeScript contract
// (tool metadata carries a `diff` string), so both ends stay interoperable.
package diff

import (
	"strconv"
	"strings"

	udiff "github.com/aymanbagabas/go-udiff"
	godiff "github.com/sourcegraph/go-diff/diff"
)

// Stat counts the lines a change adds and removes.
type Stat struct {
	Additions int `json:"additions"`
	Deletions int `json:"deletions"`
}

// Unified renders a unified diff between two file contents. Both labels are
// usually the same path, matching createTwoFilesPatch(path, path, old, new).
func Unified(oldLabel, newLabel, oldContent, newContent string) string {
	if oldContent == newContent {
		return ""
	}
	return udiff.Unified(oldLabel, newLabel, oldContent, newContent)
}

// Count reports how many lines a change adds and removes, without rendering.
func Count(oldContent, newContent string) Stat {
	var stat Stat
	for _, edit := range udiff.Lines(oldContent, newContent) {
		stat.Deletions += countLines(oldContent[edit.Start:edit.End])
		stat.Additions += countLines(edit.New)
	}
	return stat
}

// countLines counts lines in a diff fragment. udiff aligns edits to line
// boundaries, so a fragment is newline-terminated and the trailing newline
// does not imply an extra empty line.
func countLines(fragment string) int {
	if fragment == "" {
		return 0
	}
	return strings.Count(strings.TrimSuffix(fragment, "\n"), "\n") + 1
}

// Trim removes the ---/+++ file header from a unified diff, leaving the hunks.
// Tools name the file separately, so repeating it is noise. Ports trimDiff
// from packages/opencode/src/tool/edit.ts.
func Trim(unified string) string {
	lines := strings.Split(unified, "\n")
	start := 0
	for start < len(lines) {
		line := lines[start]
		if strings.HasPrefix(line, "---") || strings.HasPrefix(line, "+++") ||
			strings.HasPrefix(line, "diff ") || strings.HasPrefix(line, "index ") {
			start++
			continue
		}
		break
	}
	return strings.Join(lines[start:], "\n")
}

// LineKind is the role of a line within a rendered diff.
type LineKind int

const (
	// LineContext is unchanged, shown for orientation.
	LineContext LineKind = iota
	// LineAdded is present only in the new content.
	LineAdded
	// LineRemoved is present only in the old content.
	LineRemoved
	// LineHunk is an @@ header.
	LineHunk
	// LineMeta is a file header or other non-content line.
	LineMeta
)

// Line is one line of a parsed diff with the line numbers it maps to. A
// number is zero when the line does not exist on that side.
type Line struct {
	Kind    LineKind
	Content string
	OldLine int
	NewLine int
}

// File is one file's parsed diff.
type File struct {
	OldName string
	NewName string
	Lines   []Line
	Stat    Stat
}

// Name is the path to show, preferring the new name and stripping git's a//b/
// prefixes.
func (f File) Name() string {
	name := strings.TrimPrefix(f.NewName, "b/")
	if name == "" || name == "/dev/null" {
		name = strings.TrimPrefix(f.OldName, "a/")
	}
	return name
}

// Parse reads unified diff text into per-file line models for rendering.
//
// A diff the strict parser rejects is not worth failing a render over: the
// fallback classifies lines by prefix so a partial or hand-written diff still
// displays, just without line numbers.
func Parse(unified string) []File {
	if strings.TrimSpace(unified) == "" {
		return nil
	}
	parsed, err := godiff.ParseMultiFileDiff([]byte(unified))
	if err != nil || len(parsed) == 0 {
		// Tool output is trimmed of its ---/+++ header (the file is named
		// separately), which the strict parser needs in order to attribute
		// hunks. Restore a synthetic one so headerless diffs still parse with
		// real line numbers instead of falling through to the loose path.
		if strings.HasPrefix(strings.TrimLeft(unified, "\n"), "@@") {
			withHeader := "--- a\n+++ b\n" + unified
			if retried, retryErr := godiff.ParseMultiFileDiff([]byte(withHeader)); retryErr == nil && len(retried) > 0 {
				parsed = retried
				err = nil
			}
		}
	}
	if err != nil || len(parsed) == 0 {
		if fallback := parseLoose(unified); len(fallback.Lines) > 0 {
			return []File{fallback}
		}
		return nil
	}

	out := make([]File, 0, len(parsed))
	for _, file := range parsed {
		entry := File{OldName: file.OrigName, NewName: file.NewName}
		for _, hunk := range file.Hunks {
			entry.Lines = append(entry.Lines, Line{Kind: LineHunk, Content: hunkHeader(hunk)})
			oldLine := int(hunk.OrigStartLine)
			newLine := int(hunk.NewStartLine)
			for _, raw := range splitHunkBody(string(hunk.Body)) {
				switch {
				case strings.HasPrefix(raw, "+"):
					entry.Lines = append(entry.Lines, Line{Kind: LineAdded, Content: raw[1:], NewLine: newLine})
					entry.Stat.Additions++
					newLine++
				case strings.HasPrefix(raw, "-"):
					entry.Lines = append(entry.Lines, Line{Kind: LineRemoved, Content: raw[1:], OldLine: oldLine})
					entry.Stat.Deletions++
					oldLine++
				case strings.HasPrefix(raw, `\`):
					// "\ No newline at end of file" is metadata, not content.
					entry.Lines = append(entry.Lines, Line{Kind: LineMeta, Content: raw})
				default:
					entry.Lines = append(entry.Lines, Line{
						Kind: LineContext, Content: strings.TrimPrefix(raw, " "),
						OldLine: oldLine, NewLine: newLine,
					})
					oldLine++
					newLine++
				}
			}
		}
		out = append(out, entry)
	}
	return out
}

// splitHunkBody splits a hunk body into lines, dropping the trailing empty
// element a newline-terminated body produces.
func splitHunkBody(body string) []string {
	lines := strings.Split(body, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

func hunkHeader(hunk *godiff.Hunk) string {
	header := "@@ -" + strconv.Itoa(int(hunk.OrigStartLine)) + "," + strconv.Itoa(int(hunk.OrigLines)) +
		" +" + strconv.Itoa(int(hunk.NewStartLine)) + "," + strconv.Itoa(int(hunk.NewLines)) + " @@"
	if hunk.Section != "" {
		header += " " + hunk.Section
	}
	return header
}

// parseLoose classifies lines by prefix when the strict parser cannot read the
// input. Line numbers are unavailable, so they stay zero.
func parseLoose(unified string) File {
	var out File
	for _, raw := range strings.Split(strings.TrimRight(unified, "\n"), "\n") {
		switch {
		case strings.HasPrefix(raw, "@@"):
			out.Lines = append(out.Lines, Line{Kind: LineHunk, Content: raw})
		case strings.HasPrefix(raw, "+++") || strings.HasPrefix(raw, "---") ||
			strings.HasPrefix(raw, "diff ") || strings.HasPrefix(raw, "index "):
			out.Lines = append(out.Lines, Line{Kind: LineMeta, Content: raw})
		case strings.HasPrefix(raw, "+"):
			out.Lines = append(out.Lines, Line{Kind: LineAdded, Content: raw[1:]})
			out.Stat.Additions++
		case strings.HasPrefix(raw, "-"):
			out.Lines = append(out.Lines, Line{Kind: LineRemoved, Content: raw[1:]})
			out.Stat.Deletions++
		default:
			out.Lines = append(out.Lines, Line{Kind: LineContext, Content: strings.TrimPrefix(raw, " ")})
		}
	}
	return out
}
