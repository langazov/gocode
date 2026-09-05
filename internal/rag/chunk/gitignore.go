// gitignore.go gives Walk a git-compatible-enough understanding of
// .gitignore and .ignore files, so a project's own build-output and
// vendored-dependency exclusions get honored automatically instead of
// needing to be hand-duplicated into Options.Exclude (or the rag-plugin's
// separate defaultExclude list, which only knows a handful of directory
// names common across ecosystems, not whatever a given project actually
// generates).
//
// This is deliberately not a full git reimplementation:
//   - No core.excludesFile or .git/info/exclude: locating the actual
//     repository root (as opposed to whatever directory happens to hold a
//     .gitignore) isn't reliable once root is an arbitrary reindexed
//     subdirectory, and the global file is a per-machine preference the
//     project's own index shouldn't depend on.
//   - Matching uses doublestar's glob semantics rather than git's own
//     wildmatch. The two agree on everything but wildmatch's rarer corners
//     (some bracket-expression edge cases), which essentially no real
//     .gitignore relies on.
//   - A directory's own ignore file cannot exclude the directory itself
//     (matches real git); once a directory is excluded its subtree is never
//     walked at all, so a negated pattern nested inside it can never
//     resurrect it — also matches real git, which requires every parent
//     directory to already be un-excluded before a negated file inside it
//     can be.
package chunk

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
)

// ignoreFilenames are read from every directory Walk descends into (unless
// Options.DisableGitignore is set). Both use identical syntax; .ignore is a
// git-independent convention (ripgrep, ag, and others) for excludes that
// don't belong in the tracked .gitignore.
var ignoreFilenames = []string{".gitignore", ".ignore"}

// ignoreRule is one compiled, non-comment, non-blank line from an ignore
// file.
type ignoreRule struct {
	// pattern is a doublestar glob already resolved to match relative to the
	// ignore file's own directory: prefixed with "**/" when the source line
	// had no other slash (so it matches at any depth below that directory,
	// per git's rule), used as-is otherwise (a leading or internal slash
	// anchors it to that directory).
	pattern string
	negate  bool
	dirOnly bool
}

// ignoreLevel is the compiled rules that apply to everything under dir —
// not dir itself; a directory's own ignore file only governs its children.
type ignoreLevel struct {
	dir   string
	rules []ignoreRule
}

// loadIgnoreLevel reads dir's ignore files, if any. It always returns a
// (possibly ruleless) level rather than an error: a missing or unreadable
// ignore file simply contributes no rules, it isn't a walk failure.
func loadIgnoreLevel(dir string) ignoreLevel {
	level := ignoreLevel{dir: dir}
	for _, name := range ignoreFilenames {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		level.rules = append(level.rules, parseIgnoreFile(string(data))...)
	}
	return level
}

func parseIgnoreFile(data string) []ignoreRule {
	var rules []ignoreRule
	scanner := bufio.NewScanner(strings.NewReader(data))
	for scanner.Scan() {
		line := strings.TrimRight(scanner.Text(), " \t")
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		var rule ignoreRule
		if strings.HasPrefix(line, "!") {
			rule.negate = true
			line = line[1:]
		}
		if strings.HasSuffix(line, "/") {
			rule.dirOnly = true
			line = strings.TrimSuffix(line, "/")
		}
		if line == "" {
			continue
		}
		anchored := strings.HasPrefix(line, "/")
		line = strings.TrimPrefix(line, "/")
		if line == "" {
			continue
		}
		if !anchored && !strings.Contains(line, "/") {
			line = "**/" + line
		}
		rule.pattern = line
		rules = append(rules, rule)
	}
	return rules
}

// isWithinDir reports whether path is dir itself or nested under it.
func isWithinDir(dir, path string) bool {
	if path == dir {
		return true
	}
	return strings.HasPrefix(path, dir+string(filepath.Separator))
}

// ignoreStack tracks the ignore levels active for the directory currently
// being walked: one level per ancestor directory from the project's
// ignore-lookup base down to whichever directory Walk is inside right now.
type ignoreStack struct {
	levels []ignoreLevel
}

// newIgnoreStack seeds the stack with every ancestor from ignoreBase down to
// root (root's own level included, as the top of the stack), so a scoped
// reindex — root a subdirectory of the actual project — still honors the
// project's top-level .gitignore. ignoreBase empty (or not actually an
// ancestor of root) means "just root," matching a whole-project walk where
// there is no wider ancestor to consult.
func newIgnoreStack(root, ignoreBase string) *ignoreStack {
	if ignoreBase == "" {
		ignoreBase = root
	}
	ignoreBase = filepath.Clean(ignoreBase)
	root = filepath.Clean(root)

	var dirs []string
	for d := root; ; {
		dirs = append(dirs, d)
		if d == ignoreBase {
			break
		}
		parent := filepath.Dir(d)
		if parent == d {
			// Reached the filesystem root without finding ignoreBase as an
			// actual ancestor; stop here rather than loop forever. root's
			// own level (already collected above) is still honored.
			break
		}
		d = parent
	}
	levels := make([]ignoreLevel, len(dirs))
	for i, d := range dirs {
		levels[len(dirs)-1-i] = loadIgnoreLevel(d)
	}
	return &ignoreStack{levels: levels}
}

// ignored reports whether path (a directory or file strictly under the
// stack's current top level) should be skipped, per every active ignore
// level from the outermost ancestor down to path's immediate parent. Rules
// are evaluated outermost-first with the last match winning, matching git's
// own precedence: a deeper, more specific pattern overrides a shallower one.
func (s *ignoreStack) ignored(path string, isDir bool) bool {
	result := false
	for _, level := range s.levels {
		rel, err := filepath.Rel(level.dir, path)
		if err != nil || rel == "." {
			continue
		}
		rel = filepath.ToSlash(rel)
		for _, rule := range level.rules {
			if rule.dirOnly && !isDir {
				continue
			}
			if ok, _ := doublestar.Match(rule.pattern, rel); ok {
				result = !rule.negate
			}
		}
	}
	return result
}

// descend pops levels no longer ancestral to path (Walk backtracking out of
// a subtree it just finished), then reports whether path itself is ignored
// given what remains. Call before deciding to skip a directory or file; for
// a directory that isn't skipped, follow with push.
func (s *ignoreStack) descend(path string, isDir bool) bool {
	for len(s.levels) > 1 && !isWithinDir(s.levels[len(s.levels)-1].dir, path) {
		s.levels = s.levels[:len(s.levels)-1]
	}
	return s.ignored(path, isDir)
}

// push loads dir's own ignore file(s) so its children get matched against
// them too. The caller must not push root itself — newIgnoreStack already
// loaded root's level as the stack's initial top.
func (s *ignoreStack) push(dir string) {
	s.levels = append(s.levels, loadIgnoreLevel(dir))
}
