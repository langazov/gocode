package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/langazov/gocode-go/internal/acp"
	"github.com/langazov/gocode-go/internal/auth"
	"github.com/langazov/gocode-go/internal/browser"
	"github.com/langazov/gocode-go/internal/clix"
	"github.com/langazov/gocode-go/internal/config"
	"github.com/langazov/gocode-go/internal/global"
	"github.com/langazov/gocode-go/internal/gocoder"
	"github.com/langazov/gocode-go/internal/loginpage"
	"github.com/langazov/gocode-go/internal/modelsdev"
	"github.com/langazov/gocode-go/internal/modelstate"
	"github.com/langazov/gocode-go/internal/provider"
	"github.com/langazov/gocode-go/internal/sync"
	"github.com/langazov/gocode-go/internal/tui/signin"
)

// ACP login method ids. The protocol-driven ones run here, over the
// connection; the terminal ones relaunch `gocode acp` with --login.
const (
	acpGocoderLogin    = "gocoder-login"
	acpGocoderRegister = "gocoder-register"
	acpProviderKey     = "provider-api-key"
	// acpOAuthPrefix + "<provider>:<n>" is a provider's nth OAuth method.
	acpOAuthPrefix = "oauth:"

	acpGocoderTerminalLogin    = "gocoder-terminal-login"
	acpGocoderTerminalRegister = "gocoder-terminal-register"
)

// --login-target values: which interactive flow `gocode acp --login` runs.
const (
	loginTargetProvider        = "provider"
	loginTargetGocoder         = "gocoder"
	loginTargetGocoderRegister = "gocoder-register"
)

// runACPLogin is the terminal side of ACP authentication: the client
// relaunched `gocode acp` with a terminal method's args in an interactive
// terminal. Exit status is the client's success signal.
func runACPLogin(target string) error {
	switch target {
	case "", loginTargetProvider:
		return runProvidersLogin(emptyArgs())
	case loginTargetGocoder:
		return runGocoderSignIn(false)
	case loginTargetGocoderRegister:
		return runGocoderSignIn(true)
	}
	return &usageError{msg: fmt.Sprintf("unknown --login-target %q", target)}
}

func emptyArgs() *clix.Args {
	return &clix.Args{
		Bools:    map[string]bool{},
		Strings:  map[string]string{},
		Numbers:  map[string]float64{},
		Arrays:   map[string][]string{},
		Pos:      map[string]string{},
		PosArray: map[string][]string{},
	}
}

// acpAuth answers ACP authentication from gocode's own credential stores:
// auth.json for model providers, gocoder.json for the gocoder.org account.
//
// Secrets never cross the protocol. Form elicitation must not carry them
// (protocol/v1/elicitation "Form mode"), so a password or API key is typed
// into a one-time page on the loopback interface (internal/loginpage) that
// the client opens through URL-mode elicitation; OAuth flows hand the
// provider's own consent page to the client the same way.
type acpAuth struct {
	directory string
	modelFlag string
}

// providerID is the default model's provider for the directory, resolved
// with the same precedence boot uses.
func (a *acpAuth) providerID() (string, *config.Config, error) {
	cfg, err := config.LoadFor(a.directory)
	if err != nil {
		return "", nil, err
	}
	var lastUsed modelstate.Ref
	if ref, ok := modelstate.Load(a.directory); ok {
		lastUsed = ref
	}
	model := resolveModelFlag(a.modelFlag, lastUsed, cfg.Model)
	providerID, _, ok := strings.Cut(model, "/")
	if !ok {
		return "", nil, fmt.Errorf("invalid model %q", model)
	}
	return providerID, cfg, nil
}

func (a *acpAuth) Authenticated(ctx context.Context) bool {
	providerID, cfg, err := a.providerID()
	if err != nil {
		return false
	}
	if _, err := provider.FromConfig(ctx, providerID, cfg); err == nil {
		return true
	}
	// Boot falls back to any provider with credentials; so does this.
	_, _, ok := provider.Fallback(ctx, providerID, "", cfg)
	return ok
}

// Logout signs out of the default model's provider. For the gocoder
// provider that is the gocoder.org account itself, whose key lives in
// gocoder.json rather than auth.json.
func (a *acpAuth) Logout(ctx context.Context) error {
	providerID, _, err := a.providerID()
	if err != nil {
		return err
	}
	defer provider.InvalidateModelCaches()
	if providerID == "gocoder" {
		return runGocoderLogout(ctx, io.Discard)
	}
	return auth.Remove(providerID)
}

// oauthMethod is one provider OAuth flow as ACP offers it.
type oauthMethod struct {
	id         string
	providerID string
	name       string
	method     provider.Method
}

// oauthMethods lists every OAuth flow a provider transform registers
// (ChatGPT, GitHub Copilot, OpenCode, ...), in provider order.
func oauthMethods(ctx context.Context) []oauthMethod {
	catalog, err := modelsdev.New().Get(ctx)
	if err != nil {
		return nil
	}
	ids := make([]string, 0, len(catalog))
	for id := range catalog {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var out []oauthMethod
	for _, id := range ids {
		n := 0
		for _, method := range provider.MethodsIn(catalog, id) {
			if method.Type != provider.MethodOAuth || method.Login == nil {
				continue
			}
			out = append(out, oauthMethod{
				id:         acpOAuthPrefix + id + ":" + strconv.Itoa(n),
				providerID: id,
				name:       firstNonEmptyString(catalog[id].Name, id),
				method:     method,
			})
			n++
		}
	}
	return out
}

func (a *acpAuth) Methods(ctx context.Context) []acp.AuthMethod {
	site := displayHost(gocoder.BaseURL())
	methods := []acp.AuthMethod{
		{
			ID:          acpGocoderLogin,
			Name:        "Log in to " + site,
			Description: "Sign in to your " + site + " account in the browser; its free models become available.",
			NeedsURL:    true,
		},
		{
			ID:          acpGocoderRegister,
			Name:        "Create a " + site + " account",
			Description: "Sign up for free in the browser — just an email and a password.",
			NeedsURL:    true,
		},
	}
	for _, flow := range oauthMethods(ctx) {
		methods = append(methods, acp.AuthMethod{
			ID:          flow.id,
			Name:        flow.name + ": " + flow.method.Label,
			Description: "Sign in to " + flow.name + " in the browser.",
			NeedsURL:    true,
		})
	}
	methods = append(methods,
		acp.AuthMethod{
			ID:          acpProviderKey,
			Name:        "Enter a provider API key",
			Description: "Pick any model provider and paste its API key into a local page in the browser.",
			NeedsURL:    true,
		},
		acp.AuthMethod{
			ID:          acpGocoderTerminalLogin,
			Name:        "Log in to " + site + " from the terminal",
			Description: "Sign in to your " + site + " account interactively.",
			Args:        []string{"--login", "--login-target=" + loginTargetGocoder},
		},
		acp.AuthMethod{
			ID:          acpGocoderTerminalRegister,
			Name:        "Create a " + site + " account from the terminal",
			Description: "Sign up for " + site + " interactively.",
			Args:        []string{"--login", "--login-target=" + loginTargetGocoderRegister},
		},
	)
	return methods
}

func (a *acpAuth) Login(ctx context.Context, methodID string, ui acp.LoginUI) error {
	switch methodID {
	case acpGocoderLogin:
		return a.gocoderLogin(ctx, false, ui)
	case acpGocoderRegister:
		return a.gocoderLogin(ctx, true, ui)
	case acpProviderKey:
		return a.providerKeyLogin(ctx, ui)
	}
	if strings.HasPrefix(methodID, acpOAuthPrefix) {
		for _, flow := range oauthMethods(ctx) {
			if flow.id == methodID {
				return oauthLogin(ctx, flow, ui)
			}
		}
	}
	return fmt.Errorf("unknown login method %q", methodID)
}

// openLoginPage puts url in front of the user: through the client when it
// supports URL elicitation, otherwise in a browser the agent opens itself —
// it runs on the user's machine.
func openLoginPage(ctx context.Context, ui acp.LoginUI, url, message string) error {
	err := ui.OpenURL(ctx, url, message)
	if !errors.Is(err, acp.ErrNoURL) {
		return err
	}
	if err := browser.Open(url); err != nil {
		return fmt.Errorf("could not open a browser (%v); run `gocode login` or `gocode auth login` in a terminal instead", err)
	}
	global.LogBackground("acp: login page opened in the browser")
	return nil
}

// gocoderLogin signs in to (or signs up for) gocoder.org through a loopback
// page: the same exchange as `gocode login` — session token → named API key
// → gocoder.json, plus the settings-sync restore, which needs the password
// and so can only happen here.
func (a *acpAuth) gocoderLogin(ctx context.Context, register bool, ui acp.LoginUI) error {
	client := gocoder.NewClient("")
	site := displayHost(client.BaseURL)
	previous, err := gocoder.LoadAccount()
	if err != nil {
		return err
	}
	var restore sync.RestoreOutcome
	submit := submitter(client, func(o sync.RestoreOutcome) { restore = o })

	form := loginpage.Form{
		Title:  "Log in to " + site,
		Intro:  "Signs gocode in on this machine. Your password goes straight to " + site + "; your editor never sees it.",
		Submit: "Log in",
		Fields: []loginpage.Field{
			{Name: "email", Label: "Email", Type: "email", Required: true},
			{Name: "password", Label: "Password", Type: "password", Required: true},
		},
	}
	if register {
		form = loginpage.Form{
			Title:  "Create a " + site + " account",
			Intro:  "Free — just an email and a password. Your editor never sees the password.",
			Submit: "Create account",
			Fields: []loginpage.Field{
				{Name: "email", Label: "Email", Type: "email", Required: true},
				{Name: "name", Label: "Display name (optional)", Type: "text"},
				{Name: "password", Label: "Password (8–128 characters)", Type: "password", Required: true},
				{Name: "confirm", Label: "Confirm password", Type: "password", Required: true},
			},
		}
	}
	if previous != nil {
		form.Intro = fmt.Sprintf("Signed in as %s — continuing replaces that account. ", previous.Email) + form.Intro
	}

	page, err := loginpage.Serve(ctx, form, func(ctx context.Context, v map[string]string) (string, error) {
		validate := func() (signin.Credentials, int, error) {
			return signin.ValidateLogin(v["email"], v["password"])
		}
		if register {
			validate = func() (signin.Credentials, int, error) {
				return signin.ValidateRegister(v["email"], v["name"], v["password"], v["confirm"])
			}
		}
		credentials, _, err := validate()
		if err != nil {
			return "", err
		}
		result, err := submit(ctx, register, credentials)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("Signed in to %s as %s.\nYou can close this window and return to your editor.", site, result.Email), nil
	})
	if err != nil {
		return err
	}
	defer page.Close()

	verb := "Log in to"
	if register {
		verb = "Create your account on"
	}
	if err := openLoginPage(ctx, ui, page.URL, verb+" "+site+" in your browser."); err != nil {
		return err
	}
	if err := page.Wait(ctx); err != nil {
		return err
	}
	ui.Done()
	retirePrevious(ctx, previous, os.Stderr)
	reportRestore(restore)
	provider.InvalidateModelCaches()
	return nil
}

// providerKeyLogin stores a pasted API key for any catalog provider, typed
// into a loopback page. The default model's provider is preselected.
func (a *acpAuth) providerKeyLogin(ctx context.Context, ui acp.LoginUI) error {
	catalog, err := modelsdev.New().Get(ctx)
	if err != nil {
		return err
	}
	if len(catalog) == 0 {
		return errors.New("no providers are known (the models.dev catalog is empty)")
	}
	options := make([]loginpage.Option, 0, len(catalog))
	for id, entry := range catalog {
		options = append(options, loginpage.Option{Value: id, Label: firstNonEmptyString(entry.Name, id)})
	}
	sort.Slice(options, func(i, j int) bool {
		return strings.ToLower(options[i].Label) < strings.ToLower(options[j].Label)
	})
	selected, _, _ := a.providerID()

	var savedName string
	page, err := loginpage.Serve(ctx, loginpage.Form{
		Title:  "Connect a model provider",
		Intro:  "The key is stored in gocode's credential file on this machine; your editor never sees it.",
		Submit: "Save API key",
		Fields: []loginpage.Field{
			{Name: "provider", Label: "Provider", Type: "select", Options: options, Value: selected, Required: true},
			{Name: "key", Label: "API key", Type: "password", Required: true},
		},
	}, func(_ context.Context, v map[string]string) (string, error) {
		providerID, key := v["provider"], strings.TrimSpace(v["key"])
		entry, ok := catalog[providerID]
		if !ok {
			return "", fmt.Errorf("unknown provider %q", providerID)
		}
		if key == "" {
			return "", errors.New("API key is required")
		}
		if err := auth.Set(providerID, auth.Info{Type: "api", Key: key}); err != nil {
			return "", err
		}
		savedName = firstNonEmptyString(entry.Name, providerID)
		return fmt.Sprintf("Saved your %s API key.\nYou can close this window and return to your editor.", savedName), nil
	})
	if err != nil {
		return err
	}
	defer page.Close()
	if err := openLoginPage(ctx, ui, page.URL, "Enter a model provider's API key in your browser."); err != nil {
		return err
	}
	if err := page.Wait(ctx); err != nil {
		return err
	}
	ui.Done()
	provider.InvalidateModelCaches()
	global.LogBackground("acp: stored an API key for %s", savedName)
	return nil
}

// oauthLogin runs a provider's OAuth flow — a PKCE redirect or a device
// code — handing its consent page to the client. Non-secret prompts (the
// Copilot deployment type) are asked through form elicitation; a client
// without forms gets each prompt's default.
func oauthLogin(ctx context.Context, flow oauthMethod, ui acp.LoginUI) error {
	answers := map[string]string{}
	if len(flow.method.Prompts) > 0 {
		fields := make([]acp.FormField, 0, len(flow.method.Prompts))
		for _, prompt := range flow.method.Prompts {
			fields = append(fields, acp.FormField{Key: prompt.Key, Label: prompt.Label, Options: prompt.Options, Optional: len(prompt.Options) == 0})
		}
		got, ok, err := ui.Ask(ctx, flow.name+": "+flow.method.Label, fields)
		switch {
		case errors.Is(err, acp.ErrNoForm):
			for _, prompt := range flow.method.Prompts {
				if len(prompt.Options) > 0 {
					answers[prompt.Key] = prompt.Options[0]
				}
			}
		case err != nil:
			return err
		case !ok:
			return acp.ErrDeclined
		default:
			answers = got
		}
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var openErr error
	loginCtx := provider.WithLoginPrompt(ctx, func(p provider.LoginPrompt) {
		message := "Sign in to " + flow.name + " in your browser."
		if p.Code != "" {
			message = fmt.Sprintf("Sign in to %s in your browser. If asked for a code, enter %s.", flow.name, p.Code)
		}
		if err := openLoginPage(ctx, ui, p.URL, message); err != nil {
			// The flow is waiting on a browser that will never come back.
			openErr = err
			cancel()
		}
	})
	credential, err := flow.method.Login(loginCtx, answers)
	if openErr != nil {
		return openErr
	}
	if err != nil {
		return err
	}
	if err := storeOAuthCredential(flow.providerID, credential, answers); err != nil {
		return err
	}
	ui.Done()
	provider.InvalidateModelCaches()
	return nil
}

// storeOAuthCredential saves a completed OAuth login to auth.json.
func storeOAuthCredential(providerID string, credential provider.Credential, answers map[string]string) error {
	info := auth.Info{
		Type:      credential.Type,
		Key:       credential.Key,
		Access:    credential.Access,
		Refresh:   credential.Refresh,
		Expires:   credential.Expires,
		AccountID: credential.AccountID,
		Metadata:  credential.Metadata,
	}
	if domain := answers["enterpriseUrl"]; domain != "" && answers["deploymentType"] == "enterprise" {
		info.EnterpriseURL = domain
	}
	return auth.Set(providerID, info)
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
