package permission

import "strings"

// ArityPrefix derives the subcommand prefix an "always" approval of a bash
// command should save, porting packages/opencode/src/permission/arity.ts.
//
// The problem it solves: saving the exact command means "allow always" on
// `git commit -m "x"` covers only that literal string, so the next commit
// re-prompts and users read the prompt as broken. Saving "*" would mean one
// approval covers every command, which is not what was agreed either. The
// arity prefix is the middle: `git commit -m "x"` saves "git commit *", which
// the wildcard suffix rule (a pattern ending in " *" also matches the bare
// prefix) matches against every variant of that subcommand and nothing else.
//
// The dictionary exists because "the subcommand" is not a syntactic notion:
// for `git commit` it is two words, for `docker run` two, for `npm test` one.
// The list below mirrors the upstream dictionary's shape — common developer
// commands with the number of leading words that form the stable prefix. A
// command not in the dictionary saves its first word, which is conservative
// in the right direction: narrower grants, more asks.
var arityDictionary = map[string]int{
	"git":       2,
	"docker":    2,
	"npm":       2,
	"npx":       2,
	"yarn":      2,
	"pnpm":      2,
	"cargo":     2,
	"go":        2,
	"python":    2,
	"python3":   2,
	"pip":       2,
	"pip3":      2,
	"uv":        2,
	"ruby":      2,
	"bundle":    2,
	"gem":       2,
	"dotnet":    2,
	"gradle":    2,
	"mvn":       2,
	"make":      2,
	"kubectl":   2,
	"helm":      2,
	"terraform": 2,
	"aws":       2,
	"gcloud":    2,
	"az":        2,
	"gh":        2,
	"brew":      2,
	"apt":       2,
	"apt-get":   2,
	"systemctl": 2,
	"ssh":       2,
	"scp":       2,
	"rsync":     2,
	"curl":      2,
	"wget":      2,
	"cd":        1,
	"ls":        1,
	"cat":       1,
	"echo":      1,
	"grep":      1,
	"find":      1,
	"sed":       1,
	"awk":       1,
	"head":      1,
	"tail":      1,
	"wc":        1,
	"sort":      1,
	"uniq":      1,
	"tr":        1,
	"cut":       1,
	"mkdir":     1,
	"touch":     1,
	"cp":        1,
	"mv":        1,
	"pwd":       1,
	"whoami":    1,
	"date":      1,
	"which":     1,
	"env":       1,
	"true":      1,
	"false":     1,
}

// ArityPrefix returns the arity-based save pattern for command, or "" when
// there is nothing to derive (empty or whitespace-only input).
//
// The returned pattern always ends in " *": the wildcard suffix rule (a
// pattern ending in " *" also matches the bare prefix) is what makes one
// pattern cover both the subcommand itself and every variant of it, so the
// grant is uniform whether the approved command was exactly at its arity or
// longer.
func ArityPrefix(command string) string {
	fields := strings.Fields(command)
	if len(fields) == 0 {
		return ""
	}
	// A leading env assignment (FOO=bar cmd) or command prefix (sudo, env)
	// is transparent: the arity of the wrapped command is what the user
	// reads the command as.
	start := 0
	for start < len(fields) {
		head := fields[start]
		if strings.Contains(head, "=") && len(head) > 1 && !strings.HasPrefix(head, "-") {
			start++
			continue
		}
		if head == "sudo" || head == "env" || head == "nohup" {
			start++
			continue
		}
		break
	}
	if start >= len(fields) {
		// Only assignments and wrappers: nothing stable to save.
		return ""
	}
	tail := fields[start:]
	// Unknown command: the first word alone, never "*".
	words := arityDictionary[strings.ToLower(tail[0])]
	if words < 1 {
		return tail[0] + " *"
	}
	if words > len(tail) {
		words = len(tail)
	}
	return strings.Join(tail[:words], " ") + " *"
}
