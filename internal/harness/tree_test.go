package harness

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const skillDoc = "---\nname: doc_review\ndescription: reviewing a long document\n---\nSteps.\n"

func TestNormalizeRelRejectsEscapes(t *testing.T) {
	for _, bad := range []string{"/etc/passwd", "../x", "a/../../b", "skills/../../x"} {
		if _, err := NormalizeRel(bad); err == nil {
			t.Errorf("NormalizeRel(%q) accepted an escape", bad)
		}
	}
	if got, err := NormalizeRel("./skills/x/SKILL.md/"); err != nil || got != "skills/x/SKILL.md" {
		t.Fatalf("NormalizeRel = %q, %v", got, err)
	}
}

func TestIsAllowed(t *testing.T) {
	allowed := []string{
		"systemprompt.md", "guardrails.md", "LongTermMEMORY.md", "tool_guidance.md", "hooks.json",
		"skills/doc_review/SKILL.md", "skills/doc_review/scripts/check.py",
		"agent/summarizer.md", "scripts/check_totals.py", "scripts/fetch.sh",
	}
	for _, rel := range allowed {
		if !IsAllowed(rel) {
			t.Errorf("IsAllowed(%q) = false", rel)
		}
	}
	denied := []string{
		"README.md", "skills/Doc/SKILL.md", "agent/x/AGENT.md", "scripts/Check.py",
		"scripts/x.rb", "plugin/x.ts", "../systemprompt.md",
	}
	for _, rel := range denied {
		if IsAllowed(rel) {
			t.Errorf("IsAllowed(%q) = true", rel)
		}
	}
}

func TestWriteReadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	tree := Tree{
		FileSystemPrompt:             "be precise",
		"skills/doc_review/SKILL.md": skillDoc,
		"scripts/check.py":           "print(1)\n",
	}
	// An off-surface file in the destination survives; a stale on-surface
	// one does not.
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "scripts"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "scripts", "stale.py"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := WriteTree(tree, dir, true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "notes.txt")); err != nil {
		t.Fatal("off-surface file was removed")
	}
	if _, err := os.Stat(filepath.Join(dir, "skills", "README.md")); err != nil {
		t.Fatal("stub was not seeded")
	}
	var rejected []string
	got, err := ReadTree(dir, &rejected)
	if err != nil {
		t.Fatal(err)
	}
	// The always-on files the tree omitted come back empty.
	want := tree.Clone()
	for _, name := range AlwaysOn {
		if _, ok := want[name]; !ok {
			want[name] = ""
		}
	}
	if d := DiffTrees(want, got); !d.Unchanged() {
		t.Fatalf("round trip differs: %+v", d)
	}
	if len(rejected) != 1 || rejected[0] != "notes.txt" {
		t.Fatalf("rejected = %v, want [notes.txt]", rejected)
	}
	if !IsTree(dir) {
		t.Fatal("IsTree = false for a written tree")
	}
}

func problems(t *testing.T, tree Tree) []string {
	t.Helper()
	_, err := Validate(tree, ValidateOptions{})
	var verr *ValidationError
	if !errors.As(err, &verr) {
		if err != nil {
			t.Fatalf("unexpected error type %T", err)
		}
		return nil
	}
	return verr.Problems
}

func TestValidateSkills(t *testing.T) {
	tree := Tree{"skills/doc_review/SKILL.md": "---\nname: other\n---\nbody"}
	got := strings.Join(problems(t, tree), "\n")
	for _, want := range []string{"needs a `description:`", "needs frontmatter `name: doc_review`", "never named in the always-on text"} {
		if !strings.Contains(got, want) {
			t.Errorf("problems lack %q:\n%s", want, got)
		}
	}
	ok := Tree{FileSystemPrompt: "load doc_review when reviewing", "skills/doc_review/SKILL.md": skillDoc}
	if p := problems(t, ok); len(p) != 0 {
		t.Fatalf("valid tree rejected: %v", p)
	}
}

func TestValidateCapAgentsAndScripts(t *testing.T) {
	tree := Tree{
		FileSystemPrompt: strings.Repeat("x", AlwaysOnMax+1),
		FileGuardrails:   "run `python3 scripts/missing.py` then `python3 scripts/read_doc.py`",
		"agent/sum.md":   "---\ndescription: d\n---\nprompt",
	}
	got := strings.Join(problems(t, tree), "\n")
	for _, want := range []string{"over the 24000 cap", "needs frontmatter `mode: subagent`", "invokes `scripts/missing.py`"} {
		if !strings.Contains(got, want) {
			t.Errorf("problems lack %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "read_doc.py") {
		t.Error("a reserved script was reported missing")
	}
	_, err := Validate(Tree{"agent/sum.md": "---\nmode: subagent\ndescription: d\n---\n"}, ValidateOptions{DisallowAgents: true})
	if err == nil || !strings.Contains(err.Error(), "not available") {
		t.Fatalf("DisallowAgents not enforced: %v", err)
	}
}

func TestAutoTrigger(t *testing.T) {
	tree := Tree{FileGuardrails: "- rule one", "skills/doc_review/SKILL.md": skillDoc}
	fixed := AutoTrigger(tree)
	if len(fixed) != 1 || fixed[0] != "doc_review" {
		t.Fatalf("fixed = %v", fixed)
	}
	if !strings.Contains(tree[FileGuardrails], "When reviewing a long document, load the `doc_review` skill.") {
		t.Fatalf("guardrails = %q", tree[FileGuardrails])
	}
	if again := AutoTrigger(tree); len(again) != 0 {
		t.Fatalf("second AutoTrigger repaired %v", again)
	}
	if p := problems(t, tree); len(p) != 0 {
		t.Fatalf("auto-triggered tree still invalid: %v", p)
	}
}

func TestDiffAndDiversity(t *testing.T) {
	before := Tree{"a.md": "1", "b.md": "2"}
	after := Tree{"a.md": "1", "b.md": "3", "c.md": "4"}
	d := DiffTrees(before, after)
	if len(d.Added) != 1 || len(d.Modified) != 1 || len(d.Removed) != 0 || d.Unchanged() {
		t.Fatalf("diff = %+v", d)
	}
	sim := Diversity(map[string]Tree{"x": before, "y": before})
	if sim.Distinct != 1 || sim.MeanLineJaccard != 1 {
		t.Fatalf("identical trees: %+v", sim)
	}
	sim = Diversity(map[string]Tree{"x": {"a": "1"}, "y": {"a": "2"}})
	if sim.Distinct != 2 || sim.MeanLineJaccard != 0 {
		t.Fatalf("disjoint trees: %+v", sim)
	}
}

func TestHooks(t *testing.T) {
	if _, err := ParseHooks(`{"rules":[{"when":"during","tool":"","action":"deny"}]}`); err == nil ||
		!strings.Contains(err.Error(), `"when" must be`) || !strings.Contains(err.Error(), `"message" is empty`) {
		t.Fatalf("bad rules accepted: %v", err)
	}
	hooks, err := ParseHooks(`{"rules":[
		{"when":"before","tool":"write","match":"\\.env\"","action":"deny","message":"no env"},
		{"when":"after","tool":"*","match":"FAILED","action":"append","message":"read it"}]}`)
	if err != nil {
		t.Fatal(err)
	}
	if err := hooks.Before("write", map[string]any{"filePath": "/x/.env"}); err == nil || !strings.Contains(err.Error(), "no env") {
		t.Fatalf("deny rule did not fire: %v", err)
	}
	if err := hooks.Before("write", map[string]any{"filePath": "/x/main.go"}); err != nil {
		t.Fatalf("deny rule over-matched: %v", err)
	}
	if out := hooks.After("bash", nil, "1 FAILED"); !strings.HasSuffix(out, "[harness] read it") {
		t.Fatalf("after = %q", out)
	}
	if out := hooks.After("bash", nil, "ok"); out != "ok" {
		t.Fatalf("after over-matched: %q", out)
	}
}

func TestOverlay(t *testing.T) {
	if NewOverlay(t.TempDir()) != nil {
		t.Fatal("overlay for a directory without a harness")
	}
	dir := t.TempDir()
	if _, err := WriteTree(Tree{FileSystemPrompt: "be precise", FileMemory: "units are cents", "scripts/check.py": "x"}, dir, true); err != nil {
		t.Fatal(err)
	}
	overlay := NewOverlay(dir)
	blocks := overlay.System()
	if len(blocks) != 1 {
		t.Fatalf("blocks = %q", blocks)
	}
	for _, want := range []string{"be precise", "units are cents", "check.py", filepath.ToSlash(dir) + "/scripts/"} {
		if !strings.Contains(blocks[0], want) {
			t.Errorf("block lacks %q:\n%s", want, blocks[0])
		}
	}
	if overlay.Hooks() != nil {
		t.Fatal("hooks without hooks.json")
	}
}

// A workspace copy, cloned or not, is independent of its source: a rollout
// writing into it must never reach the user's files.
func TestCopyDirIsIndependent(t *testing.T) {
	src := t.TempDir()
	if err := os.MkdirAll(filepath.Join(src, "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "pkg", "a.go"), []byte("package pkg\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("pkg/a.go", filepath.Join(src, "link.go")); err != nil {
		t.Fatal(err)
	}
	for _, fresh := range []bool{true, false} {
		dst := filepath.Join(t.TempDir(), "ws")
		if !fresh {
			// An existing destination takes the per-file path.
			if err := os.MkdirAll(dst, 0o755); err != nil {
				t.Fatal(err)
			}
		}
		if err := CopyDir(src, dst); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dst, "pkg", "a.go"), []byte("changed\n"), 0o640); err != nil {
			t.Fatal(err)
		}
		data, _ := os.ReadFile(filepath.Join(src, "pkg", "a.go"))
		if string(data) != "package pkg\n" {
			t.Fatalf("fresh=%v: writing the copy changed the source: %q", fresh, data)
		}
		if info, err := os.Stat(filepath.Join(dst, "pkg", "a.go")); err != nil || info.Mode().Perm() != 0o640 {
			t.Fatalf("fresh=%v: mode not kept: %v %v", fresh, info, err)
		}
		if link, err := os.Readlink(filepath.Join(dst, "link.go")); err != nil || link != "pkg/a.go" {
			t.Fatalf("fresh=%v: symlink not kept: %q %v", fresh, link, err)
		}
	}
}

func TestSyntaxCheck(t *testing.T) {
	if _, err := exec.LookPath("python3"); err == nil {
		if err := SyntaxCheck("scripts/ok.py", "import sys\nprint(sys.argv)\n"); err != nil {
			t.Errorf("valid python rejected: %v", err)
		}
		err := SyntaxCheck("scripts/bad.py", "def f(:\n  pass\n")
		if err == nil || !strings.Contains(err.Error(), "scripts/bad.py does not parse") {
			t.Errorf("broken python accepted: %v", err)
		}
		// Parsing never runs the code.
		marker := filepath.Join(t.TempDir(), "ran")
		_ = SyntaxCheck("scripts/side.py", "open("+strconv.Quote(marker)+", 'w').write('x')\n")
		if _, err := os.Stat(marker); err == nil {
			t.Fatal("SyntaxCheck executed the script")
		}
	}
	if _, err := exec.LookPath("sh"); err == nil {
		if err := SyntaxCheck("scripts/ok.sh", "echo hi\n"); err != nil {
			t.Errorf("valid sh rejected: %v", err)
		}
		if err := SyntaxCheck("scripts/bad.sh", "if then fi (\n"); err == nil {
			t.Error("broken sh accepted")
		}
	}
	if err := SyntaxCheck("skills/x/SKILL.md", "anything"); err != nil {
		t.Errorf("non-script checked: %v", err)
	}
	tree := Tree{FileGuardrails: "run `python3 scripts/a.py`", "scripts/a.py": "x"}
	_, err := Validate(tree, ValidateOptions{CheckScript: func(rel, _ string) error { return fmt.Errorf("%s does not parse", rel) }})
	if err == nil || !strings.Contains(err.Error(), "scripts/a.py does not parse") {
		t.Fatalf("Validate ignored CheckScript: %v", err)
	}
}

func TestWorkspaceDiff(t *testing.T) {
	src, ws := t.TempDir(), t.TempDir()
	write := func(root, rel, content string) {
		t.Helper()
		path := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	big := strings.Repeat("x", 200_000) // several comparison chunks
	write(src, "same.txt", big)
	write(ws, "same.txt", big)
	write(src, "edited.go", "package a\n")
	write(ws, "edited.go", "package b\n") // equal size, different bytes
	write(src, "removed.md", "gone\n")
	write(ws, "added.md", "new\n")
	write(src, "img.bin", "\x00\x01")
	write(ws, "img.bin", "\x00\x02")
	write(ws, ".git/HEAD", "ref: x\n") // machinery, never a change

	out, changed, err := WorkspaceDiff(src, ws)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(changed, ",") != "added.md,edited.go,img.bin,removed.md" {
		t.Fatalf("changed = %v", changed)
	}
	for _, want := range []string{"+package b", "-package a", "--- /dev/null", "+++ /dev/null", "Binary file img.bin changed"} {
		if !strings.Contains(out, want) {
			t.Errorf("diff lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "same.txt") {
		t.Error("an unchanged file was diffed")
	}
}
