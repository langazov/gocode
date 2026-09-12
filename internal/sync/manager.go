package sync

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/langazov/gocode-go/internal/gocoder"
)

// DeviceName is the label written to the doc so the website can show which
// machine last saved. Overridable in tests.
var DeviceName = defaultDeviceName

func defaultDeviceName() string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		return "gocode"
	}
	return host
}

// Remote is the slice of gocoder.org the manager talks to. It is an
// interface so tests can run against an httptest server through the real
// client, and so the manager never constructs HTTP itself.
type Remote interface {
	GetSettings(ctx context.Context, bearer string) (*gocoder.SettingsDoc, error)
	PutSettings(ctx context.Context, bearer, envelope string, baseRevision int64, device string) (*gocoder.SettingsDoc, error)
	SettingsRevision(ctx context.Context, bearer string) (int64, error)
}

// Manager owns the sync state machine: collect local files, apply remote
// ones, and keep State describing the last agreed point.
type Manager struct {
	Remote   Remote
	Paths    Paths
	StateDir string
	Bearer   func() string // API key, or "" when signed out
	Now      func() time.Time
}

// NewManager builds a manager. bearer may be nil (sync disabled).
func NewManager(remote Remote, paths Paths, stateDir string, bearer func() string) *Manager {
	if bearer == nil {
		bearer = func() string { return "" }
	}
	return &Manager{Remote: remote, Paths: paths, StateDir: stateDir, Bearer: bearer, Now: time.Now}
}

func hashContent(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

// key is the derived key, stored base64 in State.
func (s *State) decodedKey() (Key, error) {
	if s.Key == "" {
		return Key{}, ErrNoKey
	}
	raw, err := base64.StdEncoding.DecodeString(s.Key)
	if err != nil || len(raw) != 32 {
		return Key{}, fmt.Errorf("sync: stored key is invalid; sign in again")
	}
	var key Key
	copy(key[:], raw)
	return key, nil
}

// StoreKey records a derived key in state (base64).
func (s *State) SetKey(key Key) {
	s.Key = base64.StdEncoding.EncodeToString(key[:])
}

// DecodeStoredKey exposes the stored key for the loops (exported for the
// login path, which derives and stores it).
func (s *State) DecodeStoredKey() (Key, error) { return s.decodedKey() }

// ---- collect / apply ----

// Collect reads the local settings files into a bundle. Missing files are
// simply absent from the bundle; the global config is included even when it
// does not exist (as an empty string) so a fresh machine still registers a
// doc on first push.
func (m *Manager) Collect() (*Bundle, error) {
	bundle := NewBundle()

	if path, found := m.Paths.GlobalConfigPath(); found {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		bundle.Files[KeyGlobal] = string(data)
	}

	if data, err := os.ReadFile(m.Paths.ThemePath()); err == nil {
		bundle.Files[KeyTheme] = string(data)
	}

	return bundle, nil
}

// Apply writes a received bundle's files to disk. It returns the keys it
// wrote. Project entries for projects with no local checkout are staged,
// not written (see StageProject), and reported separately.
func (m *Manager) Apply(bundle *Bundle) (written, staged []string, err error) {
	keys := make([]string, 0, len(bundle.Files))
	for key := range bundle.Files {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	for _, key := range keys {
		content := bundle.Files[key]
		switch {
		case key == KeyGlobal:
			path, _ := m.Paths.GlobalConfigPath()
			if err := writeFileAtomic(path, content); err != nil {
				return written, staged, fmt.Errorf("write global config: %w", err)
			}
			written = append(written, key)
		case key == KeyTheme:
			if err := writeFileAtomic(m.Paths.ThemePath(), content); err != nil {
				return written, staged, fmt.Errorf("write theme state: %w", err)
			}
			written = append(written, key)
		default:
			remoteURL, relPath, ok := ParseProjectKey(key)
			if !ok {
				continue // unknown key from a newer client: ignore, never drop
			}
			target := m.localProjectPath(remoteURL, relPath)
			if target != "" {
				if err := writeFileAtomic(target, content); err != nil {
					return written, staged, fmt.Errorf("write project config: %w", err)
				}
				written = append(written, key)
			} else if err := m.stageProject(remoteURL, relPath, content); err != nil {
				return written, staged, err
			} else {
				staged = append(staged, key)
			}
		}
	}
	return written, staged, nil
}

// localProjectPath finds where a remote's project config lives on this
// machine, or "" when the project is not checked out here. It scans the
// known-projects registry (see RegisterProject) rather than the disk, so
// lookups stay cheap and testable.
func (m *Manager) localProjectPath(remoteURL, relPath string) string {
	registry, err := LoadProjectRegistry(m.Paths)
	if err != nil {
		return ""
	}
	for _, entry := range registry.Projects {
		if entry.Remote == remoteURL {
			return filepath.Join(entry.Path, filepath.FromSlash(relPath))
		}
	}
	return ""
}

// stageProject parks a project config for a project not yet cloned here.
// It is applied the first time that project is opened locally (see
// ApplyStaged), and never overwrites an existing file.
func (m *Manager) stageProject(remoteURL, relPath, content string) error {
	dir := m.Paths.ProjectStagingDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	name := stagingName(remoteURL, relPath)
	return os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644)
}

// stagingName is a filesystem-safe, collision-free name for a staged file.
func stagingName(remoteURL, relPath string) string {
	sum := sha256.Sum256([]byte(remoteURL + "|" + relPath))
	return hex.EncodeToString(sum[:])[:24] + ".json"
}

// ---- push / pull ----

// Push seals the local bundle and uploads it. The optimistic lock uses the
// state's last revision; on conflict the caller is expected to Pull and
// decide (last writer to contact the server wins).
func (m *Manager) Push(ctx context.Context) error {
	state, err := LoadState(m.StateDir)
	if err != nil {
		return err
	}
	if !state.IsEnabled() {
		return nil
	}
	key, err := state.decodedKey()
	if err != nil {
		state.NeedsRelogin = true
		_ = SaveState(m.StateDir, state)
		return err
	}

	bundle, err := m.Collect()
	if err != nil {
		return err
	}
	plaintext, err := bundle.Marshal()
	if err != nil {
		return err
	}
	salt := state.Salt
	if salt == "" {
		salt = NewSalt()
		state.Salt = salt
		state.Iter = DefaultIterations
	}
	iter := state.Iter
	if iter == 0 {
		iter = DefaultIterations
		state.Iter = iter
	}
	if hashesEqual(bundleHashes(bundle), state.Hashes) && state.LastRevision > 0 {
		return nil // nothing changed since last sync
	}
	envelope, err := Seal(plaintext, key, salt, iter)
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(envelope)
	if err != nil {
		return err
	}
	// The optimistic lock's base: -1 means "no doc yet", which is the
	// truth whenever this machine has never synced (LastRevision 0 and
	// nothing stored). Using 0 would read as "I saw revision 0" and
	// conflict against a doc that exists.
	base := state.LastRevision
	if base == 0 {
		base = -1
	}
	doc, err := m.Remote.PutSettings(ctx, m.Bearer(), string(encoded), base, DeviceName())
	if err != nil {
		return err
	}
	state.LastRevision = doc.Revision
	state.LastSyncAt = m.Now()
	state.Hashes = bundleHashes(bundle)
	state.NeedsRelogin = false
	return SaveState(m.StateDir, state)
}

// Pull fetches and applies the server bundle when its revision is newer
// than the last synced one. It returns whether anything was applied.
func (m *Manager) Pull(ctx context.Context) (bool, error) {
	state, err := LoadState(m.StateDir)
	if err != nil {
		return false, err
	}
	if !state.IsEnabled() {
		return false, nil
	}
	doc, err := m.Remote.GetSettings(ctx, m.Bearer())
	if err != nil {
		if err == gocoder.ErrNoSettings {
			return false, nil
		}
		return false, err
	}
	if doc.Revision <= state.LastRevision {
		return false, nil
	}
	key, err := state.decodedKey()
	if err != nil {
		state.NeedsRelogin = true
		_ = SaveState(m.StateDir, state)
		return false, err
	}
	var envelope Envelope
	if err := json.Unmarshal([]byte(doc.Envelope), &envelope); err != nil {
		return false, fmt.Errorf("sync: server envelope is not valid JSON: %w", err)
	}
	plaintext, err := Open(&envelope, key)
	if err != nil {
		state.NeedsRelogin = true
		_ = SaveState(m.StateDir, state)
		return false, err
	}
	bundle, err := ParseBundle(plaintext)
	if err != nil {
		return false, err
	}
	_, _, err = m.Apply(bundle)
	if err != nil {
		return false, err
	}
	state.LastRevision = doc.Revision
	state.LastSyncAt = m.Now()
	state.Hashes = bundleHashes(bundle)
	state.Salt = envelope.Salt
	state.Iter = envelope.Iter
	return true, SaveState(m.StateDir, state)
}

func bundleHashes(bundle *Bundle) map[string]string {
	hashes := make(map[string]string, len(bundle.Files))
	for key, content := range bundle.Files {
		hashes[key] = hashContent(content)
	}
	return hashes
}

// hashesEqual reports whether two file-hash maps describe identical content.
func hashesEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for key, value := range a {
		if b[key] != value {
			return false
		}
	}
	return true
}
