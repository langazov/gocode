package main

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/langazov/gocode-go/internal/clix"
	"github.com/langazov/gocode-go/internal/config"
	"github.com/langazov/gocode-go/internal/configedit"
	"github.com/langazov/gocode-go/internal/plugin"
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
				Name:     "list",
				Aliases:  []string{"ls"},
				Describe: "list configured, built-in, and installed-but-disabled plugins",
				Run:      runPluginList,
			},
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

// runPluginList reports what is configured, what is built in, and what is
// installed without being enabled — the three states a plugin can be in
// before it ever runs.
//
// It resolves without loading. Resolution is the step that actually answers
// "will this start": it finds the target and checks the entrypoint, which is
// where a missing directory, a missing manifest, or a non-executable binary
// is caught. Loading on top of that would spawn every configured process
// plugin just to print a list, and this command is run precisely when
// something is already suspected to be wrong. The live view of what did load,
// with each plugin's hooks and tools, is GET /api/plugin (and the TUI's
// plugins dialog).
func runPluginList(a *clix.Args) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	directory, _ := os.Getwd()

	specs := make([]plugin.Spec, 0, len(cfg.Plugin))
	for _, spec := range cfg.Plugin {
		specs = append(specs, plugin.Spec{Ref: spec.Ref, Options: plugin.Options(spec.Options)})
	}

	// A native named in the config is there to carry options, not to load a
	// second copy (see plugin.Load), so it is reported in the configured
	// section and skipped in the built-in one rather than listed twice.
	configuredNative := map[string]bool{}
	for _, spec := range specs {
		name := strings.TrimPrefix(spec.Ref, "native:")
		if _, ok := plugin.Native(name); ok {
			configuredNative[name] = true
		}
	}

	fmt.Println("Configured:")
	if len(specs) == 0 {
		fmt.Println("  (none — the `plugin` array in your config is empty)")
	}
	broken := 0
	for _, spec := range specs {
		resolved, stage, err := plugin.Resolve(spec, directory)
		if err != nil {
			broken++
			fmt.Printf("  %s %s\n      %s: %v\n", statusIcon("error"), spec.Ref, stage, err)
			continue
		}
		detail := string(resolved.Source)
		if resolved.Source == plugin.SourceProcess {
			detail += "  " + resolved.Target
		}
		fmt.Printf("  %s %s\n      %s\n", statusIcon("connected"), spec.Ref, detail)
	}

	var builtin []string
	for _, name := range plugin.NativeNamesSorted() {
		if !configuredNative[name] {
			builtin = append(builtin, name)
		}
	}
	if len(builtin) > 0 {
		fmt.Println("\nBuilt-in (loaded without being configured):")
		for _, name := range builtin {
			fmt.Printf("  %s %s\n", statusIcon("connected"), name)
		}
	}

	if available := plugin.Installed(specs, directory); len(available) > 0 {
		fmt.Println("\nInstalled but not enabled:")
		for _, entry := range available {
			fmt.Printf("  %s %s\n      %s\n", statusIcon("disabled"), entry.Ref, entry.Path)
		}
		fmt.Println("\nEnable one with: gocode plugin enable <name>")
	}

	if broken > 0 {
		// Reported, not returned as an error: the command did its job, and
		// the whole point of running it is to be shown the broken entry.
		fmt.Printf("\n%d configured plugin(s) could not be resolved and will not load.\n", broken)
	}
	return nil
}
