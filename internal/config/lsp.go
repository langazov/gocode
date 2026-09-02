package config

import "encoding/json"

// LSPServer is one entry in the `lsp` config section, porting
// ConfigV2.LSP.Server in packages/core/src/config/lsp.ts.
type LSPServer struct {
	Command        []string          `json:"command,omitempty"`
	Extensions     []string          `json:"extensions,omitempty"`
	Disabled       bool              `json:"disabled,omitempty"`
	Env            map[string]string `json:"env,omitempty"`
	Initialization map[string]any    `json:"initialization,omitempty"`
}

// LSPConfig is the `lsp` section, which the schema allows to be either a
// boolean or a record of server entries:
//
//	"lsp": false                                  // disable everything
//	"lsp": { "gopls": { "disabled": true } }      // disable one
//	"lsp": { "mylang": { "command": ["mylang-ls"] } }
//
// The union is why this needs a custom unmarshaller rather than a plain map.
type LSPConfig struct {
	// off is true only for an explicit `false`.
	off bool
	// servers holds the per-server entries, nil when the section is absent or
	// boolean.
	servers map[string]LSPServer
}

// Disabled reports whether the section switched every server off.
func (l LSPConfig) Disabled() bool { return l.off }

// Servers returns the configured per-server entries.
func (l LSPConfig) Servers() map[string]LSPServer { return l.servers }

func (l LSPConfig) MarshalJSON() ([]byte, error) {
	if l.off {
		return []byte("false"), nil
	}
	if l.servers == nil {
		return []byte("null"), nil
	}
	return json.Marshal(l.servers)
}

func (l *LSPConfig) UnmarshalJSON(data []byte) error {
	var flag bool
	if err := json.Unmarshal(data, &flag); err == nil {
		// `true` means "the defaults", which is also what absent means.
		l.off, l.servers = !flag, nil
		return nil
	}
	var servers map[string]LSPServer
	if err := json.Unmarshal(data, &servers); err != nil {
		return err
	}
	l.off, l.servers = false, servers
	return nil
}
