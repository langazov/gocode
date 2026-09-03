package flag

import (
	"os"
	"testing"
)

func TestTruthy(t *testing.T) {
	cases := map[string]bool{
		"true":  true,
		"TRUE":  true,
		"1":     true,
		"false": false,
		"0":     false,
		"":      false,
		"yes":   false,
	}
	for value, want := range cases {
		t.Setenv("TEST_FLAG", value)
		if got := Truthy("TEST_FLAG"); got != want {
			t.Fatalf("Truthy(%q) = %v, want %v", value, got, want)
		}
	}
}

func TestClientDefault(t *testing.T) {
	t.Setenv("GOCODE_CLIENT", "")
	if got := Client(); got != "cli" {
		t.Fatalf("expected cli, got %s", got)
	}
	t.Setenv("GOCODE_CLIENT", "tui")
	if got := Client(); got != "tui" {
		t.Fatalf("expected tui, got %s", got)
	}
}

func TestExperimentalFallback(t *testing.T) {
	prev, hadPrev := os.LookupEnv("GOCODE_EXPERIMENTAL_WORKSPACES")
	t.Cleanup(func() {
		if hadPrev {
			os.Setenv("GOCODE_EXPERIMENTAL_WORKSPACES", prev)
		} else {
			os.Unsetenv("GOCODE_EXPERIMENTAL_WORKSPACES")
		}
	})

	t.Setenv("GOCODE_EXPERIMENTAL", "true")
	os.Unsetenv("GOCODE_EXPERIMENTAL_WORKSPACES")
	if !ExperimentalWorkspaces() {
		t.Fatalf("expected fallback to GOCODE_EXPERIMENTAL when key unset")
	}

	os.Setenv("GOCODE_EXPERIMENTAL_WORKSPACES", "false")
	if ExperimentalWorkspaces() {
		t.Fatalf("explicit false should win over experimental fallback")
	}
}
