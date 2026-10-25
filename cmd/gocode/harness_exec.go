package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/langazov/gocode-go/internal/agent"
	"github.com/langazov/gocode-go/internal/db"
	"github.com/langazov/gocode-go/internal/event"
	"github.com/langazov/gocode-go/internal/global"
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
//
// Every one of those sessions is unattended, so nothing in them may wait on
// a person: the agents cannot ask questions or switch to plan mode, the
// runner has no asker, each session has a wall-clock limit, and closing a
// stack cannot outlast closeTimeout. Live sessions are registered in Live,
// so an interrupt sent to the launching server (Esc in the TUI) reaches the
// stack that actually runs them.
type harnessStacks struct {
	Database *db.DB
	Catalog  *modelsdev.Service
	// Model is the executor's provider/model; ImproverModel the improver
	// sessions'. Empty means the usual precedence (config, last used).
	Model         string
	ImproverModel string
	Variant       string
	// Relay, when set, receives every event the rollout and improver stacks
	// commit, so clients of the launching server can watch those sessions
	// stream live.
	Relay *event.Bus
	// Live tracks the stacks running right now, for interrupts.
	Live *liveStacks
	// RolloutTimeout and SessionTimeout bound one rollout and one improver
	// session; zero means the defaults.
	RolloutTimeout time.Duration
	SessionTimeout time.Duration
}

// Default limits for one unattended session. harness2 bounds every solver and
// improver call the same way (ChannelSpec.timeout, the benchmarks' own
// limits); a run must not hang on one session that never ends.
const (
	defaultRolloutTimeout = 30 * time.Minute
	defaultSessionTimeout = 20 * time.Minute
	// closeTimeout bounds tearing a stack down: a plugin or MCP server that
	// will not exit must not hold the run.
	closeTimeout = 20 * time.Second
)

// liveStacks is the set of stacks running Harness² sessions in this process.
type liveStacks struct {
	mu     sync.Mutex
	stacks map[*stack]struct{}
}

func (l *liveStacks) add(st *stack) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.stacks == nil {
		l.stacks = map[*stack]struct{}{}
	}
	l.stacks[st] = struct{}{}
}

func (l *liveStacks) remove(st *stack) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.stacks, st)
}

// Interrupt stops sessionID in whichever live stack runs it — the rollout's
// own session or a subagent it spawned, which runs in the same stack.
// Interrupting an idle session is a no-op, so every stack is simply asked.
func (l *liveStacks) Interrupt(sessionID string) {
	if l == nil {
		return
	}
	l.mu.Lock()
	stacks := make([]*stack, 0, len(l.stacks))
	for st := range l.stacks {
		stacks = append(stacks, st)
	}
	l.mu.Unlock()
	for _, st := range stacks {
		st.Service.Interrupt(sessionID)
	}
}

func (h *harnessStacks) boot(ctx context.Context, dir, model, harnessDir string) (*stack, error) {
	st, err := bootStackWith(ctx, bootOptions{
		Directory:  dir,
		ModelFlag:  model,
		Database:   h.Database,
		Catalog:    h.Catalog,
		HarnessDir: harnessDir,
		Ephemeral:  true,
	})
	if err != nil {
		return nil, err
	}
	if h.Relay != nil {
		st.Bus.Listen(h.Relay.Deliver)
	}
	// Nobody is there to answer: a network-outage "keep waiting?" question
	// would park the turn for good.
	st.Runner.Asker = nil
	h.Live.add(st)
	return st, nil
}

// release tears a stack down within closeTimeout. A close that outlasts it
// is left to finish in the background and logged.
func (h *harnessStacks) release(st *stack) {
	h.Live.remove(st)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = st.Close()
	}()
	select {
	case <-done:
	case <-time.After(closeTimeout):
		global.LogBackground("harness: closing the stack for %s took over %s; left to finish in the background", st.Workdir(), closeTimeout)
	}
}

func limit(d, fallback time.Duration) time.Duration {
	if d > 0 {
		return d
	}
	return fallback
}

// Rollout runs the task once in req.Workspace under req.HarnessDir.
func (h *harnessStacks) Rollout(ctx context.Context, req harness.RolloutRequest) (harness.Rollout, error) {
	st, err := h.boot(ctx, req.Workspace, h.Model, req.HarnessDir)
	if err != nil {
		return harness.Rollout{}, err
	}
	defer h.release(st)
	// Nobody is watching a rollout, so asks are answered — but nothing may
	// leave the workspace except into the harness it runs under, matching
	// the external_directory fence harness2 sets through OPENCODE_PERMISSION.
	applyPermissionBypass(st.Runner, st.PermissionEngine, true, false)
	fenceAgents(st.Agents, req.HarnessDir)

	sessionID, err := h.start(ctx, st, req.Workspace, "harness "+req.Task.ID+" · "+req.Label, "", h.Model)
	if err != nil {
		return harness.Rollout{}, err
	}
	if req.OnSession != nil {
		req.OnSession(sessionID)
	}
	runErr := h.drive(ctx, st, sessionID, req.Task.Prompt, limit(h.RolloutTimeout, defaultRolloutTimeout))
	// Read back on a context of its own: the run's may be the one that just
	// timed out or was cancelled, and the record of what happened still
	// has to be written.
	result := collectRollout(context.WithoutCancel(ctx), st, sessionID)
	if runErr != nil {
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
	defer h.release(st)
	registerHarnessAgents(st.Agents)
	fenceAgents(st.Agents, "")
	applyPermissionBypass(st.Runner, st.PermissionEngine, true, false)

	sessionID, err := h.start(ctx, st, req.Workspace, "harness "+req.Label, req.Role, model)
	if err != nil {
		return harness.Reply{}, err
	}
	if req.OnSession != nil {
		req.OnSession(sessionID)
	}
	runErr := h.drive(ctx, st, sessionID, req.Mission, limit(h.SessionTimeout, defaultSessionTimeout))
	result := collectRollout(context.WithoutCancel(ctx), st, sessionID)
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

// drive prompts the session and waits for its turn to settle, at most
// timeout. The turn runs as a detached drain (Prompt wakes it), so a
// cancelled or expired context does not reach it by itself: it is
// interrupted explicitly, which is also what Esc does.
func (h *harnessStacks) drive(ctx context.Context, st *stack, sessionID, prompt string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if _, err := st.Service.Prompt(ctx, sessionID, prompt, session.DeliverySteer); err != nil {
		return err
	}
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		select {
		case <-ctx.Done():
			st.Service.Interrupt(sessionID)
		case <-stop:
		}
	}()
	err := st.Service.Execution.Resume(context.WithoutCancel(ctx), sessionID)
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return fmt.Errorf("stopped after %s: the session did not finish in time", timeout)
	case ctx.Err() != nil:
		return fmt.Errorf("stopped: %w", ctx.Err())
	}
	return err
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

// fenceAgents keeps an unattended stack's agents inside their working
// directory (except the harness it runs under) and off every tool that waits
// on a person. Appended last, so it wins over the baseline's ask (last match
// wins) — an ask under --auto would be approved.
func fenceAgents(agents *agent.Registry, harnessDir string) {
	fence := permission.Ruleset{
		{Action: permission.ExternalDirectoryAction, Resource: "*", Effect: permission.Deny},
		// Nobody is there to answer. A question or a plan-mode switch parks
		// the turn on a person and the session never settles — the hang
		// this closes. Denied, the model is told the tool is unavailable
		// and carries on.
		{Action: "question", Resource: "*", Effect: permission.Deny},
		{Action: "plan_enter", Resource: "*", Effect: permission.Deny},
		{Action: "plan_exit", Resource: "*", Effect: permission.Deny},
	}
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
