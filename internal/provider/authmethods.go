package provider

import (
	"context"
	"os"

	"github.com/anomalyco/opencode-go/internal/modelsdev"
)

// Method types, matching the `type` discriminator on the auth methods that
// packages/core/src/integration.ts registers.
const (
	MethodEnv   = "env"   // credential comes from an environment variable
	MethodKey   = "key"   // user pastes an API key, stored in auth.json
	MethodOAuth = "oauth" // a browser or device-code flow
)

// Prompt is one input collected during a login flow, ported from the
// `prompts` array on ProviderAuth methods.
type Prompt struct {
	Key     string
	Label   string
	Options []string // non-empty for a select prompt
}

// Method is one way to authenticate with a provider.
type Method struct {
	Type  string
	Label string
	// Env lists the environment variables this method reads, for MethodEnv.
	Env []string
	// Prompts are collected before the method runs, for MethodKey/MethodOAuth.
	Prompts []Prompt
	// Login runs an interactive flow and returns the credential to store.
	// Nil for MethodEnv, which stores nothing, and for MethodKey, whose
	// credential is the pasted value itself.
	Login func(ctx context.Context, answers map[string]string) (Credential, error)
}

// Credential is what a completed login stores in auth.json. It maps onto
// auth.Info's oauth | api | wellknown union.
type Credential struct {
	Type    string // "api" | "oauth" | "wellknown"
	Key     string
	Access  string
	Refresh string
	Expires int64
}

// Methods returns the login methods a provider supports.
//
// This ports the registration in packages/core/src/plugin/models-dev.ts: every
// catalog entry with environment variables gets an env method, every entry
// gets a manual key method, and a provider whose transform implements
// AuthProvider contributes its own on top. That is why a provider nobody wrote
// code for is still loggable — the catalog's `env` array is the auth template.
func Methods(ctx context.Context, providerID string) ([]Method, error) {
	catalog, err := modelsdev.New().Get(ctx)
	if err != nil {
		return nil, err
	}
	entry := catalog[providerID]
	return methodsFor(providerID, entry), nil
}

func methodsFor(providerID string, entry modelsdev.Provider) []Method {
	var out []Method
	for _, t := range transformsFor(providerID, entry) {
		if provider, ok := t.(AuthProvider); ok {
			out = append(out, provider.AuthMethods()...)
		}
	}
	if len(entry.Env) > 0 {
		out = append(out, Method{
			Type:  MethodEnv,
			Label: "Environment variable",
			Env:   entry.Env,
		})
	}
	out = append(out, Method{
		Type:  MethodKey,
		Label: "API key",
	})
	return out
}

// EnvSatisfied reports whether an env method's variables are already set, so
// a login flow can tell the user they need do nothing.
func (m Method) EnvSatisfied() bool {
	if m.Type != MethodEnv || len(m.Env) == 0 {
		return false
	}
	for _, name := range m.Env {
		if os.Getenv(name) == "" {
			return false
		}
	}
	return true
}
