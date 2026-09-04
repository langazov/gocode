package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// withConfigDir seeds the global config dir and returns the canonical file's
// path.
func withConfigDir(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	configDir := filepath.Join(dir, "gocode")
	for name, content := range files {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(configDir, name)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(configDir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// global.Resolve reads XDG_CONFIG_HOME at call time, so a t.Setenv is
	// the whole setup.
	t.Setenv("XDG_CONFIG_HOME", dir)
	return filepath.Join(configDir, "gocode.json")
}

// SavePlugins rewrites the plugin array in place, preserving every other key.
func TestSavePluginsRewritesArray(t *testing.T) {
	path := withConfigDir(t, map[string]string{
		"gocode.json": `{"model":"anthropic/claude-sonnet-5","plugin":["a","b"]}`,
	})
	if err := SavePlugins([]PluginSpec{{Ref: "a"}}); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	var out struct {
		Model  string       `json:"model"`
		Plugin []PluginSpec `json:"plugin"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if out.Model != "anthropic/claude-sonnet-5" {
		t.Errorf("model = %q, want preserved", out.Model)
	}
	if len(out.Plugin) != 1 || out.Plugin[0].Ref != "a" {
		t.Errorf("plugin = %v, want just [a]", out.Plugin)
	}
}

// An empty save removes the key instead of writing "plugin": [].
func TestSavePluginsEmptyRemovesKey(t *testing.T) {
	path := withConfigDir(t, map[string]string{
		"gocode.json": `{"plugin":["a"],"theme":"dark"}`,
	})
	if err := SavePlugins(nil); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	var out map[string]json.RawMessage
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if _, ok := out["plugin"]; ok {
		t.Errorf("plugin key survived an empty save: %s", raw)
	}
	if string(out["theme"]) != `"dark"` {
		t.Errorf("theme = %s, want preserved", out["theme"])
	}
}

// A missing config file is created.
func TestSavePluginsCreatesFile(t *testing.T) {
	path := withConfigDir(t, nil)
	if err := SavePlugins([]PluginSpec{{Ref: "rag-plugin"}}); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	if string(raw) == "" {
		t.Fatal("nothing was written")
	}
	var out struct {
		Plugin []PluginSpec `json:"plugin"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Plugin) != 1 || out.Plugin[0].Ref != "rag-plugin" {
		t.Errorf("plugin = %v, want [rag-plugin]", out.Plugin)
	}
}

// A commented JSONC file is refused rather than silently stripped.
func TestSavePluginsRefusesJSONC(t *testing.T) {
	withConfigDir(t, map[string]string{
		"gocode.json": `{
			// my settings
			"plugin": ["a"]
		}`,
	})
	if err := SavePlugins([]PluginSpec{{Ref: "b"}}); err == nil {
		t.Fatal("expected an error saving over a commented config")
	}
}
