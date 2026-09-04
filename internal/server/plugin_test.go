package server

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/langazov/gocode-go/internal/config"
	"github.com/langazov/gocode-go/internal/plugin"
)

// GET /api/plugin reports the loaded plugins with their tier and state, and
// is registered only when the server was given a plugin host.
func TestListPlugins(t *testing.T) {
	host := plugin.NewHost(nil)
	hooks := &plugin.Hooks{}
	host.Add(&plugin.Instance{
		ID: "rag-plugin", Spec: "./cmd/rag-plugin", Source: plugin.SourceProcess,
		Hooks: hooks,
	})
	// Instance.state is set by Spawn for real process plugins; unset it reads
	// as "loaded", the same default a native plugin gets.
	srv := Server{Plugins: host}

	rec := httptest.NewRecorder()
	srv.Mux().ServeHTTP(rec, httptest.NewRequest("GET", "/api/plugin", nil))
	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var body struct {
		Plugins    []pluginStatus      `json:"plugins"`
		Configured []config.PluginSpec `json:"configured"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if len(body.Plugins) != 1 || body.Plugins[0].ID != "rag-plugin" || body.Plugins[0].State != "loaded" {
		t.Fatalf("plugins = %+v, want one loaded rag-plugin", body.Plugins)
	}
	if body.Plugins[0].Source != "process" {
		t.Errorf("source = %q, want process", body.Plugins[0].Source)
	}
	if body.Plugins[0].Hooks == nil || body.Plugins[0].Tools == nil {
		t.Errorf("hooks/tools must be arrays, never null: %+v", body.Plugins[0])
	}
	if body.Configured == nil {
		t.Errorf("configured must be an array, never null")
	}
}

// The response also carries what is installed under a config directory's
// plugin folder but missing from the `plugin` array — the rows the interface
// offers as disabled. What the config already names is not repeated there.
func TestListPluginsReportsInstalledButUnconfigured(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GOCODE_TEST_HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("GOCODE_CONFIG_DIR", "")
	t.Setenv("GOCODE_DISABLE_PROJECT_CONFIG", "true")

	root := plugin.InstallRoot()
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"lint", "rag"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	srv := Server{
		Plugins: plugin.NewHost(nil),
		Config:  &config.Config{Plugin: []config.PluginSpec{{Ref: "rag"}}},
	}
	rec := httptest.NewRecorder()
	srv.Mux().ServeHTTP(rec, httptest.NewRequest("GET", "/api/plugin", nil))
	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var body pluginStatusResponse
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if len(body.Available) != 1 || body.Available[0].Name != "lint" {
		t.Fatalf("available = %+v, want just the unconfigured lint", body.Available)
	}
	if body.Available[0].Ref != "lint" {
		t.Errorf("ref = %q, want the bare name the loader resolves", body.Available[0].Ref)
	}
}

// Without a plugin host the route does not exist (the loader always produces
// a host, so this only happens in tests assembling a partial server).
func TestListPluginsWithoutHost(t *testing.T) {
	srv := Server{}
	rec := httptest.NewRecorder()
	srv.Mux().ServeHTTP(rec, httptest.NewRequest("GET", "/api/plugin", nil))
	if rec.Code != 404 {
		t.Fatalf("expected 404 without a plugin host, got %d", rec.Code)
	}
}
