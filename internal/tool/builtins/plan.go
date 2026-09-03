package builtins

import (
	"context"
	"fmt"
	"strings"

	"github.com/langazov/gocode-go/internal/question"
	"github.com/langazov/gocode-go/internal/tool"
)

// AgentSwitcher changes the agent a session runs under. Implemented by
// session.Service; kept as an interface so the tool layer does not import the
// session package (which imports internal/tool).
type AgentSwitcher interface {
	SetAgent(ctx context.Context, sessionID, agent string) error
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
	return "Switched to the " + t.target + " agent. Continue with that agent's instructions.", nil
}
