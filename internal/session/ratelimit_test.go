package session

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/langazov/gocode-go/internal/llm"
	"github.com/langazov/gocode-go/internal/tool"
)

// rateLimitProvider answers the first limits attempts with a 429 that states
// its retry time, then answers normally.
type rateLimitProvider struct {
	limits   int
	attempts int
	hint     time.Duration
}

func (p *rateLimitProvider) Stream(ctx context.Context, request llm.Request, emit func(llm.StreamEvent)) error {
	p.attempts++
	if p.attempts <= p.limits {
		err := &llm.RateLimitError{
			Provider:        "anthropic",
			Message:         "Number of requests too high",
			RetryAfter:      p.hint,
			RetryAfterKnown: true,
		}
		emit(llm.StreamEvent{Type: llm.EventProviderError, Error: err})
		return err
	}
	emit(llm.StreamEvent{Type: llm.EventTextDelta, Text: "window reopened"})
	emit(llm.StreamEvent{Type: llm.EventFinish, Finish: "end_turn"})
	return nil
}

// A 429 that states its retry time is waited out exactly — no backoff
// doubling against it, no jitter — and then the step is re-issued unchanged
// and succeeds, leaving one clean assistant message rather than a settled
// failure the user has to notice and resend.
func Test429WithHintIsWaitedOutAndRetried(t *testing.T) {
	provider := &rateLimitProvider{limits: 1, hint: 25 * time.Millisecond}
	runner, bus := newRunnerFixture(t, nil, tool.NewRegistry())
	runner.Provider = provider
	admitPrompt(t, bus, runner, "hello")

	start := time.Now()
	if err := runner.Run(context.Background(), RunInput{SessionID: "ses_1"}); err != nil {
		t.Fatalf("the turn should have survived the rate limit: %v", err)
	}
	if elapsed := time.Since(start); elapsed < 25*time.Millisecond {
		t.Fatalf("returned after %s — the stated hint was not waited out", elapsed)
	}
	if provider.attempts != 2 {
		t.Fatalf("provider attempts = %d, want 2 (one limited, one retry)", provider.attempts)
	}

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
			t.Fatalf("a waited-out rate limit must not settle as an error: %v", data["error"])
		}
	}
	if assistants != 1 {
		t.Fatalf("assistant messages = %d, want 1", assistants)
	}
}

// The interface has to see the hold, or it reads as a hang: one waiting event
// per limited attempt, carrying both the reason and the stated delay.
func Test429PublishesWaitingEvents(t *testing.T) {
	// 40ms: small enough to keep the test quick, large enough to survive the
	// event's millisecond resolution (5ms would round to a confusing 0).
	provider := &rateLimitProvider{limits: 2, hint: 40 * time.Millisecond}
	runner, bus := newRunnerFixture(t, nil, tool.NewRegistry())
	runner.Provider = provider
	admitPrompt(t, bus, runner, "hello")

	events, unsubscribe := bus.Subscribe(64)
	defer unsubscribe()

	if err := runner.Run(context.Background(), RunInput{SessionID: "ses_1"}); err != nil {
		t.Fatalf("run: %v", err)
	}
	waits := 0
	for {
		select {
		case payload := <-events:
			if payload.Type == StepWaiting.Type {
				waits++
				if payload.Data["error"] == nil || payload.Data["retryInMS"] == nil {
					t.Fatalf("waiting event must say why and for how long: %+v", payload.Data)
				}
				// The bus hands the map through as built, so the value is the
				// int64 Milliseconds() produced — not a JSON-decoded float64.
				if retryMS, _ := payload.Data["retryInMS"].(int64); retryMS != 40 {
					t.Fatalf("retryInMS = %v, want the provider's own 40ms", payload.Data["retryInMS"])
				}
			}
			continue
		default:
		}
		break
	}
	if waits != 2 {
		t.Fatalf("waiting events = %d, want one per limited attempt (2)", waits)
	}
}

// A 429 without a stated time is the settled failure it always was: there is
// no "then" to wait for, so inventing one would only delay the bad news.
func Test429WithoutHintSettles(t *testing.T) {
	provider := &hintlessProvider{}
	runner, bus := newRunnerFixture(t, nil, tool.NewRegistry())
	runner.Provider = provider
	admitPrompt(t, bus, runner, "hello")

	start := time.Now()
	if err := runner.Run(context.Background(), RunInput{SessionID: "ses_1"}); err == nil {
		t.Fatal("expected the 429 to surface")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("settled after %s — a hint-less 429 must not be waited on", elapsed)
	}
	if provider.attempts != 1 {
		t.Fatalf("provider attempts = %d, want 1", provider.attempts)
	}
	assistant := lastAssistantData(t, runner)
	if assistant["error"] == nil {
		t.Fatalf("the step must settle with the 429, got %v", assistant)
	}
}

// hintlessProvider always answers 429 without stating a retry time.
type hintlessProvider struct{ attempts int }

func (p *hintlessProvider) Stream(ctx context.Context, request llm.Request, emit func(llm.StreamEvent)) error {
	p.attempts++
	err := &llm.RateLimitError{Provider: "openai", Message: "Too many requests"}
	emit(llm.StreamEvent{Type: llm.EventProviderError, Error: err})
	return err
}

// The counterweight to the no-dispatch rule everywhere else in the retry
// path: a step that already ran a tool may have changed the world, so a 429
// after that settles as a failure and is never re-run — waiting would be
// queuing a replay of side effects.
func Test429AfterAToolCallIsNotRetried(t *testing.T) {
	provider := &toolThen429Provider{}
	runner, bus := newRunnerFixture(t, nil, tool.NewRegistry())
	registry := tool.NewRegistry()
	registry.Register(&fakeTool{name: "echo", output: "echoed: hi"})
	runner.Tools = registry
	runner.Provider = provider
	admitPrompt(t, bus, runner, "run it")

	if err := runner.Run(context.Background(), RunInput{SessionID: "ses_1"}); err == nil {
		t.Fatal("expected the 429 reported rather than replayed")
	}
	if provider.attempts != 1 {
		t.Fatalf("provider attempts = %d, want 1 — a dispatched tool must not be re-run", provider.attempts)
	}
	assistant := lastAssistantData(t, runner)
	if assistant["error"] == nil {
		t.Fatalf("the step must settle with its error, got %v", assistant)
	}
}

// toolThen429Provider dispatches a tool and then answers 429 with a hint —
// the combination that must not retry.
type toolThen429Provider struct{ attempts int }

func (p *toolThen429Provider) Stream(ctx context.Context, request llm.Request, emit func(llm.StreamEvent)) error {
	p.attempts++
	emit(llm.StreamEvent{Type: llm.EventToolCall, ToolCall: &llm.ToolCall{
		ID: "call_1", Name: "echo", Input: map[string]any{"text": "hi"},
	}})
	err := &llm.RateLimitError{
		Provider:        "anthropic",
		Message:         "Number of requests too high",
		RetryAfter:      10 * time.Millisecond,
		RetryAfterKnown: true,
	}
	emit(llm.StreamEvent{Type: llm.EventProviderError, Error: err})
	return err
}

// asRateLimited gates on the hint: known, positive, and within the cap. Each
// boundary is the difference between a held turn and a settled one.
func TestAsRateLimited(t *testing.T) {
	cases := []struct {
		name string
		err  *llm.RateLimitError
		want bool
	}{
		{"known hint", &llm.RateLimitError{RetryAfter: time.Second, RetryAfterKnown: true}, true},
		{"unknown hint", &llm.RateLimitError{RetryAfter: time.Second}, false},
		{"zero wait", &llm.RateLimitError{RetryAfter: 0, RetryAfterKnown: true}, false},
		{"past the cap", &llm.RateLimitError{RetryAfter: rateLimitRetryCap + time.Second, RetryAfterKnown: true}, false},
		{"exactly the cap", &llm.RateLimitError{RetryAfter: rateLimitRetryCap, RetryAfterKnown: true}, true},
	}
	for _, tc := range cases {
		if _, ok := asRateLimited(tc.err); ok != tc.want {
			t.Errorf("%s: asRateLimited = %v, want %v", tc.name, ok, tc.want)
		}
	}
	if _, ok := asRateLimited(nil); ok {
		t.Error("nil must not read as rate limited")
	}
	if _, ok := asRateLimited(context.Canceled); ok {
		t.Error("an unrelated error must not read as rate limited")
	}
}

// A hint that outlasts the budget asks before waiting — the length of the
// next wait is already known, so the user decides up front. Declining
// settles the turn with the provider's own 429 rather than a disguised one.
func TestOversizedHintAsksBeforeWaiting(t *testing.T) {
	shortRetries(t, time.Minute)
	// 2 minutes sits inside the cap the gate allows but outruns the
	// one-minute budget the helper sets — the question path is what is under
	// test, not the cap's outright refusal.
	provider := &rateLimitProvider{limits: 99, hint: 2 * time.Minute}
	runner, bus := newRunnerFixture(t, nil, tool.NewRegistry())
	runner.Provider = provider
	asker := &recordingAsker{replies: []string{stopWaitingLabel}}
	runner.Asker = asker
	admitPrompt(t, bus, runner, "hello")

	start := time.Now()
	if err := runner.Run(context.Background(), RunInput{SessionID: "ses_1"}); err == nil {
		t.Fatal("expected the 429 to surface once waiting was declined")
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("asked and settled after %s — the wait should never have started", elapsed)
	}
	if len(asker.asked) != 1 {
		t.Fatalf("asked %d times, want 1", len(asker.asked))
	}
	if provider.attempts != 1 {
		t.Fatalf("provider attempts = %d, want 1 — declining must not buy a retry", provider.attempts)
	}
	assistant := lastAssistantData(t, runner)
	recorded, _ := assistant["error"].(map[string]any)
	if recorded == nil || recorded["type"] != ErrorTypeUnknown {
		t.Fatalf("the settled failure must carry the provider's 429: %v", assistant["error"])
	}
}
