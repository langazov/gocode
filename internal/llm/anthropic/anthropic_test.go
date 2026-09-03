package anthropic

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

func newTestServer(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	client := New("test-key")
	client.BaseURL = srv.URL
	return client
}

func assertHeaders(t *testing.T, r *http.Request) {
	t.Helper()
	if got := r.Header.Get("x-api-key"); got != "test-key" {
		t.Errorf("x-api-key = %q", got)
	}
	if got := r.Header.Get("anthropic-version"); got != apiVersion {
		t.Errorf("anthropic-version = %q", got)
	}
	if got := r.Header.Get("anthropic-beta"); got != betaHeader {
		t.Errorf("anthropic-beta = %q", got)
	}
}

func TestComplete(t *testing.T) {
	client := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		assertHeaders(t, r)
		body, _ := io.ReadAll(r.Body)
		var req Request
		if err := json.Unmarshal(body, &req); err != nil {
			t.Errorf("invalid request body: %v", err)
		}
		if req.Stream {
			t.Errorf("expected stream=false for Complete")
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{
			"id": "msg_1", "type": "message", "role": "assistant", "model": "claude-sonnet-4-5",
			"content": [{"type": "text", "text": "Hello!"}],
			"stop_reason": "end_turn",
			"usage": {"input_tokens": 10, "output_tokens": 3}
		}`))
	})
	res, err := client.Complete(context.Background(), Request{
		Model:     "claude-sonnet-4-5",
		MaxTokens: 1024,
		Messages:  []Message{{Role: "user", Content: []ContentBlock{{Type: "text", Text: "hi"}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.ID != "msg_1" || res.StopReason != "end_turn" {
		t.Fatalf("unexpected response: %+v", res)
	}
	if len(res.Content) != 1 || res.Content[0].Text != "Hello!" {
		t.Fatalf("unexpected content: %+v", res.Content)
	}
}

const sseStream = `event: message_start
data: {"type":"message_start","message":{"id":"msg_2","type":"message","role":"assistant","model":"claude-sonnet-4-5","content":[],"usage":{"input_tokens":25,"output_tokens":1}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"Let me think."}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: content_block_start
data: {"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"Hel"}}

event: content_block_delta
data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"lo"}}

event: content_block_stop
data: {"type":"content_block_stop","index":1}

event: content_block_start
data: {"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"toolu_1","name":"bash","input":{}}}

event: content_block_delta
data: {"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"{\"comma"}}

event: content_block_delta
data: {"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"nd\":\"ls\"}"}}

event: content_block_stop
data: {"type":"content_block_stop","index":2}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":42}}

event: message_stop
data: {"type":"message_stop"}

`

func TestStream(t *testing.T) {
	client := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		assertHeaders(t, r)
		body, _ := io.ReadAll(r.Body)
		var req Request
		json.Unmarshal(body, &req)
		if !req.Stream {
			t.Errorf("expected stream=true for Stream")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte(sseStream))
	})

	var text strings.Builder
	var thinking strings.Builder
	var toolID, toolName string
	var toolInput json.RawMessage
	handler := StreamHandler{
		OnText:     func(s string) { text.WriteString(s) },
		OnThinking: func(s string) { thinking.WriteString(s) },
		OnToolUse:  func(id, name string, input json.RawMessage) { toolID, toolName, toolInput = id, name, input },
	}
	res, err := client.streamHandler(context.Background(), Request{
		Model:     "claude-sonnet-4-5",
		MaxTokens: 1024,
		Messages:  []Message{{Role: "user", Content: []ContentBlock{{Type: "text", Text: "run ls"}}}},
	}, handler)
	if err != nil {
		t.Fatal(err)
	}
	if text.String() != "Hello" {
		t.Fatalf("assembled text = %q", text.String())
	}
	if thinking.String() != "Let me think." {
		t.Fatalf("assembled thinking = %q", thinking.String())
	}
	if toolID != "toolu_1" || toolName != "bash" {
		t.Fatalf("tool_use start = %s/%s", toolID, toolName)
	}
	if res == nil {
		t.Fatal("expected final message")
	}
	if res.StopReason != "tool_use" {
		t.Fatalf("stop_reason = %q", res.StopReason)
	}
	if res.Usage.OutputTokens != 42 || res.Usage.InputTokens != 25 {
		t.Fatalf("usage = %+v", res.Usage)
	}
	if len(res.Content) != 3 {
		t.Fatalf("expected 3 assembled blocks, got %d", len(res.Content))
	}
	if string(res.Content[2].Input) != `{"command":"ls"}` {
		t.Fatalf("assembled tool input = %s", res.Content[2].Input)
	}
	_ = toolInput
}

func TestStreamClientInterface(t *testing.T) {
	client := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte(sseStream))
	})
	var events []llm.StreamEvent
	request := llm.Request{
		ProviderID: "anthropic",
		ModelID:    "claude-sonnet-4-5",
		System:     []string{"You are gocode."},
		Messages:   []llm.Message{llm.UserText("msg_1", "run ls")},
		Tools: []llm.ToolDefinition{{
			Name:        "bash",
			Description: "run a shell command",
			InputSchema: map[string]any{"type": "object"},
		}},
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
		llm.EventReasoningDelta,
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
	finish := events[len(events)-1]
	if finish.Finish != "tool_use" || finish.Usage.Output != 42 || finish.Usage.Input != 25 {
		t.Fatalf("unexpected finish event: %+v", finish)
	}
	call := events[3].ToolCall
	if call == nil || call.Name != "bash" {
		t.Fatalf("unexpected tool call: %+v", call)
	}
}

func TestAPIError(t *testing.T) {
	client := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key"}}`))
	})
	_, err := client.Complete(context.Background(), Request{Model: "x", MaxTokens: 1})
	if err == nil {
		t.Fatal("expected error")
	}
	apiErr, ok := err.(*APIError)
	if !ok {
		t.Fatalf("expected *APIError, got %T", err)
	}
	if apiErr.StatusCode != 401 || apiErr.Type != "authentication_error" {
		t.Fatalf("unexpected error: %v", apiErr)
	}
}

func TestStreamErrorEvent(t *testing.T) {
	client := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte("data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_3\"}}\n\n"))
		w.Write([]byte("data: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"overloaded\"}}\n\n"))
	})
	var sawError error
	handler := StreamHandler{OnError: func(err error) { sawError = err }}
	_, err := client.streamHandler(context.Background(), Request{Model: "x", MaxTokens: 1}, handler)
	if err == nil || sawError == nil {
		t.Fatalf("expected stream error, got %v / %v", err, sawError)
	}
}

// Regression: catalog API URLs already include the version segment
// (https://api.minimax.io/anthropic/v1); the client must not double it.
func TestMessagesURLConventions(t *testing.T) {
	cases := []struct {
		base string
		want string
	}{
		{"", DefaultBaseURL + "/v1/messages"},
		{"https://api.minimax.io/anthropic/v1", "https://api.minimax.io/anthropic/v1/messages"},
		{"https://api.minimax.io/anthropic/v1/", "https://api.minimax.io/anthropic/v1/messages"},
		{"http://127.0.0.1:1", "http://127.0.0.1:1/v1/messages"},
	}
	for _, c := range cases {
		client := New("k")
		client.BaseURL = c.base
		if got := client.messagesURL("m"); got != c.want {
			t.Errorf("messagesURL(base=%q) = %q, want %q", c.base, got, c.want)
		}
	}
}
