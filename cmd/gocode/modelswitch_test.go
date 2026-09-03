package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/langazov/gocode-go/internal/session"
)

// anthropic SSE for the minimax-style provider; openai SSE for the zai-style.
func sseServer(t *testing.T, text string, hits *atomic.Int32) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		if strings.Contains(r.URL.Path, "/messages") {
			for _, e := range []map[string]any{
				{"type": "message_start", "message": map[string]any{"id": "m", "model": "x", "usage": map[string]any{"input_tokens": 1, "output_tokens": 1}}},
				{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "text", "text": ""}},
				{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "text_delta", "text": text}},
				{"type": "content_block_stop", "index": 0},
				{"type": "message_delta", "delta": map[string]any{"stop_reason": "end_turn"}, "usage": map[string]any{"output_tokens": 1}},
				{"type": "message_stop"},
			} {
				encoded, _ := json.Marshal(e)
				w.Write([]byte("data: " + string(encoded) + "\n\n"))
			}
			return
		}
		for _, e := range []map[string]any{
			{"choices": []map[string]any{{"delta": map[string]any{"content": text}}}},
			{"choices": []map[string]any{{"delta": map[string]any{}, "finish_reason": "stop"}}, "usage": map[string]any{"prompt_tokens": 1, "completion_tokens": 1}},
		} {
			encoded, _ := json.Marshal(e)
			w.Write([]byte("data: " + string(encoded) + "\n\n"))
		}
		w.Write([]byte("data: [DONE]\n\n"))
	}))
}

// TestModelSwitchRoutesToNewProvider is the regression for "switching models
// doesn't work": after SetModel, the next prompt must hit the new provider.
func TestModelSwitchRoutesToNewProvider(t *testing.T) {
	var minimaxHits, zaiHits atomic.Int32
	minimaxSrv := sseServer(t, "minimax-reply", &minimaxHits)
	defer minimaxSrv.Close()
	zaiSrv := sseServer(t, "zai-reply", &zaiHits)
	defer zaiSrv.Close()

	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("GOCODE_DISABLE_MODELS_FETCH", "true")
	t.Setenv("GOCODE_AUTH_CONTENT", `{
		"minimax-coding-plan":{"type":"api","key":"mm-key"},
		"zai-coding-plan":{"type":"api","key":"zai-key"}
	}`)
	t.Setenv("GOCODE_CONFIG_CONTENT", `{"model":"minimax-coding-plan/MiniMax-M3"}`)

	catalogDir := t.TempDir()
	catalogPath := filepath.Join(catalogDir, "models.json")
	catalog := `{
	  "minimax-coding-plan": {
	    "id": "minimax-coding-plan", "npm": "@ai-sdk/anthropic",
	    "api": "` + minimaxSrv.URL + `",
	    "env": ["MINIMAX_API_KEY"],
	    "models": {"MiniMax-M3": {"id": "MiniMax-M3", "name": "M3", "release_date": "2026-01-01", "attachment": false, "reasoning": false, "temperature": true, "tool_call": true, "limit": {"context": 100000, "output": 10000}}}
	  },
	  "zai-coding-plan": {
	    "id": "zai-coding-plan", "npm": "@ai-sdk/openai-compatible",
	    "api": "` + zaiSrv.URL + `",
	    "env": ["ZHIPU_API_KEY"],
	    "models": {"glm-5.3": {"id": "glm-5.3", "name": "GLM 5.3", "release_date": "2026-01-01", "attachment": false, "reasoning": false, "temperature": false, "tool_call": true, "limit": {"context": 100000, "output": 10000}}}
	  }
	}`
	if err := osWriteFile(catalogPath, catalog); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GOCODE_MODELS_PATH", catalogPath)

	stack, err := bootStack(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}

	info, err := stack.Service.Create(context.Background(), session.CreateInput{Directory: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}

	// 1. Default model from config: prompt must hit minimax.
	if _, err := stack.Service.Prompt(context.Background(), info.ID, "first", session.DeliverySteer); err != nil {
		t.Fatal(err)
	}
	waitForHit(t, &minimaxHits, 1)
	if zaiHits.Load() != 0 {
		t.Fatalf("zai should not be hit before the switch, got %d", zaiHits.Load())
	}

	// 2. Switch models via the dialog path (SetModel), prompt again.
	if err := stack.Service.SetModel(context.Background(), info.ID, session.ModelRef{ProviderID: "zai-coding-plan", ID: "glm-5.3"}); err != nil {
		t.Fatal(err)
	}
	if _, err := stack.Service.Prompt(context.Background(), info.ID, "second", session.DeliverySteer); err != nil {
		t.Fatal(err)
	}
	waitForHit(t, &zaiHits, 1)
}

func waitForHit(t *testing.T, counter *atomic.Int32, want int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if counter.Load() >= int32(want) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("provider was not hit %d time(s)", want)
}

func osWriteFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o644)
}
