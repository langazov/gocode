package tui

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/langazov/gocode-go/internal/tui/client"
)

// TestAggregateCoalesces is the phase 4 acceptance check: a flood of events
// must collapse into a handful of snapshots, so the main goroutine's work is
// bounded by the frame rate rather than by how many agents are running.
func TestAggregateCoalesces(t *testing.T) {
	events := make(chan client.Event)
	snapshots := make(chan Snapshot, 64)
	ctx, stop := context.WithCancel(context.Background())
	defer stop()

	frame := 16 * time.Millisecond
	// Aggregate returns when the event channel closes; closing snapshots
	// after it returns is what lets the range below terminate. Run() wires it
	// the same way.
	go func() {
		defer close(snapshots)
		Aggregate(ctx, events, snapshots, frame)
	}()

	const total = 10000
	const sessions = 5
	start := time.Now()
	for i := range total {
		events <- client.Event{
			Type:    "session.next.text.delta",
			Session: fmt.Sprintf("ses_%d", i%sessions),
			Data:    map[string]any{"assistantMessageID": "msg", "delta": "x"},
		}
	}
	elapsed := time.Since(start)
	close(events)

	var got []Snapshot
	for snapshot := range snapshots {
		got = append(got, snapshot)
	}

	// One snapshot per elapsed frame, plus the final flush and a little slack
	// for scheduling.
	budget := int(elapsed/frame) + 4
	if len(got) > budget {
		t.Fatalf("%d events over %v produced %d snapshots, want <= %d", total, elapsed, len(got), budget)
	}
	if len(got) == 0 {
		t.Fatal("no snapshot was emitted")
	}
	last := got[len(got)-1]
	if len(last.Sessions) != sessions {
		t.Fatalf("snapshot covers %d sessions, want %d", len(last.Sessions), sessions)
	}
	t.Logf("%d events over %v collapsed into %d snapshots", total, elapsed, len(got))
}

// TestAggregateRoutesBySession pins the isolation rule: an event only ever
// mutates its own session's node.
func TestAggregateRoutesBySession(t *testing.T) {
	state := newTree()
	state.apply(client.Event{Type: "session.next.text.delta", Session: "parent",
		Data: map[string]any{"assistantMessageID": "m", "delta": "P"}})
	state.apply(client.Event{Type: "session.next.text.delta", Session: "child",
		Data: map[string]any{"assistantMessageID": "m", "delta": "C"}})
	state.apply(client.Event{Type: "session.next.tool.called", Session: "child",
		Data: map[string]any{"callID": "call_1", "tool": "bash"}})

	snapshot := state.snapshot(0)
	if got := snapshot.Sessions["parent"].Text["m"].String(); got != "P" {
		t.Fatalf("parent text = %q, want %q", got, "P")
	}
	if got := snapshot.Sessions["child"].Text["m"].String(); got != "C" {
		t.Fatalf("child text = %q, want %q", got, "C")
	}
	if len(snapshot.Sessions["parent"].Tools) != 0 {
		t.Fatal("a child tool call landed on the parent node")
	}
	if snapshot.Sessions["child"].Tools["call_1"].Name != "bash" {
		t.Fatalf("child tool not recorded: %+v", snapshot.Sessions["child"].Tools)
	}
	if !snapshot.Dirty["child"] || snapshot.Dirty["parent"] {
		t.Fatalf("dirty set = %+v, want only the child", snapshot.Dirty)
	}
}

// TestAggregateNeverBlocksOnSlowConsumer is the liveness guarantee: a consumer
// that stops reading must not stall the event stream. Dropping a snapshot is
// safe because each one carries complete state.
func TestAggregateNeverBlocksOnSlowConsumer(t *testing.T) {
	events := make(chan client.Event)
	snapshots := make(chan Snapshot) // unbuffered, never read
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	go Aggregate(ctx, events, snapshots, time.Millisecond)

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := range 500 {
			events <- client.Event{Type: "session.next.text.delta", Session: "ses_1",
				Data: map[string]any{"assistantMessageID": "m", "delta": fmt.Sprint(i)}}
			if i%50 == 0 {
				time.Sleep(2 * time.Millisecond) // let frames elapse and be dropped
			}
		}
	}()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("a consumer that never reads blocked the event stream")
	}
}

// TestSnapshotIsIsolatedFromLaterEvents guards the goroutine boundary: a
// published snapshot must not be mutated by events that arrive afterwards.
func TestSnapshotIsIsolatedFromLaterEvents(t *testing.T) {
	state := newTree()
	state.apply(client.Event{Type: "session.next.text.delta", Session: "ses_1",
		Data: map[string]any{"assistantMessageID": "m", "delta": "first"}})
	snapshot := state.snapshot(0)

	state.apply(client.Event{Type: "session.next.text.delta", Session: "ses_1",
		Data: map[string]any{"assistantMessageID": "m", "delta": "-second"}})

	if got := snapshot.Sessions["ses_1"].Text["m"].String(); got != "first" {
		t.Fatalf("published snapshot was mutated after the fact: %q", got)
	}
}

type recordingSender struct {
	mu   sync.Mutex
	msgs []tea.Msg
}

func (s *recordingSender) Send(msg tea.Msg) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.msgs = append(s.msgs, msg)
}

func TestPumpSnapshotsForwardsAll(t *testing.T) {
	sender := &recordingSender{}
	snapshots := make(chan Snapshot, 3)
	for range 3 {
		snapshots <- Snapshot{Sessions: map[string]*SessionNode{}}
	}
	close(snapshots)

	var wg sync.WaitGroup
	wg.Add(1)
	go pumpSnapshots(sender, snapshots, &wg)
	wg.Wait()

	sender.mu.Lock()
	defer sender.mu.Unlock()
	if len(sender.msgs) != 3 {
		t.Fatalf("forwarded %d snapshots, want 3", len(sender.msgs))
	}
	if _, ok := sender.msgs[0].(snapshotMsg); !ok {
		t.Fatalf("unexpected message type %T", sender.msgs[0])
	}
}

// A turn is many steps, and the gaps between them — the model deciding what to
// do next, a tool running — are most of its wall-clock time. Busy has to
// survive those gaps: it used to drop on every step.ended and come back on the
// next step.started, which is why the footer spinner disappeared partway
// through a task and reappeared seconds later.
func TestBusySurvivesTheGapBetweenSteps(t *testing.T) {
	state := newTree()
	step := func(kind string) {
		state.apply(client.Event{Type: "session.next." + kind, Session: "ses_1"})
	}

	state.apply(client.Event{Type: "session.next.run.started", Session: "ses_1"})
	step("step.started")
	step("step.ended")
	if !state.node("ses_1").Busy {
		t.Fatal("a settled step must not idle the session: the turn continues")
	}

	step("step.started")
	step("step.failed")
	if !state.node("ses_1").Busy {
		t.Fatal("a failed step is still not the end of the turn")
	}

	state.apply(client.Event{Type: "session.next.run.ended", Session: "ses_1"})
	if state.node("ses_1").Busy {
		t.Fatal("run.ended is the turn boundary and must idle the session")
	}
}

// A client that connects mid-turn never sees run.started, so a step start has
// to be enough to report the session busy.
func TestStepStartedMarksBusyForALateJoiner(t *testing.T) {
	state := newTree()
	state.apply(client.Event{Type: "session.next.step.started", Session: "ses_1"})
	if !state.node("ses_1").Busy {
		t.Fatal("a step start must mark the session busy on its own")
	}
}

// A drain failure has to reach the interface. It used to go to the background
// log alone, so a turn stopped by a locked database (a second gocode instance
// booting into it) simply vanished mid-task with nothing on screen.
func TestRunFailedCarriesTheReason(t *testing.T) {
	state := newTree()
	state.apply(client.Event{Type: "session.next.run.started", Session: "ses_1"})
	state.apply(client.Event{
		Type:    "session.next.run.failed",
		Session: "ses_1",
		Data:    map[string]any{"error": "database is locked (517)"},
	})
	node := state.node("ses_1")
	if node.Failure != "database is locked (517)" {
		t.Fatalf("expected the reason, got %q", node.Failure)
	}
	if node.Failures != 1 {
		t.Fatalf("expected one failure, got %d", node.Failures)
	}
	// The count is what a reader watches, so it has to survive cloning: a
	// snapshot may be dropped, and the next one still has to say a turn died.
	if clone := node.clone(); clone.Failures != 1 || clone.Failure != node.Failure {
		t.Fatal("a snapshot must carry the failure")
	}
	// A failure with no reason still counts — silence is the bug.
	state.apply(client.Event{Type: "session.next.run.failed", Session: "ses_1"})
	if node.Failures != 2 || node.Failure == "" {
		t.Fatalf("expected a second, described failure, got %d %q", node.Failures, node.Failure)
	}
}

// A held turn produces no events of its own, so without the waiting notice
// the interface shows a spinner over nothing for the length of an outage.
func TestWaitingNoticeTracksTheHold(t *testing.T) {
	state := newTree()
	state.apply(client.Event{
		Type:    "session.next.step.waiting",
		Session: "ses_1",
		Data: map[string]any{
			"error":     `Post "https://provider.example/v1/chat": read tcp 10.0.0.2:1->1.2.3.4:443: read: connection reset by peer`,
			"retryInMS": float64(8000),
			"linkDown":  false,
		},
	})
	node := state.node("ses_1")
	if !node.Busy {
		t.Fatal("a held turn is still running")
	}
	if !strings.Contains(node.Waiting, "retrying in 8s") {
		t.Fatalf("the notice must say when the next attempt is: %q", node.Waiting)
	}
	if len(node.Waiting) > 100 {
		t.Fatalf("the notice has one footer line to live in: %q", node.Waiting)
	}

	// With no interface on the machine, the provider's error describes a
	// symptom; the cause is what the user can act on.
	state.apply(client.Event{
		Type:    "session.next.step.waiting",
		Session: "ses_1",
		Data:    map[string]any{"error": "dial tcp: connect: network is unreachable", "retryInMS": float64(2000), "linkDown": true},
	})
	if !strings.Contains(node.Waiting, "no network connection") {
		t.Fatalf("a down link should be named as one: %q", node.Waiting)
	}

	// Waiting on the user's answer instead of a timer.
	state.apply(client.Event{
		Type:    "session.next.step.waiting",
		Session: "ses_1",
		Data:    map[string]any{"error": "boom", "retryInMS": float64(0), "linkDown": false},
	})
	if !strings.Contains(node.Waiting, "waiting for your answer") {
		t.Fatalf("a question-bound hold should say so: %q", node.Waiting)
	}

	// The hold is over the moment the turn moves again.
	state.apply(client.Event{
		Type:    "session.next.step.started",
		Session: "ses_1",
		Data:    map[string]any{"assistantMessageID": "msg_1"},
	})
	if node.Waiting != "" {
		t.Fatalf("a resumed turn is not waiting: %q", node.Waiting)
	}
}

// The retracted attempt's live buffers go with its message row, or the
// half-sentence the connection cut off keeps rendering beside its replacement.
func TestDiscardedStepDropsItsBuffers(t *testing.T) {
	state := newTree()
	state.apply(client.Event{
		Type:    "session.next.reasoning.delta",
		Session: "ses_1",
		Data:    map[string]any{"assistantMessageID": "msg_1", "reasoningID": "msg_1-reasoning", "delta": "thinking..."},
	})
	state.apply(client.Event{
		Type:    "session.next.text.delta",
		Session: "ses_1",
		Data:    map[string]any{"assistantMessageID": "msg_1", "delta": "Good context so far. Let me dig into"},
	})
	node := state.node("ses_1")
	if len(node.Text) != 1 || len(node.Reasoning) != 1 {
		t.Fatalf("expected the partial buffered, got %d text and %d reasoning", len(node.Text), len(node.Reasoning))
	}

	state.apply(client.Event{
		Type:    "session.next.step.discarded",
		Session: "ses_1",
		Data:    map[string]any{"assistantMessageID": "msg_1"},
	})
	if len(node.Text) != 0 || len(node.Reasoning) != 0 {
		t.Fatalf("the retracted step left buffers behind: %v %v", node.Text, node.Reasoning)
	}
}
