package builtins

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anomalyco/opencode-go/internal/skill"
)

func newSkillFixture(t *testing.T) (*SkillTool, *skill.Registry, string) {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "skill", "review")
	if err := os.MkdirAll(filepath.Join(dir, "scripts"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"),
		[]byte("---\nname: review\ndescription: Reviews code\n---\nRun the checklist.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "scripts", "check.sh"), []byte("#!/bin/sh\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	registry := skill.Discover(root)
	return NewSkillTool(registry), registry, dir
}

func TestSkillToolLoadsContent(t *testing.T) {
	tool, _, dir := newSkillFixture(t)
	out, err := tool.Execute(context.Background(), map[string]any{"name": "review"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`<skill_content name="review">`,
		"# Skill: review",
		"Run the checklist.",
		"Base directory for this skill: " + dir,
		"<skill_files>",
		filepath.Join(dir, "scripts", "check.sh"),
		"</skill_content>",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "SKILL.md") {
		t.Fatal("the skill file itself should not be listed among its resources")
	}
}

func TestSkillToolUnknownSkill(t *testing.T) {
	tool, _, _ := newSkillFixture(t)
	_, err := tool.Execute(context.Background(), map[string]any{"name": "ghost"})
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected a not-found error, got %v", err)
	}
	if !strings.Contains(err.Error(), "review") {
		t.Fatalf("error should list available skills: %v", err)
	}
}

func TestSkillToolRequiresName(t *testing.T) {
	tool, _, _ := newSkillFixture(t)
	if _, err := tool.Execute(context.Background(), map[string]any{}); err == nil {
		t.Fatal("expected name to be required")
	}
}

func TestSkillPromptListsSkills(t *testing.T) {
	_, registry, _ := newSkillFixture(t)
	prompt := SkillPrompt(registry)
	if !strings.Contains(prompt, "<available_skills>") || !strings.Contains(prompt, "- review: Reviews code") {
		t.Fatalf("unexpected prompt block:\n%s", prompt)
	}
	if SkillPrompt(skill.NewRegistry()) != "" {
		t.Fatal("an empty registry should render no prompt block")
	}
	if SkillPrompt(nil) != "" {
		t.Fatal("a nil registry should render no prompt block")
	}
}
