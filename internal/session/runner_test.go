package session

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/langazov/gocode-go/internal/agent"
	"github.com/langazov/gocode-go/internal/event"
	"github.com/langazov/gocode-go/internal/llm"
	"github.com/langazov/gocode-go/internal/permission"
	"github.com/langazov/gocode-go/internal/tool"
)

type fakeProvider struct {
	mu       sync.Mutex
	turns    [][]llm.StreamEvent
	requests []llm.Request
}

func (p *fakeProvider) Stream(ctx context.Context, request llm.Request, emit func(llm.StreamEvent)) error {
	p.mu.Lock()
	p.requests = append(p.requests, request)
	turn := p.turns[len(p.requests)-1]
	p.mu.Unlock()
	for _, streamEvent := range turn {
		emit(streamEvent)
	}
	return nil
}

func (p *fakeProvider) callCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.requests)
}

type fakeTool struct {
	name   string
	output string
	inputs []map[string]any
}

func (t *fakeTool) Name() string        { return t.name }
func (t *fakeTool) Description() string { return "fake tool" }
func (t *fakeTool) InputSchema() map[string]any {
	return map[string]any{"type": "object"}
}
func (t *fakeTool) Execute(ctx context.Context, input map[string]any) (string, error) {
	t.inputs = append(t.inputs, input)
	return t.output, nil
}

func newRunnerFixture(t *testing.T, provider *fakeProvider, tools *tool.Registry) (*Runner, *event.Bus) {
	t.Helper()
	bus, database := setup(t)
	RegisterRunnerProjectors(bus)
	return &Runner{
		DB:       database,
		Bus:      bus,
		Messages: NewMessageStore(database),
		Provider: provider,
		Tools:    tools,
		Agent:    "build",
		System:   "You are gocode.",
		Model:    ModelRef{ProviderID: "anthropic", ID: "claude-sonnet-4-5"},
	}, bus
}

func admitPrompt(t *testing.T, bus *event.Bus, runner *Runner, text string) {
	t.Helper()
	_, err := Admit(context.Background(), bus, runner.DB, AdmitInput{
		ID:        "msg_user_1",
		SessionID: "ses_1",
		Prompt:    Prompt{Text: text},
		Delivery:  DeliverySteer,
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestRunnerSingleTurn(t *testing.T) {
	provider := &fakeProvider{turns: [][]llm.StreamEvent{{
		{Type: llm.EventTextDelta, Text: "Hello"},
		{Type: llm.EventTextDelta, Text: " world"},
		{Type: llm.EventFinish, Finish: "end_turn", Usage: llm.Usage{Input: 10, Output: 5}},
	}}}
	runner, bus := newRunnerFixture(t, provider, tool.NewRegistry())
	admitPrompt(t, bus, runner, "say hello")

	if err := runner.Run(context.Background(), RunInput{SessionID: "ses_1"}); err != nil {
		t.Fatal(err)
	}
	if provider.callCount() != 1 {
		t.Fatalf("expected one provider turn, got %d", provider.callCount())
	}

	messages, err := runner.Messages.List(context.Background(), "ses_1")
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 {
		t.Fatalf("expected user + assistant messages, got %d", len(messages))
	}
	if messages[0].Type != TypeUser || messages[1].Type != TypeAssistant {
		t.Fatalf("unexpected message types: %s, %s", messages[0].Type, messages[1].Type)
	}
	assistant, err := DecodeAssistant(messages[1].Data)
	if err != nil {
		t.Fatal(err)
	}
	if len(assistant.Content) != 1 || assistant.Content[0].Text != "Hello world" {
		t.Fatalf("unexpected assistant content: %+v", assistant.Content)
	}
	if assistant.Finish != "end_turn" || assistant.Tokens == nil || assistant.Tokens.Input != 10 || assistant.Tokens.Output != 5 {
		t.Fatalf("unexpected settlement: finish=%s tokens=%+v", assistant.Finish, assistant.Tokens)
	}

	request := provider.requests[0]
	if request.System[0] != "You are gocode." {
		t.Fatalf("unexpected system: %v", request.System)
	}
	if len(request.Messages) != 1 || request.Messages[0].Content[0].Text != "say hello" {
		t.Fatalf("provider did not receive the promoted prompt: %+v", request.Messages)
	}
}

func TestRunnerNoWorkWithoutForce(t *testing.T) {
	provider := &fakeProvider{}
	runner, _ := newRunnerFixture(t, provider, tool.NewRegistry())
	if err := runner.Run(context.Background(), RunInput{SessionID: "ses_1"}); err != nil {
		t.Fatal(err)
	}
	if provider.callCount() != 0 {
		t.Fatalf("expected no provider turn without work, got %d", provider.callCount())
	}
}

func TestRunnerToolContinuation(t *testing.T) {
	provider := &fakeProvider{turns: [][]llm.StreamEvent{
		{
			{Type: llm.EventToolCall, ToolCall: &llm.ToolCall{ID: "call_1", Name: "echo", Input: map[string]any{"text": "hi"}}},
			{Type: llm.EventFinish, Finish: "tool_use"},
		},
		{
			{Type: llm.EventTextDelta, Text: "done"},
			{Type: llm.EventFinish, Finish: "end_turn"},
		},
	}}
	registry := tool.NewRegistry()
	echo := &fakeTool{name: "echo", output: "echoed: hi"}
	registry.Register(echo)
	runner, bus := newRunnerFixture(t, provider, registry)
	admitPrompt(t, bus, runner, "echo hi")

	if err := runner.Run(context.Background(), RunInput{SessionID: "ses_1"}); err != nil {
		t.Fatal(err)
	}
	if provider.callCount() != 2 {
		t.Fatalf("expected continuation turn, got %d calls", provider.callCount())
	}
	if len(echo.inputs) != 1 || echo.inputs[0]["text"] != "hi" {
		t.Fatalf("tool not executed with input: %+v", echo.inputs)
	}

	messages, err := runner.Messages.List(context.Background(), "ses_1")
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 3 {
		t.Fatalf("expected user + 2 assistant messages, got %d", len(messages))
	}
	first, err := DecodeAssistant(messages[1].Data)
	if err != nil {
		t.Fatal(err)
	}
	var toolPart *AssistantContent
	for i := range first.Content {
		if first.Content[i].Type == "tool" {
			toolPart = &first.Content[i]
		}
	}
	if toolPart == nil || toolPart.State == nil || toolPart.State.Status != ToolCompleted || toolPart.State.Output != "echoed: hi" {
		t.Fatalf("expected completed tool part, got %+v", toolPart)
	}

	second := provider.requests[1]
	hasToolResult := false
	for _, message := range second.Messages {
		if message.Role != llm.RoleTool {
			continue
		}
		for _, part := range message.Content {
			if part.Type == llm.PartToolResult && part.Result == "echoed: hi" && part.ToolCallID == "call_1" {
				hasToolResult = true
			}
		}
	}
	if !hasToolResult {
		t.Fatalf("continuation turn must include tool result: %+v", second.Messages)
	}
}

func TestRunnerMaxStepsDisablesTools(t *testing.T) {
	provider := &fakeProvider{turns: [][]llm.StreamEvent{{
		{Type: llm.EventTextDelta, Text: "summary"},
		{Type: llm.EventFinish, Finish: "end_turn"},
	}}}
	registry := tool.NewRegistry()
	registry.Register(&fakeTool{name: "echo", output: "x"})
	runner, bus := newRunnerFixture(t, provider, registry)
	runner.MaxSteps = 1
	admitPrompt(t, bus, runner, "do work")

	if err := runner.Run(context.Background(), RunInput{SessionID: "ses_1"}); err != nil {
		t.Fatal(err)
	}
	request := provider.requests[0]
	if len(request.Tools) != 0 {
		t.Fatalf("tools must be disabled on the last step, got %d", len(request.Tools))
	}
	if request.ToolChoice != "none" {
		t.Fatalf("expected tool_choice none, got %q", request.ToolChoice)
	}
	last := request.Messages[len(request.Messages)-1]
	if last.Role != llm.RoleAssistant || !strings.Contains(last.Content[0].Text, "MAXIMUM STEPS REACHED") {
		t.Fatalf("expected max-steps assistant prompt, got %+v", last)
	}
}

func TestRunnerPermissionDeniedFailsTool(t *testing.T) {
	provider := &fakeProvider{turns: [][]llm.StreamEvent{
		{
			{Type: llm.EventToolCall, ToolCall: &llm.ToolCall{ID: "call_1", Name: "bash", Input: map[string]any{"command": "ls"}}},
			{Type: llm.EventFinish, Finish: "tool_use"},
		},
		{
			{Type: llm.EventTextDelta, Text: "done"},
			{Type: llm.EventFinish, Finish: "end_turn"},
		},
	}}
	registry := tool.NewRegistry()
	bash := &fakeTool{name: "bash", output: "should not run"}
	registry.Register(bash)
	runner, bus := newRunnerFixture(t, provider, registry)
	engine := permission.NewEngine(
		permission.StaticRules{Rules: permission.Ruleset{{Action: "bash", Resource: "*", Effect: permission.Deny}}},
		nil, permission.Hooks{}, nil)
	runner.Permissions = &EnginePermissionGate{Engine: engine}
	admitPrompt(t, bus, runner, "run ls")

	if err := runner.Run(context.Background(), RunInput{SessionID: "ses_1"}); err != nil {
		t.Fatal(err)
	}
	if len(bash.inputs) != 0 {
		t.Fatalf("denied tool must not execute, ran %d times", len(bash.inputs))
	}
	messages, err := runner.Messages.List(context.Background(), "ses_1")
	if err != nil {
		t.Fatal(err)
	}
	first, err := DecodeAssistant(messages[1].Data)
	if err != nil {
		t.Fatal(err)
	}
	var toolPart *AssistantContent
	for i := range first.Content {
		if first.Content[i].Type == "tool" {
			toolPart = &first.Content[i]
		}
	}
	if toolPart == nil || toolPart.State == nil || toolPart.State.Status != ToolError {
		t.Fatalf("expected errored tool part after denial, got %+v", toolPart)
	}
	if toolPart.State.Error == "" {
		t.Fatal("expected a permission error message on the tool state")
	}
}

func TestRunnerPermissionAllowExecutes(t *testing.T) {
	provider := &fakeProvider{turns: [][]llm.StreamEvent{
		{
			{Type: llm.EventToolCall, ToolCall: &llm.ToolCall{ID: "call_1", Name: "bash", Input: map[string]any{"command": "ls"}}},
			{Type: llm.EventFinish, Finish: "tool_use"},
		},
		{
			{Type: llm.EventTextDelta, Text: "done"},
			{Type: llm.EventFinish, Finish: "end_turn"},
		},
	}}
	registry := tool.NewRegistry()
	bash := &fakeTool{name: "bash", output: "listed"}
	registry.Register(bash)
	runner, bus := newRunnerFixture(t, provider, registry)
	engine := permission.NewEngine(
		permission.StaticRules{Rules: permission.Ruleset{{Action: "bash", Resource: "*", Effect: permission.Allow}}},
		nil, permission.Hooks{}, nil)
	runner.Permissions = &EnginePermissionGate{Engine: engine}
	admitPrompt(t, bus, runner, "run ls")

	if err := runner.Run(context.Background(), RunInput{SessionID: "ses_1"}); err != nil {
		t.Fatal(err)
	}
	if len(bash.inputs) != 1 {
		t.Fatalf("allowed tool should execute once, ran %d times", len(bash.inputs))
	}
}

func TestRunnerOverflowRecovery(t *testing.T) {
	provider := &fakeProvider{turns: [][]llm.StreamEvent{
		{{Type: llm.EventProviderError, Error: errors.New("maximum context length exceeded")}},
		{{Type: llm.EventTextDelta, Text: "anchored summary"}, {Type: llm.EventFinish, Finish: "end_turn"}},
		{{Type: llm.EventTextDelta, Text: "done after compaction"}, {Type: llm.EventFinish, Finish: "end_turn"}},
	}}
	runner, bus := newRunnerFixture(t, provider, tool.NewRegistry())
	runner.Compactor = &Compactor{
		Bus:      bus,
		Provider: provider,
		Settings: CompactionSettings{Auto: true, Buffer: 20000, KeepTokens: 0},
	}
	admitPrompt(t, bus, runner, "a very long conversation")

	if err := runner.Run(context.Background(), RunInput{SessionID: "ses_1"}); err != nil {
		t.Fatal(err)
	}
	if provider.callCount() != 3 {
		t.Fatalf("expected overflow turn + summary turn + retry turn, got %d calls", provider.callCount())
	}
	messages, err := runner.Messages.List(context.Background(), "ses_1")
	if err != nil {
		t.Fatal(err)
	}
	var sawCompaction bool
	var finalAssistant *AssistantMessage
	for _, message := range messages {
		if message.Type == "compaction" {
			sawCompaction = true
		}
		if message.Type == TypeAssistant {
			decoded, err := DecodeAssistant(message.Data)
			if err != nil {
				t.Fatal(err)
			}
			finalAssistant = &decoded
		}
	}
	if !sawCompaction {
		t.Fatal("expected a compaction message after overflow recovery")
	}
	if finalAssistant == nil || len(finalAssistant.Content) == 0 || finalAssistant.Content[0].Text != "done after compaction" {
		t.Fatalf("expected recovered assistant turn, got %+v", finalAssistant)
	}
}

func TestRunnerIncrementalTextProjection(t *testing.T) {
	provider := &fakeProvider{turns: [][]llm.StreamEvent{{
		{Type: llm.EventTextDelta, Text: "He"},
		{Type: llm.EventTextDelta, Text: "llo"},
		{Type: llm.EventTextDelta, Text: " world"},
		{Type: llm.EventFinish, Finish: "end_turn"},
	}}}
	runner, bus := newRunnerFixture(t, provider, tool.NewRegistry())
	admitPrompt(t, bus, runner, "say hello")

	if err := runner.Run(context.Background(), RunInput{SessionID: "ses_1"}); err != nil {
		t.Fatal(err)
	}
	messages, err := runner.Messages.List(context.Background(), "ses_1")
	if err != nil {
		t.Fatal(err)
	}
	assistant, err := DecodeAssistant(messages[1].Data)
	if err != nil {
		t.Fatal(err)
	}
	var textPart *AssistantContent
	for i := range assistant.Content {
		if assistant.Content[i].Type == "text" {
			textPart = &assistant.Content[i]
		}
	}
	if textPart == nil || textPart.Text != "Hello world" {
		t.Fatalf("expected accumulated text 'Hello world', got %+v", textPart)
	}
}

func TestRunnerAgentResolution(t *testing.T) {
	provider := &fakeProvider{turns: [][]llm.StreamEvent{{
		{Type: llm.EventTextDelta, Text: "hi"},
		{Type: llm.EventFinish, Finish: "end_turn"},
	}}}
	runner, bus := newRunnerFixture(t, provider, tool.NewRegistry())
	agents := agent.NewRegistry()
	agents.Update(agent.Info{
		ID:     "plan",
		Mode:   "primary",
		System: "You are the plan agent.",
		Model:  &agent.ModelRef{ProviderID: "openai", ID: "gpt-5"},
	})
	runner.Agents = agents
	if _, err := runner.DB.Exec(context.Background(),
		`UPDATE session SET agent = 'plan' WHERE id = 'ses_1'`); err != nil {
		t.Fatal(err)
	}
	admitPrompt(t, bus, runner, "hello")

	if err := runner.Run(context.Background(), RunInput{SessionID: "ses_1"}); err != nil {
		t.Fatal(err)
	}
	request := provider.requests[0]
	if request.ProviderID != "openai" || request.ModelID != "gpt-5" {
		t.Fatalf("expected agent model, got %s/%s", request.ProviderID, request.ModelID)
	}
	if len(request.System) != 1 || request.System[0] != "You are the plan agent." {
		t.Fatalf("expected agent system prompt, got %v", request.System)
	}
	messages, err := runner.Messages.List(context.Background(), "ses_1")
	if err != nil {
		t.Fatal(err)
	}
	assistant, err := DecodeAssistant(messages[1].Data)
	if err != nil {
		t.Fatal(err)
	}
	if assistant.Agent != "plan" || assistant.Model.ProviderID != "openai" || assistant.Model.ID != "gpt-5" {
		t.Fatalf("expected projected agent/model, got %+v", assistant)
	}
}

func TestRunnerSessionModelOverridesAgent(t *testing.T) {
	provider := &fakeProvider{turns: [][]llm.StreamEvent{{
		{Type: llm.EventFinish, Finish: "end_turn"},
	}}}
	runner, bus := newRunnerFixture(t, provider, tool.NewRegistry())
	agents := agent.NewRegistry()
	agents.Update(agent.Info{
		ID:    "plan",
		Mode:  "primary",
		Model: &agent.ModelRef{ProviderID: "openai", ID: "gpt-5"},
	})
	runner.Agents = agents
	if _, err := runner.DB.Exec(context.Background(),
		`UPDATE session SET agent = 'plan', model = '{"providerID":"anthropic","id":"claude-opus-5"}' WHERE id = 'ses_1'`); err != nil {
		t.Fatal(err)
	}
	admitPrompt(t, bus, runner, "hello")

	if err := runner.Run(context.Background(), RunInput{SessionID: "ses_1"}); err != nil {
		t.Fatal(err)
	}
	request := provider.requests[0]
	if request.ProviderID != "anthropic" || request.ModelID != "claude-opus-5" {
		t.Fatalf("session model must override agent model, got %s/%s", request.ProviderID, request.ModelID)
	}
}

func TestRunnerQueuedPromptWaitsForIdle(t *testing.T) {
	provider := &fakeProvider{turns: [][]llm.StreamEvent{
		{{Type: llm.EventTextDelta, Text: "first"}, {Type: llm.EventFinish, Finish: "end_turn"}},
		{{Type: llm.EventTextDelta, Text: "second"}, {Type: llm.EventFinish, Finish: "end_turn"}},
	}}
	runner, bus := newRunnerFixture(t, provider, tool.NewRegistry())
	if _, err := Admit(context.Background(), bus, runner.DB, AdmitInput{
		ID:        "msg_steer",
		SessionID: "ses_1",
		Prompt:    Prompt{Text: "first"},
		Delivery:  DeliverySteer,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := Admit(context.Background(), bus, runner.DB, AdmitInput{
		ID:        "msg_queue",
		SessionID: "ses_1",
		Prompt:    Prompt{Text: "second"},
		Delivery:  DeliveryQueue,
	}); err != nil {
		t.Fatal(err)
	}
	if err := runner.Run(context.Background(), RunInput{SessionID: "ses_1"}); err != nil {
		t.Fatal(err)
	}
	if provider.callCount() != 2 {
		t.Fatalf("expected one turn per delivery boundary, got %d", provider.callCount())
	}
	pending, err := HasPending(context.Background(), runner.DB, "ses_1", DeliveryQueue)
	if err != nil {
		t.Fatal(err)
	}
	if pending {
		t.Fatal("queued input should be promoted after the drain")
	}
}
