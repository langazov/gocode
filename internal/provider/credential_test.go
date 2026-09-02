package provider

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/anomalyco/opencode-go/internal/auth"
	"github.com/anomalyco/opencode-go/internal/modelsdev"
)

// writeAuth isolates the credential store to this test and seeds it.
func writeAuth(t *testing.T, entries map[string]any) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dir)
	t.Setenv("OPENCODE_AUTH_CONTENT", "")
	payload, err := json.Marshal(entries)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "opencode")
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "auth.json"), payload, 0o600); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(path, "auth.json")
}

// stubRefresher is a Transform + Refresher registered for one test-only id.
type stubRefresher struct {
	byID
	calls   *int
	fail    bool
	renewed string
}

func (s stubRefresher) Apply(context.Context, *Resolved) error { return nil }

func (s stubRefresher) RefreshCredential(_ context.Context, info auth.Info) (auth.Info, error) {
	*s.calls++
	if s.fail {
		return auth.Info{}, context.DeadlineExceeded
	}
	info.Access = s.renewed
	info.Refresh = "next-refresh"
	info.Expires = time.Now().Add(time.Hour).UnixMilli()
	return info, nil
}

// withStubRefresher registers a refresher for the duration of a test.
func withStubRefresher(t *testing.T, id string, stub stubRefresher) {
	t.Helper()
	before := len(registry)
	Register(stub)
	t.Cleanup(func() { registry = registry[:before] })
}

func TestNeedsRefresh(t *testing.T) {
	cases := []struct {
		name string
		info auth.Info
		want bool
	}{
		{"expired", auth.Info{Type: "oauth", Expires: time.Now().Add(-time.Hour).UnixMilli()}, true},
		{"inside the 5-minute window", auth.Info{Type: "oauth", Expires: time.Now().Add(2 * time.Minute).UnixMilli()}, true},
		{"comfortably valid", auth.Info{Type: "oauth", Expires: time.Now().Add(time.Hour).UnixMilli()}, false},
		// GitHub tokens do not expire and the Copilot flow stores 0; treating
		// that as "expired in 1970" would refresh on every single request.
		{"no expiry recorded", auth.Info{Type: "oauth", Expires: 0}, false},
		{"api keys never expire", auth.Info{Type: "api", Key: "k"}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := auth.NeedsRefresh(c.info); got != c.want {
				t.Errorf("NeedsRefresh = %v, want %v", got, c.want)
			}
		})
	}
}

// TestExpiresAtIsMilliseconds guards the unit shared with the TypeScript
// binary: auth.json is read by both, and TS writes Date.now()-based millis.
func TestExpiresAtIsMilliseconds(t *testing.T) {
	got := auth.TokenResponse{ExpiresIn: 3600}.ExpiresAt()
	want := time.Now().Add(time.Hour).UnixMilli()
	if diff := got - want; diff > 5000 || diff < -5000 {
		t.Fatalf("ExpiresAt = %d, want ~%d (milliseconds since epoch)", got, want)
	}
	// A seconds-based value would land in 1970 when read back as millis.
	if time.UnixMilli(got).Year() < 2000 {
		t.Errorf("ExpiresAt produced %v — looks like seconds, not milliseconds", time.UnixMilli(got))
	}
}

func TestResolveCredentialRefreshesExpiredToken(t *testing.T) {
	path := writeAuth(t, map[string]any{
		"stub-provider": map[string]any{
			"type": "oauth", "access": "old", "refresh": "r",
			"expires": time.Now().Add(-time.Hour).UnixMilli(),
		},
	})
	calls := 0
	withStubRefresher(t, "stub-provider", stubRefresher{byID: byID{"stub-provider"}, calls: &calls, renewed: "fresh"})

	info, err := ResolveCredential(context.Background(), "stub-provider", modelsdev.Provider{})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Errorf("refresher called %d times, want 1", calls)
	}
	if info.Access != "fresh" {
		t.Errorf("access = %q, want the renewed token", info.Access)
	}

	// The renewal must be persisted, or every process start refreshes again.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "fresh") {
		t.Errorf("refreshed credential was not written back: %s", data)
	}
}

func TestResolveCredentialLeavesValidTokenAlone(t *testing.T) {
	writeAuth(t, map[string]any{
		"stub-provider": map[string]any{
			"type": "oauth", "access": "current", "refresh": "r",
			"expires": time.Now().Add(time.Hour).UnixMilli(),
		},
	})
	calls := 0
	withStubRefresher(t, "stub-provider", stubRefresher{byID: byID{"stub-provider"}, calls: &calls, renewed: "fresh"})

	info, err := ResolveCredential(context.Background(), "stub-provider", modelsdev.Provider{})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Errorf("refresher called %d times for a valid token, want 0", calls)
	}
	if info.Access != "current" {
		t.Errorf("access = %q, want the stored token untouched", info.Access)
	}
}

// TestResolveCredentialKeepsCredentialWhenRefreshFails: a flaky token endpoint
// must not look like being logged out — the existing token may still work.
func TestResolveCredentialKeepsCredentialWhenRefreshFails(t *testing.T) {
	writeAuth(t, map[string]any{
		"stub-provider": map[string]any{
			"type": "oauth", "access": "old", "refresh": "r",
			"expires": time.Now().Add(time.Minute).UnixMilli(),
		},
	})
	calls := 0
	withStubRefresher(t, "stub-provider", stubRefresher{byID: byID{"stub-provider"}, calls: &calls, fail: true})

	info, err := ResolveCredential(context.Background(), "stub-provider", modelsdev.Provider{})
	if err != nil {
		t.Fatalf("a failed refresh must not be fatal: %v", err)
	}
	if info == nil || info.Access != "old" {
		t.Errorf("info = %+v, want the existing credential preserved", info)
	}
}

// TestResolveCredentialWithoutRefresherIsUnchanged mirrors TS's
// `if (!implementation?.refresh) return credential.value`.
func TestResolveCredentialWithoutRefresherIsUnchanged(t *testing.T) {
	writeAuth(t, map[string]any{
		"no-refresher": map[string]any{
			"type": "oauth", "access": "stale", "refresh": "r",
			"expires": time.Now().Add(-time.Hour).UnixMilli(),
		},
	})
	info, err := ResolveCredential(context.Background(), "no-refresher", modelsdev.Provider{})
	if err != nil {
		t.Fatal(err)
	}
	if info.Access != "stale" {
		t.Errorf("access = %q, want the stored value when nothing can refresh it", info.Access)
	}
}
