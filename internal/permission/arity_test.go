package permission

import "testing"

// TestArityPrefix pins the arity dictionary's contract: the save pattern for
// a bash command is the subcommand plus " *", never the literal command and
// never bare "*".
func TestArityPrefix(t *testing.T) {
	cases := []struct {
		command string
		want    string
	}{
		{"git commit -m \"x\"", "git commit *"},
		{"git status", "git status *"},
		{"git", "git *"},
		{"docker run alpine", "docker run *"},
		{"npm test", "npm test *"},
		{"npm install --no-audit", "npm install *"},
		{"cargo build --release", "cargo build *"},
		{"make build", "make build *"},
		{"ls", "ls *"},
		{"cat foo.txt", "cat *"},
		// Unknown commands save the first word, not "*".
		{"mytool --flag arg", "mytool *"},
		// Wrappers and env assignments are transparent.
		{"sudo git push", "git push *"},
		{"FOO=1 npm test", "npm test *"},
		{"nohup cargo test", "cargo test *"},
		// Nothing derivable.
		{"", ""},
		{"   ", ""},
		{"sudo", ""},
	}
	for _, c := range cases {
		if got := ArityPrefix(c.command); got != c.want {
			t.Errorf("ArityPrefix(%q) = %q, want %q", c.command, got, c.want)
		}
	}
}

// TestArityPrefixCoversVariantsButNotSiblings checks the pattern against the
// matcher itself: this is the pair of properties the whole design turns on.
func TestArityPrefixCoversVariantsButNotSiblings(t *testing.T) {
	pattern := ArityPrefix("git commit -m a")
	if pattern != "git commit *" {
		t.Fatalf("pattern = %q", pattern)
	}
	for _, covered := range []string{"git commit", "git commit -m b", "git commit --amend"} {
		if !Match(covered, pattern) {
			t.Errorf("pattern %q does not cover %q", pattern, covered)
		}
	}
	for _, sibling := range []string{"git push", "git status", "docker run"} {
		if Match(sibling, pattern) {
			t.Errorf("pattern %q unexpectedly covers sibling %q", pattern, sibling)
		}
	}
}
