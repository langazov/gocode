package session

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
)

type fakeRunner struct {
	calls atomic.Int32
	force atomic.Bool
	last  atomic.Value
}

func (r *fakeRunner) Run(ctx context.Context, input RunInput) error {
	r.calls.Add(1)
	r.force.Store(input.Force)
	r.last.Store(input.SessionID)
	return nil
}

func TestExecutionResumeRunsSession(t *testing.T) {
	bus, database := setup(t)
	_ = bus
	runner := &fakeRunner{}
	execution := NewExecution(&DBSessionLookup{DB: database}, runner)
	if err := execution.Resume(context.Background(), "ses_1"); err != nil {
		t.Fatal(err)
	}
	if runner.calls.Load() != 1 {
		t.Fatalf("expected one run, got %d", runner.calls.Load())
	}
	if !runner.force.Load() {
		t.Fatal("resume must force a provider attempt")
	}
}

func TestExecutionResumeMissingSession(t *testing.T) {
	_, database := setup(t)
	execution := NewExecution(&DBSessionLookup{DB: database}, &fakeRunner{})
	err := execution.Resume(context.Background(), "ses_missing")
	if err == nil {
		t.Fatal("expected error for missing session")
	}
}

func TestExecutionWakeIsAdvisory(t *testing.T) {
	_, database := setup(t)
	runner := &fakeRunner{}
	execution := NewExecution(&DBSessionLookup{DB: database}, runner)
	execution.Wake(context.Background(), "ses_1")
	waitFor(t, func() bool { return runner.calls.Load() == 1 }, "wake should drain")
	if runner.force.Load() {
		t.Fatal("advisory wake must not force")
	}
	waitFor(t, func() bool { return len(execution.Active()) == 0 }, "wake drain should settle")
}

func TestExecutionInterruptIdleNoop(t *testing.T) {
	_, database := setup(t)
	execution := NewExecution(&DBSessionLookup{DB: database}, &fakeRunner{})
	execution.Interrupt("ses_idle")
}

func TestNoopExecution(t *testing.T) {
	execution := NoopExecution()
	if err := execution.Resume(context.Background(), "anything"); err != nil {
		t.Fatal(err)
	}
	execution.Wake(context.Background(), "anything")
	execution.Interrupt("anything")
}

func TestMessageStoreAppendAndList(t *testing.T) {
	_, database := setup(t)
	store := NewMessageStore(database)
	ctx := context.Background()

	if err := store.Append(ctx, "ses_1", 0, "msg_u1", TypeUser, map[string]any{"time": map[string]any{"created": 1}}); err != nil {
		t.Fatal(err)
	}
	assistant := AssistantMessage{
		Agent: "build",
		Model: ModelRef{ProviderID: "anthropic", ID: "claude-sonnet-4-5"},
		Content: []AssistantContent{{
			Type: "text",
			ID:   "prt_1",
			Text: "done",
		}},
		Time: AssistantTime{Created: 2},
	}
	if err := store.Append(ctx, "ses_1", 1, "msg_a1", TypeAssistant, assistant); err != nil {
		t.Fatal(err)
	}

	messages, err := store.List(ctx, "ses_1")
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(messages))
	}
	if messages[0].Seq != 0 || messages[1].Seq != 1 {
		t.Fatalf("expected sequence order, got %d,%d", messages[0].Seq, messages[1].Seq)
	}
	decoded, err := DecodeAssistant(messages[1].Data)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Agent != "build" || len(decoded.Content) != 1 || decoded.Content[0].Text != "done" {
		t.Fatalf("unexpected decoded assistant: %+v", decoded)
	}

	next, err := store.NextSeq(ctx, "ses_1")
	if err != nil {
		t.Fatal(err)
	}
	if next != 2 {
		t.Fatalf("expected next seq 2, got %d", next)
	}

	got, err := store.Get(ctx, "msg_a1")
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.Type != TypeAssistant {
		t.Fatalf("expected assistant message, got %+v", got)
	}
	missing, err := store.Get(ctx, "msg_missing")
	if err != nil {
		t.Fatal(err)
	}
	if missing != nil {
		t.Fatalf("expected nil for missing message, got %+v", missing)
	}
}

// busyError is a stand-in for the driver error a second gocode process
// produces when it holds the write lock: db.Retryable matches on the result
// code, and the concrete driver type cannot be constructed from a test.
type busyError struct{ code int }

func (e *busyError) Error() string { return "database is locked" }
func (e *busyError) Code() int     { return e.code }

// flakyRunner fails the first failures runs with err, then succeeds.
type flakyRunner struct {
	failures int32
	err      error
	calls    atomic.Int32
}

func (r *flakyRunner) Run(ctx context.Context, input RunInput) error {
	if r.calls.Add(1) <= r.failures {
		return r.err
	}
	return nil
}

// A turn must survive another process taking the database lock. This is the
// reported failure: starting a second gocode instance stopped the first one's
// task, because the drain returned the lock error and the session went idle
// mid-turn with nothing on screen to say why.
func TestExecutionRetriesDrainOnLockedDatabase(t *testing.T) {
	_, database := setup(t)
	runner := &flakyRunner{failures: 2, err: &busyError{code: 517}} // SQLITE_BUSY_SNAPSHOT
	execution := NewExecution(&DBSessionLookup{DB: database}, runner)
	var reported atomic.Int32
	execution.ErrorLogger = func(string, error) { reported.Add(1) }

	if err := execution.Resume(context.Background(), "ses_1"); err != nil {
		t.Fatalf("drain should have survived a transient lock: %v", err)
	}
	if runner.calls.Load() != 3 {
		t.Fatalf("expected two retries then a success, got %d runs", runner.calls.Load())
	}
	if reported.Load() != 0 {
		t.Fatal("a drain that recovered must not be reported as a failure")
	}
}

// The retry is bounded, and a drain that stays locked still reports — the
// point is to survive the transient case, not to hide a persistent one.
func TestExecutionReportsPersistentLock(t *testing.T) {
	_, database := setup(t)
	runner := &flakyRunner{failures: 1 << 30, err: &busyError{code: 5}} // SQLITE_BUSY
	execution := NewExecution(&DBSessionLookup{DB: database}, runner)
	var reported atomic.Int32
	execution.ErrorLogger = func(string, error) { reported.Add(1) }

	if err := execution.Resume(context.Background(), "ses_1"); err == nil {
		t.Fatal("expected the persistent lock to surface")
	}
	if got := runner.calls.Load(); got != drainRetries+1 {
		t.Fatalf("expected %d attempts, got %d", drainRetries+1, got)
	}
	if reported.Load() != 1 {
		t.Fatalf("expected one failure report, got %d", reported.Load())
	}
}

// An error that is not contention is not retried: a provider failure or a
// programming error must fail the turn immediately, as it always has.
func TestExecutionDoesNotRetryOrdinaryErrors(t *testing.T) {
	_, database := setup(t)
	runner := &flakyRunner{failures: 1, err: errors.New("provider exploded")}
	execution := NewExecution(&DBSessionLookup{DB: database}, runner)

	if err := execution.Resume(context.Background(), "ses_1"); err == nil {
		t.Fatal("expected the provider error to surface")
	}
	if runner.calls.Load() != 1 {
		t.Fatalf("expected exactly one attempt, got %d", runner.calls.Load())
	}
}
