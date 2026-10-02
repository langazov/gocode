// Package gitops shells out to the git CLI for the desktop client's Source
// Control view: status, diffs, staging (files and hunks), commits, branches,
// remotes, history, stashes and conflict resolution. Served over HTTP by
// internal/server/git.go.
//
// Ported from goide's host (host/internal/gitops/gitops.go). Unlike the
// read-only parent package vcs, which ports opencode's diff plumbing, these
// operations mutate the repository; every call is non-interactive so a
// request can never hang on a credential prompt or an editor.
package gitops

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	localTimeout  = 30 * time.Second
	remoteTimeout = 3 * time.Minute // fetch/pull/push talk to the network
)

// Status is the parsed `git status --porcelain=v2 --branch` output plus
// repository state the UI needs.
type Status struct {
	IsRepo            bool         `json:"isRepo"`
	Root              string       `json:"root"`
	Branch            string       `json:"branch"` // "HEAD" when detached
	Detached          bool         `json:"detached"`
	Head              string       `json:"head"` // short hash
	HasCommits        bool         `json:"hasCommits"`
	Upstream          string       `json:"upstream"`
	Ahead             int          `json:"ahead"`
	Behind            int          `json:"behind"`
	Operation         string       `json:"operation"` // "", merge, rebase, cherry-pick, revert
	StashCount        int          `json:"stashCount"`
	LastCommitMessage string       `json:"lastCommitMessage"`
	Remotes           []string     `json:"remotes"`
	Files             []FileStatus `json:"files"`
}

// FileStatus is one entry (a path may appear twice: staged + worktree).
type FileStatus struct {
	Path     string `json:"path"`
	OrigPath string `json:"origPath,omitempty"` // renames/copies
	Status   string `json:"status"`             // M A D R C ? U
	Staged   bool   `json:"staged"`
}

type runOpts struct {
	stdin      string
	timeout    time.Duration
	okExitCode int // additional exit code treated as success (e.g. diff --no-index => 1)
}

// git runs a git command in dir non-interactively: no credential prompts,
// no editors, so a call can never hang waiting for input.
func git(ctx context.Context, dir string, o runOpts, args ...string) (string, error) {
	if o.timeout == 0 {
		o.timeout = localTimeout
	}
	cctx, cancel := context.WithTimeout(ctx, o.timeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, "git", append([]string{"-c", "core.quotepath=off", "-c", "color.ui=false"}, args...)...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_TERMINAL_PROMPT=0", // fail instead of asking for credentials
		"GIT_EDITOR=true",       // merge/rebase --continue keep the prepared message
		"GIT_SEQUENCE_EDITOR=true",
		"GIT_OPTIONAL_LOCKS=0", // status from the IDE must not fight the user's git
		"LC_ALL=C",
	)
	if o.stdin != "" {
		cmd.Stdin = strings.NewReader(o.stdin)
	}
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	err := cmd.Run()
	if err != nil {
		var ee *exec.ExitError
		if o.okExitCode != 0 && errors.As(err, &ee) && ee.ExitCode() == o.okExitCode {
			return out.String(), nil
		}
		if cctx.Err() == context.DeadlineExceeded {
			return out.String(), fmt.Errorf("git %s timed out", args[0])
		}
		msg := strings.TrimSpace(errb.String())
		if msg == "" {
			msg = strings.TrimSpace(out.String())
		}
		if msg == "" {
			msg = err.Error()
		}
		return out.String(), errors.New(msg)
	}
	return out.String(), nil
}

func runGit(ctx context.Context, dir string, args ...string) (string, error) {
	return git(ctx, dir, runOpts{}, args...)
}

// combined runs git and returns stdout+stderr (remote ops report progress
// and results on stderr).
func combined(ctx context.Context, dir string, timeout time.Duration, args ...string) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_EDITOR=true", "LC_ALL=C")
	out, err := cmd.CombinedOutput()
	text := strings.TrimSpace(string(out))
	if err != nil {
		if cctx.Err() == context.DeadlineExceeded {
			return text, fmt.Errorf("git %s timed out", args[0])
		}
		if text == "" {
			text = err.Error()
		}
		return text, errors.New(text)
	}
	return text, nil
}

func gitDir(ctx context.Context, dir string) string {
	out, err := runGit(ctx, dir, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// RepoRoot returns the repository top level containing dir, or dir itself
// when it is not inside a repository. Git reports paths relative to the
// top level, so every operation runs there.
func RepoRoot(ctx context.Context, dir string) string {
	out, err := runGit(ctx, dir, "rev-parse", "--show-toplevel")
	if err != nil || strings.TrimSpace(out) == "" {
		return dir
	}
	return strings.TrimSpace(out)
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

// Operation reports an in-progress merge/rebase/cherry-pick/revert.
func Operation(ctx context.Context, dir string) string {
	gd := gitDir(ctx, dir)
	switch {
	case gd == "":
		return ""
	case exists(filepath.Join(gd, "rebase-merge")) || exists(filepath.Join(gd, "rebase-apply")):
		return "rebase"
	case exists(filepath.Join(gd, "MERGE_HEAD")):
		return "merge"
	case exists(filepath.Join(gd, "CHERRY_PICK_HEAD")):
		return "cherry-pick"
	case exists(filepath.Join(gd, "REVERT_HEAD")):
		return "revert"
	}
	return ""
}

// StatusOf collects repo status for the workspace at dir.
func StatusOf(ctx context.Context, dir string) (*Status, error) {
	st := &Status{}
	if _, err := runGit(ctx, dir, "rev-parse", "--git-dir"); err != nil {
		return st, nil // not a repository
	}
	st.IsRepo = true
	st.Root = RepoRoot(ctx, dir)

	out, err := runGit(ctx, dir, "status", "--porcelain=v2", "--branch", "-z", "--untracked-files=all")
	if err != nil {
		return st, err
	}
	parseStatusV2(out, st)

	st.Operation = Operation(ctx, dir)
	if st.HasCommits {
		if msg, err := runGit(ctx, dir, "log", "-1", "--format=%B"); err == nil {
			st.LastCommitMessage = strings.TrimRight(msg, "\n")
		}
	}
	if out, err := runGit(ctx, dir, "stash", "list", "--format=%gd"); err == nil {
		st.StashCount = len(nonEmptyLines(out))
	}
	if out, err := runGit(ctx, dir, "remote"); err == nil {
		st.Remotes = nonEmptyLines(out)
	}
	return st, nil
}

func parseStatusV2(out string, st *Status) {
	recs := strings.Split(out, "\x00")
	for i := 0; i < len(recs); i++ {
		r := recs[i]
		if r == "" {
			continue
		}
		switch {
		case strings.HasPrefix(r, "# branch.oid "):
			oid := strings.TrimPrefix(r, "# branch.oid ")
			st.HasCommits = oid != "(initial)"
			if st.HasCommits && len(oid) >= 7 {
				st.Head = oid[:7]
			}
		case strings.HasPrefix(r, "# branch.head "):
			st.Branch = strings.TrimPrefix(r, "# branch.head ")
			if st.Branch == "(detached)" {
				st.Branch, st.Detached = "HEAD", true
			}
		case strings.HasPrefix(r, "# branch.upstream "):
			st.Upstream = strings.TrimPrefix(r, "# branch.upstream ")
		case strings.HasPrefix(r, "# branch.ab "):
			var a, b int
			fmt.Sscanf(strings.TrimPrefix(r, "# branch.ab "), "+%d -%d", &a, &b)
			st.Ahead, st.Behind = a, b
		case strings.HasPrefix(r, "1 "), strings.HasPrefix(r, "2 "):
			// 1 XY sub mH mI mW hH hI path
			// 2 XY sub mH mI mW hH hI Xscore path\0origPath
			n := 9
			if r[0] == '2' {
				n = 10
			}
			f := strings.SplitN(r, " ", n)
			if len(f) < n {
				continue
			}
			path, orig := f[n-1], ""
			if r[0] == '2' && i+1 < len(recs) {
				i++
				orig = recs[i]
			}
			x, y := f[1][0], f[1][1]
			if x != '.' {
				st.Files = append(st.Files, FileStatus{Path: path, OrigPath: orig, Status: string(x), Staged: true})
			}
			if y != '.' {
				st.Files = append(st.Files, FileStatus{Path: path, Status: string(y)})
			}
		case strings.HasPrefix(r, "u "):
			// u XY sub m1 m2 m3 mW h1 h2 h3 path
			f := strings.SplitN(r, " ", 11)
			if len(f) == 11 {
				st.Files = append(st.Files, FileStatus{Path: f[10], Status: "U"})
			}
		case strings.HasPrefix(r, "? "):
			st.Files = append(st.Files, FileStatus{Path: r[2:], Status: "?"})
		}
	}
}

func nonEmptyLines(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if strings.TrimSpace(l) != "" {
			out = append(out, strings.TrimRight(l, "\r"))
		}
	}
	return out
}

// DiffOptions selects which diff of a path to produce.
type DiffOptions struct {
	Staged    bool
	Untracked bool
	Commit    string
	Context   int
}

// Diff returns a unified diff for one path.
func Diff(ctx context.Context, dir, path string, o DiffOptions) (string, error) {
	u := "-U3"
	if o.Context > 0 {
		u = "-U" + strconv.Itoa(o.Context)
	}
	switch {
	case o.Commit != "":
		return runGit(ctx, dir, "show", "--format=", u, "--diff-merges=first-parent", "-M", o.Commit, "--", path)
	case o.Untracked:
		// Exit code 1 just means "there are differences".
		return git(ctx, dir, runOpts{okExitCode: 1}, "diff", u, "--no-index", "--", os.DevNull, path)
	case o.Staged:
		return runGit(ctx, dir, "diff", u, "--cached", "-M", "--", path)
	default:
		return runGit(ctx, dir, "diff", u, "--", path)
	}
}

// BranchInfo describes a local branch.
type BranchInfo struct {
	Name           string `json:"name"`
	Upstream       string `json:"upstream"`
	LastCommit     string `json:"lastCommit"` // subject
	Ahead          int    `json:"ahead"`
	Behind         int    `json:"behind"`
	LastCommitTime int64  `json:"lastCommitTime"` // unix seconds
	Current        bool   `json:"current"`
}

// BranchList returns local branches (with tracking info) and remote branches.
func BranchList(ctx context.Context, dir string) (locals []BranchInfo, remotes []string, err error) {
	out, err := runGit(ctx, dir, "for-each-ref",
		"--format=%(HEAD)\x1f%(refname:short)\x1f%(upstream:short)\x1f%(upstream:track,nobracket)\x1f%(subject)\x1f%(committerdate:unix)",
		"--sort=-committerdate", "refs/heads")
	if err != nil {
		return nil, nil, err
	}
	for _, line := range nonEmptyLines(out) {
		f := strings.Split(line, "\x1f")
		if len(f) < 6 {
			continue
		}
		b := BranchInfo{Current: f[0] == "*", Name: f[1], Upstream: f[2], LastCommit: f[4]}
		b.LastCommitTime, _ = strconv.ParseInt(f[5], 10, 64)
		for _, part := range strings.Split(f[3], ",") {
			part = strings.TrimSpace(part)
			if v, ok := strings.CutPrefix(part, "ahead "); ok {
				b.Ahead, _ = strconv.Atoi(v)
			}
			if v, ok := strings.CutPrefix(part, "behind "); ok {
				b.Behind, _ = strconv.Atoi(v)
			}
		}
		locals = append(locals, b)
	}
	out, err = runGit(ctx, dir, "for-each-ref", "--format=%(refname:short)", "--sort=refname", "refs/remotes")
	if err == nil {
		for _, r := range nonEmptyLines(out) {
			if !strings.HasSuffix(r, "/HEAD") && strings.Contains(r, "/") {
				remotes = append(remotes, r)
			}
		}
	}
	return locals, remotes, nil
}

// Checkout switches to a branch/commit, optionally creating a branch.
// Checking out a remote branch ("origin/feat") creates or reuses the local
// tracking branch "feat".
func Checkout(ctx context.Context, dir, target string, create bool, startPoint string) error {
	if create {
		args := []string{"checkout", "-b", target}
		if startPoint != "" {
			args = append(args, startPoint)
		}
		_, err := runGit(ctx, dir, args...)
		return err
	}
	if _, err := runGit(ctx, dir, "rev-parse", "--verify", "--quiet", "refs/remotes/"+target); err == nil {
		local := target[strings.Index(target, "/")+1:]
		if _, err := runGit(ctx, dir, "rev-parse", "--verify", "--quiet", "refs/heads/"+local); err == nil {
			_, err := runGit(ctx, dir, "checkout", local)
			return err
		}
		_, err := runGit(ctx, dir, "checkout", "--track", target)
		return err
	}
	_, err := runGit(ctx, dir, "checkout", target)
	return err
}

func hasCommits(ctx context.Context, dir string) bool {
	_, err := runGit(ctx, dir, "rev-parse", "--verify", "--quiet", "HEAD")
	return err == nil
}

// StagePaths stages/unstages paths, or everything with all.
func StagePaths(ctx context.Context, dir string, paths []string, all, unstage bool) error {
	if !all && len(paths) == 0 {
		return nil
	}
	if !unstage {
		args := []string{"add", "-A", "--"}
		if all {
			args = append(args, ".")
		} else {
			args = append(args, paths...)
		}
		_, err := runGit(ctx, dir, args...)
		return err
	}
	if !hasCommits(ctx, dir) {
		// No HEAD to restore from: drop the paths from the index instead.
		args := []string{"rm", "-r", "--cached", "-q", "--"}
		if all {
			args = append(args, ".")
		} else {
			args = append(args, paths...)
		}
		_, err := runGit(ctx, dir, args...)
		return err
	}
	args := []string{"restore", "--staged", "--"}
	if all {
		args = append(args, ".")
	} else {
		args = append(args, paths...)
	}
	_, err := runGit(ctx, dir, args...)
	return err
}

// Discard throws away worktree changes: tracked paths are restored from the
// index, untracked paths are deleted.
func Discard(ctx context.Context, dir string, paths []string, all, includeUntracked bool) error {
	if all {
		if hasCommits(ctx, dir) {
			if _, err := runGit(ctx, dir, "restore", "--worktree", "--", "."); err != nil {
				return err
			}
		}
		if includeUntracked {
			_, err := runGit(ctx, dir, "clean", "-fd", "--", ".")
			return err
		}
		return nil
	}
	st, err := StatusOf(ctx, dir)
	if err != nil {
		return err
	}
	untracked := map[string]bool{}
	for _, f := range st.Files {
		if f.Status == "?" {
			untracked[f.Path] = true
		}
	}
	var tracked, clean []string
	for _, p := range paths {
		if untracked[p] {
			clean = append(clean, p)
		} else {
			tracked = append(tracked, p)
		}
	}
	if len(tracked) > 0 {
		if _, err := runGit(ctx, dir, append([]string{"restore", "--worktree", "--"}, tracked...)...); err != nil {
			return err
		}
	}
	if len(clean) > 0 {
		if _, err := runGit(ctx, dir, append([]string{"clean", "-f", "--"}, clean...)...); err != nil {
			return err
		}
	}
	return nil
}

// ApplyPatch applies a (partial) patch to the index or worktree; used for
// staging, unstaging and discarding individual hunks.
func ApplyPatch(ctx context.Context, dir, patch string, cached, reverse bool) error {
	args := []string{"apply", "--whitespace=nowarn", "--recount"}
	if cached {
		args = append(args, "--cached")
	}
	if reverse {
		args = append(args, "--reverse")
	}
	args = append(args, "-")
	_, err := git(ctx, dir, runOpts{stdin: patch}, args...)
	return err
}

// CommitOptions controls Commit.
type CommitOptions struct {
	Paths      []string
	StageFirst bool
	Amend      bool
	All        bool
	Signoff    bool
}

// CommitWith commits staged changes and returns the short hash.
func CommitWith(ctx context.Context, dir, message string, o CommitOptions) (string, error) {
	if strings.TrimSpace(message) == "" && !o.Amend {
		return "", fmt.Errorf("empty commit message")
	}
	if o.All {
		if _, err := runGit(ctx, dir, "add", "-A"); err != nil {
			return "", err
		}
	} else if o.StageFirst && len(o.Paths) > 0 {
		if _, err := runGit(ctx, dir, append([]string{"add", "-A", "--"}, o.Paths...)...); err != nil {
			return "", err
		}
	}
	args := []string{"commit"}
	if o.Amend {
		args = append(args, "--amend")
	}
	if o.Signoff {
		args = append(args, "--signoff")
	}
	if strings.TrimSpace(message) == "" {
		args = append(args, "--no-edit")
	} else {
		args = append(args, "-F", "-") // message via stdin: any length/characters
	}
	if _, err := git(ctx, dir, runOpts{stdin: message}, args...); err != nil {
		return "", err
	}
	out, err := runGit(ctx, dir, "rev-parse", "--short", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// RemoteOptions controls Remote.
type RemoteOptions struct {
	Op, Remote, Branch                    string
	Rebase, SetUpstream, Force, All, Tags bool
}

// Remote runs fetch/pull/push and returns git's combined output.
func Remote(ctx context.Context, dir string, o RemoteOptions) (string, error) {
	switch o.Op {
	case "fetch":
		args := []string{"fetch", "--prune"}
		if o.All || o.Remote == "" {
			args = append(args, "--all")
		} else {
			args = append(args, o.Remote)
		}
		return combined(ctx, dir, remoteTimeout, args...)
	case "pull":
		args := []string{"pull"}
		if o.Rebase {
			args = append(args, "--rebase")
		} else {
			args = append(args, "--no-rebase")
		}
		if o.Remote != "" {
			args = append(args, o.Remote)
			if o.Branch != "" {
				args = append(args, o.Branch)
			}
		}
		return combined(ctx, dir, remoteTimeout, args...)
	case "push":
		args := []string{"push"}
		if o.Force {
			args = append(args, "--force-with-lease")
		}
		if o.Tags {
			args = append(args, "--tags")
		}
		if o.SetUpstream {
			remote := o.Remote
			if remote == "" {
				remote = defaultRemote(ctx, dir)
			}
			if remote == "" {
				return "", fmt.Errorf("no remote configured — add one with `git remote add origin <url>`")
			}
			branch := o.Branch
			if branch == "" {
				out, err := runGit(ctx, dir, "rev-parse", "--abbrev-ref", "HEAD")
				if err != nil {
					return "", err
				}
				branch = strings.TrimSpace(out)
			}
			args = append(args, "-u", remote, branch)
		} else if o.Remote != "" {
			args = append(args, o.Remote)
			if o.Branch != "" {
				args = append(args, o.Branch)
			}
		}
		return combined(ctx, dir, remoteTimeout, args...)
	}
	return "", fmt.Errorf("unknown remote op %q", o.Op)
}

func defaultRemote(ctx context.Context, dir string) string {
	out, err := runGit(ctx, dir, "remote")
	if err != nil {
		return ""
	}
	remotes := nonEmptyLines(out)
	for _, r := range remotes {
		if r == "origin" {
			return r
		}
	}
	if len(remotes) > 0 {
		return remotes[0]
	}
	return ""
}

// CommitInfo is one log entry.
type CommitInfo struct {
	Hash      string   `json:"hash"`
	ShortHash string   `json:"shortHash"`
	Author    string   `json:"author"`
	Email     string   `json:"email"`
	Subject   string   `json:"subject"`
	Parents   []string `json:"parents"`
	Refs      []string `json:"refs"`
	Time      int64    `json:"time"` // unix seconds
}

const logFormat = "%H\x1f%h\x1f%P\x1f%an\x1f%ae\x1f%at\x1f%D\x1f%s\x1e"

func parseLog(out string) []CommitInfo {
	var commits []CommitInfo
	for _, rec := range strings.Split(out, "\x1e") {
		rec = strings.TrimLeft(rec, "\n")
		if rec == "" {
			continue
		}
		f := strings.Split(rec, "\x1f")
		if len(f) < 8 {
			continue
		}
		c := CommitInfo{Hash: f[0], ShortHash: f[1], Author: f[3], Email: f[4], Subject: f[7]}
		if f[2] != "" {
			c.Parents = strings.Fields(f[2])
		}
		c.Time, _ = strconv.ParseInt(f[5], 10, 64)
		for _, r := range strings.Split(f[6], ", ") {
			if r = strings.TrimSpace(r); r != "" {
				c.Refs = append(c.Refs, r)
			}
		}
		commits = append(commits, c)
	}
	return commits
}

// LogOptions controls Log.
type LogOptions struct {
	Limit, Skip int
	Path, Query string
	All         bool
}

// Log lists commits, newest first (topological order for graph drawing).
func Log(ctx context.Context, dir string, o LogOptions) ([]CommitInfo, error) {
	if !hasCommits(ctx, dir) {
		return nil, nil
	}
	if o.Limit <= 0 {
		o.Limit = 200
	}
	args := []string{"log", "--topo-order", "--format=" + logFormat, "-n", strconv.Itoa(o.Limit)}
	if o.Skip > 0 {
		args = append(args, "--skip", strconv.Itoa(o.Skip))
	}
	if o.All {
		args = append(args, "--all")
	}
	if o.Query != "" {
		args = append(args, "--regexp-ignore-case", "--fixed-strings", "--grep="+o.Query)
	}
	if o.Path != "" {
		args = append(args, "--follow", "--", o.Path)
	}
	out, err := runGit(ctx, dir, args...)
	if err != nil {
		return nil, err
	}
	return parseLog(out), nil
}

// CommitDetails is GitShow's result.
type CommitDetails struct {
	Commit    CommitInfo   `json:"commit"`
	Body      string       `json:"body"`
	Files     []FileStatus `json:"files"`
	Additions int          `json:"additions"`
	Deletions int          `json:"deletions"`
}

// Show returns a commit's message, changed files and line stats.
func Show(ctx context.Context, dir, hash string) (*CommitDetails, error) {
	out, err := runGit(ctx, dir, "show", "--no-patch", "--format="+logFormat, hash)
	if err != nil {
		return nil, err
	}
	commits := parseLog(out)
	if len(commits) == 0 {
		return nil, fmt.Errorf("commit %s not found", hash)
	}
	d := &CommitDetails{Commit: commits[0]}
	if body, err := runGit(ctx, dir, "show", "--no-patch", "--format=%B", hash); err == nil {
		d.Body = strings.TrimRight(body, "\n")
	}
	if out, err := runGit(ctx, dir, "show", "--format=", "--name-status", "-z", "-M", "--diff-merges=first-parent", hash); err == nil {
		f := strings.Split(out, "\x00")
		for i := 0; i < len(f); i++ {
			st := strings.TrimSpace(f[i])
			if st == "" || i+1 >= len(f) {
				continue
			}
			fs := FileStatus{Status: st[:1]}
			if (st[0] == 'R' || st[0] == 'C') && i+2 < len(f) {
				fs.OrigPath, fs.Path = f[i+1], f[i+2]
				i += 2
			} else {
				fs.Path = f[i+1]
				i++
			}
			d.Files = append(d.Files, fs)
		}
	}
	if out, err := runGit(ctx, dir, "show", "--format=", "--numstat", "--diff-merges=first-parent", hash); err == nil {
		for _, l := range nonEmptyLines(out) {
			f := strings.Fields(l)
			if len(f) >= 2 {
				a, _ := strconv.Atoi(f[0]) // "-" for binary => 0
				r, _ := strconv.Atoi(f[1])
				d.Additions += a
				d.Deletions += r
			}
		}
	}
	return d, nil
}

// StashEntry is one `git stash list` entry.
type StashEntry struct {
	Index   int    `json:"index"`
	Message string `json:"message"`
	Time    int64  `json:"time"` // unix seconds
}

// Stash runs a stash operation and returns the (updated) stash list.
func Stash(ctx context.Context, dir, op string, index int, message string, includeUntracked bool) ([]StashEntry, error) {
	ref := fmt.Sprintf("stash@{%d}", index)
	var err error
	switch op {
	case "list", "":
	case "push":
		args := []string{"stash", "push"}
		if includeUntracked {
			args = append(args, "--include-untracked")
		}
		if message != "" {
			args = append(args, "-m", message)
		}
		_, err = runGit(ctx, dir, args...)
	case "pop", "apply", "drop":
		_, err = runGit(ctx, dir, "stash", op, ref)
	default:
		err = fmt.Errorf("unknown stash op %q", op)
	}
	entries, lerr := stashList(ctx, dir)
	if err != nil {
		return entries, err
	}
	return entries, lerr
}

func stashList(ctx context.Context, dir string) ([]StashEntry, error) {
	out, err := runGit(ctx, dir, "stash", "list", "--format=%gd\x1f%ct\x1f%gs")
	if err != nil {
		return nil, err
	}
	var entries []StashEntry
	for _, l := range nonEmptyLines(out) {
		f := strings.SplitN(l, "\x1f", 3)
		if len(f) < 3 {
			continue
		}
		e := StashEntry{Message: f[2]}
		fmt.Sscanf(f[0], "stash@{%d}", &e.Index)
		e.Time, _ = strconv.ParseInt(f[1], 10, 64)
		entries = append(entries, e)
	}
	return entries, nil
}

// Init creates a repository in dir.
func Init(ctx context.Context, dir string) error {
	_, err := runGit(ctx, dir, "init")
	return err
}

// BranchOp deletes, renames or merges a branch.
func BranchOp(ctx context.Context, dir, op, name, newName string, force bool) (string, error) {
	switch op {
	case "delete":
		flag := "-d"
		if force {
			flag = "-D"
		}
		return combined(ctx, dir, localTimeout, "branch", flag, name)
	case "rename":
		return combined(ctx, dir, localTimeout, "branch", "-m", name, newName)
	case "merge":
		return combined(ctx, dir, localTimeout, "merge", "--no-edit", name)
	}
	return "", fmt.Errorf("unknown branch op %q", op)
}

// CommitOp acts on a commit from history.
func CommitOp(ctx context.Context, dir, op, hash, name string) (string, error) {
	switch op {
	case "revert":
		return combined(ctx, dir, localTimeout, "revert", "--no-edit", hash)
	case "cherry-pick":
		return combined(ctx, dir, localTimeout, "cherry-pick", hash)
	case "reset-soft", "reset-mixed", "reset-hard":
		return combined(ctx, dir, localTimeout, "reset", "--"+strings.TrimPrefix(op, "reset-"), hash)
	case "tag":
		if name == "" {
			return "", fmt.Errorf("tag name required")
		}
		return combined(ctx, dir, localTimeout, "tag", name, hash)
	}
	return "", fmt.Errorf("unknown commit op %q", op)
}

// Conflict resolves a conflicted path or aborts/continues the operation.
func Conflict(ctx context.Context, dir, op, path string) (string, error) {
	switch op {
	case "ours", "theirs":
		if _, err := runGit(ctx, dir, "checkout", "--"+op, "--", path); err != nil {
			return "", err
		}
		_, err := runGit(ctx, dir, "add", "--", path)
		return "", err
	case "resolved":
		_, err := runGit(ctx, dir, "add", "--", path)
		return "", err
	case "abort", "continue":
		operation := Operation(ctx, dir)
		if operation == "" {
			return "", fmt.Errorf("no merge, rebase, cherry-pick or revert in progress")
		}
		if op == "continue" && operation == "merge" {
			// `merge --continue` refuses without an editor on some versions.
			return combined(ctx, dir, localTimeout, "commit", "--no-edit")
		}
		return combined(ctx, dir, localTimeout, operation, "--"+op)
	}
	return "", fmt.Errorf("unknown conflict op %q", op)
}

// PendingChanges is what a commit would include, for describing it.
type PendingChanges struct {
	Stat      string // `git diff --stat` summary of every file
	Patch     string // unified diff, cut at the byte limit
	Truncated bool
	Files     int
}

// ChangesToCommit collects the changes a commit would take: the index when
// stagedOnly, else everything (tracked edits and untracked files, like
// `commit -a` after `add -A`). The patch stops near maxBytes; Stat always
// covers every file. The index and worktree are left untouched.
func ChangesToCommit(ctx context.Context, dir string, stagedOnly bool, maxBytes int) (*PendingChanges, error) {
	pc := &PendingChanges{}
	var diffArgs []string
	switch {
	case stagedOnly:
		diffArgs = []string{"diff", "--cached", "-M"}
	case hasCommits(ctx, dir):
		diffArgs = []string{"diff", "HEAD", "-M"}
	default:
		// No HEAD yet: staged plus unstaged edits against the empty tree.
		empty, err := runGit(ctx, dir, "hash-object", "-t", "tree", os.DevNull)
		if err != nil {
			return nil, err
		}
		diffArgs = []string{"diff", strings.TrimSpace(empty), "-M"}
	}
	stat, err := runGit(ctx, dir, append(diffArgs, "--stat=100")...)
	if err != nil {
		return nil, err
	}
	patch, err := runGit(ctx, dir, append(diffArgs, "-U3")...)
	if err != nil {
		return nil, err
	}
	var b strings.Builder
	add := func(s string) {
		if b.Len()+len(s) > maxBytes {
			if room := maxBytes - b.Len(); room > 0 {
				b.WriteString(s[:room])
			}
			pc.Truncated = true
			return
		}
		b.WriteString(s)
	}
	add(patch)
	pc.Files = strings.Count(patch, "\ndiff --git ")
	if strings.HasPrefix(patch, "diff --git ") {
		pc.Files++
	}
	statLines := []string{strings.TrimRight(stat, "\n")}
	if !stagedOnly {
		st, err := StatusOf(ctx, dir)
		if err != nil {
			return nil, err
		}
		for _, f := range st.Files {
			if f.Status != "?" {
				continue
			}
			pc.Files++
			statLines = append(statLines, " "+f.Path+" (new, untracked)")
			if !pc.Truncated {
				d, err := Diff(ctx, dir, f.Path, DiffOptions{Untracked: true})
				if err == nil {
					add(d)
				}
			}
		}
	}
	pc.Stat = strings.TrimSpace(strings.Join(statLines, "\n"))
	pc.Patch = b.String()
	return pc, nil
}
