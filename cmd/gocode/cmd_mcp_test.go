package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/langazov/gocode-go/internal/clix"
)

// mcpCLIEnv seeds an isolated global config with one server and returns the
// config path plus a project dir.
func mcpCLIEnv(t *testing.T) (configPath, projectDir string) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	configDir := filepath.Join(root, "gocode")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	configPath = filepath.Join(configDir, "gocode.json")
	server := `{"mcp":{"github":{"type":"local","command":["gh-mcp"]}}}`
	if err := os.WriteFile(configPath, []byte(server), 0o644); err != nil {
		t.Fatal(err)
	}
	return configPath, t.TempDir()
}

func runMCPCommand(t *testing.T, cmd func(*clix.Args) error, name string) {
	t.Helper()
	t.Chdir(t.TempDir())
	if err := cmd(&clix.Args{Pos: map[string]string{"name": name}}); err != nil {
		t.Fatal(err)
	}
}

func TestMCPCLIDisableEnable(t *testing.T) {
	configPath, _ := mcpCLIEnv(t)

	runMCPCommand(t, runMCPDisable, "github")
	saved, _ := os.ReadFile(configPath)
	if !strings.Contains(string(saved), `"enabled": false`) {
		t.Fatalf("config after disable:\n%s", saved)
	}

	runMCPCommand(t, runMCPEnable, "github")
	saved, _ = os.ReadFile(configPath)
	if strings.Contains(string(saved), "enabled") {
		t.Fatalf("config after re-enable still carries enabled:\n%s", saved)
	}
}

func TestMCPCLIRemove(t *testing.T) {
	// Remove drops the stored usage measurement (mcp-usage.json in the
	// data dir); keep the test off the developer's real store.
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	configPath, _ := mcpCLIEnv(t)

	runMCPCommand(t, runMCPRemove, "github")
	saved, _ := os.ReadFile(configPath)
	if strings.Contains(string(saved), "github") {
		t.Fatalf("config after remove:\n%s", saved)
	}
}

func TestMCPCLIUnknownServer(t *testing.T) {
	_, _ = mcpCLIEnv(t)
	if err := runMCPSetEnabled(&clix.Args{Pos: map[string]string{"name": "nope"}}, false); err == nil {
		t.Fatal("expected an error for an unknown server")
	}
}
