package builtins

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

// pathCommands are the commands whose arguments name files, ported from FILES
// in packages/opencode/src/tool/shell.ts. An argument to one of these that
// resolves outside the working directory needs external_directory approval.
var pathCommands = map[string]bool{
	"cd": true, "pushd": true, "popd": true,
	"rm": true, "cp": true, "mv": true, "mkdir": true, "rmdir": true,
	"touch": true, "chmod": true, "chown": true, "ln": true,
	"cat": true, "tee": true, "dd": true, "truncate": true,
	"install": true, "rsync": true, "shred": true,
}

// ScanExternalPaths returns the directories a shell command would touch
// outside root, so the caller can ask for approval before running it.
//
// This closes a hole the file tools cannot: write, edit and apply_patch all
// refuse a path outside the working directory, but the model can simply run
// `cat > /tmp/x` instead and reach anywhere on the filesystem. TypeScript
// guards this with an `external_directory` permission driven by a parse of the
// command (shell.ts's collect/ask); this is the Go equivalent.
//
// It parses rather than pattern-matches because the interesting cases are
// exactly the ones a regex gets wrong: quoting, heredocs, redirects, and
// commands buried in subshells or after && and |. Anything unresolvable at
// parse time — a path built from a variable, a command substitution — is not
// reported, so this is a guard against the common case, not a sandbox. The
// permission system, not this scan, is the security boundary.
func ScanExternalPaths(command, root string) []string {
	parser := syntax.NewParser(syntax.KeepComments(false))
	file, err := parser.Parse(strings.NewReader(command), "")
	if err != nil {
		// An unparseable command is reported as touching nothing rather than
		// blocking: the shell itself will fail on it in a moment.
		return nil
	}

	root = canonicalRoot(root)
	found := map[string]bool{}

	note := func(word *syntax.Word) {
		literal, ok := literalWord(word)
		if !ok {
			return
		}
		if dir, outside := externalDirectory(literal, root); outside {
			found[dir] = true
		}
	}

	syntax.Walk(file, func(node syntax.Node) bool {
		switch typed := node.(type) {
		case *syntax.Redirect:
			// Every redirect target is a path, whatever the command: this is
			// the `cat > /tmp/x` case that motivated the check.
			if typed.Word != nil {
				note(typed.Word)
			}
		case *syntax.CallExpr:
			if len(typed.Args) == 0 {
				return true
			}
			name, ok := literalWord(typed.Args[0])
			if !ok || !pathCommands[filepath.Base(name)] {
				return true
			}
			for _, arg := range typed.Args[1:] {
				literal, ok := literalWord(arg)
				if !ok || strings.HasPrefix(literal, "-") {
					continue // a flag, not a path
				}
				note(arg)
			}
		}
		return true
	})

	out := make([]string, 0, len(found))
	for dir := range found {
		out = append(out, dir)
	}
	sort.Strings(out)
	return out
}

// literalWord returns a word's text when it is statically known — literals and
// quoted literals. A word containing an expansion (a variable, a command
// substitution, a glob that has not run yet) has no single value at parse
// time, so it is reported as unknown rather than guessed at.
func literalWord(word *syntax.Word) (string, bool) {
	if word == nil {
		return "", false
	}
	var builder strings.Builder
	for _, part := range word.Parts {
		switch typed := part.(type) {
		case *syntax.Lit:
			builder.WriteString(typed.Value)
		case *syntax.SglQuoted:
			builder.WriteString(typed.Value)
		case *syntax.DblQuoted:
			for _, inner := range typed.Parts {
				lit, ok := inner.(*syntax.Lit)
				if !ok {
					return "", false
				}
				builder.WriteString(lit.Value)
			}
		default:
			return "", false
		}
	}
	return builder.String(), builder.Len() > 0
}

// externalDirectory reports the directory a path lives in when that path falls
// outside root.
//
// Only absolute paths and explicit parent traversals are considered: a plain
// relative path resolves inside the working directory, which is what the
// command's own permission already covers.
func externalDirectory(path, root string) (string, bool) {
	if path == "" {
		return "", false
	}
	// ~ is expanded by the shell, not by us, but it always lands outside a
	// project directory unless the project is the home directory itself.
	if strings.HasPrefix(path, "~") {
		if home, err := homeDir(); err == nil {
			path = filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(path, "~"), "/"))
		} else {
			return "", false
		}
	}
	if !filepath.IsAbs(path) {
		if !strings.Contains(path, "..") {
			return "", false
		}
		path = filepath.Join(root, path)
	}
	// Canonicalize the same way as the root before comparing. Without this,
	// every absolute path inside the project is reported as external on macOS,
	// where a temp or project directory routinely sits under a symlink
	// (/var -> /private/var), so the two spellings never match.
	path = canonicalPath(path)

	if containsPathIn(root, path) {
		return "", false
	}
	// Report the directory, matching the TypeScript scan, which asks for a
	// directory rather than each file in it. A path that is itself a directory
	// is its own answer; anything else contributes its parent.
	if info, err := os.Stat(path); err == nil && info.IsDir() {
		return path, true
	}
	return filepath.Dir(path), true
}

// canonicalPath resolves symlinks as far down as the path exists, then
// re-attaches the part that does not. A target being created does not exist
// yet, so EvalSymlinks on the whole path would fail and leave it uncomparable
// to the canonicalized root.
func canonicalPath(path string) string {
	path = filepath.Clean(path)
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	parent := filepath.Dir(path)
	if parent == path {
		return path
	}
	return filepath.Join(canonicalPath(parent), filepath.Base(path))
}

// containsPathIn reports whether path is root or sits beneath it.
func containsPathIn(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

func canonicalRoot(root string) string {
	if abs, err := filepath.Abs(root); err == nil {
		root = abs
	}
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		return resolved
	}
	return filepath.Clean(root)
}

func homeDir() (string, error) { return os.UserHomeDir() }
