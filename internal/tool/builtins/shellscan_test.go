package builtins

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/anomalyco/opencode-go/internal/permission"
	"github.com/anomalyco/opencode-go/internal/tool"
)

func TestScanExternalPaths(t *testing.T) {
	root := t.TempDir()

	cases := []struct {
		name    string
		command string
		want    []string
	}{
		{
			// The reported case: the write tool refuses a path outside the
			// working directory, so the model reaches for the shell instead.
			name:    "heredoc redirect outside the root",
			command: "cat > /tmp/test/channels.go << 'EOF'\npackage main\nEOF",
			want:    []string{canonicalPath("/tmp/test")},
		},
		{name: "plain redirect", command: "echo hi > /tmp/x.txt", want: []string{canonicalPath("/tmp")}},
		{name: "append redirect", command: "echo hi >> /etc/hosts", want: []string{canonicalPath("/etc")}},
		{name: "tee", command: "echo hi | tee /tmp/out.txt", want: []string{canonicalPath("/tmp")}},
		{name: "rm outside", command: "rm -rf /tmp/whatever", want: []string{canonicalPath("/tmp")}},
		{name: "cp destination", command: "cp a.txt /tmp/b.txt", want: []string{canonicalPath("/tmp")}},
		{name: "mv destination", command: "mv a.txt /var/tmp/b.txt", want: []string{canonicalPath("/var/tmp")}},
		{name: "mkdir", command: "mkdir -p /tmp/newdir", want: []string{canonicalPath("/tmp")}},

		// Quoting must not hide the path: this is the whole reason a real
		// parser is used rather than a pattern match.
		{name: "single quoted", command: "cat > '/tmp/quoted.go'", want: []string{canonicalPath("/tmp")}},
		{name: "double quoted", command: `cat > "/tmp/quoted.go"`, want: []string{canonicalPath("/tmp")}},

		// Nor must nesting: a command inside a subshell, a pipeline, or after
		// a conditional is still a command.
		{name: "after &&", command: "true && rm /tmp/x", want: []string{canonicalPath("/tmp")}},
		{name: "in a subshell", command: "(cd /tmp && touch y)", want: []string{canonicalPath("/tmp")}},
		{name: "second in a list", command: "echo one; echo two > /tmp/z", want: []string{canonicalPath("/tmp")}},

		// Paths inside the working directory are the tool's own business.
		{name: "relative path", command: "echo hi > out.txt", want: nil},
		{name: "nested relative", command: "mkdir -p a/b/c", want: nil},
		{name: "no paths at all", command: "go test ./...", want: nil},
		{name: "flags are not paths", command: "rm -rf --preserve-root", want: nil},

		// Escaping upward is external even when spelled relatively.
		{name: "parent traversal", command: "cat > ../outside.txt", want: []string{filepath.Dir(canonicalRoot(root))}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ScanExternalPaths(c.command, root)
			if len(got) != len(c.want) {
				t.Fatalf("ScanExternalPaths(%q) = %v, want %v", c.command, got, c.want)
			}
			for i := range c.want {
				if got[i] != c.want[i] {
					t.Errorf("ScanExternalPaths(%q) = %v, want %v", c.command, got, c.want)
				}
			}
		})
	}
}

// TestScanTreatsRootItselfAsInternal: a command naming the working directory
// by absolute path is not reaching outside it.
func TestScanTreatsRootItselfAsInternal(t *testing.T) {
	root := t.TempDir()
	command := "cat > " + filepath.Join(root, "inside.go")
	if got := ScanExternalPaths(command, root); len(got) != 0 {
		t.Errorf("writing inside the root by absolute path is not external, got %v", got)
	}
}

// TestScanIgnoresUnresolvablePaths documents the limit honestly: a path built
// at runtime cannot be known at parse time, so it is not reported. The
// permission system is the boundary; this scan is a guard for the common case.
func TestScanIgnoresUnresolvablePaths(t *testing.T) {
	root := t.TempDir()
	for _, command := range []string{
		`cat > "$TARGET/file.go"`,
		"cat > $(mktemp -d)/file.go",
	} {
		if got := ScanExternalPaths(command, root); len(got) != 0 {
			t.Errorf("ScanExternalPaths(%q) = %v; a runtime path is not knowable statically", command, got)
		}
	}
}

func TestScanHandlesUnparseableCommand(t *testing.T) {
	// A syntax error must not panic or block; the shell will reject it.
	if got := ScanExternalPaths("cat > 'unterminated", t.TempDir()); got != nil {
		t.Errorf("got %v, want nil for an unparseable command", got)
	}
}

// TestBashToolDeclaresExternalDirectory wires the scan to the permission the
// runner asks for.
func TestBashToolDeclaresExternalDirectory(t *testing.T) {
	root := t.TempDir()
	bash := NewBashTool(Resolver{Root: root})

	scoped, ok := any(bash).(tool.PermissionScoped)
	if !ok {
		t.Fatal("the bash tool must declare extra permissions, or the shell bypasses the directory restriction")
	}

	extras := scoped.ExtraPermissions(map[string]any{
		"command": "cat > /tmp/test/channels.go << 'EOF'\npackage main\nEOF",
	})
	if len(extras) != 1 {
		t.Fatalf("got %d extra permissions, want 1: %+v", len(extras), extras)
	}
	if extras[0].Action != permission.ExternalDirectoryAction {
		t.Errorf("action = %q, want %q", extras[0].Action, permission.ExternalDirectoryAction)
	}
	// A glob over the directory, so approving once covers it rather than one file.
	if len(extras[0].Resources) != 1 || extras[0].Resources[0] != filepath.Join(canonicalPath("/tmp/test"), "*") {
		t.Errorf("resources = %v, want a glob over %s", extras[0].Resources, canonicalPath("/tmp/test"))
	}
}

func TestBashToolNoExtrasForInternalCommand(t *testing.T) {
	bash := NewBashTool(Resolver{Root: t.TempDir()})
	scoped := any(bash).(tool.PermissionScoped)
	if extras := scoped.ExtraPermissions(map[string]any{"command": "go build ./..."}); len(extras) != 0 {
		t.Errorf("an ordinary command must need no extra approval, got %+v", extras)
	}
}

// TestExternalDirectoryDefaultsToAsk is the regression for the reported
// behavior: it ran without asking. The allow-all baseline must not cover this
// action, matching agent.ts's `external_directory: {"*": "ask"}`.
func TestExternalDirectoryDefaultsToAsk(t *testing.T) {
	defaults := permission.Defaults()

	got := permission.Evaluate(permission.ExternalDirectoryAction, "/tmp/*", defaults).Effect
	if got != permission.Ask {
		t.Fatalf("external_directory = %v, want ask — the shell would reach outside the working directory unprompted", got)
	}
	// The baseline must still allow everything else, or the port regresses to
	// asking constantly (see the 2026-08-30 entry in the gap notes).
	for _, action := range []string{"bash", "edit", "todowrite", "some-mcp_tool"} {
		if permission.Evaluate(action, "*", defaults).Effect != permission.Allow {
			t.Errorf("%s should still be allowed by default", action)
		}
	}
}

// TestWriteToolStillRefusesExternalPaths: the shell gate is in addition to the
// file tools' own restriction, not a replacement for it.
func TestWriteToolStillRefusesExternalPaths(t *testing.T) {
	resolver := Resolver{Root: t.TempDir()}
	if _, err := resolver.Resolve("/tmp/elsewhere.go"); err == nil {
		t.Error("the file tools must keep refusing paths outside the working directory")
	} else if !strings.Contains(err.Error(), "escapes working directory") {
		t.Errorf("unexpected error: %v", err)
	}
}
