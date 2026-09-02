package command

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anomalyco/opencode-go/internal/config"
	"github.com/anomalyco/opencode-go/internal/skill"
)

func configFrom(t *testing.T, document string) *config.Config {
	t.Helper()
	cfg := &config.Config{}
	if err := json.Unmarshal([]byte(document), cfg); err != nil {
		t.Fatal(err)
	}
	return cfg
}

// TestBuiltinCommands: init and review ship with the binary, and review runs
// as a subtask.
func TestBuiltinCommands(t *testing.T) {
	registry := Load(nil, "/work", nil, nil)

	init, ok := registry.Get(NameInit)
	if !ok {
		t.Fatal("init is missing")
	}
	if !strings.Contains(init.Template, "AGENTS.md") {
		t.Errorf("init template does not look like the AGENTS.md setup prompt")
	}
	// ${path} is substituted with the working directory.
	if strings.Contains(init.Template, "${path}") {
		t.Error("init template still holds the ${path} marker")
	}
	if !strings.Contains(init.Template, "/work") {
		t.Error("init template should name the working directory")
	}

	review, ok := registry.Get(NameReview)
	if !ok {
		t.Fatal("review is missing")
	}
	if !review.Subtask {
		t.Error("review runs as a subtask upstream")
	}
}

func TestConfigCommands(t *testing.T) {
	cfg := configFrom(t, `{"command": {
		"deploy": {"template": "deploy $1", "description": "ship it", "agent": "build", "subtask": true},
		"bad": {"description": "no template"}
	}}`)
	registry := Load(cfg, "/work", nil, nil)

	deploy, ok := registry.Get("deploy")
	if !ok {
		t.Fatal("the configured command is missing")
	}
	if deploy.Template != "deploy $1" || deploy.Description != "ship it" {
		t.Errorf("command = %+v", deploy)
	}
	if deploy.Agent != "build" || !deploy.Subtask {
		t.Errorf("agent/subtask lost: %+v", deploy)
	}
	if len(deploy.Hints) != 1 || deploy.Hints[0] != "$1" {
		t.Errorf("hints = %v, want [$1]", deploy.Hints)
	}
	if _, ok := registry.Get("bad"); ok {
		t.Error("a command with no template must be skipped")
	}
}

// TestMarkdownCommands: `.opencode/command/**/*.md` defines commands, and a
// nested path namespaces the name rather than colliding.
func TestMarkdownCommands(t *testing.T) {
	dir := t.TempDir()
	write := func(rel, content string) {
		path := filepath.Join(dir, rel)
		os.MkdirAll(filepath.Dir(path), 0o755)
		os.WriteFile(path, []byte(content), 0o644)
	}
	write("command/ship.md", "---\ndescription: ship it\nagent: build\n---\nDeploy $1 now")
	write("command/git/commit.md", "---\ndescription: commit\n---\nCommit the changes")
	write("commands/legacy.md", "Legacy directory still works")

	registry := Load(nil, "/work", nil, []string{dir})

	ship, ok := registry.Get("ship")
	if !ok {
		t.Fatal("ship is missing")
	}
	if ship.Template != "Deploy $1 now" || ship.Description != "ship it" || ship.Agent != "build" {
		t.Errorf("ship = %+v", ship)
	}
	if _, ok := registry.Get("git/commit"); !ok {
		t.Error("a nested command should be named by its path")
	}
	if _, ok := registry.Get("legacy"); !ok {
		t.Error("the commands/ directory should be read too")
	}
}

// TestMarkdownOverridesConfig: a file on disk is the more specific definition.
func TestMarkdownOverridesConfig(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "command"), 0o755)
	os.WriteFile(filepath.Join(dir, "command", "ship.md"), []byte("from markdown"), 0o644)

	cfg := configFrom(t, `{"command": {"ship": {"template": "from config"}}}`)
	registry := Load(cfg, "/work", nil, []string{dir})

	ship, _ := registry.Get("ship")
	if ship.Template != "from markdown" {
		t.Errorf("template = %q, want the markdown definition to win", ship.Template)
	}
}

// TestSkillsBecomeCommands, and yield to a command of the same name.
func TestSkillsBecomeCommands(t *testing.T) {
	skills := skill.NewRegistry()
	skills.Add(skill.Info{
		Name:        "brainstorm",
		Description: "think out loud",
		Location:    "/skills/brainstorm/SKILL.md",
		Content:     "Brainstorm about the topic.",
	})
	skills.Add(skill.Info{Name: "review", Description: "shadowed", Content: "should not win"})

	registry := Load(nil, "/work", skills, nil)

	brainstorm, ok := registry.Get("brainstorm")
	if !ok {
		t.Fatal("the skill is missing as a command")
	}
	if brainstorm.Source != SourceSkill {
		t.Errorf("source = %q, want %q", brainstorm.Source, SourceSkill)
	}
	if !strings.Contains(brainstorm.Template, "Brainstorm about the topic.") {
		t.Errorf("template = %q", brainstorm.Template)
	}
	// The base-directory note lets relative paths in the skill resolve.
	if !strings.Contains(brainstorm.Template, "/skills/brainstorm") {
		t.Errorf("template should name the skill's base directory: %q", brainstorm.Template)
	}

	// review is a built-in command; the skill must not displace it.
	review, _ := registry.Get(NameReview)
	if review.Source == SourceSkill {
		t.Error("a skill must not shadow a command of the same name")
	}
}

func TestListIsSorted(t *testing.T) {
	cfg := configFrom(t, `{"command": {"zebra": {"template": "z"}, "alpha": {"template": "a"}}}`)
	list := Load(cfg, "/work", nil, nil).List()
	for i := 1; i < len(list); i++ {
		if list[i-1].Name > list[i].Name {
			t.Fatalf("list is not sorted: %s before %s", list[i-1].Name, list[i].Name)
		}
	}
}

func TestNilRegistryIsSafe(t *testing.T) {
	var registry *Registry
	if _, ok := registry.Get("x"); ok {
		t.Error("a nil registry should find nothing")
	}
	if got := registry.List(); got != nil {
		t.Errorf("List = %v, want nil", got)
	}
}
