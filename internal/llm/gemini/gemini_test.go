package gemini

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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
		if got := r.Header.Get("x-goog-api-key"); got != "test-key" {
			t.Errorf("x-goog-api-key = %q", got)
		}
		if !strings.Contains(r.URL.Path, ":streamGenerateContent") {
			t.Errorf("path = %q", r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		var req generateRequest
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatalf("invalid request: %v", err)
		}
		if req.SystemInstruction == nil || len(req.SystemInstruction.Parts) == 0 {
			t.Error("expected system instruction")
		}
		if len(req.Contents) != 1 || req.Contents[0].Role != "user" {
			t.Errorf("unexpected contents: %+v", req.Contents)
		}
		w.Write([]byte("data: [DONE]\n\n"))
	})
	err := client.Stream(context.Background(), llm.Request{
		ProviderID: "google",
		ModelID:    "gemini-2.5-pro",
		System:     []string{"You are gocode."},
		Messages:   []llm.Message{llm.UserText("m1", "hello")},
	}, func(event llm.StreamEvent) {})
	if err != nil {
		t.Fatal(err)
	}
}

const geminiStream = `data: {"candidates":[{"content":{"parts":[{"text":"Hello "}]}}]}

data: {"candidates":[{"content":{"parts":[{"text":"from Gemini"}]}}],"usageMetadata":{"promptTokenCount":5,"candidatesTokenCount":7}}

data: {"candidates":[{"content":{"parts":[{"functionCall":{"name":"bash","args":{"command":"ls"}}}]},"finishReason":"STOP"}]}

data: [DONE]

`

func TestStream(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte(geminiStream))
	})
	var events []llm.StreamEvent
	request := llm.Request{
		ProviderID: "google",
		ModelID:    "gemini-2.5-pro",
		Messages:   []llm.Message{llm.UserText("m1", "hi")},
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
	if toolCall == nil || toolCall.Name != "bash" || toolCall.Input["command"] != "ls" {
		t.Fatalf("unexpected tool call: %+v", toolCall)
	}
	finish := events[len(events)-1]
	if finish.Finish != "stop" || finish.Usage.Input != 5 || finish.Usage.Output != 7 {
		t.Fatalf("unexpected finish: %+v", finish)
	}
}

func TestToolMessageConversion(t *testing.T) {
	converted := convertMessage(llm.ToolResultMessage("", "call_1", "bash", "output", false))
	if len(converted) != 1 {
		t.Fatalf("expected 1 content, got %d", len(converted))
	}
	parts := converted[0].Parts
	if len(parts) != 1 || parts[0].FunctionResponse == nil {
		t.Fatalf("expected function response part, got %+v", parts)
	}
	if parts[0].FunctionResponse.Name != "bash" || parts[0].FunctionResponse.Response["result"] != "output" {
		t.Fatalf("unexpected function response: %+v", parts[0].FunctionResponse)
	}
}

func TestAPIError(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":{"message":"API key not valid"}}`))
	})
	var gotProviderError bool
	err := client.Stream(context.Background(), llm.Request{ModelID: "gemini-2.5-pro"}, func(event llm.StreamEvent) {
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
	if !ok || apiErr.StatusCode != 400 {
		t.Fatalf("unexpected error: %v", err)
	}
}

// A 429 with a stated wait becomes a RateLimitError carrying that wait.
// Gemini's most reliable hint is the typed retryDelay inside its error
// details — preferred over the header and the prose alike.
func Test429CarriesRetryHint(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "5")
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte(`{"error":{"code":429,"message":"Resource has been exhausted","status":"RESOURCE_EXHAUSTED",
			"details":[{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"32s"}]}}`))
	})
	err := client.Stream(context.Background(), llm.Request{ModelID: "gemini-2.5-pro"}, func(event llm.StreamEvent) {})
	var limited *llm.RateLimitError
	if !errors.As(err, &limited) {
		t.Fatalf("expected *llm.RateLimitError, got %T: %v", err, err)
	}
	if limited.RetryAfter != 32*time.Second || !limited.RetryAfterKnown {
		t.Fatalf("RetryAfter = (%v, known=%v), want the typed 32s, not the header's 5s", limited.RetryAfter, limited.RetryAfterKnown)
	}
	if limited.Provider != "gemini" || !strings.Contains(limited.Message, "exhausted") {
		t.Fatalf("error text lost the provider's own message: %+v", limited)
	}
}

// With no typed delay, the standard header serves.
func Test429FallsBackToHeader(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "9")
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte(`{"error":{"code":429,"message":"Resource has been exhausted"}}`))
	})
	err := client.Stream(context.Background(), llm.Request{ModelID: "gemini-2.5-pro"}, func(event llm.StreamEvent) {})
	var limited *llm.RateLimitError
	if !errors.As(err, &limited) {
		t.Fatalf("expected *llm.RateLimitError, got %T: %v", err, err)
	}
	if limited.RetryAfter != 9*time.Second || !limited.RetryAfterKnown {
		t.Fatalf("RetryAfter = (%v, known=%v), want 9s from the header", limited.RetryAfter, limited.RetryAfterKnown)
	}
}
