// Package event ports the durable event store core of packages/core/src/event.ts:
// aggregate sequence allocation, atomic commit of projections with events,
// replay with divergence detection, and in-process notification.
package event

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"

	"github.com/langazov/gocode-go/internal/db"
	"github.com/langazov/gocode-go/internal/id"
)

type DurableDef struct {
	Aggregate string
	Version   int
}

type Definition struct {
	Type    string
	Durable *DurableDef
}

func VersionedType(def Definition) string {
	if def.Durable == nil {
		return def.Type
	}
	return fmt.Sprintf("%s.%d", def.Type, def.Durable.Version)
}

type DurableInfo struct {
	AggregateID string
	Seq         int
	Version     int
}

type Payload struct {
	ID      string
	Type    string
	Data    map[string]any
	Durable *DurableInfo
}

type SerializedEvent struct {
	ID          string
	Type        string
	Seq         int
	AggregateID string
	Data        map[string]any
}

var ErrInvalidDurable = errors.New("event: invalid durable event")

// Projector writes a projection atomically within the durable commit
// transaction. It mirrors the TypeScript project() subscribers.
type Projector func(ctx context.Context, tx *sql.Tx, event Payload) error

type PublishOptions struct {
	ID string
	// Commit is a local operational projection committed atomically with the
	// durable event. Requires a durable definition.
	Commit Projector
}

type subscriber struct {
	ch     chan Payload
	active bool
}

type Bus struct {
	db *db.DB

	mu          sync.Mutex
	projectors  map[string][]Projector
	listeners   []func(Payload)
	subscribers []*subscriber
}

func NewBus(database *db.DB) *Bus {
	return &Bus{
		db:         database,
		projectors: map[string][]Projector{},
	}
}

// Project registers a projector for a definition; it runs inside the durable
// commit transaction.
func (b *Bus) Project(def Definition, projector Projector) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.projectors[def.Type] = append(b.projectors[def.Type], projector)
}

// Listen registers an in-memory listener invoked after a successful commit.
func (b *Bus) Listen(listener func(Payload)) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.listeners = append(b.listeners, listener)
}

// Subscribe returns a buffered channel of committed events plus an
// unsubscribe function. Delivery is best-effort: events are dropped when the
// subscriber's buffer is full, matching the bounded-subscription behavior.
func (b *Bus) Subscribe(buffer int) (<-chan Payload, func()) {
	if buffer <= 0 {
		buffer = 256
	}
	sub := &subscriber{ch: make(chan Payload, buffer), active: true}
	b.mu.Lock()
	b.subscribers = append(b.subscribers, sub)
	b.mu.Unlock()
	unsubscribe := func() {
		b.mu.Lock()
		sub.active = false
		b.mu.Unlock()
	}
	return sub.ch, unsubscribe
}

// LatestSequence returns the latest durable sequence for an aggregate, or -1.
func (b *Bus) LatestSequence(ctx context.Context, aggregateID string) (int, error) {
	var seq int
	err := b.db.QueryRow(ctx,
		`SELECT seq FROM event_sequence WHERE aggregate_id = ?`, aggregateID).Scan(&seq)
	if errors.Is(err, sql.ErrNoRows) {
		return -1, nil
	}
	if err != nil {
		return 0, err
	}
	return seq, nil
}

// Publish appends a durable event, allocating the next aggregate sequence and
// running registered projectors plus the commit hook inside one transaction.
func (b *Bus) Publish(ctx context.Context, def Definition, data map[string]any, opts PublishOptions) (Payload, error) {
	eventID := opts.ID
	if eventID == "" {
		generated, err := id.Ascending(id.KindEvent)
		if err != nil {
			return Payload{}, err
		}
		eventID = generated
	}
	payload := Payload{ID: eventID, Type: def.Type, Data: data}

	if def.Durable != nil {
		committed, err := b.commitDurable(ctx, def, payload, nil, opts.Commit)
		if err != nil {
			return Payload{}, err
		}
		payload.Durable = committed
	} else if err := b.runLiveProjectors(ctx, def, payload); err != nil {
		return Payload{}, err
	}
	b.notify(payload)
	return payload, nil
}

// runLiveProjectors executes projectors registered for a non-durable (live)
// event in their own transaction. Durable events run their projectors inside
// the commit transaction instead.
func (b *Bus) runLiveProjectors(ctx context.Context, def Definition, payload Payload) error {
	b.mu.Lock()
	list := append([]Projector{}, b.projectors[def.Type]...)
	b.mu.Unlock()
	if len(list) == 0 {
		return nil
	}
	return b.db.Transaction(ctx, func(tx *sql.Tx) error {
		for _, projector := range list {
			if err := projector(ctx, tx, payload); err != nil {
				return err
			}
		}
		return nil
	})
}

// Replay commits a serialized event at an explicit sequence, matching the
// TypeScript replay semantics: identical replays are idempotent, divergent
// replays fail.
func (b *Bus) Replay(ctx context.Context, event SerializedEvent, def Definition, opts *ReplayOptions) error {
	payload := Payload{ID: event.ID, Type: def.Type, Data: event.Data}
	input := replayInput{seq: event.Seq, aggregateID: event.AggregateID}
	if opts != nil {
		input.ownerID = opts.OwnerID
		input.strictOwner = opts.StrictOwner
	}
	_, err := b.commitDurable(ctx, def, payload, &input, nil)
	return err
}

type ReplayOptions struct {
	OwnerID     string
	StrictOwner bool
}

type replayInput struct {
	seq         int
	aggregateID string
	ownerID     string
	strictOwner bool
}

func (b *Bus) commitDurable(
	ctx context.Context,
	def Definition,
	payload Payload,
	input *replayInput,
	commit Projector,
) (*DurableInfo, error) {
	durable := def.Durable
	rawAggregate, ok := payload.Data[durable.Aggregate].(string)
	if !ok {
		return nil, fmt.Errorf("%w: expected string aggregate field %s for %s", ErrInvalidDurable, durable.Aggregate, def.Type)
	}
	if input != nil && input.aggregateID != rawAggregate {
		return nil, fmt.Errorf("%w: aggregate mismatch for %s: expected %s, got %s", ErrInvalidDurable, def.Type, input.aggregateID, rawAggregate)
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	var committed *DurableInfo
	err := b.db.Transaction(ctx, func(tx *sql.Tx) error {
		var latest = -1
		var ownerID sql.NullString
		row := tx.QueryRowContext(ctx,
			`SELECT seq, owner_id FROM event_sequence WHERE aggregate_id = ?`, rawAggregate)
		if err := row.Scan(&latest, &ownerID); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}

		if input != nil && input.strictOwner && ownerID.Valid && ownerID.String != input.ownerID {
			return fmt.Errorf("%w: replay owner mismatch for aggregate %s", ErrInvalidDurable, rawAggregate)
		}

		seq := latest + 1
		if input != nil {
			if input.seq <= latest {
				return b.verifyReplay(ctx, tx, def, payload, rawAggregate, input, ownerID)
			}
			if ownerID.Valid && ownerID.String != input.ownerID {
				return nil
			}
			if input.seq != latest+1 {
				return fmt.Errorf("%w: sequence mismatch for aggregate %s: expected %d, got %d", ErrInvalidDurable, rawAggregate, latest+1, input.seq)
			}
			seq = input.seq
		}

		var existingSeq int
		err := tx.QueryRowContext(ctx, `SELECT seq FROM event WHERE id = ?`, payload.ID).Scan(&existingSeq)
		if err == nil {
			return fmt.Errorf("%w: event %s already exists", ErrInvalidDurable, payload.ID)
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}

		committedPayload := payload
		committedPayload.Durable = &DurableInfo{AggregateID: rawAggregate, Seq: seq, Version: durable.Version}
		for _, projector := range b.projectors[def.Type] {
			if err := projector(ctx, tx, committedPayload); err != nil {
				return err
			}
		}
		if commit != nil {
			if err := commit(ctx, tx, committedPayload); err != nil {
				return err
			}
		}

		if input != nil && input.ownerID != "" && !ownerID.Valid {
			if _, err := tx.ExecContext(ctx,
				`UPDATE event_sequence SET owner_id = ? WHERE aggregate_id = ?`, input.ownerID, rawAggregate); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO event_sequence (aggregate_id, seq, owner_id) VALUES (?, ?, ?)
			ON CONFLICT(aggregate_id) DO UPDATE SET seq = excluded.seq`,
			rawAggregate, seq, nullableOwner(input)); err != nil {
			return err
		}
		data, err := encodeJSON(payload.Data)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO event (id, aggregate_id, seq, type, data) VALUES (?, ?, ?, ?, ?)`,
			payload.ID, rawAggregate, seq, VersionedType(def), data); err != nil {
			return err
		}
		committed = committedPayload.Durable
		return nil
	})
	if err != nil {
		return nil, err
	}
	return committed, nil
}

func nullableOwner(input *replayInput) any {
	if input != nil && input.ownerID != "" {
		return input.ownerID
	}
	return nil
}

// verifyReplay handles replaying a sequence that already exists: identical
// events are accepted idempotently, anything else diverges.
func (b *Bus) verifyReplay(
	ctx context.Context,
	tx *sql.Tx,
	def Definition,
	payload Payload,
	aggregateID string,
	input *replayInput,
	ownerID sql.NullString,
) error {
	var storedID, storedType, storedData string
	var storedSeq int
	err := tx.QueryRowContext(ctx,
		`SELECT id, type, seq, data FROM event WHERE aggregate_id = ? AND seq = ?`,
		aggregateID, input.seq).Scan(&storedID, &storedType, &storedSeq, &storedData)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: replay diverged at aggregate %s sequence %d", ErrInvalidDurable, aggregateID, input.seq)
	}
	if err != nil {
		return err
	}
	encoded, err := encodeJSON(payload.Data)
	if err != nil {
		return err
	}
	if storedID == payload.ID && storedType == VersionedType(def) && storedData == string(encoded) {
		if input.ownerID != "" && !ownerID.Valid {
			if _, err := tx.ExecContext(ctx,
				`UPDATE event_sequence SET owner_id = ? WHERE aggregate_id = ?`, input.ownerID, aggregateID); err != nil {
				return err
			}
		}
		return nil
	}
	return fmt.Errorf("%w: replay diverged at aggregate %s sequence %d", ErrInvalidDurable, aggregateID, input.seq)
}

// ReadAggregate returns stored events for an aggregate after a sequence,
// filtered to known types, with pagination.
func (b *Bus) ReadAggregate(ctx context.Context, input ReadInput) ([]SerializedEvent, bool, error) {
	after := -1
	if input.After != nil {
		after = *input.After
	}
	query := `SELECT id, aggregate_id, seq, type, data FROM event
		WHERE aggregate_id = ? AND seq > ?`
	args := []any{input.AggregateID, after}
	if len(input.Types) > 0 {
		query += ` AND type IN (` + placeholders(len(input.Types)) + `)`
		for _, t := range input.Types {
			args = append(args, t)
		}
	}
	query += ` ORDER BY seq ASC LIMIT ?`
	args = append(args, input.Limit+1)

	rows, err := b.db.Query(ctx, query, args...)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	var events []SerializedEvent
	for rows.Next() {
		var (
			eventID     string
			aggregateID string
			seq         int
			eventType   string
			data        string
		)
		if err := rows.Scan(&eventID, &aggregateID, &seq, &eventType, &data); err != nil {
			return nil, false, err
		}
		decoded, err := decodeJSON(data)
		if err != nil {
			return nil, false, err
		}
		events = append(events, SerializedEvent{
			ID:          eventID,
			Type:        eventType,
			Seq:         seq,
			AggregateID: aggregateID,
			Data:        decoded,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	hasMore := len(events) > input.Limit
	if hasMore {
		events = events[:input.Limit]
	}
	return events, hasMore, nil
}

type ReadInput struct {
	AggregateID string
	After       *int
	Limit       int
	Types       []string
}

// Remove deletes an aggregate's events and sequence, matching remove().
func (b *Bus) Remove(ctx context.Context, aggregateID string) error {
	return b.db.Transaction(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM event WHERE aggregate_id = ?`, aggregateID); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `DELETE FROM event_sequence WHERE aggregate_id = ?`, aggregateID)
		return err
	})
}

// Claim sets the replay owner for an aggregate.
func (b *Bus) Claim(ctx context.Context, aggregateID, ownerID string) error {
	_, err := b.db.Exec(ctx,
		`UPDATE event_sequence SET owner_id = ? WHERE aggregate_id = ?`, ownerID, aggregateID)
	return err
}

func (b *Bus) notify(payload Payload) {
	b.mu.Lock()
	listeners := make([]func(Payload), len(b.listeners))
	copy(listeners, b.listeners)
	subscribers := make([]*subscriber, 0, len(b.subscribers))
	for _, sub := range b.subscribers {
		if sub.active {
			subscribers = append(subscribers, sub)
		}
	}
	b.mu.Unlock()
	for _, listener := range listeners {
		listener(payload)
	}
	for _, sub := range subscribers {
		select {
		case sub.ch <- payload:
		default:
		}
	}
}

func placeholders(n int) string {
	out := "?"
	for i := 1; i < n; i++ {
		out += ",?"
	}
	return out
}
