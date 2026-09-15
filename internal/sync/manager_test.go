package sync

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/langazov/gocode-go/internal/gocoder"
)

// fakeServer is a minimal gocoder.org settings backend: enough state for
// the manager's optimistic lock and revision semantics, one doc per
// deviceID (the query param on GET/pending, the "deviceId" body field on
// PUT — "" is the legacy shared doc), mirroring
// gocode-infra/website/backend/internal/settings.
type fakeServer struct {
	mu   sync.Mutex
	docs map[string]*fakeDoc // keyed by deviceID
	puts int
	gets int
}

type fakeDoc struct {
	envelope  string
	revision  int64
	conflicts int
}

func (f *fakeServer) handler(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.docs == nil {
		f.docs = map[string]*fakeDoc{}
	}
	deviceID := r.URL.Query().Get("device")
	switch {
	case r.URL.Path == "/api/settings" && r.Method == http.MethodGet:
		f.gets++
		doc := f.docs[deviceID]
		if doc == nil {
			writeErr(w, http.StatusNotFound, "not_found", "no settings saved")
			return
		}
		writeJSON(w, map[string]any{
			"deviceId": deviceID, "envelope": doc.envelope, "revision": doc.revision, "updatedAt": time.Now().Format(time.RFC3339),
		})
	case r.URL.Path == "/api/settings" && r.Method == http.MethodPut:
		f.puts++
		var body struct {
			Envelope     string `json:"envelope"`
			BaseRevision int64  `json:"baseRevision"`
			DeviceID     string `json:"deviceId"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		doc := f.docs[body.DeviceID]
		if doc != nil && body.BaseRevision != doc.revision {
			doc.conflicts++
			writeErr(w, http.StatusConflict, "conflict", "stale revision")
			return
		}
		if doc == nil {
			doc = &fakeDoc{}
			f.docs[body.DeviceID] = doc
		}
		doc.envelope = body.Envelope
		doc.revision++
		writeJSON(w, map[string]any{
			"deviceId": body.DeviceID, "envelope": doc.envelope, "revision": doc.revision, "updatedAt": time.Now().Format(time.RFC3339),
		})
	case r.URL.Path == "/api/settings/pending":
		revision := int64(0)
		if doc := f.docs[deviceID]; doc != nil {
			revision = doc.revision
		}
		writeJSON(w, map[string]any{"revision": revision})
	default:
		writeErr(w, http.StatusNotFound, "not_found", "unknown")
	}
}

// doc returns deviceID's stored doc, or nil.
func (f *fakeServer) doc(deviceID string) *fakeDoc {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.docs[deviceID]
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": code, "message": message}})
}

// env is a scratch home + manager + server per test. deviceID is fixed
// ("test-device") rather than "" so these tests exercise the same
// per-device codepath production does, not just the legacy shared doc.
type env struct {
	server    *fakeServer
	client    *gocoder.Client
	manager   *Manager
	configDir string
	stateDir  string
	dataDir   string
	deviceID  string
}

func newEnv(t *testing.T) *env {
	t.Helper()
	root := t.TempDir()
	e := &env{
		server:    &fakeServer{},
		configDir: filepath.Join(root, "config"),
		stateDir:  filepath.Join(root, "state"),
		dataDir:   filepath.Join(root, "data"),
		deviceID:  "test-device",
	}
	srv := httptest.NewServer(http.HandlerFunc(e.server.handler))
	t.Cleanup(srv.Close)
	e.client = gocoder.NewClient(srv.URL)
	e.manager = NewManager(e.client, Paths{ConfigDir: e.configDir, StateDir: e.stateDir, DataDir: e.dataDir}, e.stateDir,
		func() string { return "gk_test" }, func() string { return e.deviceID })
	return e
}

// setDoc seeds deviceID's doc directly, simulating a bundle already sitting
// on the server (from another sync, or the website).
func (e *env) setDoc(deviceID, envelope string, revision int64) {
	e.server.mu.Lock()
	defer e.server.mu.Unlock()
	if e.server.docs == nil {
		e.server.docs = map[string]*fakeDoc{}
	}
	e.server.docs[deviceID] = &fakeDoc{envelope: envelope, revision: revision}
}

// prime writes a state with a known key, simulating a completed login.
func (e *env) prime(t *testing.T, password string) {
	t.Helper()
	key, err := DeriveKey(password, vectorSalt, vectorIter)
	if err != nil {
		t.Fatal(err)
	}
	state, _ := LoadState(e.stateDir)
	state.Salt = vectorSalt
	state.Iter = vectorIter
	state.SetKey(key)
	if err := SaveState(e.stateDir, state); err != nil {
		t.Fatal(err)
	}
}

func (e *env) writeGlobal(t *testing.T, content string) {
	t.Helper()
	if err := os.MkdirAll(e.configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(e.configDir, "gocode.json"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func (e *env) readGlobal(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(e.configDir, "gocode.json"))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestPushThenPullRoundTrip(t *testing.T) {
	e := newEnv(t)
	e.prime(t, vectorPassword)
	e.writeGlobal(t, `{"theme":"dark"}`)

	if err := e.manager.Push(context.Background()); err != nil {
		t.Fatalf("push: %v", err)
	}
	if got := e.server.doc(e.deviceID).revision; got != 1 {
		t.Fatalf("server revision after push: %d, want 1", got)
	}

	// A second push with no change is a no-op (no new revision).
	before := e.server.puts
	if err := e.manager.Push(context.Background()); err != nil {
		t.Fatalf("push 2: %v", err)
	}
	if e.server.puts != before {
		t.Errorf("unchanged push contacted the server (%d puts)", e.server.puts-before)
	}

	// Simulate the fresh-machine case: local file gone AND no sync state
	// (a new checkout has never synced), so the server revision is newer
	// than the state's zero and the pull applies.
	if err := os.Remove(filepath.Join(e.configDir, "gocode.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(StatePath(e.stateDir)); err != nil {
		t.Fatal(err)
	}
	e.prime(t, vectorPassword)
	// RestoreOnLogin stamps the revision; a bare Pull from zero state
	// matches the same condition (0 < 1).
	applied, err := e.manager.Pull(context.Background())
	if err != nil || !applied {
		t.Fatalf("pull: applied=%v err=%v", applied, err)
	}
	if got := e.readGlobal(t); got != `{"theme":"dark"}` {
		t.Errorf("pulled config: %q", got)
	}

	// With the state current, a further pull is a no-op (no echo).
	applied, err = e.manager.Pull(context.Background())
	if err != nil || applied {
		t.Errorf("re-pull should be a no-op: applied=%v err=%v", applied, err)
	}
}

func TestPushConflictThenRetry(t *testing.T) {
	e := newEnv(t)
	e.prime(t, vectorPassword)
	e.writeGlobal(t, `{"v":1}`)

	// Another machine (or the website) bumps the revision behind our back.
	other, err := DeriveKey(vectorPassword, vectorSalt, vectorIter)
	if err != nil {
		t.Fatal(err)
	}
	otherBundle := NewBundle()
	otherBundle.Files[KeyGlobal] = `{"v":2,"from":"web"}`
	plaintext, _ := otherBundle.Marshal()
	envl, err := Seal(plaintext, other, vectorSalt, vectorIter)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(envl)
	e.setDoc(e.deviceID, string(encoded), 7)

	// Local push must hit the conflict, then the caller pulls (server wins:
	// it wrote after our base), and a subsequent push is consistent.
	err = e.manager.Push(context.Background())
	if err == nil {
		t.Fatal("stale base revision should conflict")
	}
	var apiErr *gocoder.APIError
	if !errorsAs(err, &apiErr) || apiErr.Status != http.StatusConflict {
		t.Fatalf("expected 409, got %v", err)
	}
	if got := e.server.doc(e.deviceID).conflicts; got != 1 {
		t.Errorf("conflicts counted: %d", got)
	}
	// Reconcile: pull applies the web edit locally.
	if _, err := e.manager.Pull(context.Background()); err != nil {
		t.Fatalf("pull after conflict: %v", err)
	}
	if got := e.readGlobal(t); got != `{"v":2,"from":"web"}` {
		t.Errorf("after reconcile, local should be server copy: %q", got)
	}
}

func TestPullNewerAppliesAndStamps(t *testing.T) {
	e := newEnv(t)
	e.prime(t, vectorPassword)

	key, _ := DeriveKey(vectorPassword, vectorSalt, vectorIter)
	bundle := NewBundle()
	bundle.Files[KeyGlobal] = `{"from":"other-machine"}`
	bundle.Files[KeyTheme] = `{"theme":"gocode-light"}`
	plaintext, _ := bundle.Marshal()
	envl, _ := Seal(plaintext, key, vectorSalt, vectorIter)
	encoded, _ := json.Marshal(envl)
	e.setDoc(e.deviceID, string(encoded), 3)

	applied, err := e.manager.Pull(context.Background())
	if err != nil || !applied {
		t.Fatalf("pull: applied=%v err=%v", applied, err)
	}
	if got := e.readGlobal(t); got != `{"from":"other-machine"}` {
		t.Errorf("global after pull: %q", got)
	}
	theme, err := os.ReadFile(filepath.Join(e.stateDir, "theme.json"))
	if err != nil || string(theme) != `{"theme":"gocode-light"}` {
		t.Errorf("theme after pull: %q %v", theme, err)
	}
	state, _ := LoadState(e.stateDir)
	if state.LastRevision != 3 {
		t.Errorf("state revision: %d, want 3", state.LastRevision)
	}
	if state.NeedsRelogin {
		t.Error("needsRelogin set on a healthy pull")
	}

	// Pull again: revision unchanged, nothing applied.
	applied, err = e.manager.Pull(context.Background())
	if err != nil || applied {
		t.Errorf("re-pull should be a no-op: applied=%v err=%v", applied, err)
	}
}

func TestDecryptFailurePausesNotClobbers(t *testing.T) {
	e := newEnv(t)
	e.prime(t, vectorPassword)

	// Envelope sealed under a DIFFERENT password (changed on the website).
	otherKey, _ := DeriveKey("changed-on-website", vectorSalt, vectorIter)
	bundle := NewBundle()
	bundle.Files[KeyGlobal] = `{"from":"web","evil":true}`
	plaintext, _ := bundle.Marshal()
	envl, _ := Seal(plaintext, otherKey, vectorSalt, vectorIter)
	encoded, _ := json.Marshal(envl)
	e.setDoc(e.deviceID, string(encoded), 9)

	e.writeGlobal(t, `{"local":"precious"}`)
	applied, err := e.manager.Pull(context.Background())
	if err == nil || applied {
		t.Fatalf("undecryptable envelope must fail the pull: applied=%v err=%v", applied, err)
	}
	if got := e.readGlobal(t); got != `{"local":"precious"}` {
		t.Errorf("local config was clobbered: %q", got)
	}
	state, _ := LoadState(e.stateDir)
	if !state.NeedsRelogin {
		t.Error("needsRelogin not set after decrypt failure")
	}
}

func TestDisabledSkipsServer(t *testing.T) {
	e := newEnv(t)
	e.prime(t, vectorPassword)
	e.writeGlobal(t, `{"x":1}`)
	state, _ := LoadState(e.stateDir)
	disabled := false
	state.Enabled = &disabled
	if err := SaveState(e.stateDir, state); err != nil {
		t.Fatal(err)
	}
	if err := e.manager.Push(context.Background()); err != nil {
		t.Fatalf("push while disabled: %v", err)
	}
	if e.server.puts != 0 {
		t.Errorf("disabled push contacted server: %d puts", e.server.puts)
	}
}

func TestRestoreOnLoginFreshAccount(t *testing.T) {
	e := newEnv(t)
	outcome := RestoreOnLogin(context.Background(), e.client, &gocoder.Account{Key: "gk_test"}, vectorPassword, e.manager.Paths, e.stateDir, e.deviceID, os.Stderr)
	if outcome.Restored || outcome.LocalWon {
		t.Errorf("fresh account should restore nothing: %+v", outcome)
	}
	if !outcome.KeyStored {
		t.Error("key not stored for the loops")
	}
	state, _ := LoadState(e.stateDir)
	if state.Salt == "" || state.Key == "" {
		t.Error("state not primed with salt+key")
	}
}

func TestRestoreOnLoginAppliesServerBundle(t *testing.T) {
	e := newEnv(t)
	// Server already holds settings (this is the fresh-machine case).
	key, _ := DeriveKey(vectorPassword, vectorSalt, vectorIter)
	bundle := NewBundle()
	bundle.Files[KeyGlobal] = `{"theme":"restored"}`
	plaintext, _ := bundle.Marshal()
	envl, _ := Seal(plaintext, key, vectorSalt, vectorIter)
	encoded, _ := json.Marshal(envl)
	e.setDoc(e.deviceID, string(encoded), 5)

	outcome := RestoreOnLogin(context.Background(), e.client, &gocoder.Account{Key: "gk_test"}, vectorPassword, e.manager.Paths, e.stateDir, e.deviceID, os.Stderr)
	if !outcome.Restored {
		t.Fatalf("expected restore: %+v", outcome)
	}
	if got := e.readGlobal(t); got != `{"theme":"restored"}` {
		t.Errorf("restored config: %q", got)
	}
	state, _ := LoadState(e.stateDir)
	if state.LastRevision != 5 {
		t.Errorf("state revision after restore: %d, want 5", state.LastRevision)
	}
}

func TestRestoreOnLoginLocalWins(t *testing.T) {
	e := newEnv(t)
	key, _ := DeriveKey(vectorPassword, vectorSalt, vectorIter)
	bundle := NewBundle()
	bundle.Files[KeyGlobal] = `{"theme":"from-server"}`
	plaintext, _ := bundle.Marshal()
	envl, _ := Seal(plaintext, key, vectorSalt, vectorIter)
	encoded, _ := json.Marshal(envl)
	e.setDoc(e.deviceID, string(encoded), 2)

	// This machine already has its own settings: they must survive.
	e.writeGlobal(t, `{"theme":"local-choice"}`)
	outcome := RestoreOnLogin(context.Background(), e.client, &gocoder.Account{Key: "gk_test"}, vectorPassword, e.manager.Paths, e.stateDir, e.deviceID, os.Stderr)
	if !outcome.LocalWon {
		t.Fatalf("expected local-wins: %+v", outcome)
	}
	if got := e.readGlobal(t); got != `{"theme":"local-choice"}` {
		t.Errorf("local config overwritten: %q", got)
	}
	// And the very next push replaces the server copy with local truth.
	if err := e.manager.Push(context.Background()); err != nil {
		t.Fatalf("push after local-wins: %v", err)
	}
	if got := e.readGlobal(t); got != `{"theme":"local-choice"}` {
		t.Errorf("local config mutated by push: %q", got)
	}
}

func TestRestoreUndecryptableKeepsLocalAndStoresKey(t *testing.T) {
	e := newEnv(t)
	otherKey, _ := DeriveKey("different-password", vectorSalt, vectorIter)
	bundle := NewBundle()
	bundle.Files[KeyGlobal] = `{"evil":true}`
	plaintext, _ := bundle.Marshal()
	envl, _ := Seal(plaintext, otherKey, vectorSalt, vectorIter)
	encoded, _ := json.Marshal(envl)
	e.setDoc(e.deviceID, string(encoded), 1)

	e.writeGlobal(t, `{"local":true}`)
	outcome := RestoreOnLogin(context.Background(), e.client, &gocoder.Account{Key: "gk_test"}, vectorPassword, e.manager.Paths, e.stateDir, e.deviceID, os.Stderr)
	if !outcome.LocalWon || outcome.Restored {
		t.Fatalf("expected local-wins on decrypt failure: %+v", outcome)
	}
	if got := e.readGlobal(t); got != `{"local":true}` {
		t.Errorf("local config clobbered: %q", got)
	}
}

func TestPerDeviceDocsDoNotClobber(t *testing.T) {
	e := newEnv(t)
	e.prime(t, vectorPassword)
	e.writeGlobal(t, `{"machine":"a"}`)
	if err := e.manager.Push(context.Background()); err != nil {
		t.Fatalf("device A push: %v", err)
	}

	// A second manager on the same fake server, same account, different
	// deviceID — a different real machine.
	other := &env{server: e.server, client: e.client, configDir: filepath.Join(t.TempDir(), "config"),
		stateDir: filepath.Join(t.TempDir(), "state"), dataDir: filepath.Join(t.TempDir(), "data"), deviceID: "other-device"}
	other.manager = NewManager(other.client, Paths{ConfigDir: other.configDir, StateDir: other.stateDir, DataDir: other.dataDir}, other.stateDir,
		func() string { return "gk_test" }, func() string { return other.deviceID })
	other.prime(t, vectorPassword)
	other.writeGlobal(t, `{"machine":"b"}`)
	if err := other.manager.Push(context.Background()); err != nil {
		t.Fatalf("device B push: %v", err)
	}

	// Each device's own doc is unaffected by the other's push.
	if got := e.server.doc(e.deviceID).envelope; got == e.server.doc(other.deviceID).envelope {
		t.Fatal("device A and device B share one doc; per-device isolation broken")
	}
	applied, err := e.manager.Pull(context.Background())
	if err != nil || applied {
		t.Fatalf("device A must not see device B's push: applied=%v err=%v", applied, err)
	}
	if got := e.readGlobal(t); got != `{"machine":"a"}` {
		t.Errorf("device A config changed by device B's push: %q", got)
	}
}

func TestNormalizeRemoteURL(t *testing.T) {
	cases := map[string]string{
		"https://github.com/a/b.git":  "https://github.com/a/b",
		"https://github.com/a/b":      "https://github.com/a/b",
		"git@github.com:a/b.git":      "https://github.com/a/b",
		"git@github.com:a/b":          "https://github.com/a/b",
		"https://gitlab.com/x/y.git/": "https://gitlab.com/x/y",
		"":                            "",
	}
	for in, want := range cases {
		if got := NormalizeRemoteURL(in); got != want {
			t.Errorf("NormalizeRemoteURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestApplyStagesUnknownProjects(t *testing.T) {
	e := newEnv(t)
	e.prime(t, vectorPassword)
	key, _ := DeriveKey(vectorPassword, vectorSalt, vectorIter)
	bundle := NewBundle()
	bundle.Files[ProjectKey("https://github.com/elsewhere/other", "gocode.json")] = `{"agent":{}}`
	plaintext, _ := bundle.Marshal()
	envl, _ := Seal(plaintext, key, vectorSalt, vectorIter)
	encoded, _ := json.Marshal(envl)
	e.setDoc(e.deviceID, string(encoded), 2)

	applied, err := e.manager.Pull(context.Background())
	if err != nil || !applied {
		t.Fatalf("pull: %v %v", applied, err)
	}
	entries, err := os.ReadDir(e.dataDir + "/sync/projects")
	if err != nil || len(entries) != 1 {
		t.Fatalf("expected one staged project: %v %v", entries, err)
	}
	if strings.HasSuffix(entries[0].Name(), registryFileName) {
		t.Errorf("registry mistaken for a staged config")
	}
}

// errorsAs avoids importing errors twice with different names in this test.
func errorsAs(err error, target *(*gocoder.APIError)) bool {
	if apiErr, ok := err.(*gocoder.APIError); ok {
		*target = apiErr
		return true
	}
	return false
}
