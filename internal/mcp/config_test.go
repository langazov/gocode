package mcp

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestParseServersLocalAndRemote(t *testing.T) {
	raw := map[string]any{
		"fs": map[string]any{
			"type":        "local",
			"command":     []any{"opencode", "x", "@modelcontextprotocol/server-filesystem"},
			"environment": map[string]any{"FOO": "bar"},
		},
		"remote": map[string]any{
			"type":    "remote",
			"url":     "https://example.com/mcp",
			"headers": map[string]any{"X-Api-Key": "secret"},
		},
	}
	servers, errs := ParseServers(raw)
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	fs := servers["fs"]
	if fs.Type != "local" || len(fs.Command) != 3 || fs.Environment["FOO"] != "bar" {
		t.Fatalf("fs = %+v", fs)
	}
	if !fs.IsEnabled() {
		t.Fatal("default enabled should be true")
	}
	remote := servers["remote"]
	if remote.Type != "remote" || remote.URL != "https://example.com/mcp" || remote.Headers["X-Api-Key"] != "secret" {
		t.Fatalf("remote = %+v", remote)
	}
}

func TestParseServersRejectsUnknownType(t *testing.T) {
	_, errs := ParseServers(map[string]any{"bad": map[string]any{"type": "sftp"}})
	if len(errs) != 1 {
		t.Fatalf("expected one error, got %v", errs)
	}
}

func TestParseServersEnabledFalse(t *testing.T) {
	servers, _ := ParseServers(map[string]any{
		"s": map[string]any{"type": "remote", "url": "https://x", "enabled": false},
	})
	if servers["s"].IsEnabled() {
		t.Fatal("enabled:false should be respected")
	}
}

func TestOAuthConfigBoolFalseDisables(t *testing.T) {
	servers, errs := ParseServers(map[string]any{
		"s": map[string]any{"type": "remote", "url": "https://x", "oauth": false},
	})
	if len(errs) != 0 {
		t.Fatalf("errs = %v", errs)
	}
	oauth := servers["s"].OAuth
	if !oauth.Set || !oauth.Disabled {
		t.Fatalf("oauth = %+v, want Set+Disabled", oauth)
	}
}

func TestOAuthConfigObject(t *testing.T) {
	servers, errs := ParseServers(map[string]any{
		"s": map[string]any{"type": "remote", "url": "https://x", "oauth": map[string]any{
			"clientId": "abc", "clientSecret": "shh", "scope": "read write", "callbackPort": 4000,
		}},
	})
	if len(errs) != 0 {
		t.Fatalf("errs = %v", errs)
	}
	oauth := servers["s"].OAuth
	if oauth.Disabled {
		t.Fatal("object form should not disable oauth")
	}
	if oauth.ClientID != "abc" || oauth.ClientSecret != "shh" || oauth.Scope != "read write" || oauth.CallbackPort != 4000 {
		t.Fatalf("oauth = %+v", oauth)
	}
}

// TestOAuthConfigRoundTripsWhenUnset guards the bug where marshaling a
// never-configured OAuthConfig leaked its internal Set/Disabled bookkeeping
// fields into the config file as literal "Set"/"Disabled" JSON keys.
func TestOAuthConfigRoundTripsWhenUnset(t *testing.T) {
	server := ServerConfig{Type: "remote", URL: "https://x"}
	encoded, err := json.Marshal(server)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "\"Set\"") || strings.Contains(string(encoded), "\"Disabled\"") {
		t.Fatalf("marshaled output leaked internal fields: %s", encoded)
	}

	var decoded ServerConfig
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.OAuth.Set {
		t.Fatalf("round-tripped OAuth should stay unset, got %+v", decoded.OAuth)
	}
}

func TestServerConfigTimeoutOr(t *testing.T) {
	var s ServerConfig
	if got := s.TimeoutOr(30000); got != 30000 {
		t.Fatalf("TimeoutOr with unset Timeout = %d, want default 30000", got)
	}
	custom := 5000
	s.Timeout = &custom
	if got := s.TimeoutOr(30000); got != 5000 {
		t.Fatalf("TimeoutOr = %d, want 5000", got)
	}
}
