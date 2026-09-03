// Package mcp ports packages/opencode/src/mcp (the MCP client subsystem):
// connecting to local (stdio) and remote (streamable HTTP / SSE) MCP
// servers, OAuth for remote servers, and exposing their tools to the
// session runner. It uses the official github.com/modelcontextprotocol/go-sdk
// for the wire protocol and OAuth authorization-code flow (PKCE, discovery,
// dynamic client registration), matching the TypeScript port's use of
// @modelcontextprotocol/sdk — only the persistence, status machine, and CLI
// surface are hand-ported.
package mcp

import (
	"encoding/json"
	"fmt"
)

// ServerConfig mirrors ConfigMCPV1.Info: a local (stdio subprocess) or
// remote (streamable HTTP) server, discriminated by Type.
type ServerConfig struct {
	Type string `json:"type"`

	// Local
	Command     []string          `json:"command,omitempty"`
	Cwd         string            `json:"cwd,omitempty"`
	Environment map[string]string `json:"environment,omitempty"`

	// Remote
	URL     string            `json:"url,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
	OAuth   OAuthConfig       `json:"oauth"`

	Enabled *bool `json:"enabled,omitempty"`
	Timeout *int  `json:"timeout,omitempty"`
}

// OAuthConfig mirrors ConfigMCPV1's oauth field: `false` disables OAuth
// entirely for a remote server (Disabled=true), an object customizes it,
// and absence (both false and Set false) means "use OAuth with defaults".
type OAuthConfig struct {
	Set          bool   `json:"-"`
	Disabled     bool   `json:"-"`
	ClientID     string `json:"clientId,omitempty"`
	ClientSecret string `json:"clientSecret,omitempty"`
	Scope        string `json:"scope,omitempty"`
	CallbackPort int    `json:"callbackPort,omitempty"`
	RedirectURI  string `json:"redirectUri,omitempty"`
}

func (o *OAuthConfig) UnmarshalJSON(data []byte) error {
	if string(data) == "null" {
		*o = OAuthConfig{}
		return nil
	}
	var b bool
	if err := json.Unmarshal(data, &b); err == nil {
		o.Set = true
		o.Disabled = !b
		return nil
	}
	type plain OAuthConfig
	var p plain
	if err := json.Unmarshal(data, &p); err != nil {
		return err
	}
	*o = OAuthConfig(p)
	o.Set = true
	return nil
}

func (o OAuthConfig) MarshalJSON() ([]byte, error) {
	if !o.Set {
		return []byte("null"), nil
	}
	if o.Disabled {
		return []byte("false"), nil
	}
	type plain OAuthConfig
	return json.Marshal(plain(o))
}

// IsEnabled reports whether the server should be connected, defaulting to
// true (matching TS's `mcp.enabled === false` check — anything else,
// including unset, connects).
func (s ServerConfig) IsEnabled() bool {
	return s.Enabled == nil || *s.Enabled
}

// TimeoutOr returns the configured per-server timeout in milliseconds, or
// def if unset.
func (s ServerConfig) TimeoutOr(def int) int {
	if s.Timeout == nil {
		return def
	}
	return *s.Timeout
}

// ParseServers decodes the config package's raw `mcp` map (map[string]any,
// as loaded from gocode.json) into typed ServerConfig entries. Invalid
// entries are dropped with an error collected per-name rather than failing
// the whole catalog, so one bad entry doesn't take down every other server.
func ParseServers(raw map[string]any) (map[string]ServerConfig, map[string]error) {
	servers := map[string]ServerConfig{}
	errs := map[string]error{}
	for name, value := range raw {
		encoded, err := json.Marshal(value)
		if err != nil {
			errs[name] = err
			continue
		}
		var server ServerConfig
		if err := json.Unmarshal(encoded, &server); err != nil {
			errs[name] = err
			continue
		}
		if server.Type != "local" && server.Type != "remote" {
			errs[name] = fmt.Errorf("mcp: unknown server type %q", server.Type)
			continue
		}
		servers[name] = server
	}
	return servers, errs
}
