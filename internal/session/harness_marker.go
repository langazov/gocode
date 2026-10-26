package session

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/langazov/gocode-go/internal/event"
	"github.com/langazov/gocode-go/internal/id"
)

// HarnessInfo describes the harness overlay a runner applies — what the chat
// timeline shows about it (internal/harness Overlay.Describe).
type HarnessInfo struct {
	Dir         string
	Fingerprint string
	AlwaysOn    []string
	Skills      []string
	Agents      []string
	Scripts     int
	Hooks       int
}

// HarnessMessage is a projected harness marker: the overlay a turn ran under
// from here on, or Loaded false when it ran under none after having had one.
type HarnessMessage struct {
	Loaded bool `json:"loaded"`
	// Changed marks a harness that replaced another one mid-session.
	Changed     bool          `json:"changed,omitempty"`
	Dir         string        `json:"dir,omitempty"`
	Fingerprint string        `json:"fingerprint,omitempty"`
	AlwaysOn    []string      `json:"alwaysOn,omitempty"`
	Skills      []string      `json:"skills,omitempty"`
	Agents      []string      `json:"agents,omitempty"`
	Scripts     int           `json:"scripts,omitempty"`
	Hooks       int           `json:"hooks,omitempty"`
	Time        AssistantTime `json:"time"`
}

// noteHarness records a harness marker before a step when the overlay differs
// from the one the session last ran under: the first step under a harness, a
// step after its files changed, or the first one after it was removed. A
// session that never had one gets nothing.
func (r *Runner) noteHarness(ctx context.Context, sessionID string) error {
	if r.Bus == nil || r.DB == nil {
		return nil
	}
	var info HarnessInfo
	loaded := false
	if r.DescribeHarness != nil {
		info, loaded = r.DescribeHarness()
	}
	last, had, err := r.lastHarness(ctx, sessionID)
	if err != nil {
		return err
	}
	switch {
	case loaded && had && last.Loaded && last.Fingerprint == info.Fingerprint:
		return nil
	case !loaded && (!had || !last.Loaded):
		return nil
	}
	messageID, err := id.Ascending(id.KindMessage)
	if err != nil {
		return err
	}
	data := map[string]any{
		"timestamp": time.Now().UnixMilli(),
		"sessionID": sessionID,
		"messageID": messageID,
		"loaded":    loaded,
	}
	if loaded {
		data["dir"], data["fingerprint"] = info.Dir, info.Fingerprint
		if had && last.Loaded {
			data["changed"] = true
		}
		for key, list := range map[string][]string{"alwaysOn": info.AlwaysOn, "skills": info.Skills, "agents": info.Agents} {
			if len(list) > 0 {
				data[key] = list
			}
		}
		if info.Scripts > 0 {
			data["scripts"] = info.Scripts
		}
		if info.Hooks > 0 {
			data["hooks"] = info.Hooks
		}
	}
	_, err = r.Bus.Publish(ctx, HarnessLoaded, data, event.PublishOptions{})
	return err
}

// lastHarness reads the session's newest harness marker.
func (r *Runner) lastHarness(ctx context.Context, sessionID string) (HarnessMessage, bool, error) {
	var raw string
	err := r.DB.QueryRow(ctx, `
		SELECT data FROM session_message WHERE session_id = ? AND type = ?
		ORDER BY seq DESC LIMIT 1`, sessionID, TypeHarness).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return HarnessMessage{}, false, nil
	}
	if err != nil {
		return HarnessMessage{}, false, err
	}
	var message HarnessMessage
	if err := json.Unmarshal([]byte(raw), &message); err != nil {
		// An unreadable marker is treated as none, so the next one is
		// written rather than the turn failing on a display record.
		return HarnessMessage{}, false, nil
	}
	return message, true, nil
}
