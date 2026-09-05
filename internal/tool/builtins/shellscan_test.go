package builtins

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/langazov/gocode-go/internal/permission"
	"github.com/langazov/gocode-go/internal/tool"
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

	// A redirect outside the tree is both things at once: it leaves the
	// working directory *and* it writes.
	extras := scoped.ExtraPermissions(map[string]any{
		"command": "cat > /tmp/test/channels.go << 'EOF'\npackage main\nEOF",
	})
	external := extraFor(extras, permission.ExternalDirectoryAction)
	if external == nil {
		t.Fatalf("no external_directory permission declared: %+v", extras)
	}
	// A glob over the directory, so approving once covers it rather than one
	// file. Resources are always forward-slash, regardless of host — see
	// ExtraPermissions's ToSlash.
	want := filepath.ToSlash(filepath.Join(canonicalPath("/tmp/test"), "*"))
	if len(external.Resources) != 1 || external.Resources[0] != want {
		t.Errorf("resources = %v, want a glob over %s", external.Resources, want)
	}
	if edit := extraFor(extras, "edit"); edit == nil {
		t.Errorf("a redirect must also be declared as an edit: %+v", extras)
	}
}

func extraFor(extras []tool.ExtraPermission, action string) *tool.ExtraPermission {
	for i := range extras {
		if extras[i].Action == action {
			return &extras[i]
		}
	}
	return nil
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

// ScanWrites is what makes an `edit` rule a rule about editing rather than a
// rule about three tools. The read cases matter as much as the write ones:
// plan mode denies edit, so a false positive here stops it reading.
func TestScanWritesSeparatesReadsFromWrites(t *testing.T) {
	root := t.TempDir()
	for _, tc := range []struct {
		command string
		want    []string
	}{
		// Redirects.
		{"echo hi > notes.md", []string{"notes.md"}},
		{"echo hi >> notes.md", []string{"notes.md"}},
		{"go test ./... &> out.log", []string{"out.log"}},
		{"cat < notes.md", nil},
		{"cat <<'EOF'\nhi\nEOF", nil},
		// Mutating commands.
		{"rm -rf build", []string{"build"}},
		{"mv a.txt b.txt", []string{"a.txt", "b.txt"}},
		{"mkdir -p a/b", []string{"a/b"}},
		{"touch NEW", []string{"NEW"}},
		// Commands that name a path without changing it.
		{"cat notes.md", nil},
		{"cd src", nil},
		{"ls -la", nil},
		{"grep -r foo .", nil},
		{"go build ./...", nil},
		{"git status", nil},
		// In place only when asked. Once it is, every non-flag argument is
		// reported, script included: sed's grammar is not worth modelling,
		// and the error that matters is the other one. An extra resource
		// costs an approval that would have been asked for anyway; a missed
		// one is a file edited with nobody asked.
		{"sed -n '1,5p' notes.md", nil},
		{"sed -i '' -e s/a/b/ notes.md", []string{"notes.md", "s/a/b/"}},
		// Buried in a pipeline, a subshell, and behind &&.
		{"cat x | tee out.txt", []string{"out.txt"}},
		{"(echo hi > inner.txt)", []string{"inner.txt"}},
		{"go build ./... && echo ok > done.txt", []string{"done.txt"}},
		// Not statically known: reported as nothing rather than guessed at.
		{"echo hi > $TARGET", nil},
		{"echo hi > $(mktemp)", nil},
		{"echo hi > 'unterminated", nil},
	} {
		got := ScanWrites(tc.command, root)
		if len(got) != len(tc.want) {
			t.Errorf("ScanWrites(%q) = %v, want %v", tc.command, got, tc.want)
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("ScanWrites(%q) = %v, want %v", tc.command, got, tc.want)
				break
			}
		}
	}
}

// A command that only reads must declare nothing, or an agent with edit denied
// cannot look at the repository it is supposed to be planning against.
func TestBashToolDeclaresNoEditForReadOnlyCommands(t *testing.T) {
	bash := NewBashTool(Resolver{Root: t.TempDir()})
	scoped := any(bash).(tool.PermissionScoped)
	for _, command := range []string{"ls -la", "cat README.md", "git log --oneline -20", "rg TODO"} {
		extras := scoped.ExtraPermissions(map[string]any{"command": command})
		if edit := extraFor(extras, "edit"); edit != nil {
			t.Errorf("%q should need no edit approval, got %+v", command, edit)
		}
	}
}

// The paths are reported as written, so a rule a user wrote against a
// repo-relative path matches the same way it does for the write tool.
func TestBashToolDeclaresEditForInTreeWrite(t *testing.T) {
	bash := NewBashTool(Resolver{Root: t.TempDir()})
	scoped := any(bash).(tool.PermissionScoped)
	extras := scoped.ExtraPermissions(map[string]any{"command": "echo hi > src/main.go"})
	edit := extraFor(extras, "edit")
	if edit == nil {
		t.Fatalf("an in-tree write must be declared as an edit: %+v", extras)
	}
	if len(edit.Resources) != 1 || edit.Resources[0] != "src/main.go" {
		t.Errorf("resources = %v, want [src/main.go]", edit.Resources)
	}
	// "may you edit files", not "may you edit this one" — matching what
	// write/edit/apply_patch persist.
	if len(edit.Save) != 1 || edit.Save[0] != "*" {
		t.Errorf("save = %v, want [*]", edit.Save)
	}
	// An in-tree path is not an external directory.
	if external := extraFor(extras, permission.ExternalDirectoryAction); external != nil {
		t.Errorf("an in-tree write is not external, got %+v", external)
	}
}
