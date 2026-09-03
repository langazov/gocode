package session

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/langazov/gocode-go/internal/event"
	"github.com/langazov/gocode-go/internal/llm"
	"github.com/langazov/gocode-go/internal/tool"
)

// gateTool blocks in Execute until its release channel is closed, recording
// when it started. It is how the tests below prove overlap rather than mere
// interleaving.
type gateTool struct {
	name    string
	started chan struct{} // closed on first Execute
	release chan struct{} // Execute returns once this is closed
	once    sync.Once
}

func newGateTool(name string) *gateTool {
	return &gateTool{name: name, started: make(chan struct{}), release: make(chan struct{})}
}

func (t *gateTool) Name() string                { return t.name }
func (t *gateTool) Description() string         { return "gate tool" }
func (t *gateTool) InputSchema() map[string]any { return map[string]any{"type": "object"} }
func (t *gateTool) Execute(ctx context.Context, input map[string]any) (string, error) {
	t.once.Do(func() { close(t.started) })
	select {
	case <-t.release:
		return t.name + " done", nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func mustStart(t *testing.T, tool *gateTool, within time.Duration) {
	t.Helper()
	select {
	case <-tool.started:
	case <-time.After(within):
		t.Fatalf("%s never started: tools are not running concurrently", tool.name)
	}
}

// TestToolsSettleConcurrently is the phase 1 acceptance check. Three tool
// calls arrive in one stream; the middle one blocks. If settlement were
// sequential, tools B and C would never start while A is blocked.
func TestToolsSettleConcurrently(t *testing.T) {
	a, b, c := newGateTool("alpha"), newGateTool("beta"), newGateTool("gamma")
	tools := tool.NewRegistry()
	tools.Register(a)
	tools.Register(b)
	tools.Register(c)

	provider := &fakeProvider{turns: [][]llm.StreamEvent{
		{
			{Type: llm.EventToolCall, ToolCall: &llm.ToolCall{ID: "call_a", Name: "alpha", Input: map[string]any{}}},
			{Type: llm.EventToolCall, ToolCall: &llm.ToolCall{ID: "call_b", Name: "beta", Input: map[string]any{}}},
			{Type: llm.EventToolCall, ToolCall: &llm.ToolCall{ID: "call_c", Name: "gamma", Input: map[string]any{}}},
			{Type: llm.EventFinish, Finish: "tool_calls"},
		},
		{
			{Type: llm.EventTextDelta, Text: "all done"},
			{Type: llm.EventFinish, Finish: "end_turn"},
		},
	}}
	runner, bus := newRunnerFixture(t, provider, tools)
	admitPrompt(t, bus, runner, "run three tools")

	done := make(chan error, 1)
	go func() { done <- runner.Run(context.Background(), RunInput{SessionID: "ses_1"}) }()

	// All three must be in flight simultaneously before any is released.
	mustStart(t, a, 5*time.Second)
	mustStart(t, b, 5*time.Second)
	mustStart(t, c, 5*time.Second)

	// Release out of call order; settlement order must not matter.
	close(c.release)
	close(a.release)
	close(b.release)

	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("run did not complete")
	}

	settled := toolParts(t, runner)
	for _, id := range []string{"call_a", "call_b", "call_c"} {
		if settled[id].Status != ToolCompleted {
			t.Fatalf("%s status = %q, want %q", id, settled[id].Status, ToolCompleted)
		}
	}
}

// TestToolCalledPreservesStreamOrder pins the ordering guarantee: tool.called
// is published in stream order (the timeline layout depends on it) even though
// the tools settle in a different order.
func TestToolCalledPreservesStreamOrder(t *testing.T) {
	a, b := newGateTool("alpha"), newGateTool("beta")
	tools := tool.NewRegistry()
	tools.Register(a)
	tools.Register(b)

	provider := &fakeProvider{turns: [][]llm.StreamEvent{
		{
			{Type: llm.EventToolCall, ToolCall: &llm.ToolCall{ID: "call_a", Name: "alpha", Input: map[string]any{}}},
			{Type: llm.EventToolCall, ToolCall: &llm.ToolCall{ID: "call_b", Name: "beta", Input: map[string]any{}}},
			{Type: llm.EventFinish, Finish: "tool_calls"},
		},
		{{Type: llm.EventFinish, Finish: "end_turn"}},
	}}
	runner, bus := newRunnerFixture(t, provider, tools)

	var mu sync.Mutex
	var called, succeeded []string
	bus.Listen(func(payload event.Payload) {
		mu.Lock()
		defer mu.Unlock()
		callID, _ := payload.Data["callID"].(string)
		switch payload.Type {
		case ToolCalled.Type:
			called = append(called, callID)
		case ToolSuccess.Type:
			succeeded = append(succeeded, callID)
		}
	})

	admitPrompt(t, bus, runner, "run two tools")
	done := make(chan error, 1)
	go func() { done <- runner.Run(context.Background(), RunInput{SessionID: "ses_1"}) }()

	mustStart(t, a, 5*time.Second)
	mustStart(t, b, 5*time.Second)
	// Settle in reverse of call order.
	close(b.release)
	<-time.After(50 * time.Millisecond)
	close(a.release)

	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("run did not complete")
	}

	mu.Lock()
	defer mu.Unlock()
	if fmt.Sprint(called) != "[call_a call_b]" {
		t.Fatalf("tool.called order = %v, want stream order [call_a call_b]", called)
	}
	if fmt.Sprint(succeeded) != "[call_b call_a]" {
		t.Fatalf("tool.success order = %v, want completion order [call_b call_a]", succeeded)
	}
}

// TestToolConcurrencyIsBounded verifies the semaphore: with a cap of one, the
// second tool must not start until the first has settled.
func TestToolConcurrencyIsBounded(t *testing.T) {
	a, b := newGateTool("alpha"), newGateTool("beta")
	tools := tool.NewRegistry()
	tools.Register(a)
	tools.Register(b)

	provider := &fakeProvider{turns: [][]llm.StreamEvent{
		{
			{Type: llm.EventToolCall, ToolCall: &llm.ToolCall{ID: "call_a", Name: "alpha", Input: map[string]any{}}},
			{Type: llm.EventToolCall, ToolCall: &llm.ToolCall{ID: "call_b", Name: "beta", Input: map[string]any{}}},
			{Type: llm.EventFinish, Finish: "tool_calls"},
		},
		{{Type: llm.EventFinish, Finish: "end_turn"}},
	}}
	runner, bus := newRunnerFixture(t, provider, tools)
	runner.ToolConcurrency = 1
	admitPrompt(t, bus, runner, "run two tools")

	done := make(chan error, 1)
	go func() { done <- runner.Run(context.Background(), RunInput{SessionID: "ses_1"}) }()

	mustStart(t, a, 5*time.Second)
	select {
	case <-b.started:
		t.Fatal("beta started while the concurrency cap of 1 was held by alpha")
	case <-time.After(100 * time.Millisecond):
	}
	close(a.release)
	mustStart(t, b, 5*time.Second)
	close(b.release)

	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("run did not complete")
	}
}

// TestProviderExecutedToolIsNotDispatched checks the providerExecuted skip:
// the runner must settle from the provider's own output without invoking the
// local tool.
func TestProviderExecutedToolIsNotDispatched(t *testing.T) {
	local := &fakeTool{name: "websearch", output: "SHOULD NOT RUN"}
	tools := tool.NewRegistry()
	tools.Register(local)

	provider := &fakeProvider{turns: [][]llm.StreamEvent{
		{
			{Type: llm.EventToolCall, ToolCall: &llm.ToolCall{
				ID: "call_p", Name: "websearch", Input: map[string]any{"query": "go"},
				ProviderExecuted: true, Output: "provider result",
			}},
			{Type: llm.EventFinish, Finish: "tool_calls"},
		},
		{{Type: llm.EventFinish, Finish: "end_turn"}},
	}}
	runner, bus := newRunnerFixture(t, provider, tools)
	admitPrompt(t, bus, runner, "search")

	if err := runner.Run(context.Background(), RunInput{SessionID: "ses_1"}); err != nil {
		t.Fatal(err)
	}
	if len(local.inputs) != 0 {
		t.Fatalf("provider-executed tool was dispatched locally: %v", local.inputs)
	}
	settled := toolParts(t, runner)
	part, ok := settled["call_p"]
	if !ok {
		t.Fatal("no tool part for the provider-executed call")
	}
	if part.Status != ToolCompleted {
		t.Fatalf("status = %q, want %q", part.Status, ToolCompleted)
	}
	if part.Output != "provider result" {
		t.Fatalf("output = %q, want the provider's own result", part.Output)
	}
}

// toolParts collects every settled tool part across all assistant messages in
// the session, keyed by call ID. A turn that dispatches tools is followed by a
// continuation turn whose assistant message holds no tool parts, so looking at
// only the last message would miss them.
func toolParts(t *testing.T, runner *Runner) map[string]ToolState {
	t.Helper()
	messages, err := runner.Messages.List(context.Background(), "ses_1")
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]ToolState{}
	for _, message := range messages {
		if message.Type != TypeAssistant {
			continue
		}
		assistant, err := DecodeAssistant(message.Data)
		if err != nil {
			t.Fatal(err)
		}
		for _, part := range assistant.Content {
			if part.Type != "tool" || part.State == nil {
				continue
			}
			out[part.ID] = *part.State
		}
	}
	return out
}
