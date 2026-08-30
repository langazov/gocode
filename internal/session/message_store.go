package session

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/anomalyco/opencode-go/internal/db"
)

// StoredMessage is a projected session_message row.
type StoredMessage struct {
	ID          string
	SessionID   string
	Type        string
	Seq         int
	TimeCreated int64
	Data        json.RawMessage
}

type MessageStore struct {
	db *db.DB
}

func NewMessageStore(database *db.DB) *MessageStore {
	return &MessageStore{db: database}
}

// Append projects a message at an explicit sequence, allocated by the caller
// from the durable event stream.
func (s *MessageStore) Append(ctx context.Context, sessionID string, seq int, messageID, messageType string, data any) error {
	now := time.Now().UnixMilli()
	encoded, err := json.Marshal(data)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(ctx, `
		INSERT INTO session_message (id, session_id, type, seq, data, time_created, time_updated)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		messageID, sessionID, messageType, seq, string(encoded), now, now)
	return err
}

// NextSeq returns the next session_message sequence for a session.
func (s *MessageStore) NextSeq(ctx context.Context, sessionID string) (int, error) {
	var seq sql.NullInt64
	err := s.db.QueryRow(ctx,
		`SELECT MAX(seq) FROM session_message WHERE session_id = ?`, sessionID).Scan(&seq)
	if err != nil {
		return 0, err
	}
	if !seq.Valid {
		return 0, nil
	}
	return int(seq.Int64) + 1, nil
}

// List returns projected messages in sequence order.
func (s *MessageStore) List(ctx context.Context, sessionID string) ([]StoredMessage, error) {
	rows, err := s.db.Query(ctx, `
		SELECT id, session_id, type, seq, data, time_created
		FROM session_message WHERE session_id = ? ORDER BY seq ASC`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []StoredMessage
	for rows.Next() {
		var message StoredMessage
		var data string
		if err := rows.Scan(&message.ID, &message.SessionID, &message.Type, &message.Seq, &data, &message.TimeCreated); err != nil {
			return nil, err
		}
		message.Data = json.RawMessage(data)
		result = append(result, message)
	}
	return result, rows.Err()
}

// ListForRunner returns the history the model should see: when a compaction
// message exists, only the latest compaction and the messages after it are
// returned; otherwise the full history. Messages are ordered by sequence.
func (s *MessageStore) ListForRunner(ctx context.Context, sessionID string) ([]StoredMessage, error) {
	all, err := s.List(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	latest := -1
	for i := range all {
		if all[i].Type == "compaction" {
			latest = i
		}
	}
	if latest < 0 {
		return all, nil
	}
	return all[latest:], nil
}

// Get returns one projected message by ID.
func (s *MessageStore) Get(ctx context.Context, messageID string) (*StoredMessage, error) {
	row := s.db.QueryRow(ctx, `
		SELECT id, session_id, type, seq, data, time_created
		FROM session_message WHERE id = ?`, messageID)
	var message StoredMessage
	var data string
	if err := row.Scan(&message.ID, &message.SessionID, &message.Type, &message.Seq, &data, &message.TimeCreated); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	message.Data = json.RawMessage(data)
	return &message, nil
}

func DecodeAssistant(data json.RawMessage) (AssistantMessage, error) {
	var message AssistantMessage
	if err := json.Unmarshal(data, &message); err != nil {
		return AssistantMessage{}, fmt.Errorf("session: decode assistant message: %w", err)
	}
	return message, nil
}
