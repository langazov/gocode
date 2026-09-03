package main

import "github.com/langazov/gocode-go/internal/clix"

// pluginCommand mirrors PluginCommand in cli/cmd/plug.ts ("plugin <module>",
// aliased "plug"): installs an npm plugin package and wires it into config.
// The Go port has no plugin host (see specs/go-port-plan.md §6).
func pluginCommand() *clix.Command {
	return &clix.Command{
		Name:        "plugin",
		Aliases:     []string{"plug"},
		Describe:    "install plugin and update config",
		Positionals: []clix.Positional{{Name: "module", Describe: "npm module name"}},
		Flags: []clix.Flag{
			{Name: "global", Aliases: []string{"g"}, Kind: clix.KindBool, Default: false, Describe: "install in global config"},
			{Name: "force", Aliases: []string{"f"}, Kind: clix.KindBool, Default: false, Describe: "replace existing plugin version"},
		},
		Run: func(a *clix.Args) error { return notImplemented("gocode plugin") },
	}
}
