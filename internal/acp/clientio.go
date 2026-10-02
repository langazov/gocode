package acp

import (
	"context"
	"errors"
	"runtime"
	"time"

	"github.com/langazov/gocode-go/internal/tool"
)

// clientFS serves the file tools through a v1 client's fs/read_text_file and
// fs/write_text_file (protocol/v1/file-system): reads see the editor's
// unsaved buffers, writes land as edits the editor tracks. Only the halves
// the client advertised are used; the other falls back to the local disk.
type clientFS struct {
	agent       *Agent
	sessionID   string
	read, write bool
}

func (f *clientFS) CanRead() bool  { return f.read }
func (f *clientFS) CanWrite() bool { return f.write }

func (f *clientFS) ReadTextFile(ctx context.Context, path string) (string, error) {
	var out struct {
		Content string `json:"content"`
	}
	err := f.agent.call(ctx, "fs/read_text_file", obj{"sessionId": f.sessionID, "path": path}, &out)
	return out.Content, err
}

func (f *clientFS) WriteTextFile(ctx context.Context, path, content string) error {
	return f.agent.call(ctx, "fs/write_text_file", obj{"sessionId": f.sessionID, "path": path, "content": content}, nil)
}

// clientTerminal runs the bash tool's commands in a v1 client's terminal
// (protocol/v1/terminals): create, embed the live terminal in the tool call,
// wait with a timeout, and always release.
type clientTerminal struct {
	agent   *Agent
	session *acpSession
}

func (t *clientTerminal) Run(ctx context.Context, request tool.TerminalRequest) (tool.TerminalResult, error) {
	a, sessionID := t.agent, t.session.id
	command, args := shellInvocation(request.Command)
	create := obj{
		"sessionId": sessionID,
		"command":   command,
		"args":      args,
		"env":       []obj{},
	}
	if request.Cwd != "" {
		create["cwd"] = request.Cwd
	}
	if request.OutputLimit > 0 {
		create["outputByteLimit"] = request.OutputLimit
	}
	var created struct {
		TerminalID string `json:"terminalId"`
	}
	if err := a.call(ctx, "terminal/create", create, &created); err != nil {
		return tool.TerminalResult{}, err
	}
	terminalID := created.TerminalID
	// Release no matter how this ends: the agent MUST release every terminal
	// it creates, and the client keeps showing an embedded one afterwards.
	defer func() {
		releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = a.call(releaseCtx, "terminal/release", obj{"sessionId": sessionID, "terminalId": terminalID}, nil)
	}()

	// Show the live terminal in the tool call (protocol/v1/terminals
	// "Embedding in Tool Calls").
	if request.CallID != "" {
		a.update(sessionID, obj{
			"sessionUpdate": "tool_call_update",
			"toolCallId":    request.CallID,
			"status":        "in_progress",
			"content":       []obj{{"type": "terminal", "terminalId": terminalID}},
		})
		t.session.markTerminal(request.CallID, terminalID)
	}

	waitCtx := ctx
	cancel := func() {}
	if request.Timeout > 0 {
		waitCtx, cancel = context.WithTimeout(ctx, request.Timeout)
	}
	defer cancel()
	var exit struct {
		ExitCode *int   `json:"exitCode"`
		Signal   string `json:"signal"`
	}
	waitErr := a.call(waitCtx, "terminal/wait_for_exit", obj{"sessionId": sessionID, "terminalId": terminalID}, &exit)
	timedOut := false
	if waitErr != nil {
		if !errors.Is(waitErr, context.DeadlineExceeded) && !errors.Is(waitErr, context.Canceled) {
			return tool.TerminalResult{}, waitErr
		}
		timedOut = errors.Is(waitErr, context.DeadlineExceeded) && ctx.Err() == nil
		// Timed out or the turn was cancelled: kill, then collect what was
		// printed before reporting.
		killCtx, killCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		_ = a.call(killCtx, "terminal/kill", obj{"sessionId": sessionID, "terminalId": terminalID}, nil)
		killCancel()
	}

	outputCtx, outputCancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer outputCancel()
	var output struct {
		Output     string `json:"output"`
		Truncated  bool   `json:"truncated"`
		ExitStatus *struct {
			ExitCode *int   `json:"exitCode"`
			Signal   string `json:"signal"`
		} `json:"exitStatus"`
	}
	if err := a.call(outputCtx, "terminal/output", obj{"sessionId": sessionID, "terminalId": terminalID}, &output); err != nil {
		return tool.TerminalResult{}, err
	}
	result := tool.TerminalResult{
		Output:    output.Output,
		Truncated: output.Truncated,
		ExitCode:  exit.ExitCode,
		Signal:    exit.Signal,
		TimedOut:  timedOut,
	}
	if waitErr != nil && output.ExitStatus != nil {
		result.ExitCode, result.Signal = output.ExitStatus.ExitCode, output.ExitStatus.Signal
	}
	if ctx.Err() != nil {
		return result, ctx.Err()
	}
	return result, nil
}

// shellInvocation turns a command line into the program and arguments
// terminal/create takes: the client executes a program, not a shell line.
func shellInvocation(line string) (string, []string) {
	if runtime.GOOS == "windows" {
		return "cmd.exe", []string{"/c", line}
	}
	return "/bin/sh", []string{"-c", line}
}
