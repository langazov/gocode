package acp

import (
	"context"
	"encoding/base64"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/langazov/gocode-go/internal/event"
	"github.com/langazov/gocode-go/internal/id"
	"github.com/langazov/gocode-go/internal/session"
)

// attach connects a freshly booted runtime to the connection: its bus feeds
// the session updates, and its permission gate is wrapped so tool starts and
// per-session auto-approval are visible here.
func (a *Agent) attach(rt *Runtime) {
	rt.Runner.Permissions = &gate{agent: a, runtime: rt, inner: rt.Runner.Permissions}
	rt.Bus.Listen(func(payload event.Payload) { a.onEvent(rt, payload) })
}

// onEvent routes one committed bus event. Listeners run synchronously on the
// publishing goroutine — the runner's own loop — which is what orders every
// update ahead of the turn's settlement; anything that waits on the client
// (permission prompts) is handed off to a queue instead.
func (a *Agent) onEvent(rt *Runtime, payload event.Payload) {
	sessionID, _ := payload.Data["sessionID"].(string)
	if sessionID == "" {
		return
	}
	switch payload.Type {
	case session.PermissionAsked.Type, session.QuestionAsked.Type:
		// A subagent's ask is answered by the client of the session that
		// spawned it: nobody else is there to answer.
		s, _ := a.sessionFor(rt, sessionID)
		if s == nil {
			return
		}
		requestID, _ := payload.Data["requestID"].(string)
		if payload.Type == session.PermissionAsked.Type {
			a.enqueuePermission(s, requestID)
		} else {
			a.enqueueQuestion(s, requestID)
		}
		return
	}
	a.mu.Lock()
	s := a.sessions[sessionID]
	a.mu.Unlock()
	if s == nil || s.runtime != rt {
		return
	}
	a.translate(s, payload)
}

// translate turns one of the session's own events into session updates.
func (a *Agent) translate(s *acpSession, payload event.Payload) {
	data := payload.Data
	str := func(key string) string {
		value, _ := data[key].(string)
		return value
	}
	switch payload.Type {
	case session.TextDelta.Type:
		if delta := str("delta"); delta != "" {
			a.update(s.id, obj{
				"sessionUpdate": "agent_message_chunk",
				"messageId":     str("textID"),
				"content":       obj{"type": "text", "text": delta},
			})
		}
	case session.ReasoningDelta.Type:
		if delta := str("delta"); delta != "" {
			a.update(s.id, obj{
				"sessionUpdate": "agent_thought_chunk",
				"messageId":     str("reasoningID"),
				"content":       obj{"type": "text", "text": delta},
			})
		}
	case session.ToolCalled.Type:
		input, _ := data["input"].(map[string]any)
		a.toolCalled(s, str("callID"), str("tool"), input)
	case session.ToolMetaUpdated.Type:
		if title := str("title"); title != "" {
			a.update(s.id, obj{"sessionUpdate": "tool_call_update", "toolCallId": str("callID"), "title": title})
		}
	case session.ToolSuccess.Type:
		output, _ := data["output"].(string)
		a.toolSettled(s, str("callID"), output, "")
	case session.ToolFailed.Type:
		message := "tool failed"
		if failure, ok := data["error"].(map[string]any); ok {
			if text, _ := failure["message"].(string); text != "" {
				message = text
			}
		}
		a.toolSettled(s, str("callID"), "", message)
	case session.AgentSwitched.Type:
		a.agentSwitched(s, str("agent"))
	case session.StepStarted.Type:
		if model, ok := data["model"].(map[string]any); ok {
			providerID, _ := model["providerID"].(string)
			modelID, _ := model["id"].(string)
			s.mu.Lock()
			s.stepModel = [2]string{providerID, modelID}
			s.mu.Unlock()
		}
	case session.StepEnded.Type:
		a.usageUpdate(s, data)
	case session.RunStarted.Type:
		// A run the client did not start (a queued prompt, a background
		// resume) is still foreground work to report in v2.
		if a.protocolVersion() == ProtocolV2 && !s.busy() {
			a.setState(s, "running", "")
		}
	case session.RunEnded.Type:
		if a.protocolVersion() == ProtocolV2 && !s.busy() {
			a.afterTurn(context.Background(), s)
			a.setState(s, "idle", "end_turn")
		}
	case session.RunFailed.Type:
		if a.protocolVersion() == ProtocolV2 && !s.busy() {
			a.update(s.id, obj{
				"sessionUpdate": "agent_message",
				"messageId":     "run-failed-" + payload.ID,
				"content":       []obj{{"type": "text", "text": "Error: " + str("error")}},
			})
		}
	}
}

// busy reports whether a client-started turn is running.
func (s *acpSession) busy() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.turn != nil
}

// setState reports the v2 foreground state, skipping a repeat of the state
// already reported — a second idle would read as a second, differently
// explained end of the same work. An idle transition that ends work carries
// its stop reason (protocol/v2/prompt-lifecycle "Report Completion").
func (a *Agent) setState(s *acpSession, state, stopReason string) {
	if a.protocolVersion() != ProtocolV2 {
		return
	}
	s.mu.Lock()
	if s.state == state {
		s.mu.Unlock()
		return
	}
	s.state = state
	s.mu.Unlock()
	update := obj{"sessionUpdate": "state_update", "state": state}
	if state == "idle" && stopReason != "" {
		update["stopReason"] = stopReason
	}
	a.update(s.id, update)
}

// toolCalled reports a new tool call: v1 creates it with tool_call, v2 with
// the first tool_call_update for the id (protocol/v2/migration "Tool calls").
// Files an edit may touch are snapshotted now, before the call runs, so its
// settlement can report a real diff.
func (a *Agent) toolCalled(s *acpSession, callID, name string, input map[string]any) {
	if callID == "" {
		return
	}
	track := &toolTrack{
		callID: callID,
		name:   name,
		input:  input,
		before: snapshot(editedPaths(name, input, s.cwd)),
		status: "pending",
	}
	s.mu.Lock()
	s.tools[callID] = track
	s.mu.Unlock()

	version := a.protocolVersion()
	update := obj{
		"toolCallId": callID,
		"name":       name,
		"title":      toolTitle(name, input, ""),
		"kind":       toolKind(name),
		"status":     "pending",
		"locations":  toolLocations(name, input, s.cwd),
		"rawInput":   toolRawInput(name, input, s.cwd),
	}
	if version == ProtocolV1 {
		update["sessionUpdate"] = "tool_call"
		a.update(s.id, update)
		return
	}
	update["sessionUpdate"] = "tool_call_update"
	if strings.EqualFold(name, "bash") {
		// v2 shows command output in an agent-owned display terminal
		// (protocol/v2/migration "Agent-owned terminal display").
		terminalID := "term_" + callID
		track.terminalID = terminalID
		update["content"] = []obj{{"type": "terminal", "terminalId": terminalID}}
		a.update(s.id, update)
		command, _ := input["command"].(string)
		workdir := s.cwd
		if dir, _ := input["workdir"].(string); dir != "" {
			workdir = absolute(dir, s.cwd)
		}
		a.update(s.id, obj{
			"sessionUpdate": "terminal_update",
			"terminalId":    terminalID,
			"command":       command,
			"cwd":           workdir,
		})
		return
	}
	a.update(s.id, update)
}

// toolStarted marks a call in progress once it has been authorized, which is
// the last step before it runs.
func (a *Agent) toolStarted(s *acpSession, callID string) {
	s.mu.Lock()
	track := s.tools[callID]
	if track == nil || track.status != "pending" {
		s.mu.Unlock()
		return
	}
	track.status = "in_progress"
	s.mu.Unlock()
	a.update(s.id, obj{"sessionUpdate": "tool_call_update", "toolCallId": callID, "status": "in_progress"})
}

// markTerminal records the v1 client terminal a call's output is shown in,
// so its settlement keeps showing it rather than replacing it with text.
func (s *acpSession) markTerminal(callID, terminalID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if track := s.tools[callID]; track != nil {
		track.terminalID = terminalID
		track.status = "in_progress"
	}
}

var exitCodePattern = regexp.MustCompile(`exited with code (-?\d+)`)

// toolSettled reports a finished call: its status, its output as content,
// and — for an edit — the diff of what it changed.
func (a *Agent) toolSettled(s *acpSession, callID, output, failure string) {
	if callID == "" {
		return
	}
	s.mu.Lock()
	track := s.tools[callID]
	if track == nil {
		track = &toolTrack{callID: callID}
	}
	delete(s.tools, callID)
	s.mu.Unlock()
	version := a.protocolVersion()

	status := "completed"
	if failure != "" {
		status = "failed"
		if version == ProtocolV2 && strings.Contains(failure, "interrupted") {
			// v2 distinguishes a call that never finished because the work
			// was cancelled (protocol/v2/migration "Tool calls").
			status = "cancelled"
		}
	}
	update := obj{"sessionUpdate": "tool_call_update", "toolCallId": callID, "status": status}

	var content []obj
	if track.terminalID != "" {
		content = append(content, obj{"type": "terminal", "terminalId": track.terminalID})
		if version == ProtocolV2 {
			a.settleDisplayTerminal(s, track, output, failure)
		}
	} else if failure != "" {
		content = append(content, textContent(failure))
	} else if output != "" {
		content = append(content, textContent(output))
	}
	if failure == "" {
		if changes := track.changes(); len(changes) > 0 {
			if version == ProtocolV1 {
				content = append(content, diffV1(changes)...)
			} else {
				content = append(content, diffV2(changes)...)
			}
		}
	}
	if len(content) > 0 {
		update["content"] = content
	}
	if failure != "" {
		update["rawOutput"] = obj{"error": failure}
	} else {
		update["rawOutput"] = obj{"output": output}
	}
	a.update(s.id, update)

	if failure == "" && strings.EqualFold(track.name, "todowrite") {
		a.planUpdate(s)
	}
}

// settleDisplayTerminal writes a finished shell call's output and exit into
// its v2 display terminal. Output goes as one independently base64-encoded
// chunk (protocol/v2/migration "Agent-owned terminal display").
func (a *Agent) settleDisplayTerminal(s *acpSession, track *toolTrack, output, failure string) {
	text := output
	if failure != "" {
		text = failure
	}
	if text != "" {
		a.update(s.id, obj{
			"sessionUpdate": "terminal_output_chunk",
			"terminalId":    track.terminalID,
			"data":          base64.StdEncoding.EncodeToString([]byte(text)),
		})
	}
	exit := obj{"exitCode": 0}
	if failure != "" {
		if match := exitCodePattern.FindStringSubmatch(failure); match != nil {
			code, _ := strconv.Atoi(match[1])
			exit = obj{"exitCode": code}
		} else if strings.Contains(failure, "timed out") || strings.Contains(failure, "interrupted") {
			exit = obj{"exitCode": nil, "signal": "SIGKILL"}
		} else {
			exit = obj{"exitCode": 1}
		}
	}
	a.update(s.id, obj{
		"sessionUpdate": "terminal_update",
		"terminalId":    track.terminalID,
		"exitStatus":    exit,
	})
}

// planUpdate reports the session's todo list as its plan. The whole list goes
// every time; the client replaces the plan it holds (protocol/v1/agent-plan
// "Updating Plans").
func (a *Agent) planUpdate(s *acpSession) {
	todos, err := s.runtime.Sessions.Todos(context.Background(), s.id)
	if err != nil {
		return
	}
	entries := make([]obj, 0, len(todos))
	for _, todo := range todos {
		entries = append(entries, obj{
			"content":  todo.Content,
			"priority": planPriority(todo.Priority),
			"status":   planStatus(todo.Status, a.protocolVersion()),
		})
	}
	if a.protocolVersion() == ProtocolV1 {
		a.update(s.id, obj{"sessionUpdate": "plan", "entries": entries})
		return
	}
	a.update(s.id, obj{
		"sessionUpdate": "plan_update",
		"plan":          obj{"type": "items", "planId": planID, "entries": entries},
	})
}

// planID names the one plan a session has: its todo list.
const planID = "todo"

func planPriority(priority string) string {
	switch priority {
	case "high", "medium", "low":
		return priority
	}
	return "medium"
}

func planStatus(status string, version int) string {
	switch status {
	case "pending", "in_progress", "completed":
		return status
	case "cancelled":
		// v1's entry status has no cancelled; a dropped item is done with.
		if version == ProtocolV2 {
			return "cancelled"
		}
		return "completed"
	}
	return "pending"
}

// agentSwitched reports a mode change the agent made itself (plan_exit, a
// command's agent): v1 hears it as a mode update, both as the config option.
func (a *Agent) agentSwitched(s *acpSession, agentID string) {
	s.mu.Lock()
	if s.agentID == agentID {
		s.mu.Unlock()
		return
	}
	s.agentID = agentID
	s.mu.Unlock()
	if a.protocolVersion() == ProtocolV1 {
		a.update(s.id, obj{"sessionUpdate": "current_mode_update", "currentModeId": agentID})
	}
	a.update(s.id, obj{
		"sessionUpdate": "config_option_update",
		"configOptions": a.configOptions(context.Background(), s),
	})
}

// usageUpdate reports context use after a step: the tokens the step's
// request occupied against the model's window, and the session's cost so
// far. Ports sendUsageUpdate in packages/opencode/src/acp/service.ts.
func (a *Agent) usageUpdate(s *acpSession, data map[string]any) {
	tokens, _ := data["tokens"].(map[string]any)
	if tokens == nil {
		return
	}
	number := func(m map[string]any, key string) int {
		switch value := m[key].(type) {
		case float64:
			return int(value)
		case int:
			return value
		case int64:
			return int(value)
		}
		return 0
	}
	cache, _ := tokens["cache"].(map[string]any)
	used := number(tokens, "input") + number(tokens, "output") + number(tokens, "reasoning") +
		number(cache, "read") + number(cache, "write")
	s.mu.Lock()
	model := s.stepModel
	s.mu.Unlock()
	size := a.contextLimit(s, model[0], model[1])
	if size <= 0 {
		return
	}
	update := obj{"sessionUpdate": "usage_update", "used": used, "size": size}
	if stats, err := s.runtime.Sessions.Stats(context.Background(), s.id); err == nil {
		if cost, ok := stats["cost"].(float64); ok {
			update["cost"] = obj{"amount": cost, "currency": "USD"}
		}
	}
	a.update(s.id, update)
}

// contextLimit is the model's context window, from the model list.
func (a *Agent) contextLimit(s *acpSession, providerID, modelID string) int {
	if s.runtime.Models == nil {
		return 0
	}
	for _, model := range s.runtime.Models(context.Background()) {
		if model.ProviderID == providerID && model.ID == modelID {
			return model.ContextLimit
		}
	}
	return 0
}

// afterTurn reports what a finished turn may have changed about the session
// itself: a title generated from its first prompt.
func (a *Agent) afterTurn(ctx context.Context, s *acpSession) {
	info, err := s.runtime.Sessions.Get(ctx, s.id)
	if err != nil || info == nil {
		return
	}
	s.mu.Lock()
	changed := info.Title != s.title
	s.title = info.Title
	s.mu.Unlock()
	if !changed {
		return
	}
	a.update(s.id, obj{
		"sessionUpdate": "session_info_update",
		"title":         info.Title,
		"updatedAt":     time.UnixMilli(info.TimeUpdated).UTC().Format(time.RFC3339),
	})
}

func newMessageID() (string, error) {
	return id.Ascending(id.KindMessage)
}
