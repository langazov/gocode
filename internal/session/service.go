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

	"github.com/langazov/gocode-go/internal/db"
	"github.com/langazov/gocode-go/internal/event"
	"github.com/langazov/gocode-go/internal/id"
	"github.com/langazov/gocode-go/internal/identifier"
	"github.com/langazov/gocode-go/internal/permission"
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
	// ParentID links this session to the one that spawned it. Set for
	// subagent sessions created by the task tool; Fork sets it directly for
	// user-facing conversation forks.
	ParentID string
	// Agent pins the session to a specific agent, so the runner resolves that
	// agent's model, system prompt, and step budget for every turn.
	Agent string
	// Permission overrides the agent's ruleset for this session only. Used by
	// the task tool to hand a subagent a ruleset derived from its parent's.
	Permission permission.Ruleset
}

type Info struct {
	ID          string    `json:"id"`
	ProjectID   string    `json:"projectID"`
	ParentID    string    `json:"parentID,omitempty"`
	Agent       string    `json:"agent,omitempty"`
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
			(id, project_id, parent_id, agent, slug, directory, title, version, cost,
			 tokens_input, tokens_output, tokens_reasoning, tokens_cache_read, tokens_cache_write,
			 time_created, time_updated)
		VALUES (?, ?, ?, ?, ?, ?, ?, '1', 0, 0, 0, 0, 0, 0, ?, ?)`,
		sessionID, projectID, nullableString(input.ParentID), nullableString(input.Agent),
		title, directory, title, now, now)
	if err != nil {
		return Info{}, err
	}
	if len(input.Permission) > 0 {
		if err := s.setPermission(ctx, sessionID, input.Permission); err != nil {
			return Info{}, err
		}
	}
	return Info{
		ID:          sessionID,
		ProjectID:   projectID,
		ParentID:    input.ParentID,
		Agent:       input.Agent,
		Title:       title,
		Directory:   directory,
		Version:     "1",
		TimeCreated: now,
		TimeUpdated: now,
	}, nil
}

// nullableString stores an empty string as SQL NULL, keeping parent_id and
// agent nullable as the schema declares them.
func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
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
// Prompt admits a text-only message. PromptWith carries attachments.
func (s *Service) Prompt(ctx context.Context, sessionID, text string, delivery Delivery) (string, error) {
	return s.PromptWith(ctx, sessionID, Prompt{Text: text}, delivery)
}

// PromptWith admits a message that may carry file attachments.
func (s *Service) PromptWith(ctx context.Context, sessionID string, prompt Prompt, delivery Delivery) (string, error) {
	text := prompt.Text
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
		Prompt:    prompt,
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

// QueuedPrompt is one prompt waiting its turn, in the shape a client renders.
type QueuedPrompt struct {
	ID          string           `json:"id"`
	Text        string           `json:"text"`
	Files       []FileAttachment `json:"files,omitempty"`
	Delivery    string           `json:"delivery"`
	TimeCreated int64            `json:"timeCreated"`
}

// Queued lists the prompts admitted for the session that the runner has not
// reached yet, oldest first. Empty when nothing is waiting.
func (s *Service) Queued(ctx context.Context, sessionID string) ([]QueuedPrompt, error) {
	pending, err := Pending(ctx, s.DB, sessionID)
	if err != nil {
		return nil, err
	}
	out := make([]QueuedPrompt, 0, len(pending))
	for _, row := range pending {
		out = append(out, QueuedPrompt{
			ID:          row.ID,
			Text:        row.Prompt.Text,
			Files:       row.Prompt.Files,
			Delivery:    string(row.Delivery),
			TimeCreated: row.TimeCreated,
		})
	}
	return out, nil
}

// Busy reports whether a turn is running for the session right now. This is
// the authoritative answer the RunStarted/RunEnded events only *announce*:
// events can be dropped under load, and a client that connects mid-turn never
// saw the one that mattered. Readers reconcile against this.
func (s *Service) Busy(sessionID string) bool {
	if s.Execution == nil {
		return false
	}
	for _, active := range s.Execution.Active() {
		if active == sessionID {
			return true
		}
	}
	return false
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
	return EnsureProject(ctx, s.DB, directory)
}

// EnsureProject returns the project row for a worktree, creating it if this is
// the first time gocode has been run there. Exported because anything keyed
// by project — saved permissions, for one — needs the same ID before a session
// exists to create it.
func EnsureProject(ctx context.Context, database *db.DB, directory string) (string, error) {
	var projectID string
	err := database.QueryRow(ctx,
		`SELECT id FROM project WHERE worktree = ? LIMIT 1`, directory).Scan(&projectID)
	if err == nil {
		return projectID, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	projectID = directory
	now := time.Now().UnixMilli()
	_, err = database.Exec(ctx, `
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

// SetAgent pins an agent on the session row and announces the switch.
//
// The announcement matters because the switch can originate server-side — the
// plan_enter/plan_exit tools call this from inside a turn — and a client that
// only tracks agent changes it initiated itself would keep showing the old
// agent while the session ran under the new one.
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
	if s.Bus == nil {
		return nil
	}
	messageID, err := id.Ascending(id.KindMessage)
	if err != nil {
		return err
	}
	_, err = s.Bus.Publish(ctx, AgentSwitched, map[string]any{
		"timestamp": time.Now().UnixMilli(),
		"sessionID": sessionID,
		"messageID": messageID,
		"agent":     agent,
	}, event.PublishOptions{})
	return err
}

// EnqueuePrompt admits a prompt for delivery at the session's next idle
// boundary. Exists as its own method so the tool layer can depend on this one
// verb without importing the session package for its Delivery type.
func (s *Service) EnqueuePrompt(ctx context.Context, sessionID, text string) error {
	_, err := s.Prompt(context.WithoutCancel(ctx), sessionID, text, DeliveryQueue)
	return err
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

// setPermission stores a session-scoped ruleset, overriding the agent's own
// for this session. Used for subagent sessions, whose ruleset is derived from
// the parent's grants intersected with the subagent's own.
func (s *Service) setPermission(ctx context.Context, sessionID string, ruleset permission.Ruleset) error {
	encoded, err := json.Marshal(ruleset)
	if err != nil {
		return err
	}
	_, err = s.DB.Exec(ctx,
		`UPDATE session SET permission = ?, time_updated = ? WHERE id = ?`,
		string(encoded), time.Now().UnixMilli(), sessionID)
	return err
}

// Permission returns the session-scoped ruleset, or nil when the session
// inherits its agent's ruleset unchanged.
func (s *Service) Permission(ctx context.Context, sessionID string) (permission.Ruleset, error) {
	var raw sql.NullString
	err := s.DB.QueryRow(ctx, `SELECT permission FROM session WHERE id = ?`, sessionID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !raw.Valid || raw.String == "" {
		return nil, nil
	}
	var ruleset permission.Ruleset
	if err := json.Unmarshal([]byte(raw.String), &ruleset); err != nil {
		return nil, fmt.Errorf("session: decode permission for %s: %w", sessionID, err)
	}
	return ruleset, nil
}

// Parents walks the parent chain upward from sessionID, nearest first. The
// session itself is not included. Used for the subagent depth limit.
func (s *Service) Parents(ctx context.Context, sessionID string) ([]string, error) {
	var out []string
	current := sessionID
	for range 64 { // guard against a cycle in corrupted data
		var parent sql.NullString
		err := s.DB.QueryRow(ctx, `SELECT parent_id FROM session WHERE id = ?`, current).Scan(&parent)
		if errors.Is(err, sql.ErrNoRows) {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
		if !parent.Valid || parent.String == "" {
			return out, nil
		}
		out = append(out, parent.String)
		current = parent.String
	}
	return out, nil
}
