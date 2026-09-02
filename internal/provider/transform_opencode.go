package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/anomalyco/opencode-go/internal/auth"
	"github.com/anomalyco/opencode-go/internal/modelsdev"
)

func init() {
	Register(opencodeTransform{byID{"opencode"}})
}

// Ported from packages/core/src/plugin/provider/opencode.ts.
const (
	zenDefaultServer = "https://opencode.ai/console"
	zenClientID      = "opencode-cli"
)

// opencodeTransform implements the opencode/Zen account: a device-flow login,
// token refresh, and the per-account catalog fetched from {server}/api/config.
//
// That last part is the fourth layer of provider defaults — an org's own
// provider and model overrides, layered on top of the public models.dev
// catalog. It is the reason CatalogOverlay exists.
type opencodeTransform struct{ byID }

func (opencodeTransform) Apply(ctx context.Context, r *Resolved) error {
	info, err := ResolveCredential(ctx, r.ID, r.Entry)
	if err != nil {
		return err
	}
	if info == nil {
		return nil
	}
	switch info.Type {
	case "oauth":
		r.APIKey = info.Access
	case "api":
		r.APIKey = info.Key
	}
	if server := zenServer(info); server != zenDefaultServer && r.BaseURL == "" {
		r.BaseURL = server
	}
	return nil
}

func (opencodeTransform) AuthMethods() []Method {
	return []Method{{
		Type:  MethodOAuth,
		Label: "OpenCode Console account",
		Login: zenLogin,
	}}
}

// RefreshCredential renews a Zen token against the same device token endpoint.
func (opencodeTransform) RefreshCredential(ctx context.Context, info auth.Info) (auth.Info, error) {
	tokens, err := auth.RefreshGrant(ctx, nil, zenServer(&info)+"/auth/device/token", zenClientID, info.Refresh, "")
	if err != nil {
		return auth.Info{}, err
	}
	next := info
	next.Access = tokens.AccessToken
	next.Refresh = tokens.RefreshToken
	next.Expires = tokens.ExpiresAt()
	return next, nil
}

// Overlay fetches the account's provider config, porting fetchProviders().
// Without a stored credential there is nothing to fetch and no overlay.
func (opencodeTransform) Overlay(ctx context.Context) (modelsdev.Catalog, error) {
	info, err := auth.Get("opencode")
	if err != nil || info == nil {
		return nil, err
	}
	token := info.Access
	if info.Type == "api" {
		token = info.Key
	}
	if token == "" {
		return nil, nil
	}
	return zenConfig(ctx, zenServer(info), token, info.Metadata["orgID"])
}

func zenServer(info *auth.Info) string {
	if info != nil {
		if server := info.Metadata["server"]; server != "" {
			return server
		}
	}
	if server := os.Getenv("OPENCODE_CONSOLE_SERVER"); server != "" {
		return server
	}
	return zenDefaultServer
}

func zenLogin(ctx context.Context, _ map[string]string) (Credential, error) {
	server := zenServer(nil)
	flow := auth.DeviceFlow{
		ClientID:      zenClientID,
		DeviceCodeURL: server + "/auth/device/code",
		TokenURL:      server + "/auth/device/token",
		JSONRequest:   true,
	}
	code, err := flow.Start(ctx)
	if err != nil {
		return Credential{}, err
	}
	target := code.VerificationURIComplete
	if target == "" {
		target = code.VerificationURI
	}
	fmt.Printf("\nOpen %s and enter code: %s\n\nWaiting for authorization...\n", target, code.UserCode)

	token, err := flow.Poll(ctx, code)
	if err != nil {
		return Credential{}, err
	}
	return Credential{
		Type:    "oauth",
		Access:  token.AccessToken,
		Refresh: token.RefreshToken,
		Expires: auth.TokenResponse{ExpiresIn: token.ExpiresIn}.ExpiresAt(),
	}, nil
}

// zenProviderConfig is the subset of the remote config this port can express.
// The TS side also carries variants, modalities, per-model headers and request
// bodies; those map onto catalog fields the Go port does not model yet, so
// decoding them here would produce values nothing reads.
type zenProviderConfig struct {
	Name   string `json:"name"`
	NPM    string `json:"npm"`
	API    string `json:"api"`
	Models map[string]struct {
		Name        string `json:"name"`
		ID          string `json:"id"`
		Family      string `json:"family"`
		ReleaseDate string `json:"release_date"`
		Status      string `json:"status"`
		ToolCall    *bool  `json:"tool_call"`
		Limit       *struct {
			Context float64 `json:"context"`
			Output  float64 `json:"output"`
		} `json:"limit"`
		Cost *struct {
			Input      float64  `json:"input"`
			Output     float64  `json:"output"`
			CacheRead  *float64 `json:"cache_read"`
			CacheWrite *float64 `json:"cache_write"`
		} `json:"cost"`
	} `json:"models"`
}

// zenConfig fetches and converts {server}/api/config.
func zenConfig(ctx context.Context, server, token, orgID string) (modelsdev.Catalog, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, server+"/api/config", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	if orgID != "" {
		req.Header.Set("x-org-id", orgID)
	}

	client := &http.Client{Timeout: 30 * time.Second}
	res, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	// A 404 means the account simply has no overrides, not a failure.
	if res.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, fmt.Errorf("opencode: %s/api/config returned %d", server, res.StatusCode)
	}
	data, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, err
	}

	var payload struct {
		Config struct {
			Provider map[string]zenProviderConfig `json:"provider"`
		} `json:"config"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, err
	}

	catalog := modelsdev.Catalog{}
	for providerID, item := range payload.Config.Provider {
		entry := modelsdev.Provider{
			ID:   providerID,
			Name: item.Name,
			NPM:  item.NPM,
			API:  item.API,
		}
		if len(item.Models) > 0 {
			entry.Models = map[string]modelsdev.Model{}
		}
		for modelID, config := range item.Models {
			model := modelsdev.Model{
				ID:          modelID,
				Name:        config.Name,
				Family:      config.Family,
				ReleaseDate: config.ReleaseDate,
				Status:      config.Status,
			}
			if config.ID != "" {
				model.ID = config.ID
			}
			if config.ToolCall != nil {
				model.ToolCall = *config.ToolCall
			}
			if config.Limit != nil {
				model.Limit = modelsdev.Limit{Context: config.Limit.Context, Output: config.Limit.Output}
			}
			if config.Cost != nil {
				cost := &modelsdev.Cost{}
				cost.Input = config.Cost.Input
				cost.Output = config.Cost.Output
				cost.CacheRead = config.Cost.CacheRead
				cost.CacheWrite = config.Cost.CacheWrite
				model.Cost = cost
			}
			entry.Models[modelID] = model
		}
		catalog[providerID] = entry
	}
	return catalog, nil
}

var (
	_ Transform      = opencodeTransform{}
	_ AuthProvider   = opencodeTransform{}
	_ Refresher      = opencodeTransform{}
	_ CatalogOverlay = opencodeTransform{}
)
