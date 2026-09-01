package session

import (
	"context"
	"fmt"
	"strings"

	"github.com/anomalyco/opencode-go/internal/agent"
	"github.com/anomalyco/opencode-go/internal/tool"
)

// DefaultSubagentDepth matches the TypeScript default (config subagent_depth,
// packages/opencode/src/tool/task.ts): a primary session may spawn subagents,
// but those subagents may not spawn their own.
const DefaultSubagentDepth = 1

// Spawner runs subagents as independent sessions. It implements tool.Spawner,
// the seam that lets internal/tool/builtins start a session without importing
// this package.
//
// Concurrency comes for free from the execution coordinator: children are
// keyed by their own session IDs, and Coordinator serializes per key while
// running different keys concurrently. A child therefore runs on its own
// goroutine alongside its parent and its siblings, with no extra machinery.
type Spawner struct {
	Service   *Service
	Execution *Execution
	Agents    *agent.Registry
	// Depth caps how deep the parent chain may go. Zero means
	// DefaultSubagentDepth.
	Depth int
}

func NewSpawner(service *Service, execution *Execution, agents *agent.Registry, depth int) *Spawner {
	return &Spawner{Service: service, Execution: execution, Agents: agents, Depth: depth}
}

func (s *Spawner) depth() int {
	if s.Depth > 0 {
		return s.Depth
	}
	return DefaultSubagentDepth
}

// Agent reports whether an agent exists and whether it may run as a subagent.
func (s *Spawner) Agent(id string) (bool, bool) {
	info, ok := s.Agents.Get(id)
	if !ok {
		return false, false
	}
	return true, info.Mode == "subagent" || info.Mode == "all"
}

// Spawn starts a subagent session and returns a channel carrying its single
// result. The returned channel is closed after the result is delivered.
func (s *Spawner) Spawn(ctx context.Context, req tool.SpawnRequest) (string, <-chan tool.SpawnResult, error) {
	info, ok := s.Agents.Get(req.AgentID)
	if !ok {
		return "", nil, fmt.Errorf("Unknown agent type: %s is not a valid agent type", req.AgentID)
	}
	if info.Mode == "primary" {
		return "", nil, fmt.Errorf("Agent %s is a primary agent and cannot run as a subagent", req.AgentID)
	}

	parents, err := s.Service.Parents(ctx, req.ParentSessionID)
	if err != nil {
		return "", nil, err
	}
	if len(parents) >= s.depth() {
		return "", nil, fmt.Errorf(
			"Subagent depth limit reached (%d). Increase \"subagent_depth\" to allow nested subagents.",
			s.depth())
	}

	childID := req.ResumeSessionID
	if childID != "" {
		existing, err := s.Service.Get(ctx, childID)
		if err != nil {
			return "", nil, err
		}
		if existing == nil {
			return "", nil, fmt.Errorf("Session not found: %s", childID)
		}
	} else {
		parent, err := s.Service.Get(ctx, req.ParentSessionID)
		if err != nil {
			return "", nil, err
		}
		if parent == nil {
			return "", nil, fmt.Errorf("Session not found: %s", req.ParentSessionID)
		}
		parentRules, err := s.Service.Permission(ctx, req.ParentSessionID)
		if err != nil {
			return "", nil, err
		}
		child, err := s.Service.Create(ctx, CreateInput{
			Directory:  parent.Directory,
			Title:      req.Description + " (@" + info.ID + " subagent)",
			ParentID:   req.ParentSessionID,
			Agent:      info.ID,
			Permission: DeriveSubagentPermissions(parentRules, info),
		})
		if err != nil {
			return "", nil, err
		}
		childID = child.ID
	}

	if _, err := s.Service.Prompt(ctx, childID, req.Prompt, DeliveryQueue); err != nil {
		return "", nil, err
	}

	done := make(chan tool.SpawnResult, 1)
	go func() {
		defer close(done)
		// Detach from the caller's context: the child's lifetime is bounded by
		// its own coordinator entry and by Cancel, not by the parent tool
		// call's context, which is also cancelled on a normal parent turn end.
		runErr := s.Execution.Resume(context.WithoutCancel(ctx), childID)
		text, textErr := s.lastAssistantText(context.WithoutCancel(ctx), childID)
		if runErr == nil {
			runErr = textErr
		}
		done <- tool.SpawnResult{SessionID: childID, Text: text, Err: runErr}
	}()
	return childID, done, nil
}

// Cancel interrupts a running child session.
func (s *Spawner) Cancel(childID string) {
	s.Execution.Interrupt(childID)
}

// Notify admits a synthetic prompt into the parent session carrying a detached
// subagent's result. DeliveryQueue rather than DeliverySteer: the result lands
// at the parent's next idle boundary instead of interrupting a turn in
// progress.
func (s *Spawner) Notify(ctx context.Context, parentSessionID, text string) error {
	_, err := s.Service.Prompt(context.WithoutCancel(ctx), parentSessionID, text, DeliveryQueue)
	return err
}

// lastAssistantText returns the child's final assistant text — the single
// message a subagent reports back. A tool error in the child surfaces as an
// error so the parent sees the failure rather than an empty result.
func (s *Spawner) lastAssistantText(ctx context.Context, sessionID string) (string, error) {
	messages, err := s.Service.Messages.List(ctx, sessionID)
	if err != nil {
		return "", err
	}
	var text string
	var failure string
	for _, message := range messages {
		if message.Type != TypeAssistant {
			continue
		}
		assistant, err := DecodeAssistant(message.Data)
		if err != nil {
			return "", err
		}
		if assistant.Error != nil && assistant.Error.Message != "" {
			failure = assistant.Error.Message
		}
		for _, part := range assistant.Content {
			switch part.Type {
			case "text":
				if strings.TrimSpace(part.Text) != "" {
					text = part.Text
				}
			case "tool":
				if part.State != nil && part.State.Status == ToolError && part.State.Error != "" {
					failure = part.State.Error
				}
			}
		}
	}
	if text == "" && failure != "" {
		return "", fmt.Errorf("Subagent failed (task_id: %s): %s", sessionID, failure)
	}
	return text, nil
}
