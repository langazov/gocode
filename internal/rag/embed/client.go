// Package embed is a small OpenAI-compatible embeddings HTTP client:
// POST {baseURL}/embeddings, {"model":..., "input":[...]} -> {"data":[{"embedding":[...]}]}.
//
// This is the wire format OpenAI, Azure OpenAI, and most self-hosted or
// OpenAI-compatible gateways already speak for embeddings, and it is the one
// format this Go port's chat clients (internal/llm/openai) already assume
// elsewhere — so it needs no new provider-family code, only a different
// endpoint suffix and response shape than chat completions.
package embed

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// DefaultBatchSize bounds how many inputs go in one request. OpenAI accepts
// up to 2048 per call; a smaller batch keeps one slow/failed request cheap to
// retry.
const DefaultBatchSize = 96

// Client calls a configured embeddings endpoint.
type Client struct {
	// BaseURL is the provider's API root, e.g. "https://api.openai.com/v1".
	// No trailing slash.
	BaseURL string
	// APIKey is sent as "Authorization: Bearer <key>".
	APIKey string
	// Model is the embedding model id, e.g. "text-embedding-3-small".
	Model string
	// HTTP is the client used for requests; defaults to a 60s-timeout client.
	HTTP *http.Client
	// BatchSize overrides DefaultBatchSize.
	BatchSize int
}

// New builds a Client with a sane default HTTP timeout.
func New(baseURL, apiKey, model string) *Client {
	return &Client{
		BaseURL: strings.TrimSuffix(baseURL, "/"),
		APIKey:  apiKey,
		Model:   model,
		HTTP:    &http.Client{Timeout: 60 * time.Second},
	}
}

type request struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

type response struct {
	Data []struct {
		Embedding []float32 `json:"embedding"`
		Index     int       `json:"index"`
	} `json:"data"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// Embed returns one vector per input text, in the same order. Inputs are
// sent in batches of BatchSize (or DefaultBatchSize); a batch failure aborts
// the whole call rather than returning a partial result, so a caller never
// has to guess which vectors are missing.
func (c *Client) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	batchSize := c.BatchSize
	if batchSize <= 0 {
		batchSize = DefaultBatchSize
	}
	out := make([][]float32, 0, len(texts))
	for start := 0; start < len(texts); start += batchSize {
		end := min(start+batchSize, len(texts))
		vectors, err := c.embedBatch(ctx, texts[start:end])
		if err != nil {
			return nil, fmt.Errorf("embed: batch %d-%d: %w", start, end, err)
		}
		out = append(out, vectors...)
	}
	return out, nil
}

func (c *Client) embedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	payload, err := json.Marshal(request{Model: c.Model, Input: texts})
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/embeddings", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+c.APIKey)

	httpClient := c.HTTP
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	resp, err := httpClient.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var parsed response
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("decode response (status %d): %w", resp.StatusCode, err)
	}
	if parsed.Error != nil {
		return nil, fmt.Errorf("embeddings endpoint (status %d): %s", resp.StatusCode, parsed.Error.Message)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("embeddings endpoint returned status %d: %s", resp.StatusCode, string(body))
	}
	if len(parsed.Data) != len(texts) {
		return nil, fmt.Errorf("embeddings endpoint returned %d vectors for %d inputs", len(parsed.Data), len(texts))
	}

	vectors := make([][]float32, len(texts))
	for _, item := range parsed.Data {
		if item.Index < 0 || item.Index >= len(vectors) {
			return nil, fmt.Errorf("embeddings endpoint returned out-of-range index %d", item.Index)
		}
		vectors[item.Index] = item.Embedding
	}
	for i, v := range vectors {
		if v == nil {
			return nil, fmt.Errorf("embeddings endpoint did not return a vector for input %d", i)
		}
	}
	return vectors, nil
}
