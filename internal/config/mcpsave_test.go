package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// mcpWithProject seeds a global config dir and a project dir, returning
// both roots. The project dir plays the role of the working directory whose
// upward config discovery LoadTraced performs.
func mcpWithProject(t *testing.T, globalFiles, projectFiles map[string]string) (projectDir string) {
	t.Helper()
	globalRoot := t.TempDir()
	for name, content := range globalFiles {
		path := filepath.Join(globalRoot, "gocode", name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("XDG_CONFIG_HOME", globalRoot)

	projectDir = t.TempDir()
	for name, content := range projectFiles {
		path := filepath.Join(projectDir, name)
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return projectDir
}

func readJSONFile(t *testing.T, path string) map[string]json.RawMessage {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]json.RawMessage{}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return out
}

// SetMCPEnabled(false) writes enabled:false into the entry, preserving
// every sibling field and every other config key.
func TestSetMCPEnabledPreservesEntry(t *testing.T) {
	projectDir := mcpWithProject(t, map[string]string{
		"gocode.json": `{"model":"anthropic/claude-sonnet-5","mcp":{"github":{"type":"local","command":["gh-mcp"],"environment":{"T":"1"}}}}`,
	}, nil)

	path, err := SetMCPEnabled(projectDir, "github", false)
	if err != nil {
		t.Fatal(err)
	}
	root := readJSONFile(t, path)
	if _, ok := root["model"]; !ok {
		t.Errorf("model key was not preserved: %v", root)
	}
	var mcpSection map[string]json.RawMessage
	if err := json.Unmarshal(root["mcp"], &mcpSection); err != nil {
		t.Fatal(err)
	}
	var entry struct {
		Type        string            `json:"type"`
		Command     []string          `json:"command"`
		Environment map[string]string `json:"environment"`
		Enabled     *bool             `json:"enabled"`
	}
	if err := json.Unmarshal(mcpSection["github"], &entry); err != nil {
		t.Fatal(err)
	}
	if entry.Type != "local" || len(entry.Command) != 1 || entry.Command[0] != "gh-mcp" {
		t.Errorf("entry fields lost: %+v", entry)
	}
	if entry.Environment["T"] != "1" {
		t.Errorf("environment lost: %+v", entry.Environment)
	}
	if entry.Enabled == nil || *entry.Enabled {
		t.Errorf("enabled = %v, want explicit false", entry.Enabled)
	}
}

// Enabling removes the key entirely, so a round trip restores the original.
func TestSetMCPEnabledRoundTrip(t *testing.T) {
	projectDir := mcpWithProject(t, map[string]string{
		"gocode.json": `{"mcp":{"a":{"type":"remote","url":"https://x"}}}`,
	}, nil)
	before := readJSONFile(t, filepath.Join(mcpGlobalDir(t), "gocode.json"))

	if _, err := SetMCPEnabled(projectDir, "a", false); err != nil {
		t.Fatal(err)
	}
	path, err := SetMCPEnabled(projectDir, "a", true)
	if err != nil {
		t.Fatal(err)
	}
	after := readJSONFile(t, path)
	if strings.Contains(string(after["mcp"]), "enabled") {
		t.Errorf("enabled:true was written instead of dropping the key: %s", after["mcp"])
	}
	// The rewrite re-indents the file, so compare semantically: the decoded
	// mcp section must equal the original's.
	var beforeMCP, afterMCP map[string]any
	if err := json.Unmarshal(before["mcp"], &beforeMCP); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(after["mcp"], &afterMCP); err != nil {
		t.Fatal(err)
	}
	beforeEnc, _ := json.Marshal(beforeMCP)
	afterEnc, _ := json.Marshal(afterMCP)
	if string(beforeEnc) != string(afterEnc) {
		t.Errorf("round trip changed the mcp section: %s -> %s", beforeEnc, afterEnc)
	}
}

// A project definition wins over the global one: the edit lands in the
// project file, where a restart will read it.
func TestProjectConfigWins(t *testing.T) {
	globalDir := mcpGlobalDir(t)
	projectDir := mcpWithProject(t,
		map[string]string{"gocode.json": `{"mcp":{"a":{"type":"remote","url":"https://global"}}}`},
		map[string]string{"gocode.json": `{"mcp":{"a":{"type":"remote","url":"https://project"}}}`})

	path, err := SetMCPEnabled(projectDir, "a", false)
	if err != nil {
		t.Fatal(err)
	}
	if path == filepath.Join(globalDir, "gocode.json") {
		t.Fatalf("edit landed in the global file %s, want the project file", path)
	}
	if filepath.Dir(path) != projectDir {
		t.Fatalf("edit landed in %s, want %s", path, projectDir)
	}
}

// RemoveMCPServer drops the entry, and the mcp key when the last entry goes.
func TestRemoveMCPServerDropsKey(t *testing.T) {
	projectDir := mcpWithProject(t, map[string]string{
		"gocode.json": `{"mcp":{"only":{"type":"remote","url":"https://x"}},"theme":"dark"}`,
	}, nil)

	path, err := RemoveMCPServer(projectDir, "only")
	if err != nil {
		t.Fatal(err)
	}
	root := readJSONFile(t, path)
	if _, ok := root["mcp"]; ok {
		t.Errorf("mcp key survived removing the last entry: %v", root)
	}
	if string(root["theme"]) != `"dark"` {
		t.Errorf("theme was not preserved")
	}
}

// A commented config is refused, not stripped.
func TestCommentedConfigRefused(t *testing.T) {
	projectDir := mcpWithProject(t, map[string]string{
		"gocode.json": "{\n  // my servers\n  \"mcp\": {\"a\": {\"type\": \"remote\", \"url\": \"https://x\"}}\n}",
	}, nil)

	if _, err := SetMCPEnabled(projectDir, "a", false); err == nil {
		t.Fatal("expected an error for a commented config")
	} else if !strings.Contains(err.Error(), "comments") {
		t.Errorf("error should name the comments, got: %v", err)
	}
}

// An unknown server is a clear error, not a new entry.
func TestUnknownServerRefused(t *testing.T) {
	projectDir := mcpWithProject(t, map[string]string{
		"gocode.json": `{"mcp":{"a":{"type":"remote","url":"https://x"}}}`,
	}, nil)
	if _, err := SetMCPEnabled(projectDir, "nope", false); err == nil {
		t.Fatal("expected an error for an unknown server")
	}
	if _, err := RemoveMCPServer(projectDir, "nope"); err == nil {
		t.Fatal("expected an error for an unknown server")
	}
}

// mcpGlobalDir returns the seeded global config dir.
func mcpGlobalDir(t *testing.T) string {
	t.Helper()
	return filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "gocode")
}
