// VCS routes: repository info and the file diff the TUI's diff viewer
// renders. Ports the TS instance group's vcsDiff/vcsInfo handlers
// (packages/opencode/src/server/routes/instance/httpapi/handlers/instance.ts)
// over internal/vcs, which ports the git plumbing itself.
//
// Divergence from TS: no `directory` parameter. The TS server is
// multi-project and resolves the worktree per request; this server is one
// project per process (bootStack), so the diff is always for the runtime
// working directory. Recorded in documentation/10-development.md.
package server

import (
	"net/http"
	"strconv"

	"github.com/langazov/gocode-go/internal/vcs"
)

// vcsInfo answers GET /api/vcs: the branch state the interface gates the
// "Main branch" diff source on (diff-viewer.tsx reads vcs.branch and
// vcs.default_branch before offering that mode).
func (s *Server) vcsInfo(w http.ResponseWriter, r *http.Request) {
	if s.VCSWorkdir == "" {
		// A stack that never set a workdir (tests, embedders) has no VCS
		// surface at all; an empty payload keeps the TUI's "working tree"
		// default working rather than 500ing.
		writeJSON(w, http.StatusOK, vcs.RepoInfo{})
		return
	}
	info, ok := vcs.Info(s.VCSWorkdir)
	if !ok {
		writeJSON(w, http.StatusOK, vcs.RepoInfo{})
		return
	}
	writeJSON(w, http.StatusOK, info)
}

// vcsDiff answers GET /api/vcs/diff?mode=git|branch&context=N, the shape
// diff-viewer.tsx's createResource fetches: one entry per changed file with
// its patch, counts, and status. Unknown modes are a client bug and get a
// 400, matching the API's error contract; a non-git directory is not an
// error — the viewer shows "No diff!" over an empty list, exactly like TS.
func (s *Server) vcsDiff(w http.ResponseWriter, r *http.Request) {
	if s.VCSWorkdir == "" {
		writeJSON(w, http.StatusOK, []vcs.FileDiff{})
		return
	}

	mode := r.URL.Query().Get("mode")
	if mode == "" {
		mode = vcs.ModeGit
	}
	resolved, ok := vcs.DiffMode(mode)
	if !ok {
		writeError(w, http.StatusBadRequest, "unknown diff mode: "+mode)
		return
	}

	context := 0
	if raw := r.URL.Query().Get("context"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 0 {
			writeError(w, http.StatusBadRequest, "invalid context: "+raw)
			return
		}
		context = value
	}

	files := vcs.Diff(s.VCSWorkdir, resolved, context)
	if files == nil {
		files = []vcs.FileDiff{}
	}
	writeJSON(w, http.StatusOK, files)
}
