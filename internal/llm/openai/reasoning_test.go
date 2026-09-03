package openai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/langazov/gocode-go/internal/llm"
)

// TestReasoningEffortSentAndAlternateFieldNamesRead is the regression for
// "I can't see thinking in the go port": a selected reasoning variant's
// "reasoning_effort" patch must reach the request, and a reply's reasoning
// text must be picked up regardless of which field name the backend uses
// (reasoning | reasoning_content | reasoning_text — openai-compatible
// backends aren't consistent here).
func TestReasoningEffortSentAndAlternateFieldNamesRead(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req chatRequest
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatalf("invalid request: %v", err)
		}
		if req.ReasoningEffort != "high" {
			t.Errorf("reasoning_effort = %q, want %q", req.ReasoningEffort, "high")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte(`data: {"choices":[{"delta":{"reasoning_content":"thinking hard"}}]}` + "\n\n" +
			`data: {"choices":[{"delta":{"content":"42"},"finish_reason":"stop"}]}` + "\n\n" +
			"data: [DONE]\n\n"))
	})

	var reasoning, text string
	err := client.Stream(context.Background(), llm.Request{
		ModelID:   "glm-5.3",
		Messages:  []llm.Message{llm.UserText("m1", "what is 6*7")},
		Reasoning: map[string]any{"reasoning_effort": "high"},
	}, func(event llm.StreamEvent) {
		switch event.Type {
		case llm.EventReasoningDelta:
			reasoning += event.Text
		case llm.EventTextDelta:
			text += event.Text
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if reasoning != "thinking hard" {
		t.Fatalf("reasoning = %q, want %q", reasoning, "thinking hard")
	}
	if text != "42" {
		t.Fatalf("text = %q, want %q", text, "42")
	}
}
