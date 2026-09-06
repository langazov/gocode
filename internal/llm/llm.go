// Package llm defines the canonical provider-neutral message and streaming
// contract used by the session runner, replacing @opencode-ai/llm.
package llm

import "context"

const (
	RoleUser      = "user"
	RoleAssistant = "assistant"
	RoleSystem    = "system"
	RoleTool      = "tool"
)

const (
	PartText       = "text"
	PartReasoning  = "reasoning"
	PartToolCall   = "tool-call"
	PartToolResult = "tool-result"
	// PartImage carries an image or PDF attached to a user message. Every
	// provider spells it differently — Anthropic an image block, OpenAI an
	// image_url with a data URI, Gemini inlineData — so it is kept as mime
	// plus base64 here and lowered per client.
	PartImage = "image"
)

type ContentPart struct {
	Type       string
	Text       string
	ToolCallID string
	ToolName   string
	Input      map[string]any
	Result     string
	IsError    bool
	// Mime and Data carry a PartImage: the media type and the base64-encoded
	// bytes, without a data: prefix.
	Mime string
	Data string
}

type Message struct {
	ID      string
	Role    string
	Content []ContentPart
}

func UserText(id, text string) Message {
	return Message{ID: id, Role: RoleUser, Content: []ContentPart{{Type: PartText, Text: text}}}
}

func AssistantText(id, text string) Message {
	return Message{ID: id, Role: RoleAssistant, Content: []ContentPart{{Type: PartText, Text: text}}}
}

func SystemMessage(text string) Message {
	return Message{Role: RoleSystem, Content: []ContentPart{{Type: PartText, Text: text}}}
}

func ToolResultMessage(id, toolCallID, toolName, result string, isError bool) Message {
	return Message{
		ID:   id,
		Role: RoleTool,
		Content: []ContentPart{{
			Type:       PartToolResult,
			ToolCallID: toolCallID,
			ToolName:   toolName,
			Result:     result,
			IsError:    isError,
		}},
	}
}

type ToolDefinition struct {
	Name        string
	Description string
	InputSchema map[string]any
}

type Request struct {
	ProviderID string
	ModelID    string
	System     []string
	Messages   []Message
	Tools      []ToolDefinition
	ToolChoice string
	MaxTokens  int
	// Temperature and TopP are the sampling controls the chat.params plugin
	// hook adjusts (packages/plugin/src/index.ts). They are pointers because
	// zero is a meaningful setting and "leave it to the provider" is a
	// different request — nil sends no field at all, which is what every turn
	// did before the hook existed.
	Temperature *float64
	TopP        *float64
	// Reasoning carries provider-specific extended-thinking/reasoning-effort
	// options selected via a model variant (see internal/provider.ReasoningVariants
	// and packages/opencode/src/provider/transform.ts's reasoningVariants).
	// Recognized keys are provider-specific: Anthropic reads "thinking"
	// ({"type":"enabled","budget_tokens":N}), Gemini reads "thinkingConfig"
	// ({"includeThoughts":true,"thinkingBudget"|"thinkingLevel":...}), and the
	// OpenAI adapter (also used for openai-compatible endpoints) reads
	// "reasoning_effort" (a string). nil/empty means no reasoning requested,
	// matching the original CLI's opt-in --variant behavior.
	Reasoning map[string]any
}

// Usage is a *non-overlapping* breakdown: the five buckets partition the
// step's tokens, so no token is counted twice. session.stepCost relies on it —
// it prices the buckets additively (see internal/session/cost.go), which is
// only correct if they are disjoint.
//
// Providers disagree about this. Anthropic reports `input_tokens` already
// exclusive of its cache counters and needs no adjustment; every
// OpenAI-shaped API reports inclusive totals (`prompt_tokens` contains
// `prompt_tokens_details.cached_tokens`, `completion_tokens` contains
// `completion_tokens_details.reasoning_tokens`) and each adapter subtracts the
// subsets out with SubtractTokens before filling this in. Upstream does the
// same normalization one layer up, in session.ts's getUsage.
//
// Input therefore excludes cache. Anything sizing the *request* — a pricing
// tier, a context-window check — wants Input + CacheRead + CacheWrite.
type Usage struct {
	Input      int
	Output     int
	Reasoning  int
	CacheRead  int
	CacheWrite int
}

// SubtractTokens derives a non-overlapping count from a provider's inclusive
// total, clamping at zero when the reported breakdown is nonsensical (a
// `cached_tokens` larger than `prompt_tokens` is rare but not unheard of, and
// a negative bucket would silently credit the step's cost). Ports
// packages/llm/src/protocols/shared.ts's subtractTokens.
func SubtractTokens(total, subset int) int {
	if subset <= 0 {
		return total
	}
	if subset >= total {
		return 0
	}
	return total - subset
}

const (
	EventTextDelta      = "text-delta"
	EventReasoningDelta = "reasoning-delta"
	EventToolCall       = "tool-call"
	EventFinish         = "finish"
	EventProviderError  = "provider-error"
)

type ToolCall struct {
	ID    string
	Name  string
	Input map[string]any

	// ProviderExecuted marks a tool the provider ran server-side (web search
	// and friends). The runner must not dispatch it locally; Output already
	// carries the result. Mirrors the AI SDK's `providerExecuted` flag, which
	// the TypeScript runtime branches on in
	// packages/opencode/src/session/llm/native-runtime.ts.
	ProviderExecuted bool
	// Output is the provider-supplied result, set only when ProviderExecuted.
	Output string
}

type StreamEvent struct {
	Type     string
	Text     string
	ToolCall *ToolCall
	Finish   string
	Usage    Usage
	Cost     float64
	Error    error
}

// StreamClient executes exactly one provider turn, emitting events through
// emit until completion.
type StreamClient interface {
	Stream(ctx context.Context, request Request, emit func(StreamEvent)) error
}
