package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/langazov/gocode-go/internal/acp"
	"github.com/langazov/gocode-go/internal/auth"
	"github.com/langazov/gocode-go/internal/clix"
	"github.com/langazov/gocode-go/internal/config"
	"github.com/langazov/gocode-go/internal/db"
	"github.com/langazov/gocode-go/internal/global"
	"github.com/langazov/gocode-go/internal/installation"
	"github.com/langazov/gocode-go/internal/modelsdev"
	"github.com/langazov/gocode-go/internal/modelstate"
	"github.com/langazov/gocode-go/internal/provider"
	"github.com/langazov/gocode-go/internal/server"
	"github.com/langazov/gocode-go/internal/session"
)

// acpCommand mirrors AcpCommand in cli/cmd/acp.ts ("acp"): runs gocode as an
// Agent Client Protocol agent over stdio, for editors (Zed, JetBrains,
// Neovim, ...) that launch it as a subprocess. Protocol versions 1 and 2 are
// both served; see internal/acp.
func acpCommand() *clix.Command {
	flags := append([]clix.Flag{}, networkFlags()...)
	flags = append(flags,
		clix.Flag{Name: "cwd", Kind: clix.KindString, Describe: "working directory for requests that do not name one"},
		clix.Flag{Name: "model", Kind: clix.KindString, Describe: "default model (provider/model); overrides the config \"model\" value"},
		// Terminal authentication: an ACP client relaunches the configured
		// agent command (`gocode acp`) with a method's args appended, so the
		// login flow has to be reachable as an acp flag
		// (agentclientprotocol.com/protocol/v1/authentication).
		clix.Flag{Name: "login", Kind: clix.KindBool, Hidden: true, Describe: "sign in to a model provider interactively, then exit"},
	)
	return &clix.Command{
		Name:     "acp",
		Describe: "start ACP (Agent Client Protocol) server",
		Flags:    flags,
		Run:      runACPCommand,
	}
}

func runACPCommand(a *clix.Args) error {
	if a.Bool("login") {
		// The same interactive flow as `gocode auth login`; exit status is
		// the client's success signal.
		return runProvidersLogin(&clix.Args{
			Bools:    map[string]bool{},
			Strings:  map[string]string{},
			Numbers:  map[string]float64{},
			Arrays:   map[string][]string{},
			Pos:      map[string]string{},
			PosArray: map[string][]string{},
		})
	}
	defaultDir, err := os.Getwd()
	if err != nil {
		return err
	}
	if dir := a.String("cwd"); dir != "" {
		if defaultDir, err = filepath.Abs(dir); err != nil {
			return err
		}
	}

	// stdout belongs to the protocol: every byte on it must be an ACP
	// message (protocol/v1/transports "stdio"). Keep the real stream for the
	// connection and point everything else that prints at stderr, which the
	// client may show or ignore.
	protocolOut := isolateStdout()

	ctx := context.Background()
	database, err := db.OpenDefault(ctx)
	if err != nil {
		return err
	}
	defer database.Close()
	catalog := modelsdev.New()
	catalog.StartBackgroundRefresh(ctx)
	provider.StartOverlayRefresh(ctx)
	if cancel, ok := startSyncLoops(ctx); ok {
		defer cancel()
	}

	modelFlag := a.String("model")
	host := acp.Host{
		Boot: func(ctx context.Context, directory string) (*acp.Runtime, error) {
			s, err := bootStackWith(ctx, bootOptions{
				Directory: directory,
				ModelFlag: modelFlag,
				Database:  database,
				Catalog:   catalog,
			})
			if err != nil {
				return nil, err
			}
			if a.Has("port") && filepath.Clean(directory) == filepath.Clean(defaultDir) {
				serveACPStack(s, networkAddr(a))
			}
			return acpRuntime(s), nil
		},
		Store:            session.NewService(database, nil),
		DefaultDirectory: defaultDir,
		Version:          installation.Version,
		Auth:             &acpAuth{directory: defaultDir, modelFlag: modelFlag},
		LoginArgs:        []string{"--login"},
		Log:              func(format string, args ...any) { global.LogBackground(format, args...) },
	}
	return acp.Serve(ctx, os.Stdin, protocolOut, host)
}

// serveACPStack also serves the HTTP API for the default directory's stack,
// when --port asks for it, so a TUI can attach to the very runtime the editor
// is driving.
func serveACPStack(s *stack, addr string) {
	listener, err := netListen(addr)
	if err != nil {
		global.LogBackground("acp: HTTP API not served on %s: %v", addr, err)
		return
	}
	global.LogBackground("acp: HTTP API on http://%s", listener.Addr())
	go server.ServeOn(listener, s.newServer().Mux())
}

// acpRuntime exposes a booted stack to the ACP agent.
func acpRuntime(s *stack) *acp.Runtime {
	return &acp.Runtime{
		Directory:   s.workdir,
		Sessions:    s.Service,
		Bus:         s.Bus,
		Runner:      s.Runner,
		Permissions: s.PermissionEngine,
		Questions:   s.Questions,
		Agents:      s.Agents,
		Commands:    s.Commands,
		MCP:         s.MCP,
		Models:      newModelLister(s).list,
		// Predictions want a fast model: small_model when configured.
		CompletionModel: completionModel(s.Config),
		FIM:             fimCompleter(s.Config),
		Close:           s.Close,
	}
}

// completionModel is the configured small_model, or zero for the default.
func completionModel(cfg *config.Config) session.ModelRef {
	if cfg == nil {
		return session.ModelRef{}
	}
	if providerID, modelID, ok := config.ParseModelRef(cfg.SmallModel); ok {
		return session.ModelRef{ProviderID: providerID, ID: modelID}
	}
	return session.ModelRef{}
}

// modelLister caches the reachable-model list briefly: the ACP agent reads
// it for every usage update and every config option list, and computing it
// walks the whole catalog.
type modelLister struct {
	stack *stack

	mu      sync.Mutex
	models  []acp.Model
	fetched time.Time
}

func newModelLister(s *stack) *modelLister { return &modelLister{stack: s} }

const modelListTTL = time.Minute

func (l *modelLister) list(ctx context.Context) []acp.Model {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.models != nil && time.Since(l.fetched) < modelListTTL {
		return l.models
	}
	entries, err := server.AvailableModels(ctx, l.stack.Models, l.stack.Config)
	if err != nil {
		global.LogBackground("acp: listing models: %v", err)
		return l.models
	}
	names := map[string]string{}
	if catalog, err := l.stack.Models.Get(ctx); err == nil {
		for id, entry := range catalog {
			names[id] = entry.Name
		}
	}
	models := make([]acp.Model, 0, len(entries))
	for _, entry := range entries {
		models = append(models, acp.Model{
			ProviderID:   entry.ProviderID,
			ProviderName: names[entry.ProviderID],
			ID:           entry.ID,
			Name:         entry.Name,
			ContextLimit: entry.ContextLimit,
			Variants:     entry.Variants,
		})
	}
	l.models, l.fetched = models, time.Now()
	return models
}

// acpAuth answers ACP authentication from gocode's own credential store.
// There is no protocol-driven way to enter a secret, so logging in means
// `gocode auth login`; authenticate only confirms that happened.
type acpAuth struct {
	directory string
	modelFlag string
}

// providerID is the default model's provider for the directory, resolved
// with the same precedence boot uses.
func (a *acpAuth) providerID() (string, *config.Config, error) {
	cfg, err := config.LoadFor(a.directory)
	if err != nil {
		return "", nil, err
	}
	var lastUsed modelstate.Ref
	if ref, ok := modelstate.Load(a.directory); ok {
		lastUsed = ref
	}
	model := resolveModelFlag(a.modelFlag, lastUsed, cfg.Model)
	providerID, _, ok := strings.Cut(model, "/")
	if !ok {
		return "", nil, fmt.Errorf("invalid model %q", model)
	}
	return providerID, cfg, nil
}

func (a *acpAuth) Authenticated(ctx context.Context) bool {
	providerID, cfg, err := a.providerID()
	if err != nil {
		return false
	}
	if _, err := provider.FromConfig(ctx, providerID, cfg); err == nil {
		return true
	}
	// Boot falls back to any provider with credentials; so does this.
	_, _, ok := provider.Fallback(ctx, providerID, "", cfg)
	return ok
}

func (a *acpAuth) Logout(ctx context.Context) error {
	providerID, _, err := a.providerID()
	if err != nil {
		return err
	}
	return auth.Remove(providerID)
}

// netListen binds addr without exiting on failure: a busy --port must not
// take the editor's agent down with it.
func netListen(addr string) (net.Listener, error) {
	return net.Listen("tcp", addr)
}
