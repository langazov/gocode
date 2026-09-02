package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/anomalyco/opencode-go/internal/auth"
	"github.com/anomalyco/opencode-go/internal/clix"
	"github.com/anomalyco/opencode-go/internal/global"
	"github.com/anomalyco/opencode-go/internal/modelsdev"
	"github.com/anomalyco/opencode-go/internal/provider"
)

// providersCommand mirrors ProvidersCommand in cli/cmd/providers.ts
// ("providers", aliased "auth").
//
// Login offers whatever methods the provider advertises (see
// provider.Methods): the env and API-key methods every models.dev catalog
// entry gets, plus any OAuth flow its transform registers. `login <url>`
// resolves a .well-known/opencode descriptor. Plugin-contributed methods are
// still out of scope — there is no plugin host yet.
func providersCommand() *clix.Command {
	return &clix.Command{
		Name:     "providers",
		Aliases:  []string{"auth"},
		Describe: "manage AI providers and credentials",
		Demand:   true,
		Sub: []*clix.Command{
			{Name: "list", Aliases: []string{"ls"}, Describe: "list providers and credentials", Run: runProvidersList},
			{
				Name:        "login",
				Describe:    "log in to a provider",
				Positionals: []clix.Positional{{Name: "url", Describe: "opencode auth provider"}},
				Flags: []clix.Flag{
					{Name: "provider", Aliases: []string{"p"}, Kind: clix.KindString, Describe: "provider id or name to log in to (skips provider selection)"},
					{Name: "method", Aliases: []string{"m"}, Kind: clix.KindString, Describe: "login method label (skips method selection)"},
				},
				Run: runProvidersLogin,
			},
			{
				Name:        "logout",
				Describe:    "log out from a configured provider",
				Positionals: []clix.Positional{{Name: "provider", Describe: "provider id or name to log out from"}},
				Run:         runProvidersLogout,
			},
		},
	}
}

func runProvidersList(a *clix.Args) error {
	ctx := context.Background()
	all, err := auth.All()
	if err != nil {
		return err
	}
	authPath := filepath.Join(global.Resolve().Data, "auth.json")
	if home, err := os.UserHomeDir(); err == nil && strings.HasPrefix(authPath, home) {
		authPath = "~" + strings.TrimPrefix(authPath, home)
	}
	fmt.Println("Credentials", authPath)

	catalog, _ := modelsdev.New().Get(ctx)
	ids := make([]string, 0, len(all))
	for id := range all {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		name := id
		if entry, ok := catalog[id]; ok && entry.Name != "" {
			name = entry.Name
		}
		fmt.Printf("%s %s\n", name, all[id].Type)
	}
	fmt.Printf("%d credentials\n", len(ids))

	var active [][2]string
	for providerID, entry := range catalog {
		for _, envVar := range entry.Env {
			if os.Getenv(envVar) != "" {
				name := entry.Name
				if name == "" {
					name = providerID
				}
				active = append(active, [2]string{name, envVar})
			}
		}
	}
	if len(active) > 0 {
		fmt.Println()
		fmt.Println("Environment")
		for _, pair := range active {
			fmt.Printf("%s %s\n", pair[0], pair[1])
		}
		fmt.Printf("%d environment variable(s)\n", len(active))
	}
	return nil
}

func runProvidersLogin(a *clix.Args) error {
	ctx := context.Background()
	if url := a.PositionalOr("url", ""); url != "" {
		return wellKnownLogin(ctx, url)
	}

	providerID := a.String("provider")
	if providerID == "" {
		selected, err := selectProvider(ctx)
		if err != nil {
			return err
		}
		providerID = selected
	}

	methods, err := provider.Methods(ctx, providerID)
	if err != nil {
		return err
	}
	method, err := selectMethod(methods, a.String("method"))
	if err != nil {
		return err
	}

	switch method.Type {
	case provider.MethodEnv:
		if method.EnvSatisfied() {
			fmt.Printf("%s is already configured from the environment (%s)\n", providerID, strings.Join(method.Env, ", "))
			return nil
		}
		fmt.Printf("Set one of these environment variables to use %s: %s\n", providerID, strings.Join(method.Env, ", "))
		return nil

	case provider.MethodOAuth:
		answers, err := askPrompts(method.Prompts)
		if err != nil {
			return err
		}
		credential, err := method.Login(ctx, answers)
		if err != nil {
			return err
		}
		info := auth.Info{
			Type:    credential.Type,
			Key:     credential.Key,
			Access:  credential.Access,
			Refresh: credential.Refresh,
			Expires: credential.Expires,
		}
		if domain := answers["enterpriseUrl"]; domain != "" && answers["deploymentType"] == "enterprise" {
			info.EnterpriseURL = domain
		}
		if err := auth.Set(providerID, info); err != nil {
			return err
		}

	default:
		fmt.Printf("Enter your API key for %s: ", providerID)
		reader := bufio.NewReader(os.Stdin)
		line, _ := reader.ReadString('\n')
		key := strings.TrimSpace(line)
		if key == "" {
			return &usageError{msg: "API key is required"}
		}
		if err := auth.Set(providerID, auth.Info{Type: "api", Key: key}); err != nil {
			return err
		}
	}

	fmt.Println("Login successful")
	return nil
}

// selectProvider lists the catalog's providers and asks which one to use,
// replacing the previous hard requirement for --provider.
func selectProvider(ctx context.Context) (string, error) {
	catalog, err := modelsdev.New().Get(ctx)
	if err != nil {
		return "", err
	}
	if len(catalog) == 0 {
		return "", &usageError{msg: "no providers are known (the models.dev catalog is empty) — pass --provider"}
	}
	ids := make([]string, 0, len(catalog))
	for id := range catalog {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	fmt.Println("Select a provider:")
	for i, id := range ids {
		name := catalog[id].Name
		if name == "" {
			name = id
		}
		fmt.Printf("%4d) %-28s %s\n", i+1, id, name)
	}
	choice, err := askLine(fmt.Sprintf("Provider [1-%d or id]: ", len(ids)))
	if err != nil {
		return "", err
	}
	if index, err := strconv.Atoi(choice); err == nil {
		if index < 1 || index > len(ids) {
			return "", &usageError{msg: fmt.Sprintf("choice out of range: %d", index)}
		}
		return ids[index-1], nil
	}
	if _, ok := catalog[choice]; !ok {
		return "", fmt.Errorf("unknown provider %q", choice)
	}
	return choice, nil
}

// selectMethod picks a login method, honoring --method when given and
// skipping the prompt when there is only one.
func selectMethod(methods []provider.Method, wanted string) (provider.Method, error) {
	if len(methods) == 0 {
		return provider.Method{}, fmt.Errorf("no login methods available")
	}
	if wanted != "" {
		for _, method := range methods {
			if strings.EqualFold(method.Label, wanted) || strings.EqualFold(method.Type, wanted) {
				return method, nil
			}
		}
		return provider.Method{}, fmt.Errorf("unknown login method %q", wanted)
	}
	if len(methods) == 1 {
		return methods[0], nil
	}

	fmt.Println("Select a login method:")
	for i, method := range methods {
		suffix := ""
		if method.Type == provider.MethodEnv {
			suffix = " (" + strings.Join(method.Env, ", ") + ")"
		}
		fmt.Printf("%4d) %s%s\n", i+1, method.Label, suffix)
	}
	choice, err := askLine(fmt.Sprintf("Method [1-%d]: ", len(methods)))
	if err != nil {
		return provider.Method{}, err
	}
	index, err := strconv.Atoi(choice)
	if err != nil || index < 1 || index > len(methods) {
		return provider.Method{}, &usageError{msg: fmt.Sprintf("invalid method choice %q", choice)}
	}
	return methods[index-1], nil
}

func askPrompts(prompts []provider.Prompt) (map[string]string, error) {
	answers := map[string]string{}
	for _, prompt := range prompts {
		label := prompt.Label
		if len(prompt.Options) > 0 {
			label += " [" + strings.Join(prompt.Options, "/") + "]"
		}
		value, err := askLine(label + ": ")
		if err != nil {
			return nil, err
		}
		answers[prompt.Key] = value
	}
	return answers, nil
}

func askLine(prompt string) (string, error) {
	fmt.Print(prompt)
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && line == "" {
		return "", err
	}
	return strings.TrimSpace(line), nil
}

func runProvidersLogout(a *clix.Args) error {
	providerID := a.PositionalOr("provider", "")
	if providerID == "" {
		all, err := auth.All()
		if err != nil {
			return err
		}
		if len(all) == 0 {
			return &usageError{msg: "no providers are logged in"}
		}
		ids := make([]string, 0, len(all))
		for id := range all {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		fmt.Println("Select a provider to log out from:")
		for i, id := range ids {
			fmt.Printf("%4d) %s\n", i+1, id)
		}
		choice, err := askLine(fmt.Sprintf("Provider [1-%d or id]: ", len(ids)))
		if err != nil {
			return err
		}
		if index, err := strconv.Atoi(choice); err == nil {
			if index < 1 || index > len(ids) {
				return &usageError{msg: fmt.Sprintf("choice out of range: %d", index)}
			}
			providerID = ids[index-1]
		} else {
			providerID = choice
		}
	}
	all, err := auth.All()
	if err != nil {
		return err
	}
	if _, ok := all[providerID]; !ok {
		return fmt.Errorf("Unknown configured provider %q", providerID)
	}
	if err := auth.Remove(providerID); err != nil {
		return err
	}
	fmt.Println("Logout successful")
	return nil
}
