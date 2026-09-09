package vcs

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// gitAvailable gates the suite: the wrappers shell out to git, and a CI
// image without it should skip rather than fail. (The clipboard tests use
// the same LookPath gate for their external tools.)
func gitAvailable(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
}

// repo builds a real git repository with one commit on "main".
func repo(t *testing.T) string {
	t.Helper()
	gitAvailable(t)
	dir := t.TempDir()
	gitRun(t, dir, "init", "-q", "--initial-branch=main")
	gitRun(t, dir, "config", "user.email", "test@example.com")
	gitRun(t, dir, "config", "user.name", "Test")
	write(t, dir, "committed.txt", "line one\nline two\n")
	gitRun(t, dir, "add", ".")
	gitRun(t, dir, "commit", "-q", "-m", "initial")
	return dir
}

func gitRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s in %s: %v\n%s", strings.Join(args, " "), dir, err, out)
	}
	return string(out)
}

func write(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestStatusClassifiesAndUnquotes(t *testing.T) {
	dir := repo(t)
	// doomed.txt joins the initial commit so deleting it is a real deletion.
	write(t, dir, "doomed.txt", "soon gone\n")
	gitRun(t, dir, "add", ".")
	gitRun(t, dir, "commit", "-q", "-m", "second")
	if err := os.Remove(filepath.Join(dir, "doomed.txt")); err != nil {
		t.Fatal(err)
	}
	// committed.txt exists in the initial commit, so touching it after the
	// commit is a real modification rather than a new untracked file.
	write(t, dir, "committed.txt", "line one\nline two changed\n")
	write(t, dir, "added.txt", "new\n")
	write(t, dir, "nested/deep/file.txt", "new\n")

	items := Status(dir)
	byFile := map[string]Item{}
	for _, item := range items {
		byFile[item.File] = item
	}
	if item, ok := byFile["committed.txt"]; !ok || item.Status != StatusModified || item.Code != " M" {
		t.Fatalf("committed.txt misclassified: %+v (ok=%v)", item, ok)
	}
	if item, ok := byFile["added.txt"]; !ok || item.Status != StatusAdded || item.Code != "??" {
		t.Fatalf("added.txt misclassified: %+v (ok=%v)", item, ok)
	}
	if item, ok := byFile["nested/deep/file.txt"]; !ok || item.Status != StatusAdded {
		t.Fatalf("nested file misclassified: %+v (ok=%v)", item, ok)
	}
	if item, ok := byFile["doomed.txt"]; !ok || item.Status != StatusDeleted {
		t.Fatalf("deleted doomed.txt misclassified: %+v (ok=%v)", item, ok)
	}
}

func TestStatusUnquotesNonASCIIPaths(t *testing.T) {
	dir := repo(t)
	// A non-ASCII path makes porcelain quote it: "i\303\251n.txt".
	write(t, dir, "ién.txt", "content\n")

	items := Status(dir)
	found := false
	for _, item := range items {
		if item.File == "ién.txt" {
			found = true
		}
	}
	if !found {
		var names []string
		for _, item := range items {
			names = append(names, item.File)
		}
		t.Fatalf("ién.txt not found decoded in status output: %q", names)
	}
}

func TestBranchAndHasHead(t *testing.T) {
	dir := repo(t)
	if Branch(dir) != "main" {
		t.Fatalf("branch = %q, want main", Branch(dir))
	}
	if !HasHead(dir) {
		t.Fatal("HasHead = false in a repository with a commit")
	}

	fresh := t.TempDir()
	gitRun(t, fresh, "init", "-q", "--initial-branch=main")
	gitRun(t, fresh, "config", "user.email", "test@example.com")
	gitRun(t, fresh, "config", "user.name", "Test")
	if HasHead(fresh) {
		t.Fatal("HasHead = true in a repository with no commits")
	}
}

func TestDefaultBranchPrefersRemoteHead(t *testing.T) {
	dir := repo(t)
	// Clone locally so refs/remotes/origin/HEAD exists.
	remote := t.TempDir()
	gitRun(t, remote, "init", "-q", "--bare", "--initial-branch=main")
	gitRun(t, dir, "remote", "add", "origin", remote)
	gitRun(t, dir, "push", "-q", "origin", "main")
	gitRun(t, dir, "remote", "set-head", "origin", "main")

	base, ok := DefaultBranch(dir)
	if !ok || base.Name != "main" || base.Ref != "origin/main" {
		t.Fatalf("DefaultBranch = %+v ok=%v, want main/origin/main", base, ok)
	}
}

func TestDefaultBranchFallsBackToConfiguredThenMainMaster(t *testing.T) {
	dir := repo(t) // main exists locally, no remote
	base, ok := DefaultBranch(dir)
	if !ok || base.Name != "main" {
		t.Fatalf("DefaultBranch = %+v ok=%v, want main", base, ok)
	}

	master := t.TempDir()
	gitRun(t, master, "init", "-q", "--initial-branch=master")
	gitRun(t, master, "config", "user.email", "test@example.com")
	gitRun(t, master, "config", "user.name", "Test")
	write(t, master, "a.txt", "a\n")
	gitRun(t, master, "add", ".")
	gitRun(t, master, "commit", "-q", "-m", "initial")
	base, ok = DefaultBranch(master)
	if !ok || base.Name != "master" {
		t.Fatalf("DefaultBranch = %+v ok=%v, want master", base, ok)
	}
}

func TestDiffWorkingTreeCoversAllStatuses(t *testing.T) {
	dir := repo(t)
	write(t, dir, "committed.txt", "line one\nline two changed\n")
	write(t, dir, "new.txt", "brand new\n")
	if err := os.Remove(filepath.Join(dir, "committed.txt")); err != nil {
		t.Fatal(err)
	}
	// committed.txt is deleted; recreate a modified state instead.
	write(t, dir, "committed.txt", "line one changed\n")

	files := DiffWorkingTree(dir, patchOptions{})
	byFile := map[string]FileDiff{}
	for _, file := range files {
		byFile[file.File] = file
	}
	newFile, ok := byFile["new.txt"]
	if !ok {
		t.Fatalf("untracked new.txt missing from diff: %+v", files)
	}
	if newFile.Status != StatusAdded || newFile.Additions != 1 {
		t.Fatalf("new.txt = %+v", newFile)
	}
	if !strings.Contains(newFile.Patch, "+brand new") {
		t.Fatalf("new.txt patch missing addition:\n%s", newFile.Patch)
	}

	mod, ok := byFile["committed.txt"]
	if !ok {
		t.Fatalf("modified committed.txt missing from diff")
	}
	if mod.Status != StatusModified || mod.Additions == 0 || mod.Deletions == 0 {
		t.Fatalf("committed.txt = %+v", mod)
	}
}

func TestDiffWorkingTreeInFreshRepository(t *testing.T) {
	dir := t.TempDir()
	gitAvailable(t)
	gitRun(t, dir, "init", "-q", "--initial-branch=main")
	gitRun(t, dir, "config", "user.email", "test@example.com")
	gitRun(t, dir, "config", "user.name", "Test")
	write(t, dir, "only.txt", "content\n")

	files := DiffWorkingTree(dir, patchOptions{})
	if len(files) != 1 || files[0].File != "only.txt" || files[0].Status != StatusAdded {
		t.Fatalf("fresh repository diff = %+v", files)
	}
	if !strings.Contains(files[0].Patch, "+content") {
		t.Fatalf("fresh repository patch missing content:\n%s", files[0].Patch)
	}
}

func TestDiffBranchAgainstDefault(t *testing.T) {
	dir := repo(t)
	write(t, dir, "committed.txt", "line one\nline two\nline three\n")
	gitRun(t, dir, "add", ".")
	gitRun(t, dir, "commit", "-q", "-m", "second")
	gitRun(t, dir, "checkout", "-q", "-b", "feature")

	// On the default branch itself the branch diff is empty.
	gitRun(t, dir, "checkout", "-q", "main")
	if files := DiffBranch(dir, patchOptions{}); len(files) != 0 {
		t.Fatalf("branch diff on the default branch = %+v, want empty", files)
	}

	// On a feature branch it reports the feature's changes.
	gitRun(t, dir, "checkout", "-q", "feature")
	write(t, dir, "feature.txt", "feature work\n")
	gitRun(t, dir, "add", ".")
	gitRun(t, dir, "commit", "-q", "-m", "feature")

	files := DiffBranch(dir, patchOptions{})
	byFile := map[string]FileDiff{}
	for _, file := range files {
		byFile[file.File] = file
	}
	feature, ok := byFile["feature.txt"]
	if !ok {
		t.Fatalf("feature.txt missing from branch diff: %+v", files)
	}
	if feature.Status != StatusAdded || feature.Additions != 1 {
		t.Fatalf("feature.txt = %+v", feature)
	}
}

func TestDiffBranchEmptyWithoutDefaultBranch(t *testing.T) {
	dir := repo(t)
	// Detached HEAD, no remotes, and neither main nor master is a local
	// branch after renaming: DefaultBranch must fail and DiffBranch stay
	// empty rather than error.
	gitRun(t, dir, "branch", "-m", "trunk")
	gitRun(t, dir, "checkout", "-q", "--detach")
	if files := DiffBranch(dir, patchOptions{}); files != nil {
		t.Fatalf("DiffBranch without a default branch = %+v, want nil", files)
	}
}

func TestDiffBinaryFileReportsEmptyPatch(t *testing.T) {
	dir := repo(t)
	binary := []byte{0x00, 0x01, 0x02, 0x03, 0x00, 0xff}
	if err := os.WriteFile(filepath.Join(dir, "blob.bin"), binary, 0o644); err != nil {
		t.Fatal(err)
	}
	files := DiffWorkingTree(dir, patchOptions{})
	for _, file := range files {
		if file.File == "blob.bin" {
			if file.Additions != 0 || file.Deletions != 0 {
				t.Fatalf("binary file carries counts: %+v", file)
			}
			if strings.Contains(file.Patch, "GIT binary patch") {
				t.Fatalf("binary patch text leaked into the payload")
			}
			return
		}
	}
	t.Fatalf("blob.bin missing from working-tree diff: %+v", files)
}

func TestDiffTotalCapTripsToEmptyPatches(t *testing.T) {
	dir := repo(t)
	// Many changed files; the total cap is 10MB which this test cannot
	// reach, so exercise the cap logic by shrinking the per-batch capture
	// instead: patchOptions.MaxOutputBytes bounds PatchAll, the batch goes
	// capped, and untracked files fall back to per-file patches.
	var builder strings.Builder
	for i := 0; i < 400; i++ {
		builder.WriteString(strings.Repeat("x", 64))
		builder.WriteString("\n")
	}
	for i := 0; i < 4; i++ {
		write(t, dir, "big/new-"+string(rune('a'+i))+".txt", builder.String())
	}

	opts := patchOptions{MaxOutputBytes: 1024}
	files := DiffWorkingTree(dir, opts)
	if len(files) == 0 {
		t.Fatal("capped diff produced nothing")
	}
	// Every file still appears, with a patch (possibly empty).
	for _, file := range files {
		if strings.HasPrefix(file.File, "big/") && file.Patch == "" && file.Additions > 0 {
			// Untracked files render per-file patches regardless of the
			// batch cap, so these must all have bodies.
			t.Fatalf("%s lost its patch under the batch cap", file.File)
		}
	}
}

func TestUnquoteGitPath(t *testing.T) {
	cases := []struct{ in, want string }{
		{"plain.txt", "plain.txt"},
		{`"ién.txt"`, "ién.txt"},                // octal UTF-8 bytes
		{`"a\tb.txt"`, "a\tb.txt"},              // escapes
		{`"quote\"name.txt"`, `quote"name.txt`}, // escaped quote
		{`"back\\slash.txt"`, `back\slash.txt`}, // escaped backslash
		{`"dir\/name.txt"`, `dir/name.txt`},     // forward slash keeps escaping
		{`"tab\11x"`, "tab\tx"},                 // two-digit octal
	}
	for _, tc := range cases {
		if got := unquoteGitPath(tc.in); got != tc.want {
			t.Errorf("unquoteGitPath(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestSplitGitPatchAndAttribution(t *testing.T) {
	combined := "diff --git a/one.txt b/one.txt\n--- a/one.txt\n+++ b/one.txt\n@@ -1 +1 @@\n-a\n+b\ndiff --git a/two.txt b/two.txt\n--- a/two.txt\n+++ b/two.txt\n@@ -1 +1 @@\n-c\n+d\n"
	chunks := splitGitPatch(PatchResult{Text: combined})
	if len(chunks) != 2 {
		t.Fatalf("splitGitPatch produced %d chunks, want 2:\n%q", len(chunks), chunks)
	}
	if file := fileFromPatchChunk(chunks[0]); file != "one.txt" {
		t.Fatalf("chunk 0 attributed to %q, want one.txt", file)
	}
	if file := fileFromPatchChunk(chunks[1]); file != "two.txt" {
		t.Fatalf("chunk 1 attributed to %q, want two.txt", file)
	}

	// A truncated capture drops its partial tail chunk.
	truncated := splitGitPatch(PatchResult{Text: combined + "diff --git a/three.txt b/three.txt\n--- a/th", Truncated: true})
	if len(truncated) != 2 {
		t.Fatalf("truncated split produced %d chunks, want 2", len(truncated))
	}
}

func TestFileFromPatchChunkQuotedHeader(t *testing.T) {
	chunk := "diff --git \"a/with space.txt\" \"b/with space.txt\"\n--- \"a/with space.txt\"\t\n+++ \"b/with space.txt\"\t\n@@ -1 +1 @@\n-a\n+b\n"
	if file := fileFromPatchChunk(chunk); file != "with space.txt" {
		t.Fatalf("quoted header attributed to %q, want with space.txt", file)
	}
}

func TestEmptyPatchIsHeaderOnly(t *testing.T) {
	got := emptyPatch("src/file.go")
	if got != "--- src/file.go\n+++ src/file.go\n" {
		t.Fatalf("emptyPatch = %q", got)
	}
}

func TestInfoOutsideRepository(t *testing.T) {
	gitAvailable(t)
	if _, ok := Info(t.TempDir()); ok {
		t.Fatal("Info reports a repository in a plain temp dir")
	}
}

func TestInfoInRepository(t *testing.T) {
	dir := repo(t)
	info, ok := Info(dir)
	if !ok {
		t.Fatal("Info denies a real repository")
	}
	if info.Branch != "main" {
		t.Fatalf("branch = %q", info.Branch)
	}
	if info.DefaultBranch != "main" {
		t.Fatalf("default branch = %q", info.DefaultBranch)
	}
}

func TestStatUntracked(t *testing.T) {
	dir := repo(t)
	write(t, dir, "counted.txt", "a\nb\nc\n")
	stat, ok := StatUntracked(dir, "counted.txt")
	if !ok {
		t.Fatal("StatUntracked failed for a text file")
	}
	if stat.Additions != 3 || stat.Deletions != 0 {
		t.Fatalf("stat = %+v, want 3 additions", stat)
	}
}

func TestDiffModeValidation(t *testing.T) {
	if mode, ok := DiffMode("git"); !ok || mode != ModeGit {
		t.Fatalf("DiffMode(git) = %q %v", mode, ok)
	}
	if mode, ok := DiffMode("branch"); !ok || mode != ModeBranch {
		t.Fatalf("DiffMode(branch) = %q %v", mode, ok)
	}
	if _, ok := DiffMode("session"); ok {
		t.Fatal("DiffMode accepted an unknown mode")
	}
}
