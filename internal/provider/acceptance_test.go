package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/langazov/gocode-go/internal/config"
	"github.com/langazov/gocode-go/internal/llm"
)

// TestAcceptanceConfigProvider is the P0 acceptance criterion: a config
// defining a custom OpenAI-compatible provider (apiKey+baseURL+models) works
// with zero environment variables.
func TestAcceptanceConfigProvider(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer cfg-key-123" {
			t.Errorf("Authorization = %q", got)
		}
		if !strings.Contains(r.URL.Path, "chat/completions") {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"works\"}}]}\n\n"))
		w.Write([]byte("data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":1}}\n\n"))
		w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer srv.Close()

	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	os.Unsetenv("ZHIPUAI_API_KEY")
	os.Unsetenv("OPENAI_API_KEY")
	os.Unsetenv("ANTHROPIC_API_KEY")
	t.Setenv("GOCODE_CONFIG_CONTENT", `{
		"provider": {
			"zhipuai": {
				"options": {"apiKey": "cfg-key-123", "baseURL": "`+srv.URL+`"},
				"models": {"glm-5.3-flash": {"name": "GLM 5.3 Flash"}}
			}
		}
	}`)

	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	streamClient, err := FromConfig(context.Background(), "zhipuai", cfg)
	if err != nil {
		t.Fatal(err)
	}

	var text strings.Builder
	err = streamClient.Stream(context.Background(), llm.Request{
		ProviderID: "zhipuai",
		ModelID:    "glm-5.3-flash",
		Messages:   []llm.Message{llm.UserText("m", "hi")},
	}, func(event llm.StreamEvent) {
		if event.Type == llm.EventTextDelta {
			text.WriteString(event.Text)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if text.String() != "works" {
		t.Fatalf("expected streamed text, got %q", text.String())
	}
}
