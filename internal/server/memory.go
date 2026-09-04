package server

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/langazov/gocode-go/internal/memory"
)

// The memory routes back the interface's /memory manager.
//
// They exist because the TUI is always an HTTP client, even locally
// (see bootStack), so it cannot reach the store directly — and because the
// plugin that injects memories into a turn cannot serve routes. Both consumers
// talk to the same internal/memory store.
//
// Everything created here is OriginUser: the agent's writes come through the
// tools instead, and keeping the two apart is what lets the interface show who
// authored a memory.

// listMemories serves GET /api/memory.
//
// The scope query parameter selects which memories to return: "project" for
// this project's, "global" for the ones that apply everywhere, and anything
// else (including absent) for both. Disabled memories are always included —
// this is the management view, and a memory the user silenced is exactly the
// one they are most likely to have come here to find.
func (s *Server) listMemories(w http.ResponseWriter, r *http.Request) {
	input := memory.ListInput{IncludeDisabled: true}
	switch r.URL.Query().Get("scope") {
	case "project":
		input.Scopes = []string{s.projectScope()}
	case "global":
		input.Scopes = []string{memory.ScopeGlobal}
	default:
		input.Scopes = []string{memory.ScopeGlobal, s.projectScope()}
	}

	memories, err := s.Memory.List(r.Context(), input)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if memories == nil {
		memories = []memory.Memory{}
	}
	writeJSON(w, http.StatusOK, memories)
}

// createMemory serves POST /api/memory.
func (s *Server) createMemory(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Content  string `json:"content"`
		Scope    string `json:"scope"`
		Category string `json:"category"`
		Pinned   bool   `json:"pinned"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	created, err := s.Memory.Create(r.Context(), memory.Memory{
		Scope:    s.resolveScope(body.Scope),
		Content:  body.Content,
		Category: body.Category,
		Origin:   memory.OriginUser,
		Pinned:   body.Pinned,
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, created)
}

// updateMemory serves PATCH /api/memory/{memoryID}. Absent fields are left
// alone, so the interface can toggle a pin without resubmitting the content.
func (s *Server) updateMemory(w http.ResponseWriter, r *http.Request) {
	var body memory.Patch
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	// A scope arrives as the interface's word ("project"/"global"), not as a
	// stored scope value, for the same reason the tool takes one: a project
	// scope is an absolute path the client has no business constructing.
	if body.Scope != nil {
		resolved := s.resolveScope(*body.Scope)
		body.Scope = &resolved
	}

	updated, err := s.Memory.Update(r.Context(), r.PathValue("memoryID"), body)
	if errors.Is(err, memory.ErrNotFound) {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

// deleteMemory serves DELETE /api/memory/{memoryID}.
func (s *Server) deleteMemory(w http.ResponseWriter, r *http.Request) {
	err := s.Memory.Delete(r.Context(), r.PathValue("memoryID"))
	if errors.Is(err, memory.ErrNotFound) {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// resolveScope maps the interface's scope word onto a stored scope value.
// Anything that is not explicitly global is this project, so a client that
// omits the field gets the narrower of the two — widening a memory to every
// project should take saying so.
func (s *Server) resolveScope(requested string) string {
	if requested == memory.ScopeGlobal {
		return memory.ScopeGlobal
	}
	return s.projectScope()
}

// projectScope is the scope value for this server's project, falling back to
// global when the runtime resolved no project — with nowhere narrower to put a
// memory, the alternative is refusing to store it at all.
func (s *Server) projectScope() string {
	if s.ProjectID == "" {
		return memory.ScopeGlobal
	}
	return s.ProjectID
}
