package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/langazov/gocode-go/internal/config"
	"github.com/langazov/gocode-go/internal/modelsdev"
)

// TestTransformRegistryMatches confirms the registry dispatches by provider id
// and that a provider with no transform gets none — the catalog-only path that
// carries the ~180 OpenAI-compatible providers.
func TestTransformRegistryMatches(t *testing.T) {
	cases := []struct {
		id   string
		want bool
	}{
		{"anthropic", true},
		{"azure", true},
		{"snowflake-cortex", true},
		{"openrouter", false},
		{"groq", false},
		{"totally-unknown", false},
	}
	for _, c := range cases {
		got := len(transformsFor(c.id, modelsdev.Provider{})) > 0
		if got != c.want {
			t.Errorf("transformsFor(%q): has transform = %v, want %v", c.id, got, c.want)
		}
	}
}

// TestProtocolPinnedByID is the regression for the behavior the hardcoded
// switch in FromConfig used to provide: the wire protocol for anthropic and
// the google family comes from the provider id, not only from the catalog's
// npm package, so an empty or wrong catalog entry cannot downgrade them to the
// OpenAI wire format.
func TestProtocolPinnedByID(t *testing.T) {
	cases := map[string]string{
		"anthropic":     ProtocolAnthropic,
		"google":        ProtocolGemini,
		"gemini":        ProtocolGemini,
		"google-vertex": ProtocolGemini,
	}
	for id, want := range cases {
		r := &Resolved{ID: id, Protocol: ProtocolOpenAI}
		if err := applyTransforms(context.Background(), r); err != nil {
			t.Fatalf("%s: %v", id, err)
		}
		if r.Protocol != want {
			t.Errorf("%s: protocol = %q, want %q", id, r.Protocol, want)
		}
	}
}

func TestAzureBuildsResourceURLAndAPIKeyHeader(t *testing.T) {
	t.Setenv("AZURE_RESOURCE_NAME", "my-resource")
	r := &Resolved{ID: "azure", Protocol: ProtocolOpenAI, APIKey: "secret"}
	if err := applyTransforms(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	if want := "https://my-resource.openai.azure.com/openai"; r.BaseURL != want {
		t.Errorf("baseURL = %q, want %q", r.BaseURL, want)
	}

	got := r.Options.URL(r.BaseURL, "gpt-4o", "unused")
	want := "https://my-resource.openai.azure.com/openai/v1/chat/completions?api-version=v1"
	if got != want {
		t.Errorf("endpoint = %q, want %q", got, want)
	}

	// The key must travel as api-key, and the bearer header must be suppressed.
	req, _ := http.NewRequest(http.MethodPost, got, nil)
	signed, err := r.Options.Authenticate(req, nil)
	if err != nil || !signed {
		t.Fatalf("Authenticate: signed=%v err=%v, want signed with no error", signed, err)
	}
	if req.Header.Get("api-key") != "secret" {
		t.Errorf("api-key header = %q, want %q", req.Header.Get("api-key"), "secret")
	}
	if req.Header.Get("Authorization") != "" {
		t.Errorf("Authorization = %q, want it unset (Azure does not take a bearer token)", req.Header.Get("Authorization"))
	}
}

// TestAzureURLVariants covers the routing branches ported from
// @ai-sdk/azure's url() closure.
func TestAzureURLVariants(t *testing.T) {
	cases := []struct {
		name       string
		base       string
		deployment bool
		want       string
	}{
		{
			name: "standard resource gets the v1 path and api-version",
			base: "https://r.openai.azure.com/openai",
			want: "https://r.openai.azure.com/openai/v1/chat/completions?api-version=v1",
		},
		{
			name:       "deployment-based routing addresses the model in the path",
			base:       "https://r.openai.azure.com/openai",
			deployment: true,
			want:       "https://r.openai.azure.com/openai/deployments/gpt-4o/chat/completions?api-version=v1",
		},
		{
			name: "an already-versioned base owns its own versioning",
			base: "https://r.openai.azure.com/openai/v1",
			want: "https://r.openai.azure.com/openai/v1/chat/completions",
		},
		{
			name: "a custom gateway is left entirely alone",
			base: "https://gateway.example.com/azure",
			want: "https://gateway.example.com/azure/chat/completions",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := azureURL(c.base, "gpt-4o", "v1", c.deployment, "/chat/completions")
			if got != c.want {
				t.Errorf("azureURL = %q, want %q", got, c.want)
			}
		})
	}
}

func TestAzureWithoutResourceNameFails(t *testing.T) {
	t.Setenv("AZURE_RESOURCE_NAME", "")
	r := &Resolved{ID: "azure", Protocol: ProtocolOpenAI}
	err := applyTransforms(context.Background(), r)
	if err == nil {
		t.Fatal("expected an error when no resource name or base URL is configured")
	}
	if !strings.Contains(err.Error(), "AZURE_RESOURCE_NAME") {
		t.Errorf("error should name the missing setting, got: %v", err)
	}
}

// TestAzureConfigBaseURLWins: an explicit baseURL means the resource name is
// not required, matching the TS plugin's three-way check.
func TestAzureConfigBaseURLWins(t *testing.T) {
	t.Setenv("AZURE_RESOURCE_NAME", "")
	r := &Resolved{ID: "azure", Protocol: ProtocolOpenAI, BaseURL: "https://gw.example.com/azure"}
	if err := applyTransforms(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	if r.BaseURL != "https://gw.example.com/azure" {
		t.Errorf("baseURL = %q, want the configured one", r.BaseURL)
	}
}

func TestAzureCognitiveServicesBaseURL(t *testing.T) {
	t.Setenv("AZURE_COGNITIVE_SERVICES_RESOURCE_NAME", "cog")
	r := &Resolved{ID: "azure-cognitive-services", Protocol: ProtocolOpenAI}
	if err := applyTransforms(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	if want := "https://cog.cognitiveservices.azure.com/openai"; r.BaseURL != want {
		t.Errorf("baseURL = %q, want %q", r.BaseURL, want)
	}
}

func TestSnowflakeTokenPrecedence(t *testing.T) {
	t.Setenv("SNOWFLAKE_CORTEX_TOKEN", "")
	t.Setenv("SNOWFLAKE_CORTEX_PAT", "pat-token")
	r := &Resolved{ID: "snowflake-cortex", APIKey: "catalog-key"}
	if err := applyTransforms(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	if r.APIKey != "pat-token" {
		t.Errorf("APIKey = %q, want the PAT env var to win", r.APIKey)
	}

	t.Setenv("SNOWFLAKE_CORTEX_TOKEN", "primary")
	r = &Resolved{ID: "snowflake-cortex", APIKey: "catalog-key"}
	if err := applyTransforms(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	if r.APIKey != "primary" {
		t.Errorf("APIKey = %q, want SNOWFLAKE_CORTEX_TOKEN to win", r.APIKey)
	}
}

// TestSnowflakeRenamesMaxTokens: the rename must remove the original key, which
// is why it goes through the transport rather than Options.Body.
func TestSnowflakeRenamesMaxTokens(t *testing.T) {
	var seen map[string]any
	base := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(req.Body)
		json.Unmarshal(body, &seen)
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("{}"))}, nil
	})

	payload := []byte(`{"model":"m","max_tokens":100}`)
	req, _ := http.NewRequest(http.MethodPost, "https://cortex.example.com/chat/completions", bytes.NewReader(payload))
	req.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(payload)), nil }

	if _, err := cortexTransport(base).RoundTrip(req); err != nil {
		t.Fatal(err)
	}
	if _, ok := seen["max_tokens"]; ok {
		t.Error("max_tokens must be removed, not just duplicated")
	}
	if seen["max_completion_tokens"] != float64(100) {
		t.Errorf("max_completion_tokens = %v, want 100", seen["max_completion_tokens"])
	}
}

// TestSnowflakeConversationComplete: Cortex signals a normal end of turn with
// a 400, which must not surface as an error.
func TestSnowflakeConversationComplete(t *testing.T) {
	base := roundTripFunc(func(*http.Request) (*http.Response, error) {
		body := `{"message":"Conversation complete"}`
		return &http.Response{
			StatusCode: http.StatusBadRequest,
			Header:     http.Header{},
			Body:       io.NopCloser(strings.NewReader(body)),
		}, nil
	})
	req, _ := http.NewRequest(http.MethodPost, "https://cortex.example.com/x", nil)
	res, err := cortexTransport(base).RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	body, _ := io.ReadAll(res.Body)
	if !strings.Contains(string(body), `"finish_reason":"stop"`) {
		t.Errorf("body = %s, want a normal stop response", body)
	}
}

// TestSnowflakeOtherBadRequestPassesThrough: an unrelated 400 must keep its
// status and its body, so the error message survives.
func TestSnowflakeOtherBadRequestPassesThrough(t *testing.T) {
	base := roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusBadRequest,
			Header:     http.Header{},
			Body:       io.NopCloser(strings.NewReader(`{"message":"invalid model"}`)),
		}, nil
	})
	req, _ := http.NewRequest(http.MethodPost, "https://cortex.example.com/x", nil)
	res, err := cortexTransport(base).RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want the original 400", res.StatusCode)
	}
	body, _ := io.ReadAll(res.Body)
	if !strings.Contains(string(body), "invalid model") {
		t.Errorf("body = %s, want the original error to survive", body)
	}
}

func TestSnowflakeFixesStreamRoles(t *testing.T) {
	stream := "data: {\"choices\":[{\"delta\":{\"role\":\"\",\"content\":\"hi\"}}]}\n" +
		"data: {\"choices\":[{\"delta\":{\"role\" : \"\",\"content\":\" there\"}}]}\n" +
		"data: [DONE]\n"
	base := roundTripFunc(func(*http.Request) (*http.Response, error) {
		header := http.Header{}
		header.Set("content-type", "text/event-stream")
		return &http.Response{StatusCode: 200, Header: header, Body: io.NopCloser(strings.NewReader(stream))}, nil
	})
	req, _ := http.NewRequest(http.MethodPost, "https://cortex.example.com/x", nil)
	res, err := cortexTransport(base).RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), `"role":""`) || strings.Contains(string(body), `"role" : ""`) {
		t.Errorf("empty roles survived: %s", body)
	}
	if got := strings.Count(string(body), `"role":"assistant"`); got != 2 {
		t.Errorf("replaced %d roles, want 2: %s", got, body)
	}
	// Content must be untouched.
	if !strings.Contains(string(body), `"content":"hi"`) || !strings.Contains(string(body), `"content":" there"`) {
		t.Errorf("content was altered: %s", body)
	}
}

// TestResolvedOptionReadsConfigPassthrough confirms provider-specific options
// reach transforms through the untyped passthrough map, which is how a
// provider adds a setting without a schema change.
func TestResolvedOptionReadsConfigPassthrough(t *testing.T) {
	r := &Resolved{
		ID: "azure",
		Config: &config.Provider{Options: config.ProviderOptions{
			APIKey: "k",
			Extra:  map[string]any{"resourceName": "from-config"},
		}},
	}
	if got := r.Option("resourceName"); got != "from-config" {
		t.Errorf("Option(resourceName) = %q, want %q", got, "from-config")
	}
	if got := r.Option("apiKey"); got != "k" {
		t.Errorf("Option(apiKey) = %q, want %q", got, "k")
	}
	if got := r.Option("missing"); got != "" {
		t.Errorf("Option(missing) = %q, want empty", got)
	}
}
