package event

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/anomalyco/opencode-go/internal/db"
)

var testDef = Definition{
	Type:    "test.event",
	Durable: &DurableDef{Aggregate: "entityID", Version: 1},
}

func openTestDB(t *testing.T) *db.DB {
	t.Helper()
	database, err := db.OpenAndMigrate(context.Background(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	return database
}

func TestPublishAssignsSequences(t *testing.T) {
	database := openTestDB(t)
	bus := NewBus(database)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		payload, err := bus.Publish(ctx, testDef, map[string]any{"entityID": "ent_1", "n": i}, PublishOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if payload.Durable == nil {
			t.Fatal("expected durable info")
		}
		if payload.Durable.Seq != i {
			t.Fatalf("expected seq %d, got %d", i, payload.Durable.Seq)
		}
		if payload.Durable.AggregateID != "ent_1" {
			t.Fatalf("unexpected aggregate: %s", payload.Durable.AggregateID)
		}
	}

	seq, err := bus.LatestSequence(ctx, "ent_1")
	if err != nil {
		t.Fatal(err)
	}
	if seq != 2 {
		t.Fatalf("expected latest seq 2, got %d", seq)
	}
	seq, err = bus.LatestSequence(ctx, "ent_missing")
	if err != nil {
		t.Fatal(err)
	}
	if seq != -1 {
		t.Fatalf("expected -1 for unknown aggregate, got %d", seq)
	}
}

func TestAggregatesAreIndependent(t *testing.T) {
	database := openTestDB(t)
	bus := NewBus(database)
	ctx := context.Background()
	if _, err := bus.Publish(ctx, testDef, map[string]any{"entityID": "a"}, PublishOptions{}); err != nil {
		t.Fatal(err)
	}
	payload, err := bus.Publish(ctx, testDef, map[string]any{"entityID": "b"}, PublishOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if payload.Durable.Seq != 0 {
		t.Fatalf("expected independent sequence, got %d", payload.Durable.Seq)
	}
}

func TestProjectorRunsInCommit(t *testing.T) {
	database := openTestDB(t)
	bus := NewBus(database)
	ctx := context.Background()
	var projectedSeqs []int
	bus.Project(testDef, func(ctx context.Context, tx *sql.Tx, p Payload) error {
		projectedSeqs = append(projectedSeqs, p.Durable.Seq)
		_, err := tx.ExecContext(ctx,
			`INSERT INTO data_migration (name, time_completed) VALUES (?, ?)`,
			fmt.Sprintf("projection-%d", p.Durable.Seq), 0)
		return err
	})
	if _, err := bus.Publish(ctx, testDef, map[string]any{"entityID": "ent_1"}, PublishOptions{}); err != nil {
		t.Fatal(err)
	}
	if len(projectedSeqs) != 1 || projectedSeqs[0] != 0 {
		t.Fatalf("expected projector to run with seq 0, got %v", projectedSeqs)
	}
	var count int
	if err := database.QueryRow(ctx, `SELECT COUNT(*) FROM data_migration`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("expected projector write committed, got %d rows", count)
	}
}

func TestProjectorFailureRollsBackEvent(t *testing.T) {
	database := openTestDB(t)
	bus := NewBus(database)
	ctx := context.Background()
	bus.Project(testDef, func(ctx context.Context, tx *sql.Tx, p Payload) error {
		return errors.New("projection failed")
	})
	if _, err := bus.Publish(ctx, testDef, map[string]any{"entityID": "ent_1"}, PublishOptions{}); err == nil {
		t.Fatal("expected publish to fail when projector fails")
	}
	seq, err := bus.LatestSequence(ctx, "ent_1")
	if err != nil {
		t.Fatal(err)
	}
	if seq != -1 {
		t.Fatalf("failed projector must not commit the event, got seq %d", seq)
	}
}

func TestReplayIdempotent(t *testing.T) {
	database := openTestDB(t)
	bus := NewBus(database)
	ctx := context.Background()
	payload, err := bus.Publish(ctx, testDef, map[string]any{"entityID": "ent_1", "v": "x"}, PublishOptions{})
	if err != nil {
		t.Fatal(err)
	}
	serialized := SerializedEvent{
		ID:          payload.ID,
		Type:        testDef.Type,
		Seq:         0,
		AggregateID: "ent_1",
		Data:        map[string]any{"entityID": "ent_1", "v": "x"},
	}
	if err := bus.Replay(ctx, serialized, testDef, nil); err != nil {
		t.Fatalf("identical replay should be idempotent: %v", err)
	}
	events, _, err := bus.ReadAggregate(ctx, ReadInput{AggregateID: "ent_1", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("replay must not duplicate events, got %d", len(events))
	}
}

func TestReplayDivergence(t *testing.T) {
	database := openTestDB(t)
	bus := NewBus(database)
	ctx := context.Background()
	payload, err := bus.Publish(ctx, testDef, map[string]any{"entityID": "ent_1", "v": "x"}, PublishOptions{})
	if err != nil {
		t.Fatal(err)
	}
	serialized := SerializedEvent{
		ID:          payload.ID,
		Type:        testDef.Type,
		Seq:         0,
		AggregateID: "ent_1",
		Data:        map[string]any{"entityID": "ent_1", "v": "DIFFERENT"},
	}
	if err := bus.Replay(ctx, serialized, testDef, nil); err == nil {
		t.Fatal("expected divergence error")
	}
}

func TestReplayContinuesSequence(t *testing.T) {
	database := openTestDB(t)
	bus := NewBus(database)
	ctx := context.Background()
	serialized := SerializedEvent{
		ID:          "evt_replayed",
		Type:        testDef.Type,
		Seq:         0,
		AggregateID: "ent_1",
		Data:        map[string]any{"entityID": "ent_1"},
	}
	if err := bus.Replay(ctx, serialized, testDef, nil); err != nil {
		t.Fatal(err)
	}
	payload, err := bus.Publish(ctx, testDef, map[string]any{"entityID": "ent_1"}, PublishOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if payload.Durable.Seq != 1 {
		t.Fatalf("expected publish to continue at seq 1, got %d", payload.Durable.Seq)
	}
}

func TestReadAggregatePagination(t *testing.T) {
	database := openTestDB(t)
	bus := NewBus(database)
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		if _, err := bus.Publish(ctx, testDef, map[string]any{"entityID": "ent_1"}, PublishOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	events, hasMore, err := bus.ReadAggregate(ctx, ReadInput{AggregateID: "ent_1", Limit: 3})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 || !hasMore {
		t.Fatalf("expected 3 events with more, got %d (hasMore=%v)", len(events), hasMore)
	}
	after := 2
	rest, hasMore, err := bus.ReadAggregate(ctx, ReadInput{AggregateID: "ent_1", After: &after, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(rest) != 2 || hasMore {
		t.Fatalf("expected 2 remaining events, got %d (hasMore=%v)", len(rest), hasMore)
	}
	if rest[0].Seq != 3 {
		t.Fatalf("expected continuation at seq 3, got %d", rest[0].Seq)
	}
}

func TestVersionedTypeStored(t *testing.T) {
	database := openTestDB(t)
	bus := NewBus(database)
	ctx := context.Background()
	if _, err := bus.Publish(ctx, testDef, map[string]any{"entityID": "ent_1"}, PublishOptions{}); err != nil {
		t.Fatal(err)
	}
	events, _, err := bus.ReadAggregate(ctx, ReadInput{AggregateID: "ent_1", Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if events[0].Type != "test.event.1" {
		t.Fatalf("expected versioned type test.event.1, got %s", events[0].Type)
	}
}

func TestRemoveAggregate(t *testing.T) {
	database := openTestDB(t)
	bus := NewBus(database)
	ctx := context.Background()
	if _, err := bus.Publish(ctx, testDef, map[string]any{"entityID": "ent_1"}, PublishOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := bus.Remove(ctx, "ent_1"); err != nil {
		t.Fatal(err)
	}
	seq, err := bus.LatestSequence(ctx, "ent_1")
	if err != nil {
		t.Fatal(err)
	}
	if seq != -1 {
		t.Fatalf("expected removed aggregate, got seq %d", seq)
	}
}

func TestDurableAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "reopen.db")
	ctx := context.Background()

	database, err := db.OpenAndMigrate(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	bus := NewBus(database)
	if _, err := bus.Publish(ctx, testDef, map[string]any{"entityID": "ent_1"}, PublishOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := db.OpenAndMigrate(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	bus = NewBus(reopened)
	seq, err := bus.LatestSequence(ctx, "ent_1")
	if err != nil {
		t.Fatal(err)
	}
	if seq != 0 {
		t.Fatalf("expected persisted seq 0 after reopen, got %d", seq)
	}
	payload, err := bus.Publish(ctx, testDef, map[string]any{"entityID": "ent_1"}, PublishOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if payload.Durable.Seq != 1 {
		t.Fatalf("expected sequence to continue at 1 after reopen, got %d", payload.Durable.Seq)
	}
}

func TestListenerNotified(t *testing.T) {
	database := openTestDB(t)
	bus := NewBus(database)
	ctx := context.Background()
	var got []Payload
	bus.Listen(func(payload Payload) { got = append(got, payload) })
	if _, err := bus.Publish(ctx, testDef, map[string]any{"entityID": "ent_1"}, PublishOptions{}); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Durable == nil {
		t.Fatalf("expected one notified durable payload, got %+v", got)
	}
}

func TestSubscribeReceivesEvents(t *testing.T) {
	database := openTestDB(t)
	bus := NewBus(database)
	ctx := context.Background()
	events, unsubscribe := bus.Subscribe(16)
	defer unsubscribe()
	if _, err := bus.Publish(ctx, testDef, map[string]any{"entityID": "ent_1"}, PublishOptions{}); err != nil {
		t.Fatal(err)
	}
	select {
	case payload := <-events:
		if payload.Durable == nil || payload.Durable.Seq != 0 {
			t.Fatalf("unexpected subscribed payload: %+v", payload)
		}
	case <-time.After(time.Second):
		t.Fatal("subscriber did not receive the event")
	}
}

func TestSubscribeUnsubscribe(t *testing.T) {
	database := openTestDB(t)
	bus := NewBus(database)
	ctx := context.Background()
	events, unsubscribe := bus.Subscribe(16)
	unsubscribe()
	if _, err := bus.Publish(ctx, testDef, map[string]any{"entityID": "ent_1"}, PublishOptions{}); err != nil {
		t.Fatal(err)
	}
	select {
	case payload := <-events:
		t.Fatalf("unsubscribed channel should be silent, got %+v", payload)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestSubscribeDropsOnOverflow(t *testing.T) {
	database := openTestDB(t)
	bus := NewBus(database)
	ctx := context.Background()
	events, unsubscribe := bus.Subscribe(2)
	defer unsubscribe()
	for i := 0; i < 5; i++ {
		if _, err := bus.Publish(ctx, testDef, map[string]any{"entityID": "ent_1"}, PublishOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	received := 0
	for {
		select {
		case <-events:
			received++
		case <-time.After(100 * time.Millisecond):
			if received < 2 {
				t.Fatalf("expected at least the buffered events, got %d", received)
			}
			return
		}
	}
}
