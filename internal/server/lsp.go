package server

import "net/http"

// listLSP serves GET /api/lsp with the connected language servers, backing the
// sidebar's LSP section. It ports the shape of LSP.status() in
// packages/opencode/src/lsp/lsp.ts.
func (s *Server) listLSP(w http.ResponseWriter, r *http.Request) {
	type entry struct {
		ID     string `json:"id"`
		Name   string `json:"name"`
		Root   string `json:"root"`
		Status string `json:"status"`
	}
	out := []entry{}
	if s.LSP == nil || !s.LSP.Enabled() {
		// An empty list with enabled=false is what tells the sidebar to say
		// "disabled" rather than "none have started yet".
		writeJSON(w, http.StatusOK, map[string]any{"enabled": false, "servers": out, "available": []string{}})
		return
	}
	for _, item := range s.LSP.Status() {
		out = append(out, entry{ID: item.ID, Name: item.Name, Root: item.Root, Status: item.Status})
	}
	available := s.LSP.Available()
	if available == nil {
		available = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"enabled":   true,
		"servers":   out,
		"available": available,
	})
}
