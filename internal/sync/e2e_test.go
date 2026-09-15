package sync

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/langazov/gocode-go/internal/gocoder"
)

// TestE2EAgainstLocalServer runs the whole multi-machine story against a
// local gocode_api. Skipped unless GOCODE_E2E_SYNC_URL points at one:
//
//	go run ./cmd/gocode_api &   # in website/backend, memory store is fine
//	GOCODE_E2E_SYNC_URL=http://127.0.0.1:8080 go test ./internal/sync/ -run E2E -v
//
// It covers: fresh-account restore (primes key only), push, a genuinely
// different machine getting its own isolated doc rather than inheriting the
// first machine's settings (the whole point of per-device docs — see the
// deviceID doc comment on gocode-infra/website/backend/internal/settings),
// the same machine restoring its own doc after a state wipe (re-login), the
// account listing both devices, and that the stored envelope decrypts with
// the password alone.
func TestE2EAgainstLocalServer(t *testing.T) {
	base := os.Getenv("GOCODE_E2E_SYNC_URL")
	if base == "" {
		t.Skip("GOCODE_E2E_SYNC_URL not set")
	}
	password := os.Getenv("GOCODE_E2E_SYNC_PASSWORD")
	if password == "" {
		password = "password123"
	}

	client := gocoder.NewClient(base)
	email := "e2e-" + fmt.Sprint(os.Getpid()) + "@example.com"
	session, err := client.Register(context.Background(), email, password, "E2E")
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	key, err := client.CreateKey(context.Background(), session.Token, "e2e")
	if err != nil {
		t.Fatalf("create key: %v", err)
	}
	account := &gocoder.Account{URL: base, Email: email, Key: key.Secret}

	// Machine A: has local settings, signs in on a fresh account.
	root := t.TempDir()
	paths := Paths{ConfigDir: filepath.Join(root, "config"), StateDir: filepath.Join(root, "state"), DataDir: filepath.Join(root, "data")}
	os.MkdirAll(paths.ConfigDir, 0o755)
	local := `{"theme":"machine-a-theme"}`
	os.WriteFile(filepath.Join(paths.ConfigDir, "gocode.json"), []byte(local), 0o644)

	deviceA, err := DeviceID(paths, account.Email)
	if err != nil {
		t.Fatalf("machine A device id: %v", err)
	}
	outcome := RestoreOnLogin(context.Background(), client, account, password, paths, paths.StateDir, deviceA, io.Discard)
	if outcome.Restored || outcome.LocalWon || !outcome.KeyStored {
		t.Fatalf("machine A (fresh account): %+v", outcome)
	}
	mgrA := NewManager(client, paths, paths.StateDir, func() string { return account.Key }, func() string { return deviceA })
	if err := mgrA.Push(context.Background()); err != nil {
		t.Fatalf("machine A push: %v", err)
	}

	// Machine B: a genuinely different machine (own DataDir, so its own
	// deviceID), same account. Its own doc doesn't exist yet, so restore
	// primes fresh state rather than inheriting machine A's settings.
	rootB := t.TempDir()
	pathsB := Paths{ConfigDir: filepath.Join(rootB, "config"), StateDir: filepath.Join(rootB, "state"), DataDir: filepath.Join(rootB, "data")}
	deviceB, err := DeviceID(pathsB, account.Email)
	if err != nil {
		t.Fatalf("machine B device id: %v", err)
	}
	if deviceB == deviceA {
		t.Fatalf("machine A and B derived the same deviceID: %q", deviceA)
	}
	outcomeB := RestoreOnLogin(context.Background(), client, account, password, pathsB, pathsB.StateDir, deviceB, io.Discard)
	if outcomeB.Restored || !outcomeB.KeyStored {
		t.Fatalf("machine B (own, empty doc) should prime only: %+v", outcomeB)
	}
	if _, err := os.Stat(filepath.Join(pathsB.ConfigDir, "gocode.json")); !os.IsNotExist(err) {
		t.Fatalf("machine B should not have inherited machine A's config: err=%v", err)
	}

	// Machine B pushes its own settings; machine A's doc — and local file —
	// must be untouched by it.
	newConfig := `{"theme":"machine-b-theme"}`
	os.MkdirAll(pathsB.ConfigDir, 0o755)
	os.WriteFile(filepath.Join(pathsB.ConfigDir, "gocode.json"), []byte(newConfig), 0o644)
	mgrB := NewManager(client, pathsB, pathsB.StateDir, func() string { return account.Key }, func() string { return deviceB })
	if err := mgrB.Push(context.Background()); err != nil {
		t.Fatalf("machine B push: %v", err)
	}
	applied, err := mgrA.Pull(context.Background())
	if err != nil || applied {
		t.Fatalf("machine A must not see machine B's push: applied=%v err=%v", applied, err)
	}
	gotA, _ := os.ReadFile(filepath.Join(paths.ConfigDir, "gocode.json"))
	if string(gotA) != local {
		t.Fatalf("machine A config changed by machine B's push: %q", gotA)
	}

	// The account now has two device docs.
	devices, err := client.ListSettingsDevices(context.Background(), account.Key)
	if err != nil {
		t.Fatalf("list devices: %v", err)
	}
	if len(devices) != 2 {
		t.Fatalf("expected 2 devices, got %d: %+v", len(devices), devices)
	}

	// Machine A, re-signing in after wiping its state dir (a reinstall):
	// same DataDir means the same deviceID, so its own doc comes back.
	if err := os.RemoveAll(paths.StateDir); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(paths.ConfigDir, "gocode.json")); err != nil {
		t.Fatal(err)
	}
	deviceA2, err := DeviceID(paths, account.Email)
	if err != nil || deviceA2 != deviceA {
		t.Fatalf("machine A deviceID changed after state wipe: %q vs %q (err=%v)", deviceA2, deviceA, err)
	}
	reOutcome := RestoreOnLogin(context.Background(), client, account, password, paths, paths.StateDir, deviceA, io.Discard)
	if !reOutcome.Restored {
		t.Fatalf("machine A re-login should restore its own doc: %+v", reOutcome)
	}
	gotA2, _ := os.ReadFile(filepath.Join(paths.ConfigDir, "gocode.json"))
	if string(gotA2) != local {
		t.Fatalf("machine A restored the wrong doc: %q", gotA2)
	}

	// The envelope the server holds for machine B must open with nothing
	// but the password (this is what the website's unlock does in the
	// browser).
	doc, err := client.GetSettings(context.Background(), account.Key, deviceB)
	if err != nil {
		t.Fatalf("get settings: %v", err)
	}
	var envelope Envelope
	if err := json.Unmarshal([]byte(doc.Envelope), &envelope); err != nil {
		t.Fatal(err)
	}
	k, err := DeriveKey(password, envelope.Salt, envelope.Iter)
	if err != nil {
		t.Fatal(err)
	}
	plaintext, err := Open(&envelope, k)
	if err != nil {
		t.Fatalf("envelope did not decrypt: %v", err)
	}
	var bundle Bundle
	if err := json.Unmarshal([]byte(plaintext), &bundle); err != nil {
		t.Fatal(err)
	}
	if bundle.Files[KeyGlobal] != newConfig {
		t.Fatalf("decrypted bundle: %q", bundle.Files[KeyGlobal])
	}
}
