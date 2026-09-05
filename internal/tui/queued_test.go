package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/langazov/gocode-go/internal/tui/client"
)

// A prompt sent while the turn is still running has no message of its own in
// the timeline (see session.Pending), so the interface renders it from the
// server's queue, below everything the assistant has produced, badged the way
// UserMessage badges a queued message upstream.
func TestQueuedPromptRendersBelowTheTimeline(t *testing.T) {
	app := cacheApp(t)
	app.busy = true
	app.queued = []client.QueuedPrompt{
		{ID: "msg_q1", Text: "and then deploy it", Delivery: "steer", TimeCreated: 5},
	}

	lines, _, _ := app.buildTimeline()
	plain := ansi.Strip(strings.Join(lines, "\n"))
	if !strings.Contains(plain, "and then deploy it") {
		t.Fatal("the queued prompt should be visible")
	}
	if !strings.Contains(plain, "QUEUED") {
		t.Fatal("a queued prompt should carry the QUEUED badge")
	}
	if strings.Index(plain, "second") > strings.Index(plain, "and then deploy it") {
		t.Fatal("the queued prompt belongs below the existing messages")
	}
}

// Promotion reuses the inbox row's ID for the message it projects. The queue
// and the timeline are refetched by separate commands, so for a frame the
// promoted prompt is in both — it must render once, as a real message.
func TestPromotedPromptIsNotAlsoShownAsQueued(t *testing.T) {
	app := cacheApp(t)
	app.busy = true
	app.queued = []client.QueuedPrompt{{ID: "msg_q1", Text: "and then deploy it", TimeCreated: 5}}
	app.timeline = append(app.timeline, client.Message{
		ID: "msg_q1", Type: "user", TimeCreated: 5,
		Data: []byte(`{"text":"and then deploy it","time":{"created":5}}`),
	})

	plain := ansi.Strip(strings.Join(firstOf(app.buildTimeline()), "\n"))
	if strings.Count(plain, "and then deploy it") != 1 {
		t.Fatalf("expected the prompt to render exactly once, got:\n%s", plain)
	}
	if strings.Contains(plain, "QUEUED") {
		t.Fatal("a promoted prompt is no longer queued")
	}
}

// Nothing waiting means nothing extra rendered.
func TestNoQueuedPromptsAddNothing(t *testing.T) {
	app := cacheApp(t)
	plain := ansi.Strip(strings.Join(firstOf(app.buildTimeline()), "\n"))
	if strings.Contains(plain, "QUEUED") {
		t.Fatal("an idle session should show no queue")
	}
}

func firstOf(lines []string, _ map[int]string, _ map[int]string) []string { return lines }

// A prompt entering or leaving the inbox is only ever a signal on the stream —
// the waiting prompts themselves are served over HTTP — so the snapshot has to
// ask for the refetch, exactly once per change.
func TestQueueEventTriggersRefetch(t *testing.T) {
	_, server := newMockAPI(t)
	app := newTestApp(t, server.URL)
	openSession(t, app)

	state := newTree()
	if !state.apply(client.Event{
		Type:    "session.next.prompt.admitted",
		Session: "ses_1",
		Data:    map[string]any{"messageID": "msg_q1"},
	}) {
		t.Fatal("an admitted prompt must register as a change")
	}
	if !app.applySnapshot(state.snapshot(0)).queue {
		t.Fatal("the snapshot should ask for a queue refetch")
	}
	if again := app.applySnapshot(state.snapshot(0)); again.queue {
		t.Fatal("an unchanged queue count should not refetch again")
	}

	// Promotion changes it too: the prompt leaves the queue for the timeline.
	state.apply(client.Event{
		Type:    "session.next.prompted",
		Session: "ses_1",
		Data:    map[string]any{"messageID": "msg_q1"},
	})
	effect := app.applySnapshot(state.snapshot(0))
	if !effect.queue {
		t.Fatal("promotion should refetch the queue")
	}
	if !effect.timeline {
		t.Fatal("promotion should refetch the timeline too")
	}
}
