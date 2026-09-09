// vcs.go ports packages/opencode/src/project/vcs.ts's Vcs.diff: the
// per-file patch assembly with byte caps, batched patch rendering, and the
// working-tree vs branch-mode split.
package vcs

import (
	"math"
	"regexp"
	"sort"
	"strings"
)

// Cap constants, porting project/vcs.ts lines 11-13. The context request is
// MaxInt32 because git clamps it to "whole file"; the producers want full
// patches so the renderer can compute its own context windows.
// DiffContextLines is the context-window the TUI asks for
// (packages/tui/src/feature-plugins/system/diff-viewer.tsx's
// VCS_DIFF_CONTEXT_LINES = 12), exported so the server can default to it.
const DiffContextLines = vcsDiffContextLines

const (
	PatchContextLines   = math.MaxInt32
	MaxPatchBytes       = 10 << 20
	MaxTotalPatchBytes  = 10 << 20
	defaultPatchBytes   = MaxPatchBytes
	vcsDiffContextLines = 12
)

// FileDiff is one file's diff, mirroring the VcsFileDiff schema
// (project/vcs.ts): the patch text, line counts, and coarse status. The
// patch is present but empty for binary files and for files beyond the
// total-patch byte cap, exactly like the TS producer's emptyPatch fallback.
type FileDiff struct {
	File      string     `json:"file"`
	Patch     string     `json:"patch"`
	Additions int        `json:"additions"`
	Deletions int        `json:"deletions"`
	Status    StatusKind `json:"status"`
}

// Mode selects the comparison base: "git" is the working tree against HEAD
// (or untracked-only in a repository with no commits yet), "branch" is the
// working tree against the merge base with the default branch.
const (
	ModeGit    = "git"
	ModeBranch = "branch"
)

// DiffMode wraps Mode with validation so callers can reject unknowns before
// shelling out.
func DiffMode(mode string) (string, bool) {
	switch mode {
	case ModeGit, ModeBranch:
		return mode, true
	}
	return "", false
}

// RepoInfo reports the repository state the interface gates on: the
// checked-out branch, the default branch when one resolves, and whether
// this is a git repository at all (TS Vcs.info → Info schema).
type RepoInfo struct {
	Branch        string `json:"branch,omitempty"`
	DefaultBranch string `json:"defaultBranch,omitempty"`
}

// Info resolves branch and default branch for dir. ok=false outside a git
// repository, where every diff endpoint answers an empty list.
func Info(dir string) (RepoInfo, bool) {
	branch := Branch(dir)
	if branch == "" && !HasHead(dir) {
		// Both probes failing means git either isn't installed or this isn't
		// a work tree. Branch alone can be empty on a detached HEAD inside a
		// perfectly good repository, so HasHead is the tiebreaker.
		if !gitCheck(dir, "rev-parse", "--is-inside-work-tree") {
			return RepoInfo{}, false
		}
	}
	info := RepoInfo{Branch: branch}
	if base, ok := DefaultBranch(dir); ok {
		info.DefaultBranch = base.Name
	}
	return info, true
}

// emptyPatch ports project/vcs.ts's emptyPatch: formatPatch of an empty
// change is a header-only patch ("--- file\n+++ file"), which the viewer
// renders as the file's header with no body — the "this file changed but its
// patch was capped or binary" placeholder. An empty string would instead
// read as "no patch available".
func emptyPatch(file string) string {
	return "--- " + file + "\n+++ " + file + "\n"
}

// emptyBatch is the no-batch batch (project/vcs.ts's emptyBatch): nothing
// batched, nothing capped.
type patchBatch struct {
	patches map[string]string
	capped  bool
}

// splitGitPatch splits a combined multi-file patch into per-file chunks,
// porting splitGitPatch. A truncated capture loses the tail mid-chunk, so
// the final (partial) chunk is dropped rather than rendered.
func splitGitPatch(result PatchResult) []string {
	if result.Text == "" {
		return nil
	}
	// TS matches /(?:^|\n)diff --git /g and adjusts each start past a
	// leading newline.
	starts := []int{}
	for _, at := range diffGitStarts(result.Text) {
		if at > 0 && result.Text[at-1] == '\n' {
			at++
		}
		starts = append(starts, at)
	}
	if len(starts) == 0 {
		return nil
	}
	chunks := make([]string, 0, len(starts))
	for i, start := range starts {
		end := len(result.Text)
		if i+1 < len(starts) {
			end = starts[i+1]
		}
		chunks = append(chunks, result.Text[start:end])
	}
	if result.Truncated {
		return chunks[:max(0, len(chunks)-1)]
	}
	return chunks
}

// diffGitPattern matches the "diff --git " line that opens each file's chunk
// (splitGitPatch's regex, precompiled).
var diffGitPattern = regexp.MustCompile(`(?:^|\n)diff --git `)

func diffGitStarts(text string) []int {
	var starts []int
	for _, at := range diffGitPattern.FindAllStringIndex(text, -1) {
		starts = append(starts, at[0])
	}
	return starts
}

// fileFromPatchChunk recovers a file path from one patch chunk, porting
// fileFromPatchChunk: prefer the +++/-path line, fall back to parsing the
// "diff --git a/x b/y" header (including its quoted forms).
func fileFromPatchChunk(chunk string) string {
	if next := patchHeaderFile(chunk, "+++ "); next != "" {
		return next
	}
	if before := patchHeaderFile(chunk, "--- "); before != "" {
		return before
	}
	header := firstLineAfter(chunk, "diff --git ")
	if header == "" {
		return ""
	}
	return fileFromGitHeader(header)
}

// patchHeaderFile reads the path off a ---/+++ header line, ignoring the
// timestamps that follow the tab.
func patchHeaderFile(chunk, prefix string) string {
	for _, line := range strings.Split(chunk, "\n") {
		if rest, ok := strings.CutPrefix(line, prefix); ok {
			return fileFromDiffPath(rest)
		}
	}
	return ""
}

// firstLineAfter returns the remainder of the first line containing marker,
// for the "diff --git a/x b/y" form.
func firstLineAfter(chunk, marker string) string {
	at := strings.Index(chunk, marker)
	if at == -1 {
		return ""
	}
	rest := chunk[at+len(marker):]
	if end := strings.IndexByte(rest, '\n'); end != -1 {
		return rest[:end]
	}
	return rest
}

// fileFromDiffPath normalizes one side of a diff path: strips an a/ or b/
// prefix, decodes quoting, maps /dev/null to "" (fileFromDiffPath +
// parsePathToken).
func fileFromDiffPath(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || value == "/dev/null" {
		return ""
	}
	var path string
	// A quoted path can carry a timestamp after a tab outside the quotes.
	if strings.HasPrefix(value, `"`) {
		path = unquoteGitPath(value)
	} else {
		path = value
		if at := strings.IndexByte(path, '\t'); at != -1 {
			path = path[:at]
		}
	}
	return strings.TrimPrefix(strings.TrimPrefix(path, "a/"), "b/")
}

// fileFromGitHeader parses the two paths out of a "diff --git a/x b/y"
// header line (fileFromGitHeader), preferring the b/ side.
func fileFromGitHeader(header string) string {
	if strings.HasPrefix(header, `"`) {
		// Quoted: '"a/path with space" "b/path with space"'.
		first, rest := splitQuoted(header)
		if rest == "" {
			return ""
		}
		second, _ := splitQuoted(rest)
		if second != "" {
			return fileFromDiffPath(second)
		}
		return fileFromDiffPath(first)
	}
	// Unquoted git collapses the shared prefix; the b/ side follows " b/".
	at := strings.Index(header, " b/")
	if at == -1 {
		return ""
	}
	return fileFromDiffPath(header[at+3:])
}

// splitQuoted pulls one C-quoted token off the front of value, returning it
// decoded along with the remainder.
func splitQuoted(value string) (string, string) {
	value = strings.TrimLeft(value, " ")
	if !strings.HasPrefix(value, `"`) {
		return "", ""
	}
	// Walk to the closing quote, honoring backslash escapes.
	for i := 1; i < len(value); i++ {
		if value[i] == '\\' {
			i++
			continue
		}
		if value[i] == '"' {
			return unquoteGitPath(value[:i+1]), strings.TrimLeft(value[i+1:], " ")
		}
	}
	return "", ""
}

// batchPatches renders one combined patch and indexes the chunks by file,
// porting batchPatches. Files the split cannot attribute land under their
// list-order name (TS's `list[index]?.file` fallback).
func batchPatches(dir, ref string, list []Item, opts patchOptions) patchBatch {
	if len(list) == 0 {
		return patchBatch{patches: map[string]string{}}
	}
	result := PatchAll(dir, ref, opts)
	patches := map[string]string{}
	for i, chunk := range splitGitPatch(result) {
		file := fileFromPatchChunk(chunk)
		if file == "" && i < len(list) {
			file = list[i].File
		}
		if file == "" {
			continue
		}
		patches[file] += chunk
	}
	return patchBatch{patches: patches, capped: result.Truncated}
}

// nativePatch renders a single file's patch, untracked-aware
// (project/vcs.ts's nativePatch): tracked files diff against ref; untracked
// ones diff against /dev/null. A truncated render is discarded — the caller
// substitutes an empty patch rather than show half a diff.
func nativePatch(dir, ref string, item Item, opts patchOptions) string {
	var result PatchResult
	if item.Code == "??" || ref == "" {
		result = PatchUntracked(dir, item.File, opts)
	} else {
		result = Patch(dir, ref, item.File, opts)
	}
	if !result.Truncated {
		return result.Text
	}
	return emptyPatch(item.File)
}

// totalPatch applies the total-bytes budget, porting totalPatch: once the
// running total would exceed the cap, the file gets an empty patch and the
// cap stays tripped for every later file.
func totalPatch(file, patch string, total int) (string, bool, int) {
	if total+len(patch) <= MaxTotalPatchBytes {
		return patch, false, total + len(patch)
	}
	return emptyPatch(file), true, total
}

// patchForItem picks a file's patch: the batched chunk when present, the
// per-file render when the batch was capped or the file is untracked
// (patchForItem).
func patchForItem(dir, ref string, item Item, batch patchBatch, capped bool, opts patchOptions) string {
	if capped {
		return emptyPatch(item.File)
	}
	if batched, ok := batch.patches[item.File]; ok {
		return batched
	}
	if item.Code != "??" && batch.capped {
		return emptyPatch(item.File)
	}
	return nativePatch(dir, ref, item, opts)
}

// buildFiles assembles the final FileDiff list, porting `files`: sorted by
// path, each with its stat (numstat map, untracked stat fallback) and patch,
// under the total-patch byte budget.
func buildFiles(dir, ref string, list []Item, stats map[string]Stat, batch patchBatch, opts patchOptions) []FileDiff {
	sort.Slice(list, func(i, j int) bool { return list[i].File < list[j].File })

	out := make([]FileDiff, 0, len(list))
	total := 0
	capped := false
	for _, item := range list {
		stat, ok := stats[item.File]
		if !ok && item.Status == StatusAdded {
			if untracked, found := StatUntracked(dir, item.File); found {
				stat = untracked
				ok = true
			}
		}
		var patch string
		if capped {
			patch = emptyPatch(item.File)
		} else {
			var tripped bool
			patch, tripped, total = totalPatch(item.File, patchForItem(dir, ref, item, batch, capped, opts), total)
			capped = capped || tripped
		}
		status := item.Status
		if ok {
			out = append(out, FileDiff{
				File:      item.File,
				Patch:     patch,
				Additions: stat.Additions,
				Deletions: stat.Deletions,
				Status:    status,
			})
		} else {
			out = append(out, FileDiff{
				File:   item.File,
				Patch:  patch,
				Status: status,
			})
		}
	}
	return out
}

// statMap indexes a stat list by path (project/vcs.ts's `nums`).
func statMap(list []Stat) map[string]Stat {
	out := make(map[string]Stat, len(list))
	for _, stat := range list {
		out[stat.File] = stat
	}
	return out
}

// mergeItems unions item lists by path, first occurrence winning
// (project/vcs.ts's `merge`).
func mergeItems(lists ...[]Item) []Item {
	seen := map[string]bool{}
	var out []Item
	for _, list := range lists {
		for _, item := range list {
			if seen[item.File] {
				continue
			}
			seen[item.File] = true
			out = append(out, item)
		}
	}
	return out
}

// diffAgainstRef assembles the diff of the working tree (including
// untracked files) against a resolved ref, porting diffAgainstRef.
func diffAgainstRef(dir, ref string, opts patchOptions) []FileDiff {
	list := NameStatus(dir, ref)
	stats := Numstat(dir, ref)
	untracked := untrackedItems(Status(dir))
	merged := mergeItems(list, untracked)
	batch := batchPatches(dir, ref, list, opts)
	return buildFiles(dir, ref, merged, statMap(stats), batch, opts)
}

// untrackedItems filters a status list down to untracked paths.
func untrackedItems(list []Item) []Item {
	var out []Item
	for _, item := range list {
		if item.Code == "??" {
			out = append(out, item)
		}
	}
	return out
}

// DiffWorkingTree is mode "git": HEAD as the base when one exists, otherwise
// the untracked-only listing a fresh repository produces (project/vcs.ts's
// `track` with ref undefined).
func DiffWorkingTree(dir string, opts patchOptions) []FileDiff {
	var ref string
	if HasHead(dir) {
		ref = "HEAD"
	}
	// Untracked files are listed by status either way; a fresh repository's
	// NameStatus(HEAD) would error and return nothing, which the untracked
	// union restores.
	list := Status(dir)
	stats := statMap(Numstat(dir, ref))
	var batch patchBatch
	if ref != "" {
		list = mergeItems(NameStatus(dir, ref), untrackedItems(list))
		batch = batchPatches(dir, ref, NameStatus(dir, ref), opts)
	} else {
		batch = patchBatch{patches: map[string]string{}}
	}
	return buildFiles(dir, ref, list, stats, batch, opts)
}

// DiffBranch is mode "branch": the merge base with the default branch, empty
// when there is no default branch or the current branch already is it
// (Vcs.diff's branch arm).
func DiffBranch(dir string, opts patchOptions) []FileDiff {
	base, ok := DefaultBranch(dir)
	if !ok {
		return nil
	}
	branch := Branch(dir)
	if branch != "" && branch == base.Name {
		return nil
	}
	ref := MergeBase(dir, base.Ref)
	if ref == "" {
		return nil
	}
	return diffAgainstRef(dir, ref, opts)
}

// Diff produces the file diff for a mode. Unknown modes and non-git
// directories yield an empty result rather than an error, matching the TS
// service's empty-array arms. Context is the number of context lines the
// interface wants (the TUI passes 12, its VCS_DIFF_CONTEXT_LINES); zero
// means the producers' full-context default.
func Diff(dir, mode string, context int) []FileDiff {
	opts := patchOptions{Context: context}
	switch mode {
	case ModeBranch:
		return DiffBranch(dir, opts)
	default:
		return DiffWorkingTree(dir, opts)
	}
}
