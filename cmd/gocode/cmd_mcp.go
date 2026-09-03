package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/langazov/gocode-go/internal/clix"
	"github.com/langazov/gocode-go/internal/config"
	"github.com/langazov/gocode-go/internal/global"
	"github.com/langazov/gocode-go/internal/mcp"
)

// mcpCommand mirrors McpCommand in cli/cmd/mcp.ts.
func mcpCommand() *clix.Command {
	namePositional := []clix.Positional{{Name: "name", Describe: "name of the MCP server"}}
	return &clix.Command{
		Name:     "mcp",
		Describe: "manage MCP (Model Context Protocol) servers",
		Demand:   true,
		Sub: []*clix.Command{
			{Name: "add", Describe: "add an MCP server",
				Positionals: []clix.Positional{{Name: "name", Describe: "name of the MCP server"}},
				AllowExtra:  true,
				Flags: []clix.Flag{
					{Name: "url", Kind: clix.KindString, Describe: "URL for a remote MCP server"},
					{Name: "env", Kind: clix.KindStringArray, Describe: "environment variable for a local MCP server (KEY=VALUE)"},
					{Name: "header", Kind: clix.KindStringArray, Describe: "HTTP header for a remote MCP server (KEY=VALUE)"},
				},
				Run: runMCPAdd},
			{Name: "list", Aliases: []string{"ls"}, Describe: "list MCP servers and their status", Run: runMCPList},
			{Name: "auth", Describe: "authenticate with an OAuth-enabled MCP server", Positionals: namePositional,
				Sub: []*clix.Command{
					{Name: "list", Aliases: []string{"ls"}, Describe: "list OAuth-capable MCP servers and their auth status",
						Run: runMCPAuthList},
				},
				Run: runMCPAuth},
			{Name: "logout", Describe: "remove OAuth credentials for an MCP server", Positionals: namePositional,
				Run: runMCPLogout},
			{Name: "debug", Describe: "debug OAuth connection for an MCP server",
				Positionals: []clix.Positional{{Name: "name", Required: true, Describe: "name of the MCP server"}},
				Run:         runMCPDebug},
		},
	}
}

func loadMCPServers() (map[string]mcp.ServerConfig, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	servers, errs := mcp.ParseServers(cfg.MCP)
	for name, err := range errs {
		fmt.Fprintf(os.Stderr, "warning: mcp server %q: %v\n", name, err)
	}
	return servers, nil
}

func statusIcon(status string) string {
	switch status {
	case "connected":
		return "✓"
	case "disabled":
		return "○"
	case "needs_auth":
		return "⚠"
	default:
		return "✗"
	}
}

func statusText(status string) string {
	switch status {
	case "connected":
		return "connected"
	case "disabled":
		return "disabled"
	case "needs_auth":
		return "needs authentication"
	case "needs_client_registration":
		return "needs client registration"
	default:
		return "failed"
	}
}

func authStatusIcon(status string) string {
	switch status {
	case "authenticated":
		return "✓"
	case "expired":
		return "⚠"
	default:
		return "✗"
	}
}

func runMCPList(a *clix.Args) error {
	servers, err := loadMCPServers()
	if err != nil {
		return err
	}
	if len(servers) == 0 {
		fmt.Println("No MCP servers configured")
		fmt.Println("Add servers with: gocode mcp add")
		return nil
	}

	cwd, _ := os.Getwd()
	service := mcp.NewService(cwd)
	defer service.Close()
	service.Load(context.Background(), servers)
	statuses := service.Statuses()

	names := sortedKeys(servers)
	for _, name := range names {
		cfg := servers[name]
		status := statuses[name]
		hint := ""
		if status.Error != "" {
			hint = "\n    " + status.Error
		}
		if status.Status == "connected" && cfg.Type == "remote" && cfg.OAuth.Set && !cfg.OAuth.Disabled && mcp.HasStoredTokens(name, cfg) {
			hint = " (OAuth)"
		}
		typeHint := cfg.URL
		if cfg.Type == "local" {
			typeHint = strings.Join(cfg.Command, " ")
		}
		fmt.Printf("%s %s %s%s\n    %s\n", statusIcon(status.Status), name, statusText(status.Status), hint, typeHint)
	}
	fmt.Printf("%d server(s)\n", len(servers))
	return nil
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func oauthServers(servers map[string]mcp.ServerConfig) []string {
	var names []string
	for name, cfg := range servers {
		if mcp.SupportsOAuth(cfg) {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

func runMCPAuthList(a *clix.Args) error {
	servers, err := loadMCPServers()
	if err != nil {
		return err
	}
	names := oauthServers(servers)
	if len(names) == 0 {
		fmt.Println("No OAuth-capable MCP servers configured")
		return nil
	}
	for _, name := range names {
		cfg := servers[name]
		status := mcp.GetAuthStatus(name, cfg)
		fmt.Printf("%s %s %s\n    %s\n", authStatusIcon(status), name, status, cfg.URL)
	}
	fmt.Printf("%d OAuth-capable server(s)\n", len(names))
	return nil
}

func runMCPAuth(a *clix.Args) error {
	servers, err := loadMCPServers()
	if err != nil {
		return err
	}
	name := a.PositionalOr("name", "")
	if name == "" {
		names := oauthServers(servers)
		if len(names) == 0 {
			return &usageError{msg: "No OAuth-capable MCP servers configured. Remote servers support OAuth by default; add one with: gocode mcp add"}
		}
		return &usageError{msg: "a server name is required (interactive server selection is not yet implemented in the Go port); one of: " + strings.Join(names, ", ")}
	}
	cfg, ok := servers[name]
	if !ok {
		return fmt.Errorf("MCP server not found: %s", name)
	}
	if !mcp.SupportsOAuth(cfg) {
		return fmt.Errorf("MCP server %s is not an OAuth-capable remote server", name)
	}

	fmt.Println("Starting OAuth flow...")
	cwd, _ := os.Getwd()
	service := mcp.NewService(cwd)
	defer service.Close()
	_, err = service.Authenticate(context.Background(), name, cfg, func(url string) {
		fmt.Println("Authorize in your browser:")
		fmt.Println(url)
		fmt.Println("Waiting for authorization...")
	})
	if err != nil {
		return fmt.Errorf("authentication failed: %w", err)
	}
	fmt.Println("Authentication successful!")
	return nil
}

func runMCPLogout(a *clix.Args) error {
	servers, err := loadMCPServers()
	if err != nil {
		return err
	}
	name := a.PositionalOr("name", "")
	if name == "" {
		var withCreds []string
		all, _ := mcp.StoreAll()
		for n := range all {
			withCreds = append(withCreds, n)
		}
		sort.Strings(withCreds)
		if len(withCreds) == 0 {
			fmt.Println("No MCP OAuth credentials stored")
			return nil
		}
		return &usageError{msg: "a server name is required (interactive server selection is not yet implemented in the Go port); one of: " + strings.Join(withCreds, ", ")}
	}
	all, err := mcp.StoreAll()
	if err != nil {
		return err
	}
	if _, ok := all[name]; !ok {
		return fmt.Errorf("no credentials found for: %s", name)
	}
	_ = servers
	if err := mcp.RemoveAuth(name); err != nil {
		return err
	}
	fmt.Printf("Removed OAuth credentials for %s\n", name)
	return nil
}

func runMCPDebug(a *clix.Args) error {
	servers, err := loadMCPServers()
	if err != nil {
		return err
	}
	name := a.Pos["name"]
	cfg, ok := servers[name]
	if !ok {
		return fmt.Errorf("MCP server not found: %s", name)
	}
	if cfg.Type != "remote" {
		return fmt.Errorf("MCP server %s is not a remote server", name)
	}
	fmt.Printf("Server: %s\n", name)
	fmt.Printf("URL: %s\n", cfg.URL)
	authStatus := mcp.GetAuthStatus(name, cfg)
	fmt.Printf("Auth status: %s %s\n", authStatusIcon(authStatus), authStatus)
	if entry, ok := mcp.StoreGet(name, cfg.URL); ok {
		if entry.AccessToken != "" {
			token := entry.AccessToken
			masked := "***"
			if len(token) > 8 {
				masked = token[:4] + "***" + token[len(token)-4:]
			}
			fmt.Printf("  Access token: %s\n", masked)
		}
		if entry.RefreshToken != "" {
			fmt.Println("  Refresh token: present")
		}
		if entry.ClientID != "" {
			fmt.Printf("  Client ID: %s\n", entry.ClientID)
		}
	}

	fmt.Println("Testing connection...")
	cwd, _ := os.Getwd()
	service := mcp.NewService(cwd)
	defer service.Close()
	status := service.Connect(context.Background(), name, cfg)
	fmt.Printf("Connection status: %s %s\n", statusIcon(status.Status), statusText(status.Status))
	if status.Error != "" {
		fmt.Println("  " + status.Error)
	}
	return nil
}

func runMCPAdd(a *clix.Args) error {
	name := a.PositionalOr("name", "")
	url := a.String("url")
	command := a.Extra
	if name == "" {
		if url != "" || len(a.Array("env")) > 0 || len(a.Array("header")) > 0 || len(command) > 0 {
			return &usageError{msg: "A server name is required for non-interactive MCP configuration"}
		}
		return &usageError{msg: "usage: gocode mcp add <name> --url <url> | -- <command...> (interactive prompts are not yet implemented in the Go port)"}
	}
	if (url != "") == (len(command) > 0) {
		return &usageError{msg: "Provide either --url <url> or a command after --"}
	}
	if len(a.Array("env")) > 0 && url != "" {
		return &usageError{msg: "--env is only valid for local MCP servers"}
	}
	if len(a.Array("header")) > 0 && len(command) > 0 {
		return &usageError{msg: "--header is only valid for remote MCP servers"}
	}

	entries := func(values []string) (map[string]string, error) {
		out := map[string]string{}
		for _, entry := range values {
			key, value, ok := strings.Cut(entry, "=")
			if !ok || key == "" {
				return nil, fmt.Errorf("invalid KEY=VALUE entry: %s", entry)
			}
			out[key] = value
		}
		return out, nil
	}
	environment, err := entries(a.Array("env"))
	if err != nil {
		return err
	}
	headers, err := entries(a.Array("header"))
	if err != nil {
		return err
	}

	server := mcp.ServerConfig{}
	if url != "" {
		server.Type = "remote"
		server.URL = url
		if len(headers) > 0 {
			server.Headers = headers
		}
	} else {
		server.Type = "local"
		server.Command = command
		if len(environment) > 0 {
			server.Environment = environment
		}
	}

	configPath, err := addMCPToGlobalConfig(name, server)
	if err != nil {
		return err
	}
	fmt.Printf("MCP server %q added to %s\n", name, configPath)
	return nil
}

// addMCPToGlobalConfig writes the server into the global gocode.json,
// matching resolveConfigPath(Global.Path.config, true) + addMcpToConfig in
// mcp.ts. Unlike TS (which uses jsonc-parser's modify/applyEdits to
// preserve comments and formatting), this reads the file with the existing
// JSONC-stripping parser and rewrites it as plain indented JSON — an
// existing config file's comments are not preserved. A missing file is
// created fresh.
func addMCPToGlobalConfig(name string, server mcp.ServerConfig) (string, error) {
	dir := global.Resolve().Config
	candidates := []string{filepath.Join(dir, "gocode.json"), filepath.Join(dir, "gocode.jsonc")}
	path := candidates[0]
	raw := "{}"
	for _, candidate := range candidates {
		if data, err := os.ReadFile(candidate); err == nil {
			path = candidate
			raw = string(data)
			break
		}
	}
	stripped, err := config.StripJSONC(raw)
	if err != nil {
		return "", fmt.Errorf("invalid JSON in %s: %w", path, err)
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(stripped), &doc); err != nil {
		return "", fmt.Errorf("invalid JSON in %s: %w", path, err)
	}
	if doc == nil {
		doc = map[string]any{}
	}
	mcpSection, _ := doc["mcp"].(map[string]any)
	if mcpSection == nil {
		mcpSection = map[string]any{}
	}
	encoded, err := json.Marshal(server)
	if err != nil {
		return "", err
	}
	var serverAny any
	if err := json.Unmarshal(encoded, &serverAny); err != nil {
		return "", err
	}
	mcpSection[name] = serverAny
	doc["mcp"] = mcpSection

	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, out, 0o644); err != nil {
		return "", err
	}
	return path, nil
}
