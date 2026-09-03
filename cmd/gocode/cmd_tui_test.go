package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/langazov/gocode-go/internal/clix"
	"github.com/langazov/gocode-go/internal/session"
)

// testCatalog pins a fixture catalog for a test, the pattern cmd_run_test and
// modelswitch_test already use.
//
// Pinning is not optional here. provider.Fallback scans the whole catalog for
// a provider whose env vars happen to be set, so with the real catalog in
// play any credential in the developer's own shell — GITHUB_TOKEN is the
// common one, which the catalog lists as github-copilot's key — silently
// becomes a fallback candidate and switches the model out from under the
// assertion. These tests are about resolution order, so the candidate set has
// to be fixed rather than inherited from whoever is running them.
func testCatalog(t *testing.T) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "models.json")
	catalog := `{
		"faketest":            {"id":"faketest","npm":"@ai-sdk/openai-compatible","api":"http://127.0.0.1:1","env":["FAKETEST_API_KEY"],"name":"Fake","models":{"fake-model":{"id":"fake-model","name":"Fake","release_date":"2026-01-01","attachment":false,"reasoning":false,"temperature":true,"tool_call":true,"limit":{"context":128000,"output":8192}}}},
		"anthropic":           {"id":"anthropic","npm":"@ai-sdk/anthropic","env":["ANTHROPIC_API_KEY"],"name":"Anthropic","models":{"claude-sonnet-4-5":{"id":"claude-sonnet-4-5","name":"Sonnet","release_date":"2026-01-01","attachment":true,"reasoning":true,"temperature":true,"tool_call":true,"limit":{"context":200000,"output":64000}}}},
		"zhipuai":             {"id":"zhipuai","npm":"@ai-sdk/openai-compatible","api":"https://open.bigmodel.cn/api/paas/v4","env":["ZHIPU_API_KEY"],"name":"Zhipu","models":{"glm-5.3-flash":{"id":"glm-5.3-flash","name":"GLM","release_date":"2026-01-01","attachment":false,"reasoning":false,"temperature":true,"tool_call":true,"limit":{"context":128000,"output":8192}}}},
		"zai-coding-plan":     {"id":"zai-coding-plan","npm":"@ai-sdk/openai-compatible","api":"https://api.z.ai/api/coding/paas/v4","env":["ZHIPU_API_KEY"],"name":"Z.ai","models":{"glm-5.3":{"id":"glm-5.3","name":"GLM","release_date":"2026-01-01","attachment":false,"reasoning":false,"temperature":true,"tool_call":true,"limit":{"context":128000,"output":8192}}}},
		"minimax-coding-plan": {"id":"minimax-coding-plan","npm":"@ai-sdk/anthropic","api":"https://api.minimax.io/anthropic/v1","env":["MINIMAX_API_KEY"],"name":"MiniMax","models":{"MiniMax-M3":{"id":"MiniMax-M3","name":"M3","release_date":"2026-01-01","attachment":false,"reasoning":false,"temperature":true,"tool_call":true,"limit":{"context":128000,"output":8192}}}}
	}`
	if err := os.WriteFile(path, []byte(catalog), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("GOCODE_DISABLE_MODELS_FETCH", "true")
	t.Setenv("GOCODE_MODELS_PATH", path)
}

func testEnv(t *testing.T) {
	t.Helper()
	testCatalog(t)
	t.Setenv("GOCODE_CONFIG_CONTENT", `{"model":"faketest/fake-model"}`)
}

// TestResolveExistingSessionBySessionFlag is the regression for
// "./gocode -s <sessionID> is not resuming the session": resolveExistingSession
// (shared by the root/tui $0 command and "run") must resolve a real
// --session id created against the actual DB, not just a mocked HTTP client.
func TestResolveExistingSessionBySessionFlag(t *testing.T) {
	testEnv(t)
	ctx := context.Background()
	stack := bootStackT(t, ctx, "")
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
	stack := bootStackT(t, ctx, "")
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
	stack := bootStackT(t, ctx, "")
	args := &clix.Args{Strings: map[string]string{}, Bools: map[string]bool{}}
	id, ok, err := resolveExistingSession(ctx, stack, args)
	if err != nil {
		t.Fatal(err)
	}
	if ok || id != "" {
		t.Fatalf("expected ok=false and empty id with no session flags, got ok=%v id=%q", ok, id)
	}
}
