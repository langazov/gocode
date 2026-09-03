package provider

import (
	"strings"

	"github.com/langazov/gocode-go/internal/modelsdev"
)

// outputTokenMax mirrors OUTPUT_TOKEN_MAX in provider/transform.ts: the cap
// used when a model's budget_tokens reasoning option has no declared max.
const outputTokenMax = 32_000

// Protocol classifies a catalog npm package string into one of the Go
// port's three wire protocols — the same classification FromConfig already
// used inline for picking a stream client, now shared so reasoning-variant
// resolution stays consistent with it.
func Protocol(npm string) string {
	switch {
	case strings.Contains(npm, "anthropic"):
		return "anthropic"
	case strings.Contains(npm, "gemini"), strings.Contains(npm, "google"):
		return "gemini"
	default:
		return "openai"
	}
}

// ReasoningVariants computes variant-id -> provider-specific request option
// patches from a model's catalog reasoning_options, mirroring
// ProviderTransform.reasoningVariants in
// packages/opencode/src/provider/transform.ts. Only the three wire
// protocols the Go port actually speaks are covered (github-copilot,
// bedrock, vertex, ... have no Go adapter); toggle-only reasoning_options
// (no effort/budget_tokens entry) produce no variants, matching TS's
// behavior for every npm package but alibaba/cohere.
//
// Deliberate simplification vs TS: real Anthropic "effort" reasoning_options
// (e.g. claude-opus-4-5) go through a hand-picked-model allowlist in TS
// (adaptive thinking only on specific SKUs) that this port can't replicate
// without hardcoding exact model IDs. Instead every Anthropic effort value
// synthesizes a matching budget_tokens thinking config via effortBudget, so
// --variant always does something useful instead of silently no-op'ing on
// models TS would special-case.
func ReasoningVariants(protocol string, options []modelsdev.ReasoningOption, outputLimit int) map[string]map[string]any {
	for _, opt := range options {
		if opt.Type == "effort" {
			return effortVariants(protocol, opt.Values)
		}
	}
	for _, opt := range options {
		if opt.Type == "budget_tokens" {
			return budgetVariants(protocol, opt.Min, opt.Max, outputLimit)
		}
	}
	return nil
}

func effortVariants(protocol string, values []*string) map[string]map[string]any {
	out := map[string]map[string]any{}
	for _, v := range values {
		id := "none"
		if v != nil {
			id = *v
		}
		if settings := reasoningEffort(protocol, id); settings != nil {
			out[id] = settings
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func reasoningEffort(protocol, effort string) map[string]any {
	switch protocol {
	case "anthropic":
		return map[string]any{"thinking": map[string]any{"type": "enabled", "budget_tokens": effortBudget(effort)}}
	case "gemini":
		return map[string]any{"thinkingConfig": map[string]any{"includeThoughts": true, "thinkingLevel": effort}}
	default: // "openai": native OpenAI and every openai-compatible endpoint
		return map[string]any{"reasoning_effort": effort}
	}
}

// effortBudget approximates a thinking token budget for an effort label,
// for protocols (Anthropic) whose native API has no "effort" field of its
// own — see ReasoningVariants' doc comment.
func effortBudget(effort string) int {
	switch effort {
	case "none":
		return 0
	case "low":
		return 4096
	case "medium":
		return 10_000
	case "high":
		return 16_000
	case "xhigh":
		return 24_000
	case "max":
		return outputTokenMax - 1
	default:
		return 10_000
	}
}

// budgetVariants mirrors budgetVariants() in transform.ts: "high" (half the
// available budget, floored, but never below min) and "max" (the full
// available budget).
func budgetVariants(protocol string, min, max *float64, outputLimit int) map[string]map[string]any {
	maximum := outputTokenMax - 1
	if max != nil && int(*max) < maximum {
		maximum = int(*max)
	}
	if outputLimit > 0 && outputLimit-1 < maximum {
		maximum = outputLimit - 1
	}
	if maximum <= 0 {
		return nil
	}
	minimum := 0
	if min != nil {
		minimum = int(*min)
	}
	high := (maximum + 1) / 2
	if minimum > high {
		high = minimum
	}
	if high > maximum {
		high = maximum
	}

	out := map[string]map[string]any{}
	if settings := reasoningBudget(protocol, high); settings != nil {
		out["high"] = settings
	}
	if settings := reasoningBudget(protocol, maximum); settings != nil {
		out["max"] = settings
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func reasoningBudget(protocol string, budget int) map[string]any {
	switch protocol {
	case "anthropic":
		return map[string]any{"thinking": map[string]any{"type": "enabled", "budget_tokens": budget}}
	case "gemini":
		return map[string]any{"thinkingConfig": map[string]any{"includeThoughts": true, "thinkingBudget": budget}}
	default:
		return nil // matches TS: openai/openai-compatible have no budget-based reasoning param
	}
}
