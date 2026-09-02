package builtins

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	"github.com/anomalyco/opencode-go/internal/permission"
	"github.com/anomalyco/opencode-go/internal/tool"
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

// ExtraPermissions implements tool.PermissionScoped: it reports the
// directories this command would reach outside the working directory.
//
// Without it the working-directory restriction on write, edit and apply_patch
// is decorative — the model that cannot write /tmp/x with the write tool can
// always run `cat > /tmp/x` instead, which is exactly what happens in practice.
func (t *BashTool) ExtraPermissions(input map[string]any) []tool.ExtraPermission {
	command := stringArg(input, "command")
	if command == "" {
		return nil
	}
	directories := ScanExternalPaths(command, t.resolver.Root)
	if len(directories) == 0 {
		return nil
	}
	// The resource is a glob over the directory, matching
	// LocationMutation.externalDirectoryPermission, so approving once covers
	// the directory, its subdirectories and everything in them rather than
	// the single file the command happened to name. Wildcard `*` compiles to
	// `.*`, which crosses separators, so `/srv/data/*` also covers
	// `/srv/data/a/b.txt`.
	//
	// Save mirrors Resources: the TypeScript store persists the same glob it
	// asked about, which is what makes "allow always" cover the directory
	// tree instead of re-asking for each new file in it.
	resources := make([]string, 0, len(directories))
	for _, dir := range directories {
		resources = append(resources, filepath.ToSlash(filepath.Join(dir, "*")))
	}
	return []tool.ExtraPermission{{
		Action:    permission.ExternalDirectoryAction,
		Resources: resources,
		Save:      resources,
	}}
}
