package session

import (
	"context"

	"github.com/anomalyco/opencode-go/internal/agent"
	"github.com/anomalyco/opencode-go/internal/permission"
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
		Source: &permission.Source{
			Type:      "tool",
			MessageID: input.AssistantMessageID,
			CallID:    input.CallID,
		},
	})
}

// AgentRulesProvider supplies the permission engine with the resolved agent's
// ruleset, defaulting to deny-all for unknown agents, matching the TypeScript
// configured() behavior.
type AgentRulesProvider struct {
	Agents *agent.Registry
}

func (p *AgentRulesProvider) Configured(sessionID, agentID string) (permission.Ruleset, error) {
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
