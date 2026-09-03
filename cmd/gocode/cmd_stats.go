package main

import (
	"context"
	"fmt"
	"time"

	"github.com/langazov/gocode-go/internal/clix"
	"github.com/langazov/gocode-go/internal/db"
)

// statsCommand mirrors StatsCommand in cli/cmd/stats.ts. The overview and
// cost/token totals are computed for real from the session table's
// aggregate columns (cost, tokens_*); the per-model and per-tool breakdown
// sections from the TS version need per-message token/tool-part data that
// the Go session schema doesn't project yet, so they're reported as
// unavailable rather than silently wrong (see specs/go-port-gaps.md).
func statsCommand() *clix.Command {
	return &clix.Command{
		Name:     "stats",
		Describe: "show token usage and cost statistics",
		Flags: []clix.Flag{
			{Name: "days", Kind: clix.KindNumber, Describe: "show stats for the last N days (default: all time)"},
			{Name: "tools", Kind: clix.KindNumber, Describe: "number of tools to show (default: all)"},
			{Name: "models", Kind: clix.KindString, Describe: "show model statistics (default: hidden)"},
			{Name: "project", Kind: clix.KindString, Describe: "filter by project (default: all projects, empty string: current project)"},
		},
		Run: runStats,
	}
}

func runStats(a *clix.Args) error {
	ctx := context.Background()
	database, err := db.OpenDefault(ctx)
	if err != nil {
		return err
	}
	defer database.Close()

	query := `SELECT COUNT(*), COALESCE(SUM(cost),0), COALESCE(SUM(tokens_input),0),
		COALESCE(SUM(tokens_output),0), COALESCE(SUM(tokens_reasoning),0),
		COALESCE(SUM(tokens_cache_read),0), COALESCE(SUM(tokens_cache_write),0)
		FROM session WHERE 1=1`
	args := []any{}
	if a.Has("days") {
		days := a.IntOr("days", 0)
		cutoff := time.Now().AddDate(0, 0, -days).UnixMilli()
		if days == 0 {
			now := time.Now()
			cutoff = time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()).UnixMilli()
		}
		query += " AND time_updated >= ?"
		args = append(args, cutoff)
	}
	if a.Has("project") {
		project := a.String("project")
		query += " AND project_id = ?"
		args = append(args, project)
	}

	var count int
	var cost float64
	var input, output, reasoning, cacheRead, cacheWrite int64
	row := database.QueryRow(ctx, query, args...)
	if err := row.Scan(&count, &cost, &input, &output, &reasoning, &cacheRead, &cacheWrite); err != nil {
		return err
	}

	days := 1
	if a.Has("days") {
		days = a.IntOr("days", 0)
		if days == 0 {
			days = 1
		}
	}

	fmt.Println("OVERVIEW")
	fmt.Printf("Sessions: %d\n", count)
	fmt.Printf("Days: %d\n", days)
	fmt.Println()
	fmt.Println("COST & TOKENS")
	fmt.Printf("Total Cost: $%.2f\n", cost)
	if days > 0 {
		fmt.Printf("Avg Cost/Day: $%.2f\n", cost/float64(days))
	}
	fmt.Printf("Input: %d\n", input)
	fmt.Printf("Output: %d\n", output+reasoning)
	fmt.Printf("Cache Read: %d\n", cacheRead)
	fmt.Printf("Cache Write: %d\n", cacheWrite)

	if a.Has("models") {
		fmt.Println("\nMODEL USAGE: not yet available in the Go port (per-message model tracking is not projected)")
	}
	fmt.Println("\nTOOL USAGE: not yet available in the Go port (per-message tool-part tracking is not projected)")
	return nil
}
