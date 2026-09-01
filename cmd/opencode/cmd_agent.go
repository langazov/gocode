package main

import (
	"encoding/json"
	"fmt"

	"github.com/anomalyco/opencode-go/internal/agent"
	"github.com/anomalyco/opencode-go/internal/clix"
	"github.com/anomalyco/opencode-go/internal/config"
	"github.com/anomalyco/opencode-go/internal/permission"
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
				Flags: []clix.Flag{
					{Name: "path", Kind: clix.KindString, Describe: "directory path to generate the agent file"},
					{Name: "description", Kind: clix.KindString, Describe: "what the agent should do"},
					{Name: "mode", Kind: clix.KindString, Choices: []string{"all", "primary", "subagent"}, Describe: "agent mode"},
					{Name: "permissions", Aliases: []string{"tools"}, Kind: clix.KindString, Describe: "comma-separated list of permissions to allow (default: all)"},
					{Name: "model", Aliases: []string{"m"}, Kind: clix.KindString, Describe: "model to use in the format of provider/model"},
				},
				Run: func(a *clix.Args) error { return notImplemented("opencode agent create") },
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
