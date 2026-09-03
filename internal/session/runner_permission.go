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
		Source: &permission.Source{
			Type:      "tool",
			MessageID: input.AssistantMessageID,
			CallID:    input.CallID,
		},
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
