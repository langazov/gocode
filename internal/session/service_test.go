package session

import (
	"context"
	"strings"
	"testing"
)

func TestSessionTitle(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"Fix the login bug", "Fix the login bug"},
		{"  lots   of   spaces  ", "lots of spaces"},
		{strings.Repeat("word ", 30), strings.TrimSpace(strings.Repeat("word ", 12)) + "…"},
		{"", ""},
	}
	for _, c := range cases {
		if got := sessionTitle(c.input); got != c.want {
			t.Errorf("sessionTitle(%q) = %q, want %q", c.input, got, c.want)
		}
	}
}

func TestPromptSetsTitleFromFirstPrompt(t *testing.T) {
	bus, database := setup(t)
	service := NewService(database, bus)
	ctx := context.Background()

	info, err := service.Create(ctx, CreateInput{Directory: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Prompt(ctx, info.ID, "Refactor the auth module", DeliverySteer); err != nil {
		t.Fatal(err)
	}
	got, err := service.Get(ctx, info.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "Refactor the auth module" {
		t.Fatalf("expected title from first prompt, got %q", got.Title)
	}

	if _, err := service.Prompt(ctx, info.ID, "A second, later prompt", DeliverySteer); err != nil {
		t.Fatal(err)
	}
	got, err = service.Get(ctx, info.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "Refactor the auth module" {
		t.Fatalf("title must not change after the first prompt, got %q", got.Title)
	}
}

func TestServiceLifecycle(t *testing.T) {
	bus, database := setup(t)
	service := NewService(database, bus)
	ctx := context.Background()

	info, err := service.Create(ctx, CreateInput{Directory: t.TempDir(), Title: "Explicit"})
	if err != nil {
		t.Fatal(err)
	}
	if info.Title != "Explicit" {
		t.Fatalf("expected explicit title, got %q", info.Title)
	}

	list, err := service.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, item := range list {
		if item.ID == info.ID {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected created session in list, got %+v", list)
	}

	if _, err := service.Prompt(ctx, "ses_missing", "hi", DeliverySteer); err == nil {
		t.Fatal("expected error prompting a missing session")
	}
	service.Interrupt(info.ID)
}

// TestParentIDSurvivesRefetch is the regression for the bug that disabled the
// TUI's whole subagent surface: Get/List/Children never selected parent_id or
// agent, so Info.ParentID was set only by Create's return value and went empty
// on every refetch. The subagent footer, up/left/right navigation and the
// children overlay all key off ParentID and could never activate.
func TestParentIDSurvivesRefetch(t *testing.T) {
	bus, database := setup(t)
	service := NewService(database, bus)
	ctx := context.Background()

	parent, err := service.Create(ctx, CreateInput{Directory: t.TempDir(), Title: "Parent"})
	if err != nil {
		t.Fatal(err)
	}
	child, err := service.Create(ctx, CreateInput{
		Directory: t.TempDir(),
		Title:     "Find the bug (@general-purpose subagent)",
		ParentID:  parent.ID,
		Agent:     "general-purpose",
	})
	if err != nil {
		t.Fatal(err)
	}

	got, err := service.Get(ctx, child.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ParentID != parent.ID {
		t.Fatalf("Get: child ParentID = %q, want %q", got.ParentID, parent.ID)
	}
	if got.Agent != "general-purpose" {
		t.Fatalf("Get: child Agent = %q, want %q", got.Agent, "general-purpose")
	}

	list, err := service.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var listed *Info
	for i := range list {
		if list[i].ID == child.ID {
			listed = &list[i]
		}
	}
	if listed == nil {
		t.Fatal("List: child session missing")
	}
	if listed.ParentID != parent.ID {
		t.Fatalf("List: child ParentID = %q, want %q", listed.ParentID, parent.ID)
	}
	if listed.Agent != "general-purpose" {
		t.Fatalf("List: child Agent = %q, want %q", listed.Agent, "general-purpose")
	}

	children, err := service.Children(ctx, parent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(children) != 1 || children[0].ID != child.ID {
		t.Fatalf("Children = %+v, want exactly [%s]", children, child.ID)
	}
	if children[0].ParentID != parent.ID {
		t.Fatalf("Children: ParentID = %q, want %q", children[0].ParentID, parent.ID)
	}
	if children[0].Agent != "general-purpose" {
		t.Fatalf("Children: Agent = %q, want %q", children[0].Agent, "general-purpose")
	}

	// A root session reports no parent, not an empty-looking one.
	root, err := service.Get(ctx, parent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if root.ParentID != "" || root.Agent != "" {
		t.Fatalf("root session ParentID/Agent = %q/%q, want empty", root.ParentID, root.Agent)
	}
}
