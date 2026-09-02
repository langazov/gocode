package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"

	"github.com/anomalyco/opencode-go/internal/clix"
	"github.com/anomalyco/opencode-go/internal/server"
	"github.com/anomalyco/opencode-go/internal/tui"
	"github.com/anomalyco/opencode-go/internal/tui/client"
)

// rootTuiFlags mirrors TuiThreadCommand's builder in cli/cmd/tui.ts ("$0
// [project]"). --mini and the network options (--port, --hostname, ...) are
// accepted for CLI-surface parity; the Go TUI is a single Bubble Tea program
// (see specs/go-port-plan.md §7) so --mini currently launches the same full
// interface rather than the TS split-footer minimal mode.
func rootTuiFlags() []clix.Flag {
	flags := append([]clix.Flag{}, networkFlags()...)
	flags = append(flags,
		clix.Flag{Name: "model", Aliases: []string{"m"}, Kind: clix.KindString, Describe: "model to use in the format of provider/model"},
		clix.Flag{Name: "prompt", Kind: clix.KindString, Describe: "prompt to use"},
		clix.Flag{Name: "agent", Kind: clix.KindString, Describe: "agent to use"},
		clix.Flag{Name: "auto", Kind: clix.KindBool, Default: false, Describe: "auto-approve permissions that are not explicitly denied (dangerous!)"},
		clix.Flag{Name: "yolo", Kind: clix.KindBool, Default: false, Hidden: true},
		clix.Flag{Name: "dangerously-skip-permissions", Kind: clix.KindBool, Default: false, Hidden: true},
		clix.Flag{Name: "mini", Kind: clix.KindBool, Default: false, Describe: "start the minimal interactive interface"},
		clix.Flag{Name: "demo", Kind: clix.KindBool, Hidden: true},
	)
	flags = append(flags, sessionSelectFlags()...)
	flags = append(flags, replayFlags(nil, "", "disable mini session history replay on resume and after resize")...)
	return flags
}

func tuiCommand() *clix.Command {
	return &clix.Command{
		Name:        "tui",
		Describe:    "start the interactive terminal interface",
		Positionals: []clix.Positional{{Name: "project", Describe: "path to start opencode in"}},
		Flags:       rootTuiFlags(),
		Run:         runRootTui,
	}
}

func runRootTui(a *clix.Args) error {
	if a.Has("replay") && a.Bool("replay") {
		return &usageError{msg: "--replay is not supported; replay is enabled by default"}
	}
	if a.Bool("fork") && !a.Bool("continue") && a.String("session") == "" {
		return &usageError{msg: "--fork requires --continue or --session"}
	}
	auto := a.Bool("auto") || a.Bool("yolo") || a.Bool("dangerously-skip-permissions")
	addr := "127.0.0.1:0"
	if a.Has("port") {
		addr = fmt.Sprintf("127.0.0.1:%d", a.IntOr("port", 0))
	}
	model := a.String("model")
	themeName := "opencode-dark"

	project := a.PositionalOr("project", "")
	directory := resolveThreadDirectory(project)
	if err := os.Chdir(directory); err != nil {
		return fmt.Errorf("failed to change directory to %s: %w", directory, err)
	}

	stack, err := bootStack(context.Background(), model)
	if err != nil {
		return err
	}
	if auto {
		stack.Runner.Permissions = nil
	}
	if agentID := a.String("agent"); agentID != "" {
		stack.Runner.Agent = agentID
	}

	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	go func() {
		srv := &server.Server{Session: stack.Service, Bus: stack.Bus, Permissions: stack.Permissions, Models: stack.Models, Agents: stack.Agents, Config: stack.Config, MCP: stack.MCP, Jobs: stack.Jobs, Questions: stack.Questions, Skills: stack.Skills, LSP: stack.LSP, Commands: stack.Commands}
		server.ServeOn(listener, srv.Mux())
	}()

	sessionID, _, err := resolveExistingSession(context.Background(), stack, a)
	if err != nil {
		return err
	}

	opts := tui.RunOptions{
		DefaultModel: stack.ProviderID + "/" + stack.ModelID,
		SessionID:    sessionID,
	}
	if err := tui.Run(context.Background(), client.New("http://"+listener.Addr().String()), themeName, opts); err != nil {
		return fmt.Errorf("tui: %w", err)
	}
	fmt.Fprintln(os.Stderr, "bye")
	return nil
}

// resolveThreadDirectory mirrors resolveThreadDirectory() in cli/cmd/tui.ts:
// a relative --project/positional path is resolved against $PWD (falling
// back to cwd), an absolute one is used as-is.
func resolveThreadDirectory(project string) string {
	root := os.Getenv("PWD")
	if root == "" {
		root, _ = os.Getwd()
	}
	if project == "" {
		cwd, _ := os.Getwd()
		return cwd
	}
	if filepath.IsAbs(project) {
		return project
	}
	return filepath.Join(root, project)
}
