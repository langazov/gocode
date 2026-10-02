// Git routes: the Source Control surface the desktop client (gocode_gui)
// drives — status, per-file diffs, staging files and hunks, commits,
// branches, remotes, history, stashes and conflict resolution — over
// internal/vcs/gitops. These have no TypeScript counterpart; they port
// goide's GitService RPCs (proto/host/v1/host.proto) to HTTP.
//
// Unlike /api/vcs, every route takes an optional `directory` query
// parameter: the client groups sessions by project folder, and each folder
// is its own repository. Without it the routes act on VCSWorkdir.
//
// A git command that fails (merge conflict, rejected push, nothing to
// commit) is an expected outcome, not a server fault: it answers 422 with
// git's own message so the client can show it verbatim.
package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strconv"

	"github.com/langazov/gocode-go/internal/vcs/gitops"
)

func (s *Server) registerGitRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/vcs/git/status", s.gitStatus)
	mux.HandleFunc("GET /api/vcs/git/diff", s.gitDiff)
	mux.HandleFunc("GET /api/vcs/git/branches", s.gitBranches)
	mux.HandleFunc("GET /api/vcs/git/log", s.gitLog)
	mux.HandleFunc("GET /api/vcs/git/show/{hash}", s.gitShow)
	mux.HandleFunc("GET /api/vcs/git/stash", s.gitStashList)
	mux.HandleFunc("POST /api/vcs/git/init", s.gitInit)
	mux.HandleFunc("POST /api/vcs/git/stage", s.gitStage)
	mux.HandleFunc("POST /api/vcs/git/discard", s.gitDiscard)
	mux.HandleFunc("POST /api/vcs/git/apply", s.gitApply)
	mux.HandleFunc("POST /api/vcs/git/commit", s.gitCommit)
	mux.HandleFunc("POST /api/vcs/git/remote", s.gitRemote)
	mux.HandleFunc("POST /api/vcs/git/checkout", s.gitCheckout)
	mux.HandleFunc("POST /api/vcs/git/branch", s.gitBranchOp)
	mux.HandleFunc("POST /api/vcs/git/commit-op", s.gitCommitOp)
	mux.HandleFunc("POST /api/vcs/git/stash", s.gitStash)
	mux.HandleFunc("POST /api/vcs/git/conflict", s.gitConflict)
	mux.HandleFunc("POST /api/vcs/git/commit-message", s.gitCommitMessage)
}

var errNoGitDir = errors.New("no working directory: pass ?directory=")

// gitDir resolves the directory a git request acts on: the `directory`
// query parameter when given (absolute, existing), else VCSWorkdir.
func (s *Server) gitDir(r *http.Request) (string, error) {
	dir := r.URL.Query().Get("directory")
	if dir == "" {
		dir = s.VCSWorkdir
	}
	if dir == "" {
		return "", errNoGitDir
	}
	if !filepath.IsAbs(dir) {
		return "", errors.New("directory must be absolute: " + dir)
	}
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return "", errors.New("not a directory: " + dir)
	}
	return dir, nil
}

// gitRoot resolves the request's directory to its repository top level:
// git reports paths relative to it, so every operation runs there.
func (s *Server) gitRoot(w http.ResponseWriter, r *http.Request) (string, bool) {
	dir, err := s.gitDir(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return "", false
	}
	return gitops.RepoRoot(r.Context(), dir), true
}

// decodeGitBody reads a JSON request body into v, answering 400 itself.
func decodeGitBody(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return false
	}
	return true
}

// writeGitResult answers a mutation: git's failure as 422, else value.
func writeGitResult(w http.ResponseWriter, err error, value any) {
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, value)
}

// gitOutput is the reply of operations whose git output is worth showing
// (fetch/pull/push progress, merge summaries).
type gitOutput struct {
	Output string `json:"output"`
}

func queryBool(r *http.Request, name string) bool {
	v, _ := strconv.ParseBool(r.URL.Query().Get(name))
	return v
}

// gitStatus answers GET /api/vcs/git/status. Outside a repository it is
// {isRepo:false}, not an error, so the client can offer `git init`.
func (s *Server) gitStatus(w http.ResponseWriter, r *http.Request) {
	dir, err := s.gitDir(r)
	if errors.Is(err, errNoGitDir) {
		writeJSON(w, http.StatusOK, &gitops.Status{})
		return
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	st, err := gitops.StatusOf(r.Context(), dir)
	writeGitResult(w, err, st)
}

// gitDiff answers GET /api/vcs/git/diff?path=&staged=&untracked=&commit=
// with one file's unified diff.
func (s *Server) gitDiff(w http.ResponseWriter, r *http.Request) {
	root, ok := s.gitRoot(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	path := q.Get("path")
	if path == "" {
		writeError(w, http.StatusBadRequest, "path is required")
		return
	}
	opts := gitops.DiffOptions{
		Staged:    queryBool(r, "staged"),
		Untracked: queryBool(r, "untracked"),
		Commit:    q.Get("commit"),
	}
	if raw := q.Get("context"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 {
			writeError(w, http.StatusBadRequest, "invalid context: "+raw)
			return
		}
		opts.Context = n
	}
	diff, err := gitops.Diff(r.Context(), root, path, opts)
	writeGitResult(w, err, map[string]string{"diff": diff})
}

// gitBranches answers GET /api/vcs/git/branches: local branches with
// tracking details (most recently committed first) and remote branches.
func (s *Server) gitBranches(w http.ResponseWriter, r *http.Request) {
	root, ok := s.gitRoot(w, r)
	if !ok {
		return
	}
	locals, remotes, err := gitops.BranchList(r.Context(), root)
	current := ""
	for _, b := range locals {
		if b.Current {
			current = b.Name
		}
	}
	if locals == nil {
		locals = []gitops.BranchInfo{}
	}
	if remotes == nil {
		remotes = []string{}
	}
	writeGitResult(w, err, map[string]any{
		"current":  current,
		"branches": locals,
		"remote":   remotes,
	})
}

// gitLog answers GET /api/vcs/git/log?limit=&skip=&all=&query=&path=,
// newest first in topological order (the client draws the graph).
func (s *Server) gitLog(w http.ResponseWriter, r *http.Request) {
	root, ok := s.gitRoot(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	opts := gitops.LogOptions{
		All:   queryBool(r, "all"),
		Query: q.Get("query"),
		Path:  q.Get("path"),
	}
	opts.Limit, _ = strconv.Atoi(q.Get("limit"))
	opts.Skip, _ = strconv.Atoi(q.Get("skip"))
	commits, err := gitops.Log(r.Context(), root, opts)
	if commits == nil {
		commits = []gitops.CommitInfo{}
	}
	writeGitResult(w, err, map[string]any{"commits": commits})
}

// gitShow answers GET /api/vcs/git/show/{hash}: message, files and stats.
func (s *Server) gitShow(w http.ResponseWriter, r *http.Request) {
	root, ok := s.gitRoot(w, r)
	if !ok {
		return
	}
	details, err := gitops.Show(r.Context(), root, r.PathValue("hash"))
	writeGitResult(w, err, details)
}

func stashReply(entries []gitops.StashEntry) map[string]any {
	if entries == nil {
		entries = []gitops.StashEntry{}
	}
	return map[string]any{"entries": entries}
}

// gitStashList answers GET /api/vcs/git/stash.
func (s *Server) gitStashList(w http.ResponseWriter, r *http.Request) {
	root, ok := s.gitRoot(w, r)
	if !ok {
		return
	}
	entries, err := gitops.Stash(r.Context(), root, "list", 0, "", false)
	writeGitResult(w, err, stashReply(entries))
}

// gitInit answers POST /api/vcs/git/init: a new repository in the
// directory itself (not a parent's top level — there is none yet).
func (s *Server) gitInit(w http.ResponseWriter, r *http.Request) {
	dir, err := s.gitDir(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeGitResult(w, gitops.Init(r.Context(), dir), struct{}{})
}

// gitStage answers POST /api/vcs/git/stage {paths, all, unstage}.
func (s *Server) gitStage(w http.ResponseWriter, r *http.Request) {
	root, ok := s.gitRoot(w, r)
	if !ok {
		return
	}
	var body struct {
		Paths   []string `json:"paths"`
		All     bool     `json:"all"`
		Unstage bool     `json:"unstage"`
	}
	if !decodeGitBody(w, r, &body) {
		return
	}
	err := gitops.StagePaths(r.Context(), root, body.Paths, body.All, body.Unstage)
	writeGitResult(w, err, struct{}{})
}

// gitDiscard answers POST /api/vcs/git/discard {paths, all,
// includeUntracked}: tracked paths are restored, untracked ones deleted.
func (s *Server) gitDiscard(w http.ResponseWriter, r *http.Request) {
	root, ok := s.gitRoot(w, r)
	if !ok {
		return
	}
	var body struct {
		Paths            []string `json:"paths"`
		All              bool     `json:"all"`
		IncludeUntracked bool     `json:"includeUntracked"`
	}
	if !decodeGitBody(w, r, &body) {
		return
	}
	err := gitops.Discard(r.Context(), root, body.Paths, body.All, body.IncludeUntracked)
	writeGitResult(w, err, struct{}{})
}

// gitApply answers POST /api/vcs/git/apply {patch, cached, reverse}: a
// single-hunk patch staged (cached), unstaged (cached+reverse) or
// discarded (reverse).
func (s *Server) gitApply(w http.ResponseWriter, r *http.Request) {
	root, ok := s.gitRoot(w, r)
	if !ok {
		return
	}
	var body struct {
		Patch   string `json:"patch"`
		Cached  bool   `json:"cached"`
		Reverse bool   `json:"reverse"`
	}
	if !decodeGitBody(w, r, &body) {
		return
	}
	if body.Patch == "" {
		writeError(w, http.StatusBadRequest, "patch is required")
		return
	}
	err := gitops.ApplyPatch(r.Context(), root, body.Patch, body.Cached, body.Reverse)
	writeGitResult(w, err, struct{}{})
}

// gitCommit answers POST /api/vcs/git/commit {message, amend, all,
// signoff} with the new commit's short hash.
func (s *Server) gitCommit(w http.ResponseWriter, r *http.Request) {
	root, ok := s.gitRoot(w, r)
	if !ok {
		return
	}
	var body struct {
		Message string `json:"message"`
		Amend   bool   `json:"amend"`
		All     bool   `json:"all"`
		Signoff bool   `json:"signoff"`
	}
	if !decodeGitBody(w, r, &body) {
		return
	}
	hash, err := gitops.CommitWith(r.Context(), root, body.Message, gitops.CommitOptions{
		Amend:   body.Amend,
		All:     body.All,
		Signoff: body.Signoff,
	})
	writeGitResult(w, err, map[string]string{"commit": hash})
}

// gitRemote answers POST /api/vcs/git/remote {op: fetch|pull|push, …}.
func (s *Server) gitRemote(w http.ResponseWriter, r *http.Request) {
	root, ok := s.gitRoot(w, r)
	if !ok {
		return
	}
	var body struct {
		Op          string `json:"op"`
		Remote      string `json:"remote"`
		Branch      string `json:"branch"`
		Rebase      bool   `json:"rebase"`
		SetUpstream bool   `json:"setUpstream"`
		Force       bool   `json:"force"`
		All         bool   `json:"all"`
		Tags        bool   `json:"tags"`
	}
	if !decodeGitBody(w, r, &body) {
		return
	}
	out, err := gitops.Remote(r.Context(), root, gitops.RemoteOptions{
		Op: body.Op, Remote: body.Remote, Branch: body.Branch,
		Rebase: body.Rebase, SetUpstream: body.SetUpstream,
		Force: body.Force, All: body.All, Tags: body.Tags,
	})
	writeGitResult(w, err, gitOutput{out})
}

// gitCheckout answers POST /api/vcs/git/checkout {branch, create,
// startPoint}. A remote branch ("origin/x") checks out its local tracker.
func (s *Server) gitCheckout(w http.ResponseWriter, r *http.Request) {
	root, ok := s.gitRoot(w, r)
	if !ok {
		return
	}
	var body struct {
		Branch     string `json:"branch"`
		Create     bool   `json:"create"`
		StartPoint string `json:"startPoint"`
	}
	if !decodeGitBody(w, r, &body) {
		return
	}
	if body.Branch == "" {
		writeError(w, http.StatusBadRequest, "branch is required")
		return
	}
	err := gitops.Checkout(r.Context(), root, body.Branch, body.Create, body.StartPoint)
	writeGitResult(w, err, struct{}{})
}

// gitBranchOp answers POST /api/vcs/git/branch {op: delete|rename|merge,
// name, newName, force}.
func (s *Server) gitBranchOp(w http.ResponseWriter, r *http.Request) {
	root, ok := s.gitRoot(w, r)
	if !ok {
		return
	}
	var body struct {
		Op      string `json:"op"`
		Name    string `json:"name"`
		NewName string `json:"newName"`
		Force   bool   `json:"force"`
	}
	if !decodeGitBody(w, r, &body) {
		return
	}
	out, err := gitops.BranchOp(r.Context(), root, body.Op, body.Name, body.NewName, body.Force)
	writeGitResult(w, err, gitOutput{out})
}

// gitCommitOp answers POST /api/vcs/git/commit-op {op: revert|cherry-pick|
// reset-soft|reset-mixed|reset-hard|tag, hash, name}.
func (s *Server) gitCommitOp(w http.ResponseWriter, r *http.Request) {
	root, ok := s.gitRoot(w, r)
	if !ok {
		return
	}
	var body struct {
		Op   string `json:"op"`
		Hash string `json:"hash"`
		Name string `json:"name"`
	}
	if !decodeGitBody(w, r, &body) {
		return
	}
	out, err := gitops.CommitOp(r.Context(), root, body.Op, body.Hash, body.Name)
	writeGitResult(w, err, gitOutput{out})
}

// gitStash answers POST /api/vcs/git/stash {op: push|pop|apply|drop,
// index, message, includeUntracked} with the updated stash list.
func (s *Server) gitStash(w http.ResponseWriter, r *http.Request) {
	root, ok := s.gitRoot(w, r)
	if !ok {
		return
	}
	var body struct {
		Op               string `json:"op"`
		Index            int    `json:"index"`
		Message          string `json:"message"`
		IncludeUntracked bool   `json:"includeUntracked"`
	}
	if !decodeGitBody(w, r, &body) {
		return
	}
	entries, err := gitops.Stash(r.Context(), root, body.Op, body.Index, body.Message, body.IncludeUntracked)
	writeGitResult(w, err, stashReply(entries))
}

// gitConflict answers POST /api/vcs/git/conflict {op: ours|theirs|
// resolved|abort|continue, path}.
func (s *Server) gitConflict(w http.ResponseWriter, r *http.Request) {
	root, ok := s.gitRoot(w, r)
	if !ok {
		return
	}
	var body struct {
		Op   string `json:"op"`
		Path string `json:"path"`
	}
	if !decodeGitBody(w, r, &body) {
		return
	}
	out, err := gitops.Conflict(r.Context(), root, body.Op, body.Path)
	writeGitResult(w, err, gitOutput{out})
}
