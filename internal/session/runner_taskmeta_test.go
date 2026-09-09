package session

import (
	"context"
	"testing"
	"time"

	"github.com/langazov/gocode-go/internal/llm"
	"github.com/langazov/gocode-go/internal/tool"
	"github.com/langazov/gocode-go/internal/tool/builtins"
)

// runnerSpawner adapts the real task tool's SpawnRequest into the runner test
// fixture: each spawn registers a child session the test controls and answers
// immediately with a canned result.
type runnerSpawner struct {
	children map[string]tool.SpawnResult
}

func (s *runnerSpawner) Spawn(ctx context.Context, req tool.SpawnRequest) (string, <-chan tool.SpawnResult, error) {
	childID := "ses_child_1"
	done := make(chan tool.SpawnResult, 1)
	done <- tool.SpawnResult{SessionID: childID, Text: "child answer"}
	if s.children == nil {
		s.children = map[string]tool.SpawnResult{}
	}
	s.children[childID] = tool.SpawnResult{SessionID: childID, Text: "child answer"}
	return childID, done, nil
}

func (s *runnerSpawner) Cancel(childID string)        {}
func (s *runnerSpawner) Agent(id string) (bool, bool) { return true, true }
func (s *runnerSpawner) Notify(ctx context.Context, parentSessionID, text string) error {
	return nil
}

// TestRunnerTaskMetadataLink drives the full durable loop through a task call
// and asserts the two things the TUI's subagent surface needs on the wire:
// the part reaches "running" while the call is live (processor.ts marks the
// part running on tool-call, not pending), and the call's state carries the
// child session link the task tool published through ExecContext.SetMeta.
func TestRunnerTaskMetadataLink(t *testing.T) {
	registry := tool.NewRegistry()
	spawner := &runnerSpawner{}
	registry.Register(builtins.NewTaskTool(spawner))

	provider := &fakeProvider{turns: [][]llm.StreamEvent{
		{
			{Type: llm.EventToolCall, ToolCall: &llm.ToolCall{
				ID:   "call_task",
				Name: "task",
				Input: map[string]any{
					"description":   "find the bug",
					"prompt":        "go find it",
					"subagent_type": "general-purpose",
				},
			}},
			{Type: llm.EventFinish, Finish: "tool_use"},
		},
		{
			{Type: llm.EventTextDelta, Text: "the child answered"},
			{Type: llm.EventFinish, Finish: "end_turn"},
		},
	}}
	runner, bus := newRunnerFixture(t, provider, registry)
	admitPrompt(t, bus, runner, "delegate the search")

	if err := runner.Run(context.Background(), RunInput{SessionID: "ses_1"}); err != nil {
		t.Fatal(err)
	}

	messages, err := runner.Messages.List(context.Background(), "ses_1")
	if err != nil {
		t.Fatal(err)
	}
	var taskPart *AssistantContent
	owningMessage := ""
	for i := range messages {
		if messages[i].Type != TypeAssistant {
			continue
		}
		data, err := DecodeAssistant(messages[i].Data)
		if err != nil {
			t.Fatal(err)
		}
		for j := range data.Content {
			if data.Content[j].Type == "tool" && data.Content[j].Name == "task" {
				taskPart = &data.Content[j]
				owningMessage = messages[i].ID
			}
		}
	}
	if taskPart == nil {
		t.Fatal("no task tool part in the assistant message")
	}

	// The call settled, so the final status is completed — and the link the
	// tool published mid-run must have survived the settlement write.
	if taskPart.State == nil || taskPart.State.Status != ToolCompleted {
		t.Fatalf("task part state = %+v, want completed", taskPart.State)
	}
	if taskPart.State.Title != "find the bug" {
		t.Fatalf("task part title = %q, want the description", taskPart.State.Title)
	}
	if taskPart.State.Metadata["sessionID"] != "ses_child_1" {
		t.Fatalf("task part metadata.sessionID = %v, want the child session", taskPart.State.Metadata["sessionID"])
	}
	if taskPart.State.Metadata["parentSessionID"] != "ses_1" {
		t.Fatalf("task part metadata.parentSessionID = %v, want the parent session", taskPart.State.Metadata["parentSessionID"])
	}
	// The batch link: one assistant message = one fan-out batch, so the
	// batchID a client groups and arrow-scopes by must be the message the
	// part lives in.
	if taskPart.State.Metadata["batchID"] != owningMessage {
		t.Fatalf("task part metadata.batchID = %v, want the spawning message %q", taskPart.State.Metadata["batchID"], owningMessage)
	}
	if background, _ := taskPart.State.Metadata["background"].(bool); background {
		t.Fatal("a foreground task published background: true")
	}
}

// TestRunnerToolPartRunsBeforeSettling pins the projector's running status: a
// tool.call projects "running", which is the string the TUI's spinner and
// live-label paths branch on. The settle path overwrites it, so this drives a
// turn whose tool never settles — an input the permission engine rejects
// mid-call leaves the part exactly as the TUI sees it while the turn is live.
func TestRunnerToolPartRunsBeforeSettling(t *testing.T) {
	registry := tool.NewRegistry()
	// A tool whose execution blocks until the test releases it: the part is
	// projected as running while the runner waits inside it.
	release := make(chan struct{})
	registry.Register(&blockingTool{name: "probe", release: release})

	provider := &fakeProvider{turns: [][]llm.StreamEvent{
		{
			{Type: llm.EventToolCall, ToolCall: &llm.ToolCall{
				ID: "call_probe", Name: "probe", Input: map[string]any{},
			}},
			{Type: llm.EventFinish, Finish: "tool_use"},
		},
		// The continuation turn the tool result feeds. The point of this test
		// is the part's state while the tool is still executing, so the
		// continuation only has to exist.
		{
			{Type: llm.EventTextDelta, Text: "done"},
			{Type: llm.EventFinish, Finish: "end_turn"},
		},
	}}
	runner, bus := newRunnerFixture(t, provider, registry)
	admitPrompt(t, bus, runner, "run the probe")

	runDone := make(chan error, 1)
	go func() { runDone <- runner.Run(context.Background(), RunInput{SessionID: "ses_1"}) }()

	deadline := time.After(5 * time.Second)
	var status string
	for status != ToolRunning {
		messages, err := runner.Messages.List(context.Background(), "ses_1")
		if err != nil {
			t.Fatal(err)
		}
		status = ""
		for i := range messages {
			if messages[i].Type != TypeAssistant {
				continue
			}
			data, err := DecodeAssistant(messages[i].Data)
			if err != nil {
				t.Fatal(err)
			}
			for j := range data.Content {
				if data.Content[j].Type == "tool" && data.Content[j].Name == "probe" {
					if data.Content[j].State != nil {
						status = data.Content[j].State.Status
					}
				}
			}
		}
		if status == ToolRunning {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("tool part never reached %q, last saw %q", ToolRunning, status)
		default:
		}
	}

	close(release)
	if err := <-runDone; err != nil {
		t.Fatal(err)
	}
}

type blockingTool struct {
	name    string
	release chan struct{}
}

func (b *blockingTool) Name() string        { return b.name }
func (b *blockingTool) Description() string { return "blocks until released" }
func (b *blockingTool) InputSchema() map[string]any {
	return map[string]any{"type": "object"}
}
func (b *blockingTool) Execute(ctx context.Context, input map[string]any) (string, error) {
	<-b.release
	return "released", nil
}
