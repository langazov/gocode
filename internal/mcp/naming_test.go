package mcp

import "testing"

func TestToolNameSingleUnderscore(t *testing.T) {
	// catalog.ts's toolName() joins with a single underscore, not
	// "mcp__server__tool".
	if got, want := ToolName("myserver", "mytool"), "myserver_mytool"; got != want {
		t.Fatalf("ToolName() = %q, want %q", got, want)
	}
}

func TestSanitizeReplacesInvalidChars(t *testing.T) {
	if got, want := sanitize("my server!.name"), "my_server__name"; got != want {
		t.Fatalf("sanitize() = %q, want %q", got, want)
	}
	if got, want := sanitize("valid-name_123"), "valid-name_123"; got != want {
		t.Fatalf("sanitize() should leave valid chars alone, got %q", got)
	}
}

func TestResourceKey(t *testing.T) {
	if got, want := resourceKey("my.server", "my/resource"), "my_server:my_resource"; got != want {
		t.Fatalf("resourceKey() = %q, want %q", got, want)
	}
}
