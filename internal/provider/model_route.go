package provider

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/langazov/gocode-go/internal/llm"
	"github.com/langazov/gocode-go/internal/llm/anthropic"
	"github.com/langazov/gocode-go/internal/llm/gemini"
	"github.com/langazov/gocode-go/internal/llm/openai"
	"github.com/langazov/gocode-go/internal/llm/openairesponses"
	"github.com/langazov/gocode-go/internal/modelsdev"
)

// modelsNeedRouting reports whether any model in a catalog declares a
// per-model provider override, the signal that Client() needs to wrap its
// default client rather than hand it out directly.
func modelsNeedRouting(models map[string]modelsdev.Model) bool {
	for _, model := range models {
		if model.Provider != nil && model.Provider.NPM != "" {
			return true
		}
	}
	return false
}

// modelRoutedClient wraps a provider's default client for a catalog where
// some models proxy to a different upstream API than the rest — Zen's own
// account config is the motivating case: most of its models are plain
// OpenAI-compatible Chat Completions, but Claude models go to Anthropic's
// Messages API, Gemini models to Google's Generative Language API, and
// GPT-5-family/Grok/Muse-Spark models to OpenAI's Responses API — three
// distinct wire protocols the account's own device-flow token authenticates
// against just as validly as the default endpoint, once the request is
// shaped correctly and sent to the matching URL. Sending them all to Zen's
// default Chat Completions endpoint instead produces a bare "Endpoint is
// unavailable" from the gateway with no indication that the model needed
// somewhere else entirely.
type modelRoutedClient struct {
	resolved *Resolved
	fallback llm.StreamClient

	mu      sync.Mutex
	clients map[string]llm.StreamClient // keyed by NPM package + base URL
}

func (m *modelRoutedClient) Stream(ctx context.Context, request llm.Request, emit func(llm.StreamEvent)) error {
	model, ok := m.resolved.Models[request.ModelID]
	if !ok || model.Provider == nil || model.Provider.NPM == "" {
		return m.fallback.Stream(ctx, request, emit)
	}
	client, err := m.clientFor(model.Provider)
	if err != nil {
		emit(llm.StreamEvent{Type: llm.EventProviderError, Error: err})
		return err
	}
	return client.Stream(ctx, request, emit)
}

func (m *modelRoutedClient) clientFor(override *modelsdev.ProviderOverride) (llm.StreamClient, error) {
	baseURL := override.API
	if baseURL == "" {
		// No endpoint of its own: the model rides the provider's own base
		// URL, just addressed via its protocol's own path (e.g. Zen's GPT-5
		// models proxy to {provider base}/responses rather than
		// {provider base}/chat/completions). Matches account.ts's own
		// fallback when a per-model `provider.api` is absent.
		baseURL = m.resolved.BaseURL
	}
	key := override.NPM + "|" + baseURL

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.clients == nil {
		m.clients = map[string]llm.StreamClient{}
	}
	if client, ok := m.clients[key]; ok {
		return client, nil
	}

	var client llm.StreamClient
	switch {
	case override.NPM == "@ai-sdk/anthropic":
		c := anthropic.New(m.resolved.APIKey)
		c.BaseURL = baseURL
		c.Options = m.resolved.Options
		client = c
	case strings.Contains(override.NPM, "google"):
		c := gemini.New(m.resolved.APIKey)
		c.BaseURL = baseURL
		c.Options = m.resolved.Options
		client = c
	case override.NPM == "@ai-sdk/openai":
		c := openairesponses.New(m.resolved.APIKey)
		c.BaseURL = baseURL
		c.Options = m.resolved.Options
		client = c
	case override.NPM == "@ai-sdk/openai-compatible", override.NPM == "":
		c := openai.New(m.resolved.APIKey)
		c.BaseURL = baseURL
		c.Options = m.resolved.Options
		client = c
	default:
		return nil, fmt.Errorf("provider %q: model needs unsupported SDK %q (no Go client implements it)", m.resolved.ID, override.NPM)
	}
	m.clients[key] = client
	return client, nil
}
