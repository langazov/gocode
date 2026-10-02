package acp

import (
	"context"
	"encoding/json"
	"sort"
	"strings"

	"github.com/langazov/gocode-go/internal/agent"
	"github.com/langazov/gocode-go/internal/session"
)

// Config option ids. They are stable identifiers the client sends back.
const (
	configMode        = "mode"
	configModel       = "model"
	configThought     = "thought_level"
	configAutoApprove = "auto_approve"
	// defaultVariant is the thought level meaning "the model's default".
	defaultVariant = "default"
)

// modes lists the agents a user can switch between: primary, visible ones.
// Ports the agent filter in packages/opencode/src/acp/service.ts
// loadDirectorySnapshot (mode !== "subagent" && !hidden).
func modes(registry *agent.Registry) []agent.Info {
	var out []agent.Info
	for _, info := range registry.All() {
		if info.ID == "" || info.Mode == "subagent" || info.Hidden {
			continue
		}
		out = append(out, info)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// currentMode is the session's agent, or the default one.
func (a *Agent) currentMode(ctx context.Context, s *acpSession) string {
	s.mu.Lock()
	current := s.agentID
	s.mu.Unlock()
	if current != "" {
		return current
	}
	if info, err := s.runtime.Sessions.Get(ctx, s.id); err == nil && info != nil && info.Agent != "" {
		return info.Agent
	}
	if s.runtime.Runner != nil && s.runtime.Runner.Agent != "" {
		return s.runtime.Runner.Agent
	}
	if info, ok := s.runtime.Agents.Default(); ok {
		return info.ID
	}
	return "build"
}

// currentModel is the session's pinned model, or the runtime default.
func (a *Agent) currentModel(ctx context.Context, s *acpSession) session.ModelRef {
	if info, err := s.runtime.Sessions.Get(ctx, s.id); err == nil && info != nil && info.Model != nil {
		return *info.Model
	}
	return s.runtime.Sessions.DefaultModel
}

// modeState is the v1 modes field (protocol/v1/session-modes "Initial
// state"): kept alongside config options for clients that predate them.
func (a *Agent) modeState(ctx context.Context, s *acpSession) obj {
	available := modes(s.runtime.Agents)
	if len(available) == 0 {
		return nil
	}
	list := make([]obj, 0, len(available))
	for _, info := range available {
		entry := obj{"id": info.ID, "name": modeName(info)}
		if info.Description != "" {
			entry["description"] = info.Description
		}
		list = append(list, entry)
	}
	return obj{"currentModeId": a.currentMode(ctx, s), "availableModes": list}
}

func modeName(info agent.Info) string {
	if info.ID == "" {
		return info.ID
	}
	return strings.ToUpper(info.ID[:1]) + info.ID[1:]
}

// configOptions is the complete, ordered option list: mode, model, thought
// level when the model has variants, and auto-approve for clients that take
// boolean options. Mirrors buildConfigOptions in
// packages/opencode/src/acp/config-option.ts. v2 renames the id fields
// (protocol/v2/migration "Consistent ID naming").
func (a *Agent) configOptions(ctx context.Context, s *acpSession) []obj {
	v2 := a.protocolVersion() == ProtocolV2
	idKey, groupKey := "id", "group"
	if v2 {
		idKey, groupKey = "configId", "groupId"
	}
	var options []obj

	if available := modes(s.runtime.Agents); len(available) > 0 {
		values := make([]obj, 0, len(available))
		for _, info := range available {
			value := obj{"value": info.ID, "name": modeName(info)}
			if info.Description != "" {
				value["description"] = info.Description
			}
			values = append(values, value)
		}
		options = append(options, obj{
			idKey:          configMode,
			"name":         "Mode",
			"description":  "The agent that runs the session",
			"category":     "mode",
			"type":         "select",
			"currentValue": a.currentMode(ctx, s),
			"options":      values,
		})
	}

	model := a.currentModel(ctx, s)
	current := model.ProviderID + "/" + model.ID
	var models []Model
	if s.runtime.Models != nil {
		models = s.runtime.Models(ctx)
	}
	groups := []obj{}
	groupIndex := map[string]int{}
	var variants []string
	listed := false
	for _, entry := range models {
		ref := entry.ProviderID + "/" + entry.ID
		if ref == current {
			listed = true
			variants = entry.Variants
		}
		index, ok := groupIndex[entry.ProviderID]
		if !ok {
			index = len(groups)
			groupIndex[entry.ProviderID] = index
			groups = append(groups, obj{
				groupKey:  entry.ProviderID,
				"name":    firstNonEmpty(entry.ProviderName, entry.ProviderID),
				"options": []obj{},
			})
		}
		groups[index]["options"] = append(groups[index]["options"].([]obj), obj{"value": ref, "name": firstNonEmpty(entry.Name, entry.ID)})
	}
	if !listed && model.ID != "" {
		// The pinned model must be selectable even when it is no longer in
		// the reachable list, or the selector would show no current value.
		groups = append(groups, obj{
			groupKey:  "current",
			"name":    "Current",
			"options": []obj{{"value": current, "name": current}},
		})
	}
	if len(groups) > 0 {
		options = append(options, obj{
			idKey:          configModel,
			"name":         "Model",
			"category":     "model",
			"type":         "select",
			"currentValue": current,
			"options":      groups,
		})
	}

	if len(variants) > 0 {
		values := []obj{{"value": defaultVariant, "name": "Default"}}
		for _, variant := range variants {
			values = append(values, obj{"value": variant, "name": strings.ToUpper(variant[:1]) + variant[1:]})
		}
		currentVariant := defaultVariant
		for _, variant := range variants {
			if variant == model.Variant {
				currentVariant = variant
			}
		}
		options = append(options, obj{
			idKey:          configThought,
			"name":         "Reasoning",
			"description":  "How much the model thinks before answering",
			"category":     "thought_level",
			"type":         "select",
			"currentValue": currentVariant,
			"options":      values,
		})
	}

	// Boolean options only for clients that advertised them
	// (protocol/v1/session-config-options "Boolean Config Options").
	if a.clientCapabilities().booleanOptions {
		options = append(options, obj{
			idKey:          configAutoApprove,
			"name":         "Auto-approve",
			"description":  "Answer permission prompts automatically; configured denies still apply",
			"type":         "boolean",
			"currentValue": s.autoApproving(),
		})
	}
	return options
}

// modelVariants is the variants the model offers, from the model list.
func (a *Agent) modelVariants(ctx context.Context, s *acpSession, providerID, modelID string) ([]string, bool) {
	if s.runtime.Models == nil {
		return nil, false
	}
	for _, entry := range s.runtime.Models(ctx) {
		if entry.ProviderID == providerID && entry.ID == modelID {
			return entry.Variants, true
		}
	}
	return nil, false
}

func (a *Agent) setConfigOption(ctx context.Context, params json.RawMessage) (any, error) {
	var in struct {
		SessionID string          `json:"sessionId"`
		ConfigID  string          `json:"configId"`
		Type      string          `json:"type"`
		Value     json.RawMessage `json:"value"`
	}
	if err := decode(params, &in); err != nil {
		return nil, err
	}
	s, err := a.session(in.SessionID)
	if err != nil {
		return nil, err
	}
	if a.protocolVersion() == ProtocolV2 && in.Type == "" {
		return nil, invalidParams("type is required")
	}
	switch in.Type {
	case "", "id", "boolean":
	default:
		return nil, invalidParams("unsupported value type %q", in.Type)
	}

	if in.ConfigID == configAutoApprove {
		if !a.clientCapabilities().booleanOptions {
			return nil, invalidParams("unknown config option %q", in.ConfigID)
		}
		var value bool
		if json.Unmarshal(in.Value, &value) != nil || (in.Type != "" && in.Type != "boolean") {
			return nil, invalidParams("%s takes a boolean value", in.ConfigID)
		}
		s.mu.Lock()
		s.autoApprove = value
		s.mu.Unlock()
		return obj{"configOptions": a.configOptions(ctx, s)}, nil
	}

	var value string
	if json.Unmarshal(in.Value, &value) != nil || in.Type == "boolean" {
		return nil, invalidParams("%s takes a value id", in.ConfigID)
	}
	switch in.ConfigID {
	case configMode:
		if err := a.switchMode(ctx, s, value); err != nil {
			return nil, err
		}
	case configModel:
		providerID, modelID, ok := strings.Cut(value, "/")
		if !ok || providerID == "" || modelID == "" {
			return nil, invalidParams("model must be provider/model, got %q", value)
		}
		current := a.currentModel(ctx, s)
		if current.ProviderID+"/"+current.ID != value {
			if _, known := a.modelVariants(ctx, s, providerID, modelID); !known {
				return nil, invalidParams("unknown model %q", value)
			}
		}
		variant := current.Variant
		if variants, _ := a.modelVariants(ctx, s, providerID, modelID); !contains(variants, variant) {
			variant = ""
		}
		if err := s.runtime.Sessions.SetModel(ctx, s.id, session.ModelRef{ProviderID: providerID, ID: modelID, Variant: variant}); err != nil {
			return nil, internalError("switching model: %v", err)
		}
	case configThought:
		current := a.currentModel(ctx, s)
		variants, _ := a.modelVariants(ctx, s, current.ProviderID, current.ID)
		variant := value
		if value == defaultVariant {
			variant = ""
		} else if !contains(variants, value) {
			return nil, invalidParams("unknown thought level %q", value)
		}
		current.Variant = variant
		if err := s.runtime.Sessions.SetModel(ctx, s.id, current); err != nil {
			return nil, internalError("switching thought level: %v", err)
		}
	default:
		return nil, invalidParams("unknown config option %q", in.ConfigID)
	}
	// The full state, since one change can reshape others: a new model
	// brings its own thought levels.
	return obj{"configOptions": a.configOptions(ctx, s)}, nil
}

// setMode is v1 session/set_mode, the pre-config-options way to switch.
func (a *Agent) setMode(ctx context.Context, params json.RawMessage) (any, error) {
	var in struct {
		SessionID string `json:"sessionId"`
		ModeID    string `json:"modeId"`
	}
	if err := decode(params, &in); err != nil {
		return nil, err
	}
	s, err := a.session(in.SessionID)
	if err != nil {
		return nil, err
	}
	if err := a.switchMode(ctx, s, in.ModeID); err != nil {
		return nil, err
	}
	return obj{}, nil
}

// switchMode moves the session to another agent. The switch is recorded
// before it is published, so the agent.switched event it raises is not
// echoed back to the client that asked for it.
func (a *Agent) switchMode(ctx context.Context, s *acpSession, modeID string) error {
	found := false
	for _, info := range modes(s.runtime.Agents) {
		if info.ID == modeID {
			found = true
			break
		}
	}
	if !found {
		return invalidParams("unknown mode %q", modeID)
	}
	s.mu.Lock()
	s.agentID = modeID
	s.mu.Unlock()
	if err := s.runtime.Sessions.SetAgent(ctx, s.id, modeID); err != nil {
		return internalError("switching mode: %v", err)
	}
	return nil
}

// availableCommands is the slash command list: gocode's commands and skills,
// plus /compact. v2 command input carries a type discriminator
// (protocol/v2/migration "Slash commands").
func (a *Agent) availableCommands(s *acpSession) []obj {
	v2 := a.protocolVersion() == ProtocolV2
	var out []obj
	seen := map[string]bool{}
	for _, cmd := range s.runtime.Commands.List() {
		if cmd.Name == "" || seen[cmd.Name] {
			continue
		}
		seen[cmd.Name] = true
		entry := obj{"name": cmd.Name, "description": firstNonEmpty(firstLine(cmd.Description), "Run /"+cmd.Name)}
		if len(cmd.Hints) > 0 {
			input := obj{"hint": strings.Join(cmd.Hints, " ")}
			if v2 {
				input["type"] = "text"
			}
			entry["input"] = input
		}
		out = append(out, entry)
	}
	if !seen["compact"] {
		out = append(out, obj{"name": "compact", "description": "Summarize the conversation to free up context"})
	}
	return out
}

func firstLine(text string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(text), "\n")
	return line
}

func contains(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}
