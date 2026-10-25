package session

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/bmatcuk/doublestar/v4"

	"github.com/langazov/gocode-go/internal/fsutil"
)

// Instructions resolves the always-on instruction files that ride in every
// turn's system prompt, porting the system half of
// packages/opencode/src/session/instruction.ts:
//
//   - the first global file that exists: <config>/AGENTS.md, then
//     ~/.claude/CLAUDE.md;
//   - the first project file name with any match walking up from Directory
//     to Worktree — AGENTS.md, then CLAUDE.md, then the deprecated CONTEXT.md
//     — every ancestor match of that one name, so AGENTS.md and CLAUDE.md are
//     never stacked;
//   - each config "instructions" entry: an absolute path or glob, a ~/ path,
//     or a glob resolved upward from Directory to Worktree.
//
// Files are re-read on every call, so an edit lands on the next turn, as
// upstream does. URL entries are skipped: upstream fetches them, gocode does
// not reach the network to build a prompt (documentation/10-development.md).
type Instructions struct {
	Directory string
	Worktree  string
	// Config is the merged config's "instructions" list.
	Config []string
	// GlobalConfig is the gocode config directory holding the global AGENTS.md.
	GlobalConfig string
	Home         string
	// DisableClaudeCode drops CLAUDE.md at both scopes, porting
	// OPENCODE_DISABLE_CLAUDE_CODE_PROMPT.
	DisableClaudeCode bool
	// DisableProject skips project-level discovery, porting
	// OPENCODE_DISABLE_PROJECT_CONFIG.
	DisableProject bool
}

// Paths returns the instruction files in prompt order, deduplicated.
func (in *Instructions) Paths() []string {
	if in == nil {
		return nil
	}
	var paths []string
	seen := map[string]bool{}
	add := func(path string) {
		if abs, err := filepath.Abs(path); err == nil {
			path = abs
		}
		if !seen[path] {
			seen[path] = true
			paths = append(paths, path)
		}
	}

	var globals []string
	if in.GlobalConfig != "" {
		globals = append(globals, filepath.Join(in.GlobalConfig, "AGENTS.md"))
	}
	if !in.DisableClaudeCode && in.Home != "" {
		globals = append(globals, filepath.Join(in.Home, ".claude", "CLAUDE.md"))
	}
	for _, file := range globals {
		if isFile(file) {
			add(file)
			break
		}
	}

	if !in.DisableProject && in.Directory != "" {
		names := []string{"AGENTS.md"}
		if !in.DisableClaudeCode {
			names = append(names, "CLAUDE.md")
		}
		names = append(names, "CONTEXT.md")
		for _, name := range names {
			matches := fsutil.FindUp(name, in.Directory, in.stop())
			if len(matches) > 0 {
				for _, match := range matches {
					add(match)
				}
				break
			}
		}
	}

	for _, raw := range in.Config {
		raw = strings.TrimSpace(raw)
		if raw == "" || strings.HasPrefix(raw, "https://") || strings.HasPrefix(raw, "http://") {
			continue
		}
		instruction := raw
		if strings.HasPrefix(instruction, "~/") && in.Home != "" {
			instruction = filepath.Join(in.Home, instruction[2:])
		}
		for _, match := range in.resolve(instruction) {
			add(match)
		}
	}
	return paths
}

// System renders each instruction file as one system block, in the
// "Instructions from: <path>" form upstream uses. Unreadable and empty files
// are dropped.
func (in *Instructions) System() []string {
	var blocks []string
	for _, path := range in.Paths() {
		data, err := os.ReadFile(path)
		if err != nil || strings.TrimSpace(string(data)) == "" {
			continue
		}
		blocks = append(blocks, "Instructions from: "+path+"\n"+string(data))
	}
	return blocks
}

func (in *Instructions) stop() string {
	if in.Worktree != "" {
		return in.Worktree
	}
	return in.Directory
}

// resolve expands one instruction entry. An absolute entry globs its base
// name inside its own directory; a relative one is globbed at every level
// from Directory up to Worktree (fs.globUp upstream), nearest first.
func (in *Instructions) resolve(instruction string) []string {
	if filepath.IsAbs(instruction) {
		return globFiles(filepath.Dir(instruction), filepath.Base(instruction))
	}
	if in.DisableProject {
		return globFiles(in.GlobalConfig, instruction)
	}
	var out []string
	current := in.Directory
	stop := in.stop()
	for {
		out = append(out, globFiles(current, instruction)...)
		if current == stop {
			break
		}
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
		current = parent
	}
	return out
}

func globFiles(dir, pattern string) []string {
	if dir == "" {
		return nil
	}
	matches, err := doublestar.Glob(os.DirFS(dir), filepath.ToSlash(pattern))
	if err != nil {
		return nil
	}
	var out []string
	for _, match := range matches {
		path := filepath.Join(dir, filepath.FromSlash(match))
		if isFile(path) {
			out = append(out, path)
		}
	}
	return out
}

func isFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
