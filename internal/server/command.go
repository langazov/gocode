package server

import "net/http"

// listCommands serves GET /api/command, the slash commands the interface
// offers for completion. Ports the shape of Command.list().
func (s *Server) listCommands(w http.ResponseWriter, r *http.Request) {
	if s.Commands == nil {
		writeJSON(w, http.StatusOK, []any{})
		return
	}
	// The template is deliberately included: the interface shows a command's
	// hints, and expansion happens server-side on execution, so the body is
	// not secret from a client that could run it anyway.
	writeJSON(w, http.StatusOK, s.Commands.List())
}
