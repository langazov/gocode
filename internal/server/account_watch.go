package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"time"

	"github.com/langazov/gocode-go/internal/event"
	"github.com/langazov/gocode-go/internal/gocoder"
)

// AccountUpdated tells interfaces this machine's sign-in changed: someone
// signed in or out — through this server, from the TUI, or with `gocode
// login` — or the stored profile was refreshed. It carries no data;
// interfaces refetch GET /api/account.
var AccountUpdated = event.Definition{Type: "account.updated"}

// accountWatchInterval is how often WatchAccount looks at gocoder.json.
var accountWatchInterval = 2 * time.Second

// WatchAccount starts publishing AccountUpdated whenever gocoder.json
// changes, until ctx is done; it returns at once. The file is the one thing
// every sign-in path shares — the TUI and `gocode login` run in other
// processes and never reach this server — so watching it is what lets a
// connected interface notice them. Sign-ins made through this server's own
// routes land in the same file and are announced the same way.
func (s *Server) WatchAccount(ctx context.Context) {
	if s.Bus == nil {
		return
	}
	watchFile(ctx, gocoder.AccountPath(), accountWatchInterval, func() {
		_, _ = s.Bus.Publish(ctx, AccountUpdated, nil, event.PublishOptions{})
	})
}

// watchFile calls changed, from its own goroutine, each time path's content
// differs from the last look, until ctx is done. The first look happens
// before it returns, so any later write counts as a change. Polling a content
// hash, as the settings-sync watcher does, keeps it dependency-free and
// immune to SaveAccount's write-then-rename.
func watchFile(ctx context.Context, path string, interval time.Duration, changed func()) {
	last := fileDigest(path)
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if digest := fileDigest(path); digest != last {
					last = digest
					changed()
				}
			}
		}
	}()
}

// fileDigest hashes path's content; "" when it can't be read (missing).
func fileDigest(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
