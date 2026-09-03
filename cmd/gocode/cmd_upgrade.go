package main

import "github.com/langazov/gocode-go/internal/clix"

// upgradeCommand mirrors UpgradeCommand in cli/cmd/upgrade.ts
// ("upgrade [target]"). The Go port has no installer/upgrade machinery yet
// (internal/installation only carries the build-time Version/Channel), so
// this validates flags but does not perform an upgrade.
func upgradeCommand() *clix.Command {
	return &clix.Command{
		Name:        "upgrade",
		Describe:    "upgrade gocode to the latest or a specific version",
		Positionals: []clix.Positional{{Name: "target", Describe: "version to upgrade to, for ex '0.1.48' or 'v0.1.48'"}},
		Flags: []clix.Flag{
			{Name: "method", Aliases: []string{"m"}, Kind: clix.KindString,
				Choices:  []string{"curl", "npm", "pnpm", "bun", "brew", "choco", "scoop"},
				Describe: "installation method to use"},
		},
		Run: func(a *clix.Args) error { return notImplemented("gocode upgrade") },
	}
}
