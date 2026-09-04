package plugin

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/langazov/gocode-go/internal/db"
)

// A config entry naming a native plugin must configure the built-in, not load
// a second copy of it. Two instances of one plugin means its hooks run twice —
// for a hook that appends to the system prompt, that is a visibly duplicated
// block in every request.
func TestConfigEntryForNativeConfiguresRatherThanDuplicates(t *testing.T) {
	withNatives(t)
	var seen []Options
	Register("configurable", func(_ context.Context, _ Input, opts Options) (*Hooks, error) {
		seen = append(seen, opts)
		hooks := &Hooks{}
		On(hooks, ConfigHook, func(context.Context, Empty, *ConfigOutput) error { return nil })
		return hooks, nil
	})

	host, err := Load(context.Background(), LoadInput{
		Input: Input{Directory: t.TempDir()},
		Specs: []Spec{{Ref: "configurable", Options: Options{"maxEntries": 50}}},
	})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if len(seen) != 1 {
		t.Fatalf("factory ran %d times, want exactly 1", len(seen))
	}
	if seen[0]["maxEntries"] != 50 {
		t.Errorf("options = %v, want the config entry's options handed to the native", seen[0])
	}
	if got := len(host.Instances()); got != 1 {
		t.Errorf("host has %d instances, want 1", got)
	}
	if got := len(host.dispatch[ConfigHook.Name()]); got != 1 {
		t.Errorf("hook registered %d times, want 1 — a duplicate would run twice per trigger", got)
	}
}

// The "native:" prefix is the explicit spelling of the same thing and must
// behave identically.
func TestNativePrefixedConfigEntryDoesNotDuplicate(t *testing.T) {
	withNatives(t)
	runs := 0
	Register("prefixed", func(context.Context, Input, Options) (*Hooks, error) {
		runs++
		return &Hooks{}, nil
	})

	host, err := Load(context.Background(), LoadInput{
		Input: Input{Directory: t.TempDir()},
		Specs: []Spec{{Ref: "native:prefixed"}},
	})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if runs != 1 {
		t.Errorf("factory ran %d times, want 1", runs)
	}
	if got := len(host.Instances()); got != 1 {
		t.Errorf("host has %d instances, want 1", got)
	}
}

// DisableNative turns off the tier's *automatic* load. Naming a native in
// config is an explicit request for that one, so it still loads — "do not load
// the built-ins for me, but do load the one I asked for" is a coherent thing
// to want, and it is what Resolve already did before natives could be
// configured. The skip above is therefore conditional on the tier being on;
// with it off there is no earlier instance to duplicate.
func TestDisableNativeStillHonorsExplicitConfigEntry(t *testing.T) {
	withNatives(t)
	runs := 0
	Register("auto-only", func(context.Context, Input, Options) (*Hooks, error) {
		runs++
		return &Hooks{}, nil
	})
	Register("named", func(context.Context, Input, Options) (*Hooks, error) {
		runs++
		return &Hooks{}, nil
	})

	host, err := Load(context.Background(), LoadInput{
		Input:         Input{Directory: t.TempDir()},
		Specs:         []Spec{{Ref: "named"}},
		DisableNative: true,
	})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if runs != 1 {
		t.Errorf("factories ran %d times, want only the one named in config", runs)
	}
	instances := host.Instances()
	if len(instances) != 1 || instances[0].ID != "named" {
		t.Errorf("instances = %v, want just the explicitly configured native", instances)
	}
}

// Services carries live in-process handles and must never reach a process
// plugin, which receives Input as JSON over a pipe. The `json:"-"` tag is what
// makes that structural; this asserts it stays that way.
func TestServicesAreNotSerialized(t *testing.T) {
	database, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })

	encoded, err := json.Marshal(Input{
		Directory: "/tmp/work",
		Services:  Services{DB: database, ProjectID: "prj_secret"},
	})
	if err != nil {
		t.Fatalf("marshaling Input: %v", err)
	}
	for _, leak := range []string{"Services", "services", "prj_secret"} {
		if strings.Contains(string(encoded), leak) {
			t.Errorf("Input JSON leaks %q to the process tier: %s", leak, encoded)
		}
	}
}
