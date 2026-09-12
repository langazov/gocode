package session

import (
	"context"

	"github.com/langazov/gocode-go/internal/agent"
	"github.com/langazov/gocode-go/internal/permission"
)

// EnginePermissionGate adapts a permission.Engine to the Runner's
// PermissionGate seam, constructing the canonical tool permission source.
type EnginePermissionGate struct {
	Engine *permission.Engine
}

func (g *EnginePermissionGate) Assert(ctx context.Context, input ToolPermissionInput) error {
	return g.Engine.Assert(ctx, permission.AssertInput{
		SessionID: input.SessionID,
		Agent:     input.Agent,
		Action:    input.Action,
		Resources: input.Resources,
		Save:      input.Save,
		Metadata:  input.Metadata,
		Source: &permission.Source{
			Type:      "tool",
			MessageID: input.AssistantMessageID,
			CallID:    input.CallID,
		},
	})
}

// Denied implements PermissionRuled: the engine's rule evaluation only, with
// no ask and no waiting. Returns the same BlockedError Assert would.
func (g *EnginePermissionGate) Denied(input ToolPermissionInput) error {
	return g.Engine.Denied(permission.AssertInput{
		SessionID: input.SessionID,
		Agent:     input.Agent,
		Action:    input.Action,
		Resources: input.Resources,
	})
}

// SessionRules resolves a session-scoped ruleset, if one was stored. Subagent
// sessions carry a ruleset derived from their parent's grants intersected with
// the subagent's own (see DeriveSubagentPermissions), which must win over the
// agent's stock ruleset.
type SessionRules interface {
	Permission(ctx context.Context, sessionID string) (permission.Ruleset, error)
}

// AgentRulesProvider supplies the permission engine with the resolved agent's
// ruleset, defaulting to deny-all for unknown agents, matching the TypeScript
// configured() behavior. When Sessions is set and the session carries its own
// ruleset, that ruleset is used instead.
type AgentRulesProvider struct {
	Agents *agent.Registry
	// Sessions, when set, is consulted first so a subagent session's derived
	// ruleset overrides its agent's.
	Sessions SessionRules
}

func (p *AgentRulesProvider) Configured(sessionID, agentID string) (permission.Ruleset, error) {
	if p.Sessions != nil && sessionID != "" {
		scoped, err := p.Sessions.Permission(context.Background(), sessionID)
		if err != nil {
			return nil, err
		}
		if len(scoped) > 0 {
			return scoped, nil
		}
	}
	info, ok := p.Agents.Resolve(agentID)
	if !ok {
		return permission.MissingAgentPermissions, nil
	}
	if info.Permissions == nil {
		// Every agent bootStack constructs already gets permission.Defaults()
		// merged in (matching agent.ts:277's Permission.merge(defaults, user)
		// for every native and custom agent), so this is a defensive
		// fallback for any other caller that builds an agent.Info without
		// setting Permissions — it should behave the same as one that did,
		// not silently ask for literally everything.
		return permission.Defaults(), nil
	}
	return info.Permissions, nil
}

// AutoAnswerGate is the --auto tier from the permissions recommendations
// (§11): asks are answered "once" automatically — no user is interrupted —
// while configured denies keep being enforced by the engine underneath.
//
// It exists because the alternative implementation — Runner.Permissions = nil
// — removes the gate entirely, denies included, so a CI pipeline that states
// `"edit": {"*.env": "deny"}` cannot trust its own config under --auto. Here
// the deny check still runs (Denied below, which the runner consults ahead of
// the plugin hook), and an ask is resolved instead of parked.
//
// No pending request is ever created, so the HTTP surface and every client
// stay quiet: --auto is an answering policy, not a permission change.
type AutoAnswerGate struct {
	Engine *permission.Engine
}

// Assert answers every ask with "once": Allow and Deny resolve through the
// engine, and an Ask returns nil as though the user had approved this one
// call. Save is deliberately never written: --auto answers once, never
// always, so no durable grant is created by a flag (P4).
func (g *AutoAnswerGate) Assert(ctx context.Context, input ToolPermissionInput) error {
	if err := g.Denied(input); err != nil {
		return err
	}
	// The deny gate has passed; whatever remains is allow or ask, and both
	// proceed. Evaluating the full input (rather than assuming allow after
	// Denied) keeps the saved-grant merge in the loop, so a resource already
	// granted is allowed rather than merely not-denied.
	if _, err := g.Engine.Evaluate(g.input(input)); err != nil {
		return err
	}
	return nil
}

// Denied implements PermissionRuled so configured denies survive --auto: the
// runner calls it ahead of the plugin hook, and a BlockedError here fails the
// tool call exactly as it would under the default gate.
func (g *AutoAnswerGate) Denied(input ToolPermissionInput) error {
	return g.Engine.Denied(g.input(input))
}

func (g *AutoAnswerGate) input(input ToolPermissionInput) permission.AssertInput {
	// Save is dropped intentionally: an automatically answered ask never
	// writes a grant.
	return permission.AssertInput{
		SessionID: input.SessionID,
		Agent:     input.Agent,
		Action:    input.Action,
		Resources: input.Resources,
	}
}
