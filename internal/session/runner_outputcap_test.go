package session

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/langazov/gocode-go/internal/event"
	"github.com/langazov/gocode-go/internal/llm"
	"github.com/langazov/gocode-go/internal/tool"
)

// The runner is the last line of defense before a tool result is persisted
// and replayed. A tool (an MCP server, a plugin hook) can return megabytes;
// without the cap that result lands whole in the event and session_message
// rows and in every later request to the model.

func TestCapPersistedOutputTruncates(t *testing.T) {
	huge := strings.Repeat("x", maxPersistedToolOutputBytes+4096)
	capped := capPersistedOutput(huge)
	if len(capped) > maxPersistedToolOutputBytes+64 {
		t.Fatalf("capped output is %d bytes, want <= %d+64", len(capped), maxPersistedToolOutputBytes)
	}
	if !strings.HasSuffix(capped, "\n(output truncated at 512 KiB)") {
		t.Fatalf("capped output misses the truncation marker: %q", capped[len(capped)-64:])
	}
}

func TestCapPersistedOutputKeepsShortOutput(t *testing.T) {
	for _, text := range []string{"", "ok", strings.Repeat("y", maxPersistedToolOutputBytes)} {
		if got := capPersistedOutput(text); got != text {
			t.Fatalf("capPersistedOutput(%d bytes) changed the output", len(text))
		}
	}
}

func TestCapPersistedOutputCutsOnRuneBoundary(t *testing.T) {
	// Fill to the cap with single bytes, then start a 3-byte rune exactly at
	// the boundary: the cut must back off to the rune start rather than split
	// the rune and produce invalid UTF-8 in the stored row.
	rune3 := "✓" // 3 bytes
	input := strings.Repeat("a", maxPersistedToolOutputBytes) + rune3 + "tail"
	capped := capPersistedOutput(input)
	body := strings.TrimSuffix(capped, "\n(output truncated at 512 KiB)")
	if !utf8.ValidString(body) {
		t.Fatal("capped output splits a multi-byte rune")
	}
	if !strings.HasPrefix(body, strings.Repeat("a", maxPersistedToolOutputBytes-2)) {
		t.Fatalf("the cut did not back off to the rune boundary: %q", body[len(body)-8:])
	}
}

// publishSettlement must cap what reaches the bus: the bus is what both the
// event table and the projected session_message rows are written from, and
// what the interface renders live.
func TestPublishSettlementCapsHugeToolOutput(t *testing.T) {
	registry := tool.NewRegistry()
	registry.Register(&fakeTool{name: "spew"})
	runner, bus := newRunnerFixture(t, nil, registry)
	events, unsubscribe := bus.Subscribe(8)
	defer unsubscribe()
	seedAssistantMessage(t, runner, "msg_a")

	huge := strings.Repeat("z", maxPersistedToolOutputBytes*4)
	err := runner.publishSettlement(context.Background(), "ses_1", "msg_a", settlement{
		call:   llm.ToolCall{ID: "call_1", Name: "spew"},
		output: huge,
	})
	if err != nil {
		t.Fatal(err)
	}
	for payload := range events {
		if payload.Type != ToolSuccess.Type {
			continue
		}
		output, _ := payload.Data["output"].(string)
		if len(output) > maxPersistedToolOutputBytes+64 {
			t.Fatalf("published output is %d bytes, want <= %d+64", len(output), maxPersistedToolOutputBytes)
		}
		if !strings.Contains(output, "(output truncated at 512 KiB)") {
			t.Fatalf("published output missing truncation marker")
		}
		break
	}
}

func TestPublishSettlementCapsHugeErrorText(t *testing.T) {
	registry := tool.NewRegistry()
	runner, bus := newRunnerFixture(t, nil, registry)
	events, unsubscribe := bus.Subscribe(8)
	defer unsubscribe()
	seedAssistantMessage(t, runner, "msg_a")

	err := runner.publishSettlement(context.Background(), "ses_1", "msg_a", settlement{
		call: llm.ToolCall{ID: "call_1", Name: "spew"},
		err:  errHugeText{},
	})
	if err != nil {
		t.Fatal(err)
	}
	for payload := range events {
		if payload.Type != ToolFailed.Type {
			continue
		}
		errValue, _ := payload.Data["error"].(map[string]any)
		message, _ := errValue["message"].(string)
		if len(message) > maxPersistedToolOutputBytes+64 {
			t.Fatalf("published error text is %d bytes, want <= %d+64", len(message), maxPersistedToolOutputBytes)
		}
		break
	}
}

type errHugeText struct{}

func (errHugeText) Error() string { return strings.Repeat("E", maxPersistedToolOutputBytes*4) }

// seedAssistantMessage creates the assistant row the settlement projectors
// update, the way a real turn's StepStarted event would.
func seedAssistantMessage(t *testing.T, runner *Runner, messageID string) {
	t.Helper()
	_, err := runner.Bus.Publish(context.Background(), StepStarted, map[string]any{
		"sessionID":          "ses_1",
		"timestamp":          nowMillis(),
		"assistantMessageID": messageID,
		"agent":              "build",
		"model":              map[string]any{"providerID": "anthropic", "id": "claude-sonnet-4-5"},
	}, event.PublishOptions{})
	if err != nil {
		t.Fatal(err)
	}
}

// The capped settlement must also be what lands in the stored session
// message: the projector reads the same event, so the row is bounded too.
func TestSettledToolOutputIsCappedInTheStoredMessage(t *testing.T) {
	registry := tool.NewRegistry()
	huge := strings.Repeat("z", maxPersistedToolOutputBytes*4)
	registry.Register(&fakeTool{name: "spew", output: huge})
	provider := &fakeProvider{turns: [][]llm.StreamEvent{
		{
			{Type: llm.EventToolCall, ToolCall: &llm.ToolCall{
				ID:    "call_1",
				Name:  "spew",
				Input: map[string]any{},
			}},
			{Type: llm.EventFinish, Finish: "tool_use"},
		},
		{
			{Type: llm.EventTextDelta, Text: "done"},
			{Type: llm.EventFinish, Finish: "end_turn"},
		},
	}}
	runner, bus := newRunnerFixture(t, provider, registry)
	_, unsubscribe := bus.Subscribe(64)
	defer unsubscribe()
	admitPrompt(t, bus, runner, "run the spew tool")

	if err := runner.Run(context.Background(), RunInput{SessionID: "ses_1"}); err != nil {
		t.Fatal(err)
	}

	messages, err := NewMessageStore(runner.DB).List(context.Background(), "ses_1")
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, message := range messages {
		if message.Type != TypeAssistant {
			continue
		}
		var data struct {
			Content []struct {
				Type  string `json:"type"`
				ID    string `json:"id"`
				Name  string `json:"name"`
				State *struct {
					Status string `json:"status"`
					Output string `json:"output"`
				} `json:"state"`
			} `json:"content"`
		}
		if err := json.Unmarshal(message.Data, &data); err != nil {
			t.Fatal(err)
		}
		for _, part := range data.Content {
			if part.Type != "tool" || part.State == nil || part.State.Status != ToolCompleted {
				continue
			}
			found = true
			if len(part.State.Output) > maxPersistedToolOutputBytes+64 {
				t.Fatalf("stored tool output is %d bytes, want <= %d+64",
					len(part.State.Output), maxPersistedToolOutputBytes)
			}
			if !strings.Contains(part.State.Output, "(output truncated at 512 KiB)") {
				t.Fatal("stored tool output missing the truncation marker")
			}
		}
	}
	if !found {
		t.Fatal("no settled tool part found in the stored history")
	}
}
