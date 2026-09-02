package provider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
)

func init() {
	Register(snowflakeTransform{byID{"snowflake-cortex"}})
}

// snowflakeTransform ports packages/core/src/plugin/provider/snowflake-cortex.ts.
//
// Cortex is OpenAI-compatible except in three places, all of which the
// TypeScript plugin patches by wrapping `fetch`. This port does the same
// through Options.Transport, the Go equivalent of that wrapper.
type snowflakeTransform struct{ byID }

func (snowflakeTransform) Apply(_ context.Context, r *Resolved) error {
	// Cortex names its token differently from the catalog's env var, and the
	// TS plugin reads four sources in this order.
	for _, candidate := range []string{
		os.Getenv("SNOWFLAKE_CORTEX_TOKEN"),
		os.Getenv("SNOWFLAKE_CORTEX_PAT"),
		r.Option("token"),
		r.Option("apiKey"),
	} {
		if candidate != "" {
			r.APIKey = candidate
			break
		}
	}
	r.Options.Transport = cortexTransport
	return nil
}

// cortexTransport applies the three Cortex quirks from cortexFetch().
func cortexTransport(base http.RoundTripper) http.RoundTripper {
	return roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if err := cortexRewriteRequest(req); err != nil {
			return nil, err
		}
		res, err := base.RoundTrip(req)
		if err != nil {
			return nil, err
		}
		if replacement, ok := cortexConversationComplete(res); ok {
			return replacement, nil
		}
		cortexFixStreamRoles(res)
		return res, nil
	})
}

// cortexRewriteRequest renames max_tokens to max_completion_tokens. This is a
// rename, not an addition, so it cannot go through Options.Body — the original
// key has to be removed or Cortex rejects the request.
func cortexRewriteRequest(req *http.Request) error {
	if req.Body == nil || req.GetBody == nil {
		return nil
	}
	body, err := io.ReadAll(req.Body)
	req.Body.Close()
	if err != nil {
		return err
	}
	var parsed map[string]any
	if err := json.Unmarshal(body, &parsed); err != nil {
		// Not JSON: pass it through untouched, matching the TS plugin's
		// silently-ignored JSON.parse failure.
		req.Body = io.NopCloser(bytes.NewReader(body))
		return nil
	}
	if value, ok := parsed["max_tokens"]; ok {
		parsed["max_completion_tokens"] = value
		delete(parsed, "max_tokens")
		if rewritten, err := json.Marshal(parsed); err == nil {
			body = rewritten
		}
	}
	req.Body = io.NopCloser(bytes.NewReader(body))
	req.ContentLength = int64(len(body))
	req.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(body)), nil
	}
	return nil
}

// cortexConversationComplete converts Cortex's 400 "conversation complete"
// into the normal stop response the client expects.
func cortexConversationComplete(res *http.Response) (*http.Response, bool) {
	if res.StatusCode != http.StatusBadRequest || res.Body == nil {
		return nil, false
	}
	data, err := io.ReadAll(res.Body)
	res.Body.Close()
	if err != nil {
		return nil, false
	}
	// Restore the body either way: if this is a different 400, the caller
	// still needs to read it to build the error message.
	res.Body = io.NopCloser(bytes.NewReader(data))

	var payload map[string]any
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, false
	}
	message, _ := payload["message"].(string)
	if message == "" {
		message, _ = payload["error"].(string)
	}
	if !strings.Contains(strings.ToLower(message), "conversation complete") {
		return nil, false
	}

	replacement := `{"choices":[{"finish_reason":"stop","message":{"content":"","role":"assistant"}}]}`
	header := http.Header{}
	header.Set("content-type", "application/json")
	return &http.Response{
		Status:        "200 OK",
		StatusCode:    http.StatusOK,
		Header:        header,
		Body:          io.NopCloser(strings.NewReader(replacement)),
		ContentLength: int64(len(replacement)),
		Request:       res.Request,
	}, true
}

// cortexFixStreamRoles rewrites the empty role Cortex sends in streaming
// deltas, which the client's parser does not accept.
func cortexFixStreamRoles(res *http.Response) {
	if res.Body == nil || !strings.Contains(res.Header.Get("content-type"), "text/event-stream") {
		return
	}
	res.Body = &replacingReader{source: res.Body, reader: bufio.NewReader(res.Body)}
}

// replacingReader applies the role fixup line by line.
//
// Working a line at a time is what makes this correct without a carry-over
// buffer: the payload is an SSE stream, whose events are newline-delimited,
// so the pattern can never span two lines and a line is always safe to
// rewrite in full. It also keeps the stream a stream — the client still sees
// each event as it arrives rather than waiting for the response to finish.
type replacingReader struct {
	source io.ReadCloser
	reader *bufio.Reader
	buf    []byte
}

var roleReplacement = []byte(`"role":"assistant"`)

func (r *replacingReader) Read(p []byte) (int, error) {
	if len(r.buf) == 0 {
		line, err := r.reader.ReadBytes('\n')
		if len(line) > 0 {
			r.buf = normalizeRoles(line)
		}
		if err != nil && len(r.buf) == 0 {
			return 0, err
		}
	}
	n := copy(p, r.buf)
	r.buf = r.buf[n:]
	return n, nil
}

func (r *replacingReader) Close() error { return r.source.Close() }

// normalizeRoles replaces `"role":""` allowing the whitespace variants the TS
// regex (/"role"\s*:\s*""/g) permits.
func normalizeRoles(data []byte) []byte {
	if !bytes.Contains(data, []byte(`"role"`)) {
		return data
	}
	var out bytes.Buffer
	for i := 0; i < len(data); {
		if data[i] != '"' || !bytes.HasPrefix(data[i:], []byte(`"role"`)) {
			out.WriteByte(data[i])
			i++
			continue
		}
		j := i + len(`"role"`)
		for j < len(data) && isSpace(data[j]) {
			j++
		}
		if j >= len(data) || data[j] != ':' {
			out.WriteByte(data[i])
			i++
			continue
		}
		j++
		for j < len(data) && isSpace(data[j]) {
			j++
		}
		if j+1 < len(data) && data[j] == '"' && data[j+1] == '"' {
			out.Write(roleReplacement)
			i = j + 2
			continue
		}
		out.WriteByte(data[i])
		i++
	}
	return out.Bytes()
}

func isSpace(b byte) bool {
	return b == ' ' || b == '\t' || b == '\n' || b == '\r'
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

var _ Transform = snowflakeTransform{}
