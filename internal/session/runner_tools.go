package session

import (
	"context"
	"time"

	"github.com/anomalyco/opencode-go/internal/event"
	"github.com/anomalyco/opencode-go/internal/llm"
)

// DefaultToolConcurrency bounds how many tool calls one turn runs at once.
//
// The TypeScript runtime's FiberSet is unbounded, which is safe on a single
// JS thread. Here each dispatch is a real goroutine that may fork a process,
// so the fan-out is capped.
const DefaultToolConcurrency = 8

// settlement is one finished tool call, handed from a worker goroutine back to
// the turn goroutine. It is the only thing workers ever produce: they never
// publish events or touch the database themselves.
type settlement struct {
	call    llm.ToolCall
	seq     int // stream order of the originating tool call
	output  string
	err     error
	started int64
	ended   int64
}

// toolRequest is the immutable context a worker needs to run one call.
type toolRequest struct {
	call               llm.ToolCall
	seq                int
	sessionID          string
	assistantMessageID string
	agentID            string
}

func (r *Runner) toolConcurrency() int {
	if r.ToolConcurrency > 0 {
		return r.ToolConcurrency
	}
	return DefaultToolConcurrency
}

// settleTool runs exactly one tool call on its own goroutine and reports the
// outcome over out. It always sends exactly one settlement, including on
// cancellation, so the turn loop's inflight count always drains.
func (r *Runner) settleTool(ctx context.Context, sem chan struct{}, req toolRequest, out chan<- settlement) {
	started := nowMillis()
	// Wait for a slot. A cancelled turn settles immediately rather than
	// queueing behind tools that will themselves be cancelled.
	select {
	case sem <- struct{}{}:
	case <-ctx.Done():
		out <- settlement{call: req.call, seq: req.seq, err: ctx.Err(), started: started, ended: nowMillis()}
		return
	}
	defer func() { <-sem }()

	output, err := r.executeTool(ctx, req.sessionID, req.assistantMessageID, req.agentID, req.call)
	out <- settlement{
		call:    req.call,
		seq:     req.seq,
		output:  output,
		err:     err,
		started: started,
		ended:   nowMillis(),
	}
}

// publishSettlement writes one settled tool call to the bus. Called only from
// the turn goroutine, so publishes stay serialized and ordered.
func (r *Runner) publishSettlement(ctx context.Context, sessionID, assistantMessageID string, settled settlement) error {
	provider := map[string]any{"executed": settled.call.ProviderExecuted}
	if settled.err != nil {
		_, err := r.Bus.Publish(ctx, ToolFailed, map[string]any{
			"sessionID":          sessionID,
			"timestamp":          nowMillis(),
			"assistantMessageID": assistantMessageID,
			"callID":             settled.call.ID,
			"error":              map[string]any{"type": "unknown", "message": settled.err.Error()},
			"provider":           provider,
		}, event.PublishOptions{})
		return err
	}
	_, err := r.Bus.Publish(ctx, ToolSuccess, map[string]any{
		"sessionID":          sessionID,
		"timestamp":          nowMillis(),
		"assistantMessageID": assistantMessageID,
		"callID":             settled.call.ID,
		"output":             settled.output,
		"provider":           provider,
	}, event.PublishOptions{})
	return err
}

// drainDeadline bounds how long an interrupted turn waits for in-flight tools
// to report back before it gives up and lets failInterruptedTools settle the
// leftovers on the next drain. Mirrors the TypeScript processor's 250ms grace
// (packages/opencode/src/session/processor.ts).
const drainDeadline = 250 * time.Millisecond
