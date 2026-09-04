package builtins

import (
	"context"
	"fmt"
	"strings"

	"github.com/langazov/gocode-go/internal/question"
	"github.com/langazov/gocode-go/internal/tool"
)

// AgentSwitcher is the slice of the session service plan mode needs: pinning
// the agent a session runs under, and admitting the prompt that starts the
// next turn under it. Implemented by session.Service; kept as an interface so
// the tool layer does not import the session package (which imports
// internal/tool).
type AgentSwitcher interface {
	SetAgent(ctx context.Context, sessionID, agent string) error
	// EnqueuePrompt admits a prompt for delivery at the session's next idle
	// boundary. Queued rather than steered: it is issued from inside a tool
	// call, and interrupting the turn that is still returning this tool's
	// result would strand it.
	EnqueuePrompt(ctx context.Context, sessionID, text string) error
}

// planTool is the shared body of plan_enter and plan_exit: ask the user
// whether to switch agents, and switch if they say yes.
type planTool struct {
	name        string
	target      string
	header      string
	prompt      string
	yesDetail   string
	noDetail    string
	description string
	// handoff, when set, is admitted as a prompt after the switch so the
	// target agent actually gets a turn. Without it the switch only takes
	// effect the next time the user happens to type something.
	handoff string
	// accepted is the tool output on a yes. It has to agree with handoff:
	// where a handoff prompt follows, the model is told to wait for it, so it
	// does not spend the rest of the current turn guessing at the work the
	// prompt is about to hand it.
	accepted string

	asker    Asker
	switcher AgentSwitcher
}

// NewPlanEnterTool suggests moving from an implementation agent into plan mode.
func NewPlanEnterTool(asker Asker, switcher AgentSwitcher) tool.Tool {
	return &planTool{
		name:      "plan_enter",
		target:    "plan",
		header:    "Plan Agent",
		prompt:    "Would you like to switch to the plan agent and design this before implementing?",
		yesDetail: "Switch to the plan agent and design the change first",
		noDetail:  "Stay with the current agent and continue implementing",
		// No handoff: the plan agent picks up the request the user already
		// made, and the next step of this same turn already runs as plan
		// (the runner re-resolves the agent per step).
		accepted: "Switched to the plan agent. You are now in plan mode — research and design, and do not make changes.",
		description: strings.Join([]string{
			"Use this tool to suggest switching to plan agent when the user's request would benefit from planning before implementation.",
			"",
			"If they explicitly mention wanting to create a plan ALWAYS call this tool first.",
			"",
			"This tool will ask the user if they want to switch to plan agent.",
			"",
			"Call this tool when:",
			"- The user's request is complex and would benefit from planning first",
			"- You want to research and design before making changes",
			"- The task involves multiple files or significant architectural decisions",
			"",
			"Do NOT call this tool:",
			"- For simple, straightforward tasks",
			"- When the user explicitly wants immediate implementation",
		}, "\n"),
		asker:    asker,
		switcher: switcher,
	}
}

// NewPlanExitTool proposes leaving plan mode once the plan is ready.
func NewPlanExitTool(asker Asker, switcher AgentSwitcher) tool.Tool {
	return &planTool{
		name:      "plan_exit",
		target:    "build",
		header:    "Build Agent",
		prompt:    "The plan is complete. Would you like to switch to the build agent and start implementing?",
		yesDetail: "Switch to build agent and start implementing the plan",
		noDetail:  "Stay with plan agent to continue refining the plan",
		// Mirrors the synthetic part tool/plan.ts attaches to the user message
		// it writes on approval. Only the exit direction gets a handoff: the
		// build agent has a plan to execute, whereas entering plan mode has
		// nothing to say beyond the request the user already made.
		handoff: "The plan has been approved, you can now edit files. Execute the plan",
		// Verbatim from tool/plan.ts. "Wait" is load-bearing: the handoff
		// prompt above arrives as its own turn, and without this the model
		// starts implementing in the tail of the planning turn instead.
		accepted: "User approved switching to build agent. Wait for further instructions.",
		description: strings.Join([]string{
			"Use this tool when you have completed the planning phase and are ready to exit plan agent.",
			"",
			"This tool will ask the user if they want to switch to build agent to start implementing the plan.",
			"",
			"Call this tool:",
			"- After you have written a complete plan to the plan file",
			"- After you have clarified any questions with the user",
			"- When you are confident the plan is ready for implementation",
			"",
			"Do NOT call this tool:",
			"- Before you have created or finalized the plan",
			"- If you still have unanswered questions about the implementation",
			"- If the user has indicated they want to continue planning",
		}, "\n"),
		asker:    asker,
		switcher: switcher,
	}
}

func (t *planTool) Name() string        { return t.name }
func (t *planTool) Description() string { return t.description }

func (t *planTool) InputSchema() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{}}
}

func (t *planTool) Execute(ctx context.Context, input map[string]any) (string, error) {
	return t.ExecuteWithContext(ctx, input, tool.ExecContext{})
}

func (t *planTool) ExecuteWithContext(ctx context.Context, _ map[string]any, exec tool.ExecContext) (string, error) {
	if exec.SessionID == "" {
		return "", fmt.Errorf("%s: missing session context", t.name)
	}
	if t.asker == nil || t.switcher == nil {
		return "", fmt.Errorf("%s: plan mode is not configured", t.name)
	}

	var source *question.Source
	if exec.CallID != "" {
		source = &question.Source{MessageID: exec.AssistantMessageID, CallID: exec.CallID}
	}
	answers, err := t.asker.Ask(ctx, question.AskInput{
		SessionID: exec.SessionID,
		Source:    source,
		Questions: []question.Prompt{{
			Question: t.prompt,
			Header:   t.header,
			Options: []question.Option{
				{Label: "Yes", Description: t.yesDetail},
				{Label: "No", Description: t.noDetail},
			},
		}},
	})
	if err != nil {
		return "", err
	}
	if len(answers) == 0 || len(answers[0]) == 0 || answers[0][0] != "Yes" {
		return "The user chose to stay with the current agent. Continue as you were.", nil
	}
	if err := t.switcher.SetAgent(ctx, exec.SessionID, t.target); err != nil {
		return "", err
	}
	if t.handoff != "" {
		if err := t.switcher.EnqueuePrompt(ctx, exec.SessionID, t.handoff); err != nil {
			return "", err
		}
	}
	return t.accepted, nil
}
