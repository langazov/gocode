// Package anthropic implements a minimal Anthropic Messages API client with
// SSE streaming, replacing the @ai-sdk/anthropic dependency from the
// TypeScript implementation.
package anthropic

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

const (
	DefaultBaseURL = "https://api.anthropic.com"
	apiVersion     = "2023-06-01"
	// betaHeader matches packages/opencode/src/provider/provider.ts which
	// enables interleaved thinking and fine-grained tool streaming.
	betaHeader = "interleaved-thinking-2025-05-14,fine-grained-tool-streaming-2025-05-14"
)

type Client struct {
	APIKey  string
	BaseURL string
	HTTP    *http.Client
	// Options carries provider-specific headers, body fields, model-id
	// remapping and request signing, supplied by the provider transform layer.
	// Bedrock and Vertex reach the Anthropic wire format through it.
	Options llm.Options
}

func New(apiKey string) *Client {
	return &Client{
		APIKey:  apiKey,
		BaseURL: DefaultBaseURL,
		HTTP:    &http.Client{Timeout: 10 * time.Minute},
	}
}

// Source is the base64 payload of an image or document block.
type Source struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type"`
	Data      string `json:"data"`
}

type ContentBlock struct {
	Type      string          `json:"type"`
	Source    *Source         `json:"source,omitempty"`
	Text      string          `json:"text,omitempty"`
	Thinking  string          `json:"thinking,omitempty"`
	Signature string          `json:"signature,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   string          `json:"content,omitempty"`
	IsError   bool            `json:"is_error,omitempty"`
}

type Message struct {
	Role    string         `json:"role"`
	Content []ContentBlock `json:"content"`
}

type Tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	InputSchema map[string]any `json:"input_schema"`
}

type ToolChoice struct {
	Type string `json:"type"`
	Name string `json:"name,omitempty"`
}

type Request struct {
	Model       string      `json:"model"`
	MaxTokens   int         `json:"max_tokens"`
	Messages    []Message   `json:"messages"`
	System      string      `json:"system,omitempty"`
	Temperature *float64    `json:"temperature,omitempty"`
	TopP        *float64    `json:"top_p,omitempty"`
	Stream      bool        `json:"stream,omitempty"`
	StopSeqs    []string    `json:"stop_sequences,omitempty"`
	Thinking    *Thinking   `json:"thinking,omitempty"`
	Tools       []Tool      `json:"tools,omitempty"`
	ToolChoice  *ToolChoice `json:"tool_choice,omitempty"`
}

type Thinking struct {
	Type         string `json:"type"`
	BudgetTokens int    `json:"budget_tokens,omitempty"`
}

type Usage struct {
	InputTokens      int `json:"input_tokens"`
	OutputTokens     int `json:"output_tokens"`
	CacheReadTokens  int `json:"cache_read_input_tokens,omitempty"`
	CacheWriteTokens int `json:"cache_creation_input_tokens,omitempty"`
}

type Response struct {
	ID         string         `json:"id"`
	Type       string         `json:"type"`
	Role       string         `json:"role"`
	Model      string         `json:"model"`
	Content    []ContentBlock `json:"content"`
	StopReason string         `json:"stop_reason"`
	Usage      Usage          `json:"usage"`
}

type APIError struct {
	StatusCode int
	Type       string `json:"type"`
	Message    string `json:"message"`
}

func (e *APIError) Error() string {
	return fmt.Sprintf("anthropic: %d %s: %s", e.StatusCode, e.Type, e.Message)
}

// StreamHandler receives callbacks as SSE events arrive.
type StreamHandler struct {
	OnText     func(text string)
	OnThinking func(thinking string)
	OnToolUse  func(id, name string, input json.RawMessage)
	OnMessage  func(response *Response)
	OnError    func(err error)
}

func (c *Client) newRequest(ctx context.Context, req Request) (*http.Request, error) {
	req.Model = c.Options.Model(req.Model)
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	body, err = c.Options.MergeBody(body)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.messagesURL(req.Model), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("anthropic-version", apiVersion)
	httpReq.Header.Set("anthropic-beta", betaHeader)
	signed, err := c.Options.Authenticate(httpReq, body)
	if err != nil {
		return nil, err
	}
	if !signed {
		httpReq.Header.Set("x-api-key", c.APIKey)
	}
	c.Options.ApplyHeaders(httpReq)
	return httpReq, nil
}

func (c *Client) baseURL() string {
	if c.BaseURL == "" {
		return DefaultBaseURL
	}
	return strings.TrimRight(c.BaseURL, "/")
}

// messagesURL builds the Messages endpoint. Base URLs come in two
// conventions: host-style ("https://api.anthropic.com") and version-style
// ("https://api.minimax.io/anthropic/v1", as shipped by the models.dev
// catalog). The version is appended only when absent.
func (c *Client) messagesURL(model string) string {
	base := c.baseURL()
	if !strings.HasSuffix(base, "/v1") {
		base += "/v1"
	}
	return c.Options.URL(c.baseURL(), model, base+"/messages")
}

// Complete performs a non-streaming request and returns the full response.
func (c *Client) Complete(ctx context.Context, req Request) (*Response, error) {
	req.Stream = false
	httpReq, err := c.newRequest(ctx, req)
	if err != nil {
		return nil, err
	}
	res, err := c.Options.HTTPClient(c.HTTP).Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	data, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, err
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, parseError(res.StatusCode, data)
	}
	var response Response
	if err := json.Unmarshal(data, &response); err != nil {
		return nil, err
	}
	return &response, nil
}

// streamHandler performs a streaming request, invoking handler callbacks per
// event. Stream (the llm.StreamClient entry point) wraps this.
func (c *Client) streamHandler(ctx context.Context, req Request, handler StreamHandler) (*Response, error) {
	req.Stream = true
	httpReq, err := c.newRequest(ctx, req)
	if err != nil {
		return nil, err
	}
	res, err := c.Options.HTTPClient(c.HTTP).Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		data, _ := io.ReadAll(res.Body)
		return nil, parseError(res.StatusCode, data)
	}
	return readSSE(res.Body, handler)
}

func parseError(status int, data []byte) error {
	var envelope struct {
		Error APIError `json:"error"`
	}
	if err := json.Unmarshal(data, &envelope); err == nil && envelope.Error.Message != "" {
		envelope.Error.StatusCode = status
		return &envelope.Error
	}
	return &APIError{StatusCode: status, Type: "unknown", Message: string(data)}
}

// readSSE parses the Anthropic event stream. Events are newline-separated
// "data: {json}" lines; the event type lives inside the JSON payload.
func readSSE(reader io.Reader, handler StreamHandler) (*Response, error) {
	var final *Response
	var assembled []ContentBlock
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
		var event struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal([]byte(payload), &event); err != nil {
			continue
		}
		switch event.Type {
		case "message_start":
			var e struct {
				Message Response `json:"message"`
			}
			if json.Unmarshal([]byte(payload), &e) == nil {
				final = &e.Message
			}
		case "content_block_start":
			var e struct {
				Index        int          `json:"index"`
				ContentBlock ContentBlock `json:"content_block"`
			}
			if json.Unmarshal([]byte(payload), &e) == nil {
				assembled = grow(assembled, e.Index)
				assembled[e.Index] = e.ContentBlock
				// tool_use input arrives via input_json_delta; the full value is
				// emitted at content_block_stop.
				if e.ContentBlock.Type == "tool_use" {
					assembled[e.Index].Input = nil
				}
			}
		case "content_block_delta":
			var e struct {
				Index int `json:"index"`
				Delta struct {
					Type        string `json:"type"`
					Text        string `json:"text"`
					Thinking    string `json:"thinking"`
					PartialJSON string `json:"partial_json"`
				} `json:"delta"`
			}
			if json.Unmarshal([]byte(payload), &e) != nil {
				continue
			}
			assembled = grow(assembled, e.Index)
			switch e.Delta.Type {
			case "text_delta":
				assembled[e.Index].Text += e.Delta.Text
				if handler.OnText != nil {
					handler.OnText(e.Delta.Text)
				}
			case "thinking_delta":
				assembled[e.Index].Thinking += e.Delta.Thinking
				if handler.OnThinking != nil {
					handler.OnThinking(e.Delta.Thinking)
				}
			case "input_json_delta":
				assembled[e.Index].Input = append(assembled[e.Index].Input, e.Delta.PartialJSON...)
			}
		case "content_block_stop":
			var e struct {
				Index int `json:"index"`
			}
			if json.Unmarshal([]byte(payload), &e) == nil && e.Index < len(assembled) {
				block := assembled[e.Index]
				if block.Type == "tool_use" && handler.OnToolUse != nil {
					handler.OnToolUse(block.ID, block.Name, block.Input)
				}
			}
		case "message_delta":
			var e struct {
				Delta struct {
					StopReason string `json:"stop_reason"`
				} `json:"delta"`
				Usage Usage `json:"usage"`
			}
			if json.Unmarshal([]byte(payload), &e) == nil && final != nil {
				if e.Delta.StopReason != "" {
					final.StopReason = e.Delta.StopReason
				}
				if e.Usage.OutputTokens != 0 {
					final.Usage.OutputTokens = e.Usage.OutputTokens
				}
				if e.Usage.CacheReadTokens != 0 {
					final.Usage.CacheReadTokens = e.Usage.CacheReadTokens
				}
				if e.Usage.CacheWriteTokens != 0 {
					final.Usage.CacheWriteTokens = e.Usage.CacheWriteTokens
				}
			}
		case "error":
			var e struct {
				Error APIError `json:"error"`
			}
			if json.Unmarshal([]byte(payload), &e) == nil {
				if handler.OnError != nil {
					handler.OnError(&e.Error)
				}
				return final, &e.Error
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return final, err
	}
	if final != nil {
		final.Content = assembled
		if handler.OnMessage != nil {
			handler.OnMessage(final)
		}
	}
	return final, nil
}

func grow(blocks []ContentBlock, index int) []ContentBlock {
	for len(blocks) <= index {
		blocks = append(blocks, ContentBlock{})
	}
	return blocks
}
