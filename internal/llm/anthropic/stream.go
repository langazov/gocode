package anthropic

import (
	"context"
	"encoding/json"

	"github.com/langazov/gocode-go/internal/llm"
)

// Stream implements llm.StreamClient: exactly one provider turn, emitting
// canonical events through emit.
func (c *Client) Stream(ctx context.Context, request llm.Request, emit func(llm.StreamEvent)) error {
	req, err := convertRequest(request)
	if err != nil {
		return err
	}
	final, err := c.streamHandler(ctx, req, StreamHandler{
		OnText: func(text string) {
			emit(llm.StreamEvent{Type: llm.EventTextDelta, Text: text})
		},
		OnThinking: func(thinking string) {
			emit(llm.StreamEvent{Type: llm.EventReasoningDelta, Text: thinking})
		},
		OnToolUse: func(id, name string, input json.RawMessage) {
			var parsed map[string]any
			if len(input) > 0 {
				json.Unmarshal(input, &parsed)
			}
			emit(llm.StreamEvent{Type: llm.EventToolCall, ToolCall: &llm.ToolCall{ID: id, Name: name, Input: parsed}})
		},
	})
	if err != nil {
		emit(llm.StreamEvent{Type: llm.EventProviderError, Error: err})
		return err
	}
	event := llm.StreamEvent{Type: llm.EventFinish}
	if final != nil {
		event.Finish = final.StopReason
		event.Usage = llm.Usage{
			Input:      final.Usage.InputTokens,
			Output:     final.Usage.OutputTokens,
			CacheRead:  final.Usage.CacheReadTokens,
			CacheWrite: final.Usage.CacheWriteTokens,
		}
	}
	emit(event)
	return nil
}

func convertRequest(request llm.Request) (Request, error) {
	// Anthropic is the only protocol this port speaks that has a wire
	// representation for a cache breakpoint, so it is where the placement
	// policy runs. Upstream gates the same pass on a set of protocol ids
	// (packages/llm/src/cache-policy.ts's RESPECTS_INLINE_HINTS); here the
	// gate is structural — no other adapter calls it.
	request = llm.ApplyCachePolicy(request)
	breakpoints := llm.NewBreakpoints(llm.AnthropicBreakpointCap)

	maxTokens := request.MaxTokens
	if maxTokens == 0 {
		maxTokens = 8192
	}
	out := Request{
		Model:       request.ModelID,
		MaxTokens:   maxTokens,
		Temperature: request.Temperature,
		TopP:        request.TopP,
	}
	if thinking := parseThinking(request.Reasoning); thinking != nil {
		out.Thinking = thinking
		// Anthropic requires max_tokens > thinking.budget_tokens; the
		// runner's default MaxTokens (8192) is routinely smaller than a
		// "high"/"max" budget, so grow it to leave room for the actual
		// reply beyond the thinking block.
		if out.MaxTokens <= thinking.BudgetTokens {
			out.MaxTokens = thinking.BudgetTokens + 1024
		}
	}
	// Tools, then system, then messages — the order the prefix is cached in,
	// so that when hand-placed hints overrun the cap the markers that survive
	// are the ones nearest the front of the request, where the reusable
	// prefix actually is.
	for _, tool := range request.Tools {
		schema := tool.InputSchema
		if schema == nil {
			schema = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		out.Tools = append(out.Tools, Tool{
			Name:         tool.Name,
			Description:  tool.Description,
			InputSchema:  schema,
			CacheControl: cacheControl(breakpoints, tool.Cache),
		})
	}
	// The system prompt arrives as separate strings and as any system-role
	// messages, and lowers to one block per non-empty entry. It used to be
	// joined into a single string with blank lines between; splitting on the
	// same boundaries leaves the prompt the model reads unchanged.
	var system []string
	for _, text := range request.System {
		if text != "" {
			system = append(system, text)
		}
	}
	for _, message := range request.Messages {
		if message.Role != llm.RoleSystem {
			continue
		}
		for _, part := range message.Content {
			if part.Type == llm.PartText && part.Text != "" {
				system = append(system, part.Text)
			}
		}
	}
	for i, text := range system {
		block := SystemBlock{Type: "text", Text: text}
		// The breakpoint covers the system prompt as a whole, so it goes on
		// the final block.
		if i == len(system)-1 {
			block.CacheControl = cacheControl(breakpoints, request.SystemCache)
		}
		out.System = append(out.System, block)
	}
	for _, message := range request.Messages {
		if message.Role == llm.RoleSystem {
			continue
		}
		out.Messages = append(out.Messages, convertMessage(message, breakpoints)...)
	}
	if request.ToolChoice == "none" {
		out.ToolChoice = &ToolChoice{Type: "none"}
	}
	return out, nil
}

// cacheControl lowers a canonical hint to the wire marker, spending one of the
// request's four breakpoints. Returns nil for no hint and for a hint that
// would overrun the cap — a fifth marker is a 400 from the API, so dropping it
// costs a cache miss where failing the turn would cost the turn.
func cacheControl(breakpoints *llm.Breakpoints, hint *llm.CacheHint) *CacheControl {
	if !breakpoints.Take(hint) {
		return nil
	}
	if hint.Extended() {
		return Ephemeral1h
	}
	return Ephemeral5m
}

// parseThinking reads the "thinking" key a reasoning variant patches into
// llm.Request.Reasoning (see internal/provider.ReasoningVariants) and
// converts it to the native request field.
func parseThinking(reasoning map[string]any) *Thinking {
	raw, ok := reasoning["thinking"].(map[string]any)
	if !ok {
		return nil
	}
	thinkingType, _ := raw["type"].(string)
	if thinkingType != "enabled" {
		return nil
	}
	budget, ok := raw["budget_tokens"].(int)
	if !ok {
		return nil
	}
	return &Thinking{Type: "enabled", BudgetTokens: budget}
}

// imageBlock lowers an image part. A PDF is a "document" block rather than an
// "image" one; everything else Anthropic accepts is an image.
func imageBlock(part llm.ContentPart) ContentBlock {
	kind := "image"
	if part.Mime == "application/pdf" {
		kind = "document"
	}
	return ContentBlock{
		Type: kind,
		Source: &Source{
			Type:      "base64",
			MediaType: part.Mime,
			Data:      part.Data,
		},
	}
}

func convertMessage(message llm.Message, breakpoints *llm.Breakpoints) []Message {
	switch message.Role {
	case llm.RoleUser:
		blocks := make([]ContentBlock, 0, len(message.Content))
		for _, part := range message.Content {
			switch part.Type {
			case llm.PartText:
				blocks = append(blocks, ContentBlock{
					Type:         "text",
					Text:         part.Text,
					CacheControl: cacheControl(breakpoints, part.Cache),
				})
			case llm.PartImage:
				block := imageBlock(part)
				block.CacheControl = cacheControl(breakpoints, part.Cache)
				blocks = append(blocks, block)
			}
		}
		return []Message{{Role: "user", Content: blocks}}
	case llm.RoleAssistant:
		blocks := make([]ContentBlock, 0, len(message.Content))
		for _, part := range message.Content {
			switch part.Type {
			case llm.PartText:
				blocks = append(blocks, ContentBlock{
					Type:         "text",
					Text:         part.Text,
					CacheControl: cacheControl(breakpoints, part.Cache),
				})
			case llm.PartReasoning:
				// No breakpoint on a thinking block: Anthropic rejects one
				// there, and a hint can only reach this part by hand.
				blocks = append(blocks, ContentBlock{Type: "thinking", Thinking: part.Text})
			case llm.PartToolCall:
				input, err := json.Marshal(part.Input)
				if err == nil {
					blocks = append(blocks, ContentBlock{
						Type:         "tool_use",
						ID:           part.ToolCallID,
						Name:         part.ToolName,
						Input:        input,
						CacheControl: cacheControl(breakpoints, part.Cache),
					})
				}
			}
		}
		return []Message{{Role: "assistant", Content: blocks}}
	case llm.RoleTool:
		blocks := make([]ContentBlock, 0, len(message.Content))
		for _, part := range message.Content {
			if part.Type != llm.PartToolResult {
				continue
			}
			blocks = append(blocks, ContentBlock{
				Type:         "tool_result",
				ToolUseID:    part.ToolCallID,
				Content:      part.Result,
				IsError:      part.IsError,
				CacheControl: cacheControl(breakpoints, part.Cache),
			})
		}
		return []Message{{Role: "user", Content: blocks}}
	}
	return nil
}
