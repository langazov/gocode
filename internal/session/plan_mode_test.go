package session

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/langazov/gocode-go/internal/agent"
	"github.com/langazov/gocode-go/internal/llm"
	"github.com/langazov/gocode-go/internal/permission"
	"github.com/langazov/gocode-go/internal/tool"
	"github.com/langazov/gocode-go/internal/tool/builtins"
)

// planRuleset mirrors what cmd/gocode registers for the plan agent. Duplicated
// rather than imported because the registration lives in package main; the
// rules themselves are asserted there, and what this file cares about is that
// a registered plan agent is what the runner and the permission engine
// actually resolve.
func planRuleset() permission.Ruleset { return planRulesetFor("") }

// planRulesetFor is planRuleset with the one place plan mode may write: the
// global plans directory, outside every repository.
func planRulesetFor(plansDir string) permission.Ruleset {
	rules := permission.Ruleset{
		{Action: "plan_exit", Resource: "*", Effect: permission.Allow},
		{Action: "edit", Resource: "*", Effect: permission.Deny},
		{Action: "task", Resource: "general", Effect: permission.Deny},
	}
	if plansDir != "" {
		glob := filepath.ToSlash(filepath.Join(plansDir, "*"))
		rules = append(rules,
			permission.Rule{Action: "edit", Resource: glob, Effect: permission.Allow},
			permission.Rule{Action: permission.ExternalDirectoryAction, Resource: glob, Effect: permission.Allow})
	}
	return permission.Merge(permission.Defaults(), rules)
}

// pinAgent writes the session's agent the way Service.SetAgent does, which is
// what plan_enter calls from inside a turn.
func pinAgent(t *testing.T, runner *Runner, agentID string) {
	t.Helper()
	if _, err := runner.DB.Exec(context.Background(),
		`UPDATE session SET agent = ? WHERE id = ?`, agentID, "ses_1"); err != nil {
		t.Fatal(err)
	}
}

func planRunnerFixture(t *testing.T, provider *fakeProvider, tools *tool.Registry, registered bool) *Runner {
	return planRunnerFixtureFor(t, provider, tools, registered, "")
}

func planRunnerFixtureFor(t *testing.T, provider *fakeProvider, tools *tool.Registry, registered bool, plansDir string) *Runner {
	t.Helper()
	runner, _ := newRunnerFixture(t, provider, tools)
	registry := agent.NewRegistry()
	registry.Update(agent.Info{ID: BuildAgentID, Mode: "primary", Permissions: permission.Defaults()})
	if registered {
		registry.Update(agent.Info{ID: PlanAgentID, Mode: "primary", Permissions: planRulesetFor(plansDir)})
	}
	runner.Agents = registry
	rules := &AgentRulesProvider{Agents: registry}
	runner.Permissions = &EnginePermissionGate{
		Engine: permission.NewEngine(rules, nil, permission.Hooks{}, nil),
	}
	return runner
}

// TestPlanModeRunsUnderThePlanAgent is the end-to-end shape of the bug this
// fixes: plan_enter pins the session to "plan" from inside a turn, and every
// step after that has to resolve to a real agent. It also pins the two halves
// of the constraint — the reminder the model is told about, and the edit denial
// that holds whether or not it listens.
func TestPlanModeRunsUnderThePlanAgent(t *testing.T) {
	workdir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workdir, "notes.md"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}

	provider := &fakeProvider{turns: [][]llm.StreamEvent{
		{
			{Type: llm.EventToolCall, ToolCall: &llm.ToolCall{
				ID: "call_read", Name: "read", Input: map[string]any{"path": "notes.md"},
			}},
			{Type: llm.EventFinish, Finish: "tool_use"},
		},
		{
			{Type: llm.EventToolCall, ToolCall: &llm.ToolCall{
				ID: "call_write", Name: "write",
				Input: map[string]any{"path": "notes.md", "content": "rewritten"},
			}},
			{Type: llm.EventFinish, Finish: "tool_use"},
		},
		{
			{Type: llm.EventTextDelta, Text: "Here is the plan"},
			{Type: llm.EventFinish, Finish: "end_turn"},
		},
	}}
	registry := tool.NewRegistry()
	builtins.Register(registry, workdir, nil)
	runner := planRunnerFixture(t, provider, registry, true)
	admitPrompt(t, runner.Bus, runner, "add a markdown language server")

	ctx := context.Background()
	pinAgent(t, runner, PlanAgentID)
	if err := runner.Run(ctx, RunInput{SessionID: "ses_1"}); err != nil {
		t.Fatal(err)
	}

	if provider.callCount() != 3 {
		t.Fatalf("expected 3 provider turns, got %d", provider.callCount())
	}

	// The read went through: plan mode is read-only, not inert. This is the
	// regression — with no plan agent registered every one of these is denied.
	if result := toolResultFor(provider.requests[1], "call_read"); !strings.Contains(result, "hello") {
		t.Fatalf("read should be allowed in plan mode, got %q", result)
	}
	// The write did not.
	if result := toolResultFor(provider.requests[2], "call_write"); !strings.Contains(result, "blocked") {
		t.Fatalf("write should be denied in plan mode, got %q", result)
	}
	if content, err := os.ReadFile(filepath.Join(workdir, "notes.md")); err != nil || string(content) != "hello" {
		t.Fatalf("plan mode wrote to disk: %q (%v)", content, err)
	}

	// Every turn carries the read-only charter, not just the first.
	for i, request := range provider.requests {
		if !strings.Contains(userText(request), "Plan Mode - System Reminder") {
			t.Fatalf("turn %d has no plan reminder", i)
		}
	}

	messages, err := runner.Messages.List(ctx, "ses_1")
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range messages {
		if message.Type != TypeAssistant {
			continue
		}
		assistant, err := DecodeAssistant(message.Data)
		if err != nil {
			t.Fatal(err)
		}
		if assistant.Agent != PlanAgentID {
			t.Fatalf("assistant message ran as %q, want plan", assistant.Agent)
		}
	}
}

// TestUnregisteredPlanAgentDeniesEverything documents why the registration is
// the fix and not a nicety: AgentRulesProvider answers an unknown agent with
// MissingAgentPermissions, so the session survives the switch and then refuses
// every tool — the "it stopped processing" symptom.
func TestUnregisteredPlanAgentDeniesEverything(t *testing.T) {
	workdir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workdir, "notes.md"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	provider := &fakeProvider{turns: [][]llm.StreamEvent{
		{
			{Type: llm.EventToolCall, ToolCall: &llm.ToolCall{
				ID: "call_read", Name: "read", Input: map[string]any{"path": "notes.md"},
			}},
			{Type: llm.EventFinish, Finish: "tool_use"},
		},
		{
			{Type: llm.EventTextDelta, Text: "I cannot do anything"},
			{Type: llm.EventFinish, Finish: "end_turn"},
		},
	}}
	registry := tool.NewRegistry()
	builtins.Register(registry, workdir, nil)
	runner := planRunnerFixture(t, provider, registry, false)
	admitPrompt(t, runner.Bus, runner, "add a markdown language server")

	ctx := context.Background()
	pinAgent(t, runner, PlanAgentID)
	if err := runner.Run(ctx, RunInput{SessionID: "ses_1"}); err != nil {
		t.Fatal(err)
	}

	if result := toolResultFor(provider.requests[1], "call_read"); !strings.Contains(result, "blocked") {
		t.Fatalf("an unregistered agent must deny even reads, got %q", result)
	}
}

// toolResultFor returns the recorded result for one tool call in a request.
func toolResultFor(request llm.Request, callID string) string {
	for _, message := range request.Messages {
		if message.Role != llm.RoleTool {
			continue
		}
		for _, part := range message.Content {
			if part.Type == llm.PartToolResult && part.ToolCallID == callID {
				return part.Result
			}
		}
	}
	return ""
}

// userText concatenates every user-role text part in a request.
func userText(request llm.Request) string {
	var out strings.Builder
	for _, message := range request.Messages {
		if message.Role != llm.RoleUser {
			continue
		}
		for _, part := range message.Content {
			out.WriteString(part.Text)
		}
	}
	return out.String()
}

// TestPlanModeDeniesShellWrites is the second half of the read-only
// constraint. Denying the `edit` action stops write/edit/apply_patch, but the
// shell was never held to it: a plan-mode session could — and did — run
// `echo x > file` and rewrite the repository it was supposed to be reading.
//
// The fix is in the shell tool, which now declares the paths a command would
// modify under the same `edit` action (builtins.ScanWrites), so one rule
// covers both routes to a file.
func TestPlanModeDeniesShellWrites(t *testing.T) {
	workdir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workdir, "notes.md"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}

	provider := &fakeProvider{turns: [][]llm.StreamEvent{
		{
			// Reading through the shell has to keep working: plan mode is
			// read-only, not inert.
			{Type: llm.EventToolCall, ToolCall: &llm.ToolCall{
				ID: "call_read", Name: "bash", Input: map[string]any{"command": "cat notes.md"},
			}},
			{Type: llm.EventFinish, Finish: "tool_use"},
		},
		{
			{Type: llm.EventToolCall, ToolCall: &llm.ToolCall{
				ID: "call_write", Name: "bash",
				Input: map[string]any{"command": "echo pwned > notes.md; echo new > added.txt"},
			}},
			{Type: llm.EventFinish, Finish: "tool_use"},
		},
		{
			{Type: llm.EventTextDelta, Text: "Here is the plan"},
			{Type: llm.EventFinish, Finish: "end_turn"},
		},
	}}
	registry := tool.NewRegistry()
	builtins.Register(registry, workdir, nil)
	runner := planRunnerFixture(t, provider, registry, true)
	admitPrompt(t, runner.Bus, runner, "plan a change to the notes")

	pinAgent(t, runner, PlanAgentID)
	if err := runner.Run(context.Background(), RunInput{SessionID: "ses_1"}); err != nil {
		t.Fatal(err)
	}

	if result := toolResultFor(provider.requests[1], "call_read"); !strings.Contains(result, "hello") {
		t.Fatalf("reading through the shell should still work in plan mode, got %q", result)
	}
	if result := toolResultFor(provider.requests[2], "call_write"); !strings.Contains(result, "blocked") {
		t.Fatalf("a shell write should be denied in plan mode, got %q", result)
	}
	if content, err := os.ReadFile(filepath.Join(workdir, "notes.md")); err != nil || string(content) != "hello" {
		t.Fatalf("plan mode rewrote a file through the shell: %q (%v)", content, err)
	}
	if _, err := os.Stat(filepath.Join(workdir, "added.txt")); err == nil {
		t.Fatal("plan mode created a file through the shell")
	}
}

// The gate is a rule, not a mode: with edit allowed — the default everywhere
// but plan mode — a shell write runs exactly as before, unprompted.
func TestBuildModeShellWritesAreUnaffected(t *testing.T) {
	workdir := t.TempDir()
	provider := &fakeProvider{turns: [][]llm.StreamEvent{
		{
			{Type: llm.EventToolCall, ToolCall: &llm.ToolCall{
				ID: "call_write", Name: "bash", Input: map[string]any{"command": "echo written > out.txt"},
			}},
			{Type: llm.EventFinish, Finish: "tool_use"},
		},
		{
			{Type: llm.EventTextDelta, Text: "done"},
			{Type: llm.EventFinish, Finish: "end_turn"},
		},
	}}
	registry := tool.NewRegistry()
	builtins.Register(registry, workdir, nil)
	runner := planRunnerFixture(t, provider, registry, true)
	admitPrompt(t, runner.Bus, runner, "write the file")

	pinAgent(t, runner, BuildAgentID)
	if err := runner.Run(context.Background(), RunInput{SessionID: "ses_1"}); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(workdir, "out.txt"))
	if err != nil || strings.TrimSpace(string(content)) != "written" {
		t.Fatalf("build mode should still write through the shell: %q (%v)", content, err)
	}
}

// TestPlanModeDeniesEveryWriteTool covers the whole set rather than `write`
// alone: all three map onto the single `edit` permission action
// (permissionAction in runner.go), so this is really a test that the mapping
// has not drifted — a tool that grew its own action name would slip out from
// under plan mode's one rule without anything else failing.
//
// Both halves are asserted deliberately: that the call was refused, and that
// the file on disk is untouched. The permission gate runs before the tool
// does, and the second assertion is what proves it.
func TestPlanModeDeniesEveryWriteTool(t *testing.T) {
	for _, tc := range []struct {
		name    string
		call    llm.ToolCall
		created string // a path the call would bring into existence
	}{
		{name: "write", call: llm.ToolCall{ID: "c", Name: "write", Input: map[string]any{
			"path": "notes.md", "content": "PWNED"}}},
		{name: "write creating a new file", call: llm.ToolCall{ID: "c", Name: "write", Input: map[string]any{
			"path": "created.md", "content": "PWNED"}}, created: "created.md"},
		{name: "edit", call: llm.ToolCall{ID: "c", Name: "edit", Input: map[string]any{
			"path": "notes.md", "oldString": "hello", "newString": "PWNED"}}},
		{name: "apply_patch", call: llm.ToolCall{ID: "c", Name: "apply_patch", Input: map[string]any{
			"patch": "*** Begin Patch\n*** Update File: notes.md\n@@\n-hello\n+PWNED\n*** End Patch"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			workdir := t.TempDir()
			if err := os.WriteFile(filepath.Join(workdir, "notes.md"), []byte("hello"), 0o644); err != nil {
				t.Fatal(err)
			}
			call := tc.call
			provider := &fakeProvider{turns: [][]llm.StreamEvent{
				{{Type: llm.EventToolCall, ToolCall: &call}, {Type: llm.EventFinish, Finish: "tool_use"}},
				{{Type: llm.EventTextDelta, Text: "ok"}, {Type: llm.EventFinish, Finish: "end_turn"}},
			}}
			registry := tool.NewRegistry()
			builtins.Register(registry, workdir, nil)
			runner := planRunnerFixture(t, provider, registry, true)
			admitPrompt(t, runner.Bus, runner, "plan a change")

			pinAgent(t, runner, PlanAgentID)
			if err := runner.Run(context.Background(), RunInput{SessionID: "ses_1"}); err != nil {
				t.Fatal(err)
			}

			if result := toolResultFor(provider.requests[1], "c"); !strings.Contains(result, "blocked") {
				t.Fatalf("%s should be denied in plan mode, got %q", tc.name, result)
			}
			if content, err := os.ReadFile(filepath.Join(workdir, "notes.md")); err != nil || string(content) != "hello" {
				t.Fatalf("%s altered a file in plan mode: %q (%v)", tc.name, content, err)
			}
			if tc.created != "" {
				if _, err := os.Stat(filepath.Join(workdir, tc.created)); err == nil {
					t.Fatalf("%s created %s in plan mode", tc.name, tc.created)
				}
			}
		})
	}
}

// TestPlanModeWritesPlansOnlyInTheGlobalDirectory is the whole constraint end
// to end, through the real tools: plan mode can write its plan, and only
// there. Everything it might otherwise reach for inside the repository — a
// PLAN.md at the root, a docs/ note, the .opencode/plans path upstream allows
// — is refused, so a planning session leaves the working tree exactly as it
// found it.
func TestPlanModeWritesPlansOnlyInTheGlobalDirectory(t *testing.T) {
	workdir := t.TempDir()
	plansDir := t.TempDir() // stands in for global.PlansDir, outside the repo

	plan := filepath.Join(plansDir, "feature.md")
	provider := &fakeProvider{turns: [][]llm.StreamEvent{
		{
			{Type: llm.EventToolCall, ToolCall: &llm.ToolCall{
				ID: "call_plan", Name: "write",
				Input: map[string]any{"path": plan, "content": "# Plan\n\nstep one"}}},
			{Type: llm.EventFinish, Finish: "tool_use"},
		},
		{
			{Type: llm.EventToolCall, ToolCall: &llm.ToolCall{
				ID: "call_repo", Name: "write",
				Input: map[string]any{"path": ".opencode/plans/feature.md", "content": "# Plan"}}},
			{Type: llm.EventFinish, Finish: "tool_use"},
		},
		{
			{Type: llm.EventToolCall, ToolCall: &llm.ToolCall{
				ID: "call_root", Name: "write",
				Input: map[string]any{"path": "PLAN.md", "content": "# Plan"}}},
			{Type: llm.EventFinish, Finish: "tool_use"},
		},
		{{Type: llm.EventTextDelta, Text: "planned"}, {Type: llm.EventFinish, Finish: "end_turn"}},
	}}

	registry := tool.NewRegistry()
	builtins.RegisterWith(registry, workdir, builtins.Options{AllowPaths: []string{plansDir}})
	runner := planRunnerFixtureFor(t, provider, registry, true, plansDir)
	admitPrompt(t, runner.Bus, runner, "plan the feature")

	pinAgent(t, runner, PlanAgentID)
	if err := runner.Run(context.Background(), RunInput{SessionID: "ses_1"}); err != nil {
		t.Fatal(err)
	}

	if result := toolResultFor(provider.requests[1], "call_plan"); strings.Contains(result, "blocked") {
		t.Fatalf("plan mode must be able to write its plan, got %q", result)
	}
	content, err := os.ReadFile(plan)
	if err != nil || !strings.Contains(string(content), "step one") {
		t.Fatalf("the plan was not written: %q (%v)", content, err)
	}

	// Each call's result comes back on the following turn, so turn N+1 holds
	// the result of the call made on turn N.
	for _, call := range []struct {
		turn int
		id   string
		path string
	}{
		{2, "call_repo", ".opencode/plans/feature.md"},
		{3, "call_root", "PLAN.md"},
	} {
		if result := toolResultFor(provider.requests[call.turn], call.id); !strings.Contains(result, "blocked") {
			t.Errorf("writing %s inside the repository should be denied, got %q", call.path, result)
		}
		if _, err := os.Stat(filepath.Join(workdir, call.path)); err == nil {
			t.Errorf("plan mode wrote %s into the repository", call.path)
		}
	}
}
