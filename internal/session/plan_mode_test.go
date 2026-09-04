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
func planRuleset() permission.Ruleset {
	return permission.Merge(permission.Defaults(), permission.Ruleset{
		{Action: "plan_exit", Resource: "*", Effect: permission.Allow},
		{Action: "edit", Resource: "*", Effect: permission.Deny},
		{Action: "task", Resource: "general", Effect: permission.Deny},
	})
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
	t.Helper()
	runner, _ := newRunnerFixture(t, provider, tools)
	registry := agent.NewRegistry()
	registry.Update(agent.Info{ID: BuildAgentID, Mode: "primary", Permissions: permission.Defaults()})
	if registered {
		registry.Update(agent.Info{ID: PlanAgentID, Mode: "primary", Permissions: planRuleset()})
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
