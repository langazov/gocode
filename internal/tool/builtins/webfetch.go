package builtins

import (
	"context"
	"fmt"
	"html"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	maxWebFetchBytes    = 5 * 1024 * 1024
	defaultWebFetchSecs = 30
	maxWebFetchTimeout  = 120
	webFetchUserAgent   = "gocode-go/1.0"
)

type WebFetchTool struct {
	client *http.Client
}

func NewWebFetchTool() *WebFetchTool {
	return &WebFetchTool{client: &http.Client{Timeout: time.Duration(maxWebFetchTimeout) * time.Second}}
}

func (t *WebFetchTool) Name() string { return "webfetch" }

func (t *WebFetchTool) Description() string {
	return "Fetch content from an HTTP or HTTPS URL and return it as text, markdown, or HTML. Markdown is the default. This tool is read-only."
}

func (t *WebFetchTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"url": map[string]any{
				"type":        "string",
				"description": "The HTTP or HTTPS URL to fetch content from",
			},
			"format": map[string]any{
				"type":        "string",
				"enum":        []string{"text", "markdown", "html"},
				"description": "The format to return the content in. Defaults to markdown.",
			},
			"timeout": map[string]any{
				"type":        "integer",
				"description": fmt.Sprintf("Optional timeout in seconds (maximum: %d)", maxWebFetchTimeout),
			},
		},
		"required": []string{"url"},
	}
}

func (t *WebFetchTool) Execute(ctx context.Context, input map[string]any) (string, error) {
	url := stringField(input, "url")
	if url == "" {
		return "", fmt.Errorf("webfetch: url is required")
	}
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		return "", fmt.Errorf("webfetch: url must be http or https")
	}
	format := stringField(input, "format")
	if format == "" {
		format = "markdown"
	}
	timeoutSecs := intField(input, "timeout", defaultWebFetchSecs)
	if timeoutSecs <= 0 {
		timeoutSecs = defaultWebFetchSecs
	}
	if timeoutSecs > maxWebFetchTimeout {
		timeoutSecs = maxWebFetchTimeout
	}

	reqCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSecs)*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("webfetch: invalid url")
	}
	req.Header.Set("User-Agent", webFetchUserAgent)
	req.Header.Set("Accept", acceptHeader(format))

	res, err := t.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("Unable to fetch %s", url)
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return "", fmt.Errorf("Unable to fetch %s (status %d)", url, res.StatusCode)
	}

	limited := io.LimitReader(res.Body, maxWebFetchBytes)
	body, err := io.ReadAll(limited)
	if err != nil {
		return "", fmt.Errorf("Unable to read response from %s", url)
	}
	contentType := res.Header.Get("Content-Type")
	text := string(body)
	if format != "html" && looksLikeHTML(contentType, text) {
		text = stripHTML(text)
	}
	return text, nil
}

func acceptHeader(format string) string {
	switch format {
	case "markdown":
		return "text/markdown;q=1.0, text/x-markdown;q=0.9, text/plain;q=0.8, text/html;q=0.7, */*;q=0.1"
	case "text":
		return "text/plain;q=1.0, text/markdown;q=0.9, text/html;q=0.8, */*;q=0.1"
	case "html":
		return "text/html;q=1.0, application/xhtml+xml;q=0.9, text/plain;q=0.8, text/markdown;q=0.7, */*;q=0.1"
	}
	return "*/*"
}

func looksLikeHTML(contentType, body string) bool {
	if strings.Contains(strings.ToLower(contentType), "html") {
		return true
	}
	trimmed := strings.TrimSpace(body)
	return strings.HasPrefix(trimmed, "<!DOCTYPE") || strings.HasPrefix(trimmed, "<html") || strings.HasPrefix(trimmed, "<HTML")
}

// stripHTML removes tags and skips script/style bodies, decoding common HTML
// entities. It is a lightweight fallback for environments without a full
// HTML-to-markdown pipeline.
func stripHTML(input string) string {
	var out strings.Builder
	inTag := false
	skipUntil := ""
	i := 0
	for i < len(input) {
		if skipUntil != "" {
			idx := strings.Index(strings.ToLower(input[i:]), skipUntil)
			if idx == -1 {
				break
			}
			i += idx + len(skipUntil)
			skipUntil = ""
			continue
		}
		c := input[i]
		if c == '<' {
			lower := strings.ToLower(input[i:])
			if strings.HasPrefix(lower, "<script") {
				skipUntil = "</script>"
				inTag = false
				i++
				continue
			}
			if strings.HasPrefix(lower, "<style") {
				skipUntil = "</style>"
				inTag = false
				i++
				continue
			}
			inTag = true
			i++
			continue
		}
		if c == '>' && inTag {
			inTag = false
			i++
			continue
		}
		if !inTag {
			out.WriteByte(c)
		}
		i++
	}
	decoded := html.UnescapeString(out.String())
	return collapseWhitespace(decoded)
}

func collapseWhitespace(input string) string {
	lines := strings.Split(input, "\n")
	var out []string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return strings.Join(out, "\n")
}

func intField(input map[string]any, key string, fallback int) int {
	switch value := input[key].(type) {
	case float64:
		return int(value)
	case int:
		return value
	case int64:
		return int(value)
	}
	return fallback
}
