package main

import (
	"errors"
	"fmt"

	"github.com/langazov/gocode-go/internal/clix"
	"github.com/langazov/gocode-go/internal/configedit"
)

// pluginCommand mirrors PluginCommand in cli/cmd/plug.ts ("plugin <module>",
// aliased "plug"): installs an npm plugin package and wires it into config.
// The Go port has no npm plugin host (see specs/go-port-plan.md §6), so the
// bare form is still unimplemented.
//
// The `enable` and `disable` subcommands are this port's own: installing a
// plugin does not run it, since a plugin runs only when the config's `plugin`
// array names it. That gap used to be closed by tools/pluginconfig.go, a
// `go run` script — unreachable from a package manager on a machine with no Go
// toolchain, which is what the Homebrew formula needs.
func pluginCommand() *clix.Command {
	refPositional := []clix.Positional{{Name: "ref", Required: true, Describe: "plugin reference: an installed name or a path"}}
	return &clix.Command{
		Name:        "plugin",
		Aliases:     []string{"plug"},
		Describe:    "install plugin and update config",
		Positionals: []clix.Positional{{Name: "module", Describe: "npm module name"}},
		Flags: []clix.Flag{
			{Name: "global", Aliases: []string{"g"}, Kind: clix.KindBool, Default: false, Describe: "install in global config"},
			{Name: "force", Aliases: []string{"f"}, Kind: clix.KindBool, Default: false, Describe: "replace existing plugin version"},
		},
		Sub: []*clix.Command{
			{
				Name:        "enable",
				Describe:    "add a plugin to the global config so it loads",
				Positionals: refPositional,
				Flags: []clix.Flag{
					globalFlag,
					{Name: "options", Kind: clix.KindString, Describe: `JSON object of plugin options, e.g. '{"embeddingProvider":"openai"}'`},
				},
				Run: runPluginEnable,
			},
			{
				Name:        "disable",
				Describe:    "remove a plugin from the global config, leaving it installed",
				Positionals: refPositional,
				Flags:       []clix.Flag{globalFlag},
				Run:         runPluginDisable,
			},
		},
		Run: func(a *clix.Args) error { return notImplemented("gocode plugin") },
	}
}

// globalFlag is shared by every config-editing subcommand. Only the global
// config is supported: a project config lives next to the code and is usually
// version-controlled, so an installer editing it would commit machine-specific
// paths into someone's repository.
var globalFlag = clix.Flag{
	Name:     "global",
	Aliases:  []string{"g"},
	Kind:     clix.KindBool,
	Default:  true,
	Describe: "edit the global config (the only supported target)",
}

// requireGlobal rejects --global=false rather than silently editing the global
// config anyway, so the flag never lies about what happened.
func requireGlobal(a *clix.Args) error {
	if !a.Bool("global") {
		return &usageError{msg: "only the global config can be edited; drop --global=false, or edit the project config by hand"}
	}
	return nil
}

func runPluginEnable(a *clix.Args) error {
	if err := requireGlobal(a); err != nil {
		return err
	}
	options, err := configedit.ParseOptions(a.String("options"))
	if err != nil {
		return err
	}
	result, err := configedit.EnablePlugin(a.PositionalOr("ref", ""), options)
	return reportEdit(result, err)
}

func runPluginDisable(a *clix.Args) error {
	if err := requireGlobal(a); err != nil {
		return err
	}
	result, err := configedit.DisablePlugin(a.PositionalOr("ref", ""))
	return reportEdit(result, err)
}

// reportEdit prints one line saying what happened. A refused edit on a
// commented config is not an error the caller should die on — the files are
// installed and only the wiring is missing — so it prints the manual snippet
// and succeeds, which keeps `brew install` from failing over a config style
// choice.
func reportEdit(result configedit.Result, err error) error {
	var commented *configedit.CommentedError
	if errors.As(err, &commented) {
		fmt.Println(commented.Error())
		return nil
	}
	if err != nil {
		return err
	}
	fmt.Printf("%s: %s\n", result.Path, result.Summary)
	return nil
}
