package server

import (
	"net/http"

	"github.com/langazov/gocode-go/internal/config"
	"github.com/langazov/gocode-go/internal/mcp"
)

// The MCP management routes. GET keeps the Statuses() shape the sidebar and
// status overlay already poll; the richer per-server view the management
// dialog needs rides on GET /api/mcp/servers, and the mutations are the
// live enable/disable/delete the dialog and `gocode mcp enable/disable/remove`
// share.

// listMCPServers serves GET /api/mcp/servers: every configured server —
// connected, disabled or failed — with type, endpoint and enable state, the
// row model the TUI's MCP dialog renders. Unlike Statuses(), disabled
// servers appear: the dialog is where they get re-enabled.
func (s *Server) listMCPServers(w http.ResponseWriter, r *http.Request) {
	if s.MCP == nil {
		writeJSON(w, http.StatusOK, []mcp.ServerInfo{})
		return
	}
	writeJSON(w, http.StatusOK, s.MCP.Servers())
}

// enableMCP serves POST /api/mcp/{name}/enable: persist enabled=true to the
// winning config file, then connect. The two steps are in that order so a
// connect failure leaves the persisted state at "enabled" — which is true,
// and what the reconnect loop will keep retrying toward.
func (s *Server) enableMCP(w http.ResponseWriter, r *http.Request) {
	if s.MCP == nil {
		writeError(w, http.StatusServiceUnavailable, "mcp service is not running")
		return
	}
	name := r.PathValue("name")
	cfg, ok := s.serverConfig(name)
	if !ok {
		writeError(w, http.StatusNotFound, "mcp server not found: "+name)
		return
	}
	if _, err := config.SetMCPEnabled(s.VCSWorkdir, name, true); err != nil {
		writeConfigError(w, err)
		return
	}
	status := s.MCP.Enable(r.Context(), name, cfg)
	writeJSON(w, http.StatusOK, status)
}

// disableMCP serves POST /api/mcp/{name}/disable: persist enabled=false,
// then stop the connection and unregister the server's tools. Persistence
// first, for the same reason as enable — the on-disk state should match the
// state a restart would boot into.
func (s *Server) disableMCP(w http.ResponseWriter, r *http.Request) {
	if s.MCP == nil {
		writeError(w, http.StatusServiceUnavailable, "mcp service is not running")
		return
	}
	name := r.PathValue("name")
	if _, ok := s.serverConfig(name); !ok {
		writeError(w, http.StatusNotFound, "mcp server not found: "+name)
		return
	}
	if _, err := config.SetMCPEnabled(s.VCSWorkdir, name, false); err != nil {
		writeConfigError(w, err)
		return
	}
	s.MCP.Disable(name)
	writeJSON(w, http.StatusOK, mcp.Status{Status: "disabled"})
}

// deleteMCP serves DELETE /api/mcp/{name}: remove the entry from the winning
// config file and disconnect. Stored OAuth credentials are kept — removing
// a server is not un-authenticating from it (see RemoveMCPServer).
func (s *Server) deleteMCP(w http.ResponseWriter, r *http.Request) {
	if s.MCP == nil {
		writeError(w, http.StatusServiceUnavailable, "mcp service is not running")
		return
	}
	name := r.PathValue("name")
	if _, ok := s.serverConfig(name); !ok {
		writeError(w, http.StatusNotFound, "mcp server not found: "+name)
		return
	}
	if _, err := config.RemoveMCPServer(s.VCSWorkdir, name); err != nil {
		writeConfigError(w, err)
		return
	}
	// Drop the persisted usage measurement too: it is keyed by name, and a
	// server re-added under the same name must measure fresh rather than
	// show the deleted one's figures.
	_ = mcp.UsageRemove(name)
	s.MCP.Disconnect(name)
	writeJSON(w, http.StatusOK, map[string]bool{"removed": true})
}

// serverConfig resolves the live connection's config for name — the
// connection's own copy, which carries the merged global+project state as
// booted, including servers the running process has already disabled.
func (s *Server) serverConfig(name string) (mcp.ServerConfig, bool) {
	for _, info := range s.MCP.Servers() {
		if info.Name == name {
			return info.Config, true
		}
	}
	return mcp.ServerConfig{}, false
}

// writeConfigError answers a config-write failure with the status that
// describes it: a config the tool refuses to rewrite is reported as the
// file's own message, which names the path and what to do by hand.
func writeConfigError(w http.ResponseWriter, err error) {
	writeError(w, http.StatusInternalServerError, err.Error())
}
