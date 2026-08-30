package main

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/anomalyco/opencode-go/internal/clix"
	"github.com/anomalyco/opencode-go/internal/db"
	"github.com/anomalyco/opencode-go/internal/event"
	"github.com/anomalyco/opencode-go/internal/session"
)

// sessionCommand mirrors SessionCommand in cli/cmd/session.ts
// ("session list|delete"). Pagination through a pager (less) is skipped;
// output always goes straight to stdout.
func sessionCommand() *clix.Command {
	return &clix.Command{
		Name:     "session",
		Describe: "manage sessions",
		Demand:   true,
		Sub: []*clix.Command{
			{
				Name:        "delete",
				Describe:    "delete a session",
				Positionals: []clix.Positional{{Name: "sessionID", Required: true, Describe: "session ID to delete"}},
				Run:         runSessionDelete,
			},
			{
				Name:     "list",
				Describe: "list sessions",
				Flags: []clix.Flag{
					{Name: "max-count", Aliases: []string{"n"}, Kind: clix.KindNumber, Describe: "limit to N most recent sessions"},
					{Name: "format", Kind: clix.KindString, Default: "table", Choices: []string{"table", "json"}, Describe: "output format"},
				},
				Run: runSessionList,
			},
		},
	}
}

func openSessionService(ctx context.Context) (*session.Service, func(), error) {
	database, err := db.OpenDefault(ctx)
	if err != nil {
		return nil, nil, err
	}
	bus := event.NewBus(database)
	return session.NewService(database, bus), func() { database.Close() }, nil
}

func runSessionDelete(a *clix.Args) error {
	ctx := context.Background()
	svc, closeFn, err := openSessionService(ctx)
	if err != nil {
		return err
	}
	defer closeFn()
	sessionID := a.Pos["sessionID"]
	if err := svc.Delete(ctx, sessionID); err != nil {
		return fmt.Errorf("Session not found: %s", sessionID)
	}
	fmt.Printf("Session %s deleted\n", sessionID)
	return nil
}

func runSessionList(a *clix.Args) error {
	ctx := context.Background()
	svc, closeFn, err := openSessionService(ctx)
	if err != nil {
		return err
	}
	defer closeFn()
	sessions, err := svc.List(ctx)
	if err != nil {
		return err
	}
	sort.Slice(sessions, func(i, j int) bool { return sessions[i].TimeUpdated > sessions[j].TimeUpdated })
	if limit := a.IntOr("max-count", 0); limit > 0 && limit < len(sessions) {
		sessions = sessions[:limit]
	}
	if len(sessions) == 0 {
		return nil
	}
	if a.String("format") == "json" {
		type row struct {
			ID        string `json:"id"`
			Title     string `json:"title"`
			Updated   int64  `json:"updated"`
			Created   int64  `json:"created"`
			ProjectID string `json:"projectId"`
			Directory string `json:"directory"`
		}
		out := make([]row, 0, len(sessions))
		for _, s := range sessions {
			out = append(out, row{s.ID, s.Title, s.TimeUpdated, s.TimeCreated, s.ProjectID, s.Directory})
		}
		data, err := json.MarshalIndent(out, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(data))
		return nil
	}

	maxID, maxTitle := 20, 25
	for _, s := range sessions {
		if len(s.ID) > maxID {
			maxID = len(s.ID)
		}
		if len(s.Title) > maxTitle {
			maxTitle = len(s.Title)
		}
	}
	header := fmt.Sprintf("Session ID%s  Title%s  Updated", pad("", maxID-10), pad("", maxTitle-5))
	fmt.Println(header)
	fmt.Println(repeat("─", len(header)))
	for _, s := range sessions {
		title := truncate(s.Title, maxTitle)
		fmt.Printf("%s  %s  %s\n", padRight(s.ID, maxID), padRight(title, maxTitle), time.UnixMilli(s.TimeUpdated).Format("2006-01-02 15:04"))
	}
	return nil
}

func pad(s string, n int) string {
	if n <= 0 {
		return s
	}
	return s + repeat(" ", n)
}

func padRight(s string, n int) string {
	for len(s) < n {
		s += " "
	}
	return s
}

func repeat(s string, n int) string {
	if n <= 0 {
		return ""
	}
	out := make([]byte, 0, n*len(s))
	for i := 0; i < n; i++ {
		out = append(out, s...)
	}
	return string(out)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n <= 3 {
		return s[:n]
	}
	return s[:n-3] + "..."
}
