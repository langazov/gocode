package session

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/anomalyco/opencode-go/internal/db"
	"github.com/anomalyco/opencode-go/internal/event"
)

// Admitted mirrors SessionInput.Admitted: a durable prompt inbox row.
type Admitted struct {
	AdmittedSeq int
	ID          string
	SessionID   string
	Prompt      Prompt
	Delivery    Delivery
	TimeCreated int64
	PromotedSeq *int
}

var (
	PromptAdmitted = event.Definition{
		Type:    "session.next.prompt.admitted",
		Durable: &event.DurableDef{Aggregate: "sessionID", Version: 1},
	}
	Prompted = event.Definition{
		Type:    "session.next.prompted",
		Durable: &event.DurableDef{Aggregate: "sessionID", Version: 1},
	}
)

var ErrLifecycleConflict = errors.New("session: input lifecycle conflict")

func promptToData(prompt Prompt) (map[string]any, error) {
	raw, err := json.Marshal(prompt)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func promptFromData(data map[string]any) (Prompt, error) {
	raw, err := json.Marshal(data)
	if err != nil {
		return Prompt{}, err
	}
	var prompt Prompt
	if err := json.Unmarshal(raw, &prompt); err != nil {
		return Prompt{}, err
	}
	return prompt, nil
}

func admittedFromRow(id, sessionID string, promptJSON string, delivery string, admittedSeq int, promotedSeq sql.NullInt64, timeCreated int64) (Admitted, error) {
	var prompt Prompt
	if err := json.Unmarshal([]byte(promptJSON), &prompt); err != nil {
		return Admitted{}, err
	}
	admitted := Admitted{
		AdmittedSeq: admittedSeq,
		ID:          id,
		SessionID:   sessionID,
		Prompt:      prompt,
		Delivery:    Delivery(delivery),
		TimeCreated: timeCreated,
	}
	if promotedSeq.Valid {
		value := int(promotedSeq.Int64)
		admitted.PromotedSeq = &value
	}
	return admitted, nil
}

// Find returns the stored inbox row for a message ID, or nil.
func Find(ctx context.Context, database *db.DB, messageID string) (*Admitted, error) {
	row := database.QueryRow(ctx, `
		SELECT id, session_id, prompt, delivery, admitted_seq, promoted_seq, time_created
		FROM session_input WHERE id = ?`, messageID)
	var (
		id, sessionID, promptJSON, delivery string
		admittedSeq                         int
		promotedSeq                         sql.NullInt64
		timeCreated                         int64
	)
	if err := row.Scan(&id, &sessionID, &promptJSON, &delivery, &admittedSeq, &promotedSeq, &timeCreated); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	admitted, err := admittedFromRow(id, sessionID, promptJSON, delivery, admittedSeq, promotedSeq, timeCreated)
	if err != nil {
		return nil, err
	}
	return &admitted, nil
}

// Admit durably admits a prompt. Reusing an existing message ID reconciles to
// the already-admitted row rather than double-admitting.
func Admit(ctx context.Context, bus *event.Bus, database *db.DB, input AdmitInput) (*Admitted, error) {
	existing, err := Find(ctx, database, input.ID)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		return existing, nil
	}
	now := time.Now().UnixMilli()
	promptData, err := promptToData(input.Prompt)
	if err != nil {
		return nil, err
	}
	payload, err := bus.Publish(ctx, PromptAdmitted, map[string]any{
		"timestamp": now,
		"sessionID": input.SessionID,
		"messageID": input.ID,
		"prompt":    promptData,
		"delivery":  string(input.Delivery),
	}, event.PublishOptions{})
	if err != nil {
		if stored, findErr := Find(ctx, database, input.ID); findErr == nil && stored != nil {
			return stored, nil
		}
		return nil, err
	}
	if payload.Durable == nil {
		return nil, fmt.Errorf("session: prompt admission event missing aggregate sequence")
	}
	return &Admitted{
		AdmittedSeq: payload.Durable.Seq,
		ID:          input.ID,
		SessionID:   input.SessionID,
		Prompt:      input.Prompt,
		Delivery:    input.Delivery,
		TimeCreated: now,
	}, nil
}

type AdmitInput struct {
	ID        string
	SessionID string
	Prompt    Prompt
	Delivery  Delivery
}

// HasPending reports whether an unpromoted inbox row exists for a delivery.
func HasPending(ctx context.Context, database *db.DB, sessionID string, delivery Delivery) (bool, error) {
	row := database.QueryRow(ctx, `
		SELECT id FROM session_input
		WHERE session_id = ? AND promoted_seq IS NULL AND delivery = ? LIMIT 1`, sessionID, string(delivery))
	var id string
	if err := row.Scan(&id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// Equivalent reports whether an admitted row matches an expected prompt for
// exact-retry reconciliation.
func Equivalent(admitted Admitted, expected ExpectedInput) bool {
	return admitted.Delivery == expected.Delivery && matchesPrompt(admitted, expected)
}

type ExpectedInput struct {
	SessionID string
	Prompt    Prompt
	Delivery  Delivery
}

func matchesPrompt(admitted Admitted, expected ExpectedInput) bool {
	if admitted.SessionID != expected.SessionID {
		return false
	}
	a, err := json.Marshal(admitted.Prompt)
	if err != nil {
		return false
	}
	b, err := json.Marshal(expected.Prompt)
	if err != nil {
		return false
	}
	return string(a) == string(b)
}

func matchesProjection(admitted Admitted, expected ProjectedInput) bool {
	return Equivalent(admitted, ExpectedInput{
		SessionID: expected.SessionID,
		Prompt:    expected.Prompt,
		Delivery:  expected.Delivery,
	}) && admitted.TimeCreated == expected.TimeCreated
}

type ProjectedInput struct {
	SessionID   string
	Prompt      Prompt
	Delivery    Delivery
	TimeCreated int64
	PromotedSeq int
}

// ProjectAdmitted inserts the inbox row when a PromptAdmitted event commits.
// It runs inside the durable commit transaction.
func ProjectAdmitted(ctx context.Context, tx *sql.Tx, payload event.Payload) error {
	input, err := decodeInputEvent(payload)
	if err != nil {
		return err
	}
	var messageID string
	err = tx.QueryRowContext(ctx, `SELECT id FROM session_message WHERE id = ?`, input.messageID).Scan(&messageID)
	if err == nil {
		return fmt.Errorf("%w: message %s already projected", ErrLifecycleConflict, input.messageID)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	promptJSON, err := json.Marshal(input.prompt)
	if err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `
		INSERT OR IGNORE INTO session_input
			(id, session_id, admitted_seq, prompt, delivery, time_created)
		VALUES (?, ?, ?, ?, ?, ?)`,
		input.messageID, input.sessionID, input.admittedSeq, string(promptJSON), input.delivery, input.timeCreated)
	if err != nil {
		return err
	}
	if affected, err := result.RowsAffected(); err == nil && affected == 0 {
		return fmt.Errorf("%w: input %s already admitted", ErrLifecycleConflict, input.messageID)
	}
	return nil
}

// ProjectPrompted marks an inbox row promoted when a Prompted event commits,
// lazily synthesizing historical rows during exact retry.
func ProjectPrompted(ctx context.Context, tx *sql.Tx, payload event.Payload) error {
	input, err := decodeInputEvent(payload)
	if err != nil {
		return err
	}
	promotedSeq := input.promotedSeq
	result, err := tx.ExecContext(ctx, `
		UPDATE session_input SET promoted_seq = ?
		WHERE id = ? AND session_id = ? AND promoted_seq IS NULL`,
		promotedSeq, input.messageID, input.sessionID)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected > 0 {
		admitted, err := scanAdmitted(ctx, tx, input.messageID)
		if err != nil {
			return err
		}
		if !matchesProjection(*admitted, ProjectedInput{
			SessionID:   input.sessionID,
			Prompt:      input.prompt,
			Delivery:    input.delivery,
			TimeCreated: input.timeCreated,
			PromotedSeq: promotedSeq,
		}) {
			return fmt.Errorf("%w: input %s projection mismatch", ErrLifecycleConflict, input.messageID)
		}
		return nil
	}

	admitted, err := scanAdmitted(ctx, tx, input.messageID)
	if err == nil && admitted != nil {
		if !matchesProjection(*admitted, ProjectedInput{
			SessionID:   input.sessionID,
			Prompt:      input.prompt,
			Delivery:    input.delivery,
			TimeCreated: input.timeCreated,
			PromotedSeq: promotedSeq,
		}) || admitted.PromotedSeq == nil || *admitted.PromotedSeq != promotedSeq {
			return fmt.Errorf("%w: input %s projection mismatch", ErrLifecycleConflict, input.messageID)
		}
		return nil
	}

	promptJSON, err := json.Marshal(input.prompt)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO session_input
			(id, session_id, prompt, delivery, admitted_seq, promoted_seq, time_created)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		input.messageID, input.sessionID, string(promptJSON), input.delivery,
		promotedSeq, promotedSeq, input.timeCreated)
	return err
}

func scanAdmitted(ctx context.Context, tx *sql.Tx, messageID string) (*Admitted, error) {
	row := tx.QueryRowContext(ctx, `
		SELECT id, session_id, prompt, delivery, admitted_seq, promoted_seq, time_created
		FROM session_input WHERE id = ?`, messageID)
	var (
		id, sessionID, promptJSON, delivery string
		admittedSeq                         int
		promotedSeq                         sql.NullInt64
		timeCreated                         int64
	)
	if err := row.Scan(&id, &sessionID, &promptJSON, &delivery, &admittedSeq, &promotedSeq, &timeCreated); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	admitted, err := admittedFromRow(id, sessionID, promptJSON, delivery, admittedSeq, promotedSeq, timeCreated)
	if err != nil {
		return nil, err
	}
	return &admitted, nil
}

type inputEvent struct {
	sessionID   string
	messageID   string
	prompt      Prompt
	delivery    Delivery
	timeCreated int64
	admittedSeq int
	promotedSeq int
}

func decodeInputEvent(payload event.Payload) (inputEvent, error) {
	data := payload.Data
	sessionID, _ := data["sessionID"].(string)
	messageID, _ := data["messageID"].(string)
	delivery, _ := data["delivery"].(string)
	promptData, _ := data["prompt"].(map[string]any)
	prompt, err := promptFromData(promptData)
	if err != nil {
		return inputEvent{}, err
	}
	out := inputEvent{
		sessionID: sessionID,
		messageID: messageID,
		prompt:    prompt,
		delivery:  Delivery(delivery),
	}
	out.timeCreated = asInt64(data["timestamp"])
	if payload.Durable != nil {
		out.admittedSeq = payload.Durable.Seq
		out.promotedSeq = payload.Durable.Seq
	}
	return out, nil
}

func asInt64(value any) int64 {
	switch v := value.(type) {
	case int64:
		return v
	case int:
		return int64(v)
	case float64:
		return int64(v)
	}
	return 0
}
