package session

import (
	"context"
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
