package session

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/anomalyco/opencode-go/internal/db"
	"github.com/anomalyco/opencode-go/internal/event"
)

// RegisterProjectors wires the inbox projections to their durable events,
// mirroring the TypeScript projector registration.
func RegisterProjectors(bus *event.Bus) {
	bus.Project(PromptAdmitted, ProjectAdmitted)
	bus.Project(Prompted, ProjectPrompted)
	bus.Project(Prompted, ProjectPromptedMessage)
}

// ProjectPromptedMessage appends the visible user message when a Prompted
// event commits, matching the session.next.prompted message-updater case.
func ProjectPromptedMessage(ctx context.Context, tx *sql.Tx, payload event.Payload) error {
	data := payload.Data
	messageID, _ := data["messageID"].(string)
	sessionID, _ := data["sessionID"].(string)
	prompt, _ := data["prompt"].(map[string]any)
	created := asInt64(data["timestamp"])

	messageData := map[string]any{
		"text": prompt["text"],
		"time": map[string]any{"created": created},
	}
	if files, ok := prompt["files"]; ok {
		messageData["files"] = files
	}
	if agents, ok := prompt["agents"]; ok {
		messageData["agents"] = agents
	}
	encoded, err := json.Marshal(messageData)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO session_message (id, session_id, type, seq, data, time_created, time_updated)
		VALUES (?, ?, 'user', ?, ?, ?, ?)`,
		messageID, sessionID, payload.Durable.Seq, string(encoded), created, created)
	return err
}

// PromoteSteers publishes Prompted for every pending steer admitted at or
// before the cutoff, returning how many were promoted.
func PromoteSteers(ctx context.Context, bus *event.Bus, database *db.DB, sessionID string, cutoff int) (int, error) {
	rows, err := pendingInputs(ctx, database, sessionID, DeliverySteer, &cutoff)
	if err != nil {
		return 0, err
	}
	return publishPromoted(ctx, bus, database, sessionID, rows)
}

// PromoteNextQueued promotes the single oldest pending queued input.
func PromoteNextQueued(ctx context.Context, bus *event.Bus, database *db.DB, sessionID string) (bool, error) {
	rows, err := pendingInputs(ctx, database, sessionID, DeliveryQueue, nil)
	if err != nil {
		return false, err
	}
	if len(rows) == 0 {
		return false, nil
	}
	if _, err := publishPromoted(ctx, bus, database, sessionID, rows[:1]); err != nil {
		return false, err
	}
	return true, nil
}

func pendingInputs(ctx context.Context, database *db.DB, sessionID string, delivery Delivery, cutoff *int) ([]Admitted, error) {
	query := `
		SELECT id, session_id, prompt, delivery, admitted_seq, promoted_seq, time_created
		FROM session_input
		WHERE session_id = ? AND promoted_seq IS NULL AND delivery = ?`
	args := []any{sessionID, string(delivery)}
	if cutoff != nil {
		query += ` AND admitted_seq <= ?`
		args = append(args, *cutoff)
	}
	query += ` ORDER BY admitted_seq ASC`
	if cutoff == nil {
		query += ` LIMIT 1`
	}
	rows, err := database.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []Admitted
	for rows.Next() {
		var (
			id, sid, promptJSON, del string
			admittedSeq              int
			promotedSeq              sql.NullInt64
			timeCreated              int64
		)
		if err := rows.Scan(&id, &sid, &promptJSON, &del, &admittedSeq, &promotedSeq, &timeCreated); err != nil {
			return nil, err
		}
		admitted, err := admittedFromRow(id, sid, promptJSON, del, admittedSeq, promotedSeq, timeCreated)
		if err != nil {
			return nil, err
		}
		result = append(result, admitted)
	}
	return result, rows.Err()
}

func publishPromoted(ctx context.Context, bus *event.Bus, database *db.DB, sessionID string, rows []Admitted) (int, error) {
	for _, row := range rows {
		promptData, err := promptToData(row.Prompt)
		if err != nil {
			return 0, err
		}
		_, err = bus.Publish(ctx, Prompted, map[string]any{
			"timestamp": row.TimeCreated,
			"sessionID": sessionID,
			"messageID": row.ID,
			"prompt":    promptData,
			"delivery":  string(row.Delivery),
		}, event.PublishOptions{})
		if err != nil {
			if errors.Is(err, ErrLifecycleConflict) {
				stored, findErr := Find(ctx, database, row.ID)
				if findErr == nil && stored != nil && stored.PromotedSeq != nil {
					continue
				}
			}
			return 0, fmt.Errorf("session: promote %s: %w", row.ID, err)
		}
	}
	return len(rows), nil
}
