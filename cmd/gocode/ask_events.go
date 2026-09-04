package main

import (
	"context"

	"github.com/langazov/gocode-go/internal/event"
)

// publishAsk announces that a run parked on the user, or stopped being parked.
//
// The payload is deliberately just the ids: the pending request itself lives
// in an in-memory map that the HTTP endpoints already serve, and duplicating
// it into a live event would give clients a second, staler copy to reconcile.
// sessionID is the field toEventWire routes on, so it has to be present for
// the event to reach a session-filtered subscriber at all.
//
// Hooks fire while the asking goroutine is blocked, so a failure here must not
// take the ask down with it — a dropped nudge degrades to the reconciliation
// tick, which is where this started.
func publishAsk(ctx context.Context, bus *event.Bus, def event.Definition, sessionID, requestID string) {
	if bus == nil || sessionID == "" {
		return
	}
	_, _ = bus.Publish(context.WithoutCancel(ctx), def, map[string]any{
		"sessionID": sessionID,
		"requestID": requestID,
	}, event.PublishOptions{})
}
