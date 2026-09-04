// Package openairesponses implements OpenAI's Responses API with SSE
// streaming — a different wire protocol from Chat Completions (internal/llm/openai):
// requests carry an `input` array instead of `messages`, and the stream is a
// sequence of typed `response.*` events instead of delta chunks shaped like
// the response object. opencode/Zen routes GPT-5-family, Grok, and Muse
// Spark models through this protocol rather than its own default
// OpenAI-compatible Chat Completions endpoint (see
// packages/llm/src/protocols/openai-responses.ts, the TS reference this
// ports — at a matching level of fidelity to this port's other clients:
// hosted (hosted-tool) events and encrypted reasoning-item replay across
// turns are not implemented, since nothing in this codebase's message model
// carries the state (item ids, encrypted content) needed to reconstruct
// them, and the existing Anthropic/Chat-Completions clients make the same
// simplification for their own provider-specific extras).
package openairesponses

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

	"github.com/langazov/gocode-go/internal/llm"
)

const DefaultBaseURL = "https://api.openai.com/v1"

type Client struct {
	APIKey  string
	BaseURL string
	HTTP    *http.Client
	// Options carries provider-specific headers, body fields, model-id
	// remapping and request signing, supplied by the provider transform layer.
	Options llm.Options
}

func New(apiKey string) *Client {
	return &Client{
		APIKey:  apiKey,
		BaseURL: DefaultBaseURL,
		HTTP:    &http.Client{Timeout: 10 * time.Minute},
	}
}

// Stream implements llm.StreamClient for the Responses API.
func (c *Client) Stream(ctx context.Context, request llm.Request, emit func(llm.StreamEvent)) error {
	request.ModelID = c.Options.Model(request.ModelID)
	body := convertRequest(request)
	payload, err := json.Marshal(body)
	if err != nil {
		return err
	}
	payload, err = c.Options.MergeBody(payload)
	if err != nil {
		return err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint(request.ModelID), bytes.NewReader(payload))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	signed, err := c.Options.Authenticate(httpReq, payload)
	if err != nil {
		return err
	}
	if !signed && c.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.APIKey)
	}
	c.Options.ApplyHeaders(httpReq)
	res, err := c.Options.HTTPClient(c.HTTP).Do(httpReq)
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

func (c *Client) endpoint(model string) string {
	base := c.baseURL()
	return c.Options.URL(base, model, base+"/responses")
}

// =============================================================================
// Request body
// =============================================================================

// contentPart is one element of an input/output content array. Every role's
// content is sent as this array form (rather than the plain string the real
// schema allows for a system item) — the Responses API accepts both, and
// using one shape everywhere keeps message conversion uniform with this
// port's other clients.
type contentPart struct {
	Type     string `json:"type"` // "input_text" | "input_image" | "output_text"
	Text     string `json:"text,omitempty"`
	ImageURL string `json:"image_url,omitempty"`
}

// inputItem is a tagged union of every item shape convertRequest produces:
// a role-addressed message (system/user/assistant) or a function call/result
// pair. Only the fields a given item type needs are set; the rest stay zero
// and are omitted.
type inputItem struct {
	Role      string        `json:"role,omitempty"`
	Content   []contentPart `json:"content,omitempty"`
	Type      string        `json:"type,omitempty"` // "function_call" | "function_call_output"
	CallID    string        `json:"call_id,omitempty"`
	Name      string        `json:"name,omitempty"`
	Arguments string        `json:"arguments,omitempty"`
	Output    string        `json:"output,omitempty"`
}

type tool struct {
	Type        string         `json:"type"`
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters"`
}

type reasoningOptions struct {
	Effort string `json:"effort,omitempty"`
}

type chatRequest struct {
	Model           string            `json:"model"`
	Input           []inputItem       `json:"input"`
	Tools           []tool            `json:"tools,omitempty"`
	ToolChoice      any               `json:"tool_choice,omitempty"`
	Stream          bool              `json:"stream"`
	MaxOutputTokens int               `json:"max_output_tokens,omitempty"`
	Temperature     *float64          `json:"temperature,omitempty"`
	TopP            *float64          `json:"top_p,omitempty"`
	Reasoning       *reasoningOptions `json:"reasoning,omitempty"`
	// Store false matches the TS route's own default: without a prior turn's
	// encrypted reasoning state to replay (see the package doc), keeping
	// state server-side buys nothing and the account's Responses history
	// would otherwise grow unbounded.
	Store *bool `json:"store,omitempty"`
}

var storeFalse = false

func convertRequest(request llm.Request) chatRequest {
	out := chatRequest{
		Model:       request.ModelID,
		Stream:      true,
		Store:       &storeFalse,
		Temperature: request.Temperature,
		TopP:        request.TopP,
	}
	if request.MaxTokens > 0 {
		out.MaxOutputTokens = request.MaxTokens
	}
	if effort, ok := request.Reasoning["reasoning_effort"].(string); ok && effort != "" {
		out.Reasoning = &reasoningOptions{Effort: effort}
	}
	for _, system := range request.System {
		if system != "" {
			out.Input = append(out.Input, inputItem{Role: "system", Content: []contentPart{{Type: "input_text", Text: system}}})
		}
	}
	for _, message := range request.Messages {
		out.Input = append(out.Input, convertMessage(message)...)
	}
	for _, t := range request.Tools {
		schema := t.InputSchema
		if schema == nil {
			schema = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		out.Tools = append(out.Tools, tool{Type: "function", Name: t.Name, Description: t.Description, Parameters: schema})
	}
	switch request.ToolChoice {
	case "none":
		out.ToolChoice = "none"
	case "":
		// leave unset (auto)
	default:
		out.ToolChoice = request.ToolChoice
	}
	return out
}

func convertMessage(message llm.Message) []inputItem {
	switch message.Role {
	case llm.RoleSystem:
		text := joinText(message)
		if text == "" {
			return nil
		}
		return []inputItem{{Role: "system", Content: []contentPart{{Type: "input_text", Text: text}}}}
	case llm.RoleUser:
		parts := make([]contentPart, 0, len(message.Content))
		for _, part := range message.Content {
			switch part.Type {
			case llm.PartText:
				if part.Text != "" {
					parts = append(parts, contentPart{Type: "input_text", Text: part.Text})
				}
			case llm.PartImage:
				parts = append(parts, contentPart{Type: "input_image", ImageURL: "data:" + part.Mime + ";base64," + part.Data})
			}
		}
		return []inputItem{{Role: "user", Content: parts}}
	case llm.RoleAssistant:
		var out []inputItem
		var text []string
		flushText := func() {
			if len(text) == 0 {
				return
			}
			out = append(out, inputItem{Role: "assistant", Content: []contentPart{{Type: "output_text", Text: strings.Join(text, "")}}})
			text = nil
		}
		for _, part := range message.Content {
			switch part.Type {
			case llm.PartText:
				text = append(text, part.Text)
			case llm.PartToolCall:
				flushText()
				arguments, err := json.Marshal(part.Input)
				if err != nil {
					continue
				}
				out = append(out, inputItem{
					Type:      "function_call",
					CallID:    part.ToolCallID,
					Name:      part.ToolName,
					Arguments: string(arguments),
				})
				// PartReasoning is intentionally not replayed: the Responses
				// API requires a stable item id (and, with store:false, the
				// item's encrypted state) to accept a prior reasoning item
				// back, neither of which llm.ContentPart carries.
			}
		}
		flushText()
		return out
	case llm.RoleTool:
		var out []inputItem
		for _, part := range message.Content {
			if part.Type != llm.PartToolResult {
				continue
			}
			out = append(out, inputItem{Type: "function_call_output", CallID: part.ToolCallID, Output: part.Result})
		}
		return out
	}
	return nil
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

// =============================================================================
// Stream parsing
// =============================================================================

type toolAccumulator struct {
	callID    string
	name      string
	arguments string
}

// streamEvent captures the subset of fields this port's callers need across
// every `response.*` event type; unused fields for a given type simply stay
// zero. See TERMINAL_TYPES in the TS reference for which types end a stream.
type streamEvent struct {
	Type   string `json:"type"`
	Delta  string `json:"delta"`
	ItemID string `json:"item_id"`
	Item   *struct {
		Type      string  `json:"type"`
		ID        string  `json:"id"`
		CallID    string  `json:"call_id"`
		Name      string  `json:"name"`
		Arguments *string `json:"arguments"`
	} `json:"item"`
	Response *struct {
		IncompleteDetails *struct {
			Reason string `json:"reason"`
		} `json:"incomplete_details"`
		Usage *struct {
			InputTokens        int `json:"input_tokens"`
			OutputTokens       int `json:"output_tokens"`
			InputTokensDetails *struct {
				CachedTokens int `json:"cached_tokens"`
			} `json:"input_tokens_details"`
			OutputTokensDetails *struct {
				ReasoningTokens int `json:"reasoning_tokens"`
			} `json:"output_tokens_details"`
		} `json:"usage"`
		Error *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	} `json:"response"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

func readStream(reader io.Reader, emit func(llm.StreamEvent)) error {
	var usage llm.Usage
	var hasFunctionCall bool
	tools := map[string]*toolAccumulator{}

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
		var event streamEvent
		if err := json.Unmarshal([]byte(payload), &event); err != nil {
			continue
		}

		switch event.Type {
		case "response.output_text.delta":
			if event.Delta != "" {
				emit(llm.StreamEvent{Type: llm.EventTextDelta, Text: event.Delta})
			}
		case "response.reasoning_text.delta", "response.reasoning_summary.delta", "response.reasoning_summary_text.delta":
			if event.Delta != "" {
				emit(llm.StreamEvent{Type: llm.EventReasoningDelta, Text: event.Delta})
			}
		case "response.output_item.added":
			if event.Item != nil && event.Item.Type == "function_call" {
				id := event.Item.ID
				tools[id] = &toolAccumulator{callID: event.Item.CallID, name: event.Item.Name}
			}
		case "response.function_call_arguments.delta":
			if acc, ok := tools[event.ItemID]; ok {
				acc.arguments += event.Delta
			}
		case "response.output_item.done":
			if event.Item == nil || event.Item.Type != "function_call" {
				continue
			}
			hasFunctionCall = true
			acc, ok := tools[event.Item.ID]
			if !ok {
				acc = &toolAccumulator{callID: event.Item.CallID, name: event.Item.Name}
			}
			if event.Item.Arguments != nil {
				acc.arguments = *event.Item.Arguments
			}
			var input map[string]any
			if acc.arguments != "" {
				json.Unmarshal([]byte(acc.arguments), &input)
			}
			id := acc.callID
			if id == "" {
				id = event.Item.CallID
			}
			emit(llm.StreamEvent{Type: llm.EventToolCall, ToolCall: &llm.ToolCall{ID: id, Name: acc.name, Input: input}})
		case "response.completed", "response.incomplete":
			finish := "stop"
			if hasFunctionCall {
				finish = "tool-calls"
			}
			if event.Response != nil {
				if u := event.Response.Usage; u != nil {
					usage.Input = u.InputTokens
					usage.Output = u.OutputTokens
					if u.InputTokensDetails != nil {
						usage.CacheRead = u.InputTokensDetails.CachedTokens
					}
					if u.OutputTokensDetails != nil {
						usage.Reasoning = u.OutputTokensDetails.ReasoningTokens
					}
				}
				if d := event.Response.IncompleteDetails; d != nil {
					switch d.Reason {
					case "max_output_tokens":
						finish = "length"
					case "content_filter":
						finish = "content-filter"
					}
				}
			}
			emit(llm.StreamEvent{Type: llm.EventFinish, Finish: finish, Usage: usage})
			return nil
		case "response.failed":
			err := providerError(event, "openai-responses: response failed")
			emit(llm.StreamEvent{Type: llm.EventProviderError, Error: err})
			return err
		case "error":
			err := providerError(event, "openai-responses: stream error")
			emit(llm.StreamEvent{Type: llm.EventProviderError, Error: err})
			return err
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	// The stream ended without a terminal event (a truncated connection);
	// still surface whatever usage/finish state was gathered rather than
	// leaving the turn silently incomplete.
	finish := "stop"
	if hasFunctionCall {
		finish = "tool-calls"
	}
	emit(llm.StreamEvent{Type: llm.EventFinish, Finish: finish, Usage: usage})
	return nil
}

func providerError(event streamEvent, fallback string) error {
	message := event.Message
	code := event.Code
	if event.Response != nil && event.Response.Error != nil {
		if message == "" {
			message = event.Response.Error.Message
		}
		if code == "" {
			code = event.Response.Error.Code
		}
	}
	switch {
	case message != "" && code != "":
		return fmt.Errorf("openai-responses: %s: %s", code, message)
	case message != "":
		return fmt.Errorf("openai-responses: %s", message)
	case code != "":
		return fmt.Errorf("openai-responses: %s", code)
	default:
		return fmt.Errorf("openai-responses: %s", fallback)
	}
}

type APIError struct {
	StatusCode int
	Message    string `json:"message"`
}

func (e *APIError) Error() string {
	return fmt.Sprintf("openai-responses: %d %s", e.StatusCode, e.Message)
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
