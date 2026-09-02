package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anomalyco/opencode-go/internal/permission"
)

func TestParseAgentMarkdown(t *testing.T) {
	content := strings.Join([]string{
		"---",
		"description: Reviews pull requests",
		"mode: subagent",
		"model: anthropic/claude-sonnet-4-5",
		"temperature: 0.2",
		"steps: 25",
		"---",
		"You are a meticulous reviewer.",
		"",
		"Be concise.",
	}, "\n")

	parsed, err := ParseAgentMarkdown("/tmp/reviewer.md", content)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Name != "reviewer" {
		t.Fatalf("name = %q, want the filename", parsed.Name)
	}
	if parsed.Agent.Description != "Reviews pull requests" || parsed.Agent.Mode != "subagent" {
		t.Fatalf("agent = %+v", parsed.Agent)
	}
	if parsed.Agent.Model != "anthropic/claude-sonnet-4-5" {
		t.Fatalf("model = %q", parsed.Agent.Model)
	}
	if parsed.Agent.Temperature == nil || *parsed.Agent.Temperature != 0.2 {
		t.Fatalf("temperature = %v", parsed.Agent.Temperature)
	}
	if parsed.Agent.EffectiveSteps() != 25 {
		t.Fatalf("steps = %d", parsed.Agent.EffectiveSteps())
	}
	if !strings.HasPrefix(parsed.Agent.Prompt, "You are a meticulous reviewer.") {
		t.Fatalf("prompt = %q", parsed.Agent.Prompt)
	}
}

func TestParseAgentMarkdownFrontmatterNameWins(t *testing.T) {
	parsed, err := ParseAgentMarkdown("/tmp/file-name.md", "---\nname: explicit\n---\nbody")
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Name != "explicit" {
		t.Fatalf("name = %q", parsed.Name)
	}
}

func TestParseAgentMarkdownDefaultsMode(t *testing.T) {
	parsed, err := ParseAgentMarkdown("/tmp/a.md", "---\ndescription: x\n---\nbody")
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Agent.Mode != "all" {
		t.Fatalf("mode = %q, want the default", parsed.Agent.Mode)
	}
}

// TestParseAgentMarkdownPermissions checks that frontmatter permissions go
// through the same decoder as opencode.json rather than a parallel one.
func TestParseAgentMarkdownPermissions(t *testing.T) {
	parsed, err := ParseAgentMarkdown("/tmp/locked.md", strings.Join([]string{
		"---",
		"permission:",
		"  bash: deny",
		"  read: allow",
		"---",
		"body",
	}, "\n"))
	if err != nil {
		t.Fatal(err)
	}
	rules, err := parsed.Agent.Permission.Ruleset()
	if err != nil {
		t.Fatal(err)
	}
	if rule := permission.Evaluate("bash", "*", rules); rule.Effect != permission.Deny {
		t.Fatalf("bash effect = %q, want deny", rule.Effect)
	}
	if rule := permission.Evaluate("read", "x", rules); rule.Effect != permission.Allow {
		t.Fatalf("read effect = %q, want allow", rule.Effect)
	}
}

func TestLoadAgentMarkdownSkipsUnparseable(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "agent")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "good.md"), []byte("---\ndescription: ok\n---\nbody"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("ignored"), 0o644); err != nil {
		t.Fatal(err)
	}

	found := LoadAgentMarkdown(root)
	if len(found) != 1 || found[0].Name != "good" {
		t.Fatalf("found = %+v", found)
	}
}

func TestDiscoverAgentsPrecedence(t *testing.T) {
	project, global := t.TempDir(), t.TempDir()
	for _, spec := range []struct{ root, name, body string }{
		{project, "shared", "---\ndescription: project\n---\nbody"},
		{global, "shared", "---\ndescription: global\n---\nbody"},
		{global, "onlyglobal", "---\ndescription: global only\n---\nbody"},
	} {
		dir := filepath.Join(spec.root, "agent")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, spec.name+".md"), []byte(spec.body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	cfg := &Config{Agent: map[string]Agent{"configured": {Description: "from json"}}}
	cfg.DiscoverAgents(project, global)

	if cfg.Agent["shared"].Description != "project" {
		t.Fatalf("project agent lost to the global one: %+v", cfg.Agent["shared"])
	}
	if cfg.Agent["onlyglobal"].Description != "global only" {
		t.Fatalf("global-only agent missing: %+v", cfg.Agent)
	}
	if cfg.Agent["configured"].Description != "from json" {
		t.Fatal("a JSON-configured agent was overwritten by markdown")
	}
}

func TestWriteAgentMarkdownRoundTrips(t *testing.T) {
	root := t.TempDir()
	temperature := 0.35
	location, err := WriteAgentMarkdown(root, "reviewer", Agent{
		Description: "Reviews code: carefully",
		Mode:        "subagent",
		Model:       "anthropic/claude-sonnet-4-5",
		Temperature: &temperature,
		Prompt:      "Be thorough.",
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(location)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseAgentMarkdown(location, string(raw))
	if err != nil {
		t.Fatal(err)
	}
	// A description containing a colon must survive the write/read round trip.
	if parsed.Agent.Description != "Reviews code: carefully" {
		t.Fatalf("description = %q", parsed.Agent.Description)
	}
	if parsed.Agent.Mode != "subagent" || parsed.Agent.Model != "anthropic/claude-sonnet-4-5" {
		t.Fatalf("agent = %+v", parsed.Agent)
	}
	if parsed.Agent.Temperature == nil || *parsed.Agent.Temperature != 0.35 {
		t.Fatalf("temperature = %v", parsed.Agent.Temperature)
	}
	if parsed.Agent.Prompt != "Be thorough." {
		t.Fatalf("prompt = %q", parsed.Agent.Prompt)
	}
}

// TestWriteAgentMarkdownRoundTripsPermissions covers the `--permissions`
// allowlist: a wildcard deny plus per-action allows must survive being written
// and read back, including the bare `*` key.
func TestWriteAgentMarkdownRoundTripsPermissions(t *testing.T) {
	root := t.TempDir()
	var written Permission
	if err := written.UnmarshalJSON([]byte(`{"*":"deny","read":"allow","grep":"allow"}`)); err != nil {
		t.Fatal(err)
	}
	location, err := WriteAgentMarkdown(root, "locked", Agent{Mode: "subagent", Permission: written})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(location)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseAgentMarkdown(location, string(raw))
	if err != nil {
		t.Fatal(err)
	}
	rules, err := parsed.Agent.Permission.Ruleset()
	if err != nil {
		t.Fatal(err)
	}
	if rule := permission.Evaluate("bash", "*", rules); rule.Effect != permission.Deny {
		t.Fatalf("bash effect = %q, want deny from the wildcard", rule.Effect)
	}
	for _, action := range []string{"read", "grep"} {
		if rule := permission.Evaluate(action, "x", rules); rule.Effect != permission.Allow {
			t.Fatalf("%s effect = %q, want allow", action, rule.Effect)
		}
	}
}

func TestWriteAgentMarkdownRefusesOverwrite(t *testing.T) {
	root := t.TempDir()
	if _, err := WriteAgentMarkdown(root, "dup", Agent{Description: "first"}); err != nil {
		t.Fatal(err)
	}
	if _, err := WriteAgentMarkdown(root, "dup", Agent{Description: "second"}); err == nil {
		t.Fatal("expected the second write to be refused")
	}
}
