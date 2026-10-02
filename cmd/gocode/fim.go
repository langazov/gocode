package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/langazov/gocode-go/internal/acp"
	"github.com/langazov/gocode-go/internal/config"
	"github.com/langazov/gocode-go/internal/provider"
	"github.com/langazov/gocode-go/internal/session"
)

// fimCompleter serves fill-in-the-middle completion for models trained for
// it (Mistral's Codestral, over /v1/fim/completions): the text before and
// after the cursor in, the text between them out. Edit predictions use it
// for completions at the cursor (internal/acp/nes.go).
func fimCompleter(cfg *config.Config) acp.FIMFunc {
	client := &http.Client{Timeout: 30 * time.Second}
	return func(ctx context.Context, model session.ModelRef, prefix, suffix string, maxTokens int) (string, bool, error) {
		if !supportsFIM(model) {
			return "", false, nil
		}
		resolved, err := provider.Resolve(ctx, model.ProviderID, cfg)
		if err != nil {
			return "", true, err
		}
		if resolved.BaseURL == "" {
			return "", true, fmt.Errorf("provider %q has no API endpoint", model.ProviderID)
		}
		payload, _ := json.Marshal(map[string]any{
			"model":       resolved.Options.Model(model.ID),
			"prompt":      prefix,
			"suffix":      suffix,
			"max_tokens":  maxTokens,
			"temperature": 0,
		})
		req, err := http.NewRequestWithContext(ctx, http.MethodPost,
			strings.TrimRight(resolved.BaseURL, "/")+"/fim/completions", bytes.NewReader(payload))
		if err != nil {
			return "", true, err
		}
		req.Header.Set("Content-Type", "application/json")
		resolved.Options.ApplyHeaders(req)
		if signed, err := resolved.Options.Authenticate(req, payload); err != nil {
			return "", true, err
		} else if !signed && resolved.APIKey != "" {
			req.Header.Set("Authorization", "Bearer "+resolved.APIKey)
		}
		resp, err := resolved.Options.HTTPClient(client).Do(req)
		if err != nil {
			return "", true, err
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		if resp.StatusCode != http.StatusOK {
			return "", true, fmt.Errorf("fim: %s: %s", resp.Status, strings.TrimSpace(string(body)))
		}
		var out struct {
			Choices []struct {
				Message struct {
					Content string `json:"content"`
				} `json:"message"`
			} `json:"choices"`
		}
		if err := json.Unmarshal(body, &out); err != nil {
			return "", true, fmt.Errorf("fim: %w", err)
		}
		if len(out.Choices) == 0 {
			return "", true, nil
		}
		return out.Choices[0].Message.Content, true, nil
	}
}

// supportsFIM reports whether model takes fill-in-the-middle requests.
func supportsFIM(model session.ModelRef) bool {
	return strings.Contains(strings.ToLower(model.ID), "codestral")
}
