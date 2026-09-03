package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"

	"github.com/langazov/gocode-go/internal/clix"
	"github.com/langazov/gocode-go/internal/server"
	"github.com/langazov/gocode-go/internal/session"
	"github.com/langazov/gocode-go/internal/tui"
	"github.com/langazov/gocode-go/internal/tui/client"
)

// runCommand mirrors RunCommand in cli/cmd/run.ts ("run [message..]").
// Interactive/attach/mini modes exist in the flag surface for parity;
// non-interactive local execution (the default and by far the most common
// path) is fully wired against the in-process session Service.
func runCommand() *clix.Command {
	flags := []clix.Flag{
		{Name: "command", Kind: clix.KindString, Describe: "the command to run, use message for args"},
		{Name: "share", Kind: clix.KindBool, Describe: "share the session"},
		{Name: "model", Aliases: []string{"m"}, Kind: clix.KindString, Describe: "model to use in the format of provider/model"},
		{Name: "agent", Kind: clix.KindString, Describe: "agent to use"},
		{Name: "format", Kind: clix.KindString, Default: "default", Choices: []string{"default", "json"}, Describe: "format: default (formatted) or json (raw JSON events)"},
		{Name: "file", Aliases: []string{"f"}, Kind: clix.KindStringArray, Describe: "file(s) to attach to message"},
		{Name: "title", Kind: clix.KindString, Describe: "title for the session (uses truncated prompt if no value provided)"},
		{Name: "attach", Kind: clix.KindString, Describe: "attach to a running gocode server (e.g., http://localhost:4096)"},
		{Name: "dir", Kind: clix.KindString, Describe: "directory to run in, path on remote server if attaching"},
		{Name: "port", Kind: clix.KindNumber, Describe: "port for the local server (defaults to random port if no value provided)"},
		{Name: "variant", Kind: clix.KindString, Describe: "model variant (provider-specific reasoning effort, e.g., high, max, minimal)"},
		{Name: "thinking", Kind: clix.KindBool, Describe: "show thinking blocks"},
		{Name: "mini", Kind: clix.KindBool, Default: false, Hidden: true},
		{Name: "interactive", Aliases: []string{"i"}, Kind: clix.KindBool, Default: false, Describe: "run in direct interactive split-footer mode"},
		{Name: "auto", Kind: clix.KindBool, Default: false, Describe: "auto-approve permissions that are not explicitly denied (dangerous!)"},
		{Name: "yolo", Kind: clix.KindBool, Default: false, Hidden: true},
		{Name: "dangerously-skip-permissions", Kind: clix.KindBool, Default: false, Hidden: true},
		{Name: "demo", Kind: clix.KindBool, Default: false, Hidden: true, Describe: "enable direct interactive demo slash commands; pass one as the message to run it immediately"},
	}
	flags = append(flags, sessionSelectFlags()...)
	flags = append(flags, authHeaderFlags()...)
	flags = append(flags, replayFlags(true, "replay interactive session history on resume and after resize (use --no-replay to disable)", "")...)
	return &clix.Command{
		Name:        "run",
		Describe:    "run gocode with a message",
		Positionals: []clix.Positional{{Name: "message", Array: true, Describe: "message to send"}},
		AllowExtra:  true,
		Flags:       flags,
		Run:         runRunCommand,
	}
}

func runRunCommand(a *clix.Args) error {
	auto := a.Bool("auto") || a.Bool("yolo") || a.Bool("dangerously-skip-permissions")
	interactive := a.Bool("mini") || a.Bool("interactive")

	if interactive && a.String("command") != "" {
		return &usageError{msg: "--mini/--interactive cannot be used with --command"}
	}
	if a.Bool("demo") && !interactive {
		return &usageError{msg: "--demo requires --mini"}
	}
	if interactive && a.String("format") == "json" {
		return &usageError{msg: "--mini cannot be used with --format json"}
	}
	if a.Has("replay-limit") && !interactive {
		return &usageError{msg: "--replay-limit requires --mini"}
	}
	if a.Bool("fork") && !a.Bool("continue") && a.String("session") == "" {
		return &usageError{msg: "--fork requires --continue or --session"}
	}

	message := strings.Join(append(append([]string{}, a.Array("message")...), a.Extra...), " ")

	if dir := a.String("dir"); dir != "" && a.String("attach") == "" {
		abs := dir
		if !filepath.IsAbs(dir) {
			cwd, _ := os.Getwd()
			abs = filepath.Join(cwd, dir)
		}
		if err := os.Chdir(abs); err != nil {
			return fmt.Errorf("failed to change directory to %s: %w", dir, err)
		}
	}

	piped := readPipedStdin()
	if piped != "" {
		if message != "" {
			message = message + "\n" + piped
		} else {
			message = piped
		}
	}

	if strings.TrimSpace(message) == "" && a.String("command") == "" && !interactive {
		return &usageError{msg: "You must provide a message or a command"}
	}

	if a.String("attach") != "" {
		return notImplemented("gocode run --attach")
	}
	if a.String("command") != "" {
		return notImplemented("gocode run --command")
	}

	ctx := context.Background()
	stack, err := bootStack(ctx, a.String("model"))
	if err != nil {
		return err
	}
	// "run" is one-shot: release the database as soon as this command is
	// done rather than leaving it for the process exit that follows a
	// moment later. In-process callers (the tests) never get that process
	// exit, so on Windows the held file handle would otherwise block their
	// t.TempDir cleanup.
	defer stack.Close()
	if auto {
		stack.Runner.Permissions = nil
	}
	if agentID := a.String("agent"); agentID != "" {
		stack.Runner.Agent = agentID
	}

	sessionID, err := resolveRunSession(ctx, stack, a)
	if err != nil {
		return err
	}

	variant := a.String("variant")
	if modelFlag := a.String("model"); modelFlag != "" {
		providerID, modelID, ok := strings.Cut(modelFlag, "/")
		if ok {
			stack.Service.SetModel(ctx, sessionID, session.ModelRef{ProviderID: providerID, ID: modelID, Variant: variant})
		}
	} else if variant != "" {
		// --variant with no --model applies to whatever model the session
		// already has pinned (a resumed session's own model), falling back
		// to the boot default for a freshly created session.
		providerID, modelID := stack.ProviderID, stack.ModelID
		if info, err := stack.Service.Get(ctx, sessionID); err == nil && info != nil && info.Model != nil {
			providerID, modelID = info.Model.ProviderID, info.Model.ID
		}
		stack.Service.SetModel(ctx, sessionID, session.ModelRef{ProviderID: providerID, ID: modelID, Variant: variant})
	}
	if agentID := a.String("agent"); agentID != "" {
		stack.Service.SetAgent(ctx, sessionID, agentID)
	}

	if interactive {
		// The Go TUI is one interactive program covering both "mini" and the
		// full interface (see specs/go-port-gaps.md TUI section); route
		// --mini/--interactive there instead of duplicating it.
		return runInteractiveViaTUI(ctx, stack, sessionID, a)
	}

	format := a.String("format")
	var inputTok, outputTok int
	var finish string
	streamErr := streamRunTurn(ctx, stack, sessionID, message, format, a.Bool("thinking"), &inputTok, &outputTok, &finish)
	if streamErr != nil {
		return streamErr
	}
	if format != "json" {
		fmt.Printf("\ntokens: %d in / %d out, stop: %s\n", inputTok, outputTok, finish)
	}
	return nil
}

func readPipedStdin() string {
	stat, err := os.Stdin.Stat()
	if err != nil || (stat.Mode()&os.ModeCharDevice) != 0 {
		return ""
	}
	data, _ := os.ReadFile("/dev/stdin")
	return strings.TrimRight(string(data), "\n")
}

// resolveRunSession implements the --session/--continue/--fork/fresh-session
// precedence from RunCommand's session() helper.
func resolveRunSession(ctx context.Context, stack *stack, a *clix.Args) (string, error) {
	if id, ok, err := resolveExistingSession(ctx, stack, a); ok || err != nil {
		return id, err
	}

	cwd, _ := os.Getwd()
	title := runSessionTitle(a)
	created, err := stack.Service.Create(ctx, session.CreateInput{Directory: cwd, Title: title})
	if err != nil {
		return "", err
	}
	return created.ID, nil
}

// resolveExistingSession implements the --session/--continue/--fork
// precedence shared by run, the root/tui $0 command, and attach — but,
// unlike resolveRunSession, never creates a fresh session: ok is false when
// neither --session nor --continue was given, so callers that shouldn't
// auto-create (tui, attach) can fall back to their own default (an empty
// home screen).
func resolveExistingSession(ctx context.Context, stack *stack, a *clix.Args) (id string, ok bool, err error) {
	if sessionID := a.String("session"); sessionID != "" {
		info, err := stack.Service.Get(ctx, sessionID)
		if err != nil || info == nil {
			return "", true, &usageError{msg: "Session not found"}
		}
		if a.Bool("fork") {
			forked, err := stack.Service.Fork(ctx, sessionID, "")
			if err != nil {
				return "", true, err
			}
			return forked.ID, true, nil
		}
		return sessionID, true, nil
	}

	if a.Bool("continue") {
		if id, found := latestRootSession(ctx, stack); found {
			if a.Bool("fork") {
				forked, err := stack.Service.Fork(ctx, id, "")
				if err != nil {
					return "", true, err
				}
				return forked.ID, true, nil
			}
			return id, true, nil
		}
		return "", true, &usageError{msg: "No session to continue"}
	}

	return "", false, nil
}

func runSessionTitle(a *clix.Args) string {
	if !a.Has("title") {
		return ""
	}
	if t := a.String("title"); t != "" {
		return t
	}
	msg := strings.Join(a.Array("message"), " ")
	if len(msg) > 50 {
		return msg[:50] + "..."
	}
	return msg
}

// latestRootSession finds the most recently updated session with no parent,
// matching RunCommand's `sessions.find(item => !item.parentID)` (list is
// newest-first).
func latestRootSession(ctx context.Context, stack *stack) (string, bool) {
	var id string
	row := stack.Service.DB.QueryRow(ctx, `
		SELECT id FROM session WHERE parent_id IS NULL ORDER BY time_updated DESC LIMIT 1`)
	if err := row.Scan(&id); err != nil {
		return "", false
	}
	return id, true
}

// streamRunTurn admits the prompt, blocks until the turn settles (via the
// same coordinator the HTTP server's SSE loop drains asynchronously — here
// we join it synchronously, since a one-shot CLI run has no other consumer
// to hand the drain to), then prints the resulting assistant message. This
// covers RunCommand's non-interactive loop() without needing a second event
// stream: the whole turn already happened by the time Resume returns.
func streamRunTurn(ctx context.Context, stack *stack, sessionID, message, format string, thinking bool, inputTok, outputTok *int, finish *string) error {
	messageID, err := stack.Service.Prompt(ctx, sessionID, message, session.DeliverySteer)
	if err != nil {
		return err
	}
	if err := stack.Service.Execution.Resume(ctx, sessionID); err != nil {
		return err
	}

	messages, err := stack.Service.Messages.List(ctx, sessionID)
	if err != nil {
		return err
	}
	var assistant *session.AssistantMessage
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Type != "assistant" {
			continue
		}
		decoded, err := session.DecodeAssistant(messages[i].Data)
		if err != nil {
			continue
		}
		assistant = &decoded
		break
	}
	if assistant == nil {
		return nil
	}

	var textOut strings.Builder
	for _, part := range assistant.Content {
		switch part.Type {
		case "text":
			textOut.WriteString(part.Text)
		case "reasoning":
			if thinking {
				fmt.Fprintf(os.Stderr, "Thinking: %s\n", part.Text)
			}
		}
	}
	if format == "json" {
		fmt.Printf(`{"type":"session.message","sessionID":%q,"messageID":%q,"text":%q}`+"\n", sessionID, messageID, textOut.String())
	} else {
		fmt.Println(textOut.String())
	}
	if assistant.Tokens != nil {
		*inputTok = assistant.Tokens.Input
		*outputTok = assistant.Tokens.Output
	}
	*finish = assistant.Finish
	if assistant.Error != nil {
		return fmt.Errorf("%s: %s", assistant.Error.Type, assistant.Error.Message)
	}
	return nil
}

// runInteractiveViaTUI starts the same Bubble Tea interface used by the bare
// "gocode" invocation, serving the already-booted stack and attached to
// the session created/resolved for this run. There is no separate "mini"
// split-footer surface in the Go port; see specs/go-port-plan.md §7.
func runInteractiveViaTUI(ctx context.Context, stack *stack, sessionID string, a *clix.Args) error {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	go func() {
		srv := &server.Server{Session: stack.Service, Bus: stack.Bus, Permissions: stack.Permissions, Models: stack.Models, Agents: stack.Agents, Config: stack.Config, MCP: stack.MCP, Jobs: stack.Jobs, Questions: stack.Questions, Skills: stack.Skills, LSP: stack.LSP, Commands: stack.Commands}
		server.ServeOn(listener, srv.Mux())
	}()
	opts := tui.RunOptions{DefaultModel: stack.ProviderID + "/" + stack.ModelID, SessionID: sessionID}
	themeName := tui.ResolveStartupTheme(stack.Config.Theme, tui.ThemeStatePath())
	if err := tui.Run(ctx, client.New("http://"+listener.Addr().String()), themeName, opts); err != nil {
		return fmt.Errorf("tui: %w", err)
	}
	return nil
}
