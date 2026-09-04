package plugin

import "context"

// Auth and provider registrations, porting the `auth` and `provider` members
// of the `Hooks` interface. These are how the TypeScript build ships login
// flows for Copilot, Codex, Azure and friends: each is a plugin that registers
// an AuthHook rather than special-cased code in the provider layer.
//
// Native tier only. Every field here is a function the host calls back into,
// and an OAuth flow is a conversation — open a URL, wait, exchange a code,
// refresh later — not a single request/response. Modelling that across the
// stdio protocol would mean a callback channel whose only user does not exist
// yet, so process plugins declare tools and trigger hooks, and auth stays with
// plugins compiled into the binary. The protocol reserves room to lift this
// later; nothing in the host assumes the restriction beyond the loader
// refusing to synthesize these from a manifest.

// Auth method types, porting the `type` discriminator on an auth method.
const (
	// AuthOAuth is a browser or device-code flow.
	AuthOAuth = "oauth"
	// AuthAPI is a pasted API key.
	AuthAPI = "api"
)

// Authorization result kinds, porting the `type` field a callback returns.
const (
	AuthSuccess = "success"
	AuthFailed  = "failed"
)

// OAuth callback modes, porting the `method` discriminator on
// `AuthOAuthResult`.
const (
	// AuthModeAuto polls for completion with no user input.
	AuthModeAuto = "auto"
	// AuthModeCode asks the user to paste a code back.
	AuthModeCode = "code"
)

// AuthHook registers login flows for a provider, porting `AuthHook`.
type AuthHook struct {
	// Provider is the provider id these methods authenticate.
	Provider string

	// Loader turns a stored credential into provider options — refreshing an
	// expired token, deriving a base URL from an enterprise host. It is
	// called on every resolution, not just after login. Ports `loader`.
	Loader func(ctx context.Context, credential func() (Credential, error)) (map[string]any, error)

	// Methods are the ways to log in, offered to the user in order.
	Methods []AuthMethod
}

// AuthMethod is one login flow.
type AuthMethod struct {
	// Type is [AuthOAuth] or [AuthAPI].
	Type  string
	Label string
	// Prompts are collected before Authorize runs.
	Prompts []AuthPrompt
	// Authorize starts the flow with the collected answers. For [AuthAPI] it
	// may be nil, which means the pasted key is the credential.
	Authorize func(ctx context.Context, answers map[string]string) (AuthResult, error)
}

// Prompt types, porting the `type` field on a prompt.
const (
	PromptText   = "text"
	PromptSelect = "select"
)

// AuthPrompt is one input collected during login.
type AuthPrompt struct {
	// Type is [PromptText] or [PromptSelect].
	Type        string
	Key         string
	Message     string
	Placeholder string
	// Options are the choices for a select prompt.
	Options []PromptOption
	// Validate rejects a text answer, returning the reason. Nil accepts any.
	Validate func(value string) string
	// When gates the prompt on an earlier answer, porting the `when` rule
	// that replaced the deprecated `condition` predicate.
	When *Rule
}

// PromptOption is one choice of a select prompt.
type PromptOption struct {
	Label string
	Value string
	Hint  string
}

// Rule conditions a prompt on an earlier answer, porting the `Rule` type.
type Rule struct {
	Key string
	// Op is "eq" or "neq".
	Op    string
	Value string
}

// Match evaluates the rule against the answers collected so far. A nil rule
// matches, which is what makes [AuthPrompt.When] optional.
func (r *Rule) Match(answers map[string]string) bool {
	if r == nil {
		return true
	}
	value, ok := answers[r.Key]
	if !ok {
		value = ""
	}
	switch r.Op {
	case "neq":
		return value != r.Value
	default:
		return value == r.Value
	}
}

// AuthResult is what starting a flow returns, porting `AuthOAuthResult` and
// the API-key result union.
//
// For an [AuthAPI] method the result is terminal: Type is [AuthSuccess] with
// Credential set, or [AuthFailed]. For [AuthOAuth] it describes where to send
// the user and how the flow finishes: Mode [AuthModeAuto] means poll Callback
// with an empty code, [AuthModeCode] means collect a code first.
type AuthResult struct {
	Type string
	// URL is where to send the user, for an OAuth flow.
	URL string
	// Instructions is what to tell the user while the flow is open.
	Instructions string
	// Mode is [AuthModeAuto] or [AuthModeCode].
	Mode string
	// Callback completes an OAuth flow. Auto flows ignore code.
	Callback func(ctx context.Context, code string) (Credential, error)
	// Credential is the finished credential, for a terminal result.
	Credential *Credential
}

// Credential is what a completed login stores. It mirrors the shape
// internal/provider persists, kept as a plugin-owned type so this package
// stays below the provider layer in the dependency graph.
type Credential struct {
	// Type is "api", "oauth" or "wellknown".
	Type     string            `json:"type"`
	Key      string            `json:"key,omitempty"`
	Access   string            `json:"access,omitempty"`
	Refresh  string            `json:"refresh,omitempty"`
	Expires  int64             `json:"expires,omitempty"`
	Account  string            `json:"account,omitempty"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

// ProviderHook registers dynamic model discovery for a provider, porting
// `ProviderHook`. It is how a gateway provider advertises the models its
// account can reach, which no baked-in catalog can know.
type ProviderHook struct {
	// ID is the provider these models belong to.
	ID string
	// Models returns the provider's models keyed by model id. The credential
	// is nil when the provider is unauthenticated.
	Models func(ctx context.Context, provider ProviderInfo, credential *Credential) (map[string]ModelInfo, error)
}
