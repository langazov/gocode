package tui

import (
	"regexp"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/langazov/gocode-go/internal/tui/client"
)

// customProviderID ports CUSTOM_PROVIDER_ID in dialog-provider.tsx.
var customProviderID = regexp.MustCompile(`^[a-z0-9][a-z0-9-_]*$`)

// providersOverlay ports DialogProvider: "Connect a provider", every catalog
// provider grouped into Popular and Providers, a ✓ beside the ones already
// holding a credential, and an "Other" row for a provider the catalog does
// not list.
// It opens from the cached provider list and refreshes in the background, for
// the same reason modelsOverlay does.
func (a *App) providersOverlay() tea.Cmd {
	a.openProviderDialog()
	return a.loadAllProvidersCmd()
}

// openProviderDialog renders the connect dialog from the unfiltered provider
// list. Shared with refreshOpenCatalogDialog so opening and refreshing cannot
// disagree about which list to read or what to say when it is empty.
func (a *App) openProviderDialog() {
	providers := a.allProviders
	var items []overlayItem
	// providerItems always appends the "Other" row, which would leave the
	// dialog showing a lone "Custom provider" entry and suppress the empty
	// state entirely — offering a custom provider is not a useful answer
	// while the real list is still loading or has failed.
	if len(providers) > 0 {
		items = a.providerItems(providers)
	}
	a.openList("Connect a provider", items)
	o := a.overlay
	o.size = dialogLarge
	if len(providers) == 0 {
		switch {
		case a.allProvidersErr != "":
			o.emptyTitle = "Could not load providers"
			o.emptyBody = a.allProvidersErr
		case !a.allProvidersLoaded:
			o.emptyTitle = "Loading providers"
			o.emptyBody = "Fetching the provider list..."
		default:
			o.emptyTitle = "No providers found"
			o.emptyBody = "The model catalog returned no providers."
		}
	}
}

// loadAllProvidersCmd refreshes the full provider list, including ones with no
// credential yet, which is what the connect dialog lists.
func (a *App) loadAllProvidersCmd() tea.Cmd {
	c := a.client
	return func() tea.Msg {
		providers, err := c.AllProviders(a.ctx)
		if err != nil {
			return providerListMsg{err: err}
		}
		return providerListMsg{providers: providers}
	}
}

// providerListMsg carries a refreshed provider list, or the error that stopped
// one arriving — reported rather than dropped, so a failure is not rendered as
// "you have no providers".
type providerListMsg struct {
	providers []client.Provider
	err       error
}

// providerItems ports providerOptions(): priority order, then name.
func (a *App) providerItems(providers []client.Provider) []overlayItem {
	sorted := append([]client.Provider(nil), providers...)
	sort.SliceStable(sorted, func(i, j int) bool {
		pi, oki := providerPriority[sorted[i].ID]
		pj, okj := providerPriority[sorted[j].ID]
		if !oki {
			pi = 99
		}
		if !okj {
			pj = 99
		}
		if pi != pj {
			return pi < pj
		}
		ni, nj := strings.ToLower(providerLabel(sorted[i])), strings.ToLower(providerLabel(sorted[j]))
		if ni != nj {
			return ni < nj
		}
		return sorted[i].ID < sorted[j].ID
	})

	items := make([]overlayItem, 0, len(sorted)+1)
	for _, entry := range sorted {
		entry := entry
		category := "Providers"
		if _, popular := providerPriority[entry.ID]; popular {
			category = "Popular"
		}
		item := overlayItem{
			label:    providerLabel(entry),
			hint:     providerBlurb[entry.ID],
			value:    entry.ID,
			category: category,
			action:   func() tea.Msg { return a.beginProviderLogin(entry.ID, providerLabel(entry)) },
		}
		if entry.Connected {
			item.gutter, item.gutterOK = "✓", true
		}
		items = append(items, item)
	}

	items = append(items, overlayItem{
		label:    "Other",
		hint:     "Custom provider",
		value:    customProviderValue,
		category: "Providers",
		action:   func() tea.Msg { return a.promptCustomProvider() },
	})
	return items
}

func providerLabel(entry client.Provider) string {
	if entry.Name != "" {
		return entry.Name
	}
	return entry.ID
}

// promptCustomProvider ports promptCustomProviderID(): ask for an id, validate
// it, then collect a key for it.
func (a *App) promptCustomProvider() tea.Msg {
	a.openInput("Other", "Provider id", func(value string) tea.Msg {
		id := strings.TrimPrefix(strings.TrimSpace(value), "@ai-sdk/")
		if !customProviderID.MatchString(id) {
			return statusMsg{
				text:  "Provider ids must start with a lowercase letter or number and use only lowercase letters, numbers, hyphens and underscores",
				isErr: true,
			}
		}
		// This only stores a credential; the provider itself still has to be
		// described in gocode.json. Same caveat the original prints.
		return a.promptAPIKey(id, id+" (configure the provider in gocode.json to use it)")
	})
	return nil
}

// beginProviderLogin picks the login method, porting the onSelect handler:
// with one method it runs straight away, with several it asks first.
func (a *App) beginProviderLogin(providerID, name string) tea.Msg {
	methods, err := a.client.AuthMethods(a.ctx, providerID)
	if err != nil {
		return statusMsg{text: "failed to load login methods: " + err.Error(), isErr: true}
	}
	if len(methods) == 0 {
		return a.promptAPIKey(providerID, name)
	}
	if len(methods) == 1 {
		return a.runAuthMethod(providerID, name, methods[0])
	}

	items := make([]overlayItem, 0, len(methods))
	for _, method := range methods {
		method := method
		hint := ""
		if method.Type == authTypeEnv {
			hint = strings.Join(method.Env, ", ")
			if method.Satisfied {
				hint += " (already set)"
			}
		}
		items = append(items, overlayItem{
			label:  method.Label,
			hint:   hint,
			value:  method.Label,
			action: func() tea.Msg { return a.runAuthMethod(providerID, name, method) },
		})
	}
	a.openList("Select auth method", items)
	return nil
}

// promptAPIKey collects a key and stores it, the port of ApiMethod.
func (a *App) promptAPIKey(providerID, name string) tea.Msg {
	a.openInput("API key for "+name, "Paste your API key", func(value string) tea.Msg {
		key := strings.TrimSpace(value)
		if key == "" {
			return statusMsg{text: "API key is required", isErr: true}
		}
		if err := a.client.SetProviderKey(a.ctx, providerID, key); err != nil {
			return statusMsg{text: "login failed: " + err.Error(), isErr: true}
		}
		return providerConnectedMsg{name: name}
	})
	return nil
}

// providerConnectedMsg reopens the model dialog once a provider is connected,
// mirroring the original returning to the model list after a login.
type providerConnectedMsg struct{ name string }

const (
	authTypeEnv   = "env"
	authTypeOAuth = "oauth"
)

func (a *App) runAuthMethod(providerID, name string, method client.AuthMethod) tea.Msg {
	switch method.Type {
	case authTypeEnv:
		if method.Satisfied {
			return statusMsg{text: name + " is already configured from " + strings.Join(method.Env, ", ")}
		}
		return statusMsg{text: "Set " + strings.Join(method.Env, " or ") + " to use " + name}
	case authTypeOAuth:
		return a.runOAuthMethod(providerID, name, method, map[string]string{}, 0)
	default:
		return a.promptAPIKey(providerID, name)
	}
}

// runOAuthMethod collects the method's prompts one at a time, then starts the
// flow. Each answered prompt re-enters with the next index, since the dialog
// surface handles one input at a time.
func (a *App) runOAuthMethod(providerID, name string, method client.AuthMethod, answers map[string]string, index int) tea.Msg {
	if index < len(method.Prompts) {
		prompt := method.Prompts[index]
		label := prompt.Label
		if len(prompt.Options) > 0 {
			label += " (" + strings.Join(prompt.Options, "/") + ")"
		}
		a.openInput(label, "", func(value string) tea.Msg {
			answers[prompt.Key] = strings.TrimSpace(value)
			return a.runOAuthMethod(providerID, name, method, answers, index+1)
		})
		return nil
	}

	attempt, err := a.client.StartOAuth(a.ctx, providerID, method.Label, answers)
	if err != nil {
		return statusMsg{text: "login failed: " + err.Error(), isErr: true}
	}
	if attempt.Status == "failed" {
		return statusMsg{text: "login failed: " + attempt.Error, isErr: true}
	}
	a.showOAuthWait(name, attempt)
	return oauthPollMsg{attemptID: attempt.ID, providerID: providerID, name: name}
}

// showOAuthWait puts the verification URL and code on screen while the flow
// completes, the port of AutoMethod/CodeMethod's waiting panel.
func (a *App) showOAuthWait(name string, attempt *client.OAuthAttempt) {
	var body strings.Builder
	if attempt.URL != "" {
		body.WriteString("Open this URL to continue:\n\n")
		body.WriteString(attempt.URL)
	}
	if attempt.Code != "" {
		body.WriteString("\n\nEnter code: " + attempt.Code)
	}
	if body.Len() == 0 {
		body.WriteString("Starting authorization...")
	}
	body.WriteString("\n\nWaiting for authorization to complete...")

	a.openList(name, nil)
	o := a.overlay
	o.size = dialogLarge
	o.locked = true
	o.hideFilter = true
	o.emptyTitle = name
	o.emptyBody = body.String()
}

// oauthPollMsg drives the wait loop.
type oauthPollMsg struct {
	attemptID  string
	providerID string
	name       string
}

// pollOAuth checks an in-flight login once and reschedules itself while it is
// still pending.
func (a *App) pollOAuth(msg oauthPollMsg) tea.Cmd {
	return tea.Tick(time.Second, func(time.Time) tea.Msg {
		attempt, err := a.client.OAuthStatus(a.ctx, msg.attemptID)
		if err != nil {
			return statusMsg{text: "login failed: " + err.Error(), isErr: true}
		}
		switch attempt.Status {
		case "done":
			return oauthDoneMsg{name: msg.name}
		case "failed":
			return oauthFailedMsg{name: msg.name, err: attempt.Error}
		default:
			return msg
		}
	})
}

type oauthDoneMsg struct{ name string }

type oauthFailedMsg struct {
	name string
	err  string
}
