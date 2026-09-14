package session

import (
	"context"
	"testing"
	"time"

	"github.com/langazov/gocode-go/internal/llm"
	"github.com/langazov/gocode-go/internal/tool"
)

// After an abandoned turn nobody reads settlements. A tool reporting back
// then used to block forever on the send, leaking its goroutine.
func TestSettleToolDoesNotBlockAfterTheTurnStops(t *testing.T) {
	registry := tool.NewRegistry()
	registry.Register(&fakeTool{name: "echo", output: "x"})
	runner, _ := newRunnerFixture(t, nil, registry)
	req := toolRequest{
		call:               llm.ToolCall{ID: "call_1", Name: "echo", Input: map[string]any{}},
		sessionID:          "ses_1",
		assistantMessageID: "msg_assistant",
		agentID:            "build",
	}
	settleReturns := func(t *testing.T, ctx context.Context, sem chan struct{}) {
		t.Helper()
		done := make(chan struct{})
		close(done) // the turn has stopped reading
		unread := make(chan settlement)
		finished := make(chan struct{})
		go func() {
			runner.settleTool(ctx, sem, done, req, unread)
			close(finished)
		}()
		select {
		case <-finished:
		case <-time.After(2 * time.Second):
			t.Fatal("settleTool blocked on a settlement nobody reads")
		}
	}

	t.Run("after running", func(t *testing.T) {
		settleReturns(t, context.Background(), make(chan struct{}, 1))
	})
	t.Run("cancelled while waiting for a slot", func(t *testing.T) {
		sem := make(chan struct{}, 1)
		sem <- struct{}{} // every slot taken
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		settleReturns(t, ctx, sem)
	})
	t.Run("a live turn still receives it", func(t *testing.T) {
		out := make(chan settlement, 1)
		runner.settleTool(context.Background(), make(chan struct{}, 1), make(chan struct{}), req, out)
		select {
		case settled := <-out:
			if settled.err != nil || settled.output != "x" {
				t.Fatalf("unexpected settlement: %+v", settled)
			}
		default:
			t.Fatal("a live turn must receive the settlement")
		}
	})
}
