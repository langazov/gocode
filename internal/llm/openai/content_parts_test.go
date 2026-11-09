package openai

import (
	"strings"
	"testing"

	"github.com/langazov/gocode-go/internal/llm"
)

// collect runs readStream over an SSE body and returns the concatenated text
// and reasoning it emitted.
func collect(t *testing.T, body string) (text, reasoning string) {
	t.Helper()
	err := readStream(strings.NewReader(body), func(event llm.StreamEvent) {
		switch event.Type {
		case llm.EventTextDelta:
			text += event.Text
		case llm.EventReasoningDelta:
			reasoning += event.Text
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	return text, reasoning
}

// TestStreamContentPartsArray is the regression for session
// ses_e43533666ffeS9SjAwjRd1zVEC, where every reply from a Mistral reasoning
// model lost its opening words ("No upstream is configured" was stored as
// " upstream is configured") and no reasoning was ever recorded. Mistral
// streams delta.content as an array of typed chunks whenever thinking is
// involved; decoding that into a string failed and the whole SSE chunk —
// thinking plus the first words of the answer — was silently skipped.
func TestStreamContentPartsArray(t *testing.T) {
	body := `data: {"choices":[{"delta":{"role":"assistant","content":[{"type":"thinking","thinking":[{"type":"text","text":"The user wants a summary."}]}]}}]}` + "\n\n" +
		`data: {"choices":[{"delta":{"content":[{"type":"thinking","thinking":[{"type":"text","text":" Check upstream."}],"closed":true},{"type":"text","text":"No"}]}}]}` + "\n\n" +
		`data: {"choices":[{"delta":{"content":" upstream is configured."},"finish_reason":"stop"}]}` + "\n\n" +
		"data: [DONE]\n\n"

	text, reasoning := collect(t, body)
	if text != "No upstream is configured." {
		t.Errorf("text = %q, want %q", text, "No upstream is configured.")
	}
	if reasoning != "The user wants a summary. Check upstream." {
		t.Errorf("reasoning = %q, want %q", reasoning, "The user wants a summary. Check upstream.")
	}
}

// TestStreamContentPartsKeepsToolCallsAndUsage checks that an array content in
// a chunk no longer takes the chunk's tool calls, finish reason and usage down
// with it.
func TestStreamContentPartsKeepsToolCallsAndUsage(t *testing.T) {
	body := `data: {"choices":[{"delta":{"content":[{"type":"text","text":"Running."}],"tool_calls":[{"index":0,"id":"call_1","function":{"name":"bash","arguments":"{\"command\":\"ls\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":10,"completion_tokens":3}}` + "\n\n" +
		"data: [DONE]\n\n"

	var events []llm.StreamEvent
	if err := readStream(strings.NewReader(body), func(event llm.StreamEvent) {
		events = append(events, event)
	}); err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 {
		t.Fatalf("events = %+v, want text, tool call, finish", events)
	}
	if events[0].Type != llm.EventTextDelta || events[0].Text != "Running." {
		t.Errorf("event 0 = %+v", events[0])
	}
	if events[1].Type != llm.EventToolCall || events[1].ToolCall.Name != "bash" {
		t.Errorf("event 1 = %+v", events[1])
	}
	if finish := events[2]; finish.Finish != "tool_calls" || finish.Usage.Input != 10 || finish.Usage.Output != 3 {
		t.Errorf("finish = %+v", finish)
	}
}

// TestStreamContentNullAndString covers the shapes every other backend sends.
func TestStreamContentNullAndString(t *testing.T) {
	body := `data: {"choices":[{"delta":{"role":"assistant","content":null}}]}` + "\n\n" +
		`data: {"choices":[{"delta":{"content":"plain"}}]}` + "\n\n" +
		`data: {"choices":[{"delta":{"content":""}}]}` + "\n\n" +
		"data: [DONE]\n\n"
	text, reasoning := collect(t, body)
	if text != "plain" || reasoning != "" {
		t.Errorf("text = %q, reasoning = %q", text, reasoning)
	}
}
