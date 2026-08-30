package auth

import (
	"testing"
)

func TestSetGetRemove(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("OPENCODE_AUTH_CONTENT", "")

	if err := Set("anthropic", Info{Type: "api", Key: "sk-ant-123"}); err != nil {
		t.Fatal(err)
	}
	got, err := Get("anthropic")
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.Key != "sk-ant-123" {
		t.Fatalf("expected stored key, got %+v", got)
	}

	all, err := All()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(all))
	}

	if err := Remove("anthropic"); err != nil {
		t.Fatal(err)
	}
	got, err = Get("anthropic")
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Fatalf("expected nil after remove, got %+v", got)
	}
}

func TestSetNormalizesTrailingSlash(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("OPENCODE_AUTH_CONTENT", "")

	if err := Set("openai/", Info{Type: "api", Key: "sk-x"}); err != nil {
		t.Fatal(err)
	}
	got, err := Get("openai")
	if err != nil {
		t.Fatal(err)
	}
	if got == nil {
		t.Fatal("expected normalized key lookup to succeed")
	}
}

func TestInvalidInfoRejected(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("OPENCODE_AUTH_CONTENT", "")
	if err := Set("anthropic", Info{Type: "api"}); err == nil {
		t.Fatal("expected error for api entry without key")
	}
}

func TestAuthContentEnv(t *testing.T) {
	t.Setenv("OPENCODE_AUTH_CONTENT", `{"anthropic":{"type":"api","key":"env-key"}}`)
	got, err := Get("anthropic")
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.Key != "env-key" {
		t.Fatalf("expected env-provided key, got %+v", got)
	}
}

func TestOAuthRoundtrip(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("OPENCODE_AUTH_CONTENT", "")
	info := Info{Type: "oauth", Refresh: "r", Access: "a", Expires: 123}
	if err := Set("github", info); err != nil {
		t.Fatal(err)
	}
	got, err := Get("github")
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.Type != "oauth" || got.Access != "a" || got.Expires != 123 {
		t.Fatalf("unexpected oauth roundtrip: %+v", got)
	}
}
