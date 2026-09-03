package builtins

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/langazov/gocode-go/internal/background"
	"github.com/langazov/gocode-go/internal/tool"
)

// stubSpawner records spawns and hands each one a channel the test controls.
type stubSpawner struct {
	mu        sync.Mutex
	spawns    []tool.SpawnRequest
	results   map[string]chan tool.SpawnResult
	cancelled []string
	notified  []string
	primary   map[string]bool
	unknown   map[string]bool
}

func newStubSpawner() *stubSpawner {
	return &stubSpawner{
		results: map[string]chan tool.SpawnResult{},
		primary: map[string]bool{},
		unknown: map[string]bool{},
	}
}

func (s *stubSpawner) Spawn(ctx context.Context, req tool.SpawnRequest) (string, <-chan tool.SpawnResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.spawns = append(s.spawns, req)
	childID := "ses_child_" + req.AgentID
	ch := make(chan tool.SpawnResult, 1)
	s.results[childID] = ch
	return childID, ch, nil
}

func (s *stubSpawner) Cancel(childID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cancelled = append(s.cancelled, childID)
}

func (s *stubSpawner) Agent(id string) (bool, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.unknown[id] {
		return false, false
	}
	return true, !s.primary[id]
}

func (s *stubSpawner) Notify(ctx context.Context, parentSessionID, text string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.notified = append(s.notified, parentSessionID+": "+text)
	return nil
}

func (s *stubSpawner) finish(childID string, result tool.SpawnResult) {
	s.mu.Lock()
	ch := s.results[childID]
	s.mu.Unlock()
	ch <- result
}

func (s *stubSpawner) notifications() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.notified...)
}

var taskExec = tool.ExecContext{SessionID: "ses_parent", Agent: "build", CallID: "call_1"}

func taskInput(extra map[string]any) map[string]any {
	input := map[string]any{
		"description":   "research something",
		"prompt":        "go and research",
		"subagent_type": "general",
	}
	for k, v := range extra {
		input[k] = v
	}
	return input
}

func TestTaskToolForegroundReturnsResult(t *testing.T) {
	spawner := newStubSpawner()
	task := NewTaskTool(spawner)

	out := make(chan string, 1)
	errs := make(chan error, 1)
	go func() {
		result, err := task.ExecuteWithContext(context.Background(), taskInput(nil), taskExec)
		out <- result
		errs <- err
	}()

	time.Sleep(50 * time.Millisecond)
	spawner.finish("ses_child_general", tool.SpawnResult{
		SessionID: "ses_child_general", Text: "found it",
	})

	select {
	case result := <-out:
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(result, "found it") {
			t.Fatalf("result missing the subagent's text: %q", result)
		}
		if !strings.Contains(result, `state="completed"`) {
			t.Fatalf("result missing completed state: %q", result)
		}
		if !strings.Contains(result, "ses_child_general") {
			t.Fatalf("result missing a resumable task id: %q", result)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("task did not return")
	}
}

func TestTaskToolRejectsBackgroundWhenDisabled(t *testing.T) {
	task := NewTaskTool(newStubSpawner())
	_, err := task.ExecuteWithContext(context.Background(),
		taskInput(map[string]any{"background": true}), taskExec)
	if err == nil || !strings.Contains(err.Error(), "GOCODE_EXPERIMENTAL_BACKGROUND_SUBAGENTS") {
		t.Fatalf("expected background mode to be gated, got %v", err)
	}
}

func TestTaskToolRejectsPrimaryAgent(t *testing.T) {
	spawner := newStubSpawner()
	spawner.primary["build"] = true
	task := NewTaskTool(spawner)
	_, err := task.ExecuteWithContext(context.Background(),
		taskInput(map[string]any{"subagent_type": "build"}), taskExec)
	if err == nil || !strings.Contains(err.Error(), "primary agent") {
		t.Fatalf("expected a primary-agent rejection, got %v", err)
	}
}

func TestTaskToolRejectsUnknownAgent(t *testing.T) {
	spawner := newStubSpawner()
	spawner.unknown["ghost"] = true
	task := NewTaskTool(spawner)
	_, err := task.ExecuteWithContext(context.Background(),
		taskInput(map[string]any{"subagent_type": "ghost"}), taskExec)
	if err == nil || !strings.Contains(err.Error(), "not a valid agent type") {
		t.Fatalf("expected an unknown-agent rejection, got %v", err)
	}
}

// TestTaskToolBackgroundReturnsImmediately is the phase 3 acceptance check:
// background mode must not block on the child, and the result must arrive
// later as a notification into the parent session.
func TestTaskToolBackgroundReturnsImmediately(t *testing.T) {
	spawner := newStubSpawner()
	jobs := background.NewRegistry()
	task := NewBackgroundTaskTool(spawner, jobs)

	result, err := task.ExecuteWithContext(context.Background(),
		taskInput(map[string]any{"background": true}), taskExec)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, `state="running"`) {
		t.Fatalf("background task did not report as running: %q", result)
	}
	if len(spawner.notifications()) != 0 {
		t.Fatal("notified the parent before the child finished")
	}

	spawner.finish("ses_child_general", tool.SpawnResult{
		SessionID: "ses_child_general", Text: "background answer",
	})

	deadline := time.After(5 * time.Second)
	for {
		notes := spawner.notifications()
		if len(notes) == 1 {
			if !strings.Contains(notes[0], "background answer") {
				t.Fatalf("notification missing the result: %q", notes[0])
			}
			if !strings.HasPrefix(notes[0], "ses_parent: ") {
				t.Fatalf("notification went to the wrong session: %q", notes[0])
			}
			return
		}
		select {
		case <-deadline:
			t.Fatalf("parent was never notified, notifications = %v", notes)
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// TestTaskToolPromotionUnblocksCaller covers the third race arm: a foreground
// task that gets promoted returns "running" instead of continuing to block.
func TestTaskToolPromotionUnblocksCaller(t *testing.T) {
	spawner := newStubSpawner()
	jobs := background.NewRegistry()
	task := NewBackgroundTaskTool(spawner, jobs)

	out := make(chan string, 1)
	errs := make(chan error, 1)
	go func() {
		result, err := task.ExecuteWithContext(context.Background(), taskInput(nil), taskExec)
		out <- result
		errs <- err
	}()

	// Wait for the job to register, then push it to the background.
	deadline := time.After(5 * time.Second)
	for {
		if _, ok := jobs.Get("ses_child_general"); ok {
			break
		}
		select {
		case <-deadline:
			t.Fatal("job was never registered")
		case <-time.After(5 * time.Millisecond):
		}
	}
	if !jobs.Promote("ses_child_general") {
		t.Fatal("promote failed")
	}

	select {
	case result := <-out:
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(result, `state="running"`) {
			t.Fatalf("promoted task did not return a running state: %q", result)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("promotion did not unblock the task call")
	}

	// The work continues and still reports in.
	spawner.finish("ses_child_general", tool.SpawnResult{
		SessionID: "ses_child_general", Text: "late answer",
	})
	for {
		if notes := spawner.notifications(); len(notes) == 1 {
			if !strings.Contains(notes[0], "late answer") {
				t.Fatalf("notification missing the result: %q", notes[0])
			}
			return
		}
		select {
		case <-time.After(5 * time.Second):
			t.Fatal("promoted task never notified the parent")
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// TestTaskToolCancelsChildOnInterrupt guards cleanup.
func TestTaskToolCancelsChildOnInterrupt(t *testing.T) {
	spawner := newStubSpawner()
	task := NewTaskTool(spawner)
	ctx, cancel := context.WithCancel(context.Background())

	errs := make(chan error, 1)
	go func() {
		_, err := task.ExecuteWithContext(ctx, taskInput(nil), taskExec)
		errs <- err
	}()
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-errs:
		if err == nil {
			t.Fatal("expected an interruption error")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("task did not observe the interrupt")
	}
	spawner.mu.Lock()
	defer spawner.mu.Unlock()
	if len(spawner.cancelled) != 1 || spawner.cancelled[0] != "ses_child_general" {
		t.Fatalf("child was not cancelled: %v", spawner.cancelled)
	}
}

func TestTaskToolSchemaGatesBackground(t *testing.T) {
	plain := NewTaskTool(newStubSpawner()).InputSchema()
	if _, ok := plain["properties"].(map[string]any)["background"]; ok {
		t.Fatal("background is advertised while the feature is disabled")
	}
	enabled := NewBackgroundTaskTool(newStubSpawner(), background.NewRegistry()).InputSchema()
	if _, ok := enabled["properties"].(map[string]any)["background"]; !ok {
		t.Fatal("background is not advertised while the feature is enabled")
	}
}
