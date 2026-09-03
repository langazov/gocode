package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/langazov/gocode-go/internal/clix"
	"github.com/langazov/gocode-go/internal/global"
)

// uninstallCommand mirrors UninstallCommand in cli/cmd/uninstall.ts. Package
// manager / shell-PATH cleanup (npm/brew/choco uninstall, shell rc file
// editing) is not implemented; removing the XDG data/cache/config/state
// directories, which is the part that matters for a Go binary managing its
// own state, is.
func uninstallCommand() *clix.Command {
	return &clix.Command{
		Name:     "uninstall",
		Describe: "uninstall gocode and remove all related files",
		Flags: []clix.Flag{
			{Name: "keep-config", Aliases: []string{"c"}, Kind: clix.KindBool, Default: false, Describe: "keep configuration files"},
			{Name: "keep-data", Aliases: []string{"d"}, Kind: clix.KindBool, Default: false, Describe: "keep session data and snapshots"},
			{Name: "dry-run", Kind: clix.KindBool, Default: false, Describe: "show what would be removed without removing"},
			{Name: "force", Aliases: []string{"f"}, Kind: clix.KindBool, Default: false, Describe: "skip confirmation prompts"},
		},
		Run: runUninstall,
	}
}

func runUninstall(a *clix.Args) error {
	paths := global.Resolve()
	targets := []struct {
		path string
		keep bool
	}{
		{paths.Data, a.Bool("keep-data")},
		{paths.Cache, false},
		{paths.Config, a.Bool("keep-config")},
		{paths.State, false},
	}

	fmt.Println("The following will be removed:")
	for _, t := range targets {
		if _, err := os.Stat(t.path); err != nil {
			continue
		}
		status := ""
		if t.keep {
			status = " (keeping)"
		}
		fmt.Printf("  %s%s\n", shortenHome(t.path), status)
	}

	if !a.Bool("force") && !a.Bool("dry-run") {
		fmt.Print("Are you sure you want to uninstall? [y/N] ")
		reader := bufio.NewReader(os.Stdin)
		line, _ := reader.ReadString('\n')
		if strings.ToLower(strings.TrimSpace(line)) != "y" {
			fmt.Println("Cancelled")
			return nil
		}
	}

	if a.Bool("dry-run") {
		fmt.Println("Dry run - no changes made")
		return nil
	}

	for _, t := range targets {
		if t.keep {
			continue
		}
		if _, err := os.Stat(t.path); err != nil {
			continue
		}
		if err := os.RemoveAll(t.path); err != nil {
			fmt.Fprintf(os.Stderr, "Failed to remove %s: %v\n", t.path, err)
			continue
		}
		fmt.Printf("Removed %s\n", shortenHome(t.path))
	}
	fmt.Println("Thank you for using GoCode!")
	return nil
}

func shortenHome(p string) string {
	if home, err := os.UserHomeDir(); err == nil && strings.HasPrefix(p, home) {
		return "~" + strings.TrimPrefix(p, home)
	}
	return p
}
