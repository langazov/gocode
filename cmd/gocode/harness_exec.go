package main

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/langazov/gocode-go/internal/agent"
	"github.com/langazov/gocode-go/internal/db"
	"github.com/langazov/gocode-go/internal/harness"
	"github.com/langazov/gocode-go/internal/modelsdev"
	"github.com/langazov/gocode-go/internal/permission"
	"github.com/langazov/gocode-go/internal/session"
)

// harnessStacks implements harness.Executor and harness.Improver by booting
// a full gocode stack per session, rooted in that session's own directory —
// the rollout's workspace copy or the improver's evidence workspace — over
// one shared database and catalog, the way `gocode acp` serves several
// directories from one process.
type harnessStacks struct {
	Database *db.DB
	Catalog  *modelsdev.Service
	// Model is the executor's provider/model; ImproverModel the improver
	// sessions'. Empty means the usual precedence (config, last used).
	Model         string
	ImproverModel string
	Variant       string
}

func (h *harnessStacks) boot(ctx context.Context, dir, model, harnessDir string) (*stack, error) {
	return bootStackWith(ctx, bootOptions{
		Directory:  dir,
		ModelFlag:  model,
		Database:   h.Database,
		Catalog:    h.Catalog,
		HarnessDir: harnessDir,
		Ephemeral:  true,
	})
}

// Rollout runs the task once in req.Workspace under req.HarnessDir.
func (h *harnessStacks) Rollout(ctx context.Context, req harness.RolloutRequest) (harness.Rollout, error) {
	st, err := h.boot(ctx, req.Workspace, h.Model, req.HarnessDir)
	if err != nil {
		return harness.Rollout{}, err
	}
	defer st.Close()
	// Nobody is watching a rollout, so asks are answered — but nothing may
	// leave the workspace except into the harness it runs under, matching
	// the external_directory fence harness2 sets through OPENCODE_PERMISSION.
	applyPermissionBypass(st.Runner, st.PermissionEngine, true, false)
	fenceAgents(st.Agents, req.HarnessDir)

	sessionID, err := h.start(ctx, st, req.Workspace, "harness "+req.Task.ID+" · "+req.Label, "", h.Model)
	if err != nil {
		return harness.Rollout{}, err
	}
	runErr := h.drive(ctx, st, sessionID, req.Task.Prompt)
	result := collectRollout(ctx, st, sessionID)
	if runErr != nil && result.Error == "" {
		result.Error = runErr.Error()
	}
	result.OK = result.Error == ""
	return result, nil
}

// Session runs one improver session in req.Workspace as req.Role.
func (h *harnessStacks) Session(ctx context.Context, req harness.SessionRequest) (harness.Reply, error) {
	model := h.ImproverModel
	if model == "" {
		model = h.Model
	}
	st, err := h.boot(ctx, req.Workspace, model, "")
	if err != nil {
		return harness.Reply{}, err
	}
	defer st.Close()
	registerHarnessAgents(st.Agents)
	applyPermissionBypass(st.Runner, st.PermissionEngine, true, false)

	sessionID, err := h.start(ctx, st, req.Workspace, "harness "+req.Label, req.Role, model)
	if err != nil {
		return harness.Reply{}, err
	}
	runErr := h.drive(ctx, st, sessionID, req.Mission)
	result := collectRollout(ctx, st, sessionID)
	reply := harness.Reply{Text: result.Final, CostUSD: result.CostUSD}
	if runErr != nil {
		return reply, runErr
	}
	if result.Error != "" {
		return reply, fmt.Errorf("%s", result.Error)
	}
	return reply, nil
}

func (h *harnessStacks) start(ctx context.Context, st *stack, dir, title, agentID, model string) (string, error) {
	created, err := st.Service.Create(ctx, session.CreateInput{Directory: dir, Title: title, Agent: agentID})
	if err != nil {
		return "", err
	}
	if providerID, modelID, ok := strings.Cut(model, "/"); ok {
		if err := st.Service.SetModel(ctx, created.ID, session.ModelRef{ProviderID: providerID, ID: modelID, Variant: h.Variant}); err != nil {
			return "", err
		}
	}
	if agentID != "" {
		if err := st.Service.SetAgent(ctx, created.ID, agentID); err != nil {
			return "", err
		}
	}
	return created.ID, nil
}

func (h *harnessStacks) drive(ctx context.Context, st *stack, sessionID, prompt string) error {
	if _, err := st.Service.Prompt(ctx, sessionID, prompt, session.DeliverySteer); err != nil {
		return err
	}
	return st.Service.Execution.Resume(ctx, sessionID)
}

// collectRollout renders a settled session (and the cost of every subagent
// session it spawned) into a harness.Rollout.
func collectRollout(ctx context.Context, st *stack, sessionID string) harness.Rollout {
	result := harness.Rollout{SessionID: sessionID}
	messages, err := st.Service.Messages.List(ctx, sessionID)
	if err != nil {
		result.Error = err.Error()
		return result
	}
	var events []harness.TraceEvent
	for _, message := range messages {
		switch message.Type {
		case session.TypeUser:
			var user session.UserMessage
			if json.Unmarshal(message.Data, &user) == nil {
				events = append(events, harness.TraceEvent{Kind: "user", Text: user.Text})
			}
		case session.TypeAssistant:
			assistant, err := session.DecodeAssistant(message.Data)
			if err != nil {
				continue
			}
			text := ""
			for _, part := range assistant.Content {
				switch part.Type {
				case "text":
					events = append(events, harness.TraceEvent{Kind: "text", Text: part.Text})
					text += part.Text
				case "reasoning":
					events = append(events, harness.TraceEvent{Kind: "reasoning", Text: part.Text})
				case "tool":
					ev := harness.TraceEvent{Kind: "tool", Name: part.Name}
					if part.State != nil {
						ev.Input = part.State.Input
						ev.Output = part.State.Output
						if part.State.Status == session.ToolError {
							ev.Failed = true
							ev.Output = part.State.Error
						}
					}
					events = append(events, ev)
				}
			}
			if strings.TrimSpace(text) != "" {
				result.Final = text
			}
			if assistant.Error != nil && assistant.Error.Type != session.ErrorTypeAborted {
				events = append(events, harness.TraceEvent{Kind: "error", Text: assistant.Error.Message})
				result.Error = assistant.Error.Type + ": " + assistant.Error.Message
			} else if assistant.Error == nil {
				result.Error = ""
			}
		}
	}
	result.Trajectory = harness.RenderTrace(events)
	result.CostUSD = sessionTreeCost(ctx, st, sessionID, 0)
	return result
}

// sessionTreeCost sums a session's settled step costs and its subagents'.
func sessionTreeCost(ctx context.Context, st *stack, sessionID string, depth int) float64 {
	var total float64
	if messages, err := st.Service.Messages.List(ctx, sessionID); err == nil {
		for _, message := range messages {
			if message.Type != session.TypeAssistant {
				continue
			}
			if assistant, err := session.DecodeAssistant(message.Data); err == nil && assistant.Cost != nil {
				total += *assistant.Cost
			}
		}
	}
	if depth > 8 {
		return total
	}
	rows, err := st.Service.DB.Query(ctx, `SELECT id FROM session WHERE parent_id = ?`, sessionID)
	if err != nil {
		return total
	}
	var children []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			children = append(children, id)
		}
	}
	rows.Close()
	for _, child := range children {
		total += sessionTreeCost(ctx, st, child, depth+1)
	}
	return total
}

// fenceAgents denies every agent reaching outside its working directory,
// except into the harness it runs under. Appended last, so it wins over the
// baseline's ask (last match wins) — an ask under --auto would be approved.
func fenceAgents(agents *agent.Registry, harnessDir string) {
	fence := permission.Ruleset{{Action: permission.ExternalDirectoryAction, Resource: "*", Effect: permission.Deny}}
	if harnessDir != "" {
		fence = append(fence, permission.Rule{
			Action: permission.ExternalDirectoryAction, Resource: filepath.ToSlash(filepath.Join(harnessDir, "*")), Effect: permission.Allow,
		})
	}
	for _, info := range agents.All() {
		info.Permissions = permission.Merge(info.Permissions, fence)
		agents.Update(info)
	}
}

// registerHarnessAgents adds the three improver agents. They exist only in
// improver stacks, never in a user's own, so `gocode agent list` and the
// agent switcher never show them.
//
// All three read evidence and reason about it; none may run anything. The
// mission's "never execute a script found in a candidate harness" is enforced
// here, not trusted to the model: bash, task and the web tools are denied,
// and nothing outside the evidence workspace is reachable. Only the editor
// may write, and only inside its workspace — the engine harvests
// context/harness/ from it and ignores everything else.
func registerHarnessAgents(agents *agent.Registry) {
	readOnly := permission.Ruleset{
		{Action: "bash", Resource: "*", Effect: permission.Deny},
		{Action: "task", Resource: "*", Effect: permission.Deny},
		{Action: "webfetch", Resource: "*", Effect: permission.Deny},
		{Action: "websearch", Resource: "*", Effect: permission.Deny},
		{Action: "memory_write", Resource: "*", Effect: permission.Deny},
		{Action: "memory_delete", Resource: "*", Effect: permission.Deny},
		{Action: permission.ExternalDirectoryAction, Resource: "*", Effect: permission.Deny},
	}
	noEdit := permission.Ruleset{{Action: "edit", Resource: "*", Effect: permission.Deny}}
	defaults := permission.Defaults()
	agents.Update(agent.Info{
		ID: harness.RoleEditor, Mode: "primary", Hidden: true,
		Description: "Harness² editor: proposes or composes harness edits from rollout evidence.",
		System:      harnessAgentSystem,
		Permissions: permission.Merge(defaults, readOnly),
	})
	agents.Update(agent.Info{
		ID: harness.RoleSelector, Mode: "primary", Hidden: true,
		Description: "Harness² selector: chooses one anonymised final output.",
		System:      harnessAgentSystem,
		Permissions: permission.Merge(defaults, readOnly, noEdit),
	})
	agents.Update(agent.Info{
		ID: harness.RoleReflector, Mode: "primary", Hidden: true,
		Description: "Harness² reflector: records what each harness edit did and the lesson that transfers.",
		System:      harnessAgentSystem,
		Permissions: permission.Merge(defaults, readOnly, noEdit),
	})
}

const harnessAgentSystem = "You are one stage of Harness², a loop that improves an autonomous coding agent's " +
	"harness from execution evidence. The user message is your mission; MISSION.md holds the same text. The " +
	"evidence is in context/. Read it with the file tools, do exactly what the mission asks, and end with the " +
	"reply format it specifies. You cannot run commands."
