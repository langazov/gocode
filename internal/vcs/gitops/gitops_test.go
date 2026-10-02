package gitops

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

var ctx = context.Background()

func sh(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=T", "GIT_AUTHOR_EMAIL=t@e.x",
		"GIT_COMMITTER_NAME=T", "GIT_COMMITTER_EMAIL=t@e.x")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func write(t *testing.T, dir, name, content string) {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// newRepo creates a repo with identity configured and one commit.
func newRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	sh(t, dir, "init", "-q", "-b", "main")
	sh(t, dir, "config", "user.name", "T")
	sh(t, dir, "config", "user.email", "t@e.x")
	write(t, dir, "a.txt", "one\ntwo\nthree\nfour\nfive\nsix\nseven\neight\nnine\nten\n")
	sh(t, dir, "add", "-A")
	sh(t, dir, "commit", "-q", "-m", "init")
	return dir
}

func status(t *testing.T, dir string) *Status {
	t.Helper()
	st, err := StatusOf(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func find(st *Status, path string, staged bool) *FileStatus {
	for i := range st.Files {
		if st.Files[i].Path == path && st.Files[i].Staged == staged {
			return &st.Files[i]
		}
	}
	return nil
}

func TestStatusParsesAllKinds(t *testing.T) {
	dir := newRepo(t)
	write(t, dir, "a.txt", "changed\n")
	write(t, dir, "new dir/ünï code.txt", "x") // spaces + non-ASCII, untracked
	write(t, dir, "b.txt", "b")
	sh(t, dir, "add", "b.txt")
	sh(t, dir, "mv", "a.txt", "renamed.txt")

	st := status(t, dir)
	if !st.IsRepo || st.Branch != "main" || !st.HasCommits || len(st.Head) != 7 {
		t.Fatalf("header: %+v", st)
	}
	if f := find(st, "renamed.txt", true); f == nil || f.Status != "R" || f.OrigPath != "a.txt" {
		t.Errorf("rename: %+v", f)
	}
	if f := find(st, "b.txt", true); f == nil || f.Status != "A" {
		t.Errorf("added: %+v", f)
	}
	if f := find(st, "new dir/ünï code.txt", false); f == nil || f.Status != "?" {
		t.Errorf("untracked unicode path: %+v (files %+v)", f, st.Files)
	}
	if st.LastCommitMessage != "init" {
		t.Errorf("last message %q", st.LastCommitMessage)
	}
}

func TestNotARepoAndInit(t *testing.T) {
	dir := t.TempDir()
	if status(t, dir).IsRepo {
		t.Fatal("plain dir reported as repo")
	}
	if err := Init(ctx, dir); err != nil {
		t.Fatal(err)
	}
	st := status(t, dir)
	if !st.IsRepo || st.HasCommits {
		t.Fatalf("fresh repo: %+v", st)
	}
	// Staging/unstaging works before the first commit.
	write(t, dir, "f.txt", "f")
	if err := StagePaths(ctx, dir, nil, true, false); err != nil {
		t.Fatal(err)
	}
	if find(status(t, dir), "f.txt", true) == nil {
		t.Fatal("not staged")
	}
	if err := StagePaths(ctx, dir, []string{"f.txt"}, false, true); err != nil {
		t.Fatal(err)
	}
	if find(status(t, dir), "f.txt", true) != nil {
		t.Fatal("still staged")
	}
	if logs, err := Log(ctx, dir, LogOptions{}); err != nil || len(logs) != 0 {
		t.Fatalf("log on empty repo: %v %v", logs, err)
	}
}

func TestStageUnstageDiscard(t *testing.T) {
	dir := newRepo(t)
	write(t, dir, "a.txt", "changed\n")
	write(t, dir, "u.txt", "untracked")

	if err := StagePaths(ctx, dir, []string{"a.txt"}, false, false); err != nil {
		t.Fatal(err)
	}
	if find(status(t, dir), "a.txt", true) == nil {
		t.Fatal("a.txt not staged")
	}
	if err := StagePaths(ctx, dir, nil, true, true); err != nil {
		t.Fatal(err)
	}
	st := status(t, dir)
	if find(st, "a.txt", true) != nil || find(st, "a.txt", false) == nil {
		t.Fatalf("unstage all: %+v", st.Files)
	}
	if err := Discard(ctx, dir, []string{"a.txt", "u.txt"}, false, false); err != nil {
		t.Fatal(err)
	}
	if st := status(t, dir); len(st.Files) != 0 {
		t.Fatalf("discard left %+v", st.Files)
	}
	if _, err := os.Stat(filepath.Join(dir, "u.txt")); !os.IsNotExist(err) {
		t.Fatal("untracked file not deleted")
	}
}

func TestHunkStagingViaPatch(t *testing.T) {
	dir := newRepo(t)
	// Two separate hunks (lines 1 and 10).
	write(t, dir, "a.txt", "ONE\ntwo\nthree\nfour\nfive\nsix\nseven\neight\nnine\nTEN\n")
	diff, err := Diff(ctx, dir, "a.txt", DiffOptions{Context: 1})
	if err != nil {
		t.Fatal(err)
	}
	// Patch = header + first hunk only.
	idx := strings.Index(diff, "\n@@")
	second := strings.Index(diff[idx+1:], "\n@@")
	if idx < 0 || second < 0 {
		t.Fatalf("expected two hunks:\n%s", diff)
	}
	patch := diff[:idx+1+second+1]
	if err := ApplyPatch(ctx, dir, patch, true, false); err != nil {
		t.Fatalf("stage hunk: %v\n%s", err, patch)
	}
	staged, _ := Diff(ctx, dir, "a.txt", DiffOptions{Staged: true})
	if !strings.Contains(staged, "+ONE") || strings.Contains(staged, "+TEN") {
		t.Fatalf("staged diff should contain only hunk 1:\n%s", staged)
	}
	// Unstage it again.
	if err := ApplyPatch(ctx, dir, patch, true, true); err != nil {
		t.Fatal(err)
	}
	if staged, _ := Diff(ctx, dir, "a.txt", DiffOptions{Staged: true}); staged != "" {
		t.Fatalf("unstage hunk left:\n%s", staged)
	}
	// Discard hunk 1 from the worktree.
	if err := ApplyPatch(ctx, dir, patch, false, true); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "a.txt"))
	if !strings.HasPrefix(string(b), "one\n") || !strings.HasSuffix(string(b), "TEN\n") {
		t.Fatalf("discard hunk result:\n%s", b)
	}
}

func TestUntrackedDiff(t *testing.T) {
	dir := newRepo(t)
	write(t, dir, "n.txt", "hello\n")
	d, err := Diff(ctx, dir, "n.txt", DiffOptions{Untracked: true})
	if err != nil || !strings.Contains(d, "+hello") {
		t.Fatalf("untracked diff %q err %v", d, err)
	}
}

func TestCommitAmendLogShow(t *testing.T) {
	dir := newRepo(t)
	write(t, dir, "c.txt", "c\n")
	h, err := CommitWith(ctx, dir, "add c\n\nbody line", CommitOptions{All: true})
	if err != nil || h == "" {
		t.Fatalf("commit: %q %v", h, err)
	}
	if _, err := CommitWith(ctx, dir, "add c (amended)", CommitOptions{Amend: true}); err != nil {
		t.Fatal(err)
	}
	logs, err := Log(ctx, dir, LogOptions{})
	if err != nil || len(logs) != 2 || logs[0].Subject != "add c (amended)" {
		t.Fatalf("log: %+v %v", logs, err)
	}
	if len(logs[0].Parents) != 1 || logs[0].Parents[0] != logs[1].Hash {
		t.Errorf("parents: %+v", logs[0].Parents)
	}
	if !strings.Contains(strings.Join(logs[0].Refs, ","), "HEAD -> main") {
		t.Errorf("refs: %v", logs[0].Refs)
	}
	d, err := Show(ctx, dir, logs[0].Hash)
	if err != nil || len(d.Files) != 1 || d.Files[0].Path != "c.txt" || d.Additions != 1 {
		t.Fatalf("show: %+v %v", d, err)
	}
	if q, _ := Log(ctx, dir, LogOptions{Query: "AMENDED"}); len(q) != 1 {
		t.Errorf("query: %+v", q)
	}
	if _, err := CommitWith(ctx, dir, "  ", CommitOptions{}); err == nil {
		t.Error("empty message accepted")
	}
}

func TestStash(t *testing.T) {
	dir := newRepo(t)
	write(t, dir, "a.txt", "dirty\n")
	write(t, dir, "u.txt", "u")
	entries, err := Stash(ctx, dir, "push", 0, "wip", true)
	if err != nil || len(entries) != 1 || !strings.Contains(entries[0].Message, "wip") {
		t.Fatalf("push: %+v %v", entries, err)
	}
	if st := status(t, dir); len(st.Files) != 0 || st.StashCount != 1 {
		t.Fatalf("after push: %+v", st)
	}
	if entries, err = Stash(ctx, dir, "pop", 0, "", false); err != nil || len(entries) != 0 {
		t.Fatalf("pop: %+v %v", entries, err)
	}
	if find(status(t, dir), "u.txt", false) == nil {
		t.Fatal("untracked file not restored")
	}
}

// remotePair returns a working repo cloned from a bare "remote", plus a second
// clone to make remote-side changes.
func remotePair(t *testing.T) (work, other string) {
	t.Helper()
	seed := newRepo(t)
	bare := filepath.Join(t.TempDir(), "remote.git")
	sh(t, seed, "clone", "-q", "--bare", seed, bare)
	work, other = t.TempDir(), t.TempDir()
	for _, d := range []string{work, other} {
		sh(t, d, "clone", "-q", bare, ".")
		sh(t, d, "config", "user.name", "T")
		sh(t, d, "config", "user.email", "t@e.x")
	}
	return work, other
}

func TestRemotesBranchesAheadBehind(t *testing.T) {
	work, other := remotePair(t)

	// Remote gets a new commit -> behind after fetch.
	write(t, other, "r.txt", "r")
	sh(t, other, "add", "-A")
	sh(t, other, "commit", "-q", "-m", "remote change")
	sh(t, other, "push", "-q")
	if _, err := Remote(ctx, work, RemoteOptions{Op: "fetch"}); err != nil {
		t.Fatal(err)
	}
	st := status(t, work)
	if st.Upstream != "origin/main" || st.Behind != 1 || st.Ahead != 0 {
		t.Fatalf("after fetch: upstream=%q ahead=%d behind=%d", st.Upstream, st.Ahead, st.Behind)
	}
	if _, err := Remote(ctx, work, RemoteOptions{Op: "pull"}); err != nil {
		t.Fatal(err)
	}
	// Local commit -> ahead; push clears it.
	write(t, work, "l.txt", "l")
	if _, err := CommitWith(ctx, work, "local", CommitOptions{All: true}); err != nil {
		t.Fatal(err)
	}
	if st := status(t, work); st.Ahead != 1 {
		t.Fatalf("ahead = %d", st.Ahead)
	}
	if _, err := Remote(ctx, work, RemoteOptions{Op: "push"}); err != nil {
		t.Fatal(err)
	}
	if st := status(t, work); st.Ahead != 0 || st.Behind != 0 {
		t.Fatalf("after push: %+v", st)
	}

	// Publish a new branch (push -u), then see it as a remote branch.
	if err := Checkout(ctx, work, "feature", true, ""); err != nil {
		t.Fatal(err)
	}
	if st := status(t, work); st.Upstream != "" {
		t.Fatalf("new branch should be unpublished, upstream %q", st.Upstream)
	}
	if _, err := Remote(ctx, work, RemoteOptions{Op: "push", SetUpstream: true}); err != nil {
		t.Fatal(err)
	}
	if st := status(t, work); st.Upstream != "origin/feature" {
		t.Fatalf("publish: upstream %q", st.Upstream)
	}
	locals, remotes, err := BranchList(ctx, work)
	if err != nil || len(locals) != 2 || !contains(remotes, "origin/feature") {
		t.Fatalf("branches: %+v %v %v", locals, remotes, err)
	}

	// Checking out a remote-only branch creates a tracking branch.
	sh(t, other, "checkout", "-q", "-b", "other-feat")
	sh(t, other, "push", "-q", "-u", "origin", "other-feat")
	if _, err := Remote(ctx, work, RemoteOptions{Op: "fetch"}); err != nil {
		t.Fatal(err)
	}
	if err := Checkout(ctx, work, "origin/other-feat", false, ""); err != nil {
		t.Fatal(err)
	}
	if st := status(t, work); st.Branch != "other-feat" || st.Upstream != "origin/other-feat" {
		t.Fatalf("tracking checkout: %+v", st)
	}

	// Rename + delete.
	if _, err := BranchOp(ctx, work, "rename", "feature", "feature2", false); err != nil {
		t.Fatal(err)
	}
	if _, err := BranchOp(ctx, work, "delete", "feature2", "", true); err != nil {
		t.Fatal(err)
	}
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

func TestMergeConflictResolveAndAbort(t *testing.T) {
	dir := newRepo(t)
	sh(t, dir, "checkout", "-q", "-b", "topic")
	write(t, dir, "a.txt", "topic\n")
	sh(t, dir, "commit", "-q", "-am", "topic")
	sh(t, dir, "checkout", "-q", "main")
	write(t, dir, "a.txt", "main\n")
	sh(t, dir, "commit", "-q", "-am", "main")

	if _, err := BranchOp(ctx, dir, "merge", "topic", "", false); err == nil {
		t.Fatal("expected conflict")
	}
	st := status(t, dir)
	if st.Operation != "merge" {
		t.Fatalf("operation = %q", st.Operation)
	}
	if f := find(st, "a.txt", false); f == nil || f.Status != "U" {
		t.Fatalf("conflict entry: %+v", st.Files)
	}
	if _, err := Conflict(ctx, dir, "theirs", "a.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := Conflict(ctx, dir, "continue", ""); err != nil {
		t.Fatal(err)
	}
	if st := status(t, dir); st.Operation != "" {
		t.Fatalf("still in %q", st.Operation)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "a.txt"))
	if string(b) != "topic\n" {
		t.Fatalf("theirs not applied: %q", b)
	}

	// Second conflict, aborted.
	sh(t, dir, "checkout", "-q", "-b", "topic2", "HEAD~1")
	write(t, dir, "a.txt", "topic2\n")
	sh(t, dir, "commit", "-q", "-am", "t2")
	sh(t, dir, "checkout", "-q", "main")
	_, _ = BranchOp(ctx, dir, "merge", "topic2", "", false)
	if _, err := Conflict(ctx, dir, "abort", ""); err != nil {
		t.Fatal(err)
	}
	if st := status(t, dir); st.Operation != "" || len(st.Files) != 0 {
		t.Fatalf("abort: %+v", st)
	}
}

func TestCommitOps(t *testing.T) {
	dir := newRepo(t)
	write(t, dir, "x.txt", "x\n")
	h, _ := CommitWith(ctx, dir, "add x", CommitOptions{All: true})
	if _, err := CommitOp(ctx, dir, "revert", h, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "x.txt")); !os.IsNotExist(err) {
		t.Fatal("revert did not remove x.txt")
	}
	if _, err := CommitOp(ctx, dir, "tag", h, "v1"); err != nil {
		t.Fatal(err)
	}
	if _, err := CommitOp(ctx, dir, "reset-hard", h, ""); err != nil {
		t.Fatal(err)
	}
	logs, _ := Log(ctx, dir, LogOptions{})
	if logs[0].ShortHash != h || !strings.Contains(strings.Join(logs[0].Refs, ","), "tag: v1") {
		t.Fatalf("after reset: %+v", logs[0])
	}
}

func TestChangesToCommit(t *testing.T) {
	dir := newRepo(t)
	write(t, dir, "a.txt", "one\nTWO\nthree\nfour\nfive\nsix\nseven\neight\nnine\nten\n")
	write(t, dir, "staged.txt", "staged\n")
	sh(t, dir, "add", "staged.txt")
	write(t, dir, "new.txt", "brand new\n")

	staged, err := ChangesToCommit(ctx, dir, true, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(staged.Patch, "+staged") || strings.Contains(staged.Patch, "TWO") || strings.Contains(staged.Stat, "new.txt") {
		t.Fatalf("staged only:\n%s\n%s", staged.Stat, staged.Patch)
	}

	all, err := ChangesToCommit(ctx, dir, false, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"+TWO", "+staged", "+brand new"} {
		if !strings.Contains(all.Patch, want) {
			t.Errorf("all changes missing %q:\n%s", want, all.Patch)
		}
	}
	if all.Files != 3 || !strings.Contains(all.Stat, "new.txt (new, untracked)") {
		t.Errorf("files=%d stat:\n%s", all.Files, all.Stat)
	}
	if st := status(t, dir); len(st.Files) != 3 {
		t.Errorf("index or worktree changed: %+v", st.Files)
	}

	cut, _ := ChangesToCommit(ctx, dir, false, 40)
	if !cut.Truncated || len(cut.Patch) != 40 || !strings.Contains(cut.Stat, "new.txt") {
		t.Errorf("truncation: %v %d %q", cut.Truncated, len(cut.Patch), cut.Stat)
	}

	// A repo without commits describes everything against the empty tree.
	fresh := t.TempDir()
	sh(t, fresh, "init", "-q", "-b", "main")
	write(t, fresh, "x.go", "package x\n")
	c, err := ChangesToCommit(ctx, fresh, false, 1<<20)
	if err != nil || !strings.Contains(c.Patch, "+package x") {
		t.Fatalf("fresh repo: %v %q", err, c.Patch)
	}
}
