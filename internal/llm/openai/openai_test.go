package openai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/langazov/gocode-go/internal/llm"
)

func newTestClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	client := New("test-key")
	client.BaseURL = srv.URL
	return client
}

func TestRequestShape(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("Authorization = %q", got)
		}
		if !strings.HasSuffix(r.URL.Path, "/chat/completions") {
			t.Errorf("path = %q", r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		var req chatRequest
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatalf("invalid request: %v", err)
		}
		if req.Model != "gpt-5" || !req.Stream {
			t.Errorf("unexpected request: %+v", req)
		}
		if len(req.Messages) != 2 || req.Messages[0].Role != "system" || req.Messages[1].Role != "user" {
			t.Errorf("unexpected messages: %+v", req.Messages)
		}
		if len(req.Tools) != 1 || req.Tools[0].Function.Name != "bash" {
			t.Errorf("unexpected tools: %+v", req.Tools)
		}
		w.Write([]byte("data: [DONE]\n\n"))
	})
	err := client.Stream(context.Background(), llm.Request{
		ProviderID: "openai",
		ModelID:    "gpt-5",
		System:     []string{"You are gocode."},
		Messages:   []llm.Message{llm.UserText("m1", "run ls")},
		Tools: []llm.ToolDefinition{{
			Name:        "bash",
			Description: "run a command",
			InputSchema: map[string]any{"type": "object"},
		}},
	}, func(event llm.StreamEvent) {})
	if err != nil {
		t.Fatal(err)
	}
}

const openAIStream = `data: {"choices":[{"delta":{"content":"Hel"}}]}

data: {"choices":[{"delta":{"content":"lo"}}]}

data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"bash","arguments":"{\"comma"}}]}}]}

data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"nd\":\"ls\"}"}}]}}],"usage":{"prompt_tokens":4,"completion_tokens":9}}

data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":4,"completion_tokens":9}}

data: [DONE]

`

func TestStream(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte(openAIStream))
	})
	var events []llm.StreamEvent
	request := llm.Request{
		ProviderID: "openai",
		ModelID:    "gpt-5",
		Messages:   []llm.Message{llm.UserText("m1", "run ls")},
	}
	if err := client.Stream(context.Background(), request, func(event llm.StreamEvent) {
		events = append(events, event)
	}); err != nil {
		t.Fatal(err)
	}

	var types []string
	for _, event := range events {
		types = append(types, event.Type)
	}
	want := []string{
		llm.EventTextDelta,
		llm.EventTextDelta,
		llm.EventToolCall,
		llm.EventFinish,
	}
	if len(types) != len(want) {
		t.Fatalf("expected events %v, got %v", want, types)
	}
	for i := range want {
		if types[i] != want[i] {
			t.Fatalf("event %d: expected %s, got %s", i, want[i], types[i])
		}
	}

	toolCall := events[2].ToolCall
	if toolCall == nil || toolCall.ID != "call_1" || toolCall.Name != "bash" {
		t.Fatalf("unexpected tool call: %+v", toolCall)
	}
	if toolCall.Input["command"] != "ls" {
		t.Fatalf("expected accumulated arguments, got %v", toolCall.Input)
	}
	finish := events[len(events)-1]
	if finish.Finish != "tool_calls" || finish.Usage.Input != 4 || finish.Usage.Output != 9 {
		t.Fatalf("unexpected finish: %+v", finish)
	}
}

func TestAPIError(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":{"message":"invalid api key"}}`))
	})
	var gotProviderError bool
	err := client.Stream(context.Background(), llm.Request{ModelID: "gpt-5"}, func(event llm.StreamEvent) {
		if event.Type == llm.EventProviderError {
			gotProviderError = true
		}
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if !gotProviderError {
		t.Fatal("expected a provider-error event")
	}
	apiErr, ok := err.(*APIError)
	if !ok || apiErr.StatusCode != 401 {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestToolMessageConversion(t *testing.T) {
	converted, err := convertMessage(llm.ToolResultMessage("", "call_1", "bash", "output text", false))
	if err != nil {
		t.Fatal(err)
	}
	if len(converted) != 1 || converted[0].Role != "tool" || converted[0].ToolCallID != "call_1" || converted[0].Content != "output text" {
		t.Fatalf("unexpected tool conversion: %+v", converted)
	}
}

func TestAssistantToolCallConversion(t *testing.T) {
	message := llm.Message{
		Role: llm.RoleAssistant,
		Content: []llm.ContentPart{
			{Type: llm.PartText, Text: "running"},
			{Type: llm.PartToolCall, ToolCallID: "call_1", ToolName: "bash", Input: map[string]any{"command": "ls"}},
		},
	}
	converted, err := convertMessage(message)
	if err != nil {
		t.Fatal(err)
	}
	if len(converted) != 1 {
		t.Fatalf("expected 1 message, got %d", len(converted))
	}
	msg := converted[0]
	if msg.Content != "running" || len(msg.ToolCalls) != 1 {
		t.Fatalf("unexpected assistant conversion: %+v", msg)
	}
	if msg.ToolCalls[0].ID != "call_1" || msg.ToolCalls[0].Function.Name != "bash" {
		t.Fatalf("unexpected tool call: %+v", msg.ToolCalls[0])
	}
}
