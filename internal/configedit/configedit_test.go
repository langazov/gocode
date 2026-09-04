package configedit

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/langazov/gocode-go/internal/config"
	"github.com/langazov/gocode-go/internal/global"
)

// withHome points the global config directory at a scratch tree.
//
// XDG_CONFIG_HOME is the knob that matters: global.Resolve() derives Config
// from it and falls back to os.UserHomeDir(), so GOCODE_TEST_HOME — which only
// sets Paths.Home — does *not* redirect it. Getting that wrong means these
// tests edit the developer's own config, deleting whatever plugins and servers
// they had configured, so the resolved path is asserted before any test runs.
func withHome(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)

	resolved := global.Resolve().Config
	if !strings.HasPrefix(resolved, root) {
		t.Fatalf("refusing to run: config resolves to %s, outside the test directory %s", resolved, root)
	}
	return root
}

// configDir is where withHome's tree keeps the global config.
func configDir(root string) string { return filepath.Join(root, "gocode") }

// writeConfig seeds the global config and returns its path.
func writeConfig(t *testing.T, root, name, body string) string {
	t.Helper()
	dir := configDir(root)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// readBack decodes the file an edit reported writing.
func readBack(t *testing.T, path string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("config is not valid JSON after the edit: %v\n%s", err, raw)
	}
	return out
}

func TestEnablePluginCreatesConfig(t *testing.T) {
	home := withHome(t)

	result, err := EnablePlugin("rag-plugin", map[string]any{"embeddingProvider": "openai"})
	if err != nil {
		t.Fatalf("EnablePlugin: %v", err)
	}
	if !result.Changed {
		t.Error("first enable should report a change")
	}
	if filepath.Base(result.Path) != DefaultName {
		t.Errorf("wrote %s, want %s", result.Path, DefaultName)
	}
	if _, err := os.Stat(filepath.Join(configDir(home), DefaultName)); err != nil {
		t.Fatalf("config was not created: %v", err)
	}

	// The tuple form is what carries options; a bare string would silently
	// drop them.
	plugins := readBack(t, result.Path)["plugin"].([]any)
	entry, ok := plugins[0].([]any)
	if !ok {
		t.Fatalf("entry is %T, want the [ref, options] tuple", plugins[0])
	}
	if entry[0] != "rag-plugin" {
		t.Errorf("ref = %v, want rag-plugin", entry[0])
	}
	if got := entry[1].(map[string]any)["embeddingProvider"]; got != "openai" {
		t.Errorf("embeddingProvider = %v, want openai", got)
	}
}

func TestEnablePluginPreservesOtherKeys(t *testing.T) {
	home := withHome(t)
	writeConfig(t, home, "gocode.json", `{"mcp":{"k8s":{"type":"local"}},"model":"anthropic/claude-opus-5"}`)

	result, err := EnablePlugin("rag-plugin", nil)
	if err != nil {
		t.Fatalf("EnablePlugin: %v", err)
	}

	decoded := readBack(t, result.Path)
	if _, ok := decoded["mcp"]; !ok {
		t.Error("the mcp section was dropped")
	}
	if decoded["model"] != "anthropic/claude-opus-5" {
		t.Errorf("model = %v, want it preserved", decoded["model"])
	}
	// No options means the bare string form, not a one-element tuple.
	if got := decoded["plugin"].([]any)[0]; got != "rag-plugin" {
		t.Errorf("entry = %#v, want the bare string form", got)
	}
}

func TestEnablePluginIsIdempotent(t *testing.T) {
	withHome(t)
	options := map[string]any{"embeddingProvider": "openai"}

	if _, err := EnablePlugin("rag-plugin", options); err != nil {
		t.Fatalf("first enable: %v", err)
	}
	result, err := EnablePlugin("rag-plugin", options)
	if err != nil {
		t.Fatalf("second enable: %v", err)
	}
	// A reinstall must not churn the file: brew reinstalls are routine.
	if result.Changed {
		t.Error("re-enabling with the same options should report no change")
	}
	if !strings.Contains(result.Summary, "already enabled") {
		t.Errorf("summary = %q, want it to say already enabled", result.Summary)
	}
}

func TestEnablePluginReplacesDifferingOptions(t *testing.T) {
	withHome(t)
	if _, err := EnablePlugin("rag-plugin", map[string]any{"embeddingProvider": "openai"}); err != nil {
		t.Fatalf("first enable: %v", err)
	}

	result, err := EnablePlugin("rag-plugin", map[string]any{"embeddingProvider": "voyage"})
	if err != nil {
		t.Fatalf("second enable: %v", err)
	}
	if !result.Changed {
		t.Fatal("changed options should rewrite the entry")
	}

	// Replaced, not appended: a duplicate ref would load the plugin twice.
	plugins := readBack(t, result.Path)["plugin"].([]any)
	if len(plugins) != 1 {
		t.Fatalf("got %d entries, want 1: %#v", len(plugins), plugins)
	}
	if got := plugins[0].([]any)[1].(map[string]any)["embeddingProvider"]; got != "voyage" {
		t.Errorf("embeddingProvider = %v, want voyage", got)
	}
}

func TestDisablePlugin(t *testing.T) {
	home := withHome(t)
	writeConfig(t, home, "gocode.json", `{"plugin":["memory","rag-plugin"]}`)

	result, err := DisablePlugin("rag-plugin")
	if err != nil {
		t.Fatalf("DisablePlugin: %v", err)
	}
	if !result.Changed {
		t.Fatal("removing a listed plugin should report a change")
	}

	plugins := readBack(t, result.Path)["plugin"].([]any)
	if len(plugins) != 1 || plugins[0] != "memory" {
		t.Errorf("plugin = %#v, want only memory left", plugins)
	}
}

func TestDisablePluginNotListed(t *testing.T) {
	home := withHome(t)
	writeConfig(t, home, "gocode.json", `{"plugin":["memory"]}`)

	result, err := DisablePlugin("rag-plugin")
	if err != nil {
		t.Fatalf("DisablePlugin: %v", err)
	}
	if result.Changed {
		t.Error("removing something that was not there should not rewrite the file")
	}
}

func TestDisableLastPluginDropsTheKey(t *testing.T) {
	home := withHome(t)
	writeConfig(t, home, "gocode.json", `{"plugin":["rag-plugin"]}`)

	result, err := DisablePlugin("rag-plugin")
	if err != nil {
		t.Fatalf("DisablePlugin: %v", err)
	}
	if _, ok := readBack(t, result.Path)["plugin"]; ok {
		t.Error(`an empty "plugin" key should be removed, not left as []`)
	}
}

func TestEnableLSP(t *testing.T) {
	withHome(t)
	server := config.LSPServer{
		Command:    []string{"/opt/homebrew/opt/gocode/bin/mdlsp"},
		Extensions: []string{".md", ".markdown"},
	}

	result, err := EnableLSP("mdlsp", server)
	if err != nil {
		t.Fatalf("EnableLSP: %v", err)
	}
	if !result.Changed {
		t.Fatal("first enable should report a change")
	}

	entry := readBack(t, result.Path)["lsp"].(map[string]any)["mdlsp"].(map[string]any)
	if got := entry["command"].([]any)[0]; got != server.Command[0] {
		t.Errorf("command = %v, want %v", got, server.Command[0])
	}
	if len(entry["extensions"].([]any)) != 2 {
		t.Errorf("extensions = %v, want two entries", entry["extensions"])
	}

	// The written section must survive the loader's own union decoding, which
	// is the thing that actually consumes it.
	var decoded config.LSPConfig
	raw, err := json.Marshal(readBack(t, result.Path)["lsp"])
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("the loader cannot read the section we wrote: %v", err)
	}
	if _, ok := decoded.Servers()["mdlsp"]; !ok {
		t.Error("mdlsp is missing after a loader round trip")
	}
}

func TestEnableLSPKeepsOtherServers(t *testing.T) {
	home := withHome(t)
	writeConfig(t, home, "gocode.json", `{"lsp":{"gopls":{"disabled":true}}}`)

	result, err := EnableLSP("mdlsp", config.LSPServer{Command: []string{"mdlsp"}})
	if err != nil {
		t.Fatalf("EnableLSP: %v", err)
	}

	servers := readBack(t, result.Path)["lsp"].(map[string]any)
	if _, ok := servers["gopls"]; !ok {
		t.Error("the existing gopls entry was dropped")
	}
	if _, ok := servers["mdlsp"]; !ok {
		t.Error("mdlsp was not added")
	}
}

func TestEnableLSPIsIdempotent(t *testing.T) {
	withHome(t)
	server := config.LSPServer{Command: []string{"mdlsp"}, Extensions: []string{".md"}}

	if _, err := EnableLSP("mdlsp", server); err != nil {
		t.Fatalf("first enable: %v", err)
	}
	result, err := EnableLSP("mdlsp", server)
	if err != nil {
		t.Fatalf("second enable: %v", err)
	}
	if result.Changed {
		t.Error("re-registering an identical server should report no change")
	}
}

// `"lsp": false` switches every server off. Merging one server into that would
// quietly re-enable the rest, so it is refused.
func TestEnableLSPRefusesWhenSectionIsFalse(t *testing.T) {
	home := withHome(t)
	writeConfig(t, home, "gocode.json", `{"lsp":false}`)

	if _, err := EnableLSP("mdlsp", config.LSPServer{Command: []string{"mdlsp"}}); err == nil {
		t.Fatal("expected an error for a config that disables every server")
	}
}

func TestDisableLSP(t *testing.T) {
	home := withHome(t)
	writeConfig(t, home, "gocode.json", `{"lsp":{"mdlsp":{"command":["mdlsp"]},"gopls":{"disabled":true}}}`)

	result, err := DisableLSP("mdlsp")
	if err != nil {
		t.Fatalf("DisableLSP: %v", err)
	}
	if !result.Changed {
		t.Fatal("removing a configured server should report a change")
	}

	servers := readBack(t, result.Path)["lsp"].(map[string]any)
	if _, ok := servers["mdlsp"]; ok {
		t.Error("mdlsp is still configured")
	}
	if _, ok := servers["gopls"]; !ok {
		t.Error("gopls was dropped along with it")
	}
}

func TestDisableLastLSPDropsTheKey(t *testing.T) {
	home := withHome(t)
	writeConfig(t, home, "gocode.json", `{"lsp":{"mdlsp":{"command":["mdlsp"]}}}`)

	result, err := DisableLSP("mdlsp")
	if err != nil {
		t.Fatalf("DisableLSP: %v", err)
	}
	// An empty object reads as "configured, with nothing in it"; the default
	// is the absent key.
	if _, ok := readBack(t, result.Path)["lsp"]; ok {
		t.Error(`an empty "lsp" key should be removed`)
	}
}

// A commented config is the user's, and a rewrite would delete the comments.
func TestCommentedConfigIsRefusedNotMangled(t *testing.T) {
	home := withHome(t)
	body := "{\n  // my notes\n  \"plugin\": [\"memory\"]\n}\n"
	path := writeConfig(t, home, "gocode.jsonc", body)

	_, err := EnablePlugin("rag-plugin", nil)
	var commented *CommentedError
	if !errors.As(err, &commented) {
		t.Fatalf("err = %v, want a CommentedError", err)
	}
	// The message has to carry what to paste, since we are not doing it.
	if !strings.Contains(commented.Manual, "rag-plugin") {
		t.Errorf("manual snippet = %q, want it to name the plugin", commented.Manual)
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != body {
		t.Errorf("the file was modified:\n%s", after)
	}
}

func TestBrokenConfigIsReportedNotOverwritten(t *testing.T) {
	home := withHome(t)
	path := writeConfig(t, home, "gocode.json", `{"plugin": [`)

	if _, err := EnablePlugin("rag-plugin", nil); err == nil {
		t.Fatal("expected an error for malformed JSON")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != `{"plugin": [` {
		t.Errorf("a malformed config was rewritten:\n%s", raw)
	}
}

// config.json is merged before gocode.json, so an installer must edit the file
// that actually wins rather than creating a second one alongside it.
func TestExistingConfigJSONIsPreferred(t *testing.T) {
	home := withHome(t)
	writeConfig(t, home, "config.json", `{}`)

	result, err := EnablePlugin("rag-plugin", nil)
	if err != nil {
		t.Fatalf("EnablePlugin: %v", err)
	}
	if filepath.Base(result.Path) != "config.json" {
		t.Errorf("edited %s, want the existing config.json", result.Path)
	}
	if _, err := os.Stat(filepath.Join(configDir(home), DefaultName)); !os.IsNotExist(err) {
		t.Error("a second config file was created alongside the existing one")
	}
}
