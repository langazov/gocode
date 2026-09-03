package openairesponses

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
		if !strings.HasSuffix(r.URL.Path, "/responses") {
			t.Errorf("path = %q, want it to end in /responses", r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		var req chatRequest
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatalf("invalid request: %v", err)
		}
		if req.Model != "gpt-5" || !req.Stream {
			t.Errorf("unexpected request: %+v", req)
		}
		if req.Store == nil || *req.Store {
			t.Errorf("store = %v, want a false pointer", req.Store)
		}
		if len(req.Input) != 2 || req.Input[0].Role != "system" || req.Input[1].Role != "user" {
			t.Errorf("unexpected input: %+v", req.Input)
		}
		if len(req.Tools) != 1 || req.Tools[0].Name != "bash" {
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

// responsesStream is a realistic (if trimmed) event sequence: text, then a
// function call streamed via output_item.added + arguments.delta +
// output_item.done, then response.completed with usage. Matches the shapes
// observed against the real opencode/Zen gateway.
const responsesStream = `data: {"type":"response.output_item.added","item":{"type":"message","id":"msg_1"}}

data: {"type":"response.output_text.delta","item_id":"msg_1","delta":"Hel"}

data: {"type":"response.output_text.delta","item_id":"msg_1","delta":"lo"}

data: {"type":"response.output_item.added","item":{"type":"function_call","id":"item_1","call_id":"call_1","name":"bash"}}

data: {"type":"response.function_call_arguments.delta","item_id":"item_1","delta":"{\"comma"}

data: {"type":"response.function_call_arguments.delta","item_id":"item_1","delta":"nd\":\"ls\"}"}

data: {"type":"response.output_item.done","item":{"type":"function_call","id":"item_1","call_id":"call_1","name":"bash","arguments":"{\"command\":\"ls\"}"}}

data: {"type":"response.completed","response":{"id":"resp_1","usage":{"input_tokens":4,"output_tokens":9,"output_tokens_details":{"reasoning_tokens":2}}}}

data: [DONE]

`

func TestStream(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte(responsesStream))
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
	if finish.Finish != "tool-calls" || finish.Usage.Input != 4 || finish.Usage.Output != 9 || finish.Usage.Reasoning != 2 {
		t.Fatalf("unexpected finish: %+v", finish)
	}
}

func TestReasoningDelta(t *testing.T) {
	stream := `data: {"type":"response.reasoning_summary_text.delta","item_id":"item_1","delta":"thinking..."}

data: {"type":"response.completed","response":{"usage":{"input_tokens":1,"output_tokens":1}}}

`
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(stream))
	})
	var got []llm.StreamEvent
	err := client.Stream(context.Background(), llm.Request{ModelID: "gpt-5"}, func(e llm.StreamEvent) { got = append(got, e) })
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Type != llm.EventReasoningDelta || got[0].Text != "thinking..." {
		t.Fatalf("unexpected events: %+v", got)
	}
}

func TestFinishReasonMapping(t *testing.T) {
	cases := []struct {
		name  string
		event string
		want  string
	}{
		{"clean stop", `{"type":"response.completed","response":{}}`, "stop"},
		{"length", `{"type":"response.completed","response":{"incomplete_details":{"reason":"max_output_tokens"}}}`, "length"},
		{"content filter", `{"type":"response.completed","response":{"incomplete_details":{"reason":"content_filter"}}}`, "content-filter"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				w.Write([]byte("data: " + tc.event + "\n\n"))
			})
			var finish string
			err := client.Stream(context.Background(), llm.Request{ModelID: "gpt-5"}, func(e llm.StreamEvent) {
				if e.Type == llm.EventFinish {
					finish = e.Finish
				}
			})
			if err != nil {
				t.Fatal(err)
			}
			if finish != tc.want {
				t.Errorf("finish = %q, want %q", finish, tc.want)
			}
		})
	}
}

func TestResponseFailedEmitsProviderError(t *testing.T) {
	stream := `data: {"type":"response.failed","response":{"error":{"code":"rate_limit_exceeded","message":"Slow down"}}}

`
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(stream))
	})
	var gotProviderError bool
	err := client.Stream(context.Background(), llm.Request{ModelID: "gpt-5"}, func(e llm.StreamEvent) {
		if e.Type == llm.EventProviderError {
			gotProviderError = true
		}
	})
	if err == nil || !gotProviderError {
		t.Fatalf("expected a provider error, err=%v gotEvent=%v", err, gotProviderError)
	}
	if !strings.Contains(err.Error(), "rate_limit_exceeded") || !strings.Contains(err.Error(), "Slow down") {
		t.Errorf("error = %v, want it to name the code and message", err)
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
	converted := convertMessage(llm.ToolResultMessage("", "call_1", "bash", "output text", false))
	if len(converted) != 1 || converted[0].Type != "function_call_output" || converted[0].CallID != "call_1" || converted[0].Output != "output text" {
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
	converted := convertMessage(message)
	if len(converted) != 2 {
		t.Fatalf("expected a text item and a function_call item, got %d: %+v", len(converted), converted)
	}
	if converted[0].Role != "assistant" || len(converted[0].Content) != 1 || converted[0].Content[0].Text != "running" {
		t.Fatalf("unexpected text item: %+v", converted[0])
	}
	if converted[1].Type != "function_call" || converted[1].CallID != "call_1" || converted[1].Name != "bash" {
		t.Fatalf("unexpected function_call item: %+v", converted[1])
	}
	var input map[string]any
	if err := json.Unmarshal([]byte(converted[1].Arguments), &input); err != nil || input["command"] != "ls" {
		t.Fatalf("unexpected arguments: %q (err=%v)", converted[1].Arguments, err)
	}
}

func TestUserImageConversion(t *testing.T) {
	message := llm.Message{
		Role: llm.RoleUser,
		Content: []llm.ContentPart{
			{Type: llm.PartText, Text: "what is this"},
			{Type: llm.PartImage, Mime: "image/png", Data: "abc123"},
		},
	}
	converted := convertMessage(message)
	if len(converted) != 1 || len(converted[0].Content) != 2 {
		t.Fatalf("unexpected conversion: %+v", converted)
	}
	if converted[0].Content[0].Type != "input_text" || converted[0].Content[1].Type != "input_image" {
		t.Fatalf("unexpected content types: %+v", converted[0].Content)
	}
	if converted[0].Content[1].ImageURL != "data:image/png;base64,abc123" {
		t.Errorf("image_url = %q", converted[0].Content[1].ImageURL)
	}
}
