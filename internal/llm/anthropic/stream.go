package anthropic

import (
	"context"
	"encoding/json"

	"github.com/anomalyco/opencode-go/internal/llm"
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
	maxTokens := request.MaxTokens
	if maxTokens == 0 {
		maxTokens = 8192
	}
	out := Request{
		Model:     request.ModelID,
		MaxTokens: maxTokens,
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
	for _, system := range request.System {
		if system != "" {
			if out.System != "" {
				out.System += "\n\n"
			}
			out.System += system
		}
	}
	for _, message := range request.Messages {
		if message.Role == llm.RoleSystem {
			for _, part := range message.Content {
				if part.Type == llm.PartText && part.Text != "" {
					if out.System != "" {
						out.System += "\n\n"
					}
					out.System += part.Text
				}
			}
			continue
		}
		converted := convertMessage(message)
		out.Messages = append(out.Messages, converted...)
	}
	for _, tool := range request.Tools {
		schema := tool.InputSchema
		if schema == nil {
			schema = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		out.Tools = append(out.Tools, Tool{
			Name:        tool.Name,
			Description: tool.Description,
			InputSchema: schema,
		})
	}
	if request.ToolChoice == "none" {
		out.ToolChoice = &ToolChoice{Type: "none"}
	}
	return out, nil
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

func convertMessage(message llm.Message) []Message {
	switch message.Role {
	case llm.RoleUser:
		blocks := make([]ContentBlock, 0, len(message.Content))
		for _, part := range message.Content {
			if part.Type == llm.PartText {
				blocks = append(blocks, ContentBlock{Type: "text", Text: part.Text})
			}
		}
		return []Message{{Role: "user", Content: blocks}}
	case llm.RoleAssistant:
		blocks := make([]ContentBlock, 0, len(message.Content))
		for _, part := range message.Content {
			switch part.Type {
			case llm.PartText:
				blocks = append(blocks, ContentBlock{Type: "text", Text: part.Text})
			case llm.PartReasoning:
				blocks = append(blocks, ContentBlock{Type: "thinking", Thinking: part.Text})
			case llm.PartToolCall:
				input, err := json.Marshal(part.Input)
				if err == nil {
					blocks = append(blocks, ContentBlock{
						Type:  "tool_use",
						ID:    part.ToolCallID,
						Name:  part.ToolName,
						Input: input,
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
				Type:      "tool_result",
				ToolUseID: part.ToolCallID,
				Content:   part.Result,
				IsError:   part.IsError,
			})
		}
		return []Message{{Role: "user", Content: blocks}}
	}
	return nil
}
