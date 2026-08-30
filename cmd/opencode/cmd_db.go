package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/anomalyco/opencode-go/internal/clix"
	"github.com/anomalyco/opencode-go/internal/db"
)

// dbCommand mirrors DbCommand in cli/cmd/db.ts ("db $0 [query]|path").
func dbCommand() *clix.Command {
	return &clix.Command{
		Name:        "db",
		Describe:    "database tools",
		Positionals: []clix.Positional{{Name: "query", Describe: "SQL query to execute"}},
		Flags: []clix.Flag{
			{Name: "format", Kind: clix.KindString, Default: "tsv", Choices: []string{"json", "tsv"}, Describe: "Output format"},
		},
		Run: runDBQuery,
		Sub: []*clix.Command{
			{Name: "path", Describe: "print the database path", Run: func(a *clix.Args) error {
				fmt.Println(db.Path())
				return nil
			}},
		},
	}
}

func runDBQuery(a *clix.Args) error {
	query := a.PositionalOr("query", "")
	if query == "" {
		cmd := exec.Command("sqlite3", db.Path())
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		return cmd.Run()
	}
	ctx := context.Background()
	database, err := db.OpenDefault(ctx)
	if err != nil {
		return err
	}
	defer database.Close()
	rows, err := database.Query(ctx, query)
	if err != nil {
		return err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return err
	}
	var results []map[string]any
	for rows.Next() {
		values := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range values {
			ptrs[i] = &values[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return err
		}
		row := make(map[string]any, len(cols))
		for i, col := range cols {
			row[col] = values[i]
		}
		results = append(results, row)
	}

	if a.String("format") == "json" {
		data, err := json.MarshalIndent(results, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(data))
		return nil
	}
	if len(results) == 0 {
		return nil
	}
	fmt.Println(strings.Join(cols, "\t"))
	for _, row := range results {
		parts := make([]string, len(cols))
		for i, col := range cols {
			parts[i] = fmt.Sprintf("%v", row[col])
		}
		fmt.Println(strings.Join(parts, "\t"))
	}
	return nil
}
