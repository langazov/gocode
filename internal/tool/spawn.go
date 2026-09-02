package tool

import "context"

// SpawnRequest asks for one subagent run.
type SpawnRequest struct {
	// ParentSessionID is the session issuing the task call.
	ParentSessionID string
	// AgentID names the subagent to run (task's subagent_type).
	AgentID string
	// Description is the short human label shown in the timeline.
	Description string
	// Prompt is the work handed to the subagent.
	Prompt string
	// ResumeSessionID continues an earlier subagent session (task's task_id)
	// instead of creating a fresh one.
	ResumeSessionID string
}

// SpawnResult is the single outcome of a subagent run.
type SpawnResult struct {
	SessionID string
	// Text is the subagent's final assistant text — its one message back.
	Text string
	Err  error
}

// Spawner starts a child agent session. It is the seam between the tool layer
// and the session layer: internal/session imports internal/tool, so a task
// tool living under internal/tool/builtins cannot import the session package
// directly. The concrete implementation is session.Spawner.
//
// Spawn returns immediately with the child's session ID and a channel that
// yields exactly one result and is then closed. The child runs on its own
// goroutine, keyed by its own session ID in the execution coordinator, so it
// is concurrent with the parent and with its siblings.
type Spawner interface {
	Spawn(ctx context.Context, req SpawnRequest) (childID string, done <-chan SpawnResult, err error)
	// Cancel interrupts a running child.
	Cancel(childID string)
	// Agent reports whether an agent exists and may be used as a subagent.
	Agent(id string) (found bool, isSubagent bool)
	// Notify delivers a detached subagent's result back to its parent session
	// as a synthetic prompt, so the parent picks it up at its next idle
	// boundary. This is how a background task reports in without its tool
	// call ever having blocked.
	Notify(ctx context.Context, parentSessionID, text string) error
}
