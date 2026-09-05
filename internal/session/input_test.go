package session

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/langazov/gocode-go/internal/db"
	"github.com/langazov/gocode-go/internal/event"
)

func setup(t *testing.T) (*event.Bus, *db.DB) {
	t.Helper()
	database, err := db.OpenAndMigrate(context.Background(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	bus := event.NewBus(database)
	RegisterProjectors(bus)
	seedSession(t, database, "ses_1")
	return bus, database
}

func seedSession(t *testing.T, database *db.DB, sessionID string) {
	t.Helper()
	ctx := context.Background()
	if _, err := database.Exec(ctx, `
		INSERT INTO project (id, worktree, sandboxes, time_created, time_updated)
		VALUES ('prj_1', '/tmp/worktree', '[]', 0, 0)`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(ctx, `
		INSERT INTO session (id, project_id, slug, directory, title, version, time_created, time_updated)
		VALUES (?, 'prj_1', 'test', '/tmp/worktree', 'Test', '1', 0, 0)`, sessionID); err != nil {
		t.Fatal(err)
	}
}

func TestAdmitCreatesInboxRow(t *testing.T) {
	bus, database := setup(t)
	ctx := context.Background()
	admitted, err := Admit(ctx, bus, database, AdmitInput{
		ID:        "msg_1",
		SessionID: "ses_1",
		Prompt:    Prompt{Text: "hello"},
		Delivery:  DeliverySteer,
	})
	if err != nil {
		t.Fatal(err)
	}
	if admitted.AdmittedSeq != 0 {
		t.Fatalf("expected admitted seq 0, got %d", admitted.AdmittedSeq)
	}
	stored, err := Find(ctx, database, "msg_1")
	if err != nil {
		t.Fatal(err)
	}
	if stored == nil {
		t.Fatal("expected inbox row")
	}
	if stored.Prompt.Text != "hello" || stored.Delivery != DeliverySteer || stored.PromotedSeq != nil {
		t.Fatalf("unexpected stored row: %+v", stored)
	}
}

func TestAdmitReconcilesExactRetry(t *testing.T) {
	bus, database := setup(t)
	ctx := context.Background()
	first, err := Admit(ctx, bus, database, AdmitInput{
		ID:        "msg_1",
		SessionID: "ses_1",
		Prompt:    Prompt{Text: "hello"},
		Delivery:  DeliverySteer,
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := Admit(ctx, bus, database, AdmitInput{
		ID:        "msg_1",
		SessionID: "ses_1",
		Prompt:    Prompt{Text: "hello"},
		Delivery:  DeliverySteer,
	})
	if err != nil {
		t.Fatal(err)
	}
	if second.AdmittedSeq != first.AdmittedSeq {
		t.Fatalf("retry should reconcile to seq %d, got %d", first.AdmittedSeq, second.AdmittedSeq)
	}
	seq, err := bus.LatestSequence(ctx, "ses_1")
	if err != nil {
		t.Fatal(err)
	}
	if seq != 0 {
		t.Fatalf("retry must not admit a second event, latest seq %d", seq)
	}
}

func TestHasPendingAndPromoteSteers(t *testing.T) {
	bus, database := setup(t)
	ctx := context.Background()
	for i, messageID := range []string{"msg_1", "msg_2"} {
		admitted, err := Admit(ctx, bus, database, AdmitInput{
			ID:        messageID,
			SessionID: "ses_1",
			Prompt:    Prompt{Text: "p"},
			Delivery:  DeliverySteer,
		})
		if err != nil {
			t.Fatal(err)
		}
		if admitted.AdmittedSeq != i {
			t.Fatalf("expected seq %d, got %d", i, admitted.AdmittedSeq)
		}
	}
	pending, err := HasPending(ctx, database, "ses_1", DeliverySteer)
	if err != nil {
		t.Fatal(err)
	}
	if !pending {
		t.Fatal("expected pending steers")
	}
	count, err := PromoteSteers(ctx, bus, database, "ses_1", 100)
	if err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("expected 2 promoted, got %d", count)
	}
	pending, err = HasPending(ctx, database, "ses_1", DeliverySteer)
	if err != nil {
		t.Fatal(err)
	}
	if pending {
		t.Fatal("expected no pending steers after promotion")
	}
	stored, err := Find(ctx, database, "msg_1")
	if err != nil {
		t.Fatal(err)
	}
	if stored.PromotedSeq == nil {
		t.Fatal("expected promoted seq set")
	}
}

func TestPromoteSteersRespectsCutoff(t *testing.T) {
	bus, database := setup(t)
	ctx := context.Background()
	for _, messageID := range []string{"msg_1", "msg_2", "msg_3"} {
		if _, err := Admit(ctx, bus, database, AdmitInput{
			ID:        messageID,
			SessionID: "ses_1",
			Prompt:    Prompt{Text: "p"},
			Delivery:  DeliverySteer,
		}); err != nil {
			t.Fatal(err)
		}
	}
	count, err := PromoteSteers(ctx, bus, database, "ses_1", 1)
	if err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("expected cutoff to promote 2, got %d", count)
	}
	stored, err := Find(ctx, database, "msg_3")
	if err != nil {
		t.Fatal(err)
	}
	if stored.PromotedSeq != nil {
		t.Fatal("steer beyond cutoff must stay pending")
	}
}

func TestPromoteNextQueuedOneAtATime(t *testing.T) {
	bus, database := setup(t)
	ctx := context.Background()
	for _, messageID := range []string{"msg_1", "msg_2"} {
		if _, err := Admit(ctx, bus, database, AdmitInput{
			ID:        messageID,
			SessionID: "ses_1",
			Prompt:    Prompt{Text: "p"},
			Delivery:  DeliveryQueue,
		}); err != nil {
			t.Fatal(err)
		}
	}
	promoted, err := PromoteNextQueued(ctx, bus, database, "ses_1")
	if err != nil {
		t.Fatal(err)
	}
	if !promoted {
		t.Fatal("expected one queued input promoted")
	}
	first, err := Find(ctx, database, "msg_1")
	if err != nil {
		t.Fatal(err)
	}
	second, err := Find(ctx, database, "msg_2")
	if err != nil {
		t.Fatal(err)
	}
	if first.PromotedSeq == nil || second.PromotedSeq != nil {
		t.Fatalf("only the oldest queued input should promote: %+v / %+v", first.PromotedSeq, second.PromotedSeq)
	}
	promoted, err = PromoteNextQueued(ctx, bus, database, "ses_1")
	if err != nil {
		t.Fatal(err)
	}
	if !promoted {
		t.Fatal("expected second queued input promoted")
	}
	promoted, err = PromoteNextQueued(ctx, bus, database, "ses_1")
	if err != nil {
		t.Fatal(err)
	}
	if promoted {
		t.Fatal("expected no more queued inputs")
	}
}

func TestHistoricalPromptLazySynthesis(t *testing.T) {
	bus, database := setup(t)
	ctx := context.Background()
	promptData, err := promptToData(Prompt{Text: "historical"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = bus.Publish(ctx, Prompted, map[string]any{
		"timestamp": int64(1234567890),
		"sessionID": "ses_1",
		"messageID": "msg_hist",
		"prompt":    promptData,
		"delivery":  "steer",
	}, event.PublishOptions{})
	if err != nil {
		t.Fatal(err)
	}
	stored, err := Find(ctx, database, "msg_hist")
	if err != nil {
		t.Fatal(err)
	}
	if stored == nil {
		t.Fatal("expected synthesized inbox row")
	}
	if stored.PromotedSeq == nil || *stored.PromotedSeq != stored.AdmittedSeq {
		t.Fatalf("synthesized row should be fully promoted: %+v", stored)
	}
}

func TestEquivalent(t *testing.T) {
	admitted := Admitted{
		SessionID: "ses_1",
		Prompt:    Prompt{Text: "hello"},
		Delivery:  DeliverySteer,
	}
	if !Equivalent(admitted, ExpectedInput{SessionID: "ses_1", Prompt: Prompt{Text: "hello"}, Delivery: DeliverySteer}) {
		t.Fatal("expected equivalence")
	}
	if Equivalent(admitted, ExpectedInput{SessionID: "ses_1", Prompt: Prompt{Text: "other"}, Delivery: DeliverySteer}) {
		t.Fatal("different prompts must not be equivalent")
	}
	if Equivalent(admitted, ExpectedInput{SessionID: "ses_1", Prompt: Prompt{Text: "hello"}, Delivery: DeliveryQueue}) {
		t.Fatal("different delivery must not be equivalent")
	}
	if Equivalent(admitted, ExpectedInput{SessionID: "ses_2", Prompt: Prompt{Text: "hello"}, Delivery: DeliverySteer}) {
		t.Fatal("different session must not be equivalent")
	}
}

// A prompt sent while a turn is running waits in the inbox with no message of
// its own — that is what keeps it out of the history the model is given — so
// Pending is the only way a client can see it and show it as queued. It
// leaves the moment it is promoted.
func TestPendingListsUnpromotedPrompts(t *testing.T) {
	bus, database := setup(t)
	ctx := context.Background()

	for _, text := range []string{"first", "second"} {
		if _, err := Admit(ctx, bus, database, AdmitInput{
			ID:        "msg_" + text,
			SessionID: "ses_1",
			Prompt:    Prompt{Text: text},
			Delivery:  DeliverySteer,
		}); err != nil {
			t.Fatal(err)
		}
	}

	pending, err := Pending(ctx, database, "ses_1")
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 2 {
		t.Fatalf("expected both prompts waiting, got %d", len(pending))
	}
	if pending[0].Prompt.Text != "first" || pending[1].Prompt.Text != "second" {
		t.Fatalf("expected admission order, got %q then %q", pending[0].Prompt.Text, pending[1].Prompt.Text)
	}

	// Neither is visible as a message yet.
	store := NewMessageStore(database)
	messages, err := store.List(ctx, "ses_1")
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 0 {
		t.Fatalf("an unpromoted prompt must not be in the timeline, got %d messages", len(messages))
	}

	// Promoting the first moves it out of the queue and into the timeline,
	// under the same ID — which is what lets a renderer tell the two apart.
	if _, err := PromoteSteers(ctx, bus, database, "ses_1", 0); err != nil {
		t.Fatal(err)
	}
	pending, err = Pending(ctx, database, "ses_1")
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0].Prompt.Text != "second" {
		t.Fatalf("expected only the unpromoted prompt to remain, got %+v", pending)
	}
	messages, err = store.List(ctx, "ses_1")
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 || messages[0].ID != "msg_first" {
		t.Fatalf("expected the promoted prompt in the timeline under its own ID, got %+v", messages)
	}
}
