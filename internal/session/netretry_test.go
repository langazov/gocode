package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"syscall"
	"testing"
	"time"

	"github.com/langazov/gocode-go/internal/llm"
	"github.com/langazov/gocode-go/internal/question"
	"github.com/langazov/gocode-go/internal/tool"
)

// flakyProvider fails the first outages attempts with a network error, then
// answers normally — a machine whose uplink came back.
type flakyProvider struct {
	outages  int
	attempts int
	err      error
}

func (p *flakyProvider) Stream(ctx context.Context, request llm.Request, emit func(llm.StreamEvent)) error {
	p.attempts++
	if p.attempts <= p.outages {
		err := p.err
		if err == nil {
			err = &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ENETUNREACH}
		}
		emit(llm.StreamEvent{Type: llm.EventProviderError, Error: err})
		return err
	}
	emit(llm.StreamEvent{Type: llm.EventTextDelta, Text: "back online"})
	emit(llm.StreamEvent{Type: llm.EventFinish, Finish: "end_turn"})
	return nil
}

// shortRetries collapses the wait loop's timings so a test exercises the real
// loop rather than a mocked one.
func shortRetries(t *testing.T, budget time.Duration) {
	t.Helper()
	budgetWas, firstWas, maxWas := transportRetryBudget, transportRetryFirstDelay, transportRetryMaxDelay
	transportRetryBudget = budget
	transportRetryFirstDelay = time.Millisecond
	transportRetryMaxDelay = 2 * time.Millisecond
	t.Cleanup(func() {
		transportRetryBudget, transportRetryFirstDelay, transportRetryMaxDelay = budgetWas, firstWas, maxWas
	})
}

// recordingAsker answers the outage question from a script, and remembers what
// it was asked.
type recordingAsker struct {
	replies []string
	asked   []question.Prompt
}

func (a *recordingAsker) Ask(ctx context.Context, input question.AskInput) ([]question.Answer, error) {
	a.asked = append(a.asked, input.Questions...)
	if len(a.replies) == 0 {
		return nil, question.ErrRejected
	}
	reply := a.replies[0]
	a.replies = a.replies[1:]
	return []question.Answer{{reply}}, nil
}

// The point of the whole mechanism: an outage before the model said anything
// is not a failed turn, it is a turn that has not started yet. The user should
// find their answer waiting for them when the network comes back, with no
// error to read and nothing to retype.
func TestTurnWaitsOutAnOutageAndRetries(t *testing.T) {
	shortRetries(t, time.Minute)
	provider := &flakyProvider{outages: 2}
	runner, bus := newRunnerFixture(t, nil, tool.NewRegistry())
	runner.Provider = provider
	admitPrompt(t, bus, runner, "hello")

	events, unsubscribe := bus.Subscribe(64)
	defer unsubscribe()

	if err := runner.Run(context.Background(), RunInput{SessionID: "ses_1"}); err != nil {
		t.Fatalf("the turn should have survived the outage: %v", err)
	}
	if provider.attempts != 3 {
		t.Fatalf("provider attempts = %d, want 3 (two outages, then success)", provider.attempts)
	}

	// One assistant message, holding the answer. A failed attempt that
	// settled would have left its own message behind — the bug this avoids.
	messages, err := NewMessageStore(runner.DB).List(context.Background(), "ses_1")
	if err != nil {
		t.Fatal(err)
	}
	assistants := 0
	for _, message := range messages {
		if message.Type != TypeAssistant {
			continue
		}
		assistants++
		var data map[string]any
		if err := json.Unmarshal(message.Data, &data); err != nil {
			t.Fatal(err)
		}
		if data["error"] != nil {
			t.Fatalf("a waited-out outage must not settle as an error: %v", data["error"])
		}
	}
	if assistants != 1 {
		t.Fatalf("assistant messages = %d, want 1", assistants)
	}

	// The interface has to be told, or the hold looks like a hang.
	waits := 0
	for {
		select {
		case payload := <-events:
			if payload.Type == StepWaiting.Type {
				waits++
				if payload.Data["error"] == nil || payload.Data["retryInMS"] == nil {
					t.Fatalf("waiting event must say why and for how long: %+v", payload.Data)
				}
			}
			continue
		default:
		}
		break
	}
	if waits != 2 {
		t.Fatalf("waiting events = %d, want one per outage (2)", waits)
	}
}

// Budget exhausted, and the user chooses to keep waiting: the turn survives a
// long outage instead of being cut off by a timer the user never set.
func TestExhaustedBudgetAsksAndKeepsWaiting(t *testing.T) {
	// A zero budget puts the question on the first outage.
	shortRetries(t, 0)
	provider := &flakyProvider{outages: 1}
	asker := &recordingAsker{replies: []string{keepWaitingLabel}}
	runner, bus := newRunnerFixture(t, nil, tool.NewRegistry())
	runner.Provider = provider
	runner.Asker = asker
	admitPrompt(t, bus, runner, "hello")

	if err := runner.Run(context.Background(), RunInput{SessionID: "ses_1"}); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(asker.asked) != 1 {
		t.Fatalf("asked %d times, want 1", len(asker.asked))
	}
	prompt := asker.asked[0]
	if len(prompt.Options) != 2 || prompt.Options[0].Label != keepWaitingLabel || prompt.Options[1].Label != stopWaitingLabel {
		t.Fatalf("the question must offer waiting and cancelling: %+v", prompt.Options)
	}
	if provider.attempts != 2 {
		t.Fatalf("provider attempts = %d, want 2 — the answer bought another attempt", provider.attempts)
	}
}

// And when the user declines, the turn settles with the connection error
// itself — visible, not disguised as an interruption.
func TestDecliningToWaitSettlesTheRealError(t *testing.T) {
	shortRetries(t, 0)
	cause := &net.OpError{Op: "dial", Net: "tcp", Err: syscall.EHOSTUNREACH}
	provider := &flakyProvider{outages: 99, err: cause}
	runner, bus := newRunnerFixture(t, nil, tool.NewRegistry())
	runner.Provider = provider
	runner.Asker = &recordingAsker{replies: []string{stopWaitingLabel}}
	admitPrompt(t, bus, runner, "hello")

	err := runner.Run(context.Background(), RunInput{SessionID: "ses_1"})
	if err == nil {
		t.Fatal("expected the run to report the outage it gave up on")
	}

	assistant := lastAssistantData(t, runner)
	recorded, _ := assistant["error"].(map[string]any)
	if recorded == nil {
		t.Fatalf("expected the failure recorded on the assistant message, got %v", assistant)
	}
	if recorded["type"] != ErrorTypeUnknown {
		t.Fatalf("error type = %v, want %q — the run was never interrupted", recorded["type"], ErrorTypeUnknown)
	}
	if message, _ := recorded["message"].(string); message == "" {
		t.Fatal("the settled failure must carry the connection error's own text")
	}
	if assistant["time"] == nil {
		t.Fatal("the turn has to settle, or the interface waits on it forever")
	}
}

// A runner with nobody to ask — headless, or a subagent — must not park
// forever on a question that can never be answered.
func TestOutageWithoutAnAskerGivesUpAtTheBudget(t *testing.T) {
	shortRetries(t, 0)
	provider := &flakyProvider{outages: 99}
	runner, bus := newRunnerFixture(t, nil, tool.NewRegistry())
	runner.Provider = provider
	admitPrompt(t, bus, runner, "hello")

	done := make(chan error, 1)
	go func() { done <- runner.Run(context.Background(), RunInput{SessionID: "ses_1"}) }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected the outage reported")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a runner with no asker parked on the outage instead of giving up")
	}
}

// Escape during a hold ends the turn like any other interrupt, and settles it
// as one.
func TestInterruptEndsTheHold(t *testing.T) {
	shortRetries(t, time.Hour) // never asks; only the interrupt can end this
	transportRetryFirstDelay = 50 * time.Millisecond
	transportRetryMaxDelay = 50 * time.Millisecond
	waiting := make(chan struct{})
	provider := &flakyProvider{outages: 99}
	runner, bus := newRunnerFixture(t, nil, tool.NewRegistry())
	runner.Provider = provider
	admitPrompt(t, bus, runner, "hello")

	events, unsubscribe := bus.Subscribe(64)
	defer unsubscribe()
	go func() {
		for payload := range events {
			if payload.Type == StepWaiting.Type {
				close(waiting)
				return
			}
		}
	}()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runner.Run(ctx, RunInput{SessionID: "ses_1"}) }()

	select {
	case <-waiting:
	case <-time.After(5 * time.Second):
		t.Fatal("the runner never reported a hold")
	}
	cancel()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the hold outlived the interrupt")
	}
	assistant := lastAssistantData(t, runner)
	recorded, _ := assistant["error"].(map[string]any)
	if recorded == nil || recorded["type"] != ErrorTypeAborted {
		t.Fatalf("an interrupted hold settles as an interruption, got %v", assistant["error"])
	}
}

// Only the network is worth waiting out. A provider that answered — with a
// refusal, a rate limit, a bad key — has given its answer, and retrying it for
// five minutes would just delay the bad news.
func TestIsTransportFailureSeparatesLinkFromProvider(t *testing.T) {
	down := []error{
		&net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED},
		&net.DNSError{Err: "no such host", Name: "api.example.com", IsNotFound: true},
		fmt.Errorf("post: %w", syscall.ENETUNREACH),
		fmt.Errorf("reading body: %w", context.DeadlineExceeded),
		errors.New("stream stalled: no data received for 5m0s"),
	}
	for _, err := range down {
		if !isTransportFailure(err) {
			t.Errorf("%v should be waited out", err)
		}
	}

	up := []error{
		nil,
		context.Canceled,
		errors.New("openai: 401 invalid api key"),
		errors.New("anthropic: 429 rate limit exceeded"),
		// The provider's own text is not evidence about the link: a model
		// quoting an error must not park the session for five minutes.
		errors.New(`the log line reads "dial tcp: connection refused"`),
	}
	for _, err := range up {
		if isTransportFailure(err) {
			t.Errorf("%v should settle, not wait", err)
		}
	}
}

// lastAssistantData returns the most recent assistant message's decoded data.
func lastAssistantData(t *testing.T, runner *Runner) map[string]any {
	t.Helper()
	messages, err := NewMessageStore(runner.DB).List(context.Background(), "ses_1")
	if err != nil {
		t.Fatal(err)
	}
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Type != TypeAssistant {
			continue
		}
		var data map[string]any
		if err := json.Unmarshal(messages[i].Data, &data); err != nil {
			t.Fatal(err)
		}
		return data
	}
	t.Fatal("no assistant message was settled")
	return nil
}
