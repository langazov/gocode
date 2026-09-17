package eval

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/langazov/gocode-go/internal/rag/chunk"
)

// Region is a 1-based, inclusive line range within one file,
// project-root-relative and slash-separated like chunk.Chunk.Path.
type Region struct {
	Path      string
	StartLine int
	EndLine   int
}

// Overlaps reports whether r and other share at least one line of the same
// file.
func (r Region) Overlaps(other Region) bool {
	return r.Path == other.Path && r.StartLine <= other.EndLine && other.StartLine <= r.EndLine
}

// GoldPair is one labeled retrieval example: Query is a natural-language
// string a developer plausibly typed, and Relevant is every region of the
// current working tree that answers it.
type GoldPair struct {
	Query      string
	CommitHash string
	Relevant   []Region
}

// MineOptions controls how MineGoldSet turns git history into gold pairs.
type MineOptions struct {
	// MaxCommits caps how many of the most recent non-merge commits are
	// scanned. Defaults to 300.
	MaxCommits int
	// MinSubjectLen filters out commit subjects too short to carry any
	// semantic signal ("wip", "fix", "typo"). Defaults to 15 runes.
	MinSubjectLen int
	// MaxFilesPerCommit skips commits touching more files than this
	// entirely, rather than truncating to the first few: a wide mechanical
	// change (a rename across 40 files) is not a query anyone would type,
	// and scoring its subject against that many unrelated regions would
	// only add noise to the gold set. Defaults to 3.
	MaxFilesPerCommit int
}

func (o MineOptions) withDefaults() MineOptions {
	if o.MaxCommits <= 0 {
		o.MaxCommits = 300
	}
	if o.MinSubjectLen <= 0 {
		o.MinSubjectLen = 15
	}
	if o.MaxFilesPerCommit <= 0 {
		o.MaxFilesPerCommit = 3
	}
	return o
}

// commitPrefix marks the pretty-printed header line git emits before each
// commit's diff, chosen to be bytes that never occur at the start of a
// unified-diff line (which only ever starts with "diff", "index", "---",
// "+++", "@@", "+", "-", " ", or is blank).
const commitPrefix = "\x02commit\x02"

// hunkHeader matches a unified-diff hunk header's post-image range, e.g.
// "@@ -12,3 +15,4 @@ func Foo() {" — group 1 is the start line, group 2 the
// line count (absent, per the format, when the count is 1).
var hunkHeader = regexp.MustCompile(`^@@ -\d+(?:,\d+)? \+(\d+)(?:,(\d+))? @@`)

// minedHunk is one hunk's post-image range plus the exact text it added, so
// the range can later be checked against the current file's content at that
// same location — not just against the file's current line count.
type minedHunk struct {
	Region
	added []string
}

// MineGoldSet mines (query, relevant-region) pairs for retrieval evaluation
// from repoRoot's own commit history: a commit's subject line stands in for
// a query a developer might type, and the lines it changed stand in for the
// answer. This is a silver-label technique (the same one docstring/commit-
// message code-search evals like CodeSearchNet use) — noisy at the level of
// any single pair, but statistically usable across the hundreds it produces,
// which a hand-labeled set of a dozen examples never is.
//
// A hunk's post-image range is kept only when the current file still has the
// exact text that hunk added at that same location. Checking the line count
// alone is not enough: on a file edited again since (a later commit adding
// or removing lines above the hunk), the range still fits inside the file
// but now names entirely different content — silently scoring the pair
// against the wrong lines rather than dropping it. Comparing the hunk's own
// recorded text against the current line range catches that directly,
// without needing to replay every intervening diff. This trades recall
// (heavily-churned files contribute fewer surviving pairs) for precision
// (the pairs that remain point at real, currently-searchable content).
func MineGoldSet(ctx context.Context, repoRoot string, opts MineOptions) ([]GoldPair, error) {
	opts = opts.withDefaults()

	// One git invocation for the whole scan — combining the commit header
	// and its diff via --pretty and -p — rather than one process per commit,
	// which would mean thousands of spawns for a several-hundred-commit
	// scan.
	cmd := exec.CommandContext(ctx, "git", "log",
		"--no-merges",
		"-n", strconv.Itoa(opts.MaxCommits),
		"--unified=0",
		"--pretty=format:"+commitPrefix+"%H\x1f%s",
		"-p",
	)
	cmd.Dir = repoRoot
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("eval: git log: %w", err)
	}

	var pairs []GoldPair
	var curHash, curSubject, curFile string
	curFiles := map[string][]minedHunk{}
	var curFileOrder []string

	flush := func() {
		defer func() {
			curHash, curSubject, curFile = "", "", ""
			curFiles = map[string][]minedHunk{}
			curFileOrder = nil
		}()
		if curHash == "" {
			return
		}
		if len([]rune(strings.TrimSpace(curSubject))) < opts.MinSubjectLen {
			return
		}
		if len(curFileOrder) == 0 || len(curFileOrder) > opts.MaxFilesPerCommit {
			return
		}
		var relevant []Region
		for _, path := range curFileOrder {
			relevant = append(relevant, validateAndMergeRegions(repoRoot, path, curFiles[path])...)
		}
		if len(relevant) == 0 {
			return
		}
		pairs = append(pairs, GoldPair{Query: curSubject, CommitHash: curHash, Relevant: relevant})
	}

	// Lines are indexed by hand (rather than ranged over via bufio.Scanner)
	// because a hunk header's body must be consumed inline: with
	// --unified=0 a hunk's lines are exactly its removed ("-") lines
	// followed by its added ("+") lines, with no surrounding context, so the
	// added text can be read off directly instead of re-deriving it later.
	lines := strings.Split(string(output), "\n")
	for i := 0; i < len(lines); {
		line := lines[i]
		switch {
		case strings.HasPrefix(line, commitPrefix):
			flush()
			rest := strings.TrimPrefix(line, commitPrefix)
			parts := strings.SplitN(rest, "\x1f", 2)
			curHash = parts[0]
			if len(parts) > 1 {
				curSubject = parts[1]
			}
			i++

		case strings.HasPrefix(line, "+++ "):
			f := strings.TrimPrefix(line, "+++ ")
			if f == "/dev/null" {
				curFile = "" // this file was deleted by the commit: nothing to index
				i++
				continue
			}
			f = strings.TrimPrefix(f, "b/")
			if !chunk.IsDefaultTextCandidate(f) {
				// rag-plugin's own indexer would never chunk this file (a
				// lockfile, go.mod/go.sum, a bare .gitignore, ...), so a gold
				// region here could never be found by search. Counting it as
				// a miss would blame retrieval quality for an unsearchable
				// gold pair instead of a real one.
				curFile = ""
				i++
				continue
			}
			curFile = f
			if _, seen := curFiles[f]; !seen {
				curFiles[f] = nil
				curFileOrder = append(curFileOrder, f)
			}
			i++

		case strings.HasPrefix(line, "@@ "):
			m := hunkHeader.FindStringSubmatch(line)
			i++
			if curFile == "" || m == nil {
				continue
			}
			start, _ := strconv.Atoi(m[1])
			count := 1
			if m[2] != "" {
				count, _ = strconv.Atoi(m[2])
			}
			var added []string
			for i < len(lines) && len(added) < count {
				body := lines[i]
				switch {
				case strings.HasPrefix(body, "+") && !strings.HasPrefix(body, "+++"):
					added = append(added, strings.TrimPrefix(body, "+"))
					i++
				case strings.HasPrefix(body, "-") && !strings.HasPrefix(body, "---"):
					i++
				default:
					added = nil // hunk body ended before count was satisfied: malformed, ignore
				}
				if added == nil && count > 0 {
					break
				}
			}
			if count == 0 {
				continue // a pure deletion at this point adds no lines to search for
			}
			curFiles[curFile] = append(curFiles[curFile], minedHunk{
				Region: Region{Path: curFile, StartLine: start, EndLine: start + count - 1},
				added:  added,
			})

		default:
			i++
		}
	}
	flush()
	return pairs, nil
}

// validateAndMergeRegions drops any hunk whose recorded text no longer
// matches the current file at that same line range — whether because the
// range no longer fits inside the file, or because later commits shifted or
// rewrote what's there — and merges the survivors, so a commit whose file
// has since changed elsewhere contributes only the part that is still real.
func validateAndMergeRegions(repoRoot, path string, hunks []minedHunk) []Region {
	if len(hunks) == 0 {
		return nil
	}
	curLines, err := readLines(filepath.Join(repoRoot, filepath.FromSlash(path)))
	if err != nil {
		return nil // file deleted or unreadable since: nothing left to point at
	}

	sort.Slice(hunks, func(i, j int) bool { return hunks[i].StartLine < hunks[j].StartLine })
	var out []Region
	for _, h := range hunks {
		if h.StartLine < 1 || h.EndLine > len(curLines) {
			continue
		}
		if !slices.Equal(curLines[h.StartLine-1:h.EndLine], h.added) {
			continue // content at this location has since changed: stale mapping, not a real answer
		}
		if n := len(out); n > 0 && h.StartLine <= out[n-1].EndLine+1 {
			if h.EndLine > out[n-1].EndLine {
				out[n-1].EndLine = h.EndLine
			}
			continue
		}
		out = append(out, h.Region)
	}
	return out
}

func readLines(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	text := strings.TrimSuffix(string(data), "\n")
	if text == "" {
		return nil, nil
	}
	return strings.Split(text, "\n"), nil
}
