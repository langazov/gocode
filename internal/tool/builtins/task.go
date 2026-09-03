package builtins

import (
	"context"
	"fmt"
	"strings"

	"github.com/langazov/gocode-go/internal/background"
	"github.com/langazov/gocode-go/internal/tool"
)

// TaskTool spawns a subagent to handle a delegated unit of work.
//
// Each call runs in its own goroutine (the runner dispatches every tool that
// way — see MULTI_AGENTS.md phase 1), and the subagent itself runs on another
// goroutine keyed by its own session ID. This tool is the join point: it
// blocks its worker goroutine on the spawner's result channel while the child
// runs. N task calls in one assistant step therefore produce N genuinely
// concurrent subagents.
type TaskTool struct {
	spawner tool.Spawner
	// jobs enables background mode. nil keeps the tool foreground-only, which
	// is the default until GOCODE_EXPERIMENTAL_BACKGROUND_SUBAGENTS is set.
	jobs *background.Registry
}

func NewTaskTool(spawner tool.Spawner) *TaskTool {
	return &TaskTool{spawner: spawner}
}

// NewBackgroundTaskTool additionally allows task(background: true) and
// promotion of a running foreground task.
func NewBackgroundTaskTool(spawner tool.Spawner, jobs *background.Registry) *TaskTool {
	return &TaskTool{spawner: spawner, jobs: jobs}
}

const backgroundDescription = `
Background mode: background=true launches the subagent asynchronously and returns immediately.
Foreground is the default; use it when you need the result before continuing.
Use background only for independent work that can run while you continue elsewhere.
You will be notified automatically when it finishes.`

const backgroundStarted = `The task is working in the background. You will be notified automatically when it finishes.
DO NOT sleep, poll for progress, ask the task for status, or duplicate this task's work — avoid working with the same files or topics it is using.
Work on non-overlapping tasks, or briefly tell the user what you launched and end your response.`

func (t *TaskTool) Name() string { return "task" }

func (t *TaskTool) Description() string {
	if t.jobs != nil {
		return t.description() + "\n" + backgroundDescription
	}
	return t.description()
}

func (t *TaskTool) description() string {
	return strings.Join([]string{
		"Launch a new agent to handle complex, multistep tasks autonomously.",
		"",
		"When using the Task tool, you must specify a subagent_type parameter to select which agent type to use.",
		"",
		"When NOT to use the Task tool:",
		"- If you want to read a specific file path, use the Read or Glob tool instead, to find the match more quickly",
		"- If you are searching for a specific class definition like \"class Foo\", use the Grep tool instead",
		"- If you are searching within a specific file or set of 2-3 files, use the Read tool instead",
		"- If no available agent is a good fit for the task, use other tools directly",
		"",
		"Usage notes:",
		"1. Launch multiple agents concurrently whenever possible, to maximize performance; to do that, use a single message with multiple tool uses",
		"2. Once you have delegated work to an agent, do not duplicate that work yourself. Continue with non-overlapping tasks, or wait for the result.",
		"3. When the agent is done, it returns a single message back to you. That result is not visible to the user, so summarize it for them yourself. The output includes a task_id you can reuse later to continue the same subagent session.",
		"4. Each agent invocation starts with a fresh context unless you provide task_id to resume the same subagent session. When starting fresh, your prompt should contain a highly detailed task description, and should say exactly what information the agent must return in its final message.",
		"5. The agent's outputs should generally be trusted.",
		"6. Clearly tell the agent whether you expect it to write code or just to do research, since it is not aware of the user's intent. Tell it how to verify its work if possible.",
	}, "\n")
}

func (t *TaskTool) InputSchema() map[string]any {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"description": map[string]any{
				"type":        "string",
				"description": "A short (3-5 words) description of the task",
			},
			"prompt": map[string]any{
				"type":        "string",
				"description": "The task for the agent to perform",
			},
			"subagent_type": map[string]any{
				"type":        "string",
				"description": "The type of specialized agent to use for this task",
			},
			"task_id": map[string]any{
				"type":        "string",
				"description": "Set this only to resume a previous task: pass a prior task_id and the task continues that subagent session instead of creating a fresh one",
			},
		},
		"required": []string{"description", "prompt", "subagent_type"},
	}
	if t.jobs != nil {
		schema["properties"].(map[string]any)["background"] = map[string]any{
			"type":        "boolean",
			"description": "Run the agent in the background. You will be notified when it completes. DO NOT sleep, poll, or proactively check on its progress.",
		}
	}
	return schema
}

// Execute without a session context cannot spawn: the child needs a parent to
// hang off. The registry routes this tool through ExecuteWithContext.
func (t *TaskTool) Execute(ctx context.Context, input map[string]any) (string, error) {
	return "", fmt.Errorf("task: missing session context")
}

func (t *TaskTool) ExecuteWithContext(ctx context.Context, input map[string]any, exec tool.ExecContext) (string, error) {
	if exec.SessionID == "" {
		return "", fmt.Errorf("task: missing session context")
	}
	description := stringArg(input, "description")
	prompt := stringArg(input, "prompt")
	subagentType := stringArg(input, "subagent_type")
	if prompt == "" {
		return "", fmt.Errorf("task: prompt is required")
	}
	if subagentType == "" {
		return "", fmt.Errorf("task: subagent_type is required")
	}
	if found, isSubagent := t.spawner.Agent(subagentType); !found {
		return "", fmt.Errorf("Unknown agent type: %s is not a valid agent type", subagentType)
	} else if !isSubagent {
		return "", fmt.Errorf("Agent %s is a primary agent and cannot run as a subagent", subagentType)
	}

	wantsBackground, _ := input["background"].(bool)
	if wantsBackground && t.jobs == nil {
		return "", fmt.Errorf("Background subagents require GOCODE_EXPERIMENTAL_BACKGROUND_SUBAGENTS=true")
	}

	childID, done, err := t.spawner.Spawn(ctx, tool.SpawnRequest{
		ParentSessionID: exec.SessionID,
		AgentID:         subagentType,
		Description:     description,
		Prompt:          prompt,
		ResumeSessionID: stringArg(input, "task_id"),
	})
	if err != nil {
		return "", err
	}

	if t.jobs == nil {
		return t.waitForeground(ctx, childID, done)
	}
	return t.runAsJob(ctx, exec.SessionID, childID, description, wantsBackground, done)
}

// waitForeground blocks this tool's worker goroutine until the child reports
// back. The runner dispatches every tool on its own goroutine, so blocking
// here does not stall the parent's stream or its sibling tool calls.
func (t *TaskTool) waitForeground(ctx context.Context, childID string, done <-chan tool.SpawnResult) (string, error) {
	select {
	case result := <-done:
		if result.Err != nil {
			return "", result.Err
		}
		return renderTask(result.SessionID, "completed", result.Text), nil
	case <-ctx.Done():
		// The parent turn was interrupted; stop the child rather than
		// orphaning it, then report the interruption.
		t.spawner.Cancel(childID)
		return "", ctx.Err()
	}
}

// runAsJob registers the spawn with the background registry, then races the
// three ways a task call can end: the child finishes, someone promotes the
// job to the background, or the parent is interrupted.
func (t *TaskTool) runAsJob(
	ctx context.Context,
	parentSessionID, childID, description string,
	wantsBackground bool,
	done <-chan tool.SpawnResult,
) (string, error) {
	t.jobs.Start(ctx, background.StartInput{
		ID:         childID,
		Type:       "task",
		Title:      description,
		Background: wantsBackground,
		Metadata: map[string]any{
			"parentSessionID": parentSessionID,
			"sessionID":       childID,
		},
		Run: func(runCtx context.Context) (string, error) {
			select {
			case result := <-done:
				return result.Text, result.Err
			case <-runCtx.Done():
				t.spawner.Cancel(childID)
				return "", runCtx.Err()
			}
		},
	})

	// Once detached, the result comes back to the parent as a synthetic
	// prompt instead of as this call's return value.
	notifyOnCompletion := func() {
		go func() {
			<-t.jobs.Done(childID)
			info, ok := t.jobs.Get(childID)
			if !ok {
				return
			}
			state, body := "completed", info.Output
			if info.Status != background.StatusCompleted {
				state, body = "error", info.Err
			}
			summary := fmt.Sprintf("Background task %s: %s", state, description)
			_ = t.spawner.Notify(context.WithoutCancel(ctx), parentSessionID,
				renderTaskWithSummary(childID, state, summary, body))
		}()
	}

	if wantsBackground {
		notifyOnCompletion()
		return renderTaskWithSummary(childID, "running", "Background task started", backgroundStarted), nil
	}

	select {
	case <-t.jobs.Done(childID):
		info, ok := t.jobs.Get(childID)
		if !ok {
			return "", fmt.Errorf("task: job %s vanished", childID)
		}
		if info.Status != background.StatusCompleted {
			return "", fmt.Errorf("Subagent failed (task_id: %s): %s", childID, info.Err)
		}
		return renderTask(childID, "completed", info.Output), nil
	case <-t.jobs.Promoted(childID):
		// Someone pushed this task to the background while it was running.
		notifyOnCompletion()
		return renderTaskWithSummary(childID, "running", "Background task started", backgroundStarted), nil
	case <-ctx.Done():
		t.jobs.Cancel(childID)
		t.spawner.Cancel(childID)
		return "", ctx.Err()
	}
}

// renderTask wraps a subagent result the way the TypeScript task tool does, so
// the model sees a task_id it can resume with.
func renderTask(sessionID, state, text string) string {
	return renderTaskWithSummary(sessionID, state, "", text)
}

func renderTaskWithSummary(sessionID, state, summary, text string) string {
	tag := "task_result"
	if state == "error" {
		tag = "task_error"
	}
	lines := []string{fmt.Sprintf("<task id=%q state=%q>", sessionID, state)}
	if summary != "" {
		lines = append(lines, "<summary>"+summary+"</summary>")
	}
	lines = append(lines, "<"+tag+">", text, "</"+tag+">", "</task>")
	return strings.Join(lines, "\n")
}
