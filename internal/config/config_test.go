package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStripJSONC(t *testing.T) {
	input := `{
		// line comment with "quote inside"
		"url": "https://example.com/api", /* block
			comment */
		"nested": {"a": 1,},
		"list": [1, 2,],
	}`
	got, err := StripJSONC(input)
	if err != nil {
		t.Fatal(err)
	}
	var parsed map[string]any
	if err := jsonUnmarshal([]byte(got), &parsed); err != nil {
		t.Fatalf("stripped output must be valid JSON: %v (%q)", err, got)
	}
	if _, ok := parsed["nested"]; !ok {
		t.Fatalf("expected nested object, got %q", got)
	}
}

func TestStripJSONCValidJSON(t *testing.T) {
	input := `{
		// comment
		"model": "zhipuai/glm-5.3-flash", // trailing
		"provider": {
			"zhipuai": {
				"options": {"apiKey": "k//not-a-comment", "baseURL": "http://x"},
			},
		},
	}`
	stripped, err := StripJSONC(input)
	if err != nil {
		t.Fatal(err)
	}
	// parse check happens in Load; here just verify slashes inside strings survive
	if !contains(stripped, "k//not-a-comment") {
		t.Fatalf("slashes inside strings must survive: %q", stripped)
	}
}

func contains(haystack, needle string) bool {
	return len(needle) > 0 && indexOf(haystack, needle) >= 0
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}

func TestPermissionFlat(t *testing.T) {
	var p Permission
	if err := unmarshal(`"ask"`, &p); err != nil {
		t.Fatal(err)
	}
	rules, err := p.Ruleset()
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != 1 || rules[0].Action != "*" || rules[0].Effect != "ask" {
		t.Fatalf("unexpected rules: %+v", rules)
	}
}

func TestPermissionMap(t *testing.T) {
	var p Permission
	if err := unmarshal(`{"edit": "allow", "bash": {"git status": "allow", "*": "ask"}}`, &p); err != nil {
		t.Fatal(err)
	}
	rules, err := p.Ruleset()
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != 3 {
		t.Fatalf("expected 3 rules, got %+v", rules)
	}
}

func TestPermissionInvalid(t *testing.T) {
	var p Permission
	if err := unmarshal(`{"edit": "sometimes"}`, &p); err == nil {
		t.Fatal("expected error for invalid effect")
	}
}

func unmarshal(data string, out any) error {
	return jsonUnmarshal([]byte(data), out)
}

// TestLoadWorktreeBoundary mirrors the original: project config is read from
// the cwd up to the git worktree root, never above it.
func TestLoadWorktreeBoundary(t *testing.T) {
	originalWD, _ := os.Getwd()
	root := t.TempDir()
	sub := filepath.Join(root, "repo", "sub")
	above := t.TempDir()

	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("GOCODE_CONFIG", "")
	t.Setenv("GOCODE_CONFIG_CONTENT", "")
	t.Setenv("GOCODE_CONFIG_DIR", "")
	t.Setenv("GOCODE_DISABLE_PROJECT_CONFIG", "")

	write := func(dir, name, model string) {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(`{"model":"`+model+`"}`), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// "git repo": a .git marker at repo root; gocode.json at repo root.
	if err := os.MkdirAll(filepath.Join(root, "repo", ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(filepath.Join(root, "repo"), "gocode.json", "inside/repo")
	// A stray config above the repo must be ignored.
	write(above, "gocode.json", "outside/repo")

	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(originalWD) })
	if err := os.Chdir(sub); err != nil {
		t.Fatal(err)
	}
	got, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.Model != "inside/repo" {
		t.Fatalf("expected repo-root config, got %q", got.Model)
	}

	// cwd config overrides the repo root.
	write(sub, "gocode.json", "inside/sub")
	got, err = Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.Model != "inside/sub" {
		t.Fatalf("expected cwd config to override root, got %q", got.Model)
	}
}

func TestSubstituteEnv(t *testing.T) {
	t.Setenv("TEST_SUB_VAR", "hello")
	got, err := substitute(`{"token": "{env:TEST_SUB_VAR}"}`, "/tmp")
	if err != nil {
		t.Fatal(err)
	}
	if !contains(got, "hello") {
		t.Fatalf("expected env substitution, got %q", got)
	}
	// Unset vars substitute to empty, matching the original.
	got, err = substitute(`{"token": "{env:TEST_SUB_MISSING}"}`, "/tmp")
	if err != nil {
		t.Fatal(err)
	}
	if !contains(got, `""`) {
		t.Fatalf("expected empty substitution, got %q", got)
	}
}

func TestSubstituteFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "key.txt"), []byte("secret-value\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := substitute(`{"key": "{file:key.txt}"}`, dir)
	if err != nil {
		t.Fatal(err)
	}
	if !contains(got, "secret-value") {
		t.Fatalf("expected file substitution, got %q", got)
	}
	// Commented tokens are left alone.
	got, err = substitute("// {file:key.txt}\n{}", dir)
	if err != nil {
		t.Fatal(err)
	}
	if contains(got, "secret-value") {
		t.Fatalf("commented file token should be preserved, got %q", got)
	}
	// Missing file errors, matching the original's missing: "error".
	if _, err := substitute(`{"key": "{file:missing.txt}"}`, dir); err == nil {
		t.Fatal("expected error for missing file reference")
	}
}
