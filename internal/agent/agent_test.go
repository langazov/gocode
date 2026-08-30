package agent

import "testing"

func TestSelectExplicit(t *testing.T) {
	registry := NewRegistry()
	registry.Update(Info{ID: "plan", Mode: "primary"})
	selection := registry.Select("plan")
	if selection.ID != "plan" || selection.Info == nil {
		t.Fatalf("expected explicit selection, got %+v", selection)
	}
	missing := registry.Select("nope")
	if missing.ID != "nope" || missing.Info != nil {
		t.Fatalf("expected missing-info selection, got %+v", missing)
	}
}

func TestSelectDefaultsToBuild(t *testing.T) {
	registry := NewRegistry()
	registry.Update(Info{ID: "build", Mode: "primary"})
	registry.Update(Info{ID: "plan", Mode: "primary"})
	selection := registry.Select("")
	if selection.ID != "build" {
		t.Fatalf("expected build default, got %s", selection.ID)
	}
}

func TestSelectConfiguredDefault(t *testing.T) {
	registry := NewRegistry()
	registry.Update(Info{ID: "build", Mode: "primary"})
	registry.Update(Info{ID: "custom", Mode: "primary"})
	registry.SetDefault("custom")
	selection := registry.Select("")
	if selection.ID != "custom" {
		t.Fatalf("expected configured default, got %s", selection.ID)
	}
}

func TestSelectSkipsSubagentAndHidden(t *testing.T) {
	registry := NewRegistry()
	registry.Update(Info{ID: "sub", Mode: "subagent"})
	registry.Update(Info{ID: "hidden", Mode: "primary", Hidden: true})
	registry.SetDefault("sub")
	selection := registry.Select("")
	if selection.ID == "sub" || selection.ID == "hidden" {
		t.Fatalf("subagent/hidden must not be selectable, got %s", selection.ID)
	}
}

func TestSelectFallsBackToFirstSelectable(t *testing.T) {
	registry := NewRegistry()
	registry.Update(Info{ID: "aaa", Mode: "primary"})
	selection := registry.Select("")
	if selection.ID != "aaa" {
		t.Fatalf("expected first selectable fallback, got %s", selection.ID)
	}
}

func TestSelectEmptyRegistry(t *testing.T) {
	registry := NewRegistry()
	selection := registry.Select("")
	if selection.ID != DefaultID || selection.Info != nil {
		t.Fatalf("expected bare build selection, got %+v", selection)
	}
}

func TestResolveAndGet(t *testing.T) {
	registry := NewRegistry()
	registry.Update(Info{ID: "build", Mode: "primary", System: "You are build."})
	info, ok := registry.Resolve("build")
	if !ok || info.System != "You are build." {
		t.Fatalf("expected build agent, got %+v ok=%v", info, ok)
	}
	info, ok = registry.Resolve("")
	if !ok || info.ID != "build" {
		t.Fatalf("expected default resolution, got %+v ok=%v", info, ok)
	}
	if _, ok := registry.Resolve("missing"); ok {
		t.Fatal("expected missing agent to be not found")
	}
}
