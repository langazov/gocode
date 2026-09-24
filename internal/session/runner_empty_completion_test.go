package session

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/langazov/gocode-go/internal/event"
	"github.com/langazov/gocode-go/internal/llm"
	"github.com/langazov/gocode-go/internal/tool"
)

// The regression for the "session silently ends" report (ses_f2dea65 on
// nemotron :free, and once on zai-glm-5-3): a provider whose stream closes
// cleanly having said nothing used to settle as a normal end-of-turn — no
// message, no error, spinner gone — and the session looked finished.

// emptyThenAnswerProvider returns an empty completion for the first attempts,
// then answers normally.
type emptyThenAnswerProvider struct {
	mu       sync.Mutex
	requests []llm.Request
	empties  int
	finishOn string // finish reason stamped on the empty attempts
}

func (p *emptyThenAnswerProvider) Stream(ctx context.Context, request llm.Request, emit func(llm.StreamEvent)) error {
	p.mu.Lock()
	p.requests = append(p.requests, request)
	attempt := len(p.requests)
	empty := attempt <= p.empties
	finishOn := p.finishOn
	p.mu.Unlock()
	if empty {
		if finishOn != "" {
			emit(llm.StreamEvent{Type: llm.EventFinish, Finish: finishOn})
		}
		return nil
	}
	emit(llm.StreamEvent{Type: llm.EventTextDelta, Text: "answered"})
	emit(llm.StreamEvent{Type: llm.EventFinish, Finish: "stop", Usage: llm.Usage{Output: 8}})
	return nil
}

func (p *emptyThenAnswerProvider) callCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.requests)
}

// A stream that ends without a single event — the nvidia :free shape: HTTP
// 200, SSE body, no frames. The runner must re-attempt it and the session
// must end with the retried answer, not a silent stop.
func TestEmptyStreamIsRetriedNotSettled(t *testing.T) {
	shortEmptyRetries(t)
	provider := &emptyThenAnswerProvider{empties: 1}
	runner, bus := newRunnerFixture(t, nil, tool.NewRegistry())
	runner.Provider = provider
	admitPrompt(t, bus, runner, "hello")

	if err := runner.Run(context.Background(), RunInput{SessionID: "ses_1"}); err != nil {
		t.Fatal(err)
	}
	if provider.callCount() != 2 {
		t.Fatalf("expected the empty attempt and exactly one retry, got %d", provider.callCount())
	}
	assertLastAssistantText(t, runner, "answered")
}

// The zai-glm-5-3 shape: a stated finish "stop" with zero content and zero
// output tokens. A finish reason alone must not make the emptiness look like
// a completed answer.
func TestEmptyFinishReasonIsRetriedNotSettled(t *testing.T) {
	shortEmptyRetries(t)
	provider := &emptyThenAnswerProvider{empties: 1, finishOn: "stop"}
	runner, bus := newRunnerFixture(t, nil, tool.NewRegistry())
	runner.Provider = provider
	admitPrompt(t, bus, runner, "hello")

	if err := runner.Run(context.Background(), RunInput{SessionID: "ses_1"}); err != nil {
		t.Fatal(err)
	}
	if provider.callCount() != 2 {
		t.Fatalf("expected the empty attempt and exactly one retry, got %d", provider.callCount())
	}
	assertLastAssistantText(t, runner, "answered")
}

// A provider that stays empty exhausts the bounded retries and settles a
// visible failure — the transcript must show why the turn ended.
func TestPersistentEmptyCompletionSettlesTheStep(t *testing.T) {
	shortEmptyRetries(t)
	provider := &emptyThenAnswerProvider{empties: emptyRetryAttempts + 1, finishOn: "stop"}
	runner, bus := newRunnerFixture(t, nil, tool.NewRegistry())
	runner.Provider = provider
	admitPrompt(t, bus, runner, "hello")

	err := runner.Run(context.Background(), RunInput{SessionID: "ses_1"})
	if err == nil || !strings.Contains(err.Error(), "empty response") {
		t.Fatalf("expected the empty-completion error, got %v", err)
	}
	if want := emptyRetryAttempts + 1; provider.callCount() != want {
		t.Fatalf("expected %d attempts before settling, got %d", want, provider.callCount())
	}
	assertSettledWith(t, runner, "empty response")
}

// A stream that produced only reasoning — thinking with no answer — is not
// the degenerate shape; it settles as a normal turn (the reasoning is content
// the user can read).
func TestReasoningOnlyStreamIsNotDegenerate(t *testing.T) {
	provider := &fakeProvider{turns: [][]llm.StreamEvent{{
		{Type: llm.EventReasoningDelta, Text: "thinking..."},
		{Type: llm.EventFinish, Finish: "stop"},
	}}}
	runner, bus := newRunnerFixture(t, provider, tool.NewRegistry())
	admitPrompt(t, bus, runner, "hello")

	if err := runner.Run(context.Background(), RunInput{SessionID: "ses_1"}); err != nil {
		t.Fatal(err)
	}
	if provider.callCount() != 1 {
		t.Fatalf("a reasoning-only answer is content, not a retry: got %d attempts", provider.callCount())
	}
}

// A retried empty attempt must leave nothing behind: one assistant message,
// the answer, and a waiting notice so the footer says why the pause happened.
func TestEmptyRetryLeavesOneAssistantMessage(t *testing.T) {
	shortEmptyRetries(t)
	provider := &emptyThenAnswerProvider{empties: 2, finishOn: "stop"}
	runner, bus := newRunnerFixture(t, nil, tool.NewRegistry())
	runner.Provider = provider
	var mu sync.Mutex
	waiting := 0
	bus.Listen(func(payload event.Payload) {
		if payload.Type == StepWaiting.Type {
			mu.Lock()
			waiting++
			mu.Unlock()
		}
	})
	admitPrompt(t, bus, runner, "hello")

	if err := runner.Run(context.Background(), RunInput{SessionID: "ses_1"}); err != nil {
		t.Fatal(err)
	}
	messages, err := NewMessageStore(runner.DB).List(context.Background(), "ses_1")
	if err != nil {
		t.Fatal(err)
	}
	assistants := 0
	for _, message := range messages {
		if message.Type == TypeAssistant {
			assistants++
		}
	}
	if assistants != 1 {
		t.Fatalf("expected exactly one assistant message after two empty attempts, got %d", assistants)
	}
	assertLastAssistantText(t, runner, "answered")
	mu.Lock()
	defer mu.Unlock()
	if waiting != 2 {
		t.Fatalf("expected a waiting notice per retry, got %d", waiting)
	}
}

// After tool results, a zero-token empty stream is the same upstream glitch
// as on a fresh prompt and is retried — but only the empty step re-runs; the
// tool step before it settled and must not execute again.
func TestEmptyAfterToolResultsIsRetried(t *testing.T) {
	shortEmptyRetries(t)
	echo := &fakeTool{name: "echo", output: "ok"}
	tools := tool.NewRegistry()
	tools.Register(echo)
	provider := &fakeProvider{turns: [][]llm.StreamEvent{
		{
			{Type: llm.EventToolCall, ToolCall: &llm.ToolCall{ID: "call_1", Name: "echo", Input: map[string]any{}}},
			{Type: llm.EventFinish, Finish: "tool_calls"},
		},
		{{Type: llm.EventFinish, Finish: "stop"}},
		{{Type: llm.EventTextDelta, Text: "done"}, {Type: llm.EventFinish, Finish: "stop", Usage: llm.Usage{Output: 1}}},
	}}
	runner, bus := newRunnerFixture(t, provider, tools)
	admitPrompt(t, bus, runner, "run it")

	if err := runner.Run(context.Background(), RunInput{SessionID: "ses_1"}); err != nil {
		t.Fatal(err)
	}
	if provider.callCount() != 3 {
		t.Fatalf("expected tool step, empty step, retried step; got %d requests", provider.callCount())
	}
	if len(echo.inputs) != 1 {
		t.Fatalf("the tool step settled before the empty one and must not re-run: executed %d times", len(echo.inputs))
	}
	assertLastAssistantText(t, runner, "done")
}

// The legitimate shape the zero-token guard exists for: after tool results a
// model may end its turn with no text, but it still bills its stop tokens.
// That is a finished turn, not a glitch — no retry, no failure.
func TestSilentFinishWithOutputTokensSettlesNormally(t *testing.T) {
	echo := &fakeTool{name: "echo", output: "ok"}
	tools := tool.NewRegistry()
	tools.Register(echo)
	provider := &fakeProvider{turns: [][]llm.StreamEvent{
		{
			{Type: llm.EventToolCall, ToolCall: &llm.ToolCall{ID: "call_1", Name: "echo", Input: map[string]any{}}},
			{Type: llm.EventFinish, Finish: "tool_calls"},
		},
		{{Type: llm.EventFinish, Finish: "end_turn", Usage: llm.Usage{Output: 3}}},
	}}
	runner, bus := newRunnerFixture(t, provider, tools)
	admitPrompt(t, bus, runner, "run it")

	if err := runner.Run(context.Background(), RunInput{SessionID: "ses_1"}); err != nil {
		t.Fatal(err)
	}
	if provider.callCount() != 2 {
		t.Fatalf("a token-billed empty finish is a real end of turn: got %d requests", provider.callCount())
	}
	if recorded := lastAssistantData(t, runner)["error"]; recorded != nil {
		t.Fatalf("expected a clean settle, got error %v", recorded)
	}
}

// alwaysEmptyProvider never answers, and signals its first request so a test
// can interrupt the run while it sits in the retry pause.
type alwaysEmptyProvider struct {
	once   sync.Once
	called chan struct{}
}

func (p *alwaysEmptyProvider) Stream(ctx context.Context, request llm.Request, emit func(llm.StreamEvent)) error {
	p.once.Do(func() { close(p.called) })
	return nil
}

// An interrupt during the pause between empty attempts is what stopped the
// turn, so that is what settles it — as aborted, not as an empty response.
func TestInterruptDuringEmptyRetrySettlesAborted(t *testing.T) {
	oldDelay := emptyRetryDelay
	emptyRetryDelay = time.Minute
	t.Cleanup(func() { emptyRetryDelay = oldDelay })
	provider := &alwaysEmptyProvider{called: make(chan struct{})}
	runner, bus := newRunnerFixture(t, nil, tool.NewRegistry())
	runner.Provider = provider
	admitPrompt(t, bus, runner, "hello")

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = runner.Run(ctx, RunInput{SessionID: "ses_1"})
	}()
	<-provider.called
	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the run did not end after the interrupt")
	}
	recorded, _ := lastAssistantData(t, runner)["error"].(map[string]any)
	if recorded == nil || recorded["type"] != ErrorTypeAborted {
		t.Fatalf("expected the step settled as aborted, got %v", recorded)
	}
}

// shortEmptyRetries collapses the retry delay so tests run instantly.
func shortEmptyRetries(t *testing.T) {
	t.Helper()
	oldDelay := emptyRetryDelay
	emptyRetryDelay = time.Millisecond
	t.Cleanup(func() { emptyRetryDelay = oldDelay })
}

// assertLastAssistantText reads the settled assistant message's text part.
func assertLastAssistantText(t *testing.T, runner *Runner, want string) {
	t.Helper()
	assistant := lastAssistantData(t, runner)
	content, _ := assistant["content"].([]any)
	for _, item := range content {
		part, _ := item.(map[string]any)
		if part["type"] != "text" {
			continue
		}
		if text, _ := part["text"].(string); text != want {
			t.Fatalf("assistant text = %q, want %q", text, want)
		}
		return
	}
	t.Fatalf("no text part on the assistant message: %v", assistant)
}
