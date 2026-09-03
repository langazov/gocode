package builtins

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/langazov/gocode-go/internal/tool"
)

func TestParseMCPSearchResponsePlainJSON(t *testing.T) {
	body := `{"result":{"content":[{"type":"text","text":"the answer"}]}}`
	if got := ParseMCPSearchResponse(body); got != "the answer" {
		t.Fatalf("parsed %q", got)
	}
}

func TestParseMCPSearchResponseSSE(t *testing.T) {
	body := strings.Join([]string{
		"event: message",
		`data: {"result":{"content":[{"type":"text","text":""}]}}`,
		"",
		`data: {"result":{"content":[{"type":"text","text":"streamed answer"}]}}`,
		"",
	}, "\n")
	if got := ParseMCPSearchResponse(body); got != "streamed answer" {
		t.Fatalf("parsed %q", got)
	}
}

func TestParseMCPSearchResponseGarbage(t *testing.T) {
	for _, body := range []string{"", "not json", "data: nope", `{"result":{}}`} {
		if got := ParseMCPSearchResponse(body); got != "" {
			t.Fatalf("parsed %q from %q", got, body)
		}
	}
}

// TestSelectWebSearchProviderIsStable pins the hash-based routing: a session
// always gets the same provider, and the override always wins.
func TestSelectWebSearchProviderIsStable(t *testing.T) {
	first := SelectWebSearchProvider("ses_abc")
	for range 5 {
		if got := SelectWebSearchProvider("ses_abc"); got != first {
			t.Fatalf("provider flipped between calls: %q then %q", first, got)
		}
	}
	if first != "exa" && first != "parallel" {
		t.Fatalf("unexpected provider %q", first)
	}

	t.Setenv("GOCODE_WEBSEARCH_PROVIDER", "parallel")
	if got := SelectWebSearchProvider("ses_abc"); got != "parallel" {
		t.Fatalf("override ignored: %q", got)
	}
	t.Setenv("GOCODE_WEBSEARCH_PROVIDER", "exa")
	if got := SelectWebSearchProvider("ses_abc"); got != "exa" {
		t.Fatalf("override ignored: %q", got)
	}
}

// TestChecksumMatchesTypeScript pins the FNV-1a hash against values computed
// with the TypeScript checksum() in packages/core/src/util/encode.ts, so
// provider selection agrees across implementations.
func TestChecksumMatchesTypeScript(t *testing.T) {
	cases := map[string]uint32{
		"":      0,
		"a":     0xe40c292c,
		"abc":   0x1a47e90b,
		"hello": 0x4f9f2cab,
	}
	for input, want := range cases {
		if got := checksum32(input); got != want {
			t.Fatalf("checksum32(%q) = %#x, want %#x", input, got, want)
		}
	}
}

func TestWebSearchCallsProviderEndpoint(t *testing.T) {
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"result":{"content":[{"type":"text","text":"result text"}]}}`))
	}))
	defer server.Close()

	tl := NewWebSearchTool()
	// Point the tool at the test server by overriding the endpoint the same
	// way the provider selection would.
	out, err := tl.call(context.Background(), server.URL, "web_search_exa", map[string]any{
		"query": "golang channels",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if out != "result text" {
		t.Fatalf("output = %q", out)
	}
	if gotBody["method"] != "tools/call" {
		t.Fatalf("request body = %#v", gotBody)
	}
	params, _ := gotBody["params"].(map[string]any)
	if params["name"] != "web_search_exa" {
		t.Fatalf("params = %#v", params)
	}
}

func TestWebSearchRequiresQuery(t *testing.T) {
	tl := NewWebSearchTool()
	if _, err := tl.ExecuteWithContext(context.Background(), map[string]any{}, tool.ExecContext{}); err == nil {
		t.Fatal("expected query to be required")
	}
}

func TestWebSearchSurfacesHTTPErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()

	tl := NewWebSearchTool()
	_, err := tl.call(context.Background(), server.URL, "web_search_exa", map[string]any{}, nil)
	if err == nil || !strings.Contains(err.Error(), "429") {
		t.Fatalf("err = %v, want a 429", err)
	}
}

func TestWebSearchDescriptionCarriesCurrentYear(t *testing.T) {
	tl := NewWebSearchTool()
	if !strings.Contains(tl.Description(), "The current year is") {
		t.Fatal("description should state the current year")
	}
}
