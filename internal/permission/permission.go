// Package permission ports packages/core/src/permission.ts: wildcard rule
// evaluation and the ask/assert/reply flow with pending requests.
package permission

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/anomalyco/opencode-go/internal/id"
)

type Effect string

const (
	Allow Effect = "allow"
	Deny  Effect = "deny"
	Ask   Effect = "ask"
)

type Rule struct {
	Action   string `json:"action"`
	Resource string `json:"resource"`
	Effect   Effect `json:"effect"`
}

type Ruleset []Rule

var MissingAgentPermissions = Ruleset{{Action: "*", Resource: "*", Effect: Deny}}

// Defaults returns the baseline ruleset TS's agent.ts (agent.ts:119-136)
// merges into every agent — native or custom — that has no explicit
// permission config of its own: a "*": allow catch-all, so an unlisted
// action (edit, bash, todowrite, task, any MCP tool name, ...) is allowed
// by default rather than falling through to Evaluate's unmatched-rule
// "ask", plus the one read carve-out that's meaningful with this port's
// current tool set (reading a .env file asks; its .example counterpart is
// still allowed). TS's remaining defaults — doom_loop, external_directory,
// question, plan_enter/plan_exit — gate features/tools this port hasn't
// implemented yet (see specs/go-port-gaps.md); add their rules here
// alongside whichever of those lands first.
func Defaults() Ruleset {
	return Ruleset{
		{Action: "*", Resource: "*", Effect: Allow},
		// Reaching outside the working directory is always asked for, even
		// though everything else is allowed by default. Matching agent.ts's
		// `external_directory: {"*": "ask"}`, which sits beside the same
		// allow-all baseline.
		//
		// Without this the directory restriction on write/edit/apply_patch is
		// decorative: a shell command can write anywhere, and does.
		{Action: ExternalDirectoryAction, Resource: "*", Effect: Ask},
		{Action: "read", Resource: "*.env", Effect: Ask},
		{Action: "read", Resource: "*.env.*", Effect: Ask},
		{Action: "read", Resource: "*.env.example", Effect: Allow},
	}
}

// ExternalDirectoryAction gates a tool reaching outside the working
// directory. Defined here rather than in the shell tool so the permission
// defaults can name it without depending on the tool package.
const ExternalDirectoryAction = "external_directory"

// Evaluate returns the last matching rule across the rulesets, defaulting to
// ask, matching PermissionV2.evaluate.
func Evaluate(action, resource string, rulesets ...Ruleset) Rule {
	var match *Rule
	for _, ruleset := range rulesets {
		for i := range ruleset {
			rule := ruleset[i]
			if Match(action, rule.Action) && Match(resource, rule.Resource) {
				match = &rule
			}
		}
	}
	if match == nil {
		return Rule{Action: action, Resource: "*", Effect: Ask}
	}
	return *match
}

func Merge(rulesets ...Ruleset) Ruleset {
	var out Ruleset
	for _, ruleset := range rulesets {
		out = append(out, ruleset...)
	}
	return out
}

type Source struct {
	Type      string `json:"type"`
	MessageID string `json:"messageID,omitempty"`
	CallID    string `json:"callID,omitempty"`
}

type Request struct {
	ID        string `json:"id"`
	SessionID string `json:"sessionID"`
	// Agent names the agent that asked. With subagents running concurrently,
	// several sessions can have prompts pending at once, so the UI needs to
	// say who is asking.
	Agent     string         `json:"agent,omitempty"`
	Action    string         `json:"action"`
	Resources []string       `json:"resources"`
	Save      []string       `json:"save,omitempty"`
	Metadata  map[string]any `json:"metadata,omitempty"`
	Source    *Source        `json:"source,omitempty"`
}

type Reply string

const (
	ReplyOnce   Reply = "once"
	ReplyAlways Reply = "always"
	ReplyReject Reply = "reject"
)

type AssertInput struct {
	ID        string
	SessionID string
	Agent     string
	Action    string
	Resources []string
	Save      []string
	Metadata  map[string]any
	Source    *Source
}

var (
	ErrDeclined  = errors.New("permission: declined")
	ErrSessionNF = errors.New("permission: session not found")
)

type CorrectedError struct {
	Feedback string
}

func (e *CorrectedError) Error() string {
	return fmt.Sprintf("permission: rejected with feedback: %s", e.Feedback)
}

type BlockedError struct {
	Rules Ruleset
}

func (e *BlockedError) Error() string {
	return fmt.Sprintf("permission: blocked by %d rule(s)", len(e.Rules))
}

type NotFoundError struct {
	RequestID string
}

func (e *NotFoundError) Error() string {
	return fmt.Sprintf("permission: request not found: %s", e.RequestID)
}

// RulesProvider supplies the configured ruleset for a session's agent.
type RulesProvider interface {
	Configured(sessionID, agent string) (Ruleset, error)
}

// SavedStore persists "always" approvals as allow rules.
type SavedStore interface {
	Add(action string, resources []string) error
	List() (Ruleset, error)
}

// Hooks observe the ask/reply lifecycle (event publication seam).
type Hooks struct {
	OnAsked   func(Request)
	OnReplied func(sessionID, requestID string, reply Reply)
}

type Engine struct {
	rules RulesProvider
	saved SavedStore
	hooks Hooks
	idGen func() string

	mu      sync.Mutex
	pending map[string]*pendingRequest
}

type pendingRequest struct {
	request Request
	agent   string
	done    chan outcome
}

type outcome struct {
	declined bool
	feedback string
}

func NewEngine(rules RulesProvider, saved SavedStore, hooks Hooks, idGen func() string) *Engine {
	if idGen == nil {
		idGen = func() string {
			generated, err := id.Ascending(id.KindPermission)
			if err != nil {
				panic(err)
			}
			return generated
		}
	}
	return &Engine{
		rules:   rules,
		saved:   saved,
		hooks:   hooks,
		idGen:   idGen,
		pending: map[string]*pendingRequest{},
	}
}

// Assert evaluates the input and blocks until an ask is resolved.
func (e *Engine) Assert(ctx context.Context, input AssertInput) error {
	effect, rules, err := e.evaluate(input)
	if err != nil {
		return err
	}
	switch effect {
	case Deny:
		return &BlockedError{Rules: relevant(input, rules)}
	case Allow:
		return nil
	}
	item := e.create(input)
	if e.hooks.OnAsked != nil {
		e.hooks.OnAsked(item.request)
	}
	select {
	case <-ctx.Done():
		e.remove(item.request.ID)
		return ctx.Err()
	case result := <-item.done:
		e.remove(item.request.ID)
		if result.declined {
			if result.feedback != "" {
				return &CorrectedError{Feedback: result.feedback}
			}
			return ErrDeclined
		}
		return nil
	}
}

// Ask evaluates without waiting, registering a pending request when the effect
// is ask.
func (e *Engine) Ask(input AssertInput) (string, Effect, error) {
	effect, _, err := e.evaluate(input)
	if err != nil {
		return "", "", err
	}
	request := e.requestFor(input)
	if effect == Ask {
		e.register(request, input.Agent)
		if e.hooks.OnAsked != nil {
			e.hooks.OnAsked(request)
		}
	}
	return request.ID, effect, nil
}

// Reply resolves a pending request, cascading per the TypeScript semantics:
// reject declines every other pending request for the session; always saves
// rules and auto-resolves pending requests they now cover.
func (e *Engine) Reply(requestID string, reply Reply, message string) error {
	e.mu.Lock()
	item, ok := e.pending[requestID]
	if !ok {
		e.mu.Unlock()
		return &NotFoundError{RequestID: requestID}
	}
	sessionID := item.request.SessionID
	e.mu.Unlock()

	if e.hooks.OnReplied != nil {
		e.hooks.OnReplied(sessionID, requestID, reply)
	}

	switch reply {
	case ReplyReject:
		e.resolve(requestID, outcome{declined: true, feedback: message})
		e.cascadeReject(sessionID)
		return nil
	case ReplyAlways:
		if len(item.request.Save) > 0 && e.saved != nil {
			if err := e.saved.Add(item.request.Action, item.request.Save); err != nil {
				return err
			}
		}
		e.resolve(requestID, outcome{})
		if len(item.request.Save) > 0 {
			e.cascadeAllow()
		}
		return nil
	default:
		e.resolve(requestID, outcome{})
		return nil
	}
}

func (e *Engine) Get(id string) *Request {
	e.mu.Lock()
	defer e.mu.Unlock()
	if item, ok := e.pending[id]; ok {
		request := item.request
		return &request
	}
	return nil
}

func (e *Engine) List() []Request {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]Request, 0, len(e.pending))
	for _, item := range e.pending {
		out = append(out, item.request)
	}
	return out
}

func (e *Engine) ForSession(sessionID string) []Request {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []Request
	for _, item := range e.pending {
		if item.request.SessionID == sessionID {
			out = append(out, item.request)
		}
	}
	return out
}

func (e *Engine) evaluate(input AssertInput) (Effect, Ruleset, error) {
	rules, err := e.rules.Configured(input.SessionID, input.Agent)
	if err != nil {
		return "", nil, err
	}
	if denied(input, rules) {
		return Deny, rules, nil
	}
	all := rules
	if e.saved != nil {
		savedRules, err := e.saved.List()
		if err != nil {
			return "", nil, err
		}
		all = Merge(rules, savedRules)
	}
	effect := Allow
	for _, resource := range input.Resources {
		switch Evaluate(input.Action, resource, all).Effect {
		case Deny:
			return Deny, all, nil
		case Ask:
			effect = Ask
		}
	}
	return effect, all, nil
}

func denied(input AssertInput, rules Ruleset) bool {
	for _, resource := range input.Resources {
		if Evaluate(input.Action, resource, rules).Effect == Deny {
			return true
		}
	}
	return false
}

func relevant(input AssertInput, rules Ruleset) Ruleset {
	var out Ruleset
	for _, rule := range rules {
		if Match(input.Action, rule.Action) {
			out = append(out, rule)
		}
	}
	return out
}

func (e *Engine) requestFor(input AssertInput) Request {
	id := input.ID
	if id == "" {
		id = e.idGen()
	}
	return Request{
		ID:        id,
		SessionID: input.SessionID,
		Agent:     input.Agent,
		Action:    input.Action,
		Resources: input.Resources,
		Save:      input.Save,
		Metadata:  input.Metadata,
		Source:    input.Source,
	}
}

func (e *Engine) create(input AssertInput) *pendingRequest {
	return e.register(e.requestFor(input), input.Agent)
}

func (e *Engine) register(request Request, agent string) *pendingRequest {
	item := &pendingRequest{request: request, agent: agent, done: make(chan outcome, 1)}
	e.mu.Lock()
	e.pending[request.ID] = item
	e.mu.Unlock()
	return item
}

func (e *Engine) resolve(requestID string, result outcome) {
	e.mu.Lock()
	item, ok := e.pending[requestID]
	delete(e.pending, requestID)
	e.mu.Unlock()
	if ok {
		item.done <- result
	}
}

func (e *Engine) remove(requestID string) {
	e.mu.Lock()
	delete(e.pending, requestID)
	e.mu.Unlock()
}

func (e *Engine) cascadeReject(sessionID string) {
	e.mu.Lock()
	var victims []*pendingRequest
	for id, item := range e.pending {
		if item.request.SessionID == sessionID {
			victims = append(victims, item)
			delete(e.pending, id)
		}
	}
	e.mu.Unlock()
	for _, item := range victims {
		if e.hooks.OnReplied != nil {
			e.hooks.OnReplied(sessionID, item.request.ID, ReplyReject)
		}
		item.done <- outcome{declined: true}
	}
}

func (e *Engine) cascadeAllow() {
	e.mu.Lock()
	candidates := make([]*pendingRequest, 0, len(e.pending))
	for _, item := range e.pending {
		candidates = append(candidates, item)
	}
	e.mu.Unlock()

	for _, item := range candidates {
		input := AssertInput{
			SessionID: item.request.SessionID,
			Agent:     item.agent,
			Action:    item.request.Action,
			Resources: item.request.Resources,
		}
		effect, _, err := e.evaluate(input)
		if err != nil || effect != Allow {
			continue
		}
		if e.hooks.OnReplied != nil {
			e.hooks.OnReplied(item.request.SessionID, item.request.ID, ReplyAlways)
		}
		e.resolve(item.request.ID, outcome{})
	}
}
