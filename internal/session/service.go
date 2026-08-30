package session

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/anomalyco/opencode-go/internal/db"
	"github.com/anomalyco/opencode-go/internal/event"
	"github.com/anomalyco/opencode-go/internal/id"
	"github.com/anomalyco/opencode-go/internal/identifier"
)

// Service is the application seam over the durable session core: creating
// sessions, admitting prompts, and waking execution.
type Service struct {
	DB           *db.DB
	Bus          *event.Bus
	Messages     *MessageStore
	Execution    *Execution
	Compactor    *Compactor
	DefaultModel ModelRef
}

func NewService(database *db.DB, bus *event.Bus) *Service {
	return &Service{
		DB:       database,
		Bus:      bus,
		Messages: NewMessageStore(database),
	}
}

type CreateInput struct {
	Directory string
	Title     string
}

type Info struct {
	ID          string    `json:"id"`
	ProjectID   string    `json:"projectID"`
	Title       string    `json:"title"`
	Directory   string    `json:"directory"`
	Version     string    `json:"version"`
	Model       *ModelRef `json:"model,omitempty"`
	TimeCreated int64     `json:"timeCreated"`
	TimeUpdated int64     `json:"timeUpdated"`
}

// Create adopts or creates the project for a directory and creates a session
// inside it, matching the V2 adoption semantics.
func (s *Service) Create(ctx context.Context, input CreateInput) (Info, error) {
	directory, err := filepath.Abs(input.Directory)
	if err != nil {
		return Info{}, err
	}
	title := input.Title
	if title == "" {
		title = filepath.Base(directory)
	}
	projectID, err := s.ensureProject(ctx, directory)
	if err != nil {
		return Info{}, err
	}
	sessionID := "ses_" + identifier.Descending()
	now := time.Now().UnixMilli()
	_, err = s.DB.Exec(ctx, `
		INSERT INTO session
			(id, project_id, slug, directory, title, version, cost,
			 tokens_input, tokens_output, tokens_reasoning, tokens_cache_read, tokens_cache_write,
			 time_created, time_updated)
		VALUES (?, ?, ?, ?, ?, '1', 0, 0, 0, 0, 0, 0, ?, ?)`,
		sessionID, projectID, title, directory, title, now, now)
	if err != nil {
		return Info{}, err
	}
	return Info{
		ID:          sessionID,
		ProjectID:   projectID,
		Title:       title,
		Directory:   directory,
		Version:     "1",
		TimeCreated: now,
		TimeUpdated: now,
	}, nil
}

// Get returns a session by ID.
func (s *Service) Get(ctx context.Context, sessionID string) (*Info, error) {
	row := s.DB.QueryRow(ctx, `
		SELECT id, project_id, title, directory, version, model, time_created, time_updated
		FROM session WHERE id = ?`, sessionID)
	var info Info
	var model sql.NullString
	err := row.Scan(&info.ID, &info.ProjectID, &info.Title, &info.Directory, &info.Version, &model, &info.TimeCreated, &info.TimeUpdated)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	info.Model = parseModelColumn(model)
	return &info, nil
}

// List returns all sessions, newest first.
func (s *Service) List(ctx context.Context) ([]Info, error) {
	rows, err := s.DB.Query(ctx, `
		SELECT id, project_id, title, directory, version, model, time_created, time_updated
		FROM session ORDER BY time_created DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []Info
	for rows.Next() {
		var info Info
		var model sql.NullString
		if err := rows.Scan(&info.ID, &info.ProjectID, &info.Title, &info.Directory, &info.Version, &model, &info.TimeCreated, &info.TimeUpdated); err != nil {
			return nil, err
		}
		info.Model = parseModelColumn(model)
		result = append(result, info)
	}
	return result, rows.Err()
}

// Prompt admits one durable prompt and wakes execution, returning the prompt
// message ID.
func (s *Service) Prompt(ctx context.Context, sessionID, text string, delivery Delivery) (string, error) {
	exists, err := s.exists(ctx, sessionID)
	if err != nil {
		return "", err
	}
	if !exists {
		return "", fmt.Errorf("Session not found: %s", sessionID)
	}
	if delivery == "" {
		delivery = DeliverySteer
	}
	messageID, err := id.Ascending(id.KindMessage)
	if err != nil {
		return "", err
	}
	if _, err := Admit(ctx, s.Bus, s.DB, AdmitInput{
		ID:        messageID,
		SessionID: sessionID,
		Prompt:    Prompt{Text: text},
		Delivery:  delivery,
	}); err != nil {
		return "", err
	}
	s.maybeSetTitle(ctx, sessionID, text)
	if s.Execution != nil {
		s.Execution.Wake(ctx, sessionID)
	}
	return messageID, nil
}

// maybeSetTitle names the session from its first prompt, replacing the
// directory-derived placeholder. Best-effort; failures do not block the prompt.
func (s *Service) maybeSetTitle(ctx context.Context, sessionID, text string) {
	var count int
	if err := s.DB.QueryRow(ctx,
		`SELECT COUNT(*) FROM session_input WHERE session_id = ?`, sessionID).Scan(&count); err != nil {
		return
	}
	if count != 1 {
		return
	}
	title := sessionTitle(text)
	if title == "" {
		return
	}
	_, _ = s.DB.Exec(ctx,
		`UPDATE session SET title = ?, time_updated = ? WHERE id = ?`,
		title, time.Now().UnixMilli(), sessionID)
}

func sessionTitle(text string) string {
	title := strings.TrimSpace(text)
	title = strings.Join(strings.Fields(title), " ")
	const max = 60
	if len(title) > max {
		title = strings.TrimSpace(title[:max]) + "…"
	}
	return title
}

// Interrupt stops any active execution for the session. Idle interruption is
// a no-op.
func (s *Service) Interrupt(sessionID string) {
	if s.Execution != nil {
		s.Execution.Interrupt(sessionID)
	}
}

func (s *Service) exists(ctx context.Context, sessionID string) (bool, error) {
	var id string
	err := s.DB.QueryRow(ctx, `SELECT id FROM session WHERE id = ?`, sessionID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func (s *Service) ensureProject(ctx context.Context, directory string) (string, error) {
	var projectID string
	err := s.DB.QueryRow(ctx,
		`SELECT id FROM project WHERE worktree = ? LIMIT 1`, directory).Scan(&projectID)
	if err == nil {
		return projectID, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	projectID = directory
	now := time.Now().UnixMilli()
	_, err = s.DB.Exec(ctx, `
		INSERT INTO project (id, worktree, sandboxes, time_created, time_updated)
		VALUES (?, ?, '[]', ?, ?)`, projectID, directory, now, now)
	if err != nil {
		return "", err
	}
	return projectID, nil
}

// Rename sets a session's title.
func (s *Service) Rename(ctx context.Context, sessionID, title string) error {
	title = strings.TrimSpace(title)
	if title == "" {
		return fmt.Errorf("title must not be empty")
	}
	res, err := s.DB.Exec(ctx,
		`UPDATE session SET title = ?, time_updated = ? WHERE id = ?`,
		title, time.Now().UnixMilli(), sessionID)
	if err != nil {
		return err
	}
	if affected, _ := res.RowsAffected(); affected == 0 {
		return fmt.Errorf("Session not found: %s", sessionID)
	}
	return nil
}

// Delete removes a session and its dependent rows via cascade.
func (s *Service) Delete(ctx context.Context, sessionID string) error {
	res, err := s.DB.Exec(ctx, `DELETE FROM session WHERE id = ?`, sessionID)
	if err != nil {
		return err
	}
	if affected, _ := res.RowsAffected(); affected == 0 {
		return fmt.Errorf("Session not found: %s", sessionID)
	}
	return nil
}

// SetModel pins a model on the session row; the runner resolves it first.
func (s *Service) SetModel(ctx context.Context, sessionID string, model ModelRef) error {
	encoded, err := json.Marshal(model)
	if err != nil {
		return err
	}
	res, err := s.DB.Exec(ctx,
		`UPDATE session SET model = ?, time_updated = ? WHERE id = ?`,
		string(encoded), time.Now().UnixMilli(), sessionID)
	if err != nil {
		return err
	}
	if affected, _ := res.RowsAffected(); affected == 0 {
		return fmt.Errorf("Session not found: %s", sessionID)
	}
	return nil
}

// SetAgent pins an agent on the session row.
func (s *Service) SetAgent(ctx context.Context, sessionID, agent string) error {
	res, err := s.DB.Exec(ctx,
		`UPDATE session SET agent = ?, time_updated = ? WHERE id = ?`,
		agent, time.Now().UnixMilli(), sessionID)
	if err != nil {
		return err
	}
	if affected, _ := res.RowsAffected(); affected == 0 {
		return fmt.Errorf("Session not found: %s", sessionID)
	}
	return nil
}

type Todo struct {
	Content  string `json:"content"`
	Status   string `json:"status"`
	Priority string `json:"priority"`
	Position int    `json:"position"`
}

// Todos returns the session's todo list in position order.
func (s *Service) Todos(ctx context.Context, sessionID string) ([]Todo, error) {
	rows, err := s.DB.Query(ctx, `
		SELECT content, status, priority, position FROM todo
		WHERE session_id = ? ORDER BY position ASC`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Todo
	for rows.Next() {
		var todo Todo
		if err := rows.Scan(&todo.Content, &todo.Status, &todo.Priority, &todo.Position); err != nil {
			return nil, err
		}
		out = append(out, todo)
	}
	return out, rows.Err()
}

// Stats summarizes a session for the status view.
func (s *Service) Stats(ctx context.Context, sessionID string) (map[string]any, error) {
	var (
		cost          float64
		input, output int
		reasoning     int
		cacheRead     int
		cacheWrite    int
		messages      int
	)
	err := s.DB.QueryRow(ctx, `
		SELECT cost, tokens_input, tokens_output, tokens_reasoning,
		       tokens_cache_read, tokens_cache_write
		FROM session WHERE id = ?`, sessionID).
		Scan(&cost, &input, &output, &reasoning, &cacheRead, &cacheWrite)
	if err != nil {
		return nil, err
	}
	if scanErr := s.DB.QueryRow(ctx,
		`SELECT COUNT(*) FROM session_message WHERE session_id = ?`, sessionID).Scan(&messages); scanErr != nil {
		return nil, scanErr
	}
	return map[string]any{
		"cost": cost, "tokensInput": input, "tokensOutput": output,
		"tokensReasoning": reasoning, "tokensCacheRead": cacheRead,
		"tokensCacheWrite": cacheWrite, "messages": messages,
	}, nil
}

// CompactNow runs context compaction immediately (the /compact endpoint and
// leader+c binding). The Compactor and default model are wired at boot.
func (s *Service) CompactNow(ctx context.Context, sessionID string) (bool, error) {
	if s.Compactor == nil {
		return false, fmt.Errorf("compaction is not configured")
	}
	history, err := s.Messages.ListForRunner(ctx, sessionID)
	if err != nil {
		return false, err
	}
	model, err := LoadSessionModel(ctx, s.DB, sessionID)
	if err != nil {
		return false, err
	}
	if model == nil {
		model = &s.DefaultModel
	}
	return s.Compactor.Compact(ctx, sessionID, history, *model)
}

// Children returns sessions forked from this one.
func (s *Service) Children(ctx context.Context, sessionID string) ([]Info, error) {
	rows, err := s.DB.Query(ctx, `
		SELECT id, project_id, title, directory, version, time_created, time_updated
		FROM session WHERE parent_id = ? ORDER BY time_created ASC`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Info
	for rows.Next() {
		var info Info
		if err := rows.Scan(&info.ID, &info.ProjectID, &info.Title, &info.Directory, &info.Version, &info.TimeCreated, &info.TimeUpdated); err != nil {
			return nil, err
		}
		out = append(out, info)
	}
	return out, rows.Err()
}

// Fork copies the session and its messages up to and including messageID into
// a new child session, returning the child. An empty messageID copies all.
func (s *Service) Fork(ctx context.Context, sessionID, messageID string) (Info, error) {
	parent, err := s.Get(ctx, sessionID)
	if err != nil {
		return Info{}, err
	}
	if parent == nil {
		return Info{}, fmt.Errorf("Session not found: %s", sessionID)
	}
	childID := "ses_" + identifier.Descending()
	now := time.Now().UnixMilli()
	child := Info{
		ID: childID, ProjectID: parent.ProjectID,
		Title: "Fork: " + parent.Title, Directory: parent.Directory,
		Version: parent.Version, TimeCreated: now, TimeUpdated: now,
	}
	err = s.DB.Transaction(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO session (id, project_id, parent_id, slug, directory, title, version,
				cost, tokens_input, tokens_output, tokens_reasoning, tokens_cache_read, tokens_cache_write,
				time_created, time_updated)
			VALUES (?, ?, ?, ?, ?, ?, '1', 0, 0, 0, 0, 0, 0, ?, ?)`,
			child.ID, child.ProjectID, parent.ID, child.Title, child.Directory, child.Title, now, now); err != nil {
			return err
		}
		query := `
			INSERT INTO session_message (id, session_id, type, seq, data, time_created, time_updated)
			SELECT id, ?, type, seq, data, time_created, ?
			FROM session_message WHERE session_id = ?`
		args := []any{child.ID, now, sessionID}
		if messageID != "" {
			query += ` AND seq <= (SELECT seq FROM session_message WHERE id = ? AND session_id = ?)`
			args = append(args, messageID, sessionID)
		}
		_, err := tx.ExecContext(ctx, query, args...)
		return err
	})
	if err != nil {
		return Info{}, err
	}
	return child, nil
}

func parseModelColumn(model sql.NullString) *ModelRef {
	if !model.Valid || model.String == "" {
		return nil
	}
	var ref ModelRef
	if err := json.Unmarshal([]byte(model.String), &ref); err != nil {
		return nil
	}
	return &ref
}
