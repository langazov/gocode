package main

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/langazov/gocode-go/internal/clix"
	"github.com/langazov/gocode-go/internal/session"
)

// exportData is the Go port's session export shape: the session row plus its
// raw stored message rows. This intentionally differs from the TS
// export/import JSON shape (packages/opencode/src/cli/cmd/export.ts), which
// serializes the richer v1 message/part schema Go's session store doesn't
// keep; see importCommand for the matching round-trip format.
type exportData struct {
	Info     session.Info            `json:"info"`
	Messages []session.StoredMessage `json:"messages"`
}

// exportCommand mirrors ExportCommand in cli/cmd/export.ts
// ("export [sessionID]"). Interactive session picking (no sessionID given)
// falls back to "most recently updated", since there is no TTY prompt
// library wired into the Go CLI yet.
func exportCommand() *clix.Command {
	return &clix.Command{
		Name:        "export",
		Describe:    "export session data as JSON",
		Positionals: []clix.Positional{{Name: "sessionID", Describe: "session id to export"}},
		Flags: []clix.Flag{
			{Name: "sanitize", Kind: clix.KindBool, Describe: "redact sensitive transcript and file data"},
		},
		Run: runExport,
	}
}

func runExport(a *clix.Args) error {
	ctx := context.Background()
	svc, closeFn, err := openSessionService(ctx)
	if err != nil {
		return err
	}
	defer closeFn()

	sessionID := a.PositionalOr("sessionID", "")
	if sessionID == "" {
		sessions, err := svc.List(ctx)
		if err != nil {
			return err
		}
		if len(sessions) == 0 {
			return &usageError{msg: "No sessions found"}
		}
		sort.Slice(sessions, func(i, j int) bool { return sessions[i].TimeUpdated > sessions[j].TimeUpdated })
		sessionID = sessions[0].ID
	}

	info, err := svc.Get(ctx, sessionID)
	if err != nil || info == nil {
		return fmt.Errorf("Session not found: %s", sessionID)
	}
	messages, err := svc.Messages.List(ctx, sessionID)
	if err != nil {
		return err
	}
	out := exportData{Info: *info, Messages: messages}
	if a.Bool("sanitize") {
		out.Info.Title = "[redacted:session-title:" + info.ID + "]"
		out.Info.Directory = "[redacted:session-directory:" + info.ID + "]"
		for i := range out.Messages {
			out.Messages[i].Data = json.RawMessage(`{"redacted":true}`)
		}
	}
	data, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(data))
	return nil
}
