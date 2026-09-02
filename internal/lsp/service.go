package lsp

import (
	"context"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/anomalyco/opencode-go/internal/config"
	"github.com/anomalyco/opencode-go/internal/global"
)

// Status is one connected server, as the status view and sidebar show it.
type Status struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Root   string `json:"root"`
	Status string `json:"status"`
}

// Service owns the running language servers for a working directory. It ports
// the LSP service in packages/opencode/src/lsp/lsp.ts.
//
// The zero value is not usable; construct with New. A nil *Service is safe to
// call every method on and does nothing, so callers that have LSP disabled do
// not need to branch.
type Service struct {
	directory string
	servers   []Server
	enabled   bool

	mu      sync.Mutex
	clients map[string]*Client // keyed by root + "\x00" + serverID
	// broken records servers that failed to start, so a doomed spawn is
	// attempted once per root rather than on every file touch.
	broken map[string]bool
	// spawning deduplicates concurrent spawns of the same server: several
	// tools touching files at once must not race to start N copies.
	spawning map[string]*sync.WaitGroup
	closed   bool
}

// New builds the service from config, porting the server assembly in
// LSP.state: all built-ins unless `lsp` is false, then per-server config
// overrides and disables.
func New(directory string, cfg *config.Config) *Service {
	service := &Service{
		directory: normalizePath(directory),
		clients:   map[string]*Client{},
		broken:    map[string]bool{},
		spawning:  map[string]*sync.WaitGroup{},
		enabled:   true,
	}

	if cfg != nil && cfg.LSP.Disabled() {
		service.enabled = false
		global.LogBackground("lsp: all language servers are disabled by config")
		return service
	}

	byID := map[string]Server{}
	order := []string{}
	for _, server := range builtinServers {
		byID[server.ID] = server
		order = append(order, server.ID)
	}

	if cfg != nil {
		for id, entry := range cfg.LSP.Servers() {
			existing, known := byID[id]
			if entry.Disabled {
				delete(byID, id)
				continue
			}
			if !known {
				existing = Server{ID: id}
				order = append(order, id)
			}
			if len(entry.Command) > 0 {
				existing.Command = entry.Command
			}
			if len(entry.Extensions) > 0 {
				existing.Extensions = entry.Extensions
			}
			if len(entry.Env) > 0 {
				existing.Env = entry.Env
			}
			if len(entry.Initialization) > 0 {
				existing.Initialization = entry.Initialization
			}
			// A configured server names its own command, so it is not held to
			// the built-in root markers: the working directory is the root
			// unless it inherited markers from a built-in of the same name.
			byID[id] = existing
		}
	}

	for _, id := range order {
		if server, ok := byID[id]; ok {
			service.servers = append(service.servers, server)
		}
	}
	return service
}

// Enabled reports whether any server may run.
func (s *Service) Enabled() bool { return s != nil && s.enabled }

// key identifies a client by root and server.
func key(root, serverID string) string { return root + "\x00" + serverID }

// clientsFor returns the running servers that handle a file, starting any that
// are not running yet.
func (s *Service) clientsFor(ctx context.Context, file string) []*Client {
	if s == nil || !s.enabled {
		return nil
	}
	file = normalizePath(file)
	// A file outside the working directory is not this instance's business,
	// porting the containsPath guard in getClients.
	if !containsPath(s.directory, file) {
		return nil
	}

	var out []*Client
	for _, server := range s.servers {
		if !server.Handles(file) {
			continue
		}
		root, ok := server.Root(file, s.directory)
		if !ok {
			continue
		}
		if client := s.ensureClient(ctx, server, root); client != nil {
			out = append(out, client)
		}
	}
	return out
}

// ensureClient returns the running client for a server/root, spawning it if
// needed and remembering failures.
func (s *Service) ensureClient(ctx context.Context, server Server, root string) *Client {
	id := key(root, server.ID)

	for {
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			return nil
		}
		if client, ok := s.clients[id]; ok {
			s.mu.Unlock()
			return client
		}
		if s.broken[id] {
			s.mu.Unlock()
			return nil
		}
		if wait, ok := s.spawning[id]; ok {
			// Someone else is starting it; wait and re-check rather than
			// starting a second copy.
			s.mu.Unlock()
			wait.Wait()
			continue
		}
		wait := &sync.WaitGroup{}
		wait.Add(1)
		s.spawning[id] = wait
		s.mu.Unlock()

		client := s.spawn(ctx, server, root)

		s.mu.Lock()
		delete(s.spawning, id)
		if client == nil {
			s.broken[id] = true
		} else if s.closed {
			s.mu.Unlock()
			wait.Done()
			client.Close()
			return nil
		} else {
			s.clients[id] = client
		}
		s.mu.Unlock()
		wait.Done()
		return client
	}
}

func (s *Service) spawn(ctx context.Context, server Server, root string) *Client {
	if !server.Available() {
		// Not an error worth surfacing: most projects have most servers
		// uninstalled, and the registry is deliberately broad.
		return nil
	}
	client, err := Spawn(ctx, server.ID, root, server.Command, server.Env, server.Initialization)
	if err != nil {
		global.LogBackground("lsp: %s failed to start in %s: %v", server.ID, root, err)
		return nil
	}
	global.LogBackground("lsp: %s started in %s", server.ID, root)
	return client
}

// Touch tells every server that handles the file to reload it. With wait set,
// it also waits for diagnostics — what the edit and write tools want, so the
// problems they caused are reported in the same turn.
func (s *Service) Touch(ctx context.Context, file string, wait bool) {
	clients := s.clientsFor(ctx, file)
	if len(clients) == 0 {
		return
	}
	var group sync.WaitGroup
	for _, client := range clients {
		client := client
		group.Add(1)
		go func() {
			defer group.Done()
			// Snapshot before notifying: a server can publish before Open even
			// returns, and that publish is the one being waited for.
			seq := client.PublishSeq(file)
			changed, err := client.Open(file)
			if err != nil {
				return
			}
			// Nothing was sent, so nothing new will be published and the
			// diagnostics already held are current.
			if wait && changed {
				client.WaitForDiagnostics(ctx, file, seq)
			}
		}()
	}
	group.Wait()
}

// Diagnostics returns everything every running server has published, keyed by
// absolute path.
func (s *Service) Diagnostics() map[string][]Diagnostic {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	clients := make([]*Client, 0, len(s.clients))
	for _, client := range s.clients {
		clients = append(clients, client)
	}
	s.mu.Unlock()

	out := map[string][]Diagnostic{}
	for _, client := range clients {
		for path, items := range client.Diagnostics() {
			out[path] = append(out[path], items...)
		}
	}
	return out
}

// DiagnosticsFor returns the diagnostics for one file across every server.
func (s *Service) DiagnosticsFor(file string) []Diagnostic {
	if s == nil {
		return nil
	}
	file = normalizePath(file)
	s.mu.Lock()
	clients := make([]*Client, 0, len(s.clients))
	for _, client := range s.clients {
		clients = append(clients, client)
	}
	s.mu.Unlock()

	var out []Diagnostic
	for _, client := range clients {
		out = append(out, client.DiagnosticsFor(file)...)
	}
	return dedupe(out)
}

// dedupe drops repeats, which happen when two servers cover one language
// (pyright and ruff both on .py). Ported from dedupeDiagnostics in client.ts.
func dedupe(items []Diagnostic) []Diagnostic {
	seen := map[string]bool{}
	out := make([]Diagnostic, 0, len(items))
	for _, item := range items {
		id := item.Message + "\x00" + item.Source + "\x00" +
			itoa(item.Range.Start.Line) + ":" + itoa(item.Range.Start.Character) + "\x00" + itoa(item.Severity)
		if seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, item)
	}
	return out
}

func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	negative := value < 0
	if negative {
		value = -value
	}
	var digits []byte
	for value > 0 {
		digits = append([]byte{byte('0' + value%10)}, digits...)
		value /= 10
	}
	if negative {
		return "-" + string(digits)
	}
	return string(digits)
}

// Status lists the connected servers, for the sidebar and status view.
func (s *Service) Status() []Status {
	if s == nil || !s.enabled {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	out := make([]Status, 0, len(s.clients))
	for _, client := range s.clients {
		root, err := filepath.Rel(s.directory, client.Root)
		if err != nil || root == "" {
			root = "."
		}
		out = append(out, Status{
			ID:     client.ServerID,
			Name:   client.ServerID,
			Root:   root,
			Status: "connected",
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Available lists the servers that could start here: installed, and matching
// something in the tree. Used to tell "no server is installed" apart from "no
// file has been read yet", which the sidebar needs to word its message.
func (s *Service) Available() []string {
	if s == nil || !s.enabled {
		return nil
	}
	var out []string
	for _, server := range s.servers {
		if server.Available() {
			out = append(out, server.ID)
		}
	}
	sort.Strings(out)
	return out
}

// Shutdown stops every running server.
func (s *Service) Shutdown() {
	if s == nil {
		return
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	clients := make([]*Client, 0, len(s.clients))
	for _, client := range s.clients {
		clients = append(clients, client)
	}
	s.clients = map[string]*Client{}
	s.mu.Unlock()

	var group sync.WaitGroup
	for _, client := range clients {
		client := client
		group.Add(1)
		go func() {
			defer group.Done()
			client.Close()
		}()
	}
	group.Wait()
}

// containsPath reports whether file sits inside directory.
func containsPath(directory, file string) bool {
	rel, err := filepath.Rel(directory, file)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
