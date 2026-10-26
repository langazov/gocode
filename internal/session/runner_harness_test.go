package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/langazov/gocode-go/internal/llm"
	"github.com/langazov/gocode-go/internal/tool"
)

type staticSource []string

func (s staticSource) System() []string { return s }

// denyGuard refuses one tool and annotates every other's output.
type denyGuard struct{ deny string }

func (g denyGuard) Before(name string, _ map[string]any) error {
	if name == g.deny {
		return errors.New("blocked by harness rule: not here")
	}
	return nil
}

func (g denyGuard) After(_ string, _ map[string]any, output string) string {
	return output + " [checked]"
}

// The system prompt is the agent charter, then instruction files, then the
// harness overlay — the project's own AGENTS.md is read before any adapted
// guidance layered over it.
func TestRunnerSystemOrderWithInstructionsAndHarness(t *testing.T) {
	provider := &fakeProvider{turns: [][]llm.StreamEvent{{
		{Type: llm.EventTextDelta, Text: "ok"},
		{Type: llm.EventFinish, Finish: "end_turn"},
	}}}
	runner, bus := newRunnerFixture(t, provider, tool.NewRegistry())
	runner.Instructions = staticSource{"Instructions from: /p/AGENTS.md\nproject rules"}
	runner.Harness = staticSource{"# HARNESS\n\nadapted rules"}
	admitPrompt(t, bus, runner, "hi")

	if err := runner.Run(context.Background(), RunInput{SessionID: "ses_1"}); err != nil {
		t.Fatal(err)
	}
	system := provider.requests[0].System
	if len(system) != 3 || system[0] != "You are gocode." ||
		!strings.Contains(system[1], "project rules") || !strings.Contains(system[2], "adapted rules") {
		t.Fatalf("system = %q", system)
	}
}

func TestRunnerGuardBlocksAndAnnotates(t *testing.T) {
	provider := &fakeProvider{turns: [][]llm.StreamEvent{
		{
			{Type: llm.EventToolCall, ToolCall: &llm.ToolCall{ID: "call_w", Name: "write", Input: map[string]any{"filePath": ".env"}}},
			{Type: llm.EventToolCall, ToolCall: &llm.ToolCall{ID: "call_r", Name: "read", Input: map[string]any{"filePath": "a"}}},
			{Type: llm.EventFinish, Finish: "tool_use"},
		},
		{{Type: llm.EventTextDelta, Text: "done"}, {Type: llm.EventFinish, Finish: "end_turn"}},
	}}
	registry := tool.NewRegistry()
	write := &fakeTool{name: "write", output: "written"}
	read := &fakeTool{name: "read", output: "contents"}
	registry.Register(write)
	registry.Register(read)
	runner, bus := newRunnerFixture(t, provider, registry)
	runner.Guard = denyGuard{deny: "write"}
	admitPrompt(t, bus, runner, "go")

	if err := runner.Run(context.Background(), RunInput{SessionID: "ses_1"}); err != nil {
		t.Fatal(err)
	}
	if len(write.inputs) != 0 {
		t.Fatal("a guarded tool ran")
	}
	if result := toolResultFor(provider.requests[1], "call_w"); !strings.Contains(result, "not here") {
		t.Fatalf("write result = %q", result)
	}
	if result := toolResultFor(provider.requests[1], "call_r"); !strings.Contains(result, "contents [checked]") {
		t.Fatalf("read result = %q", result)
	}
}

// The session's history marks where a turn first ran under a harness, under a
// changed one, and under none after having one — and only there. The marker
// is for readers; the model never sees it.
func TestRunnerMarksHarnessInHistory(t *testing.T) {
	answer := []llm.StreamEvent{{Type: llm.EventTextDelta, Text: "ok"}, {Type: llm.EventFinish, Finish: "end_turn"}}
	provider := &fakeProvider{turns: [][]llm.StreamEvent{answer, answer, answer, answer, answer}}
	runner, bus := newRunnerFixture(t, provider, tool.NewRegistry())

	var current *HarnessInfo
	runner.DescribeHarness = func() (HarnessInfo, bool) {
		if current == nil {
			return HarnessInfo{}, false
		}
		return *current, true
	}
	turns := 0
	turn := func(info *HarnessInfo) {
		t.Helper()
		current = info
		turns++
		// admitPrompt reuses one message ID; each turn here needs its own.
		if _, err := Admit(context.Background(), bus, runner.DB, AdmitInput{
			ID: fmt.Sprintf("msg_user_%d", turns), SessionID: "ses_1", Prompt: Prompt{Text: "go"}, Delivery: DeliverySteer,
		}); err != nil {
			t.Fatal(err)
		}
		if err := runner.Run(context.Background(), RunInput{SessionID: "ses_1"}); err != nil {
			t.Fatal(err)
		}
	}
	installed := &HarnessInfo{Dir: "/p/.gocode/harness", Fingerprint: "aaa", Skills: []string{"lint"}, Hooks: 1}
	turn(nil)                                                         // never had one: nothing
	turn(installed)                                                   // loaded
	turn(installed)                                                   // unchanged: nothing
	turn(&HarnessInfo{Dir: "/p/.gocode/harness", Fingerprint: "bbb"}) // changed
	turn(nil)                                                         // removed

	messages, err := runner.Messages.List(context.Background(), "ses_1")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, message := range messages {
		if message.Type != TypeHarness {
			continue
		}
		var marker HarnessMessage
		if err := json.Unmarshal(message.Data, &marker); err != nil {
			t.Fatal(err)
		}
		got = append(got, fmt.Sprintf("loaded=%v changed=%v %s %v hooks=%d", marker.Loaded, marker.Changed, marker.Fingerprint, marker.Skills, marker.Hooks))
	}
	want := []string{
		"loaded=true changed=false aaa [lint] hooks=1",
		"loaded=true changed=true bbb [] hooks=0",
		"loaded=false changed=false  [] hooks=0",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("markers:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	for _, request := range provider.requests {
		for _, message := range request.Messages {
			for _, part := range message.Content {
				if strings.Contains(part.Text, "aaa") || strings.Contains(part.Text, "fingerprint") {
					t.Fatalf("the marker reached the model: %q", part.Text)
				}
			}
		}
	}
}
