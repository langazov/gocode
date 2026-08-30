package main

import "github.com/anomalyco/opencode-go/internal/clix"

// githubCommand mirrors GithubCommand in cli/cmd/github.ts. The GitHub agent
// (workflow install + mock-event runner) is not ported.
func githubCommand() *clix.Command {
	return &clix.Command{
		Name:     "github",
		Describe: "manage GitHub agent",
		Demand:   true,
		Sub: []*clix.Command{
			{Name: "install", Describe: "install the GitHub agent", Run: func(a *clix.Args) error { return notImplemented("opencode github install") }},
			{Name: "run", Describe: "run the GitHub agent", Flags: []clix.Flag{
				{Name: "event", Kind: clix.KindString, Describe: "GitHub mock event to run the agent for"},
				{Name: "token", Kind: clix.KindString, Describe: "GitHub personal access token (github_pat_********)"},
			}, Run: func(a *clix.Args) error { return notImplemented("opencode github run") }},
		},
	}
}
