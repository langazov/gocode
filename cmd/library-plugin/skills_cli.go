package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/langazov/gocode-go/internal/configedit"
)

// CLI mode for the skill tools: the same handlers the plugin dispatches,
// for manual testing without a host.

func init() {
	cliCommands["skill-search"] = runCLISkillSearch
	cliCommands["skill-use"] = runCLISkillUse
	cliCommands["skill-show"] = runCLISkillShow
	cliCommands["skill-list"] = runCLISkillList
	cliCommands["skill-load"] = runCLISkillLoad
	cliCommands["skill-store"] = runCLISkillStore
	cliCommands["skill-diff"] = runCLISkillDiff
	cliCommands["skill-delete"] = runCLISkillDelete
	cliCommands["skill-config"] = runCLISkillConfig
}

// cliSkillRuntime parses fs and builds a runtime rooted at the current
// directory, so project scope means "this project" as it does in a
// session started here.
func cliSkillRuntime(fs *flag.FlagSet, args []string) (*runtime, error) {
	baseURL := fs.String("base-url", "", "override gocoder.org's base URL")
	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	wd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	return newRuntime(runtimeOptions{Directory: wd, BaseURL: *baseURL})
}

func printResult(out string, err error) error {
	if err != nil {
		return err
	}
	fmt.Println(out)
	return nil
}

func runCLISkillSearch(args []string) error {
	fs := flag.NewFlagSet("library-plugin skill-search", flag.ContinueOnError)
	query := fs.String("query", "", "what to find (required)")
	mode := fs.String("mode", "discover", "discover | content")
	skillName := fs.String("skill", "", "content mode: only this skill")
	role := fs.String("role", "", "content mode: skill_md | reference | script | asset")
	k := fs.Int("k", 0, "max skills (default 5)")
	rt, err := cliSkillRuntime(fs, args)
	if err != nil {
		return err
	}
	return printResult(handleSkillSearch(context.Background(), rt, *query, *mode, *skillName, *role, *k))
}

func runCLISkillUse(args []string) error {
	fs := flag.NewFlagSet("library-plugin skill-use", flag.ContinueOnError)
	name := fs.String("name", "", "skill name (required)")
	rt, err := cliSkillRuntime(fs, args)
	if err != nil {
		return err
	}
	return printResult(handleSkillUse(context.Background(), rt, *name))
}

func runCLISkillShow(args []string) error {
	fs := flag.NewFlagSet("library-plugin skill-show", flag.ContinueOnError)
	name := fs.String("name", "", "skill name (required)")
	file := fs.String("file", "", "file relative to the skill folder")
	tree := fs.Bool("tree", false, "list the skill's files")
	materialize := fs.Bool("materialize", false, "write the file to the local cache and print its path")
	rt, err := cliSkillRuntime(fs, args)
	if err != nil {
		return err
	}
	return printResult(handleSkillShow(context.Background(), rt, *name, *file, *tree, *materialize))
}

func runCLISkillList(args []string) error {
	fs := flag.NewFlagSet("library-plugin skill-list", flag.ContinueOnError)
	rt, err := cliSkillRuntime(fs, args)
	if err != nil {
		return err
	}
	return printResult(handleSkillList(context.Background(), rt))
}

func runCLISkillLoad(args []string) error {
	fs := flag.NewFlagSet("library-plugin skill-load", flag.ContinueOnError)
	name := fs.String("name", "", "skill name (required)")
	scope := fs.String("scope", "", "project (default) | global")
	overwrite := fs.Bool("overwrite", false, "replace local files that differ")
	files := fs.String("files", "", "comma-separated files to fetch (default: all)")
	rt, err := cliSkillRuntime(fs, args)
	if err != nil {
		return err
	}
	var list []string
	for _, f := range strings.Split(*files, ",") {
		if f = strings.TrimSpace(f); f != "" {
			list = append(list, f)
		}
	}
	return printResult(handleSkillLoad(context.Background(), rt, *name, *scope, *overwrite, list))
}

func runCLISkillStore(args []string) error {
	fs := flag.NewFlagSet("library-plugin skill-store", flag.ContinueOnError)
	name := fs.String("name", "", "local skill name")
	dir := fs.String("dir", "", "skill folder, instead of a name")
	overwrite := fs.Bool("overwrite", false, "replace changed library files and delete library-only ones")
	dryRun := fs.Bool("dry-run", false, "report what would change, change nothing")
	wait := fs.Bool("wait", true, "wait for indexing to finish")
	timeout := fs.Int("timeout", 0, "wait timeout in seconds (default 120)")
	rt, err := cliSkillRuntime(fs, args)
	if err != nil {
		return err
	}
	return printResult(handleSkillStore(context.Background(), rt, *name, *dir, *overwrite, *dryRun, *wait, *timeout))
}

func runCLISkillDiff(args []string) error {
	fs := flag.NewFlagSet("library-plugin skill-diff", flag.ContinueOnError)
	name := fs.String("name", "", "local skill name")
	dir := fs.String("dir", "", "skill folder, instead of a name")
	rt, err := cliSkillRuntime(fs, args)
	if err != nil {
		return err
	}
	return printResult(handleSkillDiff(context.Background(), rt, *name, *dir))
}

func runCLISkillDelete(args []string) error {
	fs := flag.NewFlagSet("library-plugin skill-delete", flag.ContinueOnError)
	name := fs.String("name", "", "skill name (required)")
	yes := fs.Bool("yes", false, "confirm the deletion")
	rt, err := cliSkillRuntime(fs, args)
	if err != nil {
		return err
	}
	return printResult(handleSkillDelete(context.Background(), rt, *name, *yes))
}

// pluginRef is the name library-plugin is enabled under in the config's
// plugin array (make install-library-plugin, the Homebrew formula).
const pluginRef = "library-plugin"

// runCLISkillConfig edits the plugin's skill options in the global config:
//
//	skill-config advertise on|off
//	skill-config limit <n>
//	skill-config chars <n>
//	skill-config ttl <seconds>
//	skill-config scope project|global
func runCLISkillConfig(args []string) error {
	const usage = "usage: library-plugin skill-config advertise on|off | limit <n> | chars <n> | ttl <seconds> | scope project|global"
	if len(args) != 2 {
		return errors.New(usage)
	}
	key, value := args[0], args[1]
	var option string
	var parsed any
	switch key {
	case "advertise":
		option = "skillsAdvertise"
		switch strings.ToLower(value) {
		case "on", "true", "1":
			parsed = true
		case "off", "false", "0":
			parsed = false
		default:
			return fmt.Errorf("advertise takes on or off, got %q", value)
		}
	case "limit", "chars", "ttl":
		option = map[string]string{"limit": "skillsAdvertiseLimit", "chars": "skillsAdvertiseMaxChars", "ttl": "skillsAdvertiseTTL"}[key]
		n, err := strconv.Atoi(value)
		if err != nil || n <= 0 {
			return fmt.Errorf("%s takes a positive integer, got %q", key, value)
		}
		parsed = n
	case "scope":
		if value != scopeProject && value != scopeGlobal {
			return fmt.Errorf("scope takes %s or %s, got %q", scopeProject, scopeGlobal, value)
		}
		option, parsed = "skillsDefaultScope", value
	default:
		return errors.New(usage)
	}

	result, err := configedit.SetPluginOptions(pluginRef, map[string]any{option: parsed})
	if err != nil {
		return err
	}
	fmt.Printf("%s: %s (%s = %v)\n", result.Path, result.Summary, option, parsed)
	if result.Changed {
		fmt.Println("Takes effect the next time gocode starts.")
	}
	if key == "advertise" {
		if env := os.Getenv(advertiseEnv); env != "" {
			fmt.Printf("Note: %s=%s is set and overrides this option.\n", advertiseEnv, env)
		}
	}
	return nil
}
