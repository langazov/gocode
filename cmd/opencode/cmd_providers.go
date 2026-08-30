package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/anomalyco/opencode-go/internal/auth"
	"github.com/anomalyco/opencode-go/internal/clix"
	"github.com/anomalyco/opencode-go/internal/global"
	"github.com/anomalyco/opencode-go/internal/modelsdev"
)

// providersCommand mirrors ProvidersCommand in cli/cmd/providers.ts
// ("providers", aliased "auth"). OAuth and plugin-driven login flows are not
// ported (no plugin host yet); a plain API-key login works end to end
// against the same auth.json store the Go provider layer already reads.
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
	if url := a.String("url"); url != "" {
		return notImplemented("opencode providers login <url> (wellknown auth provider)")
	}
	providerID := a.String("provider")
	if providerID == "" {
		return &usageError{msg: "--provider is required (interactive provider selection is not yet implemented in the Go port)"}
	}
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
	fmt.Println("Login successful")
	return nil
}

func runProvidersLogout(a *clix.Args) error {
	providerID := a.PositionalOr("provider", "")
	if providerID == "" {
		return &usageError{msg: "a provider id is required (interactive selection is not yet implemented in the Go port)"}
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
