package modelsdev

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/anomalyco/opencode-go/internal/flag"
	"github.com/anomalyco/opencode-go/internal/flock"
	"github.com/anomalyco/opencode-go/internal/global"
)

const (
	defaultSource = "https://models.opencode.ai"
	ttl           = 5 * time.Minute
)

var ErrDisabled = errors.New("modelsdev: fetch disabled")

type Service struct {
	Source    string
	Filepath  string
	UserAgent string

	client *http.Client

	mu     sync.Mutex
	cached Catalog
	loaded bool
}

// NewWithCatalog returns a service pre-populated with a fixed catalog and no
// source to fetch from, for callers (and tests) that already have one.
func NewWithCatalog(catalog Catalog) *Service {
	return &Service{cached: catalog, loaded: true}
}

func New() *Service {
	source := flag.ModelsUrl()
	if source == "" {
		source = defaultSource
	}
	name := "models.json"
	if source != defaultSource {
		sum := sha1.Sum([]byte(source))
		name = "models-" + hex.EncodeToString(sum[:]) + ".json"
	}
	path := flag.ModelsPath()
	if path == "" {
		path = filepath.Join(global.Resolve().Cache, name)
	}
	return &Service{
		Source:    source,
		Filepath:  path,
		UserAgent: "opencode/dev/" + flag.Client(),
		client:    &http.Client{Timeout: 10 * time.Second},
	}
}

// Get returns the catalog, populating it on first call. The result is cached
// for the process lifetime, matching cachedGet in the TypeScript service.
func (s *Service) Get(ctx context.Context) (Catalog, error) {
	s.mu.Lock()
	if s.loaded {
		catalog := s.cached
		s.mu.Unlock()
		return catalog, nil
	}
	s.mu.Unlock()

	catalog, err := s.populate(ctx)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.cached = catalog
	s.loaded = true
	s.mu.Unlock()
	return catalog, nil
}

// populate resolves the catalog in the same order as populate() in
// packages/core/src/models-dev.ts: disk cache, then the build-time snapshot
// compiled into the binary, then a live fetch.
func (s *Service) populate(ctx context.Context) (Catalog, error) {
	if catalog, ok := s.loadFromDisk(); ok {
		return catalog, nil
	}
	if flag.DisableModelsFetch() {
		// Explicitly offline still means "stale catalog", not "no catalog":
		// without the snapshot there would be no model list at all.
		return snapshotOrEmpty(), nil
	}
	if err := os.MkdirAll(filepath.Dir(s.Filepath), 0o755); err != nil {
		return nil, err
	}
	lock, err := flock.Lock(s.Filepath + ".lock")
	if err != nil {
		return nil, err
	}
	defer lock.Close()
	// Re-check under the lock: another process may have populated the cache.
	if catalog, ok := s.loadFromDisk(); ok {
		return catalog, nil
	}
	text, err := s.fetchAndWrite(ctx)
	if err != nil {
		// Offline or unreachable source: fall back to the embedded snapshot so
		// the app still boots with a usable model list, matching the
		// resilience of the TypeScript background refresh.
		//
		// Not stderr: Get() is reached from HTTP handlers, so this can fire
		// while the TUI owns the terminal, where a stray write is painted on
		// top of the rendered frame. See internal/global/diag.go.
		global.LogBackground("modelsdev: fetch failed, using embedded snapshot: %v", err)
		return snapshotOrEmpty(), nil
	}
	return decode(text)
}

// snapshotOrEmpty degrades to an empty catalog if even the embedded snapshot
// fails to decode — a corrupt build artifact must not stop the app booting,
// since keys still resolve via env vars and auth.json without a catalog.
func snapshotOrEmpty() Catalog {
	catalog, err := Snapshot()
	if err != nil {
		global.LogBackground("modelsdev: embedded snapshot unusable: %v", err)
		return Catalog{}
	}
	return catalog
}

func (s *Service) loadFromDisk() (Catalog, bool) {
	data, err := os.ReadFile(s.Filepath)
	if err != nil {
		return nil, false
	}
	catalog, err := decode(string(data))
	if err != nil {
		// Corrupt cache: drop the file unless the path was explicitly provided.
		if flag.ModelsPath() == "" {
			os.Remove(s.Filepath)
		}
		return nil, false
	}
	return catalog, true
}

func (s *Service) fetchAndWrite(ctx context.Context) (string, error) {
	text, err := s.fetch(ctx)
	if err != nil {
		return "", err
	}
	temp := fmt.Sprintf("%s.%d.%d.tmp", s.Filepath, os.Getpid(), time.Now().UnixNano())
	if err := os.MkdirAll(filepath.Dir(s.Filepath), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(temp, []byte(text), 0o644); err != nil {
		return "", err
	}
	if err := os.Rename(temp, s.Filepath); err != nil {
		os.Remove(temp)
		return "", err
	}
	return text, nil
}

func (s *Service) fetch(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.Source+"/api.json", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", s.UserAgent)
	res, err := s.client.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return "", fmt.Errorf("modelsdev: fetch %s/api.json: status %d", s.Source, res.StatusCode)
	}
	body, err := io.ReadAll(res.Body)
	if err != nil {
		return "", err
	}
	return string(body), nil
}

// Refresh re-fetches the catalog when the cache file is older than the TTL.
func (s *Service) Refresh(ctx context.Context, force bool) error {
	if !force && s.fresh() {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(s.Filepath), 0o755); err != nil {
		return err
	}
	lock, err := flock.Lock(s.Filepath + ".lock")
	if err != nil {
		return err
	}
	defer lock.Close()
	if !force && s.fresh() {
		return nil
	}
	if _, err := s.fetchAndWrite(ctx); err != nil {
		return err
	}
	s.mu.Lock()
	s.loaded = false
	s.mu.Unlock()
	return nil
}

func (s *Service) fresh() bool {
	info, err := os.Stat(s.Filepath)
	if err != nil {
		return false
	}
	return time.Since(info.ModTime()) < ttl
}

// StartBackgroundRefresh mirrors the TypeScript 60-minute refresh loop.
func (s *Service) StartBackgroundRefresh(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(60 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := s.Refresh(ctx, false); err != nil && !errors.Is(err, context.Canceled) {
					global.LogBackground("modelsdev: background refresh failed: %v", err)
				}
			}
		}
	}()
}

func decode(text string) (Catalog, error) {
	var catalog Catalog
	if err := json.Unmarshal([]byte(text), &catalog); err != nil {
		return nil, fmt.Errorf("modelsdev: decode: %w", err)
	}
	return catalog, nil
}
