// Package builtins ports the core filesystem and shell tools from
// packages/core/src/tool. Tools are scoped to a root directory (the Go
// analogue of the active Location); relative paths resolve within it and
// paths escaping it are rejected unless externally approved.
package builtins

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/langazov/gocode-go/internal/db"
	"github.com/langazov/gocode-go/internal/skill"
	"github.com/langazov/gocode-go/internal/tool"
)

// Resolver scopes tool paths to a root directory.
type Resolver struct {
	Root string
	// Allow names directories outside Root the file tools may still reach,
	// as absolute paths. Only the global plans directory uses this: plan mode
	// has to be able to write a plan somewhere, and that somewhere is
	// deliberately not the repository (see global.PlansDir).
	//
	// This widens the sandbox, not the policy. Whether a given tool may write
	// to one of these paths is still the permission engine's decision — the
	// plan agent allows `edit` only under the plans directory, and denies it
	// everywhere else.
	Allow []string
}

// Resolve returns the absolute path for input, keeping it within the root or
// one of the explicitly allowed directories.
func (r Resolver) Resolve(input string) (string, error) {
	candidate := input
	if !filepath.IsAbs(candidate) && !isRootedPath(candidate) {
		candidate = filepath.Join(r.Root, candidate)
	}
	candidate = filepath.Clean(candidate)
	if within(candidate, r.Root) {
		return candidate, nil
	}
	for _, allowed := range r.Allow {
		if within(candidate, allowed) {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("path escapes working directory: %s", input)
}

// within reports whether candidate is dir itself or sits underneath it.
func within(candidate, dir string) bool {
	if dir == "" {
		return false
	}
	dir = filepath.Clean(dir)
	return candidate == dir || strings.HasPrefix(candidate, dir+string(filepath.Separator))
}

// Options carries the optional services that gate the tools needing them.
// A nil field disables the tools that depend on it rather than registering a
// tool that would fail at call time.
type Options struct {
	// Database backs session-aware tools such as todowrite.
	Database *db.DB
	// Skills, when set, enables the skill tool.
	Skills *skill.Registry
	// Asker, when set, enables the question tool and plan mode.
	Asker Asker
	// AgentSwitcher, together with Asker, enables plan mode.
	AgentSwitcher AgentSwitcher
	// Diagnoser, when set, reports language-server diagnostics on the files the
	// edit, write and patch tools change, and warms servers on read.
	Diagnoser Diagnoser
	// AllowPaths are directories outside root the file tools may reach. See
	// Resolver.Allow — in practice this is the global plans directory, the one
	// place plan mode is allowed to write.
	AllowPaths []string
}

// Register adds all built-in tools to the registry, scoped to root.
func Register(registry *tool.Registry, root string, database *db.DB) {
	RegisterWith(registry, root, Options{Database: database})
}

// RegisterWith adds the built-in tools, enabling the optional ones whose
// services are supplied.
func RegisterWith(registry *tool.Registry, root string, opts Options) {
	resolver := Resolver{Root: root, Allow: opts.AllowPaths}
	registry.Register(NewReadToolWith(resolver, opts.Diagnoser))
	registry.Register(NewWriteToolWith(resolver, opts.Diagnoser))
	registry.Register(NewEditToolWith(resolver, opts.Diagnoser))
	registry.Register(NewGlobTool(resolver))
	registry.Register(NewGrepTool(resolver))
	registry.Register(NewBashTool(resolver))
	registry.Register(NewWebFetchTool())
	registry.Register(NewWebSearchTool())
	registry.Register(NewApplyPatchToolWith(resolver, opts.Diagnoser))
	if opts.Database != nil {
		registry.Register(NewTodoTool(opts.Database))
	}
	if opts.Skills != nil && len(opts.Skills.Names()) > 0 {
		registry.Register(NewSkillTool(opts.Skills))
	}
	if opts.Asker != nil {
		registry.Register(NewQuestionTool(opts.Asker))
		if opts.AgentSwitcher != nil {
			registry.Register(NewPlanEnterTool(opts.Asker, opts.AgentSwitcher))
			registry.Register(NewPlanExitTool(opts.Asker, opts.AgentSwitcher))
		}
	}
}

func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}
