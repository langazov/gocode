package session

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/langazov/gocode-go/internal/event"
)

// Durable runner events, mirroring the Step and Tool namespaces in
// packages/schema/src/session-event.ts. All aggregate on sessionID.
var (
	StepStarted = event.Definition{
		Type:    "session.next.step.started",
		Durable: &event.DurableDef{Aggregate: "sessionID", Version: 1},
	}
	StepEnded = event.Definition{
		Type:    "session.next.step.ended",
		Durable: &event.DurableDef{Aggregate: "sessionID", Version: 2},
	}
	StepFailed = event.Definition{
		Type:    "session.next.step.failed",
		Durable: &event.DurableDef{Aggregate: "sessionID", Version: 2},
	}
	ToolCalled = event.Definition{
		Type:    "session.next.tool.called",
		Durable: &event.DurableDef{Aggregate: "sessionID", Version: 1},
	}
	ToolSuccess = event.Definition{
		Type:    "session.next.tool.success",
		Durable: &event.DurableDef{Aggregate: "sessionID", Version: 1},
	}
	ToolFailed = event.Definition{
		Type:    "session.next.tool.failed",
		Durable: &event.DurableDef{Aggregate: "sessionID", Version: 1},
	}
	TextStarted = event.Definition{
		Type:    "session.next.text.started",
		Durable: &event.DurableDef{Aggregate: "sessionID", Version: 1},
	}
	// TextDelta is live-only; TextEnded is the replayable full-value boundary.
	TextDelta = event.Definition{
		Type: "session.next.text.delta",
	}
	TextEnded = event.Definition{
		Type:    "session.next.text.ended",
		Durable: &event.DurableDef{Aggregate: "sessionID", Version: 1},
	}
	ReasoningStarted = event.Definition{
		Type:    "session.next.reasoning.started",
		Durable: &event.DurableDef{Aggregate: "sessionID", Version: 1},
	}
	ReasoningDelta = event.Definition{
		Type: "session.next.reasoning.delta",
	}
	ReasoningEnded = event.Definition{
		Type:    "session.next.reasoning.ended",
		Durable: &event.DurableDef{Aggregate: "sessionID", Version: 1},
	}
	CompactionStarted = event.Definition{
		Type:    "session.next.compaction.started",
		Durable: &event.DurableDef{Aggregate: "sessionID", Version: 1},
	}
	CompactionEnded = event.Definition{
		Type:    "session.next.compaction.ended",
		Durable: &event.DurableDef{Aggregate: "sessionID", Version: 1},
	}
)

// RegisterRunnerProjectors wires the assistant-message projections to the
// runner's durable events.
func RegisterRunnerProjectors(bus *event.Bus) {
	bus.Project(StepStarted, projectStepStarted)
	bus.Project(StepEnded, projectStepEnded)
	bus.Project(StepFailed, projectStepFailed)
	bus.Project(ToolCalled, projectToolCalled)
	bus.Project(ToolSuccess, projectToolSettled)
	bus.Project(ToolFailed, projectToolSettled)
	bus.Project(TextStarted, projectContentStarted)
	bus.Project(TextDelta, projectContentDelta)
	bus.Project(TextEnded, projectContentEnded)
	bus.Project(ReasoningStarted, projectContentStarted)
	bus.Project(ReasoningDelta, projectContentDelta)
	bus.Project(ReasoningEnded, projectContentEnded)
	bus.Project(CompactionEnded, projectCompactionEnded)
}

func projectStepStarted(ctx context.Context, tx *sql.Tx, payload event.Payload) error {
	data := payload.Data
	assistantMessageID, _ := data["assistantMessageID"].(string)
	sessionID, _ := data["sessionID"].(string)
	agent, _ := data["agent"].(string)
	modelRaw, _ := data["model"].(map[string]any)
	created := asInt64(data["timestamp"])

	messageData := map[string]any{
		"agent":   agent,
		"model":   modelRaw,
		"content": []any{},
		"time":    map[string]any{"created": created},
	}
	encoded, err := json.Marshal(messageData)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO session_message (id, session_id, type, seq, data, time_created, time_updated)
		VALUES (?, ?, 'assistant', ?, ?, ?, ?)`,
		assistantMessageID, sessionID, payload.Durable.Seq, string(encoded), time.Now().UnixMilli(), time.Now().UnixMilli())
	return err
}

func projectStepEnded(ctx context.Context, tx *sql.Tx, payload event.Payload) error {
	// The session row carries the running totals the stats endpoint reports
	// (and `session.cost`, which the sidebar shows as "spent"). Nothing used
	// to write these columns, so every session reported 0 tokens and $0.00
	// for its whole life.
	if err := accumulateSessionUsage(ctx, tx, payload); err != nil {
		return err
	}
	assistantMessageID, _ := payload.Data["assistantMessageID"].(string)
	return updateAssistant(ctx, tx, assistantMessageID, func(message map[string]any) {
		// Content parts are projected incrementally by the text, reasoning, and
		// tool events; the step settlement only records finish/cost/tokens.
		if finish, ok := payload.Data["finish"]; ok {
			message["finish"] = finish
		}
		if tokens, ok := payload.Data["tokens"]; ok {
			message["tokens"] = tokens
		}
		if cost, ok := payload.Data["cost"]; ok {
			message["cost"] = cost
		}
		setCompleted(message, asInt64(payload.Data["timestamp"]))
	})
}

func projectStepFailed(ctx context.Context, tx *sql.Tx, payload event.Payload) error {
	assistantMessageID, _ := payload.Data["assistantMessageID"].(string)
	return updateAssistant(ctx, tx, assistantMessageID, func(message map[string]any) {
		if errValue, ok := payload.Data["error"]; ok {
			message["error"] = errValue
		}
		setCompleted(message, asInt64(payload.Data["timestamp"]))
	})
}

func projectToolCalled(ctx context.Context, tx *sql.Tx, payload event.Payload) error {
	assistantMessageID, _ := payload.Data["assistantMessageID"].(string)
	callID, _ := payload.Data["callID"].(string)
	toolName, _ := payload.Data["tool"].(string)
	input, _ := payload.Data["input"].(map[string]any)
	created := asInt64(payload.Data["timestamp"])
	return updateAssistant(ctx, tx, assistantMessageID, func(message map[string]any) {
		content := contentSlice(message)
		content = append(content, map[string]any{
			"type": "tool",
			"id":   callID,
			"name": toolName,
			"state": map[string]any{
				"status": ToolPending,
				"input":  input,
			},
			"time": map[string]any{"created": created},
		})
		message["content"] = content
	})
}

// projectToolSettled handles both ToolSuccess and ToolFailed by reading the
// event type to choose the resulting state.
func projectToolSettled(ctx context.Context, tx *sql.Tx, payload event.Payload) error {
	assistantMessageID, _ := payload.Data["assistantMessageID"].(string)
	callID, _ := payload.Data["callID"].(string)
	completed := asInt64(payload.Data["timestamp"])
	isSuccess := payload.Type == ToolSuccess.Type
	return updateAssistant(ctx, tx, assistantMessageID, func(message map[string]any) {
		content := contentSlice(message)
		for i := range content {
			part, ok := content[i].(map[string]any)
			if !ok || part["id"] != callID {
				continue
			}
			state, _ := part["state"].(map[string]any)
			if state == nil {
				state = map[string]any{}
			}
			state["completed"] = completed
			if isSuccess {
				state["status"] = ToolCompleted
				if output, ok := payload.Data["output"]; ok {
					state["output"] = output
				}
			} else {
				state["status"] = ToolError
				if errValue, ok := payload.Data["error"].(map[string]any); ok {
					state["error"] = errValue["message"]
				}
			}
			part["state"] = state
			content[i] = part
		}
		message["content"] = content
	})
}

func projectCompactionEnded(ctx context.Context, tx *sql.Tx, payload event.Payload) error {
	data := payload.Data
	messageID, _ := data["messageID"].(string)
	sessionID, _ := data["sessionID"].(string)
	reason, _ := data["reason"].(string)
	summary, _ := data["text"].(string)
	recent, _ := data["recent"].(string)
	created := asInt64(data["timestamp"])

	messageData := map[string]any{
		"reason":  reason,
		"summary": summary,
		"recent":  recent,
		"time":    map[string]any{"created": created},
	}
	encoded, err := json.Marshal(messageData)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO session_message (id, session_id, type, seq, data, time_created, time_updated)
		VALUES (?, ?, 'compaction', ?, ?, ?, ?)`,
		messageID, sessionID, payload.Durable.Seq, string(encoded), time.Now().UnixMilli(), time.Now().UnixMilli())
	return err
}

func contentSlice(message map[string]any) []any {
	if content, ok := message["content"].([]any); ok {
		return content
	}
	return []any{}
}

// contentMeta derives the content part type and ID field from the event type,
// shared by the text and reasoning projections.
func contentMeta(eventType string) (partType, idField string) {
	switch eventType {
	case TextStarted.Type, TextDelta.Type, TextEnded.Type:
		return "text", "textID"
	default:
		return "reasoning", "reasoningID"
	}
}

func projectContentStarted(ctx context.Context, tx *sql.Tx, payload event.Payload) error {
	partType, idField := contentMeta(payload.Type)
	assistantMessageID, _ := payload.Data["assistantMessageID"].(string)
	partID, _ := payload.Data[idField].(string)
	created := asInt64(payload.Data["timestamp"])
	return updateAssistant(ctx, tx, assistantMessageID, func(message map[string]any) {
		content := contentSlice(message)
		content = append(content, map[string]any{
			"type": partType,
			"id":   partID,
			"text": "",
			// "created" here (not TS's "start") matches the ContentTime
			// field this port already had lying around unused; see
			// AssistantContent.Time in message.go.
			"time": map[string]any{"created": created},
		})
		message["content"] = content
	})
}

func projectContentDelta(ctx context.Context, tx *sql.Tx, payload event.Payload) error {
	_, idField := contentMeta(payload.Type)
	assistantMessageID, _ := payload.Data["assistantMessageID"].(string)
	partID, _ := payload.Data[idField].(string)
	delta, _ := payload.Data["delta"].(string)
	return updateAssistant(ctx, tx, assistantMessageID, func(message map[string]any) {
		content := contentSlice(message)
		for i := range content {
			part, ok := content[i].(map[string]any)
			if !ok || part["id"] != partID {
				continue
			}
			text, _ := part["text"].(string)
			part["text"] = text + delta
			content[i] = part
		}
		message["content"] = content
	})
}

func projectContentEnded(ctx context.Context, tx *sql.Tx, payload event.Payload) error {
	_, idField := contentMeta(payload.Type)
	assistantMessageID, _ := payload.Data["assistantMessageID"].(string)
	partID, _ := payload.Data[idField].(string)
	fullText, _ := payload.Data["text"].(string)
	completed := asInt64(payload.Data["timestamp"])
	return updateAssistant(ctx, tx, assistantMessageID, func(message map[string]any) {
		content := contentSlice(message)
		for i := range content {
			part, ok := content[i].(map[string]any)
			if !ok || part["id"] != partID {
				continue
			}
			part["text"] = fullText
			partTime, _ := part["time"].(map[string]any)
			if partTime == nil {
				partTime = map[string]any{}
			}
			partTime["completed"] = completed
			part["time"] = partTime
			content[i] = part
		}
		message["content"] = content
	})
}

func setCompleted(message map[string]any, completed int64) {
	timeMap, _ := message["time"].(map[string]any)
	if timeMap == nil {
		timeMap = map[string]any{}
	}
	timeMap["completed"] = completed
	message["time"] = timeMap
}

// updateAssistant loads the assistant message row, applies mutate to its data
// JSON, and writes it back inside the transaction.
func updateAssistant(ctx context.Context, tx *sql.Tx, assistantMessageID string, mutate func(map[string]any)) error {
	var dataText string
	err := tx.QueryRowContext(ctx,
		`SELECT data FROM session_message WHERE id = ?`, assistantMessageID).Scan(&dataText)
	if err != nil {
		return fmt.Errorf("%w: assistant message %s not found", ErrLifecycleConflict, assistantMessageID)
	}
	var message map[string]any
	if err := json.Unmarshal([]byte(dataText), &message); err != nil {
		return err
	}
	mutate(message)
	encoded, err := json.Marshal(message)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx,
		`UPDATE session_message SET data = ?, time_updated = ? WHERE id = ?`,
		string(encoded), time.Now().UnixMilli(), assistantMessageID)
	return err
}

// accumulateSessionUsage adds one settled step's tokens and cost to the
// session's running totals, inside the same transaction that commits the step.
func accumulateSessionUsage(ctx context.Context, tx *sql.Tx, payload event.Payload) error {
	sessionID, _ := payload.Data["sessionID"].(string)
	if sessionID == "" {
		return nil
	}
	tokens, _ := payload.Data["tokens"].(map[string]any)
	cache, _ := tokens["cache"].(map[string]any)
	cost, _ := payload.Data["cost"].(float64)

	_, err := tx.ExecContext(ctx, `
		UPDATE session SET
			cost = cost + ?,
			tokens_input = tokens_input + ?,
			tokens_output = tokens_output + ?,
			tokens_reasoning = tokens_reasoning + ?,
			tokens_cache_read = tokens_cache_read + ?,
			tokens_cache_write = tokens_cache_write + ?
		WHERE id = ?`,
		cost,
		asInt64(tokens["input"]), asInt64(tokens["output"]), asInt64(tokens["reasoning"]),
		asInt64(cache["read"]), asInt64(cache["write"]),
		sessionID)
	return err
}
