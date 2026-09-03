// Package clix is a small yargs-like command line parser used to give the Go
// port the same command/flag surface as the TypeScript CLI
// (packages/opencode/src/cli). It is not a general purpose library: it only
// implements the subset of yargs behavior the TS commands actually use —
// options with aliases/choices/defaults/arrays, positionals (including
// trailing array positionals), nested subcommands, "--" passthrough
// (parserConfiguration({ populate--: true })), and demandCommand().
package clix

import (
	"fmt"
	"io"
	"os"
	"slices"
	"sort"
	"strconv"
	"strings"
)

// Kind is the value type of a Flag or Positional.
type Kind int

const (
	KindBool Kind = iota
	KindString
	KindNumber
	KindStringArray
)

// Flag is a yargs `.option(name, {...})`.
type Flag struct {
	Name     string // canonical long name, e.g. "log-level"
	Aliases  []string
	Kind     Kind
	Default  any // bool, string, float64, []string, or nil
	Choices  []string
	Hidden   bool
	Describe string
}

// Positional is a yargs `.positional(name, {...})` from the command string.
type Positional struct {
	Name     string
	Array    bool // trailing "..name" consumes all remaining positionals
	Required bool
	Describe string
}

// Command is one yargs CommandModule. A Command with Sub commands acts as a
// dispatcher (optionally with its own Run, matching yargs "$0" defaults);
// a Command with Run and no Sub is a leaf.
type Command struct {
	Name        string
	Aliases     []string
	Describe    string // shown in help; hidden commands still function
	Hidden      bool
	Flags       []Flag
	Positionals []Positional
	AllowExtra  bool // populate the "--" passthrough into Args.Extra
	Sub         []*Command
	Demand      bool // yargs .demandCommand(): error if no sub matches and Run is nil
	Run         func(*Args) error
}

// Args is the parsed result handed to a Command's Run.
type Args struct {
	Bools    map[string]bool
	Strings  map[string]string
	Numbers  map[string]float64
	Arrays   map[string][]string
	Pos      map[string]string
	PosArray map[string][]string
	Extra    []string // tokens after "--"
	Path     []string // command names walked to reach this handler

	explicit map[string]struct{} // flags actually supplied on the command line
}

func newArgs() *Args {
	return &Args{
		Bools:    map[string]bool{},
		Strings:  map[string]string{},
		Numbers:  map[string]float64{},
		Arrays:   map[string][]string{},
		Pos:      map[string]string{},
		PosArray: map[string][]string{},
		explicit: map[string]struct{}{},
	}
}

// Bool returns a boolean flag's value (false if unset).
func (a *Args) Bool(name string) bool { return a.Bools[name] }

// String returns a string flag's value ("" if unset).
func (a *Args) String(name string) string { return a.Strings[name] }

// Has reports whether a flag was actually supplied on the command line, as
// opposed to carrying its default value. Mirrors yargs' `args.x !== undefined`
// checks used for options like --replay-limit whose presence (vs default)
// matters.
func (a *Args) Has(name string) bool {
	_, ok := a.explicit[name]
	return ok
}

// Number returns a float64 flag's value (0 if unset).
func (a *Args) Number(name string) float64 { return a.Numbers[name] }

// IntOr returns an integer flag's value, or def if it was never set.
func (a *Args) IntOr(name string, def int) int {
	if v, ok := a.Numbers[name]; ok {
		return int(v)
	}
	return def
}

// Array returns a string-array flag or array positional's values.
func (a *Args) Array(name string) []string {
	if v, ok := a.Arrays[name]; ok {
		return v
	}
	return a.PosArray[name]
}

// PositionalOr returns a positional's value, or def if it was never supplied.
func (a *Args) PositionalOr(name, def string) string {
	if v, ok := a.Pos[name]; ok {
		return v
	}
	return def
}

// UsageError causes the dispatcher to print a usage-style message and exit
// non-zero, matching the TS CLI's yargs `.fail()` handler for "Unknown
// argument" / "Not enough non-option arguments" / "Invalid values".
type UsageError struct {
	Msg string
}

func (e *UsageError) Error() string { return e.Msg }

// Run parses argv against the command tree rooted at root and invokes the
// matched handler.
func Run(root *Command, argv []string) error {
	return dispatch(root, argv, nil)
}

func dispatch(cmd *Command, argv []string, path []string) error {
	path = append(append([]string{}, path...), cmd.Name)
	args := newArgs()
	args.Path = path

	// Apply defaults first so unset flags still read correctly.
	for _, f := range cmd.Flags {
		switch f.Kind {
		case KindBool:
			if v, ok := f.Default.(bool); ok {
				args.Bools[f.Name] = v
			}
		case KindString:
			if v, ok := f.Default.(string); ok {
				args.Strings[f.Name] = v
			}
		case KindNumber:
			if v, ok := f.Default.(float64); ok {
				args.Numbers[f.Name] = v
			}
		case KindStringArray:
			if v, ok := f.Default.([]string); ok {
				args.Arrays[f.Name] = append([]string{}, v...)
			}
		}
	}

	rest := argv
	var positionalTokens []string

	i := 0
	for i < len(rest) {
		tok := rest[i]
		if tok == "--" {
			if cmd.AllowExtra {
				args.Extra = append(args.Extra, rest[i+1:]...)
			} else {
				positionalTokens = append(positionalTokens, rest[i+1:]...)
			}
			break
		}
		if strings.HasPrefix(tok, "-") && tok != "-" {
			consumed, err := parseFlag(cmd, args, rest[i:])
			if err != nil {
				return err
			}
			i += consumed
			continue
		}

		// Non-flag token: does it select a subcommand? Only the first
		// positional token is eligible, matching yargs command dispatch.
		if len(cmd.Sub) > 0 && len(positionalTokens) == 0 {
			if sub := findSub(cmd.Sub, tok); sub != nil {
				return dispatch(sub, rest[i+1:], path)
			}
		}
		positionalTokens = append(positionalTokens, tok)
		i++
	}

	if err := bindPositionals(cmd, args, positionalTokens); err != nil {
		return err
	}

	if cmd.Run == nil {
		if len(cmd.Sub) > 0 {
			return &UsageError{Msg: fmt.Sprintf("%s: a subcommand is required (%s)", strings.Join(path, " "), subNames(cmd.Sub))}
		}
		return nil
	}
	return cmd.Run(args)
}

func findSub(subs []*Command, name string) *Command {
	for _, s := range subs {
		if s.Name == name || slices.Contains(s.Aliases, name) {
			return s
		}
	}
	return nil
}

func subNames(subs []*Command) string {
	names := make([]string, 0, len(subs))
	for _, s := range subs {
		names = append(names, s.Name)
	}
	sort.Strings(names)
	return strings.Join(names, "|")
}

func findFlag(cmd *Command, name string) *Flag {
	for idx := range cmd.Flags {
		f := &cmd.Flags[idx]
		if f.Name == name || slices.Contains(f.Aliases, name) {
			return f
		}
	}
	return nil
}

func parseFlag(cmd *Command, args *Args, toks []string) (int, error) {
	tok := toks[0]
	body := strings.TrimLeft(tok, "-")
	var inlineVal string
	hasInline := false
	if eq := strings.Index(body, "="); eq >= 0 {
		inlineVal = body[eq+1:]
		body = body[:eq]
		hasInline = true
	}

	negated := false
	lookup := body
	if strings.HasPrefix(body, "no-") {
		negated = true
		lookup = strings.TrimPrefix(body, "no-")
	}

	f := findFlag(cmd, lookup)
	if f == nil {
		// Unrecognized flags are ignored rather than hard errors, so a global
		// flag (--print-logs, --pure, ...) declared only at the root doesn't
		// break parsing when it appears after a subcommand token.
		return 1, nil
	}
	args.explicit[f.Name] = struct{}{}

	switch f.Kind {
	case KindBool:
		args.Bools[f.Name] = !negated
		return 1, nil
	case KindString:
		val := inlineVal
		consumed := 1
		if !hasInline {
			if len(toks) < 2 {
				return 1, &UsageError{Msg: fmt.Sprintf("--%s requires a value", f.Name)}
			}
			val = toks[1]
			consumed = 2
		}
		if len(f.Choices) > 0 && !slices.Contains(f.Choices, val) {
			return consumed, &UsageError{Msg: fmt.Sprintf("Invalid values: Argument: %s, Given: %q, Choices: %s", f.Name, val, strings.Join(f.Choices, ", "))}
		}
		args.Strings[f.Name] = val
		return consumed, nil
	case KindNumber:
		val := inlineVal
		consumed := 1
		if !hasInline {
			if len(toks) < 2 {
				return 1, &UsageError{Msg: fmt.Sprintf("--%s requires a value", f.Name)}
			}
			val = toks[1]
			consumed = 2
		}
		n, err := strconv.ParseFloat(val, 64)
		if err != nil {
			return consumed, &UsageError{Msg: fmt.Sprintf("--%s must be a number, got %q", f.Name, val)}
		}
		args.Numbers[f.Name] = n
		return consumed, nil
	case KindStringArray:
		if hasInline {
			args.Arrays[f.Name] = append(args.Arrays[f.Name], inlineVal)
			return 1, nil
		}
		if len(toks) < 2 {
			return 1, &UsageError{Msg: fmt.Sprintf("--%s requires a value", f.Name)}
		}
		args.Arrays[f.Name] = append(args.Arrays[f.Name], toks[1])
		return 2, nil
	}
	return 1, nil
}

func bindPositionals(cmd *Command, args *Args, tokens []string) error {
	idx := 0
	for pi, p := range cmd.Positionals {
		if p.Array {
			args.PosArray[p.Name] = append([]string{}, tokens[idx:]...)
			idx = len(tokens)
			continue
		}
		if idx >= len(tokens) {
			if p.Required {
				return &UsageError{Msg: fmt.Sprintf("Not enough non-option arguments: got %d, need %d", idx, pi+1)}
			}
			continue
		}
		args.Pos[p.Name] = tokens[idx]
		idx++
	}
	return nil
}

// Fail prints an error to stderr in a form similar to the TS CLI and returns
// the process exit code to use.
func Fail(err error) int {
	fmt.Fprintln(os.Stderr, "error:", err)
	return 1
}

// HelpRequested reports whether -h/--help appears anywhere in argv, matching
// yargs' global .help("help", ...).alias("help", "h").
func HelpRequested(argv []string) bool {
	return slices.Contains(argv, "-h") || slices.Contains(argv, "--help")
}

// ResolveForHelp walks argv the same way dispatch does, without running any
// handler, and returns the deepest command reached plus the path to it. Used
// to print contextual help for "gocode <sub> ... --help".
func ResolveForHelp(root *Command, argv []string) (*Command, []string) {
	cmd := root
	path := []string{root.Name}
	rest := argv
	for {
		matched := false
		for i, tok := range rest {
			if tok == "--" {
				break
			}
			if strings.HasPrefix(tok, "-") && tok != "-" {
				continue
			}
			if sub := findSub(cmd.Sub, tok); sub != nil {
				cmd = sub
				path = append(path, sub.Name)
				rest = rest[i+1:]
				matched = true
			}
			break
		}
		if !matched {
			break
		}
	}
	return cmd, path
}

// PrintHelp writes a basic usage summary for cmd to w: its describe line,
// positionals, flags (aliases and choices included, hidden ones omitted),
// and subcommands. It is not a byte-for-byte match of yargs' formatting, but
// covers the same information.
func PrintHelp(w io.Writer, cmd *Command, path []string) {
	usage := strings.Join(path, " ")
	for _, p := range cmd.Positionals {
		if p.Required {
			usage += fmt.Sprintf(" <%s>", p.Name)
		} else if p.Array {
			usage += fmt.Sprintf(" [%s..]", p.Name)
		} else {
			usage += fmt.Sprintf(" [%s]", p.Name)
		}
	}
	if len(cmd.Sub) > 0 {
		usage += " <command>"
	}
	fmt.Fprintf(w, "usage: %s [options]\n", usage)
	if cmd.Describe != "" {
		fmt.Fprintln(w)
		fmt.Fprintln(w, cmd.Describe)
	}
	if len(cmd.Sub) > 0 {
		fmt.Fprintln(w, "\ncommands:")
		names := append([]*Command{}, cmd.Sub...)
		sort.Slice(names, func(i, j int) bool { return names[i].Name < names[j].Name })
		for _, s := range names {
			if s.Hidden {
				continue
			}
			line := s.Name
			if len(s.Aliases) > 0 {
				line += " (" + strings.Join(s.Aliases, ", ") + ")"
			}
			fmt.Fprintf(w, "  %-28s %s\n", line, s.Describe)
		}
	}
	if len(cmd.Flags) > 0 {
		fmt.Fprintln(w, "\noptions:")
		for _, f := range cmd.Flags {
			if f.Hidden {
				continue
			}
			name := "--" + f.Name
			for _, a := range f.Aliases {
				if len(a) == 1 {
					name += ", -" + a
				} else {
					name += ", --" + a
				}
			}
			if len(f.Choices) > 0 {
				name += " (" + strings.Join(f.Choices, "|") + ")"
			}
			fmt.Fprintf(w, "  %-32s %s\n", name, f.Describe)
		}
	}
}
