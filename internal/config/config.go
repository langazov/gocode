// Package config ports the opencode configuration system
// (packages/core/src/v1/config + packages/opencode/src/config): JSONC config
// files, TS merge order, and typed access for the runtime.
package config

// Config mirrors the v1 root schema (config.ts).
type Config struct {
	Schema            string              `json:"$schema,omitempty"`
	Theme             string              `json:"theme,omitempty"`
	Shell             string              `json:"shell,omitempty"`
	Model             string              `json:"model,omitempty"`
	SmallModel        string              `json:"small_model,omitempty"`
	DefaultAgent      string              `json:"default_agent,omitempty"`
	Username          string              `json:"username,omitempty"`
	Share             string              `json:"share,omitempty"`
	AutoShare         *bool               `json:"autoshare,omitempty"`
	AutoUpdate        any                 `json:"autoupdate,omitempty"` // bool | "notify"
	SubagentDepth     *int                `json:"subagent_depth,omitempty"`
	DisabledProviders []string            `json:"disabled_providers,omitempty"`
	EnabledProviders  []string            `json:"enabled_providers,omitempty"`
	Instructions      []string            `json:"instructions,omitempty"`
	Plugin            []string            `json:"plugin,omitempty"`
	Agent             map[string]Agent    `json:"agent,omitempty"`
	Provider          map[string]Provider `json:"provider,omitempty"`
	MCP               map[string]any      `json:"mcp,omitempty"`
	LSP               LSPConfig           `json:"lsp,omitempty"`
	Permission        Permission          `json:"permission,omitempty"`
	Keybinds          map[string]string   `json:"keybinds,omitempty"`
	Tools             map[string]bool     `json:"tools,omitempty"`
	Commands          map[string]any      `json:"command,omitempty"`
	Experimental      map[string]any      `json:"experimental,omitempty"`
}

// Provider mirrors ConfigProviderV1.Info.
type Provider struct {
	API       string           `json:"api,omitempty"`
	Name      string           `json:"name,omitempty"`
	ID        string           `json:"id,omitempty"`
	NPM       string           `json:"npm,omitempty"`
	Env       []string         `json:"env,omitempty"`
	Whitelist []string         `json:"whitelist,omitempty"`
	Blacklist []string         `json:"blacklist,omitempty"`
	Options   ProviderOptions  `json:"options,omitempty"`
	Models    map[string]Model `json:"models,omitempty"`
}

// ProviderOptions mirrors the provider options record.
type ProviderOptions struct {
	APIKey        string         `json:"apiKey,omitempty"`
	BaseURL       string         `json:"baseURL,omitempty"`
	EnterpriseURL string         `json:"enterpriseUrl,omitempty"`
	SetCacheKey   *bool          `json:"setCacheKey,omitempty"`
	Timeout       any            `json:"timeout,omitempty"`
	Extra         map[string]any `json:"-"`
}

// UnmarshalJSON keeps unknown option keys for provider-specific passthrough.
func (o *ProviderOptions) UnmarshalJSON(data []byte) error {
	type plain ProviderOptions
	var typed plain
	if err := jsonUnmarshal(data, &typed); err != nil {
		return err
	}
	var raw map[string]any
	if err := jsonUnmarshal(data, &raw); err != nil {
		return err
	}
	delete(raw, "apiKey")
	delete(raw, "baseURL")
	delete(raw, "enterpriseUrl")
	delete(raw, "setCacheKey")
	delete(raw, "timeout")
	*o = ProviderOptions(typed)
	o.Extra = raw
	return nil
}

// Model mirrors ConfigProviderV1.Model.
type Model struct {
	ID          string            `json:"id,omitempty"`
	Name        string            `json:"name,omitempty"`
	Family      string            `json:"family,omitempty"`
	ReleaseDate string            `json:"release_date,omitempty"`
	Attachment  *bool             `json:"attachment,omitempty"`
	Reasoning   *bool             `json:"reasoning,omitempty"`
	Temperature *bool             `json:"temperature,omitempty"`
	ToolCall    *bool             `json:"tool_call,omitempty"`
	Status      string            `json:"status,omitempty"`
	Limit       *Limit            `json:"limit,omitempty"`
	Cost        *Cost             `json:"cost,omitempty"`
	Options     map[string]any    `json:"options,omitempty"`
	Headers     map[string]string `json:"headers,omitempty"`
}

type Limit struct {
	Context float64 `json:"context,omitempty"`
	Input   float64 `json:"input,omitempty"`
	Output  float64 `json:"output,omitempty"`
}

type Cost struct {
	Input      float64 `json:"input"`
	Output     float64 `json:"output"`
	CacheRead  float64 `json:"cache_read,omitempty"`
	CacheWrite float64 `json:"cache_write,omitempty"`
}

// Agent mirrors ConfigAgentV1.Info.
type Agent struct {
	Model       string     `json:"model,omitempty"`
	Variant     string     `json:"variant,omitempty"`
	Temperature *float64   `json:"temperature,omitempty"`
	Prompt      string     `json:"prompt,omitempty"`
	Description string     `json:"description,omitempty"`
	Mode        string     `json:"mode,omitempty"`
	Hidden      bool       `json:"hidden,omitempty"`
	Color       string     `json:"color,omitempty"`
	Steps       int        `json:"steps,omitempty"`
	MaxSteps    int        `json:"maxSteps,omitempty"`
	Permission  Permission `json:"permission,omitempty"`
}

// EffectiveSteps honors the maxSteps alias.
func (a Agent) EffectiveSteps() int {
	if a.Steps > 0 {
		return a.Steps
	}
	return a.MaxSteps
}

// ParseModelRef splits a "provider/model" string.
func ParseModelRef(value string) (providerID, modelID string, ok bool) {
	providerID, modelID, found := cut(value, "/")
	return providerID, modelID, found && modelID != ""
}

func cut(value, sep string) (before, after string, found bool) {
	for i := 0; i+len(sep) <= len(value); i++ {
		if value[i:i+len(sep)] == sep {
			return value[:i], value[i+len(sep):], true
		}
	}
	return value, "", false
}

// ProviderDisabled reports whether a provider is in disabled_providers.
func (c *Config) ProviderDisabled(providerID string) bool {
	for _, id := range c.DisabledProviders {
		if id == providerID {
			return true
		}
	}
	return false
}

// ProviderEnabled reports whether enabled_providers permits a provider. An
// empty or absent list permits everything.
func (c *Config) ProviderEnabled(providerID string) bool {
	if len(c.EnabledProviders) == 0 {
		return true
	}
	for _, id := range c.EnabledProviders {
		if id == providerID {
			return true
		}
	}
	return false
}
