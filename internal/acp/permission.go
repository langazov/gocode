package acp

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/langazov/gocode-go/internal/mcp"
	"github.com/langazov/gocode-go/internal/permission"
	"github.com/langazov/gocode-go/internal/question"
	"github.com/langazov/gocode-go/internal/session"
)

// gate wraps a runtime's permission gate. It adds two things the connection
// needs: a tool call becomes in_progress the moment it is authorized (the
// runner's last step before execution), and a session whose client turned on
// auto-approve has its asks answered "once" — configured denies still hold,
// exactly as under --auto (session.AutoAnswerGate).
type gate struct {
	agent   *Agent
	runtime *Runtime
	inner   session.PermissionGate
}

func (g *gate) Assert(ctx context.Context, input session.ToolPermissionInput) error {
	a := g.agent
	a.mu.Lock()
	s := a.sessions[input.SessionID]
	a.mu.Unlock()

	var err error
	switch {
	case s != nil && s.autoApproving():
		err = (&session.AutoAnswerGate{Engine: g.runtime.Permissions}).Assert(ctx, input)
	case g.inner != nil:
		err = g.inner.Assert(ctx, input)
	}
	if err == nil && s != nil {
		a.toolStarted(s, input.CallID)
	}
	return err
}

// Denied keeps configured denies ahead of every other answer, forwarding the
// inner gate's rule check (session.PermissionRuled).
func (g *gate) Denied(input session.ToolPermissionInput) error {
	if ruled, ok := g.inner.(session.PermissionRuled); ok {
		return ruled.Denied(input)
	}
	return nil
}

func (s *acpSession) autoApproving() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.autoApprove
}

// enqueue runs job on the session's serial queue. Asks are answered one at a
// time per session, in the order they were raised, so a client never has two
// prompts for the same session racing each other.
func (s *acpSession) enqueue(job func()) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.permissionQueue == nil {
		return false
	}
	select {
	case s.permissionQueue <- job:
		return true
	default:
		return false
	}
}

// Permission options offered for every ask. Ports permissionOptions in
// packages/opencode/src/acp/permission.ts.
const (
	optionOnce   = "once"
	optionAlways = "always"
	optionReject = "reject"
)

func permissionOptions(canRemember bool) []obj {
	options := []obj{{"optionId": optionOnce, "name": "Allow once", "kind": "allow_once"}}
	if canRemember {
		options = append(options, obj{"optionId": optionAlways, "name": "Always allow", "kind": "allow_always"})
	}
	return append(options, obj{"optionId": optionReject, "name": "Reject", "kind": "reject_once"})
}

// enqueuePermission asks the client about a pending permission request.
func (a *Agent) enqueuePermission(s *acpSession, requestID string) {
	rt := s.runtime
	queued := s.enqueue(func() { a.askPermission(s, requestID) })
	if !queued {
		// The session is closing or hopelessly backed up: a request nobody
		// can answer must not park the turn forever.
		_ = rt.Permissions.Reply(requestID, permission.ReplyReject, "")
	}
}

func (a *Agent) askPermission(s *acpSession, requestID string) {
	rt := s.runtime
	request := rt.Permissions.Get(requestID)
	if request == nil {
		return // answered elsewhere (another client, a cascade, a cancel)
	}
	a.setState(s, "requires_action", "")
	defer func() {
		if s.busy() {
			a.setState(s, "running", "")
		}
	}()

	params := a.permissionParams(s, *request)
	var result struct {
		Outcome struct {
			Outcome  string `json:"outcome"`
			OptionID string `json:"optionId"`
		} `json:"outcome"`
	}
	err := a.call(context.Background(), "session/request_permission", params, &result)
	reply := permission.ReplyReject
	// Anything but an explicitly selected allow option is a refusal: a
	// cancelled prompt, a transport error, or an outcome this agent does not
	// know MUST NOT be treated as approval (protocol/v2/migration
	// "Permission requests").
	if err == nil && result.Outcome.Outcome == "selected" {
		switch result.Outcome.OptionID {
		case optionOnce:
			reply = permission.ReplyOnce
		case optionAlways:
			reply = permission.ReplyAlways
		}
	}
	if err != nil {
		a.host.Log("acp: request_permission for %s: %v", requestID, err)
	}
	if replyErr := rt.Permissions.Reply(requestID, reply, ""); replyErr != nil {
		a.host.Log("acp: replying to permission %s: %v", requestID, replyErr)
	}
}

// permissionParams builds session/request_permission for the negotiated
// version. v1 carries the prompt in a tool-call patch; v2 separates the
// prompt copy (title, description) from its subject.
func (a *Agent) permissionParams(s *acpSession, request permission.Request) obj {
	title := permissionTitle(request)
	callID := ""
	if request.Source != nil && request.SessionID == s.id {
		callID = request.Source.CallID
	}
	toolCall := obj{
		"toolCallId": firstNonEmpty(callID, "permission_"+request.ID),
		"status":     "pending",
	}
	if callID == "" {
		// A synthetic call for an ask the client has no tool call for (a
		// subagent's, or one raised outside a call) needs its own label.
		toolCall["title"] = title
		toolCall["kind"] = permissionKind(request.Action)
	}
	if diff, _ := request.Metadata["diff"].(string); diff != "" {
		toolCall["content"] = []obj{textContent("```diff\n" + strings.TrimRight(diff, "\n") + "\n```")}
	}
	if len(request.Resources) > 0 && request.Action != "bash" {
		var locations []obj
		for _, resource := range request.Resources {
			if strings.HasPrefix(resource, "/") && !strings.ContainsAny(resource, "*?") {
				locations = append(locations, obj{"path": resource})
			}
		}
		if len(locations) > 0 {
			toolCall["locations"] = locations
		}
	}
	options := permissionOptions(len(request.Save) > 0)
	params := obj{"sessionId": s.id, "options": options}

	if a.protocolVersion() == ProtocolV1 {
		if callID != "" {
			// v1 clients render the prompt from the call itself; its title
			// is the question being asked.
			toolCall["title"] = title
		}
		params["toolCall"] = toolCall
		return params
	}
	params["title"] = title
	if request.Agent != "" && request.SessionID != s.id {
		params["description"] = fmt.Sprintf("Requested by the %s subagent.", request.Agent)
	}
	if request.Action == "bash" {
		command, _ := request.Metadata["command"].(string)
		if command == "" && len(request.Resources) > 0 {
			command = request.Resources[0]
		}
		subject := obj{"type": "command", "command": command, "cwd": s.cwd}
		if callID != "" {
			subject["toolCallId"] = callID
			subject["terminalId"] = "term_" + callID
		}
		params["subject"] = subject
		return params
	}
	params["subject"] = obj{"type": "tool_call", "toolCall": toolCall}
	return params
}

// permissionTitle is the prompt the user answers.
func permissionTitle(request permission.Request) string {
	resources := strings.Join(request.Resources, ", ")
	switch request.Action {
	case "edit":
		return "Allow editing " + resources + "?"
	case "bash":
		command, _ := request.Metadata["command"].(string)
		if command == "" {
			command = resources
		}
		return "Allow running `" + command + "`?"
	case "external_directory":
		return "Allow access outside the project: " + resources + "?"
	case "webfetch":
		return "Allow fetching " + resources + "?"
	case "read":
		return "Allow reading " + resources + "?"
	case "task":
		return "Allow starting the " + resources + " subagent?"
	}
	if resources == "" || resources == "*" {
		return "Allow " + request.Action + "?"
	}
	return "Allow " + request.Action + " on " + resources + "?"
}

func permissionKind(action string) string {
	switch action {
	case "external_directory":
		return "read"
	}
	return toolKind(action)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

// enqueueQuestion asks the client a pending question (the question tool,
// plan_enter/plan_exit).
func (a *Agent) enqueueQuestion(s *acpSession, requestID string) {
	rt := s.runtime
	if !s.enqueue(func() { a.askQuestion(s, requestID) }) {
		_ = rt.Questions.Reject(requestID)
	}
}

func (a *Agent) askQuestion(s *acpSession, requestID string) {
	rt := s.runtime
	var request *question.Request
	for _, pending := range rt.Questions.List() {
		if pending.ID == requestID {
			request = &pending
			break
		}
	}
	if request == nil {
		return
	}
	a.setState(s, "requires_action", "")
	defer func() {
		if s.busy() {
			a.setState(s, "running", "")
		}
	}()

	caps := a.clientCapabilities()
	var answers []question.Answer
	var ok bool
	if caps.elicitForm {
		answers, ok = a.elicitAnswers(s, *request)
	} else {
		answers, ok = a.permissionAnswers(s, *request)
	}
	if !ok {
		_ = rt.Questions.Reject(requestID)
		return
	}
	if err := rt.Questions.Reply(requestID, answers); err != nil {
		a.host.Log("acp: answering question %s: %v", requestID, err)
	}
}

// elicitAnswers asks every question of a request as one form
// (protocol/v1/elicitation "Form mode"): a single choice is a string enum, a
// multiple choice an array of them.
func (a *Agent) elicitAnswers(s *acpSession, request question.Request) ([]question.Answer, bool) {
	properties := obj{}
	required := make([]string, 0, len(request.Questions))
	var message strings.Builder
	for i, prompt := range request.Questions {
		key := fmt.Sprintf("q%d", i+1)
		labels := make([]string, 0, len(prompt.Options))
		for _, option := range prompt.Options {
			labels = append(labels, option.Label)
		}
		title := firstNonEmpty(prompt.Header, prompt.Question)
		var property obj
		if prompt.Multiple {
			property = obj{
				"type":        "array",
				"title":       title,
				"description": prompt.Question,
				"items":       obj{"type": "string", "enum": labels},
			}
		} else {
			property = obj{"type": "string", "title": title, "description": prompt.Question}
			if len(labels) > 0 {
				property["enum"] = labels
			}
		}
		properties[key] = property
		required = append(required, key)
		if message.Len() > 0 {
			message.WriteString("\n")
		}
		message.WriteString(prompt.Question)
	}
	params := obj{
		"sessionId": s.id,
		"mode":      "form",
		"message":   message.String(),
		"requestedSchema": obj{
			"type":       "object",
			"properties": properties,
			"required":   required,
		},
	}
	if request.Source != nil && request.Source.CallID != "" && request.SessionID == s.id {
		params["toolCallId"] = request.Source.CallID
	}
	var result struct {
		Action  string         `json:"action"`
		Content map[string]any `json:"content"`
	}
	if err := a.call(context.Background(), "elicitation/create", params, &result); err != nil {
		a.host.Log("acp: elicitation for question %s: %v", request.ID, err)
		return nil, false
	}
	if result.Action != "accept" {
		return nil, false
	}
	answers := make([]question.Answer, len(request.Questions))
	for i := range request.Questions {
		switch value := result.Content[fmt.Sprintf("q%d", i+1)].(type) {
		case string:
			answers[i] = question.Answer{value}
		case []any:
			for _, item := range value {
				if text, ok := item.(string); ok {
					answers[i] = append(answers[i], text)
				}
			}
		}
	}
	return answers, true
}

// permissionAnswers is the fallback for a client without form elicitation:
// each single-choice question becomes a permission prompt whose options are
// its choices. A multiple-choice question cannot be expressed that way and
// is declined — the agent MUST NOT assume an answer.
func (a *Agent) permissionAnswers(s *acpSession, request question.Request) ([]question.Answer, bool) {
	answers := make([]question.Answer, 0, len(request.Questions))
	for i, prompt := range request.Questions {
		if prompt.Multiple || len(prompt.Options) == 0 {
			return nil, false
		}
		options := make([]obj, 0, len(prompt.Options)+1)
		for j, option := range prompt.Options {
			options = append(options, obj{"optionId": fmt.Sprintf("choice-%d", j), "name": option.Label, "kind": "allow_once"})
		}
		options = append(options, obj{"optionId": "dismiss", "name": "Dismiss", "kind": "reject_once"})
		callID := fmt.Sprintf("question_%s_%d", request.ID, i+1)
		toolCall := obj{"toolCallId": callID, "title": prompt.Question, "kind": "other", "status": "pending"}
		params := obj{"sessionId": s.id, "options": options}
		if a.protocolVersion() == ProtocolV1 {
			params["toolCall"] = toolCall
		} else {
			params["title"] = prompt.Question
			if prompt.Header != "" {
				params["description"] = prompt.Header
			}
		}
		var result struct {
			Outcome struct {
				Outcome  string `json:"outcome"`
				OptionID string `json:"optionId"`
			} `json:"outcome"`
		}
		if err := a.call(context.Background(), "session/request_permission", params, &result); err != nil {
			return nil, false
		}
		if result.Outcome.Outcome != "selected" {
			return nil, false
		}
		var index int
		if _, err := fmt.Sscanf(result.Outcome.OptionID, "choice-%d", &index); err != nil || index < 0 || index >= len(prompt.Options) {
			return nil, false
		}
		answers = append(answers, question.Answer{prompt.Options[index].Label})
	}
	return answers, true
}

// authorizeMCP runs an MCP server's OAuth flow through URL-mode elicitation,
// when the client offers it: the client shows the authorization URL and asks
// the user's consent to open it; the agent completes the flow out of band and
// then reports completion (protocol/v1/elicitation "URL mode").
func (a *Agent) authorizeMCP(ctx context.Context, s *acpSession, name string, config mcp.ServerConfig) {
	if !a.clientCapabilities().elicitURL {
		return
	}
	elicitationID := "mcp-" + s.id + "-" + name
	a.mu.Lock()
	if a.elicitations[elicitationID] {
		a.mu.Unlock()
		return
	}
	a.elicitations[elicitationID] = true
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		delete(a.elicitations, elicitationID)
		a.mu.Unlock()
	}()

	opened := make(chan bool, 1)
	onAuthURL := func(authURL string) {
		if _, err := url.Parse(authURL); err != nil {
			opened <- false
			return
		}
		var result struct {
			Action string `json:"action"`
		}
		err := a.call(ctx, "elicitation/create", obj{
			"sessionId":     s.id,
			"mode":          "url",
			"elicitationId": elicitationID,
			"url":           authURL,
			"message":       fmt.Sprintf("Authorize gocode to use the %q MCP server.", strings.TrimPrefix(name, "acp-")),
		}, &result)
		opened <- err == nil && result.Action == "accept"
	}
	status, err := s.runtime.MCP.Authenticate(ctx, name, config, onAuthURL)
	select {
	case accepted := <-opened:
		if !accepted {
			return
		}
	default:
	}
	if err != nil || !status.Connected() {
		a.host.Log("acp: MCP authorization for %s: %v", name, err)
		return
	}
	a.notify("elicitation/complete", obj{"elicitationId": elicitationID})
}
