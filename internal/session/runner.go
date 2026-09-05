package session

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/langazov/gocode-go/internal/agent"
	"github.com/langazov/gocode-go/internal/db"
	"github.com/langazov/gocode-go/internal/event"
	"github.com/langazov/gocode-go/internal/id"
	"github.com/langazov/gocode-go/internal/llm"
	"github.com/langazov/gocode-go/internal/plugin"
	"github.com/langazov/gocode-go/internal/tool"
)

// MaxStepsPrompt matches packages/core/src/session/runner/max-steps.ts.
const MaxStepsPrompt = `CRITICAL - MAXIMUM STEPS REACHED

The maximum number of steps allowed for this task has been reached. Tools are disabled until next user input. Respond with text only.

STRICT REQUIREMENTS:
1. Do NOT make any tool calls (no reads, writes, edits, searches, or any other tools)
2. MUST provide a text response summarizing work done so far
3. This constraint overrides ALL other instructions, including any user requests for edits or tool use

Response must include:
- Statement that maximum steps for this agent have been reached
- Summary of what has been accomplished so far
- List of any remaining tasks that were not completed
- Recommendations for what should be done next

Any attempt to use tools is a critical violation. Respond with text ONLY.`

// Runner drains eligible durable work for a session, porting the core loop of
// session/runner/llm.ts: promote inbox rows, translate projected history,
// stream exactly one provider turn per attempt, settle local tool calls
// durably, and continue while work remains.
type Runner struct {
	DB       *db.DB
	Bus      *event.Bus
	Messages *MessageStore
	Provider llm.StreamClient
	Tools    *tool.Registry

	Agent    string
	System   string
	Model    ModelRef
	MaxSteps int

	// Agents, when set, drives agent/model/system-prompt resolution from the
	// session's agent column, overriding the static fields above.
	Agents *agent.Registry

	// Permissions, when set, gates every local tool execution.
	Permissions PermissionGate

	// Plugins, when set, is consulted at the hook seams in
	// runner_plugins.go: tool advertisement, request assembly, tool
	// execution, and permission asks. Nil disables every hook.
	Plugins *plugin.Host

	// ContextLimit bounds the model context window for compaction budgeting.
	ContextLimit int

	// ToolConcurrency caps how many tool calls one turn settles at once.
	// Zero means DefaultToolConcurrency. One restores the pre-concurrency
	// sequential behavior.
	ToolConcurrency int

	// Compactor, when set, summarizes history as the context limit approaches.
	Compactor *Compactor

	// ReasoningVariants resolves a model's selected reasoning/extended-thinking
	// variant into provider-specific request options (see
	// internal/provider.ReasoningVariants), given the resolved provider,
	// model, and variant IDs for a turn. Only consulted when the turn's
	// model has a non-empty Variant (matching the original CLI's opt-in
	// --variant behavior — no variant selected means no reasoning
	// requested). nil disables reasoning variant support entirely.
	ReasoningVariants func(providerID, modelID, variantID string) map[string]any

	// Pricing resolves a model's per-million-token rates so a settled step can
	// be costed. Injected from the boot wiring, like ReasoningVariants above,
	// to keep the catalog out of this package. When nil (or when the catalog
	// has no entry) a step costs nothing rather than a guessed amount.
	Pricing PricingResolver
}

// resolvedAgent carries the effective per-turn agent configuration.
type resolvedAgent struct {
	ID       string
	System   string
	Model    ModelRef
	MaxSteps int
}

// PermissionGate authorizes a tool call before side effects begin.
type PermissionGate interface {
	Assert(ctx context.Context, input ToolPermissionInput) error
}

// PermissionRuled is the optional half of PermissionGate that answers "is this
// refused outright?" without waiting on the user. A gate that implements it
// gets its denies enforced ahead of anything that could settle an ask —
// currently the plugin permission hook.
type PermissionRuled interface {
	Denied(input ToolPermissionInput) error
}

type ToolPermissionInput struct {
	SessionID string
	Agent     string
	Action    string
	Resources []string
	// Save is the rule an "allow always" reply persists. Empty means the
	// approval cannot be remembered, so the same request asks again next
	// time — which is why every ask here sets it.
	Save               []string
	AssistantMessageID string
	CallID             string
}

type turnResult struct {
	needsContinuation bool
	step              int
}

// Run mirrors SessionRunner.run: steer promotes during the drain, queue waits
// for the idle boundary, and explicit runs force one provider attempt.
func (r *Runner) Run(ctx context.Context, input RunInput) error {
	hasSteer, err := HasPending(ctx, r.DB, input.SessionID, DeliverySteer)
	if err != nil {
		return err
	}
	hasQueue := false
	if !hasSteer {
		hasQueue, err = HasPending(ctx, r.DB, input.SessionID, DeliveryQueue)
		if err != nil {
			return err
		}
	}
	if !input.Force && !hasSteer && !hasQueue {
		return nil
	}
	if err := r.failInterruptedTools(ctx, input.SessionID); err != nil {
		return err
	}
	var promotion Delivery
	if hasSteer {
		promotion = DeliverySteer
	} else if hasQueue {
		promotion = DeliveryQueue
	}
	shouldRun := input.Force || hasSteer || hasQueue
	for shouldRun {
		needsContinuation := true
		step := 1
		for needsContinuation {
			result, err := r.runTurn(ctx, input.SessionID, promotion, step)
			if err != nil {
				return err
			}
			needsContinuation = result.needsContinuation
			step = result.step + 1
			promotion = DeliverySteer
			if !needsContinuation {
				needsContinuation, err = HasPending(ctx, r.DB, input.SessionID, DeliverySteer)
				if err != nil {
					return err
				}
			}
		}
		shouldRun, err = HasPending(ctx, r.DB, input.SessionID, DeliveryQueue)
		if err != nil {
			return err
		}
		if shouldRun {
			promotion = DeliveryQueue
		} else {
			promotion = ""
		}
	}
	return nil
}

// runTurn runs one provider turn, recovering from a context-overflow failure
// by compacting and retrying once.
func (r *Runner) runTurn(runCtx context.Context, sessionID string, promotion Delivery, step int) (turnResult, error) {
	result, err := r.runTurnAttempt(runCtx, sessionID, promotion, step)
	if !errors.Is(err, errContextOverflow) || r.Compactor == nil {
		return result, err
	}
	ctx := context.WithoutCancel(runCtx)
	resolved, resolveErr := r.resolveAgent(ctx, sessionID)
	if resolveErr != nil {
		return turnResult{}, resolveErr
	}
	history, historyErr := r.Messages.ListForRunner(ctx, sessionID)
	if historyErr != nil {
		return turnResult{}, historyErr
	}
	compacted, compactErr := r.Compactor.Compact(ctx, sessionID, history, resolved.Model)
	if compactErr != nil || !compacted {
		return turnResult{}, err
	}
	return r.runTurnAttempt(runCtx, sessionID, promotion, step)
}

func (r *Runner) runTurnAttempt(runCtx context.Context, sessionID string, promotion Delivery, step int) (turnResult, error) {
	// Publishes and DB writes settle durably and must survive interruption of
	// the provider stream; only the stream itself observes cancellation.
	ctx := context.WithoutCancel(runCtx)
	currentStep := step
	if promotion != "" {
		cutoff, err := r.Bus.LatestSequence(ctx, sessionID)
		if err != nil {
			return turnResult{}, err
		}
		promoted := 0
		if promotion == DeliverySteer {
			promoted, err = PromoteSteers(ctx, r.Bus, r.DB, sessionID, cutoff)
			if err != nil {
				return turnResult{}, err
			}
		}
		if promotion == DeliveryQueue {
			queued, err := PromoteNextQueued(ctx, r.Bus, r.DB, sessionID)
			if err != nil {
				return turnResult{}, err
			}
			if queued {
				promoted++
			}
			steers, err := PromoteSteers(ctx, r.Bus, r.DB, sessionID, cutoff)
			if err != nil {
				return turnResult{}, err
			}
			promoted += steers
		}
		if promoted > 0 {
			currentStep = 1
		}
	}

	resolved, err := r.resolveAgent(ctx, sessionID)
	if err != nil {
		return turnResult{}, err
	}
	if err := r.compactIfNeeded(ctx, sessionID, resolved.Model); err != nil {
		return turnResult{}, err
	}
	messages, err := r.Messages.ListForRunner(ctx, sessionID)
	if err != nil {
		return turnResult{}, err
	}
	llmMessages, err := ToLLMMessages(messages, resolved.Model)
	if err != nil {
		return turnResult{}, err
	}
	// Plan mode's read-only charter (and its release on the way back to build)
	// rides on the newest user message. See reminders.go.
	llmMessages = applyReminders(llmMessages, messages, resolved.ID)
	isLastStep := resolved.MaxSteps > 0 && currentStep >= resolved.MaxSteps
	var tools []llm.ToolDefinition
	if r.Tools != nil && !isLastStep {
		for _, name := range r.Tools.Names() {
			registered, _ := r.Tools.Get(name)
			tools = append(tools, llm.ToolDefinition{
				Name:        registered.Name(),
				Description: registered.Description(),
				InputSchema: registered.InputSchema(),
			})
		}
	}
	r.applyToolDefinitions(ctx, tools)
	if isLastStep {
		llmMessages = append(llmMessages, llm.AssistantText("", MaxStepsPrompt))
	}
	system := []string{}
	if resolved.System != "" {
		system = append(system, resolved.System)
	}
	system = r.applySystemTransform(ctx, sessionID, resolved, system)
	request := llm.Request{
		ProviderID: resolved.Model.ProviderID,
		ModelID:    resolved.Model.ID,
		System:     system,
		Messages:   llmMessages,
		Tools:      tools,
		MaxTokens:  8192,
	}
	if isLastStep {
		request.ToolChoice = "none"
	}
	if r.ReasoningVariants != nil && resolved.Model.Variant != "" {
		request.Reasoning = r.ReasoningVariants(resolved.Model.ProviderID, resolved.Model.ID, resolved.Model.Variant)
	}
	// Last, so a plugin sees — and can override — every value the runner and
	// the reasoning variant resolved.
	r.applyChatParams(ctx, sessionID, resolved, &request)

	var assistantMessageID string
	var text strings.Builder
	var reasoning strings.Builder
	var textID string
	var reasoningID string
	var finish string
	var usage llm.Usage
	var providerErr error

	startAssistant := func() error {
		if assistantMessageID != "" {
			return nil
		}
		generated, err := id.Ascending(id.KindMessage)
		if err != nil {
			return err
		}
		assistantMessageID = generated
		_, err = r.Bus.Publish(ctx, StepStarted, map[string]any{
			"sessionID":          sessionID,
			"timestamp":          nowMillis(),
			"assistantMessageID": assistantMessageID,
			"agent":              resolved.ID,
			"model":              map[string]any{"providerID": resolved.Model.ProviderID, "id": resolved.Model.ID},
		}, event.PublishOptions{})
		return err
	}

	startText := func() error {
		if textID != "" {
			return nil
		}
		textID = assistantMessageID + "-text"
		_, err := r.Bus.Publish(ctx, TextStarted, map[string]any{
			"sessionID":          sessionID,
			"timestamp":          nowMillis(),
			"assistantMessageID": assistantMessageID,
			"textID":             textID,
		}, event.PublishOptions{})
		return err
	}

	startReasoning := func() error {
		if reasoningID != "" {
			return nil
		}
		reasoningID = assistantMessageID + "-reasoning"
		_, err := r.Bus.Publish(ctx, ReasoningStarted, map[string]any{
			"sessionID":          sessionID,
			"timestamp":          nowMillis(),
			"assistantMessageID": assistantMessageID,
			"reasoningID":        reasoningID,
		}, event.PublishOptions{})
		return err
	}

	// The loop below is the sole publisher for this turn. The provider stream
	// runs in its own goroutine and forwards events inward; every tool runs in
	// its own goroutine and reports a settlement inward. Nothing else touches
	// the bus, so event ordering stays deterministic without a lock.
	// See MULTI_AGENTS.md phase 1.
	events := make(chan llm.StreamEvent, 64)
	settlements := make(chan settlement, 16)
	sem := make(chan struct{}, r.toolConcurrency())
	var streamErr error
	go func() {
		defer close(events)
		streamErr = r.Provider.Stream(runCtx, request, func(streamEvent llm.StreamEvent) {
			select {
			case events <- streamEvent:
			case <-runCtx.Done():
			}
		})
	}()

	inflight := 0
	seq := 0
	needsContinuation := false
	// The turn is over once the stream has closed AND every dispatched tool
	// has reported back — the Go analogue of FiberSet.awaitEmpty.
	for events != nil || inflight > 0 {
		select {
		case streamEvent, ok := <-events:
			if !ok {
				events = nil
				continue
			}
			if providerErr != nil {
				continue
			}
			switch streamEvent.Type {
			case llm.EventTextDelta:
				if err := startAssistant(); err != nil {
					providerErr = err
					continue
				}
				if err := startText(); err != nil {
					providerErr = err
					continue
				}
				text.WriteString(streamEvent.Text)
				if _, err := r.Bus.Publish(ctx, TextDelta, map[string]any{
					"sessionID":          sessionID,
					"timestamp":          nowMillis(),
					"assistantMessageID": assistantMessageID,
					"textID":             textID,
					"delta":              streamEvent.Text,
				}, event.PublishOptions{}); err != nil {
					providerErr = err
					continue
				}
			case llm.EventReasoningDelta:
				if err := startAssistant(); err != nil {
					providerErr = err
					continue
				}
				if err := startReasoning(); err != nil {
					providerErr = err
					continue
				}
				reasoning.WriteString(streamEvent.Text)
				if _, err := r.Bus.Publish(ctx, ReasoningDelta, map[string]any{
					"sessionID":          sessionID,
					"timestamp":          nowMillis(),
					"assistantMessageID": assistantMessageID,
					"reasoningID":        reasoningID,
					"delta":              streamEvent.Text,
				}, event.PublishOptions{}); err != nil {
					providerErr = err
					continue
				}
			case llm.EventToolCall:
				if err := startAssistant(); err != nil {
					providerErr = err
					continue
				}
				call := *streamEvent.ToolCall
				// Published in stream order. projectToolCalled appends to the
				// assistant message content, so this ordering is what fixes
				// the timeline layout; settlements may land in any order.
				if _, err := r.Bus.Publish(ctx, ToolCalled, map[string]any{
					"sessionID":          sessionID,
					"timestamp":          nowMillis(),
					"assistantMessageID": assistantMessageID,
					"callID":             call.ID,
					"tool":               call.Name,
					"input":              call.Input,
					"provider":           map[string]any{"executed": call.ProviderExecuted},
				}, event.PublishOptions{}); err != nil {
					providerErr = err
					continue
				}
				needsContinuation = true
				if call.ProviderExecuted {
					// The provider already ran it; settle from its output
					// rather than dispatching locally.
					if err := r.publishSettlement(ctx, sessionID, assistantMessageID, settlement{
						call:   call,
						seq:    seq,
						output: call.Output,
					}); err != nil {
						providerErr = err
					}
					seq++
					continue
				}
				// Dispatch mid-stream: the tool starts now, while the rest of
				// the response is still arriving.
				inflight++
				go r.settleTool(runCtx, sem, toolRequest{
					call:               call,
					seq:                seq,
					sessionID:          sessionID,
					assistantMessageID: assistantMessageID,
					agentID:            resolved.ID,
				}, settlements)
				seq++
			case llm.EventFinish:
				finish = streamEvent.Finish
				usage = streamEvent.Usage
			case llm.EventProviderError:
				providerErr = streamEvent.Error
			}
		case settled := <-settlements:
			inflight--
			if err := r.publishSettlement(ctx, sessionID, assistantMessageID, settled); err != nil && providerErr == nil {
				providerErr = err
			}
		}
	}
	if streamErr != nil && providerErr == nil {
		providerErr = streamErr
	}

	// An overflow before any assistant content is recoverable: signal the
	// wrapper to compact and retry instead of settling a failed step.
	if providerErr != nil && assistantMessageID == "" && isContextOverflow(providerErr) {
		return turnResult{}, errContextOverflow
	}

	if providerErr != nil {
		if err := startAssistant(); err != nil {
			return turnResult{}, err
		}
		if _, err := r.Bus.Publish(ctx, StepFailed, map[string]any{
			"sessionID":          sessionID,
			"timestamp":          nowMillis(),
			"assistantMessageID": assistantMessageID,
			"error":              stepError(providerErr),
		}, event.PublishOptions{}); err != nil {
			return turnResult{}, err
		}
		return turnResult{}, providerErr
	}

	if err := startAssistant(); err != nil {
		return turnResult{}, err
	}
	if reasoning.Len() > 0 {
		if _, err := r.Bus.Publish(ctx, ReasoningEnded, map[string]any{
			"sessionID":          sessionID,
			"timestamp":          nowMillis(),
			"assistantMessageID": assistantMessageID,
			"reasoningID":        reasoningID,
			"text":               reasoning.String(),
		}, event.PublishOptions{}); err != nil {
			return turnResult{}, err
		}
	}
	if text.Len() > 0 {
		if _, err := r.Bus.Publish(ctx, TextEnded, map[string]any{
			"sessionID":          sessionID,
			"timestamp":          nowMillis(),
			"assistantMessageID": assistantMessageID,
			"textID":             textID,
			"text":               text.String(),
		}, event.PublishOptions{}); err != nil {
			return turnResult{}, err
		}
	}
	if _, err := r.Bus.Publish(ctx, StepEnded, map[string]any{
		"sessionID":          sessionID,
		"timestamp":          nowMillis(),
		"assistantMessageID": assistantMessageID,
		"finish":             finish,
		"cost": r.stepCost(resolved.Model.ProviderID, resolved.Model.ID, TokenUsage{
			Input:      usage.Input,
			Output:     usage.Output,
			Reasoning:  usage.Reasoning,
			CacheRead:  usage.CacheRead,
			CacheWrite: usage.CacheWrite,
		}),
		"tokens": map[string]any{
			"input":     usage.Input,
			"output":    usage.Output,
			"reasoning": usage.Reasoning,
			"cache":     map[string]any{"read": usage.CacheRead, "write": usage.CacheWrite},
		},
	}, event.PublishOptions{}); err != nil {
		return turnResult{}, err
	}

	return turnResult{needsContinuation: needsContinuation, step: currentStep}, nil
}

func (r *Runner) executeTool(ctx context.Context, sessionID, assistantMessageID, agentID string, call llm.ToolCall) (string, error) {
	if r.Tools == nil {
		return "", fmt.Errorf("session: no tool registry")
	}
	// Argument rewriting happens before the permission checks, not after:
	// what a call is allowed to touch is read off its arguments, so a plugin
	// that rewrites a path must have that path be the one asked about.
	call.Input = r.applyToolArgs(ctx, sessionID, call.ID, call.Name, call.Input)
	if r.Permissions != nil {
		// A tool may imply approvals beyond its own action. The shell does:
		// its command can reach outside the working directory, which every
		// file tool refuses, so it declares those paths and they are asked for
		// first. Asking before the tool's own action means a denial stops the
		// command without the model seeing a partial approval.
		if scoped, ok := r.tool(call.Name).(tool.PermissionScoped); ok {
			for _, extra := range scoped.ExtraPermissions(call.Input) {
				if len(extra.Resources) == 0 {
					continue
				}
				err := r.Permissions.Assert(ctx, ToolPermissionInput{
					SessionID:          sessionID,
					Agent:              agentID,
					Action:             extra.Action,
					Resources:          extra.Resources,
					Save:               extra.Save,
					AssistantMessageID: assistantMessageID,
					CallID:             call.ID,
				})
				if err != nil {
					return "", err
				}
			}
		}
		resources := r.permissionResourcesFor(call)
		request := ToolPermissionInput{
			SessionID:          sessionID,
			Agent:              agentID,
			Action:             permissionAction(call.Name),
			Resources:          resources,
			Save:               permissionSave(call.Name, resources),
			AssistantMessageID: assistantMessageID,
			CallID:             call.ID,
		}
		// A configured deny is checked before the plugin hook and is not
		// negotiable. The hook exists to settle a question the user would
		// otherwise be interrupted with; a rule that already says no is not a
		// question. Without this, any plugin answering "allow" switched off
		// every deny in the ruleset — plan mode's read-only constraint
		// included — with nothing in the transcript to say it had.
		if denier, ok := r.Permissions.(PermissionRuled); ok {
			if err := denier.Denied(request); err != nil {
				return "", err
			}
		}
		// A plugin can settle the remaining request before the user is
		// interrupted, porting the permission.ask hook. Only an explicit
		// decision counts: the default status leaves the engine's own
		// evaluation in charge.
		switch r.askPlugins(ctx, request) {
		case plugin.PermissionDeny:
			return "", fmt.Errorf("session: %s denied by plugin", request.Action)
		case plugin.PermissionAllow:
		default:
			if err := r.Permissions.Assert(ctx, request); err != nil {
				return "", err
			}
		}
	}
	output, err := r.Tools.Execute(ctx, call.Name, call.Input, tool.ExecContext{
		SessionID:          sessionID,
		Agent:              agentID,
		AssistantMessageID: assistantMessageID,
		CallID:             call.ID,
	})
	if err != nil {
		return "", err
	}
	return r.applyToolOutput(ctx, sessionID, call.ID, call.Name, call.Input, output), nil
}

// tool looks a tool up by name, returning nil when the registry has none.
func (r *Runner) tool(name string) tool.Tool {
	if r.Tools == nil {
		return nil
	}
	found, ok := r.Tools.Get(name)
	if !ok {
		return nil
	}
	return found
}

// permissionAction maps a tool to its permission action. edit, write, and
// apply_patch share the "edit" action, matching the TypeScript catalog.
func permissionAction(toolName string) string {
	switch toolName {
	case "edit", "write", "apply_patch":
		return "edit"
	default:
		return toolName
	}
}

// permissionResourcesFor asks the tool for its permission resources, falling
// back to the name-based mapping. A tool whose resources are not one input
// field away — apply_patch, whose targets are inside the patch — implements
// tool.PermissionResourced.
func (r *Runner) permissionResourcesFor(call llm.ToolCall) []string {
	if resourced, ok := r.tool(call.Name).(tool.PermissionResourced); ok {
		if resources := resourced.PermissionResources(call.Input); len(resources) > 0 {
			return resources
		}
	}
	return permissionResources(call.Name, call.Input)
}

// permissionResources derives the permission resource from the tool input.
//
// Every case here names the input field the TypeScript tool passes to
// permission.assert. Getting one wrong is not cosmetic: an unmapped field
// falls through to "*", and "*" as a resource matches neither an allow nor a
// deny rule written against a pattern, so the action silently escapes every
// path- or URL-scoped rule a user configured.
func permissionResources(toolName string, input map[string]any) []string {
	resource := ""
	switch toolName {
	case "glob", "grep":
		resource, _ = input["pattern"].(string)
	case "bash":
		resource, _ = input["command"].(string)
	case "task":
		// Scope the grant to the subagent being launched, so a ruleset can
		// allow `explore` while still asking for `general`. Matches the
		// TypeScript task tool's patterns: [subagent_type].
		resource, _ = input["subagent_type"].(string)
	case "webfetch":
		resource, _ = input["url"].(string)
	case "websearch":
		resource, _ = input["query"].(string)
	case "skill":
		// The skill's name, so a ruleset can allow one skill and ask for
		// another — and so "allow always" grants that skill rather than all
		// of them.
		resource, _ = input["name"].(string)
	case "todowrite", "todoread":
		// No meaningful resource; TS passes ["*"] explicitly.
		resource = "*"
	default:
		resource, _ = input["path"].(string)
	}
	if resource == "" {
		resource = "*"
	}
	return []string{resource}
}

// permissionSave derives what an "allow always" reply persists for a tool's
// own action, matching the save values the TypeScript tools pass to
// permission.assert.
//
// The file tools save "*": the question a user answers for `read` is "may you
// read files", not "may you read this one path", and saving the path would
// mean re-asking for the next file. bash and skill save what was asked about,
// because for them the reasoning inverts — remembering "may you run any
// command" or "may you load any skill" from one approval is not what was
// agreed to.
func permissionSave(toolName string, resources []string) []string {
	switch toolName {
	case "bash", "skill":
		return resources
	default:
		return []string{"*"}
	}
}

// failInterruptedTools settles tools left pending or running by an earlier
// interrupted drain, matching the TypeScript pre-drain cleanup.
func (r *Runner) failInterruptedTools(ctx context.Context, sessionID string) error {
	messages, err := r.Messages.List(ctx, sessionID)
	if err != nil {
		return err
	}
	for _, message := range messages {
		if message.Type != TypeAssistant {
			continue
		}
		assistant, err := DecodeAssistant(message.Data)
		if err != nil {
			continue
		}
		for _, part := range assistant.Content {
			if part.Type != "tool" || part.State == nil {
				continue
			}
			if part.State.Status != ToolPending && part.State.Status != ToolRunning {
				continue
			}
			if _, err := r.Bus.Publish(ctx, ToolFailed, map[string]any{
				"sessionID":          sessionID,
				"timestamp":          nowMillis(),
				"assistantMessageID": message.ID,
				"callID":             part.ID,
				"error":              map[string]any{"type": "unknown", "message": "Tool execution interrupted"},
				"provider":           map[string]any{"executed": false},
			}, event.PublishOptions{}); err != nil {
				return err
			}
		}
	}
	return nil
}

// stepError classifies a failed step for the settled assistant message. A
// canceled run context means the user interrupted the turn, which every
// consumer needs to tell apart from a provider failure — see ErrorTypeAborted.
func stepError(err error) map[string]any {
	errType := ErrorTypeUnknown
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		errType = ErrorTypeAborted
	}
	return map[string]any{"type": errType, "message": err.Error()}
}

func nowMillis() int64 {
	return time.Now().UnixMilli()
}

// LoadSessionModel reads the model stored on a session row, if any.
func LoadSessionModel(ctx context.Context, database *db.DB, sessionID string) (*ModelRef, error) {
	var model sql.NullString
	err := database.QueryRow(ctx, `SELECT model FROM session WHERE id = ?`, sessionID).Scan(&model)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, notFound(sessionID)
	}
	if err != nil {
		return nil, err
	}
	if !model.Valid || model.String == "" {
		return nil, nil
	}
	var ref ModelRef
	if err := json.Unmarshal([]byte(model.String), &ref); err != nil {
		return nil, err
	}
	return &ref, nil
}

// compactIfNeeded estimates the history size and runs the compactor when the
// context limit is approaching. Best-effort: compaction failures do not block
// the turn.
func (r *Runner) compactIfNeeded(ctx context.Context, sessionID string, model ModelRef) error {
	if r.Compactor == nil || r.ContextLimit <= 0 {
		return nil
	}
	history, err := r.Messages.ListForRunner(ctx, sessionID)
	if err != nil {
		return err
	}
	requestTokens := 0
	for _, message := range history {
		requestTokens += estimateTokens(string(message.Data))
	}
	requestTokens += estimateTokens(r.System)
	if !r.Compactor.NeedsCompaction(history, r.ContextLimit, requestTokens) {
		return nil
	}
	if _, err := r.Compactor.Compact(ctx, sessionID, history, model); err != nil {
		return err
	}
	return nil
}

// LoadSessionAgent reads the agent stored on a session row, if any.
func LoadSessionAgent(ctx context.Context, database *db.DB, sessionID string) string {
	var agentID sql.NullString
	if err := database.QueryRow(ctx, `SELECT agent FROM session WHERE id = ?`, sessionID).Scan(&agentID); err != nil {
		return ""
	}
	if !agentID.Valid {
		return ""
	}
	return agentID.String
}

// resolveAgent computes the effective agent configuration for a turn,
// mirroring SessionRunnerModel precedence: session model overrides agent
// model, which overrides the runner default.
func (r *Runner) resolveAgent(ctx context.Context, sessionID string) (resolvedAgent, error) {
	resolved := resolvedAgent{
		ID:       r.Agent,
		System:   r.System,
		Model:    r.Model,
		MaxSteps: r.MaxSteps,
	}
	if r.Agents != nil {
		selection := r.Agents.Select(LoadSessionAgent(ctx, r.DB, sessionID))
		resolved.ID = selection.ID
		if selection.Info != nil {
			if selection.Info.System != "" {
				resolved.System = selection.Info.System
			}
			if selection.Info.Model != nil {
				resolved.Model = ModelRef{
					ProviderID: selection.Info.Model.ProviderID,
					ID:         selection.Info.Model.ID,
					Variant:    selection.Info.Model.Variant,
				}
			}
			if selection.Info.Steps > 0 {
				resolved.MaxSteps = selection.Info.Steps
			}
		}
	}
	sessionModel, err := LoadSessionModel(ctx, r.DB, sessionID)
	if err != nil {
		return resolvedAgent{}, err
	}
	if sessionModel != nil {
		resolved.Model = *sessionModel
	}
	return resolved, nil
}

// stepCost prices a settled step, or returns 0 when no pricing is wired or the
// catalog has no rates for the model. See cost.go.
func (r *Runner) stepCost(providerID, modelID string, usage TokenUsage) float64 {
	if r.Pricing == nil {
		return 0
	}
	rates, ok := r.Pricing(providerID, modelID, usage.Input)
	if !ok {
		return 0
	}
	return stepCost(rates, usage)
}
