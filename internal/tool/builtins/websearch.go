package builtins

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/langazov/gocode-go/internal/installation"
	"github.com/langazov/gocode-go/internal/tool"
)

// Web search runs through the provider's public MCP endpoint rather than a
// REST API, mirroring packages/opencode/src/tool/mcp-websearch.ts: one
// JSON-RPC tools/call, and the response may come back as either plain JSON or
// an SSE stream.
const (
	exaMCPURL      = "https://mcp.exa.ai/mcp"
	parallelMCPURL = "https://search.parallel.ai/mcp"
	webSearchLimit = 25 * time.Second
	// maxWebSearchBytes caps a response body so a hostile or broken endpoint
	// cannot exhaust memory.
	maxWebSearchBytes = 8 << 20
)

// stringArgOr reads a string argument, falling back when absent or empty.
func stringArgOr(input map[string]any, key, fallback string) string {
	if value := stringArg(input, key); value != "" {
		return value
	}
	return fallback
}

// WebSearchTool searches the web through Exa or Parallel.
type WebSearchTool struct {
	client *http.Client
	// now supplies the current year for the description. Overridable in tests.
	now func() time.Time
}

func NewWebSearchTool() *WebSearchTool {
	return &WebSearchTool{
		client: &http.Client{Timeout: webSearchLimit},
		now:    time.Now,
	}
}

func (t *WebSearchTool) Name() string { return "websearch" }

func (t *WebSearchTool) Description() string {
	year := strconv.Itoa(t.now().Year())
	return strings.Join([]string{
		"- Search the web using the session's web search provider - performs real-time web searches and can scrape content from specific URLs",
		"- Provides up-to-date information for current events and recent data",
		"- Supports configurable result counts and returns the content from the most relevant websites",
		"- Use this tool for accessing information beyond knowledge cutoff",
		"- Searches are performed automatically within a single API call",
		"",
		"Usage notes:",
		"  - Supports live crawling modes when available: 'fallback' (backup if cached unavailable) or 'preferred' (prioritize live crawling)",
		"  - Search types when available: 'auto' (balanced), 'fast' (quick results), 'deep' (comprehensive search)",
		"  - Configurable context length for optimal LLM integration",
		"  - Domain filtering and advanced search options available",
		"",
		"The current year is " + year + ". You MUST use this year when searching for recent information or current events",
		"- Example: If the current year is " + year + " and the user asks for \"latest AI news\", search for \"AI news " + year + "\", NOT \"AI news " + strconv.Itoa(t.now().Year()-1) + "\"",
	}, "\n")
}

func (t *WebSearchTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"query": map[string]any{
				"type":        "string",
				"description": "Websearch query",
			},
			"numResults": map[string]any{
				"type":        "integer",
				"description": "Number of search results to return (default: 8)",
			},
			"livecrawl": map[string]any{
				"type":        "string",
				"enum":        []string{"fallback", "preferred"},
				"description": "Live crawl mode - 'fallback': use live crawling as backup if cached content unavailable, 'preferred': prioritize live crawling (default: 'fallback')",
			},
			"type": map[string]any{
				"type":        "string",
				"enum":        []string{"auto", "fast", "deep"},
				"description": "Search type - 'auto': balanced search (default), 'fast': quick results, 'deep': comprehensive search",
			},
			"contextMaxCharacters": map[string]any{
				"type":        "integer",
				"description": "Maximum characters for context string optimized for LLMs (default: 10000)",
			},
		},
		"required": []string{"query"},
	}
}

func (t *WebSearchTool) Execute(ctx context.Context, input map[string]any) (string, error) {
	return t.ExecuteWithContext(ctx, input, tool.ExecContext{})
}

func (t *WebSearchTool) ExecuteWithContext(ctx context.Context, input map[string]any, exec tool.ExecContext) (string, error) {
	query := stringArg(input, "query")
	if query == "" {
		return "", fmt.Errorf("websearch: query is required")
	}
	provider := SelectWebSearchProvider(exec.SessionID)

	var endpoint, toolName string
	var args map[string]any
	headers := map[string]string{}

	switch provider {
	case "parallel":
		endpoint, toolName = parallelMCPURL, "web_search"
		args = map[string]any{
			"objective":      query,
			"search_queries": []string{query},
		}
		if exec.SessionID != "" {
			args["session_id"] = exec.SessionID
		}
		headers["User-Agent"] = "gocode/" + installation.Version
		if key := os.Getenv("PARALLEL_API_KEY"); key != "" {
			headers["Authorization"] = "Bearer " + key
		}
	default:
		endpoint, toolName = exaEndpoint(), "web_search_exa"
		args = map[string]any{
			"query":      query,
			"type":       stringArgOr(input, "type", "auto"),
			"numResults": intArg(input, "numResults", 8),
			"livecrawl":  stringArgOr(input, "livecrawl", "fallback"),
		}
		if max := intArg(input, "contextMaxCharacters", 0); max > 0 {
			args["contextMaxCharacters"] = max
		}
	}

	text, err := t.call(ctx, endpoint, toolName, args, headers)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(text) == "" {
		return "No search results found. Please try a different query.", nil
	}
	return text, nil
}

// exaEndpoint appends the API key when one is configured, matching EXA_URL.
func exaEndpoint() string {
	key := os.Getenv("EXA_API_KEY")
	if key == "" {
		return exaMCPURL
	}
	return exaMCPURL + "?exaApiKey=" + url.QueryEscape(key)
}

// SelectWebSearchProvider picks a provider for a session. An explicit
// GOCODE_WEBSEARCH_PROVIDER wins; otherwise the session ID is hashed so a
// given session always gets the same provider, matching
// selectWebSearchProvider in websearch.ts.
func SelectWebSearchProvider(sessionID string) string {
	switch os.Getenv("GOCODE_WEBSEARCH_PROVIDER") {
	case "exa":
		return "exa"
	case "parallel":
		return "parallel"
	}
	if checksum32(sessionID)%2 == 0 {
		return "exa"
	}
	return "parallel"
}

// WebSearchProviderLabel is the human-readable provider name.
func WebSearchProviderLabel(provider string) string {
	switch provider {
	case "parallel":
		return "Parallel Web Search"
	case "exa":
		return "Exa Web Search"
	}
	return "Web Search"
}

// checksum32 is FNV-1a over UTF-16 code units, matching the TypeScript
// checksum() in packages/core/src/util/encode.ts. TS iterates charCodeAt, so
// the hash is over UTF-16 units rather than bytes or runes; encoding as UTF-16
// here keeps provider selection identical across the two implementations for
// non-ASCII session IDs.
func checksum32(content string) uint32 {
	if content == "" {
		return 0
	}
	var hash uint32 = 0x811c9dc5
	for _, unit := range utf16Units(content) {
		hash ^= uint32(unit)
		hash *= 0x01000193
	}
	return hash
}

func utf16Units(s string) []uint16 {
	out := make([]uint16, 0, len(s))
	for _, r := range s {
		if r > 0xFFFF {
			r -= 0x10000
			out = append(out, uint16(0xD800+(r>>10)), uint16(0xDC00+(r&0x3FF)))
			continue
		}
		out = append(out, uint16(r))
	}
	return out
}

// mcpEnvelope is the subset of the JSON-RPC response the search tools return.
type mcpEnvelope struct {
	Result struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	} `json:"result"`
}

func (t *WebSearchTool) call(ctx context.Context, endpoint, toolName string, args map[string]any, headers map[string]string) (string, error) {
	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tools/call",
		"params":  map[string]any{"name": toolName, "arguments": args},
	})
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, webSearchLimit)
	defer cancel()

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	for key, value := range headers {
		request.Header.Set(key, value)
	}

	response, err := t.client.Do(request)
	if err != nil {
		return "", fmt.Errorf("websearch: %s request failed: %w", toolName, err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", fmt.Errorf("websearch: %s returned %d", toolName, response.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxWebSearchBytes))
	if err != nil {
		return "", err
	}
	return ParseMCPSearchResponse(string(raw)), nil
}

// ParseMCPSearchResponse extracts the first non-empty text block from either a
// plain JSON-RPC body or an SSE stream of them, porting
// McpWebSearch.parseResponse.
func ParseMCPSearchResponse(body string) string {
	if text := parseMCPPayload(strings.TrimSpace(body)); text != "" {
		return text
	}
	for _, line := range strings.Split(body, "\n") {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		if text := parseMCPPayload(strings.TrimSpace(line[len("data: "):])); text != "" {
			return text
		}
	}
	return ""
}

func parseMCPPayload(payload string) string {
	if !strings.HasPrefix(payload, "{") {
		return ""
	}
	var envelope mcpEnvelope
	if err := json.Unmarshal([]byte(payload), &envelope); err != nil {
		return ""
	}
	for _, item := range envelope.Result.Content {
		if item.Text != "" {
			return item.Text
		}
	}
	return ""
}
