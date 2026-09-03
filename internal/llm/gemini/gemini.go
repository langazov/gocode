// Package gemini implements a Google Generative Language (Gemini) client.
package gemini

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

const DefaultBaseURL = "https://generativelanguage.googleapis.com/v1beta"

type Client struct {
	APIKey  string
	BaseURL string
	HTTP    *http.Client
	// Options carries provider-specific headers, body fields, model-id
	// remapping and request signing, supplied by the provider transform layer.
	// Vertex reaches the Gemini wire format through it.
	Options llm.Options
}

func New(apiKey string) *Client {
	return &Client{
		APIKey:  apiKey,
		BaseURL: DefaultBaseURL,
		HTTP:    &http.Client{Timeout: 10 * time.Minute},
	}
}

// Stream implements llm.StreamClient for the Gemini streamGenerateContent API.
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
	fallback := fmt.Sprintf("%s/models/%s:streamGenerateContent?alt=sse", c.baseURL(), request.ModelID)
	endpoint := c.Options.URL(c.baseURL(), request.ModelID, fallback)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	signed, err := c.Options.Authenticate(httpReq, payload)
	if err != nil {
		return err
	}
	if !signed && c.APIKey != "" {
		httpReq.Header.Set("x-goog-api-key", c.APIKey)
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

type inlineData struct {
	MimeType string `json:"mimeType"`
	Data     string `json:"data"`
}

type part struct {
	Text             string            `json:"text,omitempty"`
	InlineData       *inlineData       `json:"inlineData,omitempty"`
	Thought          bool              `json:"thought,omitempty"`
	FunctionCall     *functionCallPart `json:"functionCall,omitempty"`
	FunctionResponse *funcResponsePart `json:"functionResponse,omitempty"`
}

type functionCallPart struct {
	Name string         `json:"name"`
	Args map[string]any `json:"args,omitempty"`
}

type funcResponsePart struct {
	Name     string         `json:"name"`
	Response map[string]any `json:"response"`
}

type content struct {
	Role  string `json:"role,omitempty"`
	Parts []part `json:"parts"`
}

type functionDeclaration struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters,omitempty"`
}

type generateRequest struct {
	Contents          []content        `json:"contents"`
	SystemInstruction *content         `json:"systemInstruction,omitempty"`
	Tools             []map[string]any `json:"tools,omitempty"`
	GenerationConfig  map[string]any   `json:"generationConfig,omitempty"`
}

func convertRequest(request llm.Request) generateRequest {
	out := generateRequest{}
	var systemText []string
	for _, system := range request.System {
		if system != "" {
			systemText = append(systemText, system)
		}
	}
	if len(systemText) > 0 {
		out.SystemInstruction = &content{Parts: []part{{Text: strings.Join(systemText, "\n\n")}}}
	}
	for _, message := range request.Messages {
		out.Contents = append(out.Contents, convertMessage(message)...)
	}
	if len(request.Tools) > 0 {
		var decls []functionDeclaration
		for _, tool := range request.Tools {
			schema := tool.InputSchema
			if schema == nil {
				schema = map[string]any{"type": "object", "properties": map[string]any{}}
			}
			decls = append(decls, functionDeclaration{
				Name:        tool.Name,
				Description: tool.Description,
				Parameters:  schema,
			})
		}
		out.Tools = append(out.Tools, map[string]any{"functionDeclarations": decls})
	}
	config := map[string]any{}
	if request.MaxTokens > 0 {
		config["maxOutputTokens"] = request.MaxTokens
	}
	if request.ToolChoice == "none" {
		config["toolConfig"] = map[string]any{"functionCallingConfig": map[string]any{"mode": "NONE"}}
	}
	// thinkingConfig comes from a selected reasoning variant (see
	// internal/provider.ReasoningVariants); {"includeThoughts":true, ...}
	// makes the model stream its thinking as parts with "thought":true,
	// read back in readStream below.
	if thinking, ok := request.Reasoning["thinkingConfig"]; ok {
		config["thinkingConfig"] = thinking
	}
	if len(config) > 0 {
		out.GenerationConfig = config
	}
	return out
}

func convertMessage(message llm.Message) []content {
	switch message.Role {
	case llm.RoleSystem:
		return nil
	case llm.RoleUser:
		parts := []part{}
		if text := joinText(message); text != "" {
			parts = append(parts, part{Text: text})
		}
		for _, p := range message.Content {
			if p.Type == llm.PartImage {
				parts = append(parts, part{InlineData: &inlineData{MimeType: p.Mime, Data: p.Data}})
			}
		}
		if len(parts) == 0 {
			parts = append(parts, part{Text: ""})
		}
		return []content{{Role: "user", Parts: parts}}
	case llm.RoleAssistant:
		var parts []part
		var text []string
		for _, p := range message.Content {
			switch p.Type {
			case llm.PartText:
				text = append(text, p.Text)
			case llm.PartToolCall:
				parts = append(parts, part{FunctionCall: &functionCallPart{Name: p.ToolName, Args: p.Input}})
			}
		}
		if len(text) > 0 {
			parts = append([]part{{Text: strings.Join(text, "")}}, parts...)
		}
		return []content{{Role: "model", Parts: parts}}
	case llm.RoleTool:
		var parts []part
		for _, p := range message.Content {
			if p.Type != llm.PartToolResult {
				continue
			}
			parts = append(parts, part{FunctionResponse: &funcResponsePart{
				Name:     p.ToolName,
				Response: map[string]any{"result": p.Result},
			}})
		}
		return []content{{Role: "user", Parts: parts}}
	}
	return nil
}

func joinText(message llm.Message) string {
	var parts []string
	for _, p := range message.Content {
		if p.Type == llm.PartText {
			parts = append(parts, p.Text)
		}
	}
	return strings.Join(parts, "")
}

func readStream(reader io.Reader, emit func(llm.StreamEvent)) error {
	var usage llm.Usage
	var finish string
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
		if chunk.UsageMetadata != nil {
			usage = llm.Usage{
				Input:  chunk.UsageMetadata.PromptTokenCount,
				Output: chunk.UsageMetadata.CandidatesTokenCount,
			}
		}
		for _, candidate := range chunk.Candidates {
			for _, p := range candidate.Content.Parts {
				if p.Text != "" {
					if p.Thought {
						emit(llm.StreamEvent{Type: llm.EventReasoningDelta, Text: p.Text})
					} else {
						emit(llm.StreamEvent{Type: llm.EventTextDelta, Text: p.Text})
					}
				}
				if p.FunctionCall != nil {
					emit(llm.StreamEvent{Type: llm.EventToolCall, ToolCall: &llm.ToolCall{
						ID:    p.FunctionCall.Name,
						Name:  p.FunctionCall.Name,
						Input: p.FunctionCall.Args,
					}})
				}
			}
			if candidate.FinishReason != "" {
				finish = strings.ToLower(candidate.FinishReason)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	emit(llm.StreamEvent{Type: llm.EventFinish, Finish: finish, Usage: usage})
	return nil
}

type streamChunk struct {
	Candidates []struct {
		Content struct {
			Parts []part `json:"parts"`
		} `json:"content"`
		FinishReason string `json:"finishReason"`
	} `json:"candidates"`
	UsageMetadata *struct {
		PromptTokenCount     int `json:"promptTokenCount"`
		CandidatesTokenCount int `json:"candidatesTokenCount"`
	} `json:"usageMetadata"`
}

type APIError struct {
	StatusCode int
	Message    string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("gemini: %d %s", e.StatusCode, e.Message)
}

func parseError(status int, data []byte) error {
	var envelope struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(data, &envelope); err == nil && envelope.Error.Message != "" {
		return &APIError{StatusCode: status, Message: envelope.Error.Message}
	}
	return &APIError{StatusCode: status, Message: string(data)}
}
