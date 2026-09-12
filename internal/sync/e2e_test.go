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
// It covers: fresh-account restore (primes key only), push, fresh-machine
// restore (server wins), machine B edits + machine A pulls, and that the
// stored envelope decrypts with the password alone.
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
	session, err := client.Register(context.Background(),
		"e2e-"+fmt.Sprint(os.Getpid())+"@example.com", password, "E2E")
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	key, err := client.CreateKey(context.Background(), session.Token, "e2e")
	if err != nil {
		t.Fatalf("create key: %v", err)
	}
	account := &gocoder.Account{URL: base, Key: key.Secret}

	// Machine A: has local settings, signs in on a fresh account.
	root := t.TempDir()
	paths := Paths{ConfigDir: filepath.Join(root, "config"), StateDir: filepath.Join(root, "state"), DataDir: filepath.Join(root, "data")}
	os.MkdirAll(paths.ConfigDir, 0o755)
	local := `{"theme":"machine-a-theme"}`
	os.WriteFile(filepath.Join(paths.ConfigDir, "gocode.json"), []byte(local), 0o644)

	outcome := RestoreOnLogin(context.Background(), client, account, password, paths, paths.StateDir, io.Discard)
	if outcome.Restored || outcome.LocalWon || !outcome.KeyStored {
		t.Fatalf("machine A (fresh account): %+v", outcome)
	}
	mgrA := NewManager(client, paths, paths.StateDir, func() string { return account.Key })
	if err := mgrA.Push(context.Background()); err != nil {
		t.Fatalf("machine A push: %v", err)
	}

	// Machine B: fresh machine, same account. Server must win.
	rootB := t.TempDir()
	pathsB := Paths{ConfigDir: filepath.Join(rootB, "config"), StateDir: filepath.Join(rootB, "state"), DataDir: filepath.Join(rootB, "data")}
	outcomeB := RestoreOnLogin(context.Background(), client, account, password, pathsB, pathsB.StateDir, io.Discard)
	if !outcomeB.Restored {
		t.Fatalf("machine B: %+v", outcomeB)
	}
	got, err := os.ReadFile(filepath.Join(pathsB.ConfigDir, "gocode.json"))
	if err != nil || string(got) != local {
		t.Fatalf("machine B config: %q %v", got, err)
	}

	// Machine B edits; machine A pulls.
	newConfig := `{"theme":"machine-b-theme"}`
	os.WriteFile(filepath.Join(pathsB.ConfigDir, "gocode.json"), []byte(newConfig), 0o644)
	mgrB := NewManager(client, pathsB, pathsB.StateDir, func() string { return account.Key })
	if err := mgrB.Push(context.Background()); err != nil {
		t.Fatalf("machine B push: %v", err)
	}
	applied, err := mgrA.Pull(context.Background())
	if err != nil || !applied {
		t.Fatalf("machine A pull: applied=%v err=%v", applied, err)
	}
	gotA, _ := os.ReadFile(filepath.Join(paths.ConfigDir, "gocode.json"))
	if string(gotA) != newConfig {
		t.Fatalf("machine A after pull: %q", gotA)
	}

	// The envelope the server holds must open with nothing but the password
	// (this is what the website's unlock does in the browser).
	doc, err := client.GetSettings(context.Background(), account.Key)
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
