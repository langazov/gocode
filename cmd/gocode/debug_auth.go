package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/langazov/gocode-go/internal/auth"
	"github.com/langazov/gocode-go/internal/config"
	"github.com/langazov/gocode-go/internal/global"
	"github.com/langazov/gocode-go/internal/modelsdev"
)

// debugAuthCmd prints the resolved auth.json location, its contents (secrets
// redacted), and the credential resolution chain for a provider.
func debugAuthCmd(args []string) error {
	paths := global.Resolve()
	authPath := filepath.Join(paths.Data, "auth.json")
	fmt.Println("auth file:", authPath)
	if _, err := os.Stat(authPath); err != nil {
		fmt.Println("  status: MISSING")
	} else {
		fmt.Println("  status: exists")
	}
	if content := os.Getenv("GOCODE_AUTH_CONTENT"); content != "" {
		fmt.Println("  GOCODE_AUTH_CONTENT is set — overrides the file")
	}

	all, err := auth.All()
	if err != nil {
		return err
	}
	fmt.Println("\nauthenticated providers:")
	for id, info := range all {
		detail := info.Key
		if info.Type == "oauth" {
			detail = info.Access
		}
		fmt.Printf("  %-28s %-9s %s\n", id, info.Type, redactValue(detail))
	}

	if len(args) == 0 {
		return nil
	}
	providerID := args[0]
	fmt.Printf("\ncredential resolution for %q:\n", providerID)

	catalog, catErr := modelsdev.New().Get(context.Background())
	_ = strings.TrimSpace
	if catErr != nil {
		fmt.Println("  catalog: unavailable (" + catErr.Error() + ")")
	} else if entry, ok := catalog[providerID]; ok {
		for _, name := range entry.Env {
			value := ""
			if os.Getenv(name) != "" {
				value = " (set)"
			} else {
				value = " (unset)"
			}
			fmt.Printf("  env candidate:   %s%s\n", name, value)
		}
	} else {
		fmt.Printf("  env candidate:   %s_API_KEY\n", upper(providerID))
	}

	if cfg, cfgErr := config.Load(); cfgErr == nil {
		if entry, ok := cfg.Provider[providerID]; ok && entry.Options.APIKey != "" {
			fmt.Println("  config:          options.apiKey present")
		} else {
			fmt.Println("  config:          no options.apiKey")
		}
	}

	info, authErr := auth.Get(providerID)
	if authErr != nil {
		fmt.Println("  auth store:      error:", authErr)
	} else if info == nil {
		fmt.Println("  auth store:      no entry")
	} else {
		fmt.Printf("  auth store:      %s entry (secret present)\n", info.Type)
	}
	return nil
}

func upper(value string) string {
	out := []rune(value)
	for i := range out {
		if out[i] >= 'a' && out[i] <= 'z' {
			out[i] -= 32
		}
	}
	return string(out)
}

func redactValue(value string) string {
	runes := []rune(value)
	if len(runes) <= 8 {
		return "\"…\""
	}
	return string(runes[:6]) + "…" + string(runes[len(runes)-2:])
}

var _ = strings.TrimSpace
