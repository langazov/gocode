package main

import (
	"context"
	"testing"

	"github.com/anomalyco/opencode-go/internal/clix"
	"github.com/anomalyco/opencode-go/internal/session"
)

func testEnv(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("OPENCODE_DISABLE_MODELS_FETCH", "true")
	t.Setenv("OPENCODE_CONFIG_CONTENT", `{"model":"faketest/fake-model"}`)
}

// TestResolveExistingSessionBySessionFlag is the regression for
// "./opencode -s <sessionID> is not resuming the session": resolveExistingSession
// (shared by the root/tui $0 command and "run") must resolve a real
// --session id created against the actual DB, not just a mocked HTTP client.
func TestResolveExistingSessionBySessionFlag(t *testing.T) {
	testEnv(t)
	ctx := context.Background()
	stack, err := bootStack(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	created, err := stack.Service.Create(ctx, session.CreateInput{Directory: t.TempDir(), Title: "resume me"})
	if err != nil {
		t.Fatal(err)
	}

	args := &clix.Args{Strings: map[string]string{"session": created.ID}, Bools: map[string]bool{}}
	id, ok, err := resolveExistingSession(ctx, stack, args)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("expected resolveExistingSession to report ok=true for an explicit --session")
	}
	if id != created.ID {
		t.Fatalf("expected resolved session %s, got %s", created.ID, id)
	}
}

// TestResolveExistingSessionUnknownID errors instead of silently starting a
// fresh session — matching RunCommand's "Session not found" die().
func TestResolveExistingSessionUnknownID(t *testing.T) {
	testEnv(t)
	ctx := context.Background()
	stack, err := bootStack(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	args := &clix.Args{Strings: map[string]string{"session": "ses_does_not_exist"}, Bools: map[string]bool{}}
	if _, _, err := resolveExistingSession(ctx, stack, args); err == nil {
		t.Fatal("expected an error for an unknown session id")
	}
}

// TestResolveExistingSessionNoFlagsIsNotOK confirms tui/attach fall back to
// their own default (an empty home screen) rather than resuming/creating
// anything when neither --session nor --continue was passed.
func TestResolveExistingSessionNoFlagsIsNotOK(t *testing.T) {
	testEnv(t)
	ctx := context.Background()
	stack, err := bootStack(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	args := &clix.Args{Strings: map[string]string{}, Bools: map[string]bool{}}
	id, ok, err := resolveExistingSession(ctx, stack, args)
	if err != nil {
		t.Fatal(err)
	}
	if ok || id != "" {
		t.Fatalf("expected ok=false and empty id with no session flags, got ok=%v id=%q", ok, id)
	}
}
