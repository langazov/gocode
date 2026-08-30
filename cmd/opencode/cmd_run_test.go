package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anomalyco/opencode-go/internal/clix"
	"github.com/anomalyco/opencode-go/internal/session"
)

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	original := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	fn()
	w.Close()
	os.Stdout = original
	data, _ := io.ReadAll(r)
	return string(data)
}

// TestRunCommandNonInteractive is an end-to-end smoke test for "opencode run
// <message>": it must create a session, admit the prompt, drive it to
// completion via the shared Execution coordinator, and print the reply.
func TestRunCommandNonInteractive(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, e := range []map[string]any{
			{"choices": []map[string]any{{"delta": map[string]any{"content": "hello from the fake model"}}}},
			{"choices": []map[string]any{{"delta": map[string]any{}, "finish_reason": "stop"}}, "usage": map[string]any{"prompt_tokens": 3, "completion_tokens": 5}},
		} {
			encoded, _ := json.Marshal(e)
			w.Write([]byte("data: " + string(encoded) + "\n\n"))
		}
		w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer srv.Close()

	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("OPENCODE_DISABLE_MODELS_FETCH", "true")
	t.Setenv("OPENCODE_AUTH_CONTENT", `{"faketest":{"type":"api","key":"k"}}`)
	t.Setenv("OPENCODE_CONFIG_CONTENT", `{"model":"faketest/fake-model"}`)

	catalogDir := t.TempDir()
	catalogPath := filepath.Join(catalogDir, "models.json")
	catalog := `{"faketest":{"id":"faketest","npm":"@ai-sdk/openai-compatible","api":"` + srv.URL + `","env":["FAKETEST_API_KEY"],
	  "models":{"fake-model":{"id":"fake-model","name":"Fake","release_date":"2026-01-01","attachment":false,"reasoning":false,"temperature":false,"tool_call":true,"limit":{"context":100000,"output":10000}}}}}`
	if err := os.WriteFile(catalogPath, []byte(catalog), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPENCODE_MODELS_PATH", catalogPath)

	workdir := t.TempDir()
	original, _ := os.Getwd()
	t.Cleanup(func() { os.Chdir(original) })
	if err := os.Chdir(workdir); err != nil {
		t.Fatal(err)
	}

	cmd := runCommand()
	args, rest := parseTestArgs(t, cmd, []string{"say", "hi"})
	if len(rest) != 0 {
		t.Fatalf("unexpected leftover: %v", rest)
	}

	stdout := captureStdout(t, func() {
		if err := cmd.Run(args); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(stdout, "hello from the fake model") {
		t.Fatalf("expected reply text in output, got: %q", stdout)
	}
}

// TestRunCommandVariantEnablesThinking is the regression for "I can't see
// thinking in the go port": "opencode run --variant high" against an
// Anthropic-protocol model must (a) send a "thinking" request field derived
// from the model's catalog reasoning_options, and (b) persist the model's
// thinking-delta stream as a "reasoning" content part on the assistant
// message — the same part the TUI's reasoningBlock renders.
func TestRunCommandVariantEnablesThinking(t *testing.T) {
	var gotThinking map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Thinking map[string]any `json:"thinking"`
		}
		data, _ := io.ReadAll(r.Body)
		json.Unmarshal(data, &body)
		gotThinking = body.Thinking

		w.Header().Set("Content-Type", "text/event-stream")
		for _, e := range []map[string]any{
			{"type": "message_start", "message": map[string]any{"id": "m", "model": "x", "usage": map[string]any{"input_tokens": 1, "output_tokens": 1}}},
			{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "thinking", "thinking": ""}},
			{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "thinking_delta", "thinking": "Let me work through this."}},
			{"type": "content_block_stop", "index": 0},
			{"type": "content_block_start", "index": 1, "content_block": map[string]any{"type": "text", "text": ""}},
			{"type": "content_block_delta", "index": 1, "delta": map[string]any{"type": "text_delta", "text": "42"}},
			{"type": "content_block_stop", "index": 1},
			{"type": "message_delta", "delta": map[string]any{"stop_reason": "end_turn"}, "usage": map[string]any{"output_tokens": 5}},
			{"type": "message_stop"},
		} {
			encoded, _ := json.Marshal(e)
			w.Write([]byte("data: " + string(encoded) + "\n\n"))
		}
	}))
	defer srv.Close()

	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("OPENCODE_DISABLE_MODELS_FETCH", "true")
	t.Setenv("OPENCODE_AUTH_CONTENT", `{"faketest":{"type":"api","key":"k"}}`)
	t.Setenv("OPENCODE_CONFIG_CONTENT", `{"model":"faketest/thinking-model"}`)

	catalogDir := t.TempDir()
	catalogPath := filepath.Join(catalogDir, "models.json")
	catalog := `{"faketest":{"id":"faketest","npm":"@ai-sdk/anthropic","api":"` + srv.URL + `","env":["FAKETEST_API_KEY"],
	  "models":{"thinking-model":{"id":"thinking-model","name":"Thinking","release_date":"2026-01-01","attachment":false,"reasoning":true,"temperature":false,"tool_call":true,
	    "reasoning_options":[{"type":"budget_tokens","min":1024}],"limit":{"context":100000,"output":64000}}}}}`
	if err := os.WriteFile(catalogPath, []byte(catalog), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPENCODE_MODELS_PATH", catalogPath)

	workdir := t.TempDir()
	original, _ := os.Getwd()
	t.Cleanup(func() { os.Chdir(original) })
	if err := os.Chdir(workdir); err != nil {
		t.Fatal(err)
	}

	cmd := runCommand()
	args, _ := parseTestArgs(t, cmd, []string{"--variant", "high", "what", "is", "6*7"})

	stdout := captureStdout(t, func() {
		if err := cmd.Run(args); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(stdout, "42") {
		t.Fatalf("expected reply text in output, got: %q", stdout)
	}

	if gotThinking == nil {
		t.Fatal("expected the request to include a \"thinking\" field")
	}
	if gotThinking["type"] != "enabled" {
		t.Fatalf("thinking.type = %v, want enabled", gotThinking["type"])
	}
	if budget, ok := gotThinking["budget_tokens"].(float64); !ok || budget != 16000 {
		t.Fatalf("thinking.budget_tokens = %v, want 16000 (half of 31999, the min(64000-1, 32000-1) cap)", gotThinking["budget_tokens"])
	}

	// The reasoning text must be retrievable from storage — this is exactly
	// what the TUI's reasoningBlock reads to render "Thought: ...".
	ctx := context.Background()
	stack, err := bootStack(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	sessions, err := stack.Service.List(ctx)
	if err != nil || len(sessions) == 0 {
		t.Fatalf("expected a session to exist, err=%v sessions=%v", err, sessions)
	}
	messages, err := stack.Service.Messages.List(ctx, sessions[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, msg := range messages {
		if msg.Type != "assistant" {
			continue
		}
		assistant, err := session.DecodeAssistant(msg.Data)
		if err != nil {
			continue
		}
		for _, part := range assistant.Content {
			if part.Type == "reasoning" && part.Text == "Let me work through this." {
				found = true
			}
		}
	}
	if !found {
		t.Fatal("expected a reasoning content part with the streamed thinking text")
	}
}

// parseTestArgs is a minimal stand-in for clix.Run() that only binds the
// command's own positionals/flags (no subcommand dispatch), for unit-testing
// a single leaf command's Run function directly.
func parseTestArgs(t *testing.T, cmd *clix.Command, tokens []string) (*clix.Args, []string) {
	t.Helper()
	root := &clix.Command{Name: "test", Positionals: cmd.Positionals, Flags: cmd.Flags, AllowExtra: cmd.AllowExtra, Run: func(a *clix.Args) error {
		return nil
	}}
	var captured *clix.Args
	root.Run = func(a *clix.Args) error { captured = a; return nil }
	if err := clix.Run(root, tokens); err != nil {
		t.Fatal(err)
	}
	return captured, nil
}
