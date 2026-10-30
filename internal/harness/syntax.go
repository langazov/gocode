package harness

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// SyntaxCheck parses a proposed script without running it, porting
// tree._py_compiles: a harness whose helper does not even parse is rejected
// before any rollout pays to discover that. Python goes through
// ast.parse — which builds a syntax tree and executes nothing — and shell
// through `sh -n`, which reads commands without running them. The source is
// passed on stdin, never as a file the interpreter could import from.
//
// A missing interpreter is not the proposal's fault, so the check is skipped
// (nil) rather than failed; the rollout will report the script then.
func SyntaxCheck(rel, src string) error {
	var name string
	var args []string
	switch {
	case strings.HasSuffix(rel, ".py"):
		name = "python3"
		args = []string{"-I", "-c", "import ast,sys; ast.parse(sys.stdin.read(), sys.argv[1])", rel}
	case strings.HasSuffix(rel, ".sh"):
		name = "sh"
		args = []string{"-n"}
	default:
		return nil
	}
	path, err := exec.LookPath(name)
	if err != nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Stdin = strings.NewReader(src)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil
		}
		detail := strings.TrimSpace(stderr.String())
		if lines := strings.Split(detail, "\n"); len(lines) > 2 {
			detail = strings.Join(lines[len(lines)-2:], " ")
		}
		return fmt.Errorf("%s does not parse: %s", rel, clip(detail, 160))
	}
	return nil
}
