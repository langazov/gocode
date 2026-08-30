package builtins

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"runtime"
	"time"
)

const (
	defaultTimeoutMS = 2 * 60 * 1000
	maxTimeoutMS     = 10 * 60 * 1000
	maxOutputBytes   = 512 * 1024
)

type BashTool struct {
	resolver Resolver
}

func NewBashTool(resolver Resolver) *BashTool {
	return &BashTool{resolver: resolver}
}

func (t *BashTool) Name() string { return "bash" }

func (t *BashTool) Description() string {
	return "Execute one shell command string. The working directory is the default workdir; relative workdir values resolve from it. Timeout values are milliseconds."
}

func (t *BashTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"command": map[string]any{
				"type":        "string",
				"description": "Shell command string to execute",
			},
			"workdir": map[string]any{
				"type":        "string",
				"description": "Working directory. Defaults to the project root; relative paths resolve from it.",
			},
			"timeout": map[string]any{
				"type":        "integer",
				"description": fmt.Sprintf("Timeout in milliseconds. Defaults to %d and may not exceed %d.", defaultTimeoutMS, maxTimeoutMS),
			},
		},
		"required": []string{"command"},
	}
}

func (t *BashTool) Execute(ctx context.Context, input map[string]any) (string, error) {
	command := stringArg(input, "command")
	if command == "" {
		return "", fmt.Errorf("bash: command is required")
	}
	timeoutMS := intArg(input, "timeout", defaultTimeoutMS)
	if timeoutMS <= 0 {
		timeoutMS = defaultTimeoutMS
	}
	if timeoutMS > maxTimeoutMS {
		return "", fmt.Errorf("bash: timeout may not exceed %d", maxTimeoutMS)
	}
	workdir := t.resolver.Root
	if requested := stringArg(input, "workdir"); requested != "" {
		resolved, err := t.resolver.Resolve(requested)
		if err != nil {
			return "", err
		}
		workdir = resolved
	}

	runCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutMS)*time.Millisecond)
	defer cancel()

	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(runCtx, "cmd.exe", "/c", command)
	} else {
		cmd = exec.CommandContext(runCtx, "/bin/sh", "-c", command)
	}
	cmd.Dir = workdir

	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	err := cmd.Run()
	timedOut := runCtx.Err() == context.DeadlineExceeded

	text := output.String()
	truncated := false
	if len(text) > maxOutputBytes {
		text = text[:maxOutputBytes]
		truncated = true
	}
	if timedOut {
		return text, fmt.Errorf("bash: command timed out after %dms", timeoutMS)
	}
	if err != nil {
		exitCode := 1
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		}
		if text == "" {
			text = err.Error()
		}
		if truncated {
			text += "\n(output truncated)"
		}
		return text, fmt.Errorf("bash: command exited with code %d", exitCode)
	}
	if truncated {
		text += "\n(output truncated)"
	}
	if text == "" {
		return "(no output)", nil
	}
	return text, nil
}
