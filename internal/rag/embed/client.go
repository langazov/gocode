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
	"regexp"
	"strconv"
	"strings"
	"time"
)

// DefaultBatchSize bounds how many inputs go in one request. OpenAI accepts
// up to 2048 per call; a smaller batch keeps one slow/failed request cheap to
// retry.
const DefaultBatchSize = 96

// DefaultMaxRetries bounds how many times a single batch retries a rate
// limit (429) or transient server error (5xx, or the request never reaching
// the server at all) before Embed gives up and returns the error. Indexing a
// large project sends far more requests than any one embeddings account's
// per-minute quota allows in one burst, so a 429 mid-run is the expected
// steady state, not a failure — the fix is to wait as long as the endpoint
// asks and retry, not to abort the whole index over it.
const DefaultMaxRetries = 5

// maxRetryDelay bounds how long a single wait ever runs, even if the
// endpoint's own Retry-After (or a "try again in Ns" message) asks for
// longer — a broken or unusually generous server response shouldn't be able
// to hang indexing indefinitely on one batch.
const maxRetryDelay = 2 * time.Minute

// DefaultMaxInputChars caps each embedded input's length, in bytes (despite
// the name — len() on a Go string is a byte count, and that's what matters
// here). Embedding endpoints enforce a per-input token limit (OpenAI-style
// models: 8192).
//
// An earlier version of this clamp assumed a code-typical ~3 bytes/token and
// set the cap at 16384 — a real chunk still overflowed the endpoint's limit
// at that size, because BPE tokenizers fall back to one token per byte for
// content they have no learned merges for (base64, hex dumps, minified/
// obfuscated identifiers, high-entropy generated fixtures), so 3 bytes/token
// is not a safe assumption, let alone a worst case. The only bound that
// actually holds for any byte-fallback BPE tokenizer is 1 token per byte, so
// the cap is set there, with margin: 6000 bytes is comfortably under the
// 8192-token limit even if every single byte becomes its own token.
//
// Without this, one huge-line generated fixture poisons every batch
// containing it: the endpoint 400s the whole request, and Embed aborts
// rather than returning a partial result. Clamping only truncates
// pathological machine-generated text; a normal 60-line source chunk sits
// around 2-6 KB and is rarely touched, never truncated hard enough to lose
// meaningful content.
const DefaultMaxInputChars = 6000

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
	// MaxInputChars overrides DefaultMaxInputChars. 0 means default; a
	// negative value disables clamping entirely (send inputs as-is).
	MaxInputChars int
	// MaxRetries overrides DefaultMaxRetries. 0 means default; a negative
	// value disables retrying (fail on the first rate limit or server
	// error, matching this client's original behavior).
	MaxRetries int
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
	texts = clampInputs(texts, c.maxInputChars())
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

// maxInputChars resolves Client.MaxInputChars against the documented
// defaulting/opt-out convention: 0 means DefaultMaxInputChars, negative means
// disabled.
func (c *Client) maxInputChars() int {
	if c.MaxInputChars == 0 {
		return DefaultMaxInputChars
	}
	return c.MaxInputChars
}

// clampInputs truncates any text over maxChars, leaving the rest untouched.
// It never allocates a new slice when nothing needs truncating, since a
// whole-project index runs this over every chunk and the overwhelming
// majority are well under the limit.
func clampInputs(texts []string, maxChars int) []string {
	if maxChars < 0 {
		return texts
	}
	var clamped []string
	for i, t := range texts {
		if len(t) <= maxChars {
			continue
		}
		if clamped == nil {
			clamped = append([]string(nil), texts...)
		}
		clamped[i] = clampInput(t, maxChars)
	}
	if clamped == nil {
		return texts
	}
	return clamped
}

// clampInput truncates s to at most maxChars bytes, then drops any trailing
// incomplete UTF-8 sequence the byte-boundary cut may have left behind —
// cheaper than counting runes up front for text that's almost always well
// under the limit anyway.
func clampInput(s string, maxChars int) string {
	return strings.ToValidUTF8(s[:maxChars], "")
}

// embedBatch sends one request, retrying a rate limit or transient server
// error up to MaxRetries times. Every other failure (bad request, auth,
// malformed response) returns immediately — retrying those would just waste
// the wait.
func (c *Client) embedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	maxRetries := c.MaxRetries
	if maxRetries == 0 {
		maxRetries = DefaultMaxRetries
	}
	for attempt := 0; ; attempt++ {
		vectors, attemptErr := c.embedBatchOnce(ctx, texts)
		if attemptErr == nil {
			return vectors, nil
		}
		if !attemptErr.retryable || maxRetries < 0 || attempt >= maxRetries {
			return nil, attemptErr.err
		}
		delay := attemptErr.retryAfter
		if !attemptErr.retryAfterKnown {
			delay = backoffDelay(attempt)
		}
		if delay > maxRetryDelay {
			delay = maxRetryDelay
		}
		if werr := sleepCtx(ctx, delay); werr != nil {
			return nil, fmt.Errorf("%w (giving up waiting to retry after: %v)", werr, attemptErr.err)
		}
	}
}

// attemptError distinguishes a failure worth retrying (rate limited, a
// transient server error, or the request never reaching the server at all)
// from one that never will be: retrying a malformed request or a bad API key
// wastes the wait and delays surfacing a fix.
type attemptError struct {
	err       error
	retryable bool
	// retryAfter is the wait the endpoint itself asked for (header or
	// message), meaningful only when retryAfterKnown — a bare zero duration
	// still needs telling apart from "no hint given," since a genuine
	// Retry-After: 0 (retry immediately) is a real, if unusual, answer.
	retryAfter      time.Duration
	retryAfterKnown bool
}

func (c *Client) embedBatchOnce(ctx context.Context, texts []string) ([][]float32, *attemptError) {
	payload, err := json.Marshal(request{Model: c.Model, Input: texts})
	if err != nil {
		return nil, &attemptError{err: err}
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/embeddings", bytes.NewReader(payload))
	if err != nil {
		return nil, &attemptError{err: err}
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+c.APIKey)

	httpClient := c.HTTP
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	resp, err := httpClient.Do(httpReq)
	if err != nil {
		// The request never got a response at all (DNS, connection reset, a
		// timed-out dial) — as transient as a 5xx, and worth the same retry.
		return nil, &attemptError{err: err, retryable: true}
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, &attemptError{err: err, retryable: true}
	}

	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
		msg := string(body)
		var parsed response
		if jsonErr := json.Unmarshal(body, &parsed); jsonErr == nil && parsed.Error != nil {
			msg = parsed.Error.Message
		}
		retryAfter, ok := parseRetryAfterHeader(resp.Header.Get("Retry-After"))
		if !ok {
			retryAfter, ok = parseRetryAfterFromMessage(msg)
		}
		return nil, &attemptError{
			err:             fmt.Errorf("embeddings endpoint (status %d): %s", resp.StatusCode, msg),
			retryable:       true,
			retryAfter:      retryAfter,
			retryAfterKnown: ok,
		}
	}

	var parsed response
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, &attemptError{err: fmt.Errorf("decode response (status %d): %w", resp.StatusCode, err)}
	}
	if parsed.Error != nil {
		return nil, &attemptError{err: fmt.Errorf("embeddings endpoint (status %d): %s", resp.StatusCode, parsed.Error.Message)}
	}
	if resp.StatusCode != http.StatusOK {
		return nil, &attemptError{err: fmt.Errorf("embeddings endpoint returned status %d: %s", resp.StatusCode, string(body))}
	}
	if len(parsed.Data) != len(texts) {
		return nil, &attemptError{err: fmt.Errorf("embeddings endpoint returned %d vectors for %d inputs", len(parsed.Data), len(texts))}
	}

	vectors := make([][]float32, len(texts))
	for _, item := range parsed.Data {
		if item.Index < 0 || item.Index >= len(vectors) {
			return nil, &attemptError{err: fmt.Errorf("embeddings endpoint returned out-of-range index %d", item.Index)}
		}
		vectors[item.Index] = item.Embedding
	}
	for i, v := range vectors {
		if v == nil {
			return nil, &attemptError{err: fmt.Errorf("embeddings endpoint did not return a vector for input %d", i)}
		}
	}
	return vectors, nil
}

// parseRetryAfterHeader reads a standard Retry-After header: either a
// non-negative number of seconds, or an HTTP-date.
func parseRetryAfterHeader(v string) (time.Duration, bool) {
	if v == "" {
		return 0, false
	}
	if secs, err := strconv.ParseFloat(v, 64); err == nil && secs >= 0 {
		return time.Duration(secs * float64(time.Second)), true
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := time.Until(t); d > 0 {
			return d, true
		}
		return 0, true
	}
	return 0, false
}

// retryAfterPattern matches the wait OpenAI's rate-limit error message spells
// out in prose (e.g. "Please try again in 1.728s") when no Retry-After
// header carries the same information.
var retryAfterPattern = regexp.MustCompile(`(?i)try again in\s+([0-9]*\.?[0-9]+)\s*(ms|s)\b`)

func parseRetryAfterFromMessage(msg string) (time.Duration, bool) {
	m := retryAfterPattern.FindStringSubmatch(msg)
	if m == nil {
		return 0, false
	}
	val, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return 0, false
	}
	unit := time.Second
	if strings.EqualFold(m[2], "ms") {
		unit = time.Millisecond
	}
	return time.Duration(val * float64(unit)), true
}

// backoffDelay is the fallback wait when the endpoint didn't say how long to
// wait: doubling from 500ms, capped at 30s.
func backoffDelay(attempt int) time.Duration {
	d := 500 * time.Millisecond * time.Duration(uint64(1)<<uint(min(attempt, 10)))
	if d <= 0 || d > 30*time.Second {
		d = 30 * time.Second
	}
	return d
}

// sleepCtx waits out d, or returns ctx's error if it's canceled first.
func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
