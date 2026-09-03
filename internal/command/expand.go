package command

import (
	"context"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
)

// Regexes ported verbatim from packages/opencode/src/session/prompt.ts and
// config/markdown.ts, so tokenizing and substitution behave identically.
var (
	// argsPattern splits the argument string into tokens, keeping quoted runs
	// together and treating an "[Image N]" placeholder as one token.
	argsPattern = regexp.MustCompile(`(?i)\[Image\s+\d+\]|"[^"]*"|'[^']*'|[^\s"']+`)
	// placeholderPattern matches $1, $2, ...
	placeholderPattern = regexp.MustCompile(`\$(\d+)`)
	// shellPattern matches !`command`, whose output is substituted in.
	shellPattern = regexp.MustCompile("!`([^`]+)`")
)

// shellTimeout bounds a substitution command. A template that shells out
// should not be able to hang the turn indefinitely.
const shellTimeout = 30 * time.Second

// Expand substitutes a command's arguments into its template, porting the
// substitution block in prompt.ts.
//
// The rules, in order:
//
//   - `$1`, `$2`, ... take the corresponding argument. The *highest-numbered*
//     placeholder is greedy: it takes that argument and every one after it,
//     so `/commit $1` with three words gets all three rather than only the
//     first. A placeholder past the end of the arguments becomes empty.
//   - `$ARGUMENTS` takes the whole raw argument string.
//   - If the template uses no placeholders at all and arguments were given,
//     they are appended after a blank line — otherwise typing them would have
//     no effect.
//   - “ !`cmd` “ is replaced by the command's output.
func Expand(ctx context.Context, template, arguments, shell string) string {
	args := tokenizeArguments(arguments)

	placeholders := placeholderPattern.FindAllStringSubmatch(template, -1)
	last := 0
	for _, match := range placeholders {
		if value, err := atoi(match[1]); err == nil && value > last {
			last = value
		}
	}

	expanded := placeholderPattern.ReplaceAllStringFunc(template, func(match string) string {
		position, err := atoi(strings.TrimPrefix(match, "$"))
		if err != nil {
			return match
		}
		index := position - 1
		if index < 0 || index >= len(args) {
			return ""
		}
		if position == last {
			// The last placeholder soaks up the remaining arguments.
			return strings.Join(args[index:], " ")
		}
		return args[index]
	})

	usesArguments := strings.Contains(template, "$ARGUMENTS")
	expanded = strings.ReplaceAll(expanded, "$ARGUMENTS", arguments)

	if len(placeholders) == 0 && !usesArguments && strings.TrimSpace(arguments) != "" {
		expanded = expanded + "\n\n" + arguments
	}

	expanded = expandShell(ctx, expanded, shell)
	return strings.TrimSpace(expanded)
}

// tokenizeArguments splits an argument string the way prompt.ts does, then
// strips one layer of surrounding quotes from each token.
func tokenizeArguments(arguments string) []string {
	matches := argsPattern.FindAllString(arguments, -1)
	out := make([]string, 0, len(matches))
	for _, match := range matches {
		out = append(out, trimQuotes(match))
	}
	return out
}

// trimQuotes ports `replace(/^["']|["']$/g, "")`, which strips a leading and a
// trailing quote independently rather than requiring a matched pair.
func trimQuotes(value string) string {
	value = strings.TrimPrefix(strings.TrimPrefix(value, `"`), `'`)
	return strings.TrimSuffix(strings.TrimSuffix(value, `"`), `'`)
}

// expandShell replaces each !`cmd` with the command's output.
//
// Failures substitute empty rather than aborting: upstream runs these with
// `nothrow`, so a template referencing a command that is not installed still
// produces a usable prompt.
func expandShell(ctx context.Context, template, shell string) string {
	if !strings.Contains(template, "!`") {
		return template
	}
	flag := "-c"
	if shell == "" {
		shell = "/bin/sh"
		if runtime.GOOS == "windows" {
			shell = "cmd"
		}
	}
	if base := filepath.Base(shell); strings.EqualFold(base, "cmd") || strings.EqualFold(base, "cmd.exe") {
		// cmd.exe takes /C, not the POSIX shells' -c.
		flag = "/C"
	}
	return shellPattern.ReplaceAllStringFunc(template, func(match string) string {
		inner := shellPattern.FindStringSubmatch(match)
		if len(inner) < 2 {
			return ""
		}
		runCtx, cancel := context.WithTimeout(ctx, shellTimeout)
		defer cancel()
		out, err := exec.CommandContext(runCtx, shell, flag, inner[1]).Output()
		if err != nil {
			return ""
		}
		return strings.TrimRight(string(out), "\n")
	})
}
