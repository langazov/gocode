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
		HTTP:    llm.NewStreamHTTPClient(),
	}
}

// Stream implements llm.StreamClient for the Chat Completions API.
func (c *Client) Stream(ctx context.Context, request llm.Request, emit func(llm.StreamEvent)) error {
	request.ModelID = c.Options.Model(request.ModelID)
	// An endpoint that honours Anthropic-style cache_control markers on Chat
	// Completions blocks — OpenRouter, which translates them per route — gets
	// the same breakpoint placement the anthropic adapter applies. Every other
	// openai-compatible endpoint rejects unknown content-block fields, so the
	// markers stay off the wire unless the provider opts in (see
	// llm.Options.CacheControlBlocks).
	if c.Options.CacheControlBlocks {
		request = llm.ApplyCachePolicy(request)
	}
	body, err := convertRequest(request, c.Options.CacheControlBlocks)
	if err != nil {
		return err
	}
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
		err := parseError(res, data)
		emit(llm.StreamEvent{Type: llm.EventProviderError, Error: err})
		return err
	}
	stream := llm.NewIdleReader(res.Body, llm.StreamIdleTimeout)
	defer stream.Close()
	return readStream(stream, emit)
}

func (c *Client) baseURL() string {
	if c.BaseURL == "" {
		return DefaultBaseURL
	}
	return strings.TrimRight(c.BaseURL, "/")
}

func (c *Client) endpoint(model string) string {
	base := c.baseURL()
	return c.Options.URL(base, model, base+"/chat/completions")
}

// chatMessage's Content is `any` because the Chat Completions API accepts
// either a plain string or an array of typed parts, and only the array form can
// carry an image. Plain text still marshals as a string so requests without
// attachments are byte-identical to what they were.
type chatMessage struct {
	Role       string     `json:"role"`
	Content    any        `json:"content,omitempty"`
	ToolCalls  []toolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	Name       string     `json:"name,omitempty"`
}

// contentPart is one element of the array form.
type contentPart struct {
	Type         string        `json:"type"`
	Text         string        `json:"text,omitempty"`
	ImageURL     *contentImage `json:"image_url,omitempty"`
	CacheControl *cacheControl `json:"cache_control,omitempty"`
}

type contentImage struct {
	URL string `json:"url"`
}

// cacheControl is the Anthropic-style prompt-cache directive, carried on a
// content block or tool definition. Chat Completions has no native form, but
// OpenRouter accepts the Anthropic shape and translates it to whatever the
// routed provider understands — so it is emitted only when the endpoint opted
// in via llm.Options.CacheControlBlocks.
type cacheControl struct {
	Type string `json:"type"`
	TTL  string `json:"ttl,omitempty"`
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
	Type         string        `json:"type"`
	Function     functionSpec  `json:"function"`
	CacheControl *cacheControl `json:"cache_control,omitempty"`
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
	TopP            *float64      `json:"top_p,omitempty"`
	StreamOpts      *streamOpts   `json:"stream_options,omitempty"`
	ReasoningEffort string        `json:"reasoning_effort,omitempty"`
}

type streamOpts struct {
	IncludeUsage bool `json:"include_usage"`
}

func convertRequest(request llm.Request, cacheControlBlocks bool) (chatRequest, error) {
	out := chatRequest{
		Model:       request.ModelID,
		Stream:      true,
		MaxTokens:   request.MaxTokens,
		Temperature: request.Temperature,
		TopP:        request.TopP,
		StreamOpts:  &streamOpts{IncludeUsage: true},
	}
	if effort, ok := request.Reasoning["reasoning_effort"].(string); ok {
		out.ReasoningEffort = effort
	}
	// The system prompt lowers to one message per non-empty entry. The
	// breakpoint covers the system prompt as a whole, so — matching the
	// anthropic adapter — it rides only the final entry rather than each one:
	// a marker per entry would burn the breakpoint budget OpenRouter enforces
	// (four, Anthropic's cap) on a prefix a single trailing marker names just
	// as well.
	systemEntries := make([]string, 0, len(request.System))
	for _, system := range request.System {
		if system != "" {
			systemEntries = append(systemEntries, system)
		}
	}
	for i, system := range systemEntries {
		var hint *llm.CacheHint
		if i == len(systemEntries)-1 {
			hint = request.SystemCache
		}
		out.Messages = append(out.Messages, chatMessage{Role: "system", Content: systemContent(system, hint, cacheControlBlocks)})
	}
	for _, message := range request.Messages {
		converted, err := convertMessage(message, cacheControlBlocks)
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
		td := toolDef{
			Type: "function",
			Function: functionSpec{
				Name:        tool.Name,
				Description: tool.Description,
				Parameters:  schema,
			},
		}
		if cacheControlBlocks {
			td.CacheControl = cacheControlDirective(tool.Cache)
		}
		out.Tools = append(out.Tools, td)
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

func convertMessage(message llm.Message, cacheControlBlocks bool) ([]chatMessage, error) {
	switch message.Role {
	case llm.RoleSystem:
		return []chatMessage{{Role: "system", Content: systemContent(joinText(message), nil, cacheControlBlocks)}}, nil
	case llm.RoleUser:
		return []chatMessage{{Role: "user", Content: userContent(message, cacheControlBlocks)}}, nil
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
			// Chat Completions pairs a tool result to its call by ID and
			// rejects a tool message without one ("tool_call_id must be
			// provided for tool messages"). ToolCallID carries omitempty,
			// so an empty one is dropped rather than sent as "" — turning
			// one malformed history row into a 400 on every later request.
			// A result with no ID has no call to answer; skipping it keeps
			// the request well-formed.
			if part.ToolCallID == "" {
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

// userContent returns a plain string when the message is text only and carries
// no cache marker, and the typed-parts array when it carries an image or a
// cache breakpoint. The array form is the only shape that can carry either.
func userContent(message llm.Message, cacheControlBlocks bool) any {
	hasImage := false
	hasCache := false
	for _, part := range message.Content {
		if part.Type == llm.PartImage {
			hasImage = true
		}
		if cacheControlBlocks && part.Cache != nil {
			hasCache = true
		}
	}
	if !hasImage && !hasCache {
		return joinText(message)
	}
	parts := make([]contentPart, 0, len(message.Content))
	for _, part := range message.Content {
		switch part.Type {
		case llm.PartText:
			if part.Text != "" {
				parts = append(parts, contentPart{Type: "text", Text: part.Text})
			}
		case llm.PartImage:
			parts = append(parts, contentPart{
				Type:     "image_url",
				ImageURL: &contentImage{URL: "data:" + part.Mime + ";base64," + part.Data},
			})
		}
		if cacheControlBlocks && part.Cache != nil {
			parts[len(parts)-1].CacheControl = cacheControlDirective(part.Cache)
		}
	}
	return parts
}

// systemContent lowers one system-prompt entry. The marker rides the array
// form only when one is placed; a plain string stays a plain string so a
// request without breakpoints is byte-identical to what it always was.
func systemContent(text string, hint *llm.CacheHint, cacheControlBlocks bool) any {
	if !cacheControlBlocks || hint == nil {
		return text
	}
	return []contentPart{{Type: "text", Text: text, CacheControl: cacheControlDirective(hint)}}
}

// cacheControlDirective renders a cache hint as the Anthropic-style directive
// OpenRouter accepts on Chat Completions blocks. TTLs below the hour bucket as
// the provider default (5 minutes); nothing is emitted for a nil hint.
func cacheControlDirective(hint *llm.CacheHint) *cacheControl {
	if hint == nil {
		return nil
	}
	if hint.Extended() {
		return &cacheControl{Type: "ephemeral", TTL: "1h"}
	}
	return &cacheControl{Type: "ephemeral"}
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
			// A malformed tail can leave an accumulator with no name, no
			// id, or neither — a tool_calls delta whose function never
			// arrived, or a gateway that emitted an empty call. Flushing
			// one forwarded a call with an empty ID and name downstream:
			// the registry failed it as `unknown tool ""`, and the empty
			// callID it settled under then poisoned every later request
			// (see assistantToLLM). Dropping it here keeps a glitch from
			// becoming a malformed message the provider will reject on
			// replay.
			if acc.name == "" || acc.id == "" {
				continue
			}
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
			cacheRead := chunk.Usage.PromptCacheHitTokens
			if details := chunk.Usage.PromptTokensDetails; details != nil && details.CachedTokens != 0 {
				cacheRead = details.CachedTokens
			}
			var reasoning int
			if details := chunk.Usage.CompletionTokensDetails; details != nil {
				reasoning = details.ReasoningTokens
			}
			usage = llm.Usage{
				Input:     llm.SubtractTokens(chunk.Usage.PromptTokens, cacheRead),
				Output:    llm.SubtractTokens(chunk.Usage.CompletionTokens, reasoning),
				Reasoning: reasoning,
				CacheRead: cacheRead,
			}
			// No CacheWrite: an openai-compatible endpoint caches implicitly
			// and bills the write at the input rate, so there is no separate
			// counter to read. Anthropic's explicit breakpoints are the only
			// thing that fills that bucket.
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
			// openai-compatible backends; TS's gocode reads whichever of
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
	// PromptTokens and CompletionTokens are inclusive totals; the details
	// objects break out subsets of each. Dropping the details is what made
	// every openai-compatible provider report zero cached and zero reasoning
	// tokens no matter what it actually returned — readStream lowers them to
	// llm.Usage's disjoint buckets.
	Usage *struct {
		PromptTokens        int `json:"prompt_tokens"`
		CompletionTokens    int `json:"completion_tokens"`
		PromptTokensDetails *struct {
			CachedTokens int `json:"cached_tokens"`
		} `json:"prompt_tokens_details"`
		CompletionTokensDetails *struct {
			ReasoningTokens int `json:"reasoning_tokens"`
		} `json:"completion_tokens_details"`
		// DeepSeek predates prompt_tokens_details and reports the cache split
		// as a pair of top-level fields instead. Only the hit side maps: its
		// miss counter is the non-cached remainder, which is what subtracting
		// the hits already yields.
		PromptCacheHitTokens int `json:"prompt_cache_hit_tokens"`
	} `json:"usage"`
}

type APIError struct {
	StatusCode int
	Message    string `json:"message"`
}

func (e *APIError) Error() string {
	return fmt.Sprintf("openai: %d %s", e.StatusCode, e.Message)
}

// parseError lowers a failed HTTP exchange to an error, carrying the
// provider's own retry hint on the one status where waiting is the fix: a 429
// becomes llm.RateLimitError with the wait the response stated — its
// Retry-After header, or the "Please try again in 1.728s" prose its message
// body carries when the header is absent — so the session runner can hold
// the turn and re-run it once the window reopens.
func parseError(res *http.Response, data []byte) error {
	status := res.StatusCode
	var envelope struct {
		Error APIError `json:"error"`
	}
	if err := json.Unmarshal(data, &envelope); err == nil && envelope.Error.Message != "" {
		if status == http.StatusTooManyRequests {
			return rateLimited("openai", res, envelope.Error.Message)
		}
		envelope.Error.StatusCode = status
		return &envelope.Error
	}
	if status == http.StatusTooManyRequests {
		return rateLimited("openai", res, strings.TrimSpace(string(data)))
	}
	return &APIError{StatusCode: status, Message: string(data)}
}

// rateLimited builds the error for a 429 from everything the response said
// about the wait: the standard header, then the prose some providers write
// into the message body. Shared detection order with every other client.
func rateLimited(provider string, res *http.Response, message string) error {
	delay, known := llm.RetryAfter(res.Header.Get("Retry-After"), message)
	return &llm.RateLimitError{
		Provider:        provider,
		Message:         message,
		RetryAfter:      delay,
		RetryAfterKnown: known,
	}
}
