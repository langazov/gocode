package llm

// Prompt-cache breakpoint placement, ported from
// packages/llm/src/cache-policy.ts.
//
// A breakpoint tells the provider "everything up to here is a stable prefix —
// cache it". Anthropic charges 1.25x to write a 5-minute cache entry and 0.1x
// to read one, so a single reuse inside the window already pays for itself,
// which is why the default policy is on rather than opt-in.
//
// Placement is the whole game. The naive choice — mark the last message or two
// — moves the breakpoint on every request, so the prefix it names is never the
// prefix the next request sends and nothing is ever reused. (That is what
// packages/opencode/src/provider/transform.ts's older applyCaching does on the
// AI-SDK path; cache-policy.ts is the newer placement and the one worth
// porting.) The "auto" policy instead marks three positions that hold still
// across a turn: the end of the tool definitions, the end of the system
// prompt, and the newest user message. A turn that fans out into a dozen
// model/tool round trips keeps sending the same prefix under that last
// breakpoint, so every step after the first reads the cache instead of
// re-paying for the conversation.
//
// Only the Anthropic adapter consults these hints, and it applies the policy
// itself (see convertRequest) — upstream gates the same pass on a
// RESPECTS_INLINE_HINTS set naming anthropic-messages and bedrock-converse.
// The OpenAI-shaped and Gemini APIs cache implicitly, with nothing on the wire
// to mark; hints reaching them would be inert, so they never see them.

// CacheHint marks one request element as a breakpoint. A zero TTLSeconds takes
// the provider's default window (5 minutes on Anthropic).
type CacheHint struct {
	TTLSeconds int
}

// Cache-window buckets. Anthropic offers exactly two, and treats anything
// under an hour as the 5-minute one — ports protocols/utils/cache.ts's
// ttlBucket.
const cacheTTL1HSeconds = 3600

// Extended reports whether the hint asks for the hour-long window rather than
// the default five minutes.
func (h *CacheHint) Extended() bool {
	return h != nil && h.TTLSeconds >= cacheTTL1HSeconds
}

// Where CachePolicy.Messages puts its breakpoint.
const (
	// CacheLatestUserMessage marks the newest user message. This is the one
	// that holds still while a single turn expands into many assistant and
	// tool messages, so it is the default.
	CacheLatestUserMessage = "latest-user-message"
	// CacheLatestAssistantMessage marks the newest assistant message instead.
	CacheLatestAssistantMessage = "latest-assistant"
)

// CachePolicy says where ApplyCachePolicy places breakpoints. The zero value
// places none; AutoCachePolicy is what a request gets when it asks for
// nothing, matching upstream's `undefined -> "auto"`.
type CachePolicy struct {
	// Tools marks the last tool definition.
	Tools bool
	// System marks the end of the system prompt.
	System bool
	// Messages names a strategy above, or is empty to mark no message.
	Messages string
	// TTLSeconds is the window every breakpoint this policy places asks for.
	TTLSeconds int
}

// AutoCachePolicy is the default placement: tools, system, and the newest user
// message. Three of Anthropic's four breakpoints, leaving one for a caller
// that hand-places a hint of its own.
func AutoCachePolicy() CachePolicy {
	return CachePolicy{Tools: true, System: true, Messages: CacheLatestUserMessage}
}

// ApplyCachePolicy returns request with cache hints filled in on the elements
// its policy designates. A nil Request.Cache means AutoCachePolicy; an
// explicit &CachePolicy{} means no automatic placement.
//
// Hints the caller placed by hand are left alone — this only fills gaps — and
// the input is never mutated: the marked slices are copied, so a Request may
// be shared across retries and providers.
func ApplyCachePolicy(request Request) Request {
	policy := AutoCachePolicy()
	if request.Cache != nil {
		policy = *request.Cache
	}
	if !policy.Tools && !policy.System && policy.Messages == "" {
		return request
	}
	hint := &CacheHint{TTLSeconds: policy.TTLSeconds}
	if policy.Tools {
		request.Tools = markLastTool(request.Tools, hint)
	}
	if policy.System && request.SystemCache == nil && len(request.System) > 0 {
		request.SystemCache = hint
	}
	if policy.Messages != "" {
		request.Messages = markMessage(request.Messages, policy.Messages, hint)
	}
	return request
}

// markLastTool marks the final tool definition, which sits at the end of the
// tools block and so names the whole block as a prefix.
func markLastTool(tools []ToolDefinition, hint *CacheHint) []ToolDefinition {
	if len(tools) == 0 || tools[len(tools)-1].Cache != nil {
		return tools
	}
	out := make([]ToolDefinition, len(tools))
	copy(out, tools)
	out[len(out)-1].Cache = hint
	return out
}

// markMessage marks the newest message in the role the strategy names.
func markMessage(messages []Message, strategy string, hint *CacheHint) []Message {
	role := RoleUser
	if strategy == CacheLatestAssistantMessage {
		role = RoleAssistant
	}
	index := -1
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == role {
			index = i
			break
		}
	}
	if index < 0 {
		return messages
	}
	return markMessageAt(messages, index, hint)
}

// markMessageAt marks the last text part of one message, falling back to its
// last part of any type — a message carrying only tool results has no text,
// and its final result is still where the breakpoint belongs.
func markMessageAt(messages []Message, index int, hint *CacheHint) []Message {
	content := messages[index].Content
	if len(content) == 0 {
		return messages
	}
	at := len(content) - 1
	for i := len(content) - 1; i >= 0; i-- {
		if content[i].Type == PartText {
			at = i
			break
		}
	}
	if content[at].Cache != nil {
		return messages
	}
	nextContent := make([]ContentPart, len(content))
	copy(nextContent, content)
	nextContent[at].Cache = hint

	out := make([]Message, len(messages))
	copy(out, messages)
	out[index].Content = nextContent
	return out
}

// Breakpoints is a per-request budget for cache markers. Anthropic accepts at
// most four across tools, system and messages, and answers a fifth with a 400,
// so an adapter counts what it emits and drops the overflow rather than
// failing the turn. Ports protocols/utils/cache.ts's Breakpoints.
type Breakpoints struct {
	Remaining int
	Dropped   int
}

// AnthropicBreakpointCap is that limit.
const AnthropicBreakpointCap = 4

// NewBreakpoints opens a budget of cap markers.
func NewBreakpoints(cap int) *Breakpoints {
	return &Breakpoints{Remaining: cap}
}

// Take reports whether hint may be emitted, spending one marker if so. A nil
// hint is not a breakpoint and costs nothing.
func (b *Breakpoints) Take(hint *CacheHint) bool {
	if hint == nil {
		return false
	}
	if b == nil {
		return true
	}
	if b.Remaining <= 0 {
		b.Dropped++
		return false
	}
	b.Remaining--
	return true
}
