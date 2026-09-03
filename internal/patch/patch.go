// Package patch parses and applies gocode's file-oriented patch format,
// porting packages/core/src/patch.ts.
//
// The format is a stripped-down envelope:
//
//	*** Begin Patch
//	*** Add File: <path>      (every following line is a + line)
//	*** Delete File: <path>   (nothing follows)
//	*** Update File: <path>   (optionally *** Move to: <path>, then @@ chunks)
//	*** End Patch
package patch

import (
	"fmt"
	"regexp"
	"strings"
)

// HunkType is the operation a hunk performs.
type HunkType string

const (
	HunkAdd    HunkType = "add"
	HunkDelete HunkType = "delete"
	HunkUpdate HunkType = "update"
)

// Hunk is one file operation within a patch.
type Hunk struct {
	Type HunkType
	Path string
	// Contents is the new file body, set only for HunkAdd.
	Contents string
	// MovePath renames the file, set only for HunkUpdate with a Move to line.
	MovePath string
	// Chunks are the @@ sections, set only for HunkUpdate.
	Chunks []UpdateChunk
}

// UpdateChunk is one @@ section of an update hunk.
type UpdateChunk struct {
	OldLines []string
	NewLines []string
	// ChangeContext is the text after @@, used to anchor the search.
	ChangeContext string
	// EndOfFile anchors the chunk to the end of the file.
	EndOfFile bool
}

const (
	markerBegin  = "*** Begin Patch"
	markerEnd    = "*** End Patch"
	markerAdd    = "*** Add File:"
	markerDelete = "*** Delete File:"
	markerUpdate = "*** Update File:"
	markerMove   = "*** Move to:"
	markerEOF    = "*** End of File"
)

// heredocOpen matches the opening line of a shell heredoc, which models
// sometimes wrap a patch in. RE2 has no backreferences, so the closing
// delimiter is matched by hand in stripHeredoc rather than in the pattern.
var heredocOpen = regexp.MustCompile(`\A(?:cat[ \t]+)?<<['"]?(\w+)['"]?[ \t]*\n`)

// stripHeredoc unwraps `cat <<EOF ... EOF`, returning the body. The closing
// delimiter must be the last non-whitespace content, matching the anchored
// TypeScript regex; the earliest such delimiter wins, mirroring its lazy
// quantifier.
func stripHeredoc(input string) string {
	match := heredocOpen.FindStringSubmatchIndex(input)
	if match == nil {
		return input
	}
	delimiter := input[match[2]:match[3]]
	body := input[match[1]:]
	needle := "\n" + delimiter
	for offset := 0; ; {
		index := strings.Index(body[offset:], needle)
		if index < 0 {
			return input
		}
		index += offset
		rest := body[index+len(needle):]
		if strings.TrimSpace(rest) == "" {
			return body[:index]
		}
		offset = index + 1
	}
}

// SplitBOM separates a leading byte-order mark from the text.
func SplitBOM(text string) (body string, bom bool) {
	if strings.HasPrefix(text, "\uFEFF") {
		return strings.TrimPrefix(text, "\uFEFF"), true
	}
	return text, false
}

// JoinBOM restores a byte-order mark onto text that may or may not have one.
func JoinBOM(text string, bom bool) string {
	stripped, _ := SplitBOM(text)
	if bom {
		return "\uFEFF" + stripped
	}
	return stripped
}

// Parse reads a patch into its hunks.
func Parse(patchText string) ([]Hunk, error) {
	lines := strings.Split(stripHeredoc(strings.TrimSpace(patchText)), "\n")
	begin, end := -1, -1
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if begin == -1 && trimmed == markerBegin {
			begin = i
		}
		if trimmed == markerEnd {
			end = i
			break
		}
	}
	if begin == -1 || end == -1 || begin >= end {
		return nil, fmt.Errorf("invalid patch format: missing Begin/End markers")
	}

	var hunks []Hunk
	index := begin + 1
	for index < end {
		line := lines[index]
		switch {
		case strings.HasPrefix(line, markerAdd):
			path := strings.TrimSpace(strings.TrimPrefix(line, markerAdd))
			if path == "" {
				return nil, fmt.Errorf("invalid add file path")
			}
			contents, next, err := parseAdd(lines, index+1)
			if err != nil {
				return nil, err
			}
			hunks = append(hunks, Hunk{Type: HunkAdd, Path: path, Contents: contents})
			index = next

		case strings.HasPrefix(line, markerDelete):
			path := strings.TrimSpace(strings.TrimPrefix(line, markerDelete))
			if path == "" {
				return nil, fmt.Errorf("invalid delete file path")
			}
			hunks = append(hunks, Hunk{Type: HunkDelete, Path: path})
			index++

		case strings.HasPrefix(line, markerUpdate):
			path := strings.TrimSpace(strings.TrimPrefix(line, markerUpdate))
			if path == "" {
				return nil, fmt.Errorf("invalid update file path")
			}
			next := index + 1
			movePath := ""
			if next < len(lines) && strings.HasPrefix(lines[next], markerMove) {
				movePath = strings.TrimSpace(strings.TrimPrefix(lines[next], markerMove))
				if movePath == "" {
					return nil, fmt.Errorf("invalid move file path")
				}
				next++
			}
			chunks, after, err := parseUpdate(lines, next)
			if err != nil {
				return nil, err
			}
			if len(chunks) == 0 {
				return nil, fmt.Errorf("invalid update hunk for %s: expected at least one @@ chunk", path)
			}
			hunks = append(hunks, Hunk{Type: HunkUpdate, Path: path, MovePath: movePath, Chunks: chunks})
			index = after

		default:
			return nil, fmt.Errorf("invalid patch line: %s", line)
		}
	}
	return hunks, nil
}

func parseAdd(lines []string, start int) (string, int, error) {
	var content []string
	index := start
	for index < len(lines) && !strings.HasPrefix(lines[index], "***") {
		if !strings.HasPrefix(lines[index], "+") {
			return "", 0, fmt.Errorf("invalid add file line: %s", lines[index])
		}
		content = append(content, lines[index][1:])
		index++
	}
	return strings.Join(content, "\n"), index, nil
}

func parseUpdate(lines []string, start int) ([]UpdateChunk, int, error) {
	var chunks []UpdateChunk
	index := start
	for index < len(lines) && !strings.HasPrefix(lines[index], "***") {
		if !strings.HasPrefix(lines[index], "@@") {
			return nil, 0, fmt.Errorf("invalid update file line: %s", lines[index])
		}
		chunk := UpdateChunk{ChangeContext: strings.TrimSpace(lines[index][2:])}
		index++
		for index < len(lines) && !strings.HasPrefix(lines[index], "@@") {
			line := lines[index]
			if line == markerEOF {
				chunk.EndOfFile = true
				index++
				break
			}
			if strings.HasPrefix(line, "***") {
				break
			}
			switch {
			case strings.HasPrefix(line, " "):
				chunk.OldLines = append(chunk.OldLines, line[1:])
				chunk.NewLines = append(chunk.NewLines, line[1:])
			case strings.HasPrefix(line, "-"):
				chunk.OldLines = append(chunk.OldLines, line[1:])
			case strings.HasPrefix(line, "+"):
				chunk.NewLines = append(chunk.NewLines, line[1:])
			default:
				return nil, 0, fmt.Errorf("invalid update chunk line: %s", line)
			}
			index++
		}
		chunks = append(chunks, chunk)
	}
	return chunks, index, nil
}

// Derive applies an update hunk's chunks to the original file content,
// returning the new content and whether a BOM should be preserved.
func Derive(path string, chunks []UpdateChunk, original string) (string, bool, error) {
	body, bom := SplitBOM(original)
	lines := strings.Split(body, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	replacements, err := computeReplacements(lines, path, chunks)
	if err != nil {
		return "", false, err
	}
	updated := append([]string(nil), lines...)
	// Applied back-to-front so earlier offsets stay valid.
	for i := len(replacements) - 1; i >= 0; i-- {
		r := replacements[i]
		tail := append([]string(nil), updated[r.start+r.remove:]...)
		updated = append(updated[:r.start], append(append([]string(nil), r.insert...), tail...)...)
	}
	if len(updated) == 0 || updated[len(updated)-1] != "" {
		updated = append(updated, "")
	}
	next, nextBOM := SplitBOM(strings.Join(updated, "\n"))
	return next, bom || nextBOM, nil
}

type replacement struct {
	start  int
	remove int
	insert []string
}

func computeReplacements(lines []string, path string, chunks []UpdateChunk) ([]replacement, error) {
	var out []replacement
	lineIndex := 0
	for _, chunk := range chunks {
		if chunk.ChangeContext != "" {
			context := seek(lines, []string{chunk.ChangeContext}, lineIndex, false)
			if context == -1 {
				return nil, fmt.Errorf("failed to find context %q in %s", chunk.ChangeContext, path)
			}
			lineIndex = context + 1
		}
		if len(chunk.OldLines) == 0 {
			out = append(out, replacement{start: len(lines), remove: 0, insert: chunk.NewLines})
			continue
		}
		oldLines, newLines := chunk.OldLines, chunk.NewLines
		found := seek(lines, oldLines, lineIndex, chunk.EndOfFile)
		// A trailing blank line in the pattern is often an artifact of how the
		// chunk was written; retry without it before giving up.
		if found == -1 && oldLines[len(oldLines)-1] == "" {
			oldLines = oldLines[:len(oldLines)-1]
			if len(newLines) > 0 && newLines[len(newLines)-1] == "" {
				newLines = newLines[:len(newLines)-1]
			}
			found = seek(lines, oldLines, lineIndex, chunk.EndOfFile)
		}
		if found == -1 {
			return nil, fmt.Errorf("failed to find expected lines in %s:\n%s", path, strings.Join(chunk.OldLines, "\n"))
		}
		out = append(out, replacement{start: found, remove: len(oldLines), insert: newLines})
		lineIndex = found + len(oldLines)
	}
	// Stable sort by start offset; chunks may be found out of order.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j-1].start > out[j].start; j-- {
			out[j-1], out[j] = out[j], out[j-1]
		}
	}
	return out, nil
}

// comparators are tried in order of strictness: an exact match first, then
// progressively more forgiving ones, so a patch whose whitespace or typography
// drifted from the file still applies.
var comparators = []func(left, right string) bool{
	func(left, right string) bool { return left == right },
	func(left, right string) bool {
		return strings.TrimRight(left, " \t") == strings.TrimRight(right, " \t")
	},
	func(left, right string) bool { return strings.TrimSpace(left) == strings.TrimSpace(right) },
	func(left, right string) bool {
		return normalize(strings.TrimSpace(left)) == normalize(strings.TrimSpace(right))
	},
}

func seek(lines, pattern []string, start int, eof bool) int {
	if len(pattern) == 0 {
		return -1
	}
	for _, compare := range comparators {
		if eof {
			offset := len(lines) - len(pattern)
			if offset >= start && matches(lines, pattern, offset, compare) {
				return offset
			}
		}
		for offset := start; offset <= len(lines)-len(pattern); offset++ {
			if matches(lines, pattern, offset, compare) {
				return offset
			}
		}
	}
	return -1
}

func matches(lines, pattern []string, offset int, compare func(left, right string) bool) bool {
	if offset < 0 || offset+len(pattern) > len(lines) {
		return false
	}
	for i, line := range pattern {
		if !compare(lines[offset+i], line) {
			return false
		}
	}
	return true
}

// normalizer folds the typographic substitutions a model or editor commonly
// makes — smart quotes, en/em dashes, ellipsis, non-breaking space — back to
// their ASCII equivalents.
var normalizer = strings.NewReplacer(
	"\u2018", "'", "\u2019", "'", "\u201a", "'", "\u201b", "'",
	"\u201c", `"`, "\u201d", `"`, "\u201e", `"`, "\u201f", `"`,
	"\u2010", "-", "\u2011", "-", "\u2012", "-", "\u2013", "-", "\u2014", "-", "\u2015", "-",
	"\u2026", "...",
	"\u00a0", " ",
)

func normalize(value string) string { return normalizer.Replace(value) }
