package server

import (
	"net/http"

	"github.com/langazov/gocode-go/internal/config"
	"github.com/langazov/gocode-go/internal/plugin"
)

// listPlugins serves GET /api/plugin with the loaded plugins and their state,
// backing the sidebar's Plugins section and the status overlay. It mirrors the
// shape of GET /api/lsp and GET /api/mcp: the live list, not the config — a
// plugin that failed to load is absent, and the empty list is what tells the
// interface to say "none loaded".
func (s *Server) listPlugins(w http.ResponseWriter, r *http.Request) {
	if s.Plugins == nil {
		writeJSON(w, http.StatusOK, []pluginStatus{})
		return
	}
	statuses := s.Plugins.Status()
	out := make([]pluginStatus, 0, len(statuses))
	for _, st := range statuses {
		hooks := st.Hooks
		if hooks == nil {
			hooks = []string{}
		}
		tools := st.Tools
		if tools == nil {
			tools = []string{}
		}
		out = append(out, pluginStatus{
			ID:     st.ID,
			Spec:   st.Spec,
			Source: st.Source,
			State:  st.State,
			Hooks:  hooks,
			Tools:  tools,
		})
	}
	configured := configuredPlugins(s.Config)
	writeJSON(w, http.StatusOK, pluginStatusResponse{
		Plugins:    out,
		Configured: configured,
		Available:  availablePlugins(configured),
	})
}

// availablePlugins lists what is installed under the config directories'
// `plugin/` folders but absent from the `plugin` array — the plugins the
// dialog offers as disabled, so installing one is enough to see it.
//
// Discovery is done per request rather than at boot: `make install-plugin`
// drops a directory in while the server is running, and a list that only
// refreshed on restart would tell the user their new plugin is not there.
func availablePlugins(configured []config.PluginSpec) []availablePlugin {
	specs := make([]plugin.Spec, 0, len(configured))
	for _, spec := range configured {
		specs = append(specs, plugin.Spec{Ref: spec.Ref, Options: plugin.Options(spec.Options)})
	}
	found := plugin.Installed(specs, "")
	out := make([]availablePlugin, 0, len(found))
	for _, entry := range found {
		out = append(out, availablePlugin{
			Name: entry.Name,
			Ref:  entry.Ref,
			Path: entry.Path,
			Root: entry.Root,
		})
	}
	return out
}

// configuredPlugins reads the config's plugin array, tolerating a server
// assembled without a config (tests).
func configuredPlugins(cfg *config.Config) []config.PluginSpec {
	if cfg == nil {
		return []config.PluginSpec{}
	}
	return cfg.Plugin
}

// pluginStatusResponse is the body of GET /api/plugin: the loaded plugins
// plus the config's plugin array, which is the enable-state truth the
// interface's plugins dialog edits, plus what is installed but unconfigured.
type pluginStatusResponse struct {
	// Plugins is the loaded list, in load order.
	Plugins []pluginStatus `json:"plugins"`
	// Configured is the config's `plugin` array, in config order.
	Configured []config.PluginSpec `json:"configured"`
	// Available is what sits runnable in a config directory's plugin folder
	// without the `plugin` array naming it, in config-directory order.
	Available []availablePlugin `json:"available"`
}

// availablePlugin is one installed-but-unconfigured plugin.
type availablePlugin struct {
	Name string `json:"name"`
	// Ref is what enabling it must write to the `plugin` array.
	Ref  string `json:"ref"`
	Path string `json:"path"`
	Root string `json:"root"`
}

// pluginStatus is one entry of GET /api/plugin.
type pluginStatus struct {
	ID     string   `json:"id"`
	Spec   string   `json:"spec"`
	Source string   `json:"source"`
	State  string   `json:"state"`
	Hooks  []string `json:"hooks"`
	Tools  []string `json:"tools"`
}
