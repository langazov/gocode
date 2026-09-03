package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/langazov/gocode-go/internal/auth"
	"github.com/langazov/gocode-go/internal/installation"
	"github.com/langazov/gocode-go/internal/modelsdev"
)

func init() {
	Register(copilotTransform{byID{"github-copilot"}})
}

// Ported from packages/opencode/src/plugin/github-copilot/copilot.ts.
const (
	copilotClientID   = "Ov23li8tweQw6odWQebz"
	copilotAPIVersion = "2026-06-01"
	copilotScope      = "read:user"
	copilotDefaultAPI = "https://api.githubcopilot.com"
)

// copilotTransform ports the GitHub Copilot plugin.
//
// Copilot is OpenAI-compatible on the wire but rejects requests that lack its
// editor headers, and it publishes its own model list rather than relying on
// the catalog. The credential is the GitHub OAuth token itself: the plugin
// sends `Authorization: Bearer <token>` directly, with no separate exchange
// against copilot_internal/v2/token.
type copilotTransform struct{ byID }

func (copilotTransform) Apply(ctx context.Context, r *Resolved) error {
	info, err := ResolveCredential(ctx, r.ID, r.Entry)
	if err != nil {
		return err
	}

	enterprise := r.Option("enterpriseUrl")
	if info != nil && info.EnterpriseURL != "" {
		enterprise = info.EnterpriseURL
	}
	if r.BaseURL == "" || r.BaseURL == copilotDefaultAPI {
		r.BaseURL = copilotBaseURL(enterprise)
	}

	r.Header("X-GitHub-Api-Version", copilotAPIVersion)
	r.Header("User-Agent", "opencode/"+installation.Version)
	r.Header("Openai-Intent", "conversation-edits")
	// x-initiator tells Copilot whether a request came from a person typing or
	// from an agent acting on its own. TS decides per request, inspecting the
	// message history for compaction markers and subagent parentage; this port
	// has no per-request hook, so it sends "user", the value that matches an
	// ordinary prompt. The consequence of getting this wrong is Copilot's usage
	// accounting, not a failed request.
	r.Header("x-initiator", "user")

	// The stored OAuth token is the API credential. TS reads auth.refresh
	// rather than auth.access here — for Copilot the plugin stores the same
	// GitHub token in both fields.
	if info != nil && info.Type == "oauth" {
		if token := info.Refresh; token != "" {
			r.APIKey = token
		} else if info.Access != "" {
			r.APIKey = info.Access
		}
	}
	return nil
}

// FetchModels implements ModelSource with Copilot's own published list.
func (copilotTransform) FetchModels(ctx context.Context, r *Resolved) (map[string]modelsdev.Model, error) {
	if r.APIKey == "" {
		return nil, fmt.Errorf("github-copilot: not logged in")
	}
	return copilotModelCache.get(ctx, r.BaseURL, r.APIKey)
}

// copilotModelTTL bounds how stale the published model list may get. It
// matches the models.dev catalog's own freshness window; Copilot's list
// changes on the order of weeks, so this is about not re-fetching within a
// session rather than about staleness.
const copilotModelTTL = 5 * time.Minute

// modelCache memoizes a provider's published model list.
//
// Without it every model-list request re-fetches over the network. That is
// paid on the interface's hot path: opening the model dialog calls
// /api/model, which measured 140ms+ per open against ~3ms for a provider with
// no published list. The failure case matters just as much — an unusable
// credential means a round trip that fails slowly, every single time.
type modelCache struct {
	mu      sync.Mutex
	entries map[string]modelCacheEntry
}

type modelCacheEntry struct {
	models  map[string]modelsdev.Model
	err     error
	fetched time.Time
}

var copilotModelCache = &modelCache{}

func (c *modelCache) get(ctx context.Context, baseURL, token string) (map[string]modelsdev.Model, error) {
	// Key on the credential as well as the endpoint: logging in as someone
	// else must not serve the previous account's models.
	key := baseURL + "\x00" + token

	c.mu.Lock()
	entry, ok := c.entries[key]
	fresh := ok && time.Since(entry.fetched) < copilotModelTTL
	c.mu.Unlock()
	if fresh {
		return entry.models, entry.err
	}

	models, err := copilotModels(ctx, baseURL, token)

	c.mu.Lock()
	if c.entries == nil {
		c.entries = map[string]modelCacheEntry{}
	}
	// A failure is cached too, deliberately: a bad credential otherwise costs
	// a slow failing round trip on every dialog open.
	c.entries[key] = modelCacheEntry{models: models, err: err, fetched: time.Now()}
	c.mu.Unlock()
	return models, err
}

// invalidate drops the cache, called when credentials change.
func (c *modelCache) invalidate() {
	c.mu.Lock()
	c.entries = nil
	c.mu.Unlock()
}

// InvalidateModelCaches clears memoized provider model lists. Call it after a
// login or logout, or the interface keeps showing the previous account's
// models until the TTL lapses.
func InvalidateModelCaches() {
	copilotModelCache.invalidate()
}

// AuthMethods advertises the device flow, so `gocode providers login`
// offers it instead of only asking for a pasted key.
func (copilotTransform) AuthMethods() []Method {
	return []Method{{
		Type:  MethodOAuth,
		Label: "Login with GitHub Copilot",
		Prompts: []Prompt{
			{
				Key:     "deploymentType",
				Label:   "Select GitHub deployment type",
				Options: []string{"github.com", "enterprise"},
			},
			{
				Key:   "enterpriseUrl",
				Label: "GitHub Enterprise URL or domain (leave blank for github.com)",
			},
		},
		Login: copilotLogin,
	}}
}

func copilotLogin(ctx context.Context, answers map[string]string) (Credential, error) {
	domain := "github.com"
	if answers["deploymentType"] == "enterprise" {
		domain = normalizeDomain(answers["enterpriseUrl"])
		if domain == "" {
			return Credential{}, fmt.Errorf("provider %q: an enterprise URL or domain is required", "github-copilot")
		}
	}

	flow := auth.DeviceFlow{
		ClientID:      copilotClientID,
		DeviceCodeURL: fmt.Sprintf("https://%s/login/device/code", domain),
		TokenURL:      fmt.Sprintf("https://%s/login/oauth/access_token", domain),
		Scope:         copilotScope,
		UserAgent:     "opencode/" + installation.Version,
		JSONRequest:   true,
	}

	code, err := flow.Start(ctx)
	if err != nil {
		return Credential{}, err
	}
	promptLogin(ctx, LoginPrompt{
		URL:     code.VerificationURI,
		Code:    code.UserCode,
		Message: fmt.Sprintf("Open %s and enter code: %s\n\nWaiting for authorization...", code.VerificationURI, code.UserCode),
	})

	token, err := flow.Poll(ctx, code)
	if err != nil {
		return Credential{}, err
	}
	// Both fields carry the same GitHub token, matching what the TS plugin
	// stores; the transform above reads refresh first.
	return Credential{
		Type:    "oauth",
		Access:  token.AccessToken,
		Refresh: token.AccessToken,
	}, nil
}

// copilotBaseURL ports base(): enterprise installs are reached through a
// copilot-api host derived from the enterprise domain.
func copilotBaseURL(enterpriseURL string) string {
	domain := normalizeDomain(enterpriseURL)
	if domain == "" {
		return copilotDefaultAPI
	}
	return "https://copilot-api." + domain
}

// normalizeDomain ports normalizeDomain(): strip the scheme and any trailing
// slash, leaving a bare host.
func normalizeDomain(url string) string {
	trimmed := strings.TrimSpace(url)
	trimmed = strings.TrimPrefix(trimmed, "https://")
	trimmed = strings.TrimPrefix(trimmed, "http://")
	return strings.TrimRight(trimmed, "/")
}

// copilotModelItem is the subset of GET /models this port consumes. The TS
// schema in github-copilot/models.ts decodes considerably more (billing,
// vision limits, thinking budgets); those feed catalog fields the Go port
// does not yet track, so decoding them here would produce values nothing
// reads.
type copilotModelItem struct {
	ID                 string   `json:"id"`
	Name               string   `json:"name"`
	ModelPickerEnabled bool     `json:"model_picker_enabled"`
	SupportedEndpoints []string `json:"supported_endpoints"`
	Capabilities       struct {
		Family string `json:"family"`
		Limits struct {
			MaxContextWindowTokens int `json:"max_context_window_tokens"`
			MaxOutputTokens        int `json:"max_output_tokens"`
			MaxPromptTokens        int `json:"max_prompt_tokens"`
		} `json:"limits"`
		Supports struct {
			AdaptiveThinking bool     `json:"adaptive_thinking"`
			ReasoningEffort  []string `json:"reasoning_effort"`
			ToolCalls        bool     `json:"tool_calls"`
			Vision           bool     `json:"vision"`
		} `json:"supports"`
	} `json:"capabilities"`
}

// copilotModels fetches the live model list, porting CopilotModels.get. Only
// picker-enabled, tool-calling models are kept — the same filter the TS side
// applies before showing the list.
func copilotModels(ctx context.Context, baseURL, token string) (map[string]modelsdev.Model, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(baseURL, "/")+"/models", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("User-Agent", "opencode/"+installation.Version)
	req.Header.Set("X-GitHub-Api-Version", copilotAPIVersion)
	req.Header.Set("Accept", "application/json")

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, fmt.Errorf("github-copilot: fetching models returned %d", res.StatusCode)
	}
	data, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, err
	}

	var payload struct {
		Data []copilotModelItem `json:"data"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, err
	}

	out := map[string]modelsdev.Model{}
	for _, item := range payload.Data {
		if !item.ModelPickerEnabled || !item.Capabilities.Supports.ToolCalls {
			continue
		}
		limits := item.Capabilities.Limits
		context := limits.MaxContextWindowTokens
		if context == 0 {
			context = limits.MaxPromptTokens
		}
		out[item.ID] = modelsdev.Model{
			ID:          item.ID,
			Name:        item.Name,
			Family:      item.Capabilities.Family,
			Attachment:  item.Capabilities.Supports.Vision,
			Reasoning:   item.Capabilities.Supports.AdaptiveThinking || len(item.Capabilities.Supports.ReasoningEffort) > 0,
			Temperature: true,
			ToolCall:    true,
			Limit: modelsdev.Limit{
				Context: float64(context),
				Output:  float64(limits.MaxOutputTokens),
			},
		}
	}
	return out, nil
}

var (
	_ Transform    = copilotTransform{}
	_ AuthProvider = copilotTransform{}
	_ ModelSource  = copilotTransform{}
)
