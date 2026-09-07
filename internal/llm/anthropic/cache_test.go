package anthropic

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/langazov/gocode-go/internal/llm"
)

// captureRequest runs one turn against a stub server and returns the request
// body it received, decoded.
func captureRequest(t *testing.T, request llm.Request) (Request, http.Header) {
	t.Helper()
	var body Request
	var header http.Header
	client := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		header = r.Header.Clone()
		raw, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte("data: {\"type\":\"message_stop\"}\n\n"))
	})
	if err := client.Stream(context.Background(), request, func(llm.StreamEvent) {}); err != nil {
		t.Fatal(err)
	}
	return body, header
}

func toolRequest() llm.Request {
	return llm.Request{
		ProviderID: "anthropic",
		ModelID:    "claude-sonnet-4-5",
		System:     []string{"You are gocode.", "Be brief."},
		Messages: []llm.Message{
			llm.UserText("m1", "run ls"),
			llm.AssistantText("m2", "on it"),
		},
		Tools: []llm.ToolDefinition{
			{Name: "bash", Description: "run a command", InputSchema: map[string]any{"type": "object"}},
			{Name: "read", Description: "read a file", InputSchema: map[string]any{"type": "object"}},
		},
	}
}

// The default policy places three breakpoints, and where they land is the
// whole point: the end of the tools, the end of the system prompt, and the
// newest user message.
func TestDefaultCachePolicyPlacement(t *testing.T) {
	body, _ := captureRequest(t, toolRequest())

	if len(body.Tools) != 2 {
		t.Fatalf("tools = %d, want 2", len(body.Tools))
	}
	if body.Tools[0].CacheControl != nil {
		t.Error("first tool should carry no breakpoint")
	}
	if body.Tools[1].CacheControl == nil || body.Tools[1].CacheControl.Type != "ephemeral" {
		t.Errorf("last tool cache_control = %+v, want ephemeral", body.Tools[1].CacheControl)
	}

	if len(body.System) != 2 {
		t.Fatalf("system blocks = %d, want 2", len(body.System))
	}
	if body.System[0].Type != "text" || body.System[0].Text != "You are gocode." {
		t.Errorf("system[0] = %+v", body.System[0])
	}
	if body.System[0].CacheControl != nil {
		t.Error("only the final system block should carry a breakpoint")
	}
	if body.System[1].CacheControl == nil {
		t.Error("system prompt should end in a breakpoint")
	}

	// m1 is the newest (only) user message; the assistant reply after it must
	// stay unmarked, or the prefix would move on every step.
	if len(body.Messages) != 2 {
		t.Fatalf("messages = %d, want 2", len(body.Messages))
	}
	if body.Messages[0].Content[0].CacheControl == nil {
		t.Error("newest user message should carry a breakpoint")
	}
	if body.Messages[1].Content[0].CacheControl != nil {
		t.Error("assistant message should carry no breakpoint")
	}
	if body.Messages[0].Content[0].CacheControl.TTL != "" {
		t.Errorf("ttl = %q, want the default 5m window", body.Messages[0].Content[0].CacheControl.TTL)
	}
}

// The reason for marking the newest *user* message rather than the last
// message: a turn fans out into many assistant/tool round trips, and the
// breakpoint has to hold still across them for any of them to hit the cache.
func TestBreakpointHoldsStillAcrossToolLoop(t *testing.T) {
	request := toolRequest()
	request.Messages = append(request.Messages,
		llm.Message{ID: "m3", Role: llm.RoleAssistant, Content: []llm.ContentPart{
			{Type: llm.PartToolCall, ToolCallID: "call_1", ToolName: "bash", Input: map[string]any{"command": "ls"}},
		}},
		llm.ToolResultMessage("m4", "call_1", "bash", "a.go b.go", false),
	)
	body, _ := captureRequest(t, request)

	if body.Messages[0].Content[0].CacheControl == nil {
		t.Error("breakpoint should still sit on the user message that opened the turn")
	}
	for i, message := range body.Messages[1:] {
		for _, block := range message.Content {
			if block.CacheControl != nil {
				t.Errorf("message %d gained a breakpoint; the cached prefix would move every step", i+1)
			}
		}
	}
}

// Anthropic answers a fifth breakpoint with a 400, so the lowering drops the
// overflow. What survives is what comes first — the front of the request,
// where the reusable prefix is.
func TestBreakpointCap(t *testing.T) {
	hint := &llm.CacheHint{}
	request := toolRequest()
	request.SystemCache = hint
	for i := range request.Tools {
		request.Tools[i].Cache = hint
	}
	request.Messages = []llm.Message{
		{ID: "m1", Role: llm.RoleUser, Content: []llm.ContentPart{
			{Type: llm.PartText, Text: "one", Cache: hint},
			{Type: llm.PartText, Text: "two", Cache: hint},
		}},
	}
	body, _ := captureRequest(t, request)

	var emitted int
	for _, tool := range body.Tools {
		if tool.CacheControl != nil {
			emitted++
		}
	}
	for _, block := range body.System {
		if block.CacheControl != nil {
			emitted++
		}
	}
	for _, message := range body.Messages {
		for _, block := range message.Content {
			if block.CacheControl != nil {
				emitted++
			}
		}
	}
	if emitted != llm.AnthropicBreakpointCap {
		t.Fatalf("emitted %d breakpoints, want the cap of %d", emitted, llm.AnthropicBreakpointCap)
	}
	// Both tools and the system block came first and keep theirs; the second
	// message block is the one dropped.
	if body.Tools[0].CacheControl == nil || body.Tools[1].CacheControl == nil {
		t.Error("tool breakpoints should survive: they are the front of the prefix")
	}
	if body.Messages[0].Content[1].CacheControl != nil {
		t.Error("the overflowing breakpoint should have been dropped")
	}
}

// An explicit empty policy turns automatic placement off; a request that names
// no policy at all gets the default one.
func TestCachePolicyNone(t *testing.T) {
	request := toolRequest()
	request.Cache = &llm.CachePolicy{}
	body, header := captureRequest(t, request)

	for _, tool := range body.Tools {
		if tool.CacheControl != nil {
			t.Error("no policy means no tool breakpoint")
		}
	}
	for _, block := range body.System {
		if block.CacheControl != nil {
			t.Error("no policy means no system breakpoint")
		}
	}
	for _, message := range body.Messages {
		for _, block := range message.Content {
			if block.CacheControl != nil {
				t.Error("no policy means no message breakpoint")
			}
		}
	}
	if got := header.Get("anthropic-beta"); got != betaHeader {
		t.Errorf("anthropic-beta = %q, want the standing set unchanged", got)
	}
}

// The hour-long window is a separate opt-in; sending ttl without the beta
// header is a 400.
func TestExtendedTTLOptsIntoBeta(t *testing.T) {
	request := toolRequest()
	request.Cache = &llm.CachePolicy{System: true, TTLSeconds: 3600}
	body, header := captureRequest(t, request)

	last := body.System[len(body.System)-1]
	if last.CacheControl == nil || last.CacheControl.TTL != "1h" {
		t.Fatalf("system cache_control = %+v, want a 1h ttl", last.CacheControl)
	}
	if got := header.Get("anthropic-beta"); got != betaHeader+","+extendedTTLBeta {
		t.Errorf("anthropic-beta = %q, want the extended-cache opt-in appended", got)
	}
}

// Under an hour is the default window, which needs no opt-in.
func TestShortTTLDoesNotOptIntoBeta(t *testing.T) {
	request := toolRequest()
	request.Cache = &llm.CachePolicy{System: true, TTLSeconds: 300}
	body, header := captureRequest(t, request)

	last := body.System[len(body.System)-1]
	if last.CacheControl == nil || last.CacheControl.TTL != "" {
		t.Fatalf("system cache_control = %+v, want the default window", last.CacheControl)
	}
	if got := header.Get("anthropic-beta"); got != betaHeader {
		t.Errorf("anthropic-beta = %q, want the standing set unchanged", got)
	}
}

// System-role messages fold into the system prompt the same way the joined
// string used to, one block each, in order.
func TestSystemRoleMessagesBecomeSystemBlocks(t *testing.T) {
	request := toolRequest()
	request.Messages = append([]llm.Message{llm.SystemMessage("Extra rule.")}, request.Messages...)
	body, _ := captureRequest(t, request)

	if len(body.System) != 3 {
		t.Fatalf("system blocks = %d, want 3", len(body.System))
	}
	if body.System[2].Text != "Extra rule." {
		t.Errorf("system[2] = %q, want the system-role message last", body.System[2].Text)
	}
	if body.System[2].CacheControl == nil {
		t.Error("the breakpoint belongs on the final system block")
	}
	for _, message := range body.Messages {
		if message.Role == "system" {
			t.Error("a system-role message should not survive as a message")
		}
	}
}
