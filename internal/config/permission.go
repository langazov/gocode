package config

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/langazov/gocode-go/internal/permission"
)

func jsonUnmarshal(data []byte, out any) error {
	return json.Unmarshal(data, out)
}

// Permission is the V1 permission config: either a single action applied to
// everything ("ask") or a per-action map whose values are an action or a
// resource-to-action object. It converts to a runtime ruleset.
type Permission struct {
	Raw    map[string]json.RawMessage
	Flat   string
	IsFlat bool
}

func (p *Permission) UnmarshalJSON(data []byte) error {
	var action string
	if err := json.Unmarshal(data, &action); err == nil {
		switch action {
		case "ask", "allow", "deny":
			p.Flat = action
			p.IsFlat = true
			return nil
		}
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("config: invalid permission value: %w", err)
	}
	p.Raw = raw
	// Validate eagerly like the TypeScript schema: every effect must be
	// ask/allow/deny.
	_, err := p.Ruleset()
	return err
}

// Ruleset converts the permission config into engine rules, matching the
// TypeScript normalizeInput semantics.
func (p Permission) Ruleset() (permission.Ruleset, error) {
	if p.IsFlat {
		effect, err := parseEffect(p.Flat)
		if err != nil {
			return nil, err
		}
		return permission.Ruleset{{Action: "*", Resource: "*", Effect: effect}}, nil
	}
	// permission.Evaluate is last-match-wins, so the order rules are appended
	// in is significant: a map range is randomized per Go's spec, which made
	// this nondeterministic — a "*": deny key could land after or before a
	// specific "read": allow one depending on the run, letting the wildcard
	// randomly clobber the specific rule. Sorting fixes the order; "*" sorts
	// before every letter and digit, so it always lands first and the
	// specific rules that follow correctly override it.
	actions := make([]string, 0, len(p.Raw))
	for action := range p.Raw {
		actions = append(actions, action)
	}
	sort.Strings(actions)

	var out permission.Ruleset
	for _, action := range actions {
		raw := p.Raw[action]
		var effectStr string
		if err := json.Unmarshal(raw, &effectStr); err == nil {
			effect, err := parseEffect(effectStr)
			if err != nil {
				return nil, err
			}
			out = append(out, permission.Rule{Action: action, Resource: "*", Effect: effect})
			continue
		}
		var resourceMap map[string]string
		if err := json.Unmarshal(raw, &resourceMap); err != nil {
			return nil, fmt.Errorf("config: invalid permission rule for %q: %w", action, err)
		}
		resources := make([]string, 0, len(resourceMap))
		for resource := range resourceMap {
			resources = append(resources, resource)
		}
		sort.Strings(resources)
		for _, resource := range resources {
			effect, err := parseEffect(resourceMap[resource])
			if err != nil {
				return nil, fmt.Errorf("config: invalid permission rule for %q: %w", action, err)
			}
			out = append(out, permission.Rule{Action: action, Resource: resource, Effect: effect})
		}
	}
	return out, nil
}

func parseEffect(value string) (permission.Effect, error) {
	switch value {
	case "allow":
		return permission.Allow, nil
	case "ask":
		return permission.Ask, nil
	case "deny":
		return permission.Deny, nil
	}
	return "", fmt.Errorf("invalid permission effect %q", value)
}

// AgentRuleset converts an agent-level permission config.
func (a Agent) AgentRuleset() (permission.Ruleset, error) {
	return a.Permission.Ruleset()
}

// ToolRuleset converts the "tools" map ({"bash": false, "read": true}) into
// permission rules, porting the upstream translation
// (packages/opencode/src/config/config.ts:570-577): a disabled tool becomes a
// blanket deny on its action, collapsed onto the action it shares with its
// aliases (write/edit/apply_patch all gate on "edit"). Enabled entries add
// nothing — the baseline already allows everything the user has not denied.
//
// These rules merge UNDER the explicit permission block, so a user's
// `"permission": {"edit": "ask"}` still wins over `"tools": {"write": false}`
// exactly as upstream sequences it.
func (c Config) ToolRuleset() permission.Ruleset {
	if len(c.Tools) == 0 {
		return nil
	}
	// Sorted for the same reason Ruleset sorts: Evaluate is last-match-wins
	// and map iteration is randomised, so an unsorted expansion would make
	// policy nondeterministic per run.
	names := make([]string, 0, len(c.Tools))
	for name := range c.Tools {
		names = append(names, name)
	}
	sort.Strings(names)
	var out permission.Ruleset
	for _, name := range names {
		if !c.Tools[name] {
			out = append(out, permission.Rule{
				Action:   ToolAction(name),
				Resource: "*",
				Effect:   permission.Deny,
			})
		}
	}
	return out
}

// ToolAction collapses tool names onto the permission action they gate on.
// edit, write and apply_patch are true aliases — one action, three tools —
// so disabling any of them denies the shared "edit" action, which is the
// only way the deny can actually stop the other two spellings of the same
// write.
func ToolAction(toolName string) string {
	switch toolName {
	case "edit", "write", "apply_patch":
		return "edit"
	default:
		return toolName
	}
}
