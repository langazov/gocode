package main

import (
	"path/filepath"

	"github.com/langazov/gocode-go/internal/config"
	"github.com/langazov/gocode-go/internal/configpaths"
	"github.com/langazov/gocode-go/internal/flag"
	"github.com/langazov/gocode-go/internal/global"
	"github.com/langazov/gocode-go/internal/harness"
	"github.com/langazov/gocode-go/internal/session"
)

// instructionsFor builds the instruction-file source for a stack rooted at
// workdir, or nil when GOCODE_DISABLE_INSTRUCTIONS opts out of them entirely.
// The return type is the interface so the opt-out is a true nil, not a nil
// pointer the runner would see as a source.
func instructionsFor(workdir string, cfg *config.Config) session.InstructionSource {
	if flag.Truthy("GOCODE_DISABLE_INSTRUCTIONS") {
		return nil
	}
	paths := global.Resolve()
	var configured []string
	if cfg != nil {
		configured = cfg.Instructions
	}
	return &session.Instructions{
		Directory:         workdir,
		Worktree:          configpaths.Worktree(workdir),
		Config:            configured,
		GlobalConfig:      paths.Config,
		Home:              paths.Home,
		DisableClaudeCode: flag.Truthy("GOCODE_DISABLE_CLAUDE_CODE_PROMPT"),
		DisableProject:    flag.DisableProjectConfig(),
	}
}

// resolveHarnessOverlay picks the harness a stack runs with. An explicit
// directory (a Harness² rollout) is overlaid first, ahead of the user's own
// skills and agents. Otherwise a harness installed in the project is overlaid
// last, below everything the user wrote, unless GOCODE_DISABLE_HARNESS is set
// or the config says "harness": {"enabled": false}.
func resolveHarnessOverlay(workdir, explicit string, cfg *config.Config) (*harness.Overlay, bool) {
	if explicit != "" {
		return harness.NewOverlay(explicit), true
	}
	if flag.Truthy("GOCODE_DISABLE_HARNESS") || !cfg.HarnessEnabled() {
		return nil, false
	}
	return harness.NewOverlay(filepath.Join(workdir, filepath.FromSlash(harness.InstalledDir))), false
}

// harnessRoots places the overlay's directory among the skill or agent roots:
// first when it must win collisions, last when it must lose them. Discovery
// is first-writer-wins in both registries.
func harnessRoots(overlay *harness.Overlay, first bool, roots ...string) []string {
	if overlay == nil {
		return roots
	}
	if first {
		return append([]string{overlay.Dir}, roots...)
	}
	return append(roots, overlay.Dir)
}
