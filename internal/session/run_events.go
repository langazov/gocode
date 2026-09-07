package session

import (
	"context"

	"github.com/langazov/gocode-go/internal/event"
)

// Events announcing the edges of a *turn* — the whole unit of work a prompt
// sets off, model step plus tools plus the next model step plus however many
// more it takes, up to the point the session goes idle again.
//
// This is the port of packages/core's SessionStatus busy/idle pair, which is
// likewise published from the run coordinator (run-state.ts's onBusy/onIdle)
// and not from the step loop. The distinction is the whole reason these
// exist. A reader that infers "running" from the step events instead sees
// step.ended and step.started bracket every *step*, so it reports idle in the
// gap where the model is deciding what to do next and where tools are
// executing — which is most of a turn. In the TUI that showed up as the
// footer spinner disappearing partway through a task and coming back a few
// seconds later.
//
// Live-only. The durable step events already record what happened for anyone
// reading a session back; these say what is happening *now*, and replaying a
// "busy" from a previous process would be a lie. The HTTP endpoints stay the
// source of truth for a client that missed one.
var (
	RunStarted = event.Definition{Type: "session.next.run.started"}
	RunEnded   = event.Definition{Type: "session.next.run.ended"}
)

// PublishRunStatus is the Execution.OnStatus callback that puts those edges on
// the bus. Detached from the caller's context: the idle edge in particular
// fires as a turn unwinds, often on a context that is already cancelled, and
// dropping it there is exactly the case that leaves a spinner running forever.
func PublishRunStatus(ctx context.Context, bus *event.Bus) func(sessionID string, busy bool) {
	return func(sessionID string, busy bool) {
		if bus == nil || sessionID == "" {
			return
		}
		def := RunEnded
		if busy {
			def = RunStarted
		}
		_, _ = bus.Publish(context.WithoutCancel(ctx), def, map[string]any{
			"sessionID": sessionID,
		}, event.PublishOptions{})
	}
}

// RunFailed announces that a turn ended because the drain failed, rather than
// because the work finished. It exists so the failure is not invisible: the
// drain error used to go to the background log alone, and what the user saw
// was a turn that simply stopped — no message, no error, no spinner. See
// cmd/gocode/main.go's logDrainError for why it cannot be written to the
// terminal directly.
//
// Live-only, like the pair above: it describes what just happened to this
// process's run, not something a session replay should ever re-announce.
var RunFailed = event.Definition{Type: "session.next.run.failed"}

// PublishRunFailure puts a drain failure on the bus. It is the reporting half
// of Execution.ErrorLogger; the caller still decides which errors are worth
// reporting (a cancelled turn is the user's own interrupt, not a failure).
func PublishRunFailure(ctx context.Context, bus *event.Bus) func(sessionID string, err error) {
	return func(sessionID string, err error) {
		if bus == nil || sessionID == "" || err == nil {
			return
		}
		_, _ = bus.Publish(context.WithoutCancel(ctx), RunFailed, map[string]any{
			"sessionID": sessionID,
			"error":     err.Error(),
		}, event.PublishOptions{})
	}
}
