package memoryplugin

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/langazov/gocode-go/internal/db"
	"github.com/langazov/gocode-go/internal/memory"
	"github.com/langazov/gocode-go/internal/plugin"
)

const projectID = "prj_test"

func setup(t *testing.T) (*plugin.Hooks, *memory.Store, context.Context) {
	t.Helper()
	return setupWith(t, nil)
}

func setupWith(t *testing.T, opts plugin.Options) (*plugin.Hooks, *memory.Store, context.Context) {
	t.Helper()
	ctx := context.Background()
	database, err := db.OpenAndMigrate(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })

	hooks, err := New(ctx, plugin.Input{
		Directory: t.TempDir(),
		Services:  plugin.Services{DB: database, ProjectID: projectID},
	}, opts)
	if err != nil {
		t.Fatal(err)
	}
	return hooks, memory.New(database), ctx
}

// host wraps hooks in a real Host so Trigger dispatches exactly as it does in
// the runner, rather than the test calling the hook function directly.
func host(t *testing.T, hooks *plugin.Hooks) *plugin.Host {
	t.Helper()
	h := plugin.NewHost(func(id, hook string, err error) {
		t.Errorf("hook %s of %s failed: %v", hook, id, err)
	})
	h.Add(&plugin.Instance{ID: Name, Spec: Name, Source: plugin.SourceNative, Hooks: hooks})
	return h
}

func transform(t *testing.T, h *plugin.Host, ctx context.Context) []string {
	t.Helper()
	out := plugin.SystemTransformOutput{System: []string{"base prompt"}}
	if err := plugin.Trigger(ctx, h, plugin.SystemTransform, plugin.SystemTransformInput{
		SessionID: "ses_1",
	}, &out); err != nil {
		t.Fatal(err)
	}
	return out.System
}

// Without a database there is nothing to read, so the plugin opts out. The
// loader treats nil hooks as a successful load that registered nothing.
func TestNewOptsOutWithoutDatabase(t *testing.T) {
	hooks, err := New(context.Background(), plugin.Input{}, nil)
	if err != nil {
		t.Fatalf("a missing database should not be a load failure: %v", err)
	}
	if hooks != nil {
		t.Error("expected nil hooks with no database")
	}
}

func TestNewOptsOutWhenDisabled(t *testing.T) {
	hooks, _, _ := setupWith(t, plugin.Options{"enabled": false})
	if hooks != nil {
		t.Error("expected nil hooks when disabled by config")
	}
}

// An empty store must add nothing at all. An empty <memories> block on every
// request would be pure overhead, and it tells the model something misleading
// rather than nothing.
func TestSystemTransformAddsNothingWhenEmpty(t *testing.T) {
	hooks, _, ctx := setup(t)
	system := transform(t, host(t, hooks), ctx)
	if len(system) != 1 || system[0] != "base prompt" {
		t.Errorf("system = %v, want the base prompt untouched", system)
	}
}

func TestSystemTransformAppendsActiveMemories(t *testing.T) {
	hooks, store, ctx := setup(t)
	if _, err := store.Create(ctx, memory.Memory{Scope: projectID, Content: "Run make check"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create(ctx, memory.Memory{Scope: memory.ScopeGlobal, Content: "Prefer Go stdlib"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create(ctx, memory.Memory{Scope: "prj_other", Content: "Someone else's rule"}); err != nil {
		t.Fatal(err)
	}

	system := transform(t, host(t, hooks), ctx)
	if len(system) != 2 {
		t.Fatalf("system has %d blocks, want the base prompt plus one memory block: %v", len(system), system)
	}
	if system[0] != "base prompt" {
		t.Error("the memory block should be appended, not prepended")
	}
	block := system[1]
	for _, want := range []string{"Run make check", "Prefer Go stdlib"} {
		if !strings.Contains(block, want) {
			t.Errorf("block missing %q:\n%s", want, block)
		}
	}
	if strings.Contains(block, "Someone else's rule") {
		t.Errorf("block leaked another project's memory:\n%s", block)
	}
}

func TestOptionsOverrideBudget(t *testing.T) {
	hooks, store, ctx := setupWith(t, plugin.Options{"maxEntries": float64(1)})
	for _, content := range []string{"First rule", "Second rule"} {
		if _, err := store.Create(ctx, memory.Memory{Scope: projectID, Content: content}); err != nil {
			t.Fatal(err)
		}
	}
	block := transform(t, host(t, hooks), ctx)[1]
	if !strings.Contains(block, "1 further memory omitted") {
		t.Errorf("maxEntries option was not applied:\n%s", block)
	}
}

func TestParseOptionsDefaults(t *testing.T) {
	resolved := parseOptions(nil)
	if !resolved.Enabled {
		t.Error("default should be enabled")
	}
	if resolved.Budget != memory.DefaultBudget {
		t.Errorf("budget = %+v, want the default %+v", resolved.Budget, memory.DefaultBudget)
	}
	// A typo in a settings bag must not cost the user their memories.
	junk := parseOptions(plugin.Options{"maxEntries": "fifty", "enabled": "yes"})
	if !junk.Enabled || junk.Budget != memory.DefaultBudget {
		t.Errorf("unparseable options should be ignored, got %+v", junk)
	}
}

func TestResolveScope(t *testing.T) {
	cases := []struct {
		in, project, want string
		wantErr           bool
	}{
		{in: "", project: projectID, want: projectID},
		{in: "project", project: projectID, want: projectID},
		{in: "Global", project: projectID, want: memory.ScopeGlobal},
		{in: "global", project: "", want: memory.ScopeGlobal},
		// With no project resolved there is nothing to scope to but global.
		{in: "project", project: "", want: memory.ScopeGlobal},
		{in: "everywhere", project: projectID, wantErr: true},
	}
	for _, tc := range cases {
		got, err := resolveScope(tc.in, tc.project)
		if tc.wantErr {
			if err == nil {
				t.Errorf("resolveScope(%q) = %q, want an error", tc.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("resolveScope(%q): %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("resolveScope(%q, %q) = %q, want %q", tc.in, tc.project, got, tc.want)
		}
	}
}
