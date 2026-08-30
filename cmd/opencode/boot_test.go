package main

import (
	"context"
	"os"
	"testing"

	"github.com/anomalyco/opencode-go/internal/modelstate"
	"github.com/anomalyco/opencode-go/internal/permission"
)

// TestBootStackUsesConfigModel is the regression for "TUI ignores config":
// with no --model flag, the config default model/provider must win.
func TestBootStackUsesConfigModel(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("OPENCODE_DISABLE_MODELS_FETCH", "true")
	t.Setenv("OPENCODE_CONFIG_CONTENT", `{
		"model": "zhipuai/glm-5.3-flash",
		"provider": {
			"zhipuai": {
				"options": {"apiKey": "cfg-key", "baseURL": "http://127.0.0.1:1"}
			}
		}
	}`)

	stack, err := bootStack(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if stack.ProviderID != "zhipuai" || stack.ModelID != "glm-5.3-flash" {
		t.Fatalf("expected config model zhipuai/glm-5.3-flash, got %s/%s", stack.ProviderID, stack.ModelID)
	}

	// Explicit flag still wins over config.
	stack, err = bootStack(context.Background(), "anthropic/claude-sonnet-4-5")
	if err != nil {
		t.Fatal(err)
	}
	if stack.ProviderID != "anthropic" {
		t.Fatalf("explicit flag must override config, got %s", stack.ProviderID)
	}
}

// TestBootStackPrefersLastUsedModel: after switching models in the interface,
// a restart resumes with the last-used model, beating the config default.
func TestBootStackPrefersLastUsedModel(t *testing.T) {
	workdir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("OPENCODE_DISABLE_MODELS_FETCH", "true")
	t.Setenv("OPENCODE_CONFIG_CONTENT", `{"model":"minimax-coding-plan/MiniMax-M3"}`)

	original, _ := os.Getwd()
	t.Cleanup(func() { os.Chdir(original) })
	if err := os.Chdir(workdir); err != nil {
		t.Fatal(err)
	}

	// No state: config default wins.
	stack, err := bootStack(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if stack.ProviderID != "minimax-coding-plan" || stack.ModelID != "MiniMax-M3" {
		t.Fatalf("expected config default, got %s/%s", stack.ProviderID, stack.ModelID)
	}

	// Persist a last-used model (as the SetModel endpoint does): it must win.
	if err := modelstate.Save(workdir, modelstate.Ref{ProviderID: "zai-coding-plan", ModelID: "glm-5.3"}); err != nil {
		t.Fatal(err)
	}
	stack, err = bootStack(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if stack.ProviderID != "zai-coding-plan" || stack.ModelID != "glm-5.3" {
		t.Fatalf("expected last-used model to win, got %s/%s", stack.ProviderID, stack.ModelID)
	}

	// Explicit flag beats everything.
	stack, err = bootStack(context.Background(), "anthropic/claude-sonnet-4-5")
	if err != nil {
		t.Fatal(err)
	}
	if stack.ProviderID != "anthropic" {
		t.Fatalf("flag must beat last-used, got %s/%s", stack.ProviderID, stack.ModelID)
	}
}

// TestBootStackDefaultPermissionsMatchTS is the regression for "the Go port
// asks for access a lot more than the original": TS's real default merges
// a "*": allow baseline into every agent (agent.ts:119-136,277), so an
// unlisted action like bash/edit/todowrite/an MCP tool is allowed, not
// asked. bootStack must do the same for the build agent (no permission
// config at all) and for a custom agent that has no permission block of
// its own.
func TestBootStackDefaultPermissionsMatchTS(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("OPENCODE_DISABLE_MODELS_FETCH", "true")
	t.Setenv("OPENCODE_CONFIG_CONTENT", `{
		"agent": {"reviewer": {"description": "reviews code"}}
	}`)

	stack, err := bootStack(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}

	build, ok := stack.Agents.Resolve("build")
	if !ok {
		t.Fatal("expected a build agent")
	}
	for _, action := range []string{"bash", "edit", "todowrite", "some-mcp-server_a-tool"} {
		if got := permission.Evaluate(action, "*", build.Permissions).Effect; got != permission.Allow {
			t.Errorf("build agent: action %q = %v, want allow (TS's default is allow-by-default)", action, got)
		}
	}

	reviewer, ok := stack.Agents.Resolve("reviewer")
	if !ok {
		t.Fatal("expected the configured \"reviewer\" agent")
	}
	if got := permission.Evaluate("bash", "*", reviewer.Permissions).Effect; got != permission.Allow {
		t.Fatalf("custom agent with no permission block: bash = %v, want allow (must inherit the default baseline, not an empty ruleset)", got)
	}
}

// TestBootStackExplicitPermissionMergesWithDefaults guards against explicit
// config in opencode.json silently wiping out the default baseline for
// every action it doesn't itself mention (this port used to replace the
// whole ruleset instead of merging, unlike TS's Permission.merge(defaults,
// user)).
func TestBootStackExplicitPermissionMergesWithDefaults(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("OPENCODE_DISABLE_MODELS_FETCH", "true")
	t.Setenv("OPENCODE_CONFIG_CONTENT", `{"permission": {"bash": "ask"}}`)

	stack, err := bootStack(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}

	build, ok := stack.Agents.Resolve("build")
	if !ok {
		t.Fatal("expected a build agent")
	}
	if got := permission.Evaluate("bash", "*", build.Permissions).Effect; got != permission.Ask {
		t.Fatalf("explicit config: bash = %v, want ask (the user's own rule)", got)
	}
	if got := permission.Evaluate("edit", "*", build.Permissions).Effect; got != permission.Allow {
		t.Fatalf("explicit config for bash only: edit = %v, want allow (must still inherit the default baseline for actions it didn't mention)", got)
	}
}
