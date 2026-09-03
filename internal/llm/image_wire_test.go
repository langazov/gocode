package llm_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/langazov/gocode-go/internal/llm"
	"github.com/langazov/gocode-go/internal/llm/anthropic"
	"github.com/langazov/gocode-go/internal/llm/gemini"
	"github.com/langazov/gocode-go/internal/llm/openai"
)

// These tests capture the request body each client actually sends, because an
// image part is only useful if it comes out in the shape the provider expects
// — and each of the three spells it differently.

func imageMessage() llm.Message {
	return llm.Message{
		ID:   "m1",
		Role: llm.RoleUser,
		Content: []llm.ContentPart{
			{Type: llm.PartText, Text: "what is this?"},
			{Type: llm.PartImage, Mime: "image/png", Data: "AAAA"},
		},
	}
}

// capture runs a client against a server that records the request body.
func capture(t *testing.T, run func(baseURL string) error) map[string]any {
	t.Helper()
	var body map[string]any
	server := newRecorder(t, &body)
	if err := run(server); err != nil && !strings.Contains(err.Error(), "EOF") {
		// A truncated stream is fine; the request body is what matters.
		t.Logf("stream ended with: %v", err)
	}
	return body
}

func TestAnthropicSendsImageBlock(t *testing.T) {
	body := capture(t, func(baseURL string) error {
		client := anthropic.New("k")
		client.BaseURL = baseURL
		return client.Stream(t.Context(), llm.Request{
			ModelID:  "claude",
			Messages: []llm.Message{imageMessage()},
		}, func(llm.StreamEvent) {})
	})

	messages := body["messages"].([]any)
	content := messages[0].(map[string]any)["content"].([]any)
	if len(content) != 2 {
		t.Fatalf("content has %d blocks, want text + image: %v", len(content), content)
	}
	image := content[1].(map[string]any)
	if image["type"] != "image" {
		t.Errorf("block type = %v, want image", image["type"])
	}
	source := image["source"].(map[string]any)
	if source["type"] != "base64" || source["media_type"] != "image/png" || source["data"] != "AAAA" {
		t.Errorf("source = %v, want a base64 png source", source)
	}
}

// TestAnthropicSendsPDFAsDocument: Anthropic takes a PDF as a document block,
// not an image one.
func TestAnthropicSendsPDFAsDocument(t *testing.T) {
	body := capture(t, func(baseURL string) error {
		client := anthropic.New("k")
		client.BaseURL = baseURL
		return client.Stream(t.Context(), llm.Request{
			ModelID: "claude",
			Messages: []llm.Message{{Role: llm.RoleUser, Content: []llm.ContentPart{
				{Type: llm.PartImage, Mime: "application/pdf", Data: "QkJC"},
			}}},
		}, func(llm.StreamEvent) {})
	})
	content := body["messages"].([]any)[0].(map[string]any)["content"].([]any)
	if got := content[0].(map[string]any)["type"]; got != "document" {
		t.Errorf("block type = %v, want document for a PDF", got)
	}
}

func TestOpenAISendsImageURL(t *testing.T) {
	body := capture(t, func(baseURL string) error {
		client := openai.New("k")
		client.BaseURL = baseURL
		return client.Stream(t.Context(), llm.Request{
			ModelID:  "gpt",
			Messages: []llm.Message{imageMessage()},
		}, func(llm.StreamEvent) {})
	})

	messages := body["messages"].([]any)
	content, ok := messages[0].(map[string]any)["content"].([]any)
	if !ok {
		t.Fatalf("content is %T, want the typed-parts array when an image is present", messages[0].(map[string]any)["content"])
	}
	var sawImage bool
	for _, raw := range content {
		part := raw.(map[string]any)
		if part["type"] == "image_url" {
			sawImage = true
			url := part["image_url"].(map[string]any)["url"].(string)
			if url != "data:image/png;base64,AAAA" {
				t.Errorf("url = %q, want a data URI", url)
			}
		}
	}
	if !sawImage {
		t.Errorf("no image_url part in %v", content)
	}
}

// TestOpenAITextOnlyStaysAString: the array form is only used when needed, so
// ordinary requests are unchanged.
func TestOpenAITextOnlyStaysAString(t *testing.T) {
	body := capture(t, func(baseURL string) error {
		client := openai.New("k")
		client.BaseURL = baseURL
		return client.Stream(t.Context(), llm.Request{
			ModelID:  "gpt",
			Messages: []llm.Message{llm.UserText("m", "hello")},
		}, func(llm.StreamEvent) {})
	})
	content := body["messages"].([]any)[0].(map[string]any)["content"]
	if _, ok := content.(string); !ok {
		t.Errorf("content is %T, want a plain string for a text-only message", content)
	}
}

func TestGeminiSendsInlineData(t *testing.T) {
	body := capture(t, func(baseURL string) error {
		client := gemini.New("k")
		client.BaseURL = baseURL
		return client.Stream(t.Context(), llm.Request{
			ModelID:  "gemini",
			Messages: []llm.Message{imageMessage()},
		}, func(llm.StreamEvent) {})
	})

	parts := body["contents"].([]any)[0].(map[string]any)["parts"].([]any)
	if len(parts) != 2 {
		t.Fatalf("got %d parts, want text + inlineData: %v", len(parts), parts)
	}
	inline, ok := parts[1].(map[string]any)["inlineData"].(map[string]any)
	if !ok {
		t.Fatalf("second part is %v, want inlineData", parts[1])
	}
	if inline["mimeType"] != "image/png" || inline["data"] != "AAAA" {
		t.Errorf("inlineData = %v, want the png payload", inline)
	}
}

var _ = json.Marshal
