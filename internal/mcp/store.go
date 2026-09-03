package mcp

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/langazov/gocode-go/internal/flock"
	"github.com/langazov/gocode-go/internal/global"
)

// Entry mirrors mcp/auth.ts's per-server Entry: OAuth client registration
// plus the current token set. AuthURL/TokenURL aren't part of the TS
// schema (TS's SDK rediscovers them every connect) — this port caches them
// so a stored refresh token can be used to build a working oauth2.Config
// without rediscovery on every process start (see oauth.go's
// initialTokenSource).
type Entry struct {
	ServerURL    string `json:"serverUrl,omitempty"`
	ClientID     string `json:"clientId,omitempty"`
	ClientSecret string `json:"clientSecret,omitempty"`
	AuthURL      string `json:"authUrl,omitempty"`
	TokenURL     string `json:"tokenUrl,omitempty"`
	Scope        string `json:"scope,omitempty"`
	AccessToken  string `json:"accessToken,omitempty"`
	RefreshToken string `json:"refreshToken,omitempty"`
	// Expiry is a Unix timestamp (seconds); 0 means unknown/never expires.
	Expiry int64 `json:"expiry,omitempty"`
}

func (e Entry) HasTokens() bool { return e.AccessToken != "" }

func storePath() string {
	return filepath.Join(global.Resolve().Data, "mcp-auth.json")
}

// storeAll reads the whole mcp-auth.json file, without locking — callers
// that mutate must hold the lock (see withStoreLock).
func storeAll() (map[string]Entry, error) {
	data, err := os.ReadFile(storePath())
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]Entry{}, nil
		}
		return nil, err
	}
	var out map[string]Entry
	if err := json.Unmarshal(data, &out); err != nil {
		return map[string]Entry{}, nil
	}
	if out == nil {
		out = map[string]Entry{}
	}
	return out, nil
}

func storeWrite(data map[string]Entry) error {
	encoded, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(storePath()), 0o755); err != nil {
		return err
	}
	return os.WriteFile(storePath(), encoded, 0o600)
}

// withStoreLock file-locks mcp-auth.json (mirroring mcp/auth.ts's
// EffectFlock-guarded read+mutate) around a read-modify-write cycle.
func withStoreLock(fn func(map[string]Entry) (map[string]Entry, error)) error {
	if err := os.MkdirAll(filepath.Dir(storePath()), 0o755); err != nil {
		return err
	}
	lock, err := flock.Lock(storePath() + ".lock")
	if err != nil {
		return err
	}
	defer lock.Close()
	data, err := storeAll()
	if err != nil {
		return err
	}
	next, err := fn(data)
	if err != nil {
		return err
	}
	if next == nil {
		return nil
	}
	return storeWrite(next)
}

// StoreAll returns every stored MCP server credential entry.
func StoreAll() (map[string]Entry, error) {
	var out map[string]Entry
	err := withStoreLock(func(data map[string]Entry) (map[string]Entry, error) {
		out = data
		return nil, nil
	})
	return out, err
}

// StoreGet returns the stored entry for a server, or (Entry{}, false) if
// none is stored or it was recorded against a different server URL (a
// changed URL invalidates cached credentials, matching getForUrl in
// mcp/auth.ts).
func StoreGet(name, serverURL string) (Entry, bool) {
	all, err := StoreAll()
	if err != nil {
		return Entry{}, false
	}
	entry, ok := all[name]
	if !ok || entry.ServerURL != serverURL {
		return Entry{}, false
	}
	return entry, true
}

// StoreSet persists a server's credential entry.
func StoreSet(name string, entry Entry) error {
	return withStoreLock(func(data map[string]Entry) (map[string]Entry, error) {
		data[name] = entry
		return data, nil
	})
}

// StoreRemove deletes a server's stored credentials.
func StoreRemove(name string) error {
	return withStoreLock(func(data map[string]Entry) (map[string]Entry, error) {
		delete(data, name)
		return data, nil
	})
}
