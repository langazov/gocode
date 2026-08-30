// Package openai implements an OpenAI Chat Completions client, compatible
// with OpenAI and OpenAI-compatible endpoints (OpenRouter, Groq, Together,
// Ollama, LM Studio, etc.).
package openai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/anomalyco/opencode-go/internal/llm"
)

const DefaultBaseURL = "https://api.openai.com/v1"

type Client struct {
	APIKey  string
	BaseURL string
	HTTP    *http.Client
}

func New(apiKey string) *Client {
	return &Client{
		APIKey:  apiKey,
		BaseURL: DefaultBaseURL,
		HTTP:    &http.Client{Timeout: 10 * time.Minute},
	}
}

// Stream implements llm.StreamClient for the Chat Completions API.
func (c *Client) Stream(ctx context.Context, request llm.Request, emit func(llm.StreamEvent)) error {
	body, err := convertRequest(request)
	if err != nil {
		return err
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL()+"/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if c.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.APIKey)
	}
	res, err := c.HTTP.Do(httpReq)
	if err != nil {
		emit(llm.StreamEvent{Type: llm.EventProviderError, Error: err})
		return err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		data, _ := io.ReadAll(res.Body)
		err := parseError(res.StatusCode, data)
		emit(llm.StreamEvent{Type: llm.EventProviderError, Error: err})
		return err
	}
	return readStream(res.Body, emit)
}

func (c *Client) baseURL() string {
	if c.BaseURL == "" {
		return DefaultBaseURL
	}
	return strings.TrimRight(c.BaseURL, "/")
}

type chatMessage struct {
	Role       string     `json:"role"`
	Content    string     `json:"content,omitempty"`
	ToolCalls  []toolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	Name       string     `json:"name,omitempty"`
}

type toolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function functionCall `json:"function"`
}

type functionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type toolDef struct {
	Type     string       `json:"type"`
	Function functionSpec `json:"function"`
}

type functionSpec struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters"`
}

type chatRequest struct {
	Model           string        `json:"model"`
	Messages        []chatMessage `json:"messages"`
	Tools           []toolDef     `json:"tools,omitempty"`
	ToolChoice      interface{}   `json:"tool_choice,omitempty"`
	Stream          bool          `json:"stream"`
	MaxTokens       int           `json:"max_tokens,omitempty"`
	Temperature     *float64      `json:"temperature,omitempty"`
	StreamOpts      *streamOpts   `json:"stream_options,omitempty"`
	ReasoningEffort string        `json:"reasoning_effort,omitempty"`
}

type streamOpts struct {
	IncludeUsage bool `json:"include_usage"`
}

func convertRequest(request llm.Request) (chatRequest, error) {
	out := chatRequest{
		Model:      request.ModelID,
		Stream:     true,
		MaxTokens:  request.MaxTokens,
		StreamOpts: &streamOpts{IncludeUsage: true},
	}
	if effort, ok := request.Reasoning["reasoning_effort"].(string); ok {
		out.ReasoningEffort = effort
	}
	for _, system := range request.System {
		if system != "" {
			out.Messages = append(out.Messages, chatMessage{Role: "system", Content: system})
		}
	}
	for _, message := range request.Messages {
		converted, err := convertMessage(message)
		if err != nil {
			return chatRequest{}, err
		}
		out.Messages = append(out.Messages, converted...)
	}
	for _, tool := range request.Tools {
		schema := tool.InputSchema
		if schema == nil {
			schema = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		out.Tools = append(out.Tools, toolDef{
			Type: "function",
			Function: functionSpec{
				Name:        tool.Name,
				Description: tool.Description,
				Parameters:  schema,
			},
		})
	}
	switch request.ToolChoice {
	case "none":
		out.ToolChoice = "none"
	case "":
		// leave unset (auto)
	default:
		out.ToolChoice = request.ToolChoice
	}
	return out, nil
}

func convertMessage(message llm.Message) ([]chatMessage, error) {
	switch message.Role {
	case llm.RoleSystem:
		return []chatMessage{{Role: "system", Content: joinText(message)}}, nil
	case llm.RoleUser:
		return []chatMessage{{Role: "user", Content: joinText(message)}}, nil
	case llm.RoleAssistant:
		msg := chatMessage{Role: "assistant"}
		var text []string
		for _, part := range message.Content {
			switch part.Type {
			case llm.PartText:
				text = append(text, part.Text)
			case llm.PartToolCall:
				arguments, err := json.Marshal(part.Input)
				if err != nil {
					return nil, err
				}
				msg.ToolCalls = append(msg.ToolCalls, toolCall{
					ID:   part.ToolCallID,
					Type: "function",
					Function: functionCall{
						Name:      part.ToolName,
						Arguments: string(arguments),
					},
				})
			}
		}
		msg.Content = strings.Join(text, "")
		return []chatMessage{msg}, nil
	case llm.RoleTool:
		var out []chatMessage
		for _, part := range message.Content {
			if part.Type != llm.PartToolResult {
				continue
			}
			out = append(out, chatMessage{
				Role:       "tool",
				ToolCallID: part.ToolCallID,
				Name:       part.ToolName,
				Content:    part.Result,
			})
		}
		return out, nil
	}
	return nil, nil
}

func joinText(message llm.Message) string {
	var parts []string
	for _, part := range message.Content {
		if part.Type == llm.PartText {
			parts = append(parts, part.Text)
		}
	}
	return strings.Join(parts, "")
}

type toolAccumulator struct {
	id        string
	name      string
	arguments strings.Builder
}

func readStream(reader io.Reader, emit func(llm.StreamEvent)) error {
	var usage llm.Usage
	var finish string
	tools := map[int]*toolAccumulator{}

	flushTools := func() {
		for _, index := range sortedKeys(tools) {
			acc := tools[index]
			var input map[string]any
			if acc.arguments.Len() > 0 {
				json.Unmarshal([]byte(acc.arguments.String()), &input)
			}
			emit(llm.StreamEvent{Type: llm.EventToolCall, ToolCall: &llm.ToolCall{
				ID:    acc.id,
				Name:  acc.name,
				Input: input,
			}})
		}
	}

	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" || payload == "[DONE]" {
			continue
		}
		var chunk streamChunk
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			continue
		}
		if chunk.Usage != nil {
			usage = llm.Usage{
				Input:  chunk.Usage.PromptTokens,
				Output: chunk.Usage.CompletionTokens,
			}
		}
		for _, choice := range chunk.Choices {
			delta := choice.Delta
			if delta.Content != "" {
				emit(llm.StreamEvent{Type: llm.EventTextDelta, Text: delta.Content})
			}
			if reasoning := firstNonEmpty(delta.Reasoning, delta.ReasoningContent, delta.ReasoningText); reasoning != "" {
				emit(llm.StreamEvent{Type: llm.EventReasoningDelta, Text: reasoning})
			}
			for _, toolDelta := range delta.ToolCalls {
				acc, ok := tools[toolDelta.Index]
				if !ok {
					acc = &toolAccumulator{}
					tools[toolDelta.Index] = acc
				}
				if toolDelta.ID != "" {
					acc.id = toolDelta.ID
				}
				if toolDelta.Function.Name != "" {
					acc.name = toolDelta.Function.Name
				}
				acc.arguments.WriteString(toolDelta.Function.Arguments)
			}
			if choice.FinishReason != "" {
				finish = choice.FinishReason
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	flushTools()
	emit(llm.StreamEvent{Type: llm.EventFinish, Finish: finish, Usage: usage})
	return nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func sortedKeys(m map[int]*toolAccumulator) []int {
	keys := make([]int, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	for i := 0; i < len(keys); i++ {
		for j := i + 1; j < len(keys); j++ {
			if keys[j] < keys[i] {
				keys[i], keys[j] = keys[j], keys[i]
			}
		}
	}
	return keys
}

type streamChunk struct {
	Choices []struct {
		Delta struct {
			Content string `json:"content"`
			// Reasoning field name is not standardized across
			// openai-compatible backends; TS's opencode reads whichever of
			// these three is present (reasoning | reasoning_content |
			// reasoning_text).
			Reasoning        string `json:"reasoning"`
			ReasoningContent string `json:"reasoning_content"`
			ReasoningText    string `json:"reasoning_text"`
			ToolCalls        []struct {
				Index    int    `json:"index"`
				ID       string `json:"id"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
}

type APIError struct {
	StatusCode int
	Message    string `json:"message"`
}

func (e *APIError) Error() string {
	return fmt.Sprintf("openai: %d %s", e.StatusCode, e.Message)
}

func parseError(status int, data []byte) error {
	var envelope struct {
		Error APIError `json:"error"`
	}
	if err := json.Unmarshal(data, &envelope); err == nil && envelope.Error.Message != "" {
		envelope.Error.StatusCode = status
		return &envelope.Error
	}
	return &APIError{StatusCode: status, Message: string(data)}
}
