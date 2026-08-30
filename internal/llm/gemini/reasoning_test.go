package gemini

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/anomalyco/opencode-go/internal/llm"
)

// TestThinkingConfigSentAndThoughtPartsRouteToReasoning is the regression
// for "I can't see thinking in the go port": a selected reasoning variant's
// "thinkingConfig" patch must reach generationConfig, and response parts
// flagged "thought":true must emit reasoning-delta events, not text-delta.
func TestThinkingConfigSentAndThoughtPartsRouteToReasoning(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req generateRequest
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatalf("invalid request: %v", err)
		}
		thinkingConfig, _ := req.GenerationConfig["thinkingConfig"].(map[string]any)
		if thinkingConfig["thinkingBudget"] != float64(8000) {
			t.Errorf("thinkingConfig = %+v, want thinkingBudget 8000", req.GenerationConfig["thinkingConfig"])
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte(`data: {"candidates":[{"content":{"parts":[{"text":"pondering...","thought":true}]}}]}` + "\n\n" +
			`data: {"candidates":[{"content":{"parts":[{"text":"the answer is 4"}]},"finishReason":"STOP"}]}` + "\n\n" +
			"data: [DONE]\n\n"))
	})

	var reasoning, text string
	err := client.Stream(context.Background(), llm.Request{
		ModelID:  "gemini-2.5-pro",
		Messages: []llm.Message{llm.UserText("m1", "what is 2+2")},
		Reasoning: map[string]any{
			"thinkingConfig": map[string]any{"includeThoughts": true, "thinkingBudget": 8000},
		},
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
	if reasoning != "pondering..." {
		t.Fatalf("reasoning = %q, want %q", reasoning, "pondering...")
	}
	if text != "the answer is 4" {
		t.Fatalf("text = %q, want %q", text, "the answer is 4")
	}
}
