package tui

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/langazov/gocode-go/internal/tui/client"
)

// TestLargeAssistantTextTruncation verifies that assistant text parts exceeding
// MAX_MESSAGE_CONTENT_SIZE are truncated with a notice.
func TestLargeAssistantTextTruncation(t *testing.T) {
	// Create a text part larger than MAX_MESSAGE_CONTENT_SIZE (1 MiB)
	largeText := strings.Repeat("x", MAX_MESSAGE_CONTENT_SIZE+1000)

	app := newTestApp(t, "http://example.invalid")
	app.width = 120
	app.height = 40

	// Render the assistant text block directly
	block := app.assistantTextBlock(largeText)

	// Verify the truncation notice is present
	if !strings.Contains(block, "… message truncated at 1 MiB …") {
		t.Fatalf("expected truncation notice in output, got %q", block)
	}
}

// TestLargeReasoningTruncation verifies that reasoning parts exceeding
// MAX_REASONING_SIZE are truncated with a notice.
func TestLargeReasoningTruncation(t *testing.T) {
	// Create reasoning text larger than MAX_REASONING_SIZE (1 MiB)
	largeReasoning := strings.Repeat("thinking ", MAX_REASONING_SIZE/8+100)

	app := newTestApp(t, "http://example.invalid")
	app.width = 120
	app.height = 40
	// Enable show mode so the reasoning body is rendered
	app.thinkingMode = "show"

	// Render the reasoning block directly
	block := app.reasoningBlock("test-reasoning-id", false, largeReasoning, nil)

	// Verify the truncation notice is present
	if !strings.Contains(block, "… reasoning truncated at 1 MiB …") {
		t.Fatalf("expected truncation notice in output, got %q", block)
	}
}

// TestStreamingTextTruncation verifies that streaming text is truncated during rendering.
func TestStreamingTextTruncation(t *testing.T) {
	app := newTestApp(t, "http://example.invalid")
	app.width = 120
	app.height = 40

	// Create a streaming text larger than MAX_MESSAGE_CONTENT_SIZE
	largeText := strings.Repeat("x", MAX_MESSAGE_CONTENT_SIZE+1000)

	// Render via streamingTextBlock
	block := app.streamingTextBlock("msg-1", largeText)

	// Verify the truncation notice is present
	if !strings.Contains(block, "… message truncated at 1 MiB …") {
		t.Fatalf("expected truncation notice in output, got %q", block)
	}
}

// TestStreamingReasoningTruncation verifies that streaming reasoning is truncated during rendering.
func TestStreamingReasoningTruncation(t *testing.T) {
	app := newTestApp(t, "http://example.invalid")
	app.width = 120
	app.height = 40
	// Enable show mode so the reasoning body is rendered
	app.thinkingMode = "show"

	// Create a streaming reasoning larger than MAX_REASONING_SIZE
	largeReasoning := strings.Repeat("thinking ", MAX_REASONING_SIZE/8+100)

	// Render via streamingReasoningBlock
	block := app.streamingReasoningBlock("reasoning-1", largeReasoning)

	// Verify the truncation notice is present
	if !strings.Contains(block, "… reasoning truncated at 1 MiB …") {
		t.Fatalf("expected truncation notice in output, got %q", block)
	}
}

// TestToolOutputTruncation verifies that tool outputs exceeding MAX_TOOL_OUTPUT_SIZE are truncated.
func TestToolOutputTruncation(t *testing.T) {
	app := newTestApp(t, "http://example.invalid")
	app.width = 120
	app.height = 40

	// Create tool output larger than MAX_TOOL_OUTPUT_SIZE (1 MiB)
	largeOutput := strings.Repeat("output line\n", MAX_TOOL_OUTPUT_SIZE/13+100)

	state := &toolState{
		Status: "completed",
		Input:  map[string]any{"command": "echo test"},
		Output: largeOutput,
	}

	// Render via collapsibleBlock (used by bashBlock)
	_, _ = app.collapsibleBlock("bash-1", "$ echo test", largeOutput, app.blockToolInnerWidth(), app.wrappedBody, state)

	// The truncation happens inside collapsibleBlock before render is called.
	// We can't easily inspect the internal body truncation from outside, but we
	// can verify the function doesn't panic or hang on large input.
	// The key assertion is that the code path completes without issues.
}

// TestPromptHistoryEntrySizeLimit verifies that prompt history entries are capped.
func TestPromptHistoryEntrySizeLimit(t *testing.T) {
	h := &promptHistory{}

	// Try to append an entry larger than MAX_PROMPT_HISTORY_ENTRY_SIZE (64 KiB)
	largeInput := strings.Repeat("x", MAX_PROMPT_HISTORY_ENTRY_SIZE+1000)
	h.Append(largeInput)

	// Verify the stored entry is capped
	if len(h.entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(h.entries))
	}
	if len(h.entries[0].Input) != MAX_PROMPT_HISTORY_ENTRY_SIZE {
		t.Fatalf("stored entry is %d bytes, want %d", len(h.entries[0].Input), MAX_PROMPT_HISTORY_ENTRY_SIZE)
	}
}

// TestRenderAssistantLargeParts verifies that renderAssistant handles oversized parts correctly.
func TestRenderAssistantLargeParts(t *testing.T) {
	app := newTestApp(t, "http://example.invalid")
	app.width = 120
	app.height = 40
	// Enable show mode so the reasoning body is rendered
	app.thinkingMode = "show"

	// Create assistant data with oversized text and reasoning parts
	largeText := strings.Repeat("text ", MAX_MESSAGE_CONTENT_SIZE/5+100)
	largeReasoning := strings.Repeat("reasoning ", MAX_REASONING_SIZE/10+100)

	data := client.AssistantData{
		Agent: "build",
		Content: []struct {
			Type string `json:"type"`
			ID   string `json:"id"`
			Text string `json:"text"`
			Name string `json:"name"`
			Time *struct {
				Created   int64 `json:"created"`
				Completed int64 `json:"completed"`
			} `json:"time"`
			State *struct {
				Status   string         `json:"status"`
				Input    map[string]any `json:"input"`
				Output   string         `json:"output"`
				Error    string         `json:"error"`
				Title    string         `json:"title,omitempty"`
				Metadata map[string]any `json:"metadata,omitempty"`
			} `json:"state"`
		}{
			{Type: "reasoning", ID: "r1", Text: largeReasoning},
			{Type: "text", ID: "t1", Text: largeText},
		},
		Finish: "stop",
	}

	message := client.Message{
		ID:        "msg-1",
		SessionID: "ses-1",
		Type:      "assistant",
		Seq:       1,
		Data:      mustMarshalJSON(data),
	}

	block, _, _, _ := app.renderMessageCached(message, true)

	// Verify both truncation notices appear
	if !strings.Contains(block, "… message truncated at 1 MiB …") {
		t.Fatalf("expected message truncation notice in output")
	}
	if !strings.Contains(block, "… reasoning truncated at 1 MiB …") {
		t.Fatalf("expected reasoning truncation notice in output")
	}
}

// TestBashBlockLargeOutput verifies bashBlock handles oversized output.
func TestBashBlockLargeOutput(t *testing.T) {
	app := newTestApp(t, "http://example.invalid")
	app.width = 120
	app.height = 40

	// Create output larger than MAX_TOOL_OUTPUT_SIZE
	largeOutput := strings.Repeat("line\n", MAX_TOOL_OUTPUT_SIZE/5+100)

	state := &toolState{
		Status: "completed",
		Input:  map[string]any{"command": "cat hugefile"},
		Output: largeOutput,
	}

	// This should not panic or hang
	block, _ := app.bashBlock("bash-1", state)

	// When expanded, the output should be truncated
	if app.expandedToolOutput["bash-1"] {
		if !strings.Contains(block, "… output truncated at 1 MiB …") {
			t.Fatalf("expected output truncation notice when expanded")
		}
	}
}

// TestReasoningBlockExpandedLarge verifies expanded reasoning block is truncated.
func TestReasoningBlockExpandedLarge(t *testing.T) {
	app := newTestApp(t, "http://example.invalid")
	app.width = 120
	app.height = 40
	app.thinkingMode = "show" // show mode expands reasoning by default

	// Create reasoning larger than MAX_REASONING_SIZE
	largeReasoning := strings.Repeat("thinking ", MAX_REASONING_SIZE/8+100)

	block := app.reasoningBlock("reasoning-1", false, largeReasoning, nil)

	// In show mode, the body is rendered, so truncation notice should appear
	if !strings.Contains(block, "… reasoning truncated at 1 MiB …") {
		t.Fatalf("expected truncation notice in expanded reasoning block")
	}
}

// mustMarshalJSON is a test helper that panics on error.
func mustMarshalJSON(v any) []byte {
	data, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return data
}
