// Package vcs ports the TypeScript VCS layer: git plumbing wrappers
// (packages/opencode/src/git/index.ts) and the working-tree/branch diff
// service built on them (packages/opencode/src/project/vcs.ts).
//
// git is a system tool invoked over exec, the same posture as the bash
// builtin; the "no runtime installs" rule in documentation/10-development.md
// is about downloading code, not about calling binaries already on PATH.
package vcs

import (
	"bytes"
	"errors"
	"os/exec"
	"strconv"
	"strings"
)

// StatusKind is the coarse change classification every surface agrees on
// (git/index.ts's `kind` and the VcsFileDiff schema's status literals).
type StatusKind string

const (
	StatusAdded    StatusKind = "added"
	StatusDeleted  StatusKind = "deleted"
	StatusModified StatusKind = "modified"
)

// statusKind maps a porcelain XY code to the coarse classification, porting
// git/index.ts's `kind`:
//
//	const kind = (code: string): Kind => {
//	  if (code === "??") return "added"
//	  if (code.includes("U")) return "modified"
//	  if (code.includes("A") && !code.includes("D")) return "added"
//	  if (code.includes("D") && !code.includes("A")) return "deleted"
//	  return "modified"
//	}
func statusKind(code string) StatusKind {
	if code == "??" {
		return StatusAdded
	}
	if strings.Contains(code, "U") {
		return StatusModified
	}
	if strings.Contains(code, "A") && !strings.Contains(code, "D") {
		return StatusAdded
	}
	if strings.Contains(code, "D") && !strings.Contains(code, "A") {
		return StatusDeleted
	}
	return StatusModified
}

// Item is one changed path with its porcelain classification
// (git/index.ts's Git.Item).
type Item struct {
	File   string
	Code   string
	Status StatusKind
}

// Stat counts the lines a change adds and removes (Git.Stat).
type Stat struct {
	File      string
	Additions int
	Deletions int
}

// Base is a default-branch candidate: its short name plus the ref to diff
// against (Git.Base).
type Base struct {
	Name string
	Ref  string
}

// PatchResult is a bounded git patch: the text when it fit, or a truncated
// flag when maxOutputBytes cut it short (Git.Patch). Callers discard the
// text of a truncated patch rather than render half a diff.
type PatchResult struct {
	Text      string
	Truncated bool
}

// patchOptions carries the context-line count and byte cap shared by every
// patch producer (git/index.ts's PatchOptions).
type patchOptions struct {
	Context        int
	MaxOutputBytes int64
}

// git runs one git command in dir and returns its combined stdout. A
// non-zero exit is not always an error: the TS wrappers translate specific
// failures into "absent" (no HEAD, no default branch), so callers pass
// wantExit=false for those probes and receive ("", exitCode, nil).
func git(dir string, stdin string, wantExit bool, args ...string) (string, int, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	// exec.ExitError is the expected "git said no"; surface its code without
	// wrapping so probes can branch on it.
	var exitErr *exec.ExitError
	if err != nil && !errors.As(err, &exitErr) {
		return "", -1, err
	}
	code := 0
	if err != nil {
		code = exitErr.ExitCode()
	}
	if code != 0 && wantExit {
		return "", code, errors.New(strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), code, nil
}

// gitOut runs git and returns trimmed stdout, ignoring a non-zero exit.
func gitOut(dir string, args ...string) string {
	out, _, _ := git(dir, "", false, args...)
	return strings.TrimSpace(out)
}

// gitCheck runs git and reports whether it exited zero.
func gitCheck(dir string, args ...string) bool {
	_, code, _ := git(dir, "", false, args...)
	return code == 0
}

// gitText runs git and returns trimmed stdout, or "" on a non-zero exit
// (git/index.ts's `text`+`out` pair).
func gitText(dir string, args ...string) string {
	out, code, _ := git(dir, "", false, args...)
	if code != 0 {
		return ""
	}
	return strings.TrimSpace(out)
}

// gitRaw runs git and returns stdout verbatim (no trim) for NUL-delimited
// record streams, where porcelain's first record legitimately begins with a
// space (" M file") and a whole-output TrimSpace would corrupt it.
func gitRaw(dir string, args ...string) string {
	out, code, _ := git(dir, "", false, args...)
	if code != 0 {
		return ""
	}
	return out
}

// gitLines runs git and returns its trimmed, non-empty stdout lines
// (git/index.ts's `lines`).
func gitLines(dir string, args ...string) []string {
	out := gitText(dir, args...)
	if out == "" {
		return nil
	}
	parts := strings.Split(out, "\n")
	lines := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			lines = append(lines, trimmed)
		}
	}
	return lines
}

// nuls splits a NUL-terminated record stream (git/index.ts's `nuls`).
func nuls(out string) []string {
	return strings.Split(strings.TrimRight(out, "\x00"), "\x00")
}

// Branch returns the checked-out branch, or "" when detached or outside a
// repository (Git.branch: `symbolic-ref --quiet --short HEAD`).
func Branch(dir string) string {
	return gitText(dir, "symbolic-ref", "--quiet", "--short", "HEAD")
}

// HasHead reports whether the repository has any commit (Git.hasHead:
// `rev-parse --verify HEAD`). A fresh `git init` has none, and "diff against
// HEAD" must degrade to "untracked files only" there.
func HasHead(dir string) bool {
	return gitCheck(dir, "rev-parse", "--verify", "HEAD")
}

// MergeBase returns the merge base of base and HEAD, or "" when the two
// histories are unrelated or the ref is absent (Git.mergeBase).
func MergeBase(dir, base string) string {
	return gitText(dir, "merge-base", base, "HEAD")
}

// DefaultBranch resolves the branch "main branch" diffs compare against,
// porting Git.defaultBranch's precedence exactly:
//
//	remote HEAD of the primary remote  →  init.defaultBranch if local
//	→  "main" if local  →  "master" if local  →  none
//
// The primary remote is origin when present, otherwise the only remote,
// otherwise upstream, otherwise the first (git/index.ts's `primary`).
func DefaultBranch(dir string) (Base, bool) {
	remote := primaryRemote(dir)
	if remote != "" {
		head := gitText(dir, "symbolic-ref", "refs/remotes/"+remote+"/HEAD")
		if head != "" {
			ref := strings.TrimPrefix(head, "refs/remotes/")
			if name, ok := strings.CutPrefix(ref, remote+"/"); ok && name != "" {
				return Base{Name: name, Ref: ref}, true
			}
		}
	}

	list := localBranches(dir)
	if name := gitText(dir, "config", "init.defaultBranch"); name != "" {
		for _, branch := range list {
			if branch == name {
				return Base{Name: name, Ref: name}, true
			}
		}
	}
	for _, fallback := range []string{"main", "master"} {
		for _, branch := range list {
			if branch == fallback {
				return Base{Name: fallback, Ref: fallback}, true
			}
		}
	}
	return Base{}, false
}

// primaryRemote picks the remote whose HEAD names the default branch
// (git/index.ts's `primary`): origin, else the sole remote, else upstream,
// else the first listed.
func primaryRemote(dir string) string {
	list := gitLines(dir, "remote")
	for _, name := range []string{"origin", "upstream"} {
		for _, remote := range list {
			if remote == name {
				return name
			}
		}
	}
	if len(list) == 1 {
		return list[0]
	}
	if len(list) > 0 {
		return list[0]
	}
	return ""
}

// localBranches lists refs/heads short names (git/index.ts's `refs`).
func localBranches(dir string) []string {
	return gitLines(dir, "for-each-ref", "--format=%(refname:short)", "refs/heads")
}

// Status lists every change including untracked files, unquoted and
// classified (Git.status: porcelain v1, -z, no renames).
func Status(dir string) []Item {
	out := gitRaw(dir, "status", "--porcelain=v1", "--untracked-files=all", "--no-renames", "-z", "--", ".")
	if out == "" {
		return nil
	}
	items := make([]Item, 0, 8)
	for _, record := range nuls(out) {
		// TS reads item.slice(3) for the path and item.slice(0, 2) for the
		// code; the split record is "<XY> <path>" and a shorter record is
		// not a path-bearing one.
		if len(record) < 4 {
			continue
		}
		file := unquoteGitPath(record[3:])
		if file == "" {
			continue
		}
		code := record[:2]
		items = append(items, Item{File: file, Code: code, Status: statusKind(code)})
	}
	return items
}

// NameStatus lists tracked changes between ref and the working tree, porting
// Git.diff (`diff --name-status -z <ref> -- .`). The -z form emits
// "status\0path\0" pairs (plus an origin path after renames, which
// --no-renames never produces).
func NameStatus(dir, ref string) []Item {
	out := gitRaw(dir, "diff", "--no-ext-diff", "--no-renames", "--name-status", "-z", ref, "--", ".")
	if out == "" {
		return nil
	}
	records := nuls(out)
	items := make([]Item, 0, len(records)/2)
	for i := 0; i+1 < len(records); i += 2 {
		code, file := records[i], records[i+1]
		if code == "" || file == "" {
			continue
		}
		items = append(items, Item{File: unquoteGitPath(file), Code: code, Status: statusKind(code)})
	}
	return items
}

// Numstat lists per-file add/delete counts against ref (Git.stats:
// `diff --numstat -z`), with binary files reporting 0/0 the way the TS
// producer treats a "-" pair.
func Numstat(dir, ref string) []Stat {
	out := gitRaw(dir, "diff", "--no-ext-diff", "--no-renames", "--numstat", "-z", ref, "--", ".")
	return parseNumstat(out)
}

// parseNumstat reads "-z" numstat output: "adds\tdels\tpath\0" records.
func parseNumstat(out string) []Stat {
	if out == "" {
		return nil
	}
	stats := make([]Stat, 0, 8)
	for _, record := range nuls(out) {
		first := strings.IndexByte(record, '\t')
		if first == -1 {
			continue
		}
		second := strings.IndexByte(record[first+1:], '\t')
		if second == -1 {
			continue
		}
		second += first + 1
		file := record[second+1:]
		if file == "" {
			continue
		}
		stats = append(stats, Stat{
			File:      unquoteGitPath(file),
			Additions: parseNumField(record[:first]),
			Deletions: parseNumField(record[first+1 : second]),
		})
	}
	return stats
}

// parseNumField reads a numstat count, mapping "-" (binary) and garbage to 0
// (TS's Number.isFinite guards).
func parseNumField(field string) int {
	if field == "-" || field == "" {
		return 0
	}
	value, err := strconv.Atoi(field)
	if err != nil {
		return 0
	}
	return value
}

// PatchAll renders one combined patch for every tracked change against ref
// (Git.patchAll). maxOutputBytes bounds the capture; a truncation means the
// caller must fall back rather than show a partial diff.
func PatchAll(dir, ref string, opts patchOptions) PatchResult {
	args := []string{
		"diff", "--patch", "--no-ext-diff", "--no-renames",
		"--unified=" + strconv.Itoa(contextLines(opts)),
		ref, "--", ".",
	}
	return gitPatched(dir, args, opts)
}

// Patch renders the patch for one tracked file (Git.patch).
func Patch(dir, ref, file string, opts patchOptions) PatchResult {
	args := []string{
		"diff", "--patch", "--no-ext-diff", "--no-renames",
		"--unified=" + strconv.Itoa(contextLines(opts)),
		ref, "--", file,
	}
	return gitPatched(dir, args, opts)
}

// PatchUntracked renders the patch for a file git has never seen, diffing
// against /dev/null (Git.patchUntracked).
func PatchUntracked(dir, file string, opts patchOptions) PatchResult {
	args := []string{
		"diff", "--no-index", "--patch", "--no-ext-diff", "--no-renames",
		"--unified=" + strconv.Itoa(contextLines(opts)),
		"--", "/dev/null", file,
	}
	return gitPatched(dir, args, opts)
}

// StatUntracked counts an untracked file's lines without rendering a patch
// (Git.statUntracked). Absent or binary yields ok=false.
func StatUntracked(dir, file string) (Stat, bool) {
	out, code, _ := git(dir, "", false, "diff", "--no-index", "--numstat", "--", "/dev/null", file)
	// git diff --no-index exits 1 whenever the files differ, which is the
	// success case here; only a usage error (2+) means "no stat".
	if code > 1 {
		return Stat{}, false
	}
	stats := parseNumstat(out)
	if len(stats) == 0 {
		return Stat{}, false
	}
	return stats[0], true
}

// contextLines defaults to git's own 3 when unset, mirroring
// git/index.ts's `options?.context ?? 3`.
func contextLines(opts patchOptions) int {
	if opts.Context > 0 {
		return opts.Context
	}
	return 3
}

// gitPatched runs a patch command with a bounded capture. --no-index always
// exits 1 on a difference, so exit codes are advisory here; the truncation
// flag is what callers branch on.
func gitPatched(dir string, args []string, opts patchOptions) PatchResult {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	var stdout bytes.Buffer
	limit := opts.MaxOutputBytes
	if limit <= 0 {
		limit = defaultPatchBytes
	}
	capped := &limitedBuffer{buf: &stdout, limit: limit}
	cmd.Stdout = capped
	var stderr strings.Builder
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err != nil && capped.truncated {
		// The cap killed the pipe before git could finish: that is the
		// bounded capture working, not a failure to run.
		return PatchResult{Truncated: true}
	}
	var exitErr *exec.ExitError
	if err != nil && !errors.As(err, &exitErr) {
		return PatchResult{Truncated: true}
	}
	if capped.truncated {
		return PatchResult{Truncated: true}
	}
	return PatchResult{Text: stdout.String()}
}

// limitedBuffer accepts up to limit bytes and reports truncation instead of
// growing past it, standing in for Bun's maxOutputBytes child-process option.
type limitedBuffer struct {
	buf       *bytes.Buffer
	limit     int64
	truncated bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	room := b.limit - int64(b.buf.Len())
	if room <= 0 {
		b.truncated = true
		return len(p), nil
	}
	if int64(len(p)) > room {
		p = p[:room]
		b.truncated = true
	}
	n, err := b.buf.Write(p)
	if err != nil {
		return n, err
	}
	return n, nil
}

// unquoteGitPath decodes git's C-style quoted paths. git quotes any path
// with non-ASCII or control bytes as "i\303\251n.txt", and the TS layer
// decodes both here (git/index.ts's parseQuotedPath) and again on the API
// surface (session/summary.ts's unquoteGitPath, which this mirrors more
// closely because it carries the full escape set including octal).
func unquoteGitPath(input string) string {
	if !strings.HasPrefix(input, `"`) {
		return input
	}
	if !strings.HasSuffix(input, `"`) || len(input) < 2 {
		return input
	}
	var out []byte
	body := input[1 : len(input)-1]
	for i := 0; i < len(body); i++ {
		char := body[i]
		if char != '\\' {
			out = append(out, char)
			continue
		}
		i++
		if i >= len(body) {
			out = append(out, '\\')
			break
		}
		next := body[i]
		switch {
		case next >= '0' && next <= '7':
			// Up to three octal digits name one byte.
			end := min(i+3, len(body))
			chunk := body[i:end]
			digits := 0
			for digits < len(chunk) && chunk[digits] >= '0' && chunk[digits] <= '7' {
				digits++
			}
			if digits > 0 {
				value, err := strconv.ParseUint(chunk[:digits], 8, 8)
				if err == nil {
					out = append(out, byte(value))
					i += digits - 1
					continue
				}
			}
			out = append(out, next)
		case next == 'n':
			out = append(out, '\n')
		case next == 'r':
			out = append(out, '\r')
		case next == 't':
			out = append(out, '\t')
		case next == 'b':
			out = append(out, '\b')
		case next == 'f':
			out = append(out, '\f')
		case next == 'v':
			out = append(out, '\v')
		default:
			// \", \\ and anything unrecognized keep the escaped character,
			// matching the TS parser's fallthrough.
			out = append(out, next)
		}
	}
	return string(out)
}
