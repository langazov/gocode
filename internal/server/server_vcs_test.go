package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/langazov/gocode-go/internal/vcs"
)

// vcsTestRepo builds a git repository with one commit, the same fixture the
// internal/vcs tests use, gated on git being installed.
func vcsTestRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	run("init", "-q", "--initial-branch=main")
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("line one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-q", "-m", "initial")
	if err := os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("line one\nline two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "new.txt"), []byte("added\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestVcsInfoReportsBranches(t *testing.T) {
	dir := vcsTestRepo(t)
	server, _, _ := newTestServer(t)
	server.VCSWorkdir = dir

	rec := doJSON(t, server, http.MethodGet, "/api/vcs", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/vcs: %d %s", rec.Code, rec.Body.String())
	}
	var info vcs.RepoInfo
	if err := json.NewDecoder(rec.Body).Decode(&info); err != nil {
		t.Fatal(err)
	}
	if info.Branch != "main" {
		t.Fatalf("branch = %q, want main", info.Branch)
	}
	if info.DefaultBranch != "main" {
		t.Fatalf("defaultBranch = %q, want main", info.DefaultBranch)
	}
}

func TestVcsInfoOutsideRepositoryIsEmptyNotError(t *testing.T) {
	server, _, _ := newTestServer(t)
	server.VCSWorkdir = t.TempDir()

	rec := doJSON(t, server, http.MethodGet, "/api/vcs", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/vcs outside a repo: %d %s", rec.Code, rec.Body.String())
	}
	var info vcs.RepoInfo
	if err := json.NewDecoder(rec.Body).Decode(&info); err != nil {
		t.Fatal(err)
	}
	if info.Branch != "" || info.DefaultBranch != "" {
		t.Fatalf("info outside a repo = %+v, want empty", info)
	}
}

func TestVcsDiffWorkingTree(t *testing.T) {
	dir := vcsTestRepo(t)
	server, _, _ := newTestServer(t)
	server.VCSWorkdir = dir

	rec := doJSON(t, server, http.MethodGet, "/api/vcs/diff?mode=git", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/vcs/diff: %d %s", rec.Code, rec.Body.String())
	}
	var files []vcs.FileDiff
	if err := json.NewDecoder(rec.Body).Decode(&files); err != nil {
		t.Fatal(err)
	}
	byFile := map[string]vcs.FileDiff{}
	for _, file := range files {
		byFile[file.File] = file
	}
	if added, ok := byFile["new.txt"]; !ok || added.Status != vcs.StatusAdded || added.Additions != 1 {
		t.Fatalf("new.txt = %+v ok=%v", added, ok)
	}
	if modified, ok := byFile["hello.txt"]; !ok || modified.Status != vcs.StatusModified {
		t.Fatalf("hello.txt = %+v ok=%v", modified, ok)
	}
}

func TestVcsDiffDefaultsToGitMode(t *testing.T) {
	dir := vcsTestRepo(t)
	server, _, _ := newTestServer(t)
	server.VCSWorkdir = dir

	rec := doJSON(t, server, http.MethodGet, "/api/vcs/diff", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("modeless GET /api/vcs/diff: %d %s", rec.Code, rec.Body.String())
	}
	var files []vcs.FileDiff
	if err := json.NewDecoder(rec.Body).Decode(&files); err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 {
		t.Fatalf("default mode produced %d files, want 2: %+v", len(files), files)
	}
}

func TestVcsDiffRejectsUnknownMode(t *testing.T) {
	dir := vcsTestRepo(t)
	server, _, _ := newTestServer(t)
	server.VCSWorkdir = dir

	rec := doJSON(t, server, http.MethodGet, "/api/vcs/diff?mode=session", nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown mode: %d %s, want 400", rec.Code, rec.Body.String())
	}
	var payload map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if payload["error"] == "" {
		t.Fatalf("400 body missing error field: %s", rec.Body.String())
	}
}

func TestVcsDiffRejectsInvalidContext(t *testing.T) {
	server, _, _ := newTestServer(t)
	server.VCSWorkdir = vcsTestRepo(t)

	rec := doJSON(t, server, http.MethodGet, "/api/vcs/diff?context=banana", nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid context: %d %s, want 400", rec.Code, rec.Body.String())
	}
}

func TestVcsDiffBranchModeEmptyOnDefaultBranch(t *testing.T) {
	dir := vcsTestRepo(t)
	server, _, _ := newTestServer(t)
	server.VCSWorkdir = dir

	// The fixture's only branch is the default branch, so the branch diff
	// is empty even though the working tree is dirty.
	rec := doJSON(t, server, http.MethodGet, "/api/vcs/diff?mode=branch", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("branch mode: %d %s", rec.Code, rec.Body.String())
	}
	var files []vcs.FileDiff
	if err := json.NewDecoder(rec.Body).Decode(&files); err != nil {
		t.Fatal(err)
	}
	if len(files) != 0 {
		t.Fatalf("branch diff on the default branch = %+v, want empty", files)
	}
	if files == nil {
		// The wire form must be [], never null: the TS schema is an array.
		t.Fatalf("branch diff decoded to nil; the payload must be []")
	}
}

func TestVcsRoutesWithoutWorkdirAnswerEmpty(t *testing.T) {
	server, _, _ := newTestServer(t)

	rec := doJSON(t, server, http.MethodGet, "/api/vcs", nil)
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != "{}" {
		t.Fatalf("workdir-less /api/vcs: %d %q", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, server, http.MethodGet, "/api/vcs/diff", nil)
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Fatalf("workdir-less /api/vcs/diff: %d %q", rec.Code, rec.Body.String())
	}
}

func TestVcsDiffIsJSONArrayNeverNull(t *testing.T) {
	server, _, _ := newTestServer(t)
	server.VCSWorkdir = t.TempDir() // not a repository → empty list

	req := httptest.NewRequest(http.MethodGet, "/api/vcs/diff?mode=git", nil)
	rec := httptest.NewRecorder()
	server.Mux().ServeHTTP(rec, req)
	if strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Fatalf("empty diff payload = %q, want []", rec.Body.String())
	}
}
