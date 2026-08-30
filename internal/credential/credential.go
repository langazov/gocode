package credential

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/anomalyco/opencode-go/internal/db"
	"github.com/anomalyco/opencode-go/internal/identifier"
)

type ID string

func NewID() ID {
	return ID("cred_" + identifier.Ascending())
}

// Value is the OAuth | Key union stored as JSON text, matching
// Credential.Value in packages/schema/src/credential.ts.
type Value interface {
	valueType() string
}

type OAuth struct {
	MethodID string         `json:"methodID"`
	Refresh  string         `json:"refresh"`
	Access   string         `json:"access"`
	Expires  int64          `json:"expires"`
	Metadata map[string]any `json:"metadata,omitempty"`
}

func (OAuth) valueType() string { return "oauth" }

type Key struct {
	Key      string         `json:"key"`
	Metadata map[string]any `json:"metadata,omitempty"`
}

func (Key) valueType() string { return "key" }

func encodeValue(value Value) (string, error) {
	var payload map[string]any
	raw, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return "", err
	}
	payload["type"] = value.valueType()
	out, err := json.Marshal(payload)
	return string(out), err
}

func decodeValue(text string) (Value, error) {
	var probe struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal([]byte(text), &probe); err != nil {
		return nil, err
	}
	switch probe.Type {
	case "oauth":
		var oauth OAuth
		if err := json.Unmarshal([]byte(text), &oauth); err != nil {
			return nil, err
		}
		return oauth, nil
	case "key":
		var key Key
		if err := json.Unmarshal([]byte(text), &key); err != nil {
			return nil, err
		}
		return key, nil
	default:
		return nil, fmt.Errorf("credential: unknown value type %q", probe.Type)
	}
}

type Info struct {
	ID            ID
	IntegrationID string
	Label         string
	Value         Value
}

type Store struct {
	db *db.DB
}

func NewStore(database *db.DB) *Store {
	return &Store{db: database}
}

func (s *Store) All(ctx context.Context) ([]Info, error) {
	return s.query(ctx, `SELECT id, integration_id, label, value FROM credential ORDER BY time_created ASC`)
}

func (s *Store) List(ctx context.Context, integrationID string) ([]Info, error) {
	return s.query(ctx,
		`SELECT id, integration_id, label, value FROM credential WHERE integration_id = ? ORDER BY time_created ASC`,
		integrationID)
}

func (s *Store) query(ctx context.Context, sqlText string, args ...any) ([]Info, error) {
	rows, err := s.db.Query(ctx, sqlText, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []Info
	for rows.Next() {
		info, ok, err := scanRow(rows)
		if err != nil {
			return nil, err
		}
		if ok {
			result = append(result, info)
		}
	}
	return result, rows.Err()
}

func scanRow(rows *sql.Rows) (Info, bool, error) {
	var (
		id          string
		integration sql.NullString
		label       string
		valueText   string
	)
	if err := rows.Scan(&id, &integration, &label, &valueText); err != nil {
		return Info{}, false, err
	}
	// Rows without an integration are legacy and skipped, matching TS
	// (both NULL and empty string are falsy there).
	if !integration.Valid || integration.String == "" {
		return Info{}, false, nil
	}
	value, err := decodeValue(valueText)
	if err != nil {
		return Info{}, false, err
	}
	return Info{ID: ID(id), IntegrationID: integration.String, Label: label, Value: value}, true, nil
}

func (s *Store) Get(ctx context.Context, id ID) (*Info, error) {
	rows, err := s.db.Query(ctx,
		`SELECT id, integration_id, label, value FROM credential WHERE id = ?`, string(id))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, rows.Err()
	}
	info, ok, err := scanRow(rows)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, rows.Err()
	}
	return &info, rows.Err()
}

type CreateInput struct {
	IntegrationID string
	Value         Value
	Label         string
}

// Create replaces any credential for the integration, matching TS semantics.
func (s *Store) Create(ctx context.Context, input CreateInput) (Info, error) {
	label := input.Label
	if label == "" {
		label = "default"
	}
	info := Info{
		ID:            NewID(),
		IntegrationID: input.IntegrationID,
		Label:         label,
		Value:         input.Value,
	}
	valueText, err := encodeValue(info.Value)
	if err != nil {
		return Info{}, err
	}
	now := time.Now().UnixMilli()
	err = s.db.Transaction(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM credential WHERE integration_id = ?`, info.IntegrationID); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx,
			`INSERT INTO credential (id, integration_id, label, value, time_created, time_updated) VALUES (?, ?, ?, ?, ?, ?)`,
			string(info.ID), info.IntegrationID, info.Label, valueText, now, now)
		return err
	})
	if err != nil {
		return Info{}, err
	}
	return info, nil
}

type Updates struct {
	Label *string
	Value Value
}

func (s *Store) Update(ctx context.Context, id ID, updates Updates) error {
	if updates.Label == nil && updates.Value == nil {
		return nil
	}
	set := ""
	var args []any
	if updates.Label != nil {
		set = "label = ?"
		args = append(args, *updates.Label)
	}
	if updates.Value != nil {
		valueText, err := encodeValue(updates.Value)
		if err != nil {
			return err
		}
		if set != "" {
			set += ", "
		}
		set += "value = ?"
		args = append(args, valueText)
	}
	set += ", time_updated = ?"
	args = append(args, time.Now().UnixMilli(), string(id))
	_, err := s.db.Exec(ctx, `UPDATE credential SET `+set+` WHERE id = ?`, args...)
	return err
}

func (s *Store) Remove(ctx context.Context, id ID) error {
	_, err := s.db.Exec(ctx, `DELETE FROM credential WHERE id = ?`, string(id))
	return err
}
