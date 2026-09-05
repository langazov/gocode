package main

import (
	"encoding/json"
	"testing"

	"github.com/langazov/gocode-go/internal/agent"
	"github.com/langazov/gocode-go/internal/permission"
	"github.com/langazov/gocode-go/internal/session"
)

// testPlansDir is the global plans directory the tests register plan mode
// against — the one place plan mode may write.
const testPlansDir = "/data/gocode/plans"

func planRegistry(t *testing.T, userRules permission.Ruleset) *agent.Registry {
	t.Helper()
	registry := agent.NewRegistry()
	defaults := permission.Defaults()
	registry.Update(agent.Info{
		ID:   "build",
		Mode: "primary",
		Permissions: permission.Merge(defaults, permission.Ruleset{
			{Action: "plan_enter", Resource: "*", Effect: permission.Allow},
		}, userRules),
	})
	registerPlanAgent(registry, defaults, userRules, testPlansDir)
	registerBuiltinSubagents(registry, defaults)
	return registry
}

func effectFor(t *testing.T, registry *agent.Registry, agentID, action, resource string) permission.Effect {
	t.Helper()
	info, ok := registry.Get(agentID)
	if !ok {
		t.Fatalf("agent %q is not registered", agentID)
	}
	return permission.Evaluate(action, resource, info.Permissions).Effect
}

// TestPlanAgentIsRegistered is the regression for the hang this fixes: with no
// `plan` entry, AgentRulesProvider.Configured falls through to
// MissingAgentPermissions and every tool call after plan_enter is denied.
func TestPlanAgentIsRegistered(t *testing.T) {
	registry := planRegistry(t, nil)

	info, ok := registry.Get(session.PlanAgentID)
	if !ok {
		t.Fatal("the plan agent is not registered")
	}
	if info.Mode != "primary" {
		t.Fatalf("plan must be selectable as a primary agent, got mode %q", info.Mode)
	}
	// Select is what the runner calls; an unregistered id comes back with a
	// nil Info, which is exactly the deny-all path.
	if selection := registry.Select(session.PlanAgentID); selection.Info == nil {
		t.Fatal("Select returned no agent info for plan")
	}
}

func TestPlanAgentIsReadOnly(t *testing.T) {
	registry := planRegistry(t, nil)

	// edit is the shared permission action for edit, write and apply_patch
	// (see session.permissionAction), so one rule covers all three.
	if got := effectFor(t, registry, session.PlanAgentID, "edit", "main.go"); got != permission.Deny {
		t.Fatalf("plan must deny edits, got %q", got)
	}
	for _, action := range []string{"read", "grep", "glob", "bash", "webfetch"} {
		if got := effectFor(t, registry, session.PlanAgentID, action, "*"); got != permission.Allow {
			t.Fatalf("plan must allow %s, got %q", action, got)
		}
	}
	if got := effectFor(t, registry, session.PlanAgentID, "task", "general"); got != permission.Deny {
		t.Fatalf("plan must not delegate to the general subagent, got %q", got)
	}
	if got := effectFor(t, registry, session.PlanAgentID, "task", "explore"); got != permission.Allow {
		t.Fatalf("plan must still delegate to explore, got %q", got)
	}
}

// TestPlanSwitchToolsAreScopedToTheirAgent covers the deny-by-default in
// permission.Defaults: each direction is reachable only from the agent it
// makes sense for.
func TestPlanSwitchToolsAreScopedToTheirAgent(t *testing.T) {
	registry := planRegistry(t, nil)

	cases := []struct {
		agent, action string
		want          permission.Effect
	}{
		{"build", "plan_enter", permission.Allow},
		{"build", "plan_exit", permission.Deny},
		{session.PlanAgentID, "plan_exit", permission.Allow},
		{session.PlanAgentID, "plan_enter", permission.Deny},
		{"general", "plan_enter", permission.Deny},
		{"general", "plan_exit", permission.Deny},
		{"explore", "plan_enter", permission.Deny},
	}
	for _, tc := range cases {
		if got := effectFor(t, registry, tc.agent, tc.action, "*"); got != tc.want {
			t.Errorf("%s/%s = %q, want %q", tc.agent, tc.action, got, tc.want)
		}
	}
}

// TestPlanAgentHonoursUserPermissionConfig mirrors agent.ts merging `user`
// last into every native agent.
func TestPlanAgentHonoursUserPermissionConfig(t *testing.T) {
	var userRules permission.Ruleset
	if err := json.Unmarshal([]byte(`[{"action":"bash","resource":"*","effect":"ask"}]`), &userRules); err != nil {
		t.Fatal(err)
	}
	registry := planRegistry(t, userRules)

	if got := effectFor(t, registry, session.PlanAgentID, "bash", "ls"); got != permission.Ask {
		t.Fatalf("user config must reach the plan agent, got %q", got)
	}
}

// ...but it stops at the read-only core. `"permission": {"edit": "allow"}` is a
// reasonable thing to put in a config to stop being asked, and it used to
// switch plan mode off silently — the mode would still say "plan" everywhere
// while the model edited files. Nothing overrides plan mode.
func TestUserConfigCannotOverridePlanMode(t *testing.T) {
	for _, raw := range []string{
		`[{"action":"edit","resource":"*","effect":"allow"}]`,
		`[{"action":"*","resource":"*","effect":"allow"}]`,
		`[{"action":"edit","resource":"main.go","effect":"allow"}]`,
	} {
		var userRules permission.Ruleset
		if err := json.Unmarshal([]byte(raw), &userRules); err != nil {
			t.Fatal(err)
		}
		registry := planRegistry(t, userRules)
		if got := effectFor(t, registry, session.PlanAgentID, "edit", "main.go"); got != permission.Deny {
			t.Errorf("%s overrode plan mode: edit = %q, want deny", raw, got)
		}
		// The same config still applies to build, which is where it belongs.
		if got := effectFor(t, registry, "build", "edit", "main.go"); got != permission.Allow {
			t.Errorf("%s should still apply to build: edit = %q, want allow", raw, got)
		}
	}
}

// Plan mode has to be able to write a plan somewhere. That somewhere is the
// global plans directory and nowhere else — deliberately outside every
// repository, so a planning session cannot leave anything behind in the
// working tree. Upstream also allows `.opencode/plans/*.md` inside the
// worktree; this port does not.
func TestPlanAgentWritesPlansOnlyInTheGlobalDirectory(t *testing.T) {
	registry := planRegistry(t, nil)

	if got := effectFor(t, registry, session.PlanAgentID, "edit", testPlansDir+"/feature.md"); got != permission.Allow {
		t.Errorf("plan mode must be able to write its plan, got %q", got)
	}
	if got := effectFor(t, registry, session.PlanAgentID,
		permission.ExternalDirectoryAction, testPlansDir+"/feature.md"); got != permission.Allow {
		t.Errorf("the plans directory must not prompt as an external directory, got %q", got)
	}
	for _, inRepo := range []string{
		".opencode/plans/feature.md",
		".gocode/plans/feature.md",
		"plans/feature.md",
		"docs/plan.md",
		"PLAN.md",
	} {
		if got := effectFor(t, registry, session.PlanAgentID, "edit", inRepo); got != permission.Deny {
			t.Errorf("plan mode wrote %s into the repository: %q", inRepo, got)
		}
	}
}
