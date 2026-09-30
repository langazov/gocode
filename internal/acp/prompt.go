package acp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"sync"

	"github.com/langazov/gocode-go/internal/command"
	"github.com/langazov/gocode-go/internal/jsonrpc"
	"github.com/langazov/gocode-go/internal/session"
)

// turn is one run of foreground work: from the prompt that starts it until
// the session drains. Prompts that arrive while it runs join it — they are
// inserted into the running conversation (steered), and all of them settle
// with it.
type turn struct {
	done chan struct{}
	// cancelled is set by session/cancel, so a turn that ends because of it
	// reports the cancelled stop reason whatever the runner recorded.
	mu         sync.Mutex
	cancelled  bool
	stopReason string
	err        error
	// firstMessage is the id of the user message that opened the turn:
	// assistant messages after it belong to this turn.
	firstMessage string
}

func (t *turn) cancel() {
	t.mu.Lock()
	t.cancelled = true
	t.mu.Unlock()
}

func (t *turn) result() (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.stopReason, t.err
}

// contentBlock is an inbound ContentBlock (protocol/v1/content). The union is
// flattened: every variant's fields, discriminated by Type.
type contentBlock struct {
	Type     string `json:"type"`
	Text     string `json:"text"`
	Data     string `json:"data"`
	MimeType string `json:"mimeType"`
	URI      string `json:"uri"`
	Name     string `json:"name"`
	Title    string `json:"title"`
	Resource *struct {
		URI      string  `json:"uri"`
		MimeType string  `json:"mimeType"`
		Text     *string `json:"text"`
		Blob     *string `json:"blob"`
	} `json:"resource"`
}

// promptInput converts the client's content blocks into a gocode prompt.
//
//   - text is joined in order;
//   - an image becomes a data: attachment, the one form the providers take
//     (session/to_llm.go attachmentParts);
//   - an embedded text resource is inlined as a fenced block labelled with
//     its URI — it is the file's contents as the editor has them, which may
//     be unsaved, so it must reach the model verbatim; a blob that is an
//     image becomes an attachment, any other blob a note;
//   - a resource link is referenced by path, for the model to read with its
//     own tools (the baseline every agent must accept).
//
// Blocks the negotiated prompt capabilities exclude are refused, as the
// client MUST NOT send them (protocol/v1/prompt-turn "User Message").
func promptInput(blocks []contentBlock, cwd string) (session.Prompt, []contentBlock, error) {
	var prompt session.Prompt
	var text strings.Builder
	appendText := func(value string) {
		if value == "" {
			return
		}
		if text.Len() > 0 && !strings.HasSuffix(text.String(), "\n") {
			text.WriteString("\n")
		}
		text.WriteString(value)
	}
	for _, block := range blocks {
		switch block.Type {
		case "text":
			if text.Len() > 0 && !strings.HasSuffix(text.String(), "\n") && !strings.HasPrefix(block.Text, "\n") {
				text.WriteString(" ")
			}
			text.WriteString(block.Text)
		case "image":
			if block.Data == "" || block.MimeType == "" {
				return prompt, nil, invalidParams("image content needs data and mimeType")
			}
			prompt.Files = append(prompt.Files, session.FileAttachment{
				URI:  "data:" + block.MimeType + ";base64," + block.Data,
				Mime: block.MimeType,
				Name: nameFromURI(block.URI, "image"),
			})
		case "audio":
			return prompt, nil, invalidParams("audio content is not supported by this agent")
		case "resource":
			if block.Resource == nil {
				return prompt, nil, invalidParams("resource content needs a resource")
			}
			resource := block.Resource
			label := displayPath(resource.URI, cwd)
			switch {
			case resource.Text != nil:
				appendText(fmt.Sprintf("<file path=%q>\n%s\n</file>", label, *resource.Text))
			case resource.Blob != nil && strings.HasPrefix(resource.MimeType, "image/"):
				prompt.Files = append(prompt.Files, session.FileAttachment{
					URI:  "data:" + resource.MimeType + ";base64," + *resource.Blob,
					Mime: resource.MimeType,
					Name: nameFromURI(resource.URI, "image"),
				})
			default:
				appendText(fmt.Sprintf("[attached binary resource %s (%s) — not readable as text]", label, resource.MimeType))
			}
		case "resource_link":
			if block.URI == "" {
				return prompt, nil, invalidParams("resource_link content needs a uri")
			}
			appendText("@" + displayPath(block.URI, cwd))
		default:
			// Unknown block types are extension or future variants; a prompt
			// cannot be answered faithfully without them, so refuse rather
			// than silently drop part of the user's message.
			return prompt, nil, invalidParams("unsupported content type %q", block.Type)
		}
	}
	prompt.Text = text.String()
	return prompt, blocks, nil
}

// displayPath turns a file:// URI into a path, relative to cwd when inside
// it; any other URI is kept as is.
func displayPath(uri, cwd string) string {
	parsed, err := url.Parse(uri)
	if err != nil || parsed.Scheme != "file" {
		return uri
	}
	path := parsed.Path
	if rel, err := filepath.Rel(cwd, path); err == nil && !strings.HasPrefix(rel, "..") {
		return rel
	}
	return path
}

func nameFromURI(uri, fallback string) string {
	if uri == "" {
		return fallback
	}
	if parsed, err := url.Parse(uri); err == nil && parsed.Path != "" {
		return filepath.Base(parsed.Path)
	}
	return fallback
}

// slashCommand splits "/name args" off the prompt text. Ports
// detectSlashCommand in packages/opencode/src/acp/service.ts.
func slashCommand(text string) (name, args string, ok bool) {
	trimmed := strings.TrimSpace(text)
	if !strings.HasPrefix(trimmed, "/") {
		return "", "", false
	}
	name, args, _ = strings.Cut(trimmed[1:], " ")
	name = strings.TrimSpace(name)
	if name == "" || strings.ContainsAny(name, "/\\") {
		return "", "", false
	}
	return name, strings.TrimSpace(args), true
}

func (a *Agent) prompt(ctx context.Context, params json.RawMessage) (any, error) {
	var in struct {
		SessionID string         `json:"sessionId"`
		Prompt    []contentBlock `json:"prompt"`
	}
	if err := decode(params, &in); err != nil {
		return nil, err
	}
	s, err := a.session(in.SessionID)
	if err != nil {
		return nil, err
	}
	if len(in.Prompt) == 0 {
		return nil, invalidParams("prompt must not be empty")
	}
	input, blocks, err := promptInput(in.Prompt, s.cwd)
	if err != nil {
		return nil, err
	}
	rt := s.runtime

	// Slash commands: a known command expands to its template (applying its
	// agent and model); /compact compacts. Anything else is sent as typed.
	compact := false
	if name, args, ok := slashCommand(input.Text); ok {
		if name == "compact" {
			compact = true
		} else if cmd, found := rt.Commands.Get(name); found {
			input.Text = command.Expand(ctx, cmd.Template, args, "")
			if err := a.applyCommandOverrides(ctx, s, cmd); err != nil {
				return nil, err
			}
		}
	}

	if compact {
		return a.compactTurn(ctx, s, blocks)
	}

	// The turn is registered before the prompt is admitted: admission wakes
	// the session, and the run it starts must already count as this turn's
	// (see the RunStarted handling in translate) so the client hears about
	// the user message before the work it set off.
	t, started := s.joinTurn()
	messageID, err := rt.Sessions.PromptWith(ctx, s.id, input, session.DeliverySteer)
	if err != nil {
		if started {
			s.abandonTurn(t)
		}
		if errors.Is(err, session.ErrSessionNotFound) {
			return nil, sessionNotFound(s.id)
		}
		return nil, internalError("admitting prompt: %v", err)
	}
	if started {
		t.mu.Lock()
		t.firstMessage = messageID
		t.mu.Unlock()
	}
	if a.protocolVersion() == ProtocolV2 {
		// v2: the response acknowledges insertion; the user message, the
		// running state and completion all follow as updates
		// (protocol/v2/prompt-lifecycle).
		a.update(s.id, obj{
			"sessionUpdate": "user_message",
			"messageId":     messageID,
			"content":       outboundBlocks(blocks),
		})
		if started {
			a.setState(s, "running", "")
			go a.runTurn(s, t)
		}
		return obj{"messageId": messageID}, nil
	}

	// v1: the request stays open for the whole turn and its response is the
	// stop reason (protocol/v1/prompt-turn). The wait is deferred so the
	// connection keeps processing — session/cancel above all.
	if started {
		go a.runTurn(s, t)
	}
	return jsonrpc.Deferred(func() (any, error) {
		select {
		case <-t.done:
		case <-ctx.Done():
			// $/cancel_request on the prompt itself: treat it as
			// session/cancel, then still answer with the turn's outcome.
			a.cancel(s.id)
			<-t.done
		}
		reason, turnErr := t.result()
		if turnErr != nil {
			return nil, turnErr
		}
		return v1PromptResult(reason, messageID), nil
	}), nil
}

// v1PromptResult is the v1 session/prompt response. v1 has no field for the
// inserted message's id and forbids custom root fields, so it rides in _meta
// (protocol/v1/extensibility).
func v1PromptResult(reason, messageID string) obj {
	return obj{"stopReason": reason, "_meta": obj{"gocode": obj{"userMessageId": messageID}}}
}

// joinTurn returns the session's running turn, starting one when idle.
func (s *acpSession) joinTurn() (*turn, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.turn != nil {
		return s.turn, false
	}
	s.turn = &turn{done: make(chan struct{})}
	return s.turn, true
}

// abandonTurn drops a turn that never started because its prompt was
// refused.
func (s *acpSession) abandonTurn(t *turn) {
	s.mu.Lock()
	if s.turn == t {
		s.turn = nil
	}
	s.mu.Unlock()
	close(t.done)
}

// runTurn drives the session until it drains, then settles the turn.
func (a *Agent) runTurn(s *acpSession, t *turn) {
	rt := s.runtime
	ctx := context.Background()
	runErr := rt.Sessions.Execution.Resume(ctx, s.id)
	reason, err := a.stopReason(ctx, s, t, runErr)

	s.mu.Lock()
	s.turn = nil
	s.mu.Unlock()
	t.mu.Lock()
	t.stopReason, t.err = reason, err
	t.mu.Unlock()

	a.afterTurn(ctx, s)
	if a.protocolVersion() == ProtocolV2 {
		if err != nil {
			// v2 has no response left to fail: the failure is shown as agent
			// output and the work ends like any other.
			a.update(s.id, obj{
				"sessionUpdate": "agent_message",
				"messageId":     t.firstMessage + "-error",
				"content":       []obj{{"type": "text", "text": "Error: " + err.Error()}},
			})
			reason = "end_turn"
		}
		a.setState(s, "idle", reason)
	}
	close(t.done)
}

// compactTurn runs /compact as a turn of its own.
func (a *Agent) compactTurn(ctx context.Context, s *acpSession, blocks []contentBlock) (any, error) {
	messageID := "compact-" + fmt.Sprint(len(blocks))
	if id, err := newMessageID(); err == nil {
		messageID = id
	}
	if a.protocolVersion() == ProtocolV2 {
		a.update(s.id, obj{"sessionUpdate": "user_message", "messageId": messageID, "content": outboundBlocks(blocks)})
		a.setState(s, "running", "")
	}
	_, err := s.runtime.Sessions.CompactNow(context.WithoutCancel(ctx), s.id)
	a.afterTurn(ctx, s)
	if a.protocolVersion() == ProtocolV2 {
		if err != nil {
			a.update(s.id, obj{
				"sessionUpdate": "agent_message",
				"messageId":     messageID + "-error",
				"content":       []obj{{"type": "text", "text": "Compaction failed: " + err.Error()}},
			})
		}
		a.setState(s, "idle", "end_turn")
		return obj{"messageId": messageID}, nil
	}
	if err != nil {
		return nil, internalError("compaction failed: %v", err)
	}
	return v1PromptResult("end_turn", messageID), nil
}

// stopReason decides why a turn ended, from the run's error and the last
// assistant message it produced. Ports promptResponse in
// packages/opencode/src/acp/service.ts: an aborted message is cancelled, an
// output-length stop is max_tokens, a content filter is refusal, a provider
// auth failure is auth_required, any other error fails the prompt.
func (a *Agent) stopReason(ctx context.Context, s *acpSession, t *turn, runErr error) (string, error) {
	t.mu.Lock()
	cancelled := t.cancelled
	t.mu.Unlock()
	if cancelled || errors.Is(runErr, context.Canceled) {
		// Cancellation is not an error: clients rely on the cancelled stop
		// reason to confirm it (protocol/v1/prompt-turn "Cancellation").
		return "cancelled", nil
	}
	messages, err := s.runtime.Sessions.Messages.List(ctx, s.id)
	if err != nil {
		return "", internalError("reading session: %v", err)
	}
	var last *session.AssistantMessage
	steps := 0
	counting := false
	for _, message := range messages {
		if message.ID == t.firstMessage {
			counting = true
		}
		if message.Type != session.TypeAssistant {
			continue
		}
		decoded, err := session.DecodeAssistant(message.Data)
		if err != nil {
			continue
		}
		if counting {
			steps++
		}
		last = &decoded
	}
	if last != nil && last.Error != nil {
		switch {
		case last.Error.Type == session.ErrorTypeAborted:
			return "cancelled", nil
		case isAuthFailure(last.Error.Message):
			return "", authRequired(last.Error.Message)
		default:
			return "", internalError("%s", last.Error.Message)
		}
	}
	if runErr != nil {
		if isAuthFailure(runErr.Error()) {
			return "", authRequired(runErr.Error())
		}
		return "", internalError("%v", runErr)
	}
	if last != nil {
		switch last.Finish {
		case "length":
			return "max_tokens", nil
		case "content-filter":
			return "refusal", nil
		}
		if limit := a.stepLimit(s, last.Agent); limit > 0 && steps >= limit {
			return "max_turn_requests", nil
		}
	}
	return "end_turn", nil
}

// stepLimit is the agent's step budget, 0 when unbounded.
func (a *Agent) stepLimit(s *acpSession, agentID string) int {
	if agentID == "" {
		return 0
	}
	info, ok := s.runtime.Agents.Get(agentID)
	if !ok {
		return 0
	}
	return info.Steps
}

// isAuthFailure recognises a provider refusing the credentials, the case
// ACP reports as auth_required rather than a generic failure.
func isAuthFailure(message string) bool {
	lower := strings.ToLower(message)
	for _, marker := range []string{"401", "unauthorized", "invalid api key", "invalid x-api-key", "authentication", "no credentials", "api key"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

// cancel stops the session's foreground work: the drain is interrupted, and
// every ask it is parked on is withdrawn so the run can unwind. The turn then
// ends with the cancelled stop reason.
func (a *Agent) cancel(sessionID string) {
	s, err := a.session(sessionID)
	if err != nil {
		return
	}
	s.mu.Lock()
	t := s.turn
	s.mu.Unlock()
	if t != nil {
		t.cancel()
	}
	rt := s.runtime
	for _, request := range rt.Permissions.ForSession(sessionID) {
		_ = rt.Permissions.Reply(request.ID, "reject", "")
	}
	rt.Questions.RejectSession(sessionID)
	rt.Sessions.Interrupt(sessionID)
}

// applyCommandOverrides applies a command's agent and model before its
// prompt runs, as the command defines them.
func (a *Agent) applyCommandOverrides(ctx context.Context, s *acpSession, cmd command.Info) error {
	rt := s.runtime
	if cmd.Agent != "" {
		if _, ok := rt.Agents.Get(cmd.Agent); ok {
			if err := rt.Sessions.SetAgent(ctx, s.id, cmd.Agent); err != nil {
				return internalError("switching agent: %v", err)
			}
		}
	}
	if providerID, modelID, ok := strings.Cut(cmd.Model, "/"); ok && providerID != "" && modelID != "" {
		if err := rt.Sessions.SetModel(ctx, s.id, session.ModelRef{ProviderID: providerID, ID: modelID}); err != nil {
			return internalError("switching model: %v", err)
		}
	}
	return nil
}

// outboundBlocks echoes content blocks back to the client, keeping only the
// fields each variant defines.
func outboundBlocks(blocks []contentBlock) []obj {
	out := make([]obj, 0, len(blocks))
	for _, block := range blocks {
		switch block.Type {
		case "text":
			out = append(out, obj{"type": "text", "text": block.Text})
		case "image":
			entry := obj{"type": "image", "data": block.Data, "mimeType": block.MimeType}
			if block.URI != "" {
				entry["uri"] = block.URI
			}
			out = append(out, entry)
		case "resource_link":
			entry := obj{"type": "resource_link", "uri": block.URI, "name": block.Name}
			if block.Name == "" {
				entry["name"] = nameFromURI(block.URI, block.URI)
			}
			if block.MimeType != "" {
				entry["mimeType"] = block.MimeType
			}
			if block.Title != "" {
				entry["title"] = block.Title
			}
			out = append(out, entry)
		case "resource":
			if block.Resource == nil {
				continue
			}
			resource := obj{"uri": block.Resource.URI}
			if block.Resource.MimeType != "" {
				resource["mimeType"] = block.Resource.MimeType
			}
			if block.Resource.Text != nil {
				resource["text"] = *block.Resource.Text
			}
			if block.Resource.Blob != nil {
				resource["blob"] = *block.Resource.Blob
			}
			out = append(out, obj{"type": "resource", "resource": resource})
		}
	}
	return out
}
