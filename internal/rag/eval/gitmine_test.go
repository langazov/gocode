package eval

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.com",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func writeAndCommit(t *testing.T, dir, name, content, message string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-q", "-m", message)
}

func TestMineGoldSetFindsQueryAndRegion(t *testing.T) {
	dir := t.TempDir()
	runGit(t, dir, "init", "-q", "-b", "main")
	writeAndCommit(t, dir, "auth.go", "package auth\n\nfunc Login() {}\n", "wip")
	writeAndCommit(t, dir, "auth.go",
		"package auth\n\nfunc Login() {}\n\nfunc Logout() {\n\t// signs the user out\n}\n",
		"add session logout handling")

	pairs, err := MineGoldSet(context.Background(), dir, MineOptions{})
	if err != nil {
		t.Fatal(err)
	}

	var found bool
	for _, p := range pairs {
		if p.Query == "wip" {
			t.Fatalf("subject %q is shorter than MinSubjectLen and should have been filtered", p.Query)
		}
		if p.Query == "add session logout handling" {
			found = true
			if len(p.Relevant) != 1 || p.Relevant[0].Path != "auth.go" {
				t.Fatalf("unexpected relevant regions: %+v", p.Relevant)
			}
			if p.Relevant[0].StartLine < 1 || p.Relevant[0].EndLine > 7 {
				t.Fatalf("region out of bounds: %+v", p.Relevant[0])
			}
		}
	}
	if !found {
		t.Fatalf("expected a gold pair for the logout commit, got %+v", pairs)
	}
}

func TestMineGoldSetSkipsWideCommits(t *testing.T) {
	dir := t.TempDir()
	runGit(t, dir, "init", "-q", "-b", "main")
	for i := range 5 {
		path := filepath.Join(dir, "f"+string(rune('0'+i))+".go")
		if err := os.WriteFile(path, []byte("package p\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-q", "-m", "add five files at once, a wide mechanical change")

	pairs, err := MineGoldSet(context.Background(), dir, MineOptions{MaxFilesPerCommit: 3})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range pairs {
		if p.Query == "add five files at once, a wide mechanical change" {
			t.Fatalf("a commit touching 5 files should be dropped under MaxFilesPerCommit=3")
		}
	}
}

func TestMineGoldSetDropsRegionsPastCurrentFileLength(t *testing.T) {
	dir := t.TempDir()
	runGit(t, dir, "init", "-q", "-b", "main")
	lines := ""
	for i := 0; i < 20; i++ {
		lines += "line\n"
	}
	writeAndCommit(t, dir, "big.go", lines, "wip")
	writeAndCommit(t, dir, "big.go", lines+"tail line added at the very end\n", "append one line at the tail of the file")
	// Now shrink the file back down so the appended region no longer exists.
	writeAndCommit(t, dir, "big.go", "package p\n", "shrink the file back down to nothing")

	pairs, err := MineGoldSet(context.Background(), dir, MineOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range pairs {
		if p.Query == "append one line at the tail of the file" {
			t.Fatalf("region referring to a line the file no longer has should have been dropped, got %+v", p.Relevant)
		}
	}
}
