package command

import (
	"context"
	"strings"
	"testing"
)

func TestExpandPositionalPlaceholders(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name      string
		template  string
		arguments string
		want      string
	}{
		{"single placeholder takes everything", "commit $1", "fix the parser bug", "commit fix the parser bug"},
		{"two placeholders, last is greedy", "review $1 with $2", "main a b c", "review main with a b c"},
		{"missing argument becomes empty", "run $1 $2", "only", "run only"},
		{"no arguments at all", "run $1", "", "run"},
		{"quoted argument stays one token", `say $1 to $2`, `"hello world" bob`, "say hello world to bob"},
		{"single quotes too", `say $1 to $2`, `'hello world' bob`, "say hello world to bob"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Expand(ctx, c.template, c.arguments, "/bin/sh"); got != c.want {
				t.Errorf("Expand(%q, %q) = %q, want %q", c.template, c.arguments, got, c.want)
			}
		})
	}
}

// TestExpandLastPlaceholderIsGreedy is the rule most easily got wrong: the
// highest-numbered placeholder takes every remaining argument, so a template
// with one placeholder never silently drops the rest of what was typed.
func TestExpandLastPlaceholderIsGreedy(t *testing.T) {
	got := Expand(context.Background(), "$1", "one two three", "/bin/sh")
	if got != "one two three" {
		t.Errorf("Expand = %q, want all three arguments", got)
	}

	// With $1 and $2, only $2 is greedy.
	got = Expand(context.Background(), "[$1][$2]", "a b c d", "/bin/sh")
	if got != "[a][b c d]" {
		t.Errorf("Expand = %q, want [a][b c d]", got)
	}
}

func TestExpandArgumentsPlaceholder(t *testing.T) {
	got := Expand(context.Background(), "review this:\n$ARGUMENTS", "the whole string", "/bin/sh")
	if !strings.Contains(got, "the whole string") {
		t.Errorf("Expand = %q, want $ARGUMENTS substituted", got)
	}
	// $ARGUMENTS is the raw string, not the tokenized form.
	got = Expand(context.Background(), "$ARGUMENTS", `"quoted stays quoted"`, "/bin/sh")
	if got != `"quoted stays quoted"` {
		t.Errorf("Expand = %q, want the raw argument string", got)
	}
}

// TestExpandAppendsBareArguments: a template with no placeholders would
// otherwise ignore what the user typed after the command name.
func TestExpandAppendsBareArguments(t *testing.T) {
	got := Expand(context.Background(), "do the thing", "with this context", "/bin/sh")
	if got != "do the thing\n\nwith this context" {
		t.Errorf("Expand = %q, want the arguments appended after a blank line", got)
	}

	// Nothing to append.
	if got := Expand(context.Background(), "do the thing", "", "/bin/sh"); got != "do the thing" {
		t.Errorf("Expand = %q, want the template unchanged", got)
	}
	// A template that uses $ARGUMENTS must not also get them appended. The
	// marker is deliberately not a substring of the template.
	got = Expand(context.Background(), "context: $ARGUMENTS", "ZZmarker", "/bin/sh")
	if count := strings.Count(got, "ZZmarker"); count != 1 {
		t.Errorf("Expand = %q, arguments appear %d times, want 1", got, count)
	}
	// Likewise for a positional placeholder.
	got = Expand(context.Background(), "run $1", "ZZmarker", "/bin/sh")
	if count := strings.Count(got, "ZZmarker"); count != 1 {
		t.Errorf("Expand = %q, arguments appear %d times, want 1", got, count)
	}
}

// TestExpandShellSubstitution covers !`cmd`, which is how a template pulls in
// live state such as the current branch.
func TestExpandShellSubstitution(t *testing.T) {
	got := Expand(context.Background(), "branch is !`echo main`", "", "/bin/sh")
	if got != "branch is main" {
		t.Errorf("Expand = %q, want the command output substituted", got)
	}

	// Several substitutions in one template.
	got = Expand(context.Background(), "!`echo a` and !`echo b`", "", "/bin/sh")
	if got != "a and b" {
		t.Errorf("Expand = %q, want both substituted", got)
	}
}

// TestExpandShellFailureIsEmpty: upstream runs these with nothrow, so a
// template referencing a missing tool still produces a usable prompt.
func TestExpandShellFailureIsEmpty(t *testing.T) {
	got := Expand(context.Background(), "x!`opencode-no-such-command-xyz`y", "", "/bin/sh")
	if got != "xy" {
		t.Errorf("Expand = %q, want the failed substitution to be empty", got)
	}
}

// TestExpandImagePlaceholderIsOneToken: the argument tokenizer keeps a pasted
// "[Image 1]" together rather than splitting it in two.
func TestExpandImagePlaceholderIsOneToken(t *testing.T) {
	got := Expand(context.Background(), "look at $1 please", "[Image 1]", "/bin/sh")
	if got != "look at [Image 1] please" {
		t.Errorf("Expand = %q, want the image placeholder kept whole", got)
	}
}

func TestHints(t *testing.T) {
	cases := []struct {
		template string
		want     []string
	}{
		{"no placeholders", nil},
		{"$1 and $2", []string{"$1", "$2"}},
		{"$2 before $1", []string{"$1", "$2"}},
		{"$1 twice $1", []string{"$1"}},
		{"$ARGUMENTS only", []string{"$ARGUMENTS"}},
		{"$1 and $ARGUMENTS", []string{"$1", "$ARGUMENTS"}},
		{"$10 sorts numerically", []string{"$10"}},
	}
	for _, c := range cases {
		got := Hints(c.template)
		if len(got) != len(c.want) {
			t.Errorf("Hints(%q) = %v, want %v", c.template, got, c.want)
			continue
		}
		for i := range c.want {
			if got[i] != c.want[i] {
				t.Errorf("Hints(%q) = %v, want %v", c.template, got, c.want)
				break
			}
		}
	}
}

func TestHintsSortsNumerically(t *testing.T) {
	// Sorted as numbers, not strings: $2 comes before $10.
	got := Hints("$10 $2 $1")
	want := []string{"$1", "$2", "$10"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Hints = %v, want %v", got, want)
		}
	}
}
