package mcp

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/langazov/gocode-go/internal/global"
	"github.com/langazov/gocode-go/internal/tool"
)

// The usage store (usage.go) is what makes a disabled server's context cost
// survive a restart, and what decides whether the boot probe runs. These
// tests cover the store itself and the service behavior built on it:
// seeding from the store, the measure-once probe that never registers
// tools, and the persist-on-connect that keeps the store fresh.

// isolateUsageStore points the store at a fresh per-test data dir. The
// package TestMain already isolates XDG_DATA_HOME for the whole run; this
// gives an individual test its own store (so a write in one test can never
// leak a measurement into another).
func isolateUsageStore(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_DATA_HOME", t.TempDir())
}

// TestUsageStoreRoundTrip covers set/get/remove and the file it lands in.
func TestUsageStoreRoundTrip(t *testing.T) {
	isolateUsageStore(t)

	cfg := ServerConfig{Type: "local", Command: []string{"gh-mcp"}}
	if _, ok := UsageGet("github", cfg); ok {
		t.Fatal("expected no entry before Set")
	}

	entry := UsageEntry{Type: "local", Command: []string{"gh-mcp"}, ToolCount: 12, EstTokens: 3400}
	if err := UsageSet("github", entry); err != nil {
		t.Fatal(err)
	}

	got, ok := UsageGet("github", cfg)
	if !ok {
		t.Fatal("expected entry after Set")
	}
	if got.ToolCount != 12 || got.EstTokens != 3400 {
		t.Fatalf("got = %+v, want the stored figures", got)
	}

	// The store is a file in the data dir, so a second process (the next
	// boot) sees it without any in-process handoff.
	data, err := os.ReadFile(filepath.Join(globalDataDir(t), "mcp-usage.json"))
	if err != nil {
		t.Fatalf("usage store not written where the next boot would read it: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("usage store is empty")
	}

	if err := UsageRemove("github"); err != nil {
		t.Fatal(err)
	}
	if _, ok := UsageGet("github", cfg); ok {
		t.Fatal("expected no entry after Remove")
	}
}

// globalDataDir resolves the isolated data dir the same way usage.go does.
func globalDataDir(t *testing.T) string {
	t.Helper()
	return global.Resolve().Data
}

// TestUsageGetInvalidatesOnEndpointChange mirrors the identity rule
// StoreGet applies to credentials: a stored measurement only counts
// against the endpoint it was taken from, so a re-pointed server measures
// fresh instead of showing the old number.
func TestUsageGetInvalidatesOnEndpointChange(t *testing.T) {
	isolateUsageStore(t)

	if err := UsageSet("s", UsageEntry{Type: "remote", URL: "https://old.example"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := UsageGet("s", ServerConfig{Type: "remote", URL: "https://old.example"}); !ok {
		t.Fatal("expected the entry to match its own endpoint")
	}
	if _, ok := UsageGet("s", ServerConfig{Type: "remote", URL: "https://new.example"}); ok {
		t.Error("entry matched a changed URL")
	}
	if _, ok := UsageGet("s", ServerConfig{Type: "local", Command: []string{"cmd"}}); ok {
		t.Error("entry matched a changed type/command")
	}
}

// A disabled server that boots with a stored measurement shows it
// immediately — no probe, no connect, no waiting: seeding happens on the
// connect path that lists it as disabled.
func TestDisabledServerSeedsFromStoredUsage(t *testing.T) {
	isolateUsageStore(t)
	httpServer := newTestMCPServer(t)

	cfg := ServerConfig{Type: "remote", URL: httpServer.URL, Enabled: boolPtr(false)}
	if err := UsageSet("off", UsageEntry{Type: cfg.Type, URL: cfg.URL, ToolCount: 3, EstTokens: 400}); err != nil {
		t.Fatal(err)
	}

	svc := NewService(t.TempDir())
	defer svc.Close()
	// The probe would also fill the figures in; off here proves the numbers
	// come from the store, synchronously, rather than from the probe.
	svc.setUsageProbeEnabled(false)

	svc.Load(t.Context(), map[string]ServerConfig{"off": cfg})

	servers := svc.Servers()
	if len(servers) != 1 {
		t.Fatalf("Servers() = %+v, want the one disabled entry", servers)
	}
	got := servers[0]
	if got.Status.Status != "disabled" || got.CostLive {
		t.Errorf("status = %+v / costLive = %v, want disabled and not live", got.Status, got.CostLive)
	}
	if got.ToolCount != 3 || got.EstTokens != 400 {
		t.Errorf("figures = %d tools / %d tokens, want the stored 3 / 400", got.ToolCount, got.EstTokens)
	}
}

// A disabled server with no measurement anywhere is probed once in the
// background: the endpoint is connected far enough to count its tools, the
// figures are persisted, the session is closed again — and nothing is ever
// registered, so the tool descriptions never reach a model's context. The
// server stays disabled throughout.
func TestDisabledServerProbeMeasuresWithoutRegistering(t *testing.T) {
	isolateUsageStore(t)
	httpServer := newTestMCPServer(t)

	svc := NewService(t.TempDir())
	defer svc.Close()
	registry := tool.NewRegistry()
	svc.SetRegistry(registry)

	cfg := ServerConfig{Type: "remote", URL: httpServer.URL, Enabled: boolPtr(false)}
	svc.Load(t.Context(), map[string]ServerConfig{"off": cfg})

	// The probe is background; poll for its figures landing on the row.
	deadline := time.Now().Add(5 * time.Second)
	var got ServerInfo
	for {
		servers := svc.Servers()
		if len(servers) == 1 && servers[0].ToolCount > 0 {
			got = servers[0]
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("probe never measured the disabled server, Servers() = %+v", servers)
		}
		time.Sleep(5 * time.Millisecond)
	}

	if got.Status.Status != "disabled" {
		t.Errorf("status after probe = %q, want still disabled", got.Status.Status)
	}
	if got.CostLive {
		t.Error("CostLive = true after probe, want false (nothing is in the context)")
	}
	if got.ToolCount != 1 || got.EstTokens <= 0 {
		t.Errorf("probed figures = %d tools / %d tokens, want the upstream's 1 tool and a positive estimate", got.ToolCount, got.EstTokens)
	}
	if _, ok := registry.Get(ToolName("off", "echo")); ok {
		t.Error("probe registered the measured tool — descriptions must not reach the context")
	}

	// The measurement was persisted, which is the whole point: the next
	// boot seeds from the store instead of probing again.
	if entry, ok := UsageGet("off", cfg); !ok || entry.ToolCount != 1 || entry.EstTokens != got.EstTokens {
		t.Errorf("UsageGet after probe = %+v (ok=%v), want the probed figures persisted", entry, ok)
	}
}

// A live connect persists its measurement, so a server that is later
// disabled by editing the config by hand (not through the dialog) still
// has a figure for the next boot to seed from.
func TestLiveConnectPersistsUsage(t *testing.T) {
	isolateUsageStore(t)
	httpServer := newTestMCPServer(t)

	svc := NewService(t.TempDir())
	defer svc.Close()

	cfg := ServerConfig{Type: "remote", URL: httpServer.URL}
	if status := svc.Connect(t.Context(), "off", cfg); status.Status != "connected" {
		t.Fatalf("connect = %+v", status)
	}

	entry, ok := UsageGet("off", cfg)
	if !ok {
		t.Fatal("no measurement persisted after a live connect")
	}
	if entry.ToolCount != 1 || entry.EstTokens <= 0 {
		t.Errorf("persisted = %d tools / %d tokens, want the live measurement", entry.ToolCount, entry.EstTokens)
	}
}

// The measure-once guard is per process: a server that boots disabled is
// probed once, never again on a later disabled connect (a second Load, a
// manual Connect with a disabled config) — however often connect() sees it
// disabled. markUsageProbed is the guard connect() consults, so driving it
// directly pins its remember-and-report semantics.
func TestDisabledServerProbeRunsOncePerProcess(t *testing.T) {
	isolateUsageStore(t)
	svc := NewService(t.TempDir())
	defer svc.Close()

	if svc.markUsageProbed("off") {
		t.Error("first mark reported already-probed")
	}
	for range 3 {
		if !svc.markUsageProbed("off") {
			t.Error("later marks must report already-probed")
		}
	}
}
