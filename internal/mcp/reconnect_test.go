package mcp

import (
	"os"
	"testing"
	"time"

	"github.com/anomalyco/opencode-go/internal/tool"
)

// waitForStatus polls until svc reports name at the given status, or fails
// the test after timeout — used throughout since connect/reconnect happen
// on background goroutines with no other completion signal to wait on.
func waitForStatus(t *testing.T, svc *Service, name, status string, timeout time.Duration) Status {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		got := svc.Statuses()[name]
		if got.Status == status {
			return got
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %q to reach status %q, last saw %+v", name, status, got)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestLoadAsyncReturnsImmediately(t *testing.T) {
	httpServer := newTestMCPServer(t)
	svc := NewService(t.TempDir())
	defer svc.Close()

	start := time.Now()
	svc.LoadAsync(map[string]ServerConfig{"good": {Type: "remote", URL: httpServer.URL}})
	if elapsed := time.Since(start); elapsed > 100*time.Millisecond {
		t.Fatalf("LoadAsync blocked for %v, want near-instant return", elapsed)
	}

	// Never "missing" even before the background connect resolves.
	if _, ok := svc.Statuses()["good"]; !ok {
		t.Fatal("expected a status placeholder for \"good\" immediately after LoadAsync")
	}

	waitForStatus(t, svc, "good", "connected", time.Second)
}

func TestLoadAsyncAutoRegistersToolsViaSetRegistry(t *testing.T) {
	httpServer := newTestMCPServer(t)
	svc := NewService(t.TempDir())
	defer svc.Close()

	registry := tool.NewRegistry()
	svc.SetRegistry(registry)
	svc.LoadAsync(map[string]ServerConfig{"good": {Type: "remote", URL: httpServer.URL}})
	waitForStatus(t, svc, "good", "connected", time.Second)

	name := ToolName("good", "echo")
	if _, ok := registry.Get(name); !ok {
		t.Fatalf("expected %q auto-registered without an explicit RegisterTools call, registry has: %v", name, registry.Names())
	}
}

func TestServiceReconnectsAfterUnexpectedDrop(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	svc := NewService(t.TempDir())
	defer svc.Close()
	svc.setReconnectBackoff(5*time.Millisecond, 20*time.Millisecond)

	cfg := ServerConfig{Type: "local", Command: []string{self}, Environment: map[string]string{stdioServerEnv: "1"}}
	svc.LoadAsync(map[string]ServerConfig{"local": cfg})
	waitForStatus(t, svc, "local", "connected", time.Second)

	// Simulate the server dying out from under the service (crash, killed
	// process, network blip) — closing the session directly rather than
	// going through Disconnect, since Disconnect means "the user asked to
	// stop", which must NOT auto-reconnect, unlike this.
	svc.mu.RLock()
	conn := svc.conns["local"]
	svc.mu.RUnlock()
	conn.session.Close()

	waitForStatus(t, svc, "local", "connected", time.Second)
}

func TestDisconnectDoesNotAutoReconnect(t *testing.T) {
	httpServer := newTestMCPServer(t)
	svc := NewService(t.TempDir())
	defer svc.Close()
	svc.setReconnectBackoff(5*time.Millisecond, 20*time.Millisecond)

	svc.LoadAsync(map[string]ServerConfig{"good": {Type: "remote", URL: httpServer.URL}})
	waitForStatus(t, svc, "good", "connected", time.Second)

	svc.Disconnect("good")

	// Give any (incorrect) auto-reconnect several backoff cycles worth of
	// time to fire before asserting it stayed disconnected.
	time.Sleep(100 * time.Millisecond)
	if _, ok := svc.Statuses()["good"]; ok {
		t.Fatalf("expected no status entry after Disconnect (until a manual reconnect), got %+v", svc.Statuses()["good"])
	}
}

func TestCloseStopsReconnectLoop(t *testing.T) {
	svc := NewService(t.TempDir())
	svc.setReconnectBackoff(5*time.Millisecond, 10*time.Millisecond)
	// Nothing listens here: the initial connect fails and kicks off a
	// reconnect loop that will retry forever unless Close() stops it.
	svc.LoadAsync(map[string]ServerConfig{"bad": {Type: "remote", URL: "http://127.0.0.1:1"}})
	waitForStatus(t, svc, "bad", "failed", time.Second)

	svc.Close()

	// If reconnectLoop is still running after Close(), it'll keep calling
	// connect() and mutating conns from under us; run with -race to catch
	// that. There's nothing to assert beyond "this doesn't hang or race".
	time.Sleep(50 * time.Millisecond)
}
