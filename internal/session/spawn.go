package session

import (
	"context"
	"fmt"
	"strings"

	"github.com/langazov/gocode-go/internal/agent"
	"github.com/langazov/gocode-go/internal/permission"
	"github.com/langazov/gocode-go/internal/tool"
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
//
// # Channel topology (the ports of packages/opencode/src/tool/task.ts)
//
//	parent turn goroutine                child drain goroutine
//	┌────────────────────────┐           ┌──────────────────────────┐
//	│ runner.runTurnAttempt  │           │ Coordinator.Run(childID) │
//	│   └─ settleTool        │ Spawn ──▶ │   └─ runner.Run(child)   │
//	│       └─ task.Execute  │           │        ...turn...        │
//	└───────────┬────────────┘           └───────────┬──────────────┘
//	            │      done <-chan SpawnResult       │
//	            └────────────────◀───────────────────┘
//
// Foreground: task.Execute blocks its worker goroutine on `done`; the runner
// dispatches every tool call on its own goroutine (bounded by
// DefaultToolConcurrency), so N concurrent task calls produce N concurrent
// children whose results fan back over their own channels in any order.
//
// Background (task.go's runAsJob): the same `done` is consumed by the
// background.Registry instead, and the result reaches the parent as a
// synthetic queued prompt through Notify — the parent's turn never blocked.
//
// Cancellation travels downward only: interrupting the parent cancels its run
// context, which the task tool observes and forwards as Cancel(childID) →
// Execution.Interrupt. A child's own failure travels upward as SpawnResult.Err
// and becomes the tool call's error.
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

// parentRules is the ruleset the parent session actually runs under, which is
// what DeriveSubagentPermissions takes its floor from.
//
// The precedence mirrors AgentRulesProvider.Configured, and it has to: a
// top-level session has no stored ruleset of its own — its restrictions live
// on its agent. Reading only the stored column, as this used to, meant a
// plan-mode parent handed its children an empty floor, and every restriction
// plan mode exists to impose stopped at the first `task` call.
func (s *Spawner) parentRules(ctx context.Context, parent Info) (permission.Ruleset, error) {
	stored, err := s.Service.Permission(ctx, parent.ID)
	if err != nil {
		return nil, err
	}
	if len(stored) > 0 {
		return stored, nil
	}
	if s.Agents == nil {
		return nil, nil
	}
	if info, ok := s.Agents.Get(parent.Agent); ok {
		return info.Permissions, nil
	}
	return nil, nil
}

// Agent reports whether an agent exists and whether it may run as a subagent.
func (s *Spawner) Agent(id string) (bool, bool) {
	info, ok := s.Agents.Get(id)
	if !ok {
		return false, false
	}
	return true, info.Mode == "subagent" || info.Mode == "all"
}

// parseRequestedModel parses the task tool's model argument, "provider/model",
// into a reference. Empty means no override; malformed is an error the caller
// surfaces to the model so it can retry with a valid ID.
func parseRequestedModel(raw string) (*ModelRef, error) {
	if raw == "" {
		return nil, nil
	}
	providerID, modelID, ok := strings.Cut(raw, "/")
	if !ok || providerID == "" || modelID == "" {
		return nil, fmt.Errorf(
			"Invalid model %q: expected the format provider/model (e.g. anthropic/claude-sonnet-5)", raw)
	}
	return &ModelRef{ProviderID: providerID, ID: modelID}, nil
}

// resolveChildModel computes the model pinned on a freshly created subagent
// session, or nil to leave the child's model column empty and let the
// runner's own precedence (resolveAgent) resolve it.
//
// The order mirrors upstream task.ts (`next.model ?? parent's current model`)
// plus the per-call override:
//
//  1. req.Model — an explicit model argument on the task call wins over
//     everything: a subagent can start on any model, passed as parameter.
//     It carries no variant: naming a model means that model with its
//     default options.
//  2. The subagent's own configured model (info.Model): leave the column
//     empty — resolveAgent applies the agent's model on every turn, and a
//     pinned session model would override it (upstream issue #35126:
//     inheriting must not beat the subagent's own config).
//  3. The parent session's current model: its pinned model column first —
//     which is what a mid-session model switch writes — then the parent
//     agent's configured model, which is the effective model of a parent
//     that never pinned one. This is the inheritance the port was missing:
//     without it a child resolved to the boot default and ignored the
//     parent's switch entirely. The variant travels with an inherited
//     model (upstream issue #24757: inheriting only the IDs drops the
//     reasoning variant a switched parent runs).
//  4. Neither resolves → leave the column empty; the child falls through
//     its own agent (step 2 already found none) to the runner default,
//     which is the model the parent itself runs.
//
// parent.Agent may be empty (a session created without one); Select resolves
// that to the default agent exactly like the runner does for a turn, so the
// parent-agent lookup here matches what the parent actually runs.
func (s *Spawner) resolveChildModel(req tool.SpawnRequest, parent Info, info agent.Info) (*ModelRef, error) {
	if req.Model != "" {
		return parseRequestedModel(req.Model)
	}
	if info.Model != nil {
		return nil, nil
	}
	if parent.Model != nil {
		ref := *parent.Model
		return &ref, nil
	}
	if s.Agents != nil {
		if selection := s.Agents.Select(parent.Agent); selection.Info != nil && selection.Info.Model != nil {
			return &ModelRef{
				ProviderID: selection.Info.Model.ProviderID,
				ID:         selection.Info.Model.ID,
				Variant:    selection.Info.Model.Variant,
			}, nil
		}
	}
	return nil, nil
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
			return "", nil, notFound(childID)
		}
		// An explicit model argument follows a resumed child across turns;
		// a resume without one keeps whatever model the child already runs.
		// Inheritance is a fresh-spawn concern: re-resolving it here would
		// stomp a child's own pinned model (or its agent's) with the
		// parent's later switch every time the parent resumed it.
		if req.Model != "" {
			pinned, err := parseRequestedModel(req.Model)
			if err != nil {
				return "", nil, err
			}
			if pinned != nil {
				if err := s.Service.SetModel(ctx, childID, *pinned); err != nil {
					return "", nil, err
				}
			}
		}
	} else {
		parent, err := s.Service.Get(ctx, req.ParentSessionID)
		if err != nil {
			return "", nil, err
		}
		if parent == nil {
			return "", nil, fmt.Errorf("Session not found: %s", req.ParentSessionID)
		}
		parentRules, err := s.parentRules(ctx, *parent)
		if err != nil {
			return "", nil, err
		}
		model, err := s.resolveChildModel(req, *parent, info)
		if err != nil {
			return "", nil, err
		}
		child, err := s.Service.Create(ctx, CreateInput{
			Directory:  parent.Directory,
			Title:      req.Description + " (@" + info.ID + " subagent)",
			ParentID:   req.ParentSessionID,
			Agent:      info.ID,
			Permission: DeriveSubagentPermissions(parentRules, info),
			Model:      model,
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
