package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/langazov/gocode-go/internal/clix"
	"github.com/langazov/gocode-go/internal/modelsdev"
)

// modelsCommand mirrors ModelsCommand in cli/cmd/models.ts ("models [provider]").
func modelsCommand() *clix.Command {
	return &clix.Command{
		Name:        "models",
		Describe:    "list all available models",
		Positionals: []clix.Positional{{Name: "provider", Describe: "provider ID to filter models by"}},
		Flags: []clix.Flag{
			{Name: "verbose", Kind: clix.KindBool, Describe: "use more verbose model output (includes metadata like costs)"},
			{Name: "refresh", Kind: clix.KindBool, Describe: "refresh the models cache from models.dev"},
		},
		Run: runModelsCommand,
	}
}

func runModelsCommand(a *clix.Args) error {
	ctx := context.Background()
	service := modelsdev.New()

	if a.Bool("refresh") {
		if err := service.Refresh(ctx, true); err != nil {
			return err
		}
		fmt.Fprintln(os.Stderr, "Models cache refreshed")
	}

	catalog, err := service.Get(ctx)
	if err != nil {
		return err
	}

	print := func(providerID string, verbose bool) {
		provider := catalog[providerID]
		ids := make([]string, 0, len(provider.Models))
		for id := range provider.Models {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, modelID := range ids {
			fmt.Printf("%s/%s\n", providerID, modelID)
			if verbose {
				data, err := json.MarshalIndent(provider.Models[modelID], "", "  ")
				if err == nil {
					fmt.Println(string(data))
				}
			}
		}
	}

	if providerID := a.PositionalOr("provider", ""); providerID != "" {
		if _, ok := catalog[providerID]; !ok {
			return fmt.Errorf("Provider not found: %s", providerID)
		}
		print(providerID, a.Bool("verbose"))
		return nil
	}

	ids := make([]string, 0, len(catalog))
	for id := range catalog {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		x, y := ids[i], ids[j]
		xOpencode := strings.HasPrefix(x, "opencode")
		yOpencode := strings.HasPrefix(y, "opencode")
		if xOpencode && !yOpencode {
			return true
		}
		if !xOpencode && yOpencode {
			return false
		}
		return x < y
	})
	for _, id := range ids {
		print(id, a.Bool("verbose"))
	}
	return nil
}
