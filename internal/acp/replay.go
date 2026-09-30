package acp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"

	"github.com/langazov/gocode-go/internal/session"
)

// replay streams a session's retained history to the client as ordinary
// session updates, before the load/resume response (protocol/v1/session-setup
// "Loading Sessions", protocol/v2/session-setup "Resuming Sessions"). Message
// ids are the stored ones, so a replayed user message carries the id its
// prompt response returned.
//
// v1 replays messages as chunks, which is all it has. v2 replays each message
// as one whole-message upsert, which also clears whatever the client held for
// that id — replay is idempotent. Tool calls are replayed as a single update
// in their final state; the last todo list as the plan.
func (a *Agent) replay(ctx context.Context, s *acpSession) error {
	messages, err := s.runtime.Sessions.Messages.List(ctx, s.id)
	if err != nil {
		return err
	}
	v2 := a.protocolVersion() == ProtocolV2
	for _, message := range messages {
		switch message.Type {
		case session.TypeUser:
			var user session.UserMessage
			if json.Unmarshal(message.Data, &user) != nil {
				continue
			}
			a.replayUser(s, message.ID, user, v2)
		case session.TypeAssistant:
			assistant, err := session.DecodeAssistant(message.Data)
			if err != nil {
				continue
			}
			a.replayAssistant(s, assistant, v2)
		}
	}
	if todos, err := s.runtime.Sessions.Todos(ctx, s.id); err == nil && len(todos) > 0 {
		a.planUpdate(s)
	}
	return nil
}

func (a *Agent) replayUser(s *acpSession, messageID string, user session.UserMessage, v2 bool) {
	var content []obj
	if user.Text != "" {
		content = append(content, obj{"type": "text", "text": user.Text})
	}
	for _, file := range user.Files {
		if mime, data, ok := splitDataURI(file.URI); ok && strings.HasPrefix(mime, "image/") {
			content = append(content, obj{"type": "image", "mimeType": mime, "data": data})
			continue
		}
		if file.URI != "" {
			content = append(content, obj{"type": "resource_link", "uri": file.URI, "name": firstNonEmpty(file.Name, file.URI)})
		}
	}
	if v2 {
		a.update(s.id, obj{"sessionUpdate": "user_message", "messageId": messageID, "content": content})
		return
	}
	for _, block := range content {
		a.update(s.id, obj{"sessionUpdate": "user_message_chunk", "messageId": messageID, "content": block})
	}
}

func (a *Agent) replayAssistant(s *acpSession, assistant session.AssistantMessage, v2 bool) {
	for _, part := range assistant.Content {
		switch part.Type {
		case "text", "reasoning":
			if part.Text == "" {
				continue
			}
			kind := "agent_message"
			if part.Type == "reasoning" {
				kind = "agent_thought"
			}
			if v2 {
				a.update(s.id, obj{
					"sessionUpdate": kind,
					"messageId":     part.ID,
					"content":       []obj{{"type": "text", "text": part.Text}},
				})
				continue
			}
			a.update(s.id, obj{
				"sessionUpdate": kind + "_chunk",
				"messageId":     part.ID,
				"content":       obj{"type": "text", "text": part.Text},
			})
		case "tool":
			a.replayTool(s, part, v2)
		}
	}
}

// replayTool reports a stored tool call in its final state.
func (a *Agent) replayTool(s *acpSession, part session.AssistantContent, v2 bool) {
	var input map[string]any
	status, output, failure, title := "pending", "", "", ""
	if part.State != nil {
		input = part.State.Input
		output, failure, title = part.State.Output, part.State.Error, part.State.Title
		switch part.State.Status {
		case session.ToolCompleted:
			status = "completed"
		case session.ToolError:
			status = "failed"
		case session.ToolRunning:
			status = "in_progress"
		}
	}
	update := obj{
		"toolCallId": part.ID,
		"name":       part.Name,
		"title":      toolTitle(part.Name, input, title),
		"kind":       toolKind(part.Name),
		"status":     status,
		"locations":  toolLocations(part.Name, input, s.cwd),
		"rawInput":   toolRawInput(part.Name, input, s.cwd),
	}
	var content []obj
	switch {
	case failure != "":
		content = append(content, textContent(failure))
		update["rawOutput"] = obj{"error": failure}
	case output != "":
		content = append(content, textContent(output))
		update["rawOutput"] = obj{"output": output}
	}
	if !v2 && status == "completed" {
		// The before-image of an edit is gone; the edit's own strings are the
		// diff v1 can still show (tool.ts diffContent does the same).
		content = append(content, replayDiffV1(part.Name, input, s.cwd)...)
	}

	if v2 && strings.EqualFold(part.Name, "bash") {
		// A display terminal is replayed as one snapshot, not the chunks it
		// was built from (protocol/v2/migration checklist "Replay").
		terminalID := "term_" + part.ID
		command, _ := input["command"].(string)
		workdir := s.cwd
		if dir, _ := input["workdir"].(string); dir != "" {
			workdir = absolute(dir, s.cwd)
		}
		terminal := obj{
			"sessionUpdate": "terminal_update",
			"terminalId":    terminalID,
			"command":       command,
			"cwd":           workdir,
			"output":        obj{"data": base64.StdEncoding.EncodeToString([]byte(firstNonEmpty(output, failure)))},
		}
		switch status {
		case "completed":
			terminal["exitStatus"] = obj{"exitCode": 0}
		case "failed":
			terminal["exitStatus"] = obj{"exitCode": 1}
		}
		content = []obj{{"type": "terminal", "terminalId": terminalID}}
		update["content"] = content
		update["sessionUpdate"] = "tool_call_update"
		a.update(s.id, update)
		a.update(s.id, terminal)
		return
	}

	if len(content) > 0 {
		update["content"] = content
	}
	if v2 {
		update["sessionUpdate"] = "tool_call_update"
	} else {
		update["sessionUpdate"] = "tool_call"
	}
	a.update(s.id, update)
}

func replayDiffV1(name string, input map[string]any, cwd string) []obj {
	path, _ := input["path"].(string)
	if path == "" {
		return nil
	}
	switch strings.ToLower(name) {
	case "edit":
		oldText, _ := input["oldString"].(string)
		newText, _ := input["newString"].(string)
		return []obj{{"type": "diff", "path": absolute(path, cwd), "oldText": oldText, "newText": newText}}
	case "write":
		newText, _ := input["content"].(string)
		return []obj{{"type": "diff", "path": absolute(path, cwd), "oldText": nil, "newText": newText}}
	}
	return nil
}

// splitDataURI splits "data:<mime>;base64,<data>".
func splitDataURI(uri string) (mime, data string, ok bool) {
	rest, found := strings.CutPrefix(uri, "data:")
	if !found {
		return "", "", false
	}
	head, payload, found := strings.Cut(rest, ",")
	if !found {
		return "", "", false
	}
	mime, found = strings.CutSuffix(head, ";base64")
	if !found {
		return "", "", false
	}
	return mime, payload, true
}
