package builtins

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/anomalyco/opencode-go/internal/db"
	"github.com/anomalyco/opencode-go/internal/tool"
)

type TodoTool struct {
	db *db.DB
}

func NewTodoTool(database *db.DB) *TodoTool {
	return &TodoTool{db: database}
}

func (t *TodoTool) Name() string { return "todowrite" }

func (t *TodoTool) Description() string {
	return "Create and maintain a structured task list for the current coding session. Use it to track progress during multi-step work and keep todo statuses current."
}

func (t *TodoTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"todos": map[string]any{
				"type":        "array",
				"description": "The updated todo list",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"content":  map[string]any{"type": "string", "description": "Brief description of the task"},
						"status":   map[string]any{"type": "string", "description": "pending, in_progress, completed, or cancelled"},
						"priority": map[string]any{"type": "string", "description": "high, medium, or low"},
					},
					"required": []string{"content", "status", "priority"},
				},
			},
		},
		"required": []string{"todos"},
	}
}

type todoItem struct {
	Content  string `json:"content"`
	Status   string `json:"status"`
	Priority string `json:"priority"`
}

// Execute without a session context cannot persist todos; the registry routes
// this tool through ExecuteWithContext instead.
func (t *TodoTool) Execute(ctx context.Context, input map[string]any) (string, error) {
	return "", fmt.Errorf("todowrite: missing session context")
}

func (t *TodoTool) ExecuteWithContext(ctx context.Context, input map[string]any, exec tool.ExecContext) (string, error) {
	if exec.SessionID == "" {
		return "", fmt.Errorf("todowrite: missing session context")
	}
	rawTodos, ok := input["todos"].([]any)
	if !ok {
		return "", fmt.Errorf("todowrite: todos must be an array")
	}
	todos := make([]todoItem, 0, len(rawTodos))
	for _, raw := range rawTodos {
		itemMap, ok := raw.(map[string]any)
		if !ok {
			return "", fmt.Errorf("todowrite: invalid todo entry")
		}
		todos = append(todos, todoItem{
			Content:  stringField(itemMap, "content"),
			Status:   stringField(itemMap, "status"),
			Priority: stringField(itemMap, "priority"),
		})
	}
	if err := t.replaceAll(ctx, exec.SessionID, todos); err != nil {
		return "", fmt.Errorf("Unable to update todos")
	}
	encoded, err := json.MarshalIndent(todos, "", "  ")
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

func (t *TodoTool) replaceAll(ctx context.Context, sessionID string, todos []todoItem) error {
	return t.db.Transaction(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM todo WHERE session_id = ?`, sessionID); err != nil {
			return err
		}
		now := time.Now().UnixMilli()
		for position, item := range todos {
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO todo (session_id, content, status, priority, position, time_created, time_updated)
				VALUES (?, ?, ?, ?, ?, ?, ?)`,
				sessionID, item.Content, item.Status, item.Priority, position, now, now); err != nil {
				return err
			}
		}
		return nil
	})
}

func stringField(input map[string]any, key string) string {
	value, _ := input[key].(string)
	return value
}
