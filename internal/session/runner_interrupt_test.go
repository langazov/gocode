package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/langazov/gocode-go/internal/llm"
	"github.com/langazov/gocode-go/internal/tool"
)

// blockingProvider stalls inside the stream until its run context is canceled,
// which is what an interrupt does to a real provider call.
type blockingProvider struct{ started chan struct{} }

func (p *blockingProvider) Stream(ctx context.Context, request llm.Request, emit func(llm.StreamEvent)) error {
	emit(llm.StreamEvent{Type: llm.EventTextDelta, Text: "partial"})
	close(p.started)
	<-ctx.Done()
	return ctx.Err()
}

// The TUI decides a turn is over from the settled assistant message, so an
// interrupted run has to leave one behind. runTurnAttempt publishes on an
// uncancelable context precisely so this survives the cancellation — this
// pins that contract down.
//
// Note what the settlement does *not* include: projectStepFailed records
// `error` and `time.completed` but no `finish`. A consumer that treats a
// missing finish as "still running" will think an interrupted turn never
// ended (see internal/tui's hasUnfinishedAssistant).
func TestInterruptedRunStillSettlesTheAssistantMessage(t *testing.T) {
	provider := &blockingProvider{started: make(chan struct{})}
	runner, bus := newRunnerFixture(t, nil, tool.NewRegistry())
	runner.Provider = provider
	admitPrompt(t, bus, runner, "hello")

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runner.Run(ctx, RunInput{SessionID: "ses_1"}) }()

	select {
	case <-provider.started:
	case <-time.After(5 * time.Second):
		t.Fatal("the provider stream never started")
	}
	cancel()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the run never returned after cancellation")
	}

	messages, err := NewMessageStore(runner.DB).List(context.Background(), "ses_1")
	if err != nil {
		t.Fatal(err)
	}
	var assistant map[string]any
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Type != "assistant" {
			continue
		}
		if err := json.Unmarshal(messages[i].Data, &assistant); err != nil {
			t.Fatal(err)
		}
		break
	}
	if assistant == nil {
		t.Fatal("an interrupted run should still record its assistant message")
	}
	recorded, _ := assistant["error"].(map[string]any)
	if recorded == nil {
		t.Fatalf("expected the cancellation recorded as an error, got %v", assistant)
	}
	// Tagged as an interruption, not a failure — the port's
	// MessageAbortedError. The TUI branches on this to suppress the error
	// block, mute the settlement icon, and report "interrupted" in the footer.
	if recorded["type"] != ErrorTypeAborted {
		t.Fatalf("error type = %v, want %q", recorded["type"], ErrorTypeAborted)
	}
	timeMap, _ := assistant["time"].(map[string]any)
	if timeMap == nil || timeMap["completed"] == nil {
		t.Fatalf("expected a completion timestamp marking the turn settled, got %v", assistant["time"])
	}
}

// A genuine provider failure must stay distinguishable from an interruption.
func TestStepErrorTagsOnlyCancellationAsAborted(t *testing.T) {
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if got := stepError(canceled, context.Canceled)["type"]; got != ErrorTypeAborted {
		t.Fatalf("a canceled context is an interruption, got %v", got)
	}
	if got := stepError(canceled, context.DeadlineExceeded)["type"]; got != ErrorTypeAborted {
		t.Fatalf("a deadline on a canceled run is an interruption, got %v", got)
	}
	if got := stepError(canceled, errors.New("503 upstream unavailable"))["type"]; got != ErrorTypeUnknown {
		t.Fatalf("a provider failure is not an interruption, got %v", got)
	}
}

// The regression for turns that settled as a bare "· interrupted" with no
// message: an http.Client deadline firing mid-stream returns
// context.DeadlineExceeded while the run context is still perfectly live, and
// tagging that as aborted made a provider timeout look exactly like the user
// pressing escape.
func TestStepErrorKeepsProviderTimeoutsVisible(t *testing.T) {
	live := context.Background()
	timeout := fmt.Errorf("Post %q: %w", "https://provider.example/v1/chat/completions", context.DeadlineExceeded)
	got := stepError(live, timeout)
	if got["type"] != ErrorTypeUnknown {
		t.Fatalf("a timeout on a live run is a failure, not an interruption: type = %v", got["type"])
	}
	if !strings.Contains(got["message"].(string), "context deadline exceeded") {
		t.Fatalf("the failure must carry the provider's own message, got %v", got["message"])
	}
	if got := stepError(live, context.Canceled)["type"]; got != ErrorTypeUnknown {
		t.Fatalf("a cancellation the user did not order is a failure, got %v", got)
	}
}

// wedgedTool ignores its context entirely: nothing short of process exit can
// unblock it. This is the shape a misbehaving MCP server or an unkillable
// orphan takes — and, before the abandonment below, it held the turn open
// forever, which the interface showed as a spinner that no double-escape
// could stop.
type wedgedTool struct {
	started chan struct{}
}

func (t *wedgedTool) Name() string        { return "wedged" }
func (t *wedgedTool) Description() string { return "a tool that never returns" }
func (t *wedgedTool) InputSchema() map[string]any {
	return map[string]any{"type": "object"}
}

func (t *wedgedTool) Execute(ctx context.Context, input map[string]any) (string, error) {
	close(t.started)
	<-make(chan struct{}) // park forever; ctx is deliberately not consulted
	return "", nil
}

// The regression for "double escape does nothing, the spinner keeps going": a
// turn whose tool ignores its context used to wedge the drain loop open. The
// loop now abandons in-flight tools drainDeadline after the run context is
// cancelled, settles the turn as interrupted, and — critically for the
// interface — releases the coordinator entry, which is what Busy() reads.
func TestInterruptAbandonsWedgedTool(t *testing.T) {
	wedged := &wedgedTool{started: make(chan struct{})}
	tools := tool.NewRegistry()
	tools.Register(wedged)
	provider := &fakeProvider{turns: [][]llm.StreamEvent{
		{
			{Type: llm.EventToolCall, ToolCall: &llm.ToolCall{ID: "call_1", Name: "wedged", Input: map[string]any{}}},
			{Type: llm.EventFinish, Finish: "tool_calls"},
		},
	}}
	runner, bus := newRunnerFixture(t, provider, tools)
	admitPrompt(t, bus, runner, "go on then")

	// Route the run through Execution, exactly as the server does, so the
	// interrupt below exercises the real double-escape path.
	lookup := &fixedLookup{}
	execution := NewExecution(lookup, runner)
	runDone := make(chan error, 1)
	go func() {
		runDone <- execution.Resume(context.Background(), "ses_1")
	}()

	select {
	case <-wedged.started:
	case <-time.After(5 * time.Second):
		t.Fatal("the wedged tool never started")
	}

	// The user's double escape: Service.Interrupt lands here.
	interruptDone := make(chan struct{})
	go func() {
		execution.Interrupt("ses_1")
		close(interruptDone)
	}()

	select {
	case <-interruptDone:
	case <-time.After(10 * time.Second):
		t.Fatal("Interrupt never returned: the wedged tool still owns the turn")
	}
	select {
	case <-runDone:
	case <-time.After(5 * time.Second):
		t.Fatal("the run never returned after the interrupt")
	}

	// The settled message must mark the turn interrupted, or the interface
	// keeps showing it as running.
	assistant := lastAssistantData(t, runner)
	recorded, _ := assistant["error"].(map[string]any)
	if recorded == nil || recorded["type"] != ErrorTypeAborted {
		t.Fatalf("an abandoned turn settles as interrupted, got %v", assistant["error"])
	}
	timeMap, _ := assistant["time"].(map[string]any)
	if timeMap == nil || timeMap["completed"] == nil {
		t.Fatalf("expected a completion timestamp marking the turn settled, got %v", assistant["time"])
	}
}

// fixedLookup answers exists without touching the database; the drain's
// existence probe is not what these tests exercise.
type fixedLookup struct{}

func (l *fixedLookup) Exists(ctx context.Context, sessionID string) (bool, error) {
	return true, nil
}
