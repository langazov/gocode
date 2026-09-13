package openai

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

// cacheBody captures the JSON body a request produced, decoded generically.
func cacheBody(t *testing.T, options llm.Options) map[string]any {
	t.Helper()
	var body map[string]any
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Write([]byte("data: [DONE]\n\n"))
	})
	client.Options = options
	err := client.Stream(context.Background(), llm.Request{
		ProviderID: "gocoder",
		ModelID:    "anthropic/claude-sonnet-4.5",
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
	return body
}

// With the endpoint opted in, the auto policy's three breakpoints — the last
// tool, the system prompt, and the newest user message — must reach the wire
// as Anthropic-style cache_control, because OpenRouter translates that shape
// into whatever the routed provider understands.
func TestCacheControlBlocksEmittedWhenOptedIn(t *testing.T) {
	body := cacheBody(t, llm.Options{CacheControlBlocks: true})

	tools := body["tools"].([]any)
	last := tools[len(tools)-1].(map[string]any)
	if last["cache_control"] == nil {
		t.Errorf("last tool carries no cache_control: %v", last)
	}

	messages := body["messages"].([]any)
	system := messages[0].(map[string]any)
	// The system block is the array form only because the marker rides it.
	blocks, ok := system["content"].([]any)
	if !ok || len(blocks) == 0 {
		t.Fatalf("system content = %v, want typed blocks", system["content"])
	}
	if blocks[len(blocks)-1].(map[string]any)["cache_control"] == nil {
		t.Errorf("system block carries no cache_control: %v", blocks)
	}

	user := messages[1].(map[string]any)
	userBlocks, ok := user["content"].([]any)
	if !ok || len(userBlocks) == 0 {
		t.Fatalf("user content = %v, want typed blocks", user["content"])
	}
	if userBlocks[len(userBlocks)-1].(map[string]any)["cache_control"] == nil {
		t.Errorf("user block carries no cache_control: %v", userBlocks)
	}
}

// Without the opt-in nothing changes on the wire: the system and user
// messages stay plain strings and no tool carries a marker, because a vanilla
// openai-compatible endpoint rejects unknown content-block fields.
func TestCacheControlAbsentWithoutOptIn(t *testing.T) {
	body := cacheBody(t, llm.Options{})

	tools := body["tools"].([]any)
	for i, tool := range tools {
		if tool.(map[string]any)["cache_control"] != nil {
			t.Errorf("tool %d carries cache_control without opt-in", i)
		}
	}
	messages := body["messages"].([]any)
	for i, message := range messages {
		content := message.(map[string]any)["content"]
		if _, isBlocks := content.([]any); isBlocks {
			t.Errorf("message %d content is a block array without opt-in: %v", i, content)
		}
	}
}

// An extended-TTL hint renders as the hour-long window; anything below the
// bucket takes the provider's 5-minute default.
func TestCacheControlTTLBuckets(t *testing.T) {
	hour := cacheControlDirective(&llm.CacheHint{TTLSeconds: 3600})
	if hour == nil || hour.TTL != "1h" {
		t.Errorf("hour hint = %+v", hour)
	}
	five := cacheControlDirective(&llm.CacheHint{TTLSeconds: 300})
	if five == nil || five.TTL != "" {
		t.Errorf("sub-hour hint = %+v", five)
	}
	if cacheControlDirective(nil) != nil {
		t.Error("nil hint must render nothing")
	}
}

// The system prompt lowers to one message per entry, but the breakpoint
// covers the whole prompt — so only the final entry carries it. A marker on
// every entry would burn OpenRouter's four-breakpoint budget on a prefix one
// trailing marker already names.
func TestSystemBreakpointOnlyOnFinalEntry(t *testing.T) {
	request := llm.Request{
		ProviderID: "gocoder",
		ModelID:    "x/y:free",
		System:     []string{"first", "", "second"},
		Messages:   []llm.Message{llm.UserText("m1", "hi")},
	}
	request = llm.ApplyCachePolicy(request)
	body, err := convertRequest(request, true)
	if err != nil {
		t.Fatal(err)
	}
	marked := 0
	systemSeen := 0
	for _, message := range body.Messages {
		if message.Role != "system" {
			continue
		}
		systemSeen++
		isFinal := systemSeen == 2 // two non-empty entries were passed
		blocks, isBlocks := message.Content.([]contentPart)
		if isFinal {
			// The breakpoint covers the whole system prompt, so it rides the
			// final entry — as the block array, the only shape that can.
			if !isBlocks || len(blocks) == 0 {
				t.Fatalf("final system content = %#v, want typed blocks", message.Content)
			}
			if blocks[len(blocks)-1].CacheControl == nil {
				t.Errorf("final system block carries no cache_control: %v", blocks)
			}
			marked++
		} else if isBlocks {
			t.Errorf("earlier system entry is a block array: %#v", message.Content)
		}
	}
	if systemSeen != 2 {
		t.Fatalf("system entries = %d, want 2 (empty ones skipped)", systemSeen)
	}
	if marked != 1 {
		t.Errorf("marked system entries = %d, want 1 (the final one)", marked)
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
	converted, err := convertMessage(llm.ToolResultMessage("", "call_1", "bash", "output text", false), false)
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
	converted, err := convertMessage(message, false)
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

// The session this regression came from ran on an openai-compatible endpoint
// that does cache — and every step recorded cache read 0 and reasoning 0,
// because the usage decoder stopped at prompt_tokens/completion_tokens.
func TestStreamUsageDetails(t *testing.T) {
	const stream = `data: {"choices":[{"delta":{"content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1000,"completion_tokens":300,"prompt_tokens_details":{"cached_tokens":900},"completion_tokens_details":{"reasoning_tokens":200}}}

data: [DONE]

`
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte(stream))
	})
	var finish llm.StreamEvent
	err := client.Stream(context.Background(), llm.Request{
		ProviderID: "zhipuai-coding-plan",
		ModelID:    "glm-5.3-flash",
		Messages:   []llm.Message{llm.UserText("m1", "hi")},
	}, func(event llm.StreamEvent) {
		if event.Type == llm.EventFinish {
			finish = event
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	// The buckets are disjoint: 100 non-cached input + 900 cached is the
	// 1000 the provider reported, and 100 output + 200 reasoning is its 300.
	want := llm.Usage{Input: 100, Output: 100, Reasoning: 200, CacheRead: 900}
	if finish.Usage != want {
		t.Fatalf("usage: want %+v, got %+v", want, finish.Usage)
	}
}

// DeepSeek reports the same split as a pair of top-level fields.
func TestStreamUsageCacheHitTokens(t *testing.T) {
	const stream = `data: {"choices":[{"delta":{"content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1000,"completion_tokens":50,"prompt_cache_hit_tokens":800,"prompt_cache_miss_tokens":200}}

data: [DONE]

`
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte(stream))
	})
	var finish llm.StreamEvent
	err := client.Stream(context.Background(), llm.Request{
		ProviderID: "deepseek",
		ModelID:    "deepseek-chat",
		Messages:   []llm.Message{llm.UserText("m1", "hi")},
	}, func(event llm.StreamEvent) {
		if event.Type == llm.EventFinish {
			finish = event
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	want := llm.Usage{Input: 200, Output: 50, CacheRead: 800}
	if finish.Usage != want {
		t.Fatalf("usage: want %+v, got %+v", want, finish.Usage)
	}
}

// A provider that reports no details at all must keep the totals whole.
func TestStreamUsageWithoutDetails(t *testing.T) {
	const stream = `data: {"choices":[{"delta":{"content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1000,"completion_tokens":50}}

data: [DONE]

`
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte(stream))
	})
	var finish llm.StreamEvent
	err := client.Stream(context.Background(), llm.Request{
		ProviderID: "openai",
		ModelID:    "gpt-5",
		Messages:   []llm.Message{llm.UserText("m1", "hi")},
	}, func(event llm.StreamEvent) {
		if event.Type == llm.EventFinish {
			finish = event
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	want := llm.Usage{Input: 1000, Output: 50}
	if finish.Usage != want {
		t.Fatalf("usage: want %+v, got %+v", want, finish.Usage)
	}
}

// A 429 with a stated wait becomes a RateLimitError carrying that wait, so
// the session runner can hold the turn for exactly as long as the provider
// asked rather than settling the step as failed.
func Test429CarriesRetryHint(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "12")
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte(`{"error":{"message":"Rate limit reached","type":"rate_limit_error"}}`))
	})
	var seen *llm.RateLimitError
	err := client.Stream(context.Background(), llm.Request{ModelID: "gpt-5"}, func(event llm.StreamEvent) {
		if event.Type == llm.EventProviderError {
			_ = event.Error
		}
	})
	if !errors.As(err, &seen) {
		t.Fatalf("expected *llm.RateLimitError, got %T: %v", err, err)
	}
	if seen.RetryAfter != 12*time.Second || !seen.RetryAfterKnown {
		t.Fatalf("RetryAfter = (%v, known=%v), want 12s", seen.RetryAfter, seen.RetryAfterKnown)
	}
	if seen.Provider != "openai" || seen.Message != "Rate limit reached" {
		t.Fatalf("error text lost the provider's own message: %+v", seen)
	}
}

// The prose fallback: no header, but the message says when to retry.
func Test429WithoutHeaderParsesTheProse(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte(`{"error":{"message":"Rate limit reached. Please try again in 1.728s."}}`))
	})
	err := client.Stream(context.Background(), llm.Request{ModelID: "gpt-5"}, func(event llm.StreamEvent) {})
	var seen *llm.RateLimitError
	if !errors.As(err, &seen) {
		t.Fatalf("expected *llm.RateLimitError, got %T: %v", err, err)
	}
	if seen.RetryAfter != 1728*time.Millisecond || !seen.RetryAfterKnown {
		t.Fatalf("RetryAfter = (%v, known=%v), want 1.728s", seen.RetryAfter, seen.RetryAfterKnown)
	}
}

// A 429 that says nothing about timing is still classified as a rate limit,
// but carries no known wait — which is what the session gates its hold on,
// so the turn settles as it always did.
func Test429WithoutHintCarriesNoKnownWait(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte(`{"error":{"message":"Too many requests"}}`))
	})
	err := client.Stream(context.Background(), llm.Request{ModelID: "gpt-5"}, func(event llm.StreamEvent) {})
	var limited *llm.RateLimitError
	if !errors.As(err, &limited) {
		t.Fatalf("expected *llm.RateLimitError, got %T: %v", err, err)
	}
	if limited.RetryAfterKnown {
		t.Fatalf("a hint-less 429 must not carry a known retry time: %+v", limited)
	}
}
