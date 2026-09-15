package sync

import (
	"path/filepath"
	"testing"
)

func TestDeviceIDStableOnOneMachine(t *testing.T) {
	paths := Paths{DataDir: filepath.Join(t.TempDir(), "data")}
	first, err := DeviceID(paths, "user@example.com")
	if err != nil {
		t.Fatal(err)
	}
	second, err := DeviceID(paths, "user@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Errorf("deviceID changed across calls on the same machine: %q vs %q", first, second)
	}
	if first == "" {
		t.Error("deviceID must not be empty")
	}
}

func TestDeviceIDDiffersAcrossMachines(t *testing.T) {
	pathsA := Paths{DataDir: filepath.Join(t.TempDir(), "data")}
	pathsB := Paths{DataDir: filepath.Join(t.TempDir(), "data")}
	a, err := DeviceID(pathsA, "user@example.com")
	if err != nil {
		t.Fatal(err)
	}
	b, err := DeviceID(pathsB, "user@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Errorf("two different machines derived the same deviceID: %q", a)
	}
}

func TestDeviceIDDiffersAcrossAccountsOnOneMachine(t *testing.T) {
	paths := Paths{DataDir: filepath.Join(t.TempDir(), "data")}
	a, err := DeviceID(paths, "alice@example.com")
	if err != nil {
		t.Fatal(err)
	}
	b, err := DeviceID(paths, "bob@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Errorf("two different accounts on one machine derived the same deviceID: %q", a)
	}
}

func TestDeviceIDMatchesServerValidation(t *testing.T) {
	// The server (settings.validateDeviceID) accepts only
	// [A-Za-z0-9._:-], at most 128 chars, and rejects the empty string
	// (which addresses the legacy shared doc instead).
	paths := Paths{DataDir: filepath.Join(t.TempDir(), "data")}
	id, err := DeviceID(paths, "user@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if len(id) == 0 || len(id) > 128 {
		t.Fatalf("deviceID length out of bounds: %d", len(id))
	}
	for _, r := range id {
		valid := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') ||
			r == '.' || r == '_' || r == '-' || r == ':'
		if !valid {
			t.Fatalf("deviceID %q contains a character the server rejects: %q", id, r)
		}
	}
}
