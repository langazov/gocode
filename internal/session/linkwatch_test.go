package session

import (
	"context"
	"os"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/langazov/gocode-go/internal/llm"
	"github.com/langazov/gocode-go/internal/tool"
)

// forwardLinkChanges is the half of the watcher every platform shares, so a
// pipe stands in for the kernel socket.
func TestForwardLinkChangesWakesAndCoalesces(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	changes := forwardLinkChanges(ctx, reader)

	if _, err := writer.Write([]byte("a route changed")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-changes:
	case <-time.After(2 * time.Second):
		t.Fatal("a message on the socket did not reach the channel")
	}

	// A Wi-Fi association emits a burst; the reader's response to all of them
	// is one re-check, so the burst must not block the forwarder.
	for i := 0; i < 100; i++ {
		if _, err := writer.Write([]byte("burst")); err != nil {
			t.Fatal(err)
		}
	}
	select {
	case <-changes:
	case <-time.After(2 * time.Second):
		t.Fatal("the burst produced no wake-up")
	}
}

// Cancelling the context has to close the socket, which is what unblocks the
// goroutine parked in Read — otherwise every held turn leaks one.
func TestForwardLinkChangesStopsWithItsContext(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	ctx, cancel := context.WithCancel(context.Background())
	forwardLinkChanges(ctx, reader)

	before := runtime.NumGoroutine()
	cancel()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if runtime.NumGoroutine() < before {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	// Not fatal on its own — the scheduler may simply be slow — but the read
	// must at least have been interrupted, which the closed pipe proves.
	if _, err := reader.Read(make([]byte, 1)); err == nil {
		t.Fatal("the socket outlived its context")
	}
}

// The kernel subscription itself, where one exists. Nothing is asserted about
// its traffic: a test cannot unplug the machine's network.
func TestWatchLinkChangesOpensOnSupportedPlatforms(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	changes := watchLinkChanges(ctx)
	switch runtime.GOOS {
	case "linux", "darwin", "freebsd", "netbsd", "openbsd", "dragonfly":
		if changes == nil {
			t.Skip("the routing socket could not be opened here (sandbox?); the wait falls back to polling")
		}
	default:
		if changes != nil {
			t.Fatalf("%s has no watcher, want the polling fallback", runtime.GOOS)
		}
	}
}

// The integration: a notification ends the wait immediately, without waiting
// for the poll, let alone the backoff.
func TestWaitEndsOnALinkNotification(t *testing.T) {
	var up atomic.Bool
	stubLink(t, up.Load)
	// Long enough that neither the poll nor the timer can be what returns.
	pollWas := linkPollIntervalVar
	linkPollIntervalVar = time.Hour
	t.Cleanup(func() { linkPollIntervalVar = pollWas })

	notify := make(chan struct{}, 1)
	watchWas := watchLink
	watchLink = func(context.Context) <-chan struct{} { return notify }
	t.Cleanup(func() { watchLink = watchWas })

	go func() {
		time.Sleep(20 * time.Millisecond)
		// A change arrives while the link is still down: the wait re-checks,
		// finds nothing, and keeps waiting.
		notify <- struct{}{}
		time.Sleep(20 * time.Millisecond)
		up.Store(true)
		notify <- struct{}{}
	}()

	start := time.Now()
	if err := waitBeforeRetry(context.Background(), time.Hour); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("waited %s for a notification that arrived in 40ms", elapsed)
	}
}

// stallingProvider is a stream whose link disappears underneath it: it emits
// part of an answer and then never writes again, which is exactly what a
// socket with an unreachable peer does — no error, no EOF, just silence.
type stallingProvider struct {
	attempts int
	stalls   int
	stalled  chan struct{}
}

func (p *stallingProvider) Stream(ctx context.Context, request llm.Request, emit func(llm.StreamEvent)) error {
	p.attempts++
	if p.attempts <= p.stalls {
		emit(llm.StreamEvent{Type: llm.EventReasoningDelta, Text: "thinking about it"})
		select {
		case p.stalled <- struct{}{}:
		default:
		}
		<-ctx.Done() // only the link watcher (or an interrupt) ends this
		return ctx.Err()
	}
	emit(llm.StreamEvent{Type: llm.EventTextDelta, Text: "back on the air"})
	emit(llm.StreamEvent{Type: llm.EventFinish, Finish: "end_turn"})
	return nil
}

// The down edge, end to end: the interface disappears mid-stream, the runner
// cuts the dead read instead of waiting minutes for a timeout, holds the turn,
// and finishes it when the link returns. The failure this prevents is a turn
// that looks frozen mid-sentence for as long as the idle timeout lasts.
func TestLinkLossCutsTheStreamAndTheTurnSurvives(t *testing.T) {
	shortRetries(t, time.Minute)
	var up atomic.Bool
	up.Store(true)
	stubLink(t, up.Load)
	pollWas := linkPollIntervalVar
	linkPollIntervalVar = 5 * time.Millisecond
	t.Cleanup(func() { linkPollIntervalVar = pollWas })
	// No kernel notifications in the test: the poll is the whole watcher.
	watchWas := watchLink
	watchLink = func(context.Context) <-chan struct{} { return nil }
	t.Cleanup(func() { watchLink = watchWas })

	provider := &stallingProvider{stalls: 1, stalled: make(chan struct{}, 1)}
	runner, bus := newRunnerFixture(t, nil, tool.NewRegistry())
	runner.Provider = provider
	admitPrompt(t, bus, runner, "hello")

	go func() {
		<-provider.stalled
		up.Store(false)                   // the machine loses its network
		time.Sleep(60 * time.Millisecond) // long enough to be held
		up.Store(true)                    // ...and gets it back
	}()

	done := make(chan error, 1)
	go func() { done <- runner.Run(context.Background(), RunInput{SessionID: "ses_1"}) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("the turn should have survived the link loss: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the runner sat on the dead stream instead of cutting it")
	}
	if provider.attempts != 2 {
		t.Fatalf("provider attempts = %d, want 2", provider.attempts)
	}

	assistant := lastAssistantData(t, runner)
	if assistant["error"] != nil {
		t.Fatalf("a waited-out link loss must not settle as an error: %v", assistant["error"])
	}
	// The stalled attempt was retracted, so its half-formed thinking is gone.
	messages, err := NewMessageStore(runner.DB).List(context.Background(), "ses_1")
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range messages {
		if message.Type == TypeAssistant && strings.Contains(string(message.Data), "thinking about it") {
			t.Fatalf("the cut attempt survived into the timeline: %s", message.Data)
		}
	}
}

// An interrupt during a stream still reads as an interrupt, not as a link
// loss: both cancel the same stream context, and only the cause tells them
// apart.
func TestInterruptDuringAStreamIsNotMistakenForLinkLoss(t *testing.T) {
	stubLink(t, func() bool { return true })
	provider := &stallingProvider{stalls: 99, stalled: make(chan struct{}, 1)}
	runner, bus := newRunnerFixture(t, nil, tool.NewRegistry())
	runner.Provider = provider
	admitPrompt(t, bus, runner, "hello")

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runner.Run(ctx, RunInput{SessionID: "ses_1"}) }()
	select {
	case <-provider.stalled:
	case <-time.After(5 * time.Second):
		t.Fatal("the stream never started")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the run outlived its interrupt")
	}
	assistant := lastAssistantData(t, runner)
	recorded, _ := assistant["error"].(map[string]any)
	if recorded == nil || recorded["type"] != ErrorTypeAborted {
		t.Fatalf("an interrupted stream settles as an interruption, got %v", assistant["error"])
	}
	if provider.attempts != 1 {
		t.Fatalf("provider attempts = %d — an interrupt must not be retried", provider.attempts)
	}
}
