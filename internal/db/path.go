package db

import (
	"context"
	"os"
	"path/filepath"
	"regexp"

	"github.com/anomalyco/opencode-go/internal/flag"
	"github.com/anomalyco/opencode-go/internal/global"
	"github.com/anomalyco/opencode-go/internal/installation"
)

var channelSanitizer = regexp.MustCompile(`[^a-zA-Z0-9._-]`)

// Path resolves the database file, matching database.ts path().
func Path() string {
	if value := flag.Db(); value != "" {
		if value == ":memory:" || filepath.IsAbs(value) {
			return value
		}
		return filepath.Join(global.Resolve().Data, value)
	}
	data := global.Resolve().Data
	switch installation.Channel {
	case "latest", "beta", "prod":
		return filepath.Join(data, "opencode.db")
	}
	if v := os.Getenv("OPENCODE_DISABLE_CHANNEL_DB"); v == "1" || v == "true" {
		return filepath.Join(data, "opencode.db")
	}
	channel := channelSanitizer.ReplaceAllString(installation.Channel, "-")
	return filepath.Join(data, "opencode-"+channel+".db")
}

// OpenDefault opens the database at Path() and applies migrations.
func OpenDefault(ctx context.Context) (*DB, error) {
	return OpenAndMigrate(ctx, Path())
}

func OpenAndMigrate(ctx context.Context, path string) (*DB, error) {
	db, err := Open(path)
	if err != nil {
		return nil, err
	}
	if err := db.Apply(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}
