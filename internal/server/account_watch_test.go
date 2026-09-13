package server

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/langazov/gocode-go/internal/db"
	"github.com/langazov/gocode-go/internal/event"
	"github.com/langazov/gocode-go/internal/gocoder"
)

func TestWatchAccountAnnouncesSignInAndSignOut(t *testing.T) {
	accountEnv(t, "http://gocoder.invalid")
	database, err := db.OpenAndMigrate(context.Background(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	bus := event.NewBus(database)
	events, unsubscribe := bus.Subscribe(8)
	defer unsubscribe()

	previous := accountWatchInterval
	accountWatchInterval = 10 * time.Millisecond
	defer func() { accountWatchInterval = previous }()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	(&Server{Bus: bus}).WatchAccount(ctx)

	expectAnnouncement := func(what string) {
		t.Helper()
		select {
		case payload := <-events:
			if payload.Type != AccountUpdated.Type {
				t.Fatalf("%s published %q, want %q", what, payload.Type, AccountUpdated.Type)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%s was not announced", what)
		}
	}

	// As another process (the TUI, `gocode login`) would: straight to disk.
	if err := gocoder.SaveAccount(&gocoder.Account{Email: "a@b.c", Key: "gk_x"}); err != nil {
		t.Fatal(err)
	}
	expectAnnouncement("sign-in")
	if err := gocoder.RemoveAccount(); err != nil {
		t.Fatal(err)
	}
	expectAnnouncement("sign-out")

	select {
	case payload := <-events:
		t.Fatalf("unexpected event %q with no change on disk", payload.Type)
	case <-time.After(50 * time.Millisecond):
	}
}
