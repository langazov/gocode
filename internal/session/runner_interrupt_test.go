package session

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/anomalyco/opencode-go/internal/llm"
	"github.com/anomalyco/opencode-go/internal/tool"
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
	if got := stepError(context.Canceled)["type"]; got != ErrorTypeAborted {
		t.Fatalf("a canceled context is an interruption, got %v", got)
	}
	if got := stepError(context.DeadlineExceeded)["type"]; got != ErrorTypeAborted {
		t.Fatalf("a deadline is an interruption, got %v", got)
	}
	if got := stepError(errors.New("503 upstream unavailable"))["type"]; got != ErrorTypeUnknown {
		t.Fatalf("a provider failure is not an interruption, got %v", got)
	}
}
