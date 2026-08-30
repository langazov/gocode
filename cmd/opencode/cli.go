package main

import (
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/anomalyco/opencode-go/internal/clix"
	"github.com/anomalyco/opencode-go/internal/global"
	"github.com/anomalyco/opencode-go/internal/installation"
)

// runMain is the process entrypoint's testable core: strip yargs-style
// global options (which apply regardless of position, per
// parserConfiguration({ populate--: true }) and the top-level .option()
// calls in index.ts), handle -h/--help and -v/--version exactly like yargs
// does before ever touching the command tree, then dispatch.
func runMain(argv []string) int {
	if err := global.Init(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}

	argv, printLogs, logLevel, pure := stripGlobalFlags(argv)
	if printLogs {
		os.Setenv("OPENCODE_PRINT_LOGS", "1")
	}
	if logLevel != "" {
		os.Setenv("OPENCODE_LOG_LEVEL", logLevel)
	}
	if pure {
		os.Setenv("OPENCODE_PURE", "1")
	}
	os.Setenv("AGENT", "1")
	os.Setenv("OPENCODE", "1")

	if slices.Contains(argv, "-v") || slices.Contains(argv, "--version") {
		fmt.Println(installation.Version)
		return 0
	}

	root := newRootCommand()

	if clix.HelpRequested(argv) {
		cmd, path := clix.ResolveForHelp(root, argv)
		clix.PrintHelp(os.Stdout, cmd, path)
		return 0
	}

	if err := clix.Run(root, argv); err != nil {
		if ue, ok := err.(*clix.UsageError); ok {
			fmt.Fprintln(os.Stderr, ue.Msg)
			return 1
		}
		if ue, ok := err.(*usageError); ok {
			fmt.Fprintln(os.Stderr, ue.msg)
			return 1
		}
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	return 0
}

// stripGlobalFlags removes --print-logs, --log-level <x>, --pure,
// --completion, and "help"/"-h"/"--help" tokens that belong to the yargs
// root parser (index.ts .option(...)), wherever they appear, mirroring
// yargs' "global options work anywhere" behavior. It returns the remaining
// argv for command-tree dispatch.
func stripGlobalFlags(argv []string) (cleaned []string, printLogs bool, logLevel string, pure bool) {
	for i := 0; i < len(argv); i++ {
		tok := argv[i]
		switch {
		case tok == "--print-logs":
			printLogs = true
		case tok == "--log-level":
			if i+1 < len(argv) {
				logLevel = argv[i+1]
				i++
			}
		case strings.HasPrefix(tok, "--log-level="):
			logLevel = strings.TrimPrefix(tok, "--log-level=")
		case tok == "--pure":
			pure = true
		default:
			cleaned = append(cleaned, tok)
		}
	}
	return cleaned, printLogs, logLevel, pure
}

// newRootCommand builds the full command tree, matching every command
// registered in packages/opencode/src/index.ts plus their flags from each
// cli/cmd/*.ts builder. The root itself is TuiThreadCommand's "$0 [project]"
// default.
func newRootCommand() *clix.Command {
	root := &clix.Command{
		Name:        "opencode",
		Describe:    "start opencode tui",
		Positionals: []clix.Positional{{Name: "project", Describe: "path to start opencode in"}},
		Flags:       rootTuiFlags(),
		Run:         runRootTui,
		Sub: []*clix.Command{
			acpCommand(),
			mcpCommand(),
			tuiCommand(),
			attachCommand(),
			runCommand(),
			generateCommand(),
			debugCommand(),
			consoleCommand(),
			providersCommand(),
			agentCommand(),
			upgradeCommand(),
			uninstallCommand(),
			serveCommand(),
			webCommand(),
			modelsCommand(),
			statsCommand(),
			exportCommand(),
			importCommand(),
			githubCommand(),
			prCommand(),
			sessionCommand(),
			pluginCommand(),
			dbCommand(),
			completionCommand(),
			versionCommand(),
			helpCommand(),
		},
	}
	return root
}

func versionCommand() *clix.Command {
	return &clix.Command{
		Name:     "version",
		Describe: "show version number",
		Run: func(a *clix.Args) error {
			fmt.Println(installation.Version)
			return nil
		},
	}
}

func helpCommand() *clix.Command {
	return &clix.Command{
		Name:     "help",
		Describe: "show help",
		Run: func(a *clix.Args) error {
			root := newRootCommand()
			clix.PrintHelp(os.Stdout, root, []string{"opencode"})
			return nil
		},
	}
}

func completionCommand() *clix.Command {
	return &clix.Command{
		Name:     "completion",
		Describe: "generate shell completion script",
		Run: func(a *clix.Args) error {
			fmt.Fprintln(os.Stderr, "shell completion is not yet implemented in the Go port")
			return nil
		},
	}
}
