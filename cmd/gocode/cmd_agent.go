package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/langazov/gocode-go/internal/agent"
	"github.com/langazov/gocode-go/internal/clix"
	"github.com/langazov/gocode-go/internal/config"
	"github.com/langazov/gocode-go/internal/global"
	"github.com/langazov/gocode-go/internal/permission"
)

// availablePermissions mirrors AVAILABLE_PERMISSIONS in cli/cmd/agent.ts.
var availablePermissions = []string{
	"bash", "read", "edit", "glob", "grep", "webfetch", "task", "todowrite", "websearch", "lsp", "skill",
}

// agentCommand mirrors AgentCommand in cli/cmd/agent.ts ("agent create|list").
func agentCommand() *clix.Command {
	return &clix.Command{
		Name:     "agent",
		Describe: "manage agents",
		Demand:   true,
		Sub: []*clix.Command{
			{
				Name:     "create",
				Describe: "create a new agent",
				Positionals: []clix.Positional{
					{Name: "name", Required: true, Describe: "name of the agent to create"},
				},
				Flags: []clix.Flag{
					{Name: "path", Kind: clix.KindString, Describe: "directory path to generate the agent file"},
					{Name: "prompt", Kind: clix.KindString, Describe: "system prompt for the agent"},
					{Name: "description", Kind: clix.KindString, Describe: "what the agent should do"},
					{Name: "mode", Kind: clix.KindString, Choices: []string{"all", "primary", "subagent"}, Describe: "agent mode"},
					{Name: "permissions", Aliases: []string{"tools"}, Kind: clix.KindString, Describe: "comma-separated list of permissions to allow (default: all)"},
					{Name: "model", Aliases: []string{"m"}, Kind: clix.KindString, Describe: "model to use in the format of provider/model"},
				},
				Run: runAgentCreate,
			},
			{
				Name:     "list",
				Describe: "list all available agents",
				Run:      runAgentList,
			},
		},
	}
}

func runAgentList(a *clix.Args) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	// Markdown agents are merged the same way bootStack does it, so `agent
	// list` reports exactly what a session would load.
	cwd, _ := os.Getwd()
	cfg.DiscoverAgents(
		filepath.Join(cwd, ".gocode"),
		filepath.Join(global.Resolve().Config, "gocode"),
	)

	registry := agent.NewRegistry()
	registry.Update(agent.Info{ID: "build", Mode: "primary", Permissions: permission.Ruleset{}})
	registerBuiltinSubagents(registry, permission.Defaults())
	if cfg.DefaultAgent != "" {
		registry.SetDefault(cfg.DefaultAgent)
	}
	for id, agentConfig := range cfg.Agent {
		info := agent.Info{
			ID:          id,
			Description: agentConfig.Description,
			Mode:        agentConfig.Mode,
			Hidden:      agentConfig.Hidden,
		}
		if info.Mode == "" {
			info.Mode = "all"
		}
		if rules, err := agentConfig.AgentRuleset(); err == nil {
			info.Permissions = rules
		}
		registry.Update(info)
	}
	for _, info := range registry.All() {
		mode := info.Mode
		if mode == "" {
			mode = "all"
		}
		fmt.Printf("%s (%s)\n", info.ID, mode)
		data, _ := json.MarshalIndent(info.Permissions, "  ", "  ")
		fmt.Printf("  %s\n", data)
	}
	return nil
}

// runAgentCreate writes a markdown agent definition into .gocode/agent/.
// Markdown is the format the loader prefers for hand-authored agents: the
// system prompt is the file body rather than an escaped JSON string.
func runAgentCreate(a *clix.Args) error {
	name := strings.TrimSpace(a.PositionalOr("name", ""))
	if name == "" {
		return fmt.Errorf("agent create: an agent name is required (gocode agent create <name>)")
	}
	// The name becomes a filename, so reject anything that could escape the
	// agent directory.
	if strings.ContainsAny(name, `/\`) || name == "." || name == ".." {
		return fmt.Errorf("agent create: invalid agent name %q", name)
	}
	mode := a.String("mode")
	if mode == "" {
		mode = "all"
	}
	root := a.String("path")
	if root == "" {
		root = ".gocode"
	}
	agentConfig := config.Agent{
		Description: a.String("description"),
		Mode:        mode,
		Model:       a.String("model"),
		Prompt:      a.String("prompt"),
	}
	if permissions := a.String("permissions"); permissions != "" {
		agentConfig.Permission = allowOnlyPermissions(permissions)
	}
	if agentConfig.Prompt == "" {
		agentConfig.Prompt = "You are the " + name + " agent. Describe its behavior here."
	}
	location, err := config.WriteAgentMarkdown(root, name, agentConfig)
	if err != nil {
		if errors.Is(err, fs.ErrExist) {
			return fmt.Errorf("agent create: %s already exists", filepath.Join(root, "agent", name+".md"))
		}
		return err
	}
	fmt.Printf("created agent %q at %s\n", name, location)
	return nil
}

// allowOnlyPermissions turns `--permissions read,grep` into a ruleset that
// denies everything and then allows the named actions, so the flag reads as
// an allowlist rather than an addition to the default allow-all.
func allowOnlyPermissions(list string) config.Permission {
	rules := map[string]any{"*": "deny"}
	for _, action := range strings.Split(list, ",") {
		if action = strings.TrimSpace(action); action != "" {
			rules[action] = "allow"
		}
	}
	encoded, err := json.Marshal(rules)
	if err != nil {
		return config.Permission{}
	}
	var out config.Permission
	if err := out.UnmarshalJSON(encoded); err != nil {
		return config.Permission{}
	}
	return out
}
