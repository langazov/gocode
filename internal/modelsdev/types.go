package modelsdev

import (
	"encoding/json"
	"fmt"
)

type Catalog map[string]Provider

type Provider struct {
	API    string           `json:"api,omitempty"`
	Name   string           `json:"name"`
	Env    []string         `json:"env"`
	ID     string           `json:"id"`
	NPM    string           `json:"npm,omitempty"`
	Models map[string]Model `json:"models"`
	// Whitelist restricts Models to exactly these ids when non-empty. It has
	// no equivalent in the public models.dev catalog; a CatalogOverlay sets
	// it when the account it represents is entitled to a strict subset of
	// what the provider otherwise lists (see opencode/Zen's /api/config).
	Whitelist []string `json:"-"`
}

type Model struct {
	ID               string            `json:"id"`
	Name             string            `json:"name"`
	Family           string            `json:"family,omitempty"`
	ReleaseDate      string            `json:"release_date"`
	Attachment       bool              `json:"attachment"`
	Reasoning        bool              `json:"reasoning"`
	Temperature      bool              `json:"temperature"`
	ToolCall         bool              `json:"tool_call"`
	ReasoningOptions []ReasoningOption `json:"reasoning_options,omitempty"`
	Interleaved      *Interleaved      `json:"interleaved,omitempty"`
	Cost             *Cost             `json:"cost,omitempty"`
	Limit            Limit             `json:"limit"`
	Modalities       *Modalities       `json:"modalities,omitempty"`
	Experimental     *Experimental     `json:"experimental,omitempty"`
	Status           string            `json:"status,omitempty"`
	Provider         *ProviderOverride `json:"provider,omitempty"`
}

type Rates struct {
	Input      float64  `json:"input"`
	Output     float64  `json:"output"`
	CacheRead  *float64 `json:"cache_read,omitempty"`
	CacheWrite *float64 `json:"cache_write,omitempty"`
}

type CostTier struct {
	Rates
	Tier struct {
		Type string  `json:"type"`
		Size float64 `json:"size"`
	} `json:"tier"`
}

type Cost struct {
	Rates
	Tiers           []CostTier `json:"tiers,omitempty"`
	ContextOver200k *Rates     `json:"context_over_200k,omitempty"`
}

type Limit struct {
	Context float64  `json:"context"`
	Input   *float64 `json:"input,omitempty"`
	Output  float64  `json:"output"`
}

type Modalities struct {
	Input  []string `json:"input"`
	Output []string `json:"output"`
}

// ReasoningOption flattens the tagged union from the TypeScript schema:
// {type:"effort", values} | {type:"toggle"} | {type:"budget_tokens", min, max}.
type ReasoningOption struct {
	Type   string    `json:"type"`
	Values []*string `json:"values,omitempty"`
	Min    *float64  `json:"min,omitempty"`
	Max    *float64  `json:"max,omitempty"`
}

// Interleaved accepts the union boolean | string | {field: string}.
type Interleaved struct {
	Enabled bool
	Field   string
}

func (i *Interleaved) UnmarshalJSON(data []byte) error {
	var b bool
	if err := json.Unmarshal(data, &b); err == nil {
		i.Enabled = b
		return nil
	}
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		i.Enabled = true
		i.Field = s
		return nil
	}
	var obj struct {
		Field string `json:"field"`
	}
	if err := json.Unmarshal(data, &obj); err == nil {
		i.Enabled = true
		i.Field = obj.Field
		return nil
	}
	return fmt.Errorf("modelsdev: cannot decode interleaved from %s", data)
}

func (i Interleaved) MarshalJSON() ([]byte, error) {
	if i.Field != "" {
		return json.Marshal(i.Field)
	}
	return json.Marshal(i.Enabled)
}

type Experimental struct {
	Modes map[string]ExperimentalMode `json:"modes,omitempty"`
}

type ExperimentalMode struct {
	Cost     *Cost             `json:"cost,omitempty"`
	Provider *ExperimentalBody `json:"provider,omitempty"`
}

type ExperimentalBody struct {
	Body    map[string]any    `json:"body,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
}

type ProviderOverride struct {
	NPM string `json:"npm,omitempty"`
	API string `json:"api,omitempty"`
}
