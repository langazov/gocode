package session

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/langazov/gocode-go/internal/event"
	"github.com/langazov/gocode-go/internal/llm"
)

func TestSelectHistory(t *testing.T) {
	history := []StoredMessage{
		{Type: "user", Data: []byte(`{"text":"` + strings.Repeat("one ", 200) + `"}`)},
		{Type: "user", Data: []byte(`{"text":"recent question"}`)},
	}
	head, recent, ok := selectHistory(history, 50)
	if !ok {
		t.Fatal("expected selection")
	}
	if !strings.Contains(recent, "recent question") {
		t.Fatalf("recent should keep the small message, got %q", recent)
	}
	if !strings.Contains(head, "one one") {
		t.Fatalf("head should hold the large message, got %q", head[:40])
	}
}

func TestSelectHistorySkipsCompaction(t *testing.T) {
	history := []StoredMessage{
		{Type: "compaction", Data: []byte(`{"summary":"old","recent":"old recent"}`)},
	}
	if _, _, ok := selectHistory(history, 100); ok {
		t.Fatal("compaction-only history should not be selectable")
	}
}

func TestSerializeMessage(t *testing.T) {
	user := serializeMessage(StoredMessage{Type: "user", Data: []byte(`{"text":"hello"}`)})
	if user != "[User]: hello" {
		t.Fatalf("unexpected user serialization: %q", user)
	}
	assistant := serializeMessage(StoredMessage{Type: "assistant", Data: []byte(`{"content":[{"type":"text","text":"hi"},{"type":"tool","name":"bash","state":{"status":"completed","input":{"command":"ls"},"output":"file"}}]}`)})
	if !strings.Contains(assistant, "[Assistant]: hi") || !strings.Contains(assistant, "[Tool result]: file") {
		t.Fatalf("unexpected assistant serialization: %q", assistant)
	}
}

func TestNeedsCompaction(t *testing.T) {
	compactor := &Compactor{Settings: DefaultCompactionSettings()}
	if compactor.NeedsCompaction(nil, 100000, 1000) {
		t.Fatal("small context should not need compaction")
	}
	if !compactor.NeedsCompaction(nil, 100000, 95000) {
		t.Fatal("near-limit context should need compaction")
	}
	compactor.Settings.Auto = false
	if compactor.NeedsCompaction(nil, 100000, 95000) {
		t.Fatal("disabled compaction should not trigger")
	}
}

func TestIsContextOverflow(t *testing.T) {
	cases := []struct {
		message string
		want    bool
	}{
		{"maximum context length exceeded", true},
		{"Prompt is too long", true},
		{"request too large", true},
		{"connection refused", false},
		{"", false},
	}
	for _, c := range cases {
		err := errors.New(c.message)
		if got := isContextOverflow(err); got != c.want {
			t.Errorf("isContextOverflow(%q) = %v, want %v", c.message, got, c.want)
		}
	}
	if isContextOverflow(nil) {
		t.Error("nil error should not be overflow")
	}
}

type summaryProvider struct{}

func (p *summaryProvider) Stream(ctx context.Context, request llm.Request, emit func(llm.StreamEvent)) error {
	emit(llm.StreamEvent{Type: llm.EventTextDelta, Text: "## Objective\n- test summary"})
	emit(llm.StreamEvent{Type: llm.EventFinish, Finish: "end_turn"})
	return nil
}

func TestCompactProjectsCompactionMessage(t *testing.T) {
	bus, database := setup(t)
	RegisterRunnerProjectors(bus)
	store := NewMessageStore(database)
	ctx := context.Background()

	// Seed a large history directly as projected messages. Each message is
	// ~6k tokens so two of them exceed the default 8k keep-window.
	largeText := strings.Repeat("word ", 5000)
	for i := 0; i < 2; i++ {
		messageID := "msg_seed" + string(rune('a'+i))
		if _, err := bus.Publish(ctx, StepStarted, map[string]any{
			"sessionID":          "ses_1",
			"timestamp":          int64(1000 + i),
			"assistantMessageID": messageID,
			"agent":              "build",
			"model":              map[string]any{"providerID": "anthropic", "id": "claude"},
		}, event.PublishOptions{}); err != nil {
			t.Fatal(err)
		}
		if _, err := bus.Publish(ctx, TextStarted, map[string]any{
			"sessionID":          "ses_1",
			"timestamp":          int64(1001 + i),
			"assistantMessageID": messageID,
			"textID":             messageID + "-text",
		}, event.PublishOptions{}); err != nil {
			t.Fatal(err)
		}
		if _, err := bus.Publish(ctx, TextEnded, map[string]any{
			"sessionID":          "ses_1",
			"timestamp":          int64(1001 + i),
			"assistantMessageID": messageID,
			"textID":             messageID + "-text",
			"text":               largeText,
		}, event.PublishOptions{}); err != nil {
			t.Fatal(err)
		}
		if _, err := bus.Publish(ctx, StepEnded, map[string]any{
			"sessionID":          "ses_1",
			"timestamp":          int64(1002 + i),
			"assistantMessageID": messageID,
			"finish":             "end_turn",
			"cost":               0,
		}, event.PublishOptions{}); err != nil {
			t.Fatal(err)
		}
	}

	compactor := &Compactor{
		Bus:      bus,
		Provider: &summaryProvider{},
		Settings: DefaultCompactionSettings(),
	}
	history, err := store.ListForRunner(ctx, "ses_1")
	if err != nil {
		t.Fatal(err)
	}
	ok, err := compactor.Compact(ctx, "ses_1", history, ModelRef{ProviderID: "anthropic", ID: "claude"})
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("expected compaction to run")
	}

	compacted, err := store.ListForRunner(ctx, "ses_1")
	if err != nil {
		t.Fatal(err)
	}
	if len(compacted) != 1 || compacted[0].Type != "compaction" {
		t.Fatalf("expected history truncated to the compaction message, got %d messages", len(compacted))
	}
	if !strings.Contains(string(compacted[0].Data), "test summary") {
		t.Fatalf("compaction should carry the summary, got %s", compacted[0].Data)
	}
}
