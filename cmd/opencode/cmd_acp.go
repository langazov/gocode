package main

import "github.com/anomalyco/opencode-go/internal/clix"

// acpCommand mirrors AcpCommand in cli/cmd/acp.ts ("acp"): starts an Agent
// Client Protocol server over stdio. Needs the agentclientprotocol/sdk
// equivalent, which has no Go port yet.
func acpCommand() *clix.Command {
	flags := append([]clix.Flag{}, networkFlags()...)
	flags = append(flags, clix.Flag{Name: "cwd", Kind: clix.KindString, Describe: "working directory"})
	return &clix.Command{
		Name:     "acp",
		Describe: "start ACP (Agent Client Protocol) server",
		Flags:    flags,
		Run:      func(a *clix.Args) error { return notImplemented("opencode acp") },
	}
}
