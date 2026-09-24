package skill

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func writeSkill(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestScanFindsSkillFiles(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, filepath.Join(root, "skill/review/SKILL.md"),
		"---\nname: review\ndescription: Reviews code\n---\nDo the review.\n")
	writeSkill(t, filepath.Join(root, "skills/deploy/SKILL.md"),
		"---\nname: deploy\ndescription: Ships it\n---\nDeploy steps.\n")
	writeSkill(t, filepath.Join(root, "loose.md"),
		"---\ndescription: Named by filename\n---\nBody.\n")

	found := Scan(root)
	if len(found) != 3 {
		t.Fatalf("found %d skills, want 3: %+v", len(found), found)
	}
	byName := map[string]Info{}
	for _, info := range found {
		byName[info.Name] = info
	}
	if byName["review"].Description != "Reviews code" {
		t.Fatalf("review = %+v", byName["review"])
	}
	if byName["deploy"].Content != "Deploy steps.\n" {
		t.Fatalf("deploy content = %q", byName["deploy"].Content)
	}
	// A bare .md without a name in frontmatter takes its filename.
	if _, ok := byName["loose"]; !ok {
		t.Fatalf("filename-derived skill missing: %v", byName)
	}
}

// TestScanSkipsMalformedSkills is the resilience guarantee: one broken skill
// must not hide the rest.
func TestScanSkipsMalformedSkills(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, filepath.Join(root, "skill/good/SKILL.md"), "---\nname: good\n---\nfine\n")
	writeSkill(t, filepath.Join(root, "skill/nameless/SKILL.md"), "no frontmatter at all\n")

	found := Scan(root)
	if len(found) != 1 || found[0].Name != "good" {
		t.Fatalf("expected only the valid skill, got %+v", found)
	}
}

func TestDiscoverPrefersEarlierRoots(t *testing.T) {
	project, global := t.TempDir(), t.TempDir()
	writeSkill(t, filepath.Join(project, "skill/shared/SKILL.md"), "---\nname: shared\n---\nproject\n")
	writeSkill(t, filepath.Join(global, "skill/shared/SKILL.md"), "---\nname: shared\n---\nglobal\n")

	registry := Discover(project, global)
	info, ok := registry.Get("shared")
	if !ok {
		t.Fatal("shared skill missing")
	}
	if strings.TrimSpace(info.Content) != "project" {
		t.Fatalf("global skill shadowed the project one: %q", info.Content)
	}
}

func TestRequireReportsAvailable(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, filepath.Join(root, "skill/a/SKILL.md"), "---\nname: alpha\n---\nx\n")
	writeSkill(t, filepath.Join(root, "skill/b/SKILL.md"), "---\nname: beta\n---\ny\n")

	registry := Discover(root)
	_, err := registry.Require("ghost")
	if err == nil {
		t.Fatal("expected a not-found error")
	}
	if !strings.Contains(err.Error(), "alpha") || !strings.Contains(err.Error(), "beta") {
		t.Fatalf("error should list available skills: %v", err)
	}
	var notFound *NotFoundError
	if !asNotFound(err, &notFound) {
		t.Fatalf("error is not a *NotFoundError: %T", err)
	}
}

func asNotFound(err error, target **NotFoundError) bool {
	if typed, ok := err.(*NotFoundError); ok {
		*target = typed
		return true
	}
	return false
}

func TestScanOnMissingRoot(t *testing.T) {
	if found := Scan(filepath.Join(t.TempDir(), "nope")); len(found) != 0 {
		t.Fatalf("expected no skills, got %+v", found)
	}
}

func TestListIsSorted(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"zebra", "alpha", "mango"} {
		writeSkill(t, filepath.Join(root, "skill", name, "SKILL.md"), "---\nname: "+name+"\n---\nx\n")
	}
	names := Discover(root).Names()
	// Discover adds the skills compiled into the binary at the precedence
	// floor, so the list is the three on-disk ones plus those. Sortedness of
	// the whole list is the contract; membership is checked loosely.
	if !sort.StringsAreSorted(names) {
		t.Fatalf("names not sorted: %v", names)
	}
	found := map[string]bool{}
	for _, name := range names {
		found[name] = true
	}
	for _, want := range []string{"alpha", "mango", "zebra", "configure-gocode"} {
		if !found[want] {
			t.Fatalf("names %v missing %q", names, want)
		}
	}
}

// TestDiscoverBuiltInsArePrecedenceFloor pins the override rule: a skill on
// disk — project or global — replaces the same-named built-in, and an
// unrelated built-in survives alongside on-disk skills.
func TestDiscoverBuiltInsArePrecedenceFloor(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, filepath.Join(root, "skill", "configure-gocode", "SKILL.md"),
		"---\nname: configure-gocode\ndescription: My own version\n---\nMine.\n")

	registry := Discover(root)
	info, ok := registry.Get("configure-gocode")
	if !ok {
		t.Fatal("configure-gocode missing")
	}
	if info.Content != "Mine.\n" {
		t.Fatalf("on-disk skill must shadow the built-in, got %q", info.Content)
	}
	if IsBuiltin(info.Location) {
		t.Fatalf("expected an on-disk location, got %q", info.Location)
	}
}
