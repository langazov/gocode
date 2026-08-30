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
