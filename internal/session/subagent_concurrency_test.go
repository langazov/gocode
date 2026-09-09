package session

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/langazov/gocode-go/internal/llm"
	"github.com/langazov/gocode-go/internal/tool"
	"github.com/langazov/gocode-go/internal/tool/builtins"
)

// concurrentSpawner spawns N children and holds each until the test releases
// it, so concurrency is observable rather than incidental: a child only
// reports done after its gate closes.
type concurrentSpawner struct {
	mu       sync.Mutex
	children []string
	gates    map[string]chan struct{}
}

func newConcurrentSpawner() *concurrentSpawner {
	return &concurrentSpawner{gates: map[string]chan struct{}{}}
}

func (s *concurrentSpawner) Spawn(ctx context.Context, req tool.SpawnRequest) (string, <-chan tool.SpawnResult, error) {
	s.mu.Lock()
	childID := "ses_child_" + req.AgentID // one per subagent_type
	s.children = append(s.children, childID)
	gate := make(chan struct{})
	s.gates[childID] = gate
	s.mu.Unlock()

	done := make(chan tool.SpawnResult, 1)
	go func() {
		<-gate
		done <- tool.SpawnResult{SessionID: childID, Text: "child " + childID + " done"}
		close(done)
	}()
	return childID, done, nil
}

func (s *concurrentSpawner) Cancel(childID string) {
	s.mu.Lock()
	gate := s.gates[childID]
	s.mu.Unlock()
	if gate != nil {
		close(gate)
	}
}

func (s *concurrentSpawner) Agent(id string) (bool, bool) { return true, true }

func (s *concurrentSpawner) Notify(ctx context.Context, parentSessionID, text string) error {
	return nil
}

func (s *concurrentSpawner) releaseAll() {
	s.mu.Lock()
	gates := make([]chan struct{}, 0, len(s.gates))
	for _, gate := range s.gates {
		gates = append(gates, gate)
	}
	s.mu.Unlock()
	for _, gate := range gates {
		select {
		case <-gate:
		default:
			close(gate)
		}
	}
}

// TestConcurrentTaskCallsRunAsSeparateChildren is the architectural guarantee
// the TUI's subagent surface rests on: two task calls in one assistant step
// dispatch on two worker goroutines, each spawning a child that runs on its
// own coordinator entry — genuinely concurrent, results fanning back over
// their own channels in any order. The children are held until both are
// known to be running; a serial implementation would deadlock on the first.
func TestConcurrentTaskCallsRunAsSeparateChildren(t *testing.T) {
	spawner := newConcurrentSpawner()
	registry := tool.NewRegistry()
	registry.Register(builtins.NewTaskTool(spawner))

	// Two task calls in ONE assistant step — the single-message fan-out the
	// tool description asks the model for.
	provider := &fakeProvider{turns: [][]llm.StreamEvent{
		{
			{Type: llm.EventToolCall, ToolCall: &llm.ToolCall{
				ID: "call_task_a", Name: "task",
				Input: map[string]any{
					"description":   "first half",
					"prompt":        "do the first half",
					"subagent_type": "general-a",
				},
			}},
			{Type: llm.EventToolCall, ToolCall: &llm.ToolCall{
				ID: "call_task_b", Name: "task",
				Input: map[string]any{
					"description":   "second half",
					"prompt":        "do the second half",
					"subagent_type": "general-b",
				},
			}},
			{Type: llm.EventFinish, Finish: "tool_use"},
		},
		{
			{Type: llm.EventTextDelta, Text: "both halves answered"},
			{Type: llm.EventFinish, Finish: "end_turn"},
		},
	}}
	runner, bus := newRunnerFixture(t, provider, registry)
	admitPrompt(t, bus, runner, "split the work")

	runDone := make(chan error, 1)
	go func() { runDone <- runner.Run(context.Background(), RunInput{SessionID: "ses_1"}) }()

	// Both children must exist (and be parked on their gates) before either
	// is released — the observable definition of concurrent children.
	deadline := time.After(10 * time.Second)
	for {
		spawner.mu.Lock()
		spawned := len(spawner.children)
		spawner.mu.Unlock()
		if spawned == 2 {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("only %d of 2 children spawned — the calls are running serially", spawned)
		case <-time.After(10 * time.Millisecond):
		}
	}
	spawner.releaseAll()

	select {
	case err := <-runDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the turn never finished after both children reported")
	}

	// Both results reached the continuation turn as tool outputs.
	var results []string
	for _, request := range provider.requests[1:] {
		for _, message := range request.Messages {
			if message.Role != llm.RoleTool {
				continue
			}
			for _, part := range message.Content {
				if part.Type == llm.PartToolResult {
					results = append(results, part.Result)
				}
			}
		}
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 tool results in the continuation, got %d (%v)", len(results), results)
	}
	joined := results[0] + results[1]
	for _, want := range []string{"ses_child_general-a", "ses_child_general-b"} {
		if !contains(joined, want) {
			t.Fatalf("continuation missing %s's result: %q", want, joined)
		}
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (haystack == needle ||
		indexOf(haystack, needle) >= 0)
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}

// TestInterruptingParentCancelsChild: cancellation travels downward. The
// parent's interrupt cancels its run context; the task tool forwards it as
// Cancel(childID), which interrupts the child's coordinator entry rather
// than orphaning it.
func TestInterruptingParentCancelsChild(t *testing.T) {
	spawner := newConcurrentSpawner()
	registry := tool.NewRegistry()
	registry.Register(builtins.NewTaskTool(spawner))

	provider := &fakeProvider{turns: [][]llm.StreamEvent{
		{
			{Type: llm.EventToolCall, ToolCall: &llm.ToolCall{
				ID: "call_task", Name: "task",
				Input: map[string]any{
					"description":   "long job",
					"prompt":        "take your time",
					"subagent_type": "general",
				},
			}},
			{Type: llm.EventFinish, Finish: "tool_use"},
		},
		// A cancellation races the continuation: the runner may or may not
		// ask for a second provider turn before the interrupt lands, so the
		// fake needs the entry to exist either way.
		{
			{Type: llm.EventTextDelta, Text: "unreached"},
			{Type: llm.EventFinish, Finish: "end_turn"},
		},
	}}
	runner, bus := newRunnerFixture(t, provider, registry)
	admitPrompt(t, bus, runner, "start the long job")

	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() { runDone <- runner.Run(ctx, RunInput{SessionID: "ses_1"}) }()

	// Wait until the child is spawned and parked, then interrupt the parent.
	deadline := time.After(10 * time.Second)
	for {
		spawner.mu.Lock()
		spawned := len(spawner.children)
		spawner.mu.Unlock()
		if spawned == 1 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("the child never spawned")
		case <-time.After(10 * time.Millisecond):
		}
	}
	cancel()

	select {
	case err := <-runDone:
		if err == nil {
			t.Fatal("the parent turn should report the interruption")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the parent turn never returned after the interrupt")
	}

	// The task tool forwarded the cancellation: the child's gate is closed
	// (its goroutine released) rather than left dangling.
	spawner.mu.Lock()
	gate := spawner.gates["ses_child_general"]
	spawner.mu.Unlock()
	select {
	case <-gate:
		// released — Cancel ran
	default:
		// The task tool's context-done path calls spawner.Cancel; the gate
		// close above is what Cancel does. Still open means it did not run.
		t.Fatal("the child was not cancelled with the parent")
	}
}

// TestTaskLinkSurvivesOnTheWire is the end-to-end version of the metadata
// seam: after a full parent turn with a task call, the projected assistant
// message carries the link the TUI reads. It re-asserts the same invariants
// as TestRunnerTaskMetadataLink but with two children, covering the
// fan-out case the row rendering has to distinguish.
func TestTaskLinkSurvivesOnTheWire(t *testing.T) {
	spawner := newConcurrentSpawner()
	registry := tool.NewRegistry()
	registry.Register(builtins.NewTaskTool(spawner))

	provider := &fakeProvider{turns: [][]llm.StreamEvent{
		{
			{Type: llm.EventToolCall, ToolCall: &llm.ToolCall{
				ID: "call_task_a", Name: "task",
				Input: map[string]any{
					"description":   "first",
					"prompt":        "one",
					"subagent_type": "general-a",
				},
			}},
			{Type: llm.EventToolCall, ToolCall: &llm.ToolCall{
				ID: "call_task_b", Name: "task",
				Input: map[string]any{
					"description":   "second",
					"prompt":        "two",
					"subagent_type": "general-b",
				},
			}},
			{Type: llm.EventFinish, Finish: "tool_use"},
		},
		{
			{Type: llm.EventTextDelta, Text: "done"},
			{Type: llm.EventFinish, Finish: "end_turn"},
		},
	}}
	runner, bus := newRunnerFixture(t, provider, registry)
	admitPrompt(t, bus, runner, "split it")

	// Release each child as it spawns: with both calls in one step the two
	// children may reach their gates in either order, and gating on "both
	// spawned" would turn this into the concurrency test above rather than
	// the wire-shape test it is.
	go func() {
		seen := map[string]bool{}
		for len(seen) < 2 {
			spawner.mu.Lock()
			pending := make([]string, 0, 2)
			for _, child := range spawner.children {
				if !seen[child] {
					seen[child] = true
					pending = append(pending, child)
				}
			}
			spawner.mu.Unlock()
			for _, child := range pending {
				spawner.Cancel(child) // closes the gate
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()
	if err := runner.Run(context.Background(), RunInput{SessionID: "ses_1"}); err != nil {
		t.Fatal(err)
	}

	messages, err := runner.Messages.List(context.Background(), "ses_1")
	if err != nil {
		t.Fatal(err)
	}
	links := map[string]string{} // childID -> call title
	for _, message := range messages {
		if message.Type != TypeAssistant {
			continue
		}
		var data struct {
			Content []struct {
				Type  string `json:"type"`
				Name  string `json:"name"`
				State *struct {
					Status   string         `json:"status"`
					Title    string         `json:"title"`
					Metadata map[string]any `json:"metadata"`
				} `json:"state"`
			} `json:"content"`
		}
		if err := json.Unmarshal(message.Data, &data); err != nil {
			t.Fatal(err)
		}
		for _, part := range data.Content {
			if part.Type != "tool" || part.Name != "task" || part.State == nil {
				continue
			}
			childID, _ := part.State.Metadata["sessionID"].(string)
			if childID == "" {
				t.Fatalf("task part settled without its child link: %+v", part.State)
			}
			if part.State.Status != ToolCompleted {
				t.Fatalf("task part status = %q, want completed", part.State.Status)
			}
			links[childID] = part.State.Title
		}
	}
	if len(links) != 2 {
		t.Fatalf("expected one link per child, got %v", links)
	}
	if links["ses_child_general-a"] != "first" || links["ses_child_general-b"] != "second" {
		t.Fatalf("links carry the wrong titles: %v", links)
	}
}
