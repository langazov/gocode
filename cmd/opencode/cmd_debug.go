package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"time"

	"github.com/anomalyco/opencode-go/internal/clix"
	"github.com/anomalyco/opencode-go/internal/config"
	"github.com/anomalyco/opencode-go/internal/db"
	"github.com/anomalyco/opencode-go/internal/global"
	"github.com/anomalyco/opencode-go/internal/installation"
)

// processStart is used by "debug startup" to print elapsed process time,
// matching performance.now() in cli/cmd/debug/startup.ts.
var processStart = time.Now()

// debugCommand mirrors DebugCommand in cli/cmd/debug/index.ts, plus the Go
// port's own "auth" subcommand (not present in the TS CLI; auth.json has no
// equivalent debug surface there because credentials mostly live server-side).
func debugCommand() *clix.Command {
	return &clix.Command{
		Name:     "debug",
		Describe: "debugging and troubleshooting tools",
		Demand:   true,
		Sub: []*clix.Command{
			{Name: "config", Describe: "show resolved configuration", Run: func(a *clix.Args) error { return debugConfigCmd(nil) }},
			{Name: "auth", Describe: "show provider credential resolution (Go port extra)",
				Positionals: []clix.Positional{{Name: "provider"}},
				Run:         func(a *clix.Args) error { return debugAuthCmd(argsOf(a.Pos["provider"])) }},
			debugLSPCommand(),
			debugRipgrepCommand(),
			debugFileCommand(),
			debugScrapCommand(),
			debugSkillCommand(),
			debugSnapshotCommand(),
			debugStartupCommand(),
			debugAgentCommand(),
			debugV2Command(),
			debugInfoCommand(),
			debugPathsCommand(),
			debugWaitCommand(),
		},
	}
}

func argsOf(s string) []string {
	if s == "" {
		return nil
	}
	return []string{s}
}

func debugWaitCommand() *clix.Command {
	return &clix.Command{Name: "wait", Describe: "wait indefinitely (for debugging)", Run: func(a *clix.Args) error {
		time.Sleep(24 * time.Hour)
		return nil
	}}
}

func debugStartupCommand() *clix.Command {
	return &clix.Command{Name: "startup", Describe: "print startup timing", Run: func(a *clix.Args) error {
		fmt.Println(float64(time.Since(processStart).Nanoseconds()) / 1e6)
		return nil
	}}
}

func debugPathsCommand() *clix.Command {
	return &clix.Command{Name: "paths", Describe: "show global paths (data, config, cache, state)", Run: func(a *clix.Args) error {
		p := global.Resolve()
		fmt.Printf("%-10s %s\n", "Home", p.Home)
		fmt.Printf("%-10s %s\n", "Data", p.Data)
		fmt.Printf("%-10s %s\n", "Bin", p.Bin)
		fmt.Printf("%-10s %s\n", "Log", p.Log)
		fmt.Printf("%-10s %s\n", "Repos", p.Repos)
		fmt.Printf("%-10s %s\n", "Cache", p.Cache)
		fmt.Printf("%-10s %s\n", "Config", p.Config)
		fmt.Printf("%-10s %s\n", "State", p.State)
		fmt.Printf("%-10s %s\n", "Tmp", p.Tmp)
		return nil
	}}
}

func debugInfoCommand() *clix.Command {
	return &clix.Command{Name: "info", Describe: "show debug information", Run: func(a *clix.Args) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		termProgram := os.Getenv("TERM_PROGRAM")
		if v := os.Getenv("TERM_PROGRAM_VERSION"); termProgram != "" && v != "" {
			termProgram += " " + v
		}
		terminal := termProgram
		if term := os.Getenv("TERM"); term != "" {
			if terminal != "" {
				terminal += " / "
			}
			terminal += term
		}
		if terminal == "" {
			terminal = "unknown"
		}
		fmt.Printf("opencode version: %s\n", installation.Version)
		fmt.Printf("os: %s %s\n", runtime.GOOS, runtime.GOARCH)
		fmt.Printf("terminal: %s\n", terminal)
		fmt.Println("plugins:")
		if os.Getenv("OPENCODE_PURE") == "1" {
			fmt.Println("external plugins disabled (--pure)")
			return nil
		}
		if len(cfg.Plugin) == 0 {
			fmt.Println("none")
			return nil
		}
		for _, p := range cfg.Plugin {
			fmt.Println("-", p)
		}
		return nil
	}}
}

func debugV2Command() *clix.Command {
	return &clix.Command{Name: "v2", Describe: "debug v2 catalog and built-in plugins", Run: func(a *clix.Args) error {
		return notImplemented("opencode debug v2")
	}}
}

func debugAgentCommand() *clix.Command {
	return &clix.Command{
		Name:        "agent",
		Describe:    "show agent configuration details",
		Positionals: []clix.Positional{{Name: "name", Required: true, Describe: "Agent name"}},
		Flags: []clix.Flag{
			{Name: "tool", Kind: clix.KindString, Describe: "Tool id to execute"},
			{Name: "params", Kind: clix.KindString, Describe: "Tool params as JSON or a JS object literal"},
		},
		Run: func(a *clix.Args) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			agentConfig, ok := cfg.Agent[a.Pos["name"]]
			if !ok {
				return fmt.Errorf("agent not found: %s", a.Pos["name"])
			}
			data, err := json.MarshalIndent(agentConfig, "", "  ")
			if err != nil {
				return err
			}
			fmt.Println(string(data))
			if a.String("tool") != "" {
				return notImplemented("opencode debug agent --tool")
			}
			return nil
		},
	}
}

func debugSnapshotCommand() *clix.Command {
	hashPositional := []clix.Positional{{Name: "hash", Required: true, Describe: "hash"}}
	return &clix.Command{
		Name:     "snapshot",
		Describe: "snapshot debugging utilities",
		Demand:   true,
		Sub: []*clix.Command{
			{Name: "track", Describe: "track current snapshot state", Run: func(a *clix.Args) error { return notImplemented("opencode debug snapshot track") }},
			{Name: "patch", Describe: "show patch for a snapshot hash", Positionals: hashPositional, Run: func(a *clix.Args) error { return notImplemented("opencode debug snapshot patch") }},
			{Name: "diff", Describe: "show diff for a snapshot hash", Positionals: hashPositional, Run: func(a *clix.Args) error { return notImplemented("opencode debug snapshot diff") }},
		},
	}
}

func debugSkillCommand() *clix.Command {
	return &clix.Command{Name: "skill", Describe: "list all available skills", Run: func(a *clix.Args) error {
		return notImplemented("opencode debug skill")
	}}
}

func debugScrapCommand() *clix.Command {
	return &clix.Command{Name: "scrap", Describe: "list all known projects", Run: func(a *clix.Args) error {
		ctx := context.Background()
		database, err := db.OpenDefault(ctx)
		if err != nil {
			return err
		}
		defer database.Close()
		rows, err := database.Query(ctx, `SELECT id, worktree, vcs, name FROM project ORDER BY time_updated DESC`)
		if err != nil {
			return err
		}
		defer rows.Close()
		type projectRow struct {
			ID, Worktree, VCS, Name string
		}
		var out []projectRow
		for rows.Next() {
			var p projectRow
			var vcs, name *string
			if err := rows.Scan(&p.ID, &p.Worktree, &vcs, &name); err != nil {
				return err
			}
			if vcs != nil {
				p.VCS = *vcs
			}
			if name != nil {
				p.Name = *name
			}
			out = append(out, p)
		}
		data, err := json.MarshalIndent(out, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(data))
		return nil
	}}
}

func debugFileCommand() *clix.Command {
	pathPositional := []clix.Positional{{Name: "path", Required: true, Describe: "File path"}}
	queryPositional := []clix.Positional{{Name: "query", Required: true, Describe: "Search query"}}
	return &clix.Command{
		Name:     "file",
		Describe: "file system debugging utilities",
		Demand:   true,
		Sub: []*clix.Command{
			{Name: "read", Describe: "read file contents as JSON", Positionals: pathPositional, Run: func(a *clix.Args) error { return notImplemented("opencode debug file read") }},
			{Name: "list", Describe: "list files in a directory", Positionals: pathPositional, Run: func(a *clix.Args) error { return notImplemented("opencode debug file list") }},
			{Name: "search", Describe: "search files by query", Positionals: queryPositional, Run: func(a *clix.Args) error { return notImplemented("opencode debug file search") }},
		},
	}
}

func debugRipgrepCommand() *clix.Command {
	return &clix.Command{
		Name:     "rg",
		Describe: "ripgrep debugging utilities",
		Demand:   true,
		Sub: []*clix.Command{
			{Name: "files", Describe: "list files using ripgrep", Flags: []clix.Flag{
				{Name: "query", Kind: clix.KindString, Describe: "Filter files by query"},
				{Name: "glob", Kind: clix.KindString, Describe: "Glob pattern to match files"},
				{Name: "limit", Kind: clix.KindNumber, Describe: "Limit number of results"},
			}, Run: func(a *clix.Args) error { return notImplemented("opencode debug rg files") }},
			{Name: "search", Describe: "search file contents using ripgrep",
				Positionals: []clix.Positional{{Name: "pattern", Required: true, Describe: "Search pattern"}},
				Flags: []clix.Flag{
					{Name: "glob", Kind: clix.KindStringArray, Describe: "File glob patterns"},
					{Name: "limit", Kind: clix.KindNumber, Describe: "Limit number of results"},
				}, Run: func(a *clix.Args) error { return notImplemented("opencode debug rg search") }},
		},
	}
}

func debugLSPCommand() *clix.Command {
	return &clix.Command{
		Name:     "lsp",
		Describe: "LSP debugging utilities",
		Demand:   true,
		Sub: []*clix.Command{
			{Name: "diagnostics", Describe: "get diagnostics for a file",
				Positionals: []clix.Positional{{Name: "file", Required: true}},
				Run:         func(a *clix.Args) error { return notImplemented("opencode debug lsp diagnostics") }},
			{Name: "symbols", Describe: "search workspace symbols",
				Positionals: []clix.Positional{{Name: "query", Required: true}},
				Run:         func(a *clix.Args) error { return notImplemented("opencode debug lsp symbols") }},
			{Name: "document-symbols", Describe: "get symbols from a document",
				Positionals: []clix.Positional{{Name: "uri", Required: true}},
				Run:         func(a *clix.Args) error { return notImplemented("opencode debug lsp document-symbols") }},
		},
	}
}
