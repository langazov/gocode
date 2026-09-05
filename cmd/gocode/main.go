package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/langazov/gocode-go/internal/agent"
	"github.com/langazov/gocode-go/internal/background"
	"github.com/langazov/gocode-go/internal/command"
	"github.com/langazov/gocode-go/internal/config"
	"github.com/langazov/gocode-go/internal/db"
	"github.com/langazov/gocode-go/internal/event"
	"github.com/langazov/gocode-go/internal/flag"
	"github.com/langazov/gocode-go/internal/global"
	"github.com/langazov/gocode-go/internal/installation"
	"github.com/langazov/gocode-go/internal/llm"
	"github.com/langazov/gocode-go/internal/lsp"
	"github.com/langazov/gocode-go/internal/mcp"
	"github.com/langazov/gocode-go/internal/memory"
	// Registers the memory plugin with the native tier. Imported for effect:
	// the plugin registry is populated from init, so the boot wiring's job is
	// only to make sure the package is linked in. See internal/plugin/native.go.
	_ "github.com/langazov/gocode-go/internal/memoryplugin"
	"github.com/langazov/gocode-go/internal/modelsdev"
	"github.com/langazov/gocode-go/internal/modelstate"
	"github.com/langazov/gocode-go/internal/permission"
	"github.com/langazov/gocode-go/internal/plugin"
	"github.com/langazov/gocode-go/internal/provider"
	"github.com/langazov/gocode-go/internal/question"
	"github.com/langazov/gocode-go/internal/server"
	"github.com/langazov/gocode-go/internal/session"
	"github.com/langazov/gocode-go/internal/skill"
	"github.com/langazov/gocode-go/internal/tool"
	"github.com/langazov/gocode-go/internal/tool/builtins"
)

func main() {
	os.Exit(runMain(os.Args[1:]))
}

// defaultContextLimit budgets compaction until per-model limits are resolved
// from the catalog.
const defaultContextLimit = 200000

// lazyProvider resolves the stream client per request's provider, so model
// switches (session model, dialog picks) always hit the right endpoint with
// the right credentials. Resolution is lazy and cached per provider.
type lazyProvider struct {
	config *config.Config

	mu      sync.Mutex
	clients map[string]llm.StreamClient
	errs    map[string]error
}

func newLazyProvider(cfg *config.Config) *lazyProvider {
	return &lazyProvider{
		config:  cfg,
		clients: map[string]llm.StreamClient{},
		errs:    map[string]error{},
	}
}

func (l *lazyProvider) Stream(ctx context.Context, request llm.Request, emit func(llm.StreamEvent)) error {
	client, err := l.forProvider(request.ProviderID)
	if err != nil {
		err = fmt.Errorf("provider %q unavailable: %w (set provider.%q.options.apiKey in gocode.json, or the provider's API key env var)",
			request.ProviderID, err, request.ProviderID)
		emit(llm.StreamEvent{Type: llm.EventProviderError, Error: err})
		return err
	}
	return client.Stream(ctx, request, emit)
}

func (l *lazyProvider) forProvider(providerID string) (llm.StreamClient, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if client, ok := l.clients[providerID]; ok {
		return client, l.errs[providerID]
	}
	client, err := resolveProvider(context.Background(), providerID, l.config)
	l.clients[providerID] = client
	l.errs[providerID] = err
	return client, err
}

type stack struct {
	Service     *session.Service
	Bus         *event.Bus
	Permissions *permission.Engine
	Models      *modelsdev.Service
	Agents      *agent.Registry
	Config      *config.Config
	ProviderID  string
	ModelID     string
	// Runner is the shared runner backing Service.Execution. Exposed so CLI
	// flags like --auto/--yolo/--dangerously-skip-permissions can bypass the
	// permission gate for a single invocation.
	Runner *session.Runner
	// MCP holds the connected MCP servers backing this stack's tool
	// registry; exposed so CLI commands (mcp list/auth/logout) and the TUI
	// status dialog can query live status.
	MCP *mcp.Service
	// Jobs tracks detached background subagents. nil unless background
	// subagents are enabled.
	Jobs *background.Registry
	// Skills holds the discovered skills backing the skill tool and the
	// available-skills prompt block.
	Skills *skill.Registry
	// Questions owns the pending ask/reply rounds from the question tool and
	// plan mode.
	Questions *question.Service
	// LSP owns the running language servers backing edit/write diagnostics and
	// the status view. Servers start lazily on the first file that needs one.
	LSP *lsp.Service
	// Commands holds the slash commands: built-ins, config entries, markdown
	// definitions and skills.
	Commands *command.Registry
	// Database is the connection bootStack opened. The running CLI leaves it
	// open for the process lifetime, but tests that boot multiple stacks
	// must close it themselves — on Windows an open sqlite handle blocks
	// t.TempDir's cleanup from deleting the file.
	Database *db.DB
	// Plugins holds the loaded plugin host backing the runner's hook seams
	// and any tools plugins contributed.
	Plugins *plugin.Host
	// Memory holds durable memories, backing the interface's /memory manager.
	// The agent reaches the same store through the memory plugin's tools.
	Memory *memory.Store
	// ProjectID is the project this stack booted in, resolved once so the
	// memory routes and the memory plugin agree on what "this project" means.
	ProjectID string
}

// newServer builds the HTTP server this stack backs.
//
// Every subcommand that serves the API — tui, serve, run, web — needs the
// identical field list, and it had been copied four times. That is a quiet
// hazard rather than a style problem: a service wired at three of the four
// sites yields a feature that works under `gocode tui` and 404s under
// `gocode serve`, with nothing failing to compile to say so.
func (s *stack) newServer() *server.Server {
	return &server.Server{
		Session:     s.Service,
		Bus:         s.Bus,
		Permissions: s.Permissions,
		Models:      s.Models,
		Agents:      s.Agents,
		Config:      s.Config,
		MCP:         s.MCP,
		Jobs:        s.Jobs,
		Questions:   s.Questions,
		Skills:      s.Skills,
		LSP:         s.LSP,
		Commands:    s.Commands,
		Plugins:     s.Plugins,
		Memory:      s.Memory,
		ProjectID:   s.ProjectID,
	}
}

// Close releases the resources bootStack opened. Only tests need this: a
// running command exits the process instead, which reclaims everything —
// except a process plugin, which is a child the OS does not reap for us, so
// the host is shut down here regardless.
func (s *stack) Close() error {
	if s.Plugins != nil {
		_ = s.Plugins.Close(context.Background())
	}
	return s.Database.Close()
}

// resolveModelFlag applies precedence: explicit flag wins, then config,
// then the built-in default.
// resolveModelFlag applies the model precedence: explicit flag > last-used
// model (persisted per directory) > config "model" > built-in default.
func resolveModelFlag(modelFlag string, lastUsed modelstate.Ref, configModel string) string {
	if modelFlag != "" {
		return modelFlag
	}
	if lastUsed.ProviderID != "" && lastUsed.ModelID != "" {
		return lastUsed.ProviderID + "/" + lastUsed.ModelID
	}
	if configModel != "" {
		return configModel
	}
	return "anthropic/claude-sonnet-4-5"
}

// bootStack builds the full runtime (database, event bus, runner, tools,
// agents, permissions), shared by serve and tui. Precedence for the default
// model: explicit flag > config "model" > built-in default.
func bootStack(ctx context.Context, modelFlag string) (*stack, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	database, err := db.OpenDefault(ctx)
	if err != nil {
		return nil, err
	}
	cwd, _ := os.Getwd()

	// The project row has to exist before the plugins load: a native plugin
	// scoped to the project (memory, for one) is handed the id in Services and
	// has no way to create it itself. A failure here is not fatal — the
	// plugins that want it opt out, and everything else boots normally.
	projectID, err := session.EnsureProject(ctx, database, cwd)
	if err != nil {
		global.LogBackground("resolving project for %s: %v", cwd, err)
	}

	// Plugins load before anything is built from the config, because the
	// config hook lets them change what gets built — including the default
	// model resolved just below. Ports the ordering in
	// packages/opencode/src/plugin/index.ts, where the host is constructed
	// from the config and then immediately hands it back for mutation.
	plugins, err := plugin.Load(ctx, plugin.LoadInput{
		Input: plugin.Input{
			Directory: cwd,
			Worktree:  cwd,
			Version:   installation.Version,
			// Native plugins run on this heap, so they get the live handles
			// rather than a second connection to the same database. Never
			// serialized; see plugin.Input.Services.
			Services: plugin.Services{DB: database, ProjectID: projectID},
		},
		Specs: plugin.Specs(cfg.Plugin),
		Pure:  flag.Pure(),
		Report: &plugin.Report{
			Error: func(spec plugin.Spec, stage plugin.Stage, err error) {
				global.LogBackground("plugin %s failed at %s: %v", spec.Ref, stage, err)
			},
		},
		Log: func(message string) { global.LogBackground("%s", message) },
	})
	if err != nil {
		return nil, err
	}
	if err := plugin.ApplyConfig(ctx, plugins, cfg); err != nil {
		global.LogBackground("plugin config hook failed: %v", err)
	}

	var lastUsed modelstate.Ref
	if ref, ok := modelstate.Load(cwd); ok {
		lastUsed = ref
	}
	modelFlag = resolveModelFlag(modelFlag, lastUsed, cfg.Model)
	if err != nil {
		return nil, err
	}
	bus := event.NewBus(database)
	session.RegisterProjectors(bus)
	session.RegisterRunnerProjectors(bus)
	// Plugins see every committed event, porting the bus listener the
	// TypeScript host installs. Delivery is fire-and-forget on purpose: a
	// listener runs after the commit, and a slow plugin must not hold up the
	// next one.
	if len(plugins.Instances()) > 0 {
		bus.Listen(func(payload event.Payload) {
			plugin.Notify(ctx, plugins, plugin.Event{
				ID:         payload.ID,
				Type:       payload.Type,
				Properties: payload.Data,
			})
		})
	}

	providerID, modelID, ok := strings.Cut(modelFlag, "/")
	if !ok {
		return nil, fmt.Errorf("invalid model %q: expected provider/model", modelFlag)
	}
	// Mirror the original's autoload semantics: when the configured provider
	// has no credentials, fall back to one that does (auth entries first,
	// preferring the same provider family).
	if _, err := provider.FromConfig(ctx, providerID, cfg); err != nil {
		if fbProvider, fbModel, ok := provider.Fallback(ctx, providerID, modelID, cfg); ok {
			fmt.Fprintf(os.Stderr,
				"notice: model %s unavailable (no credentials), using %s/%s\n",
				providerID+"/"+modelID, fbProvider, fbModel)
			providerID, modelID = fbProvider, fbModel
		}
	}
	streamClient := newLazyProvider(cfg)
	catalog := modelsdev.New()

	workdir, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	// Skills are discovered from the project and the user's global config,
	// project first so a local skill overrides a global one of the same
	// name. .agents is a cross-tool convention other agent CLIs also write
	// skills into (see packages/opencode/src/skill/index.ts's external-dirs
	// scan); gocode's own dir is listed first at each scope so a same-named
	// gocode-specific skill still wins over a shared one.
	skills := skill.Discover(
		filepath.Join(workdir, ".gocode"),
		filepath.Join(workdir, ".agents"),
		filepath.Join(global.Resolve().Config, "gocode"),
		filepath.Join(global.Resolve().Home, ".agents"),
	)
	// A question parks the whole turn on the user, so clients have to hear
	// about it now rather than on their next reconciliation tick. The settled
	// hooks matter too: they retract a prompt this client did not answer
	// itself — another client did, or the run was interrupted.
	questions := question.NewService(question.Hooks{
		OnAsked: func(request question.Request) {
			publishAsk(ctx, bus, session.QuestionAsked, request.SessionID, request.ID)
		},
		OnReplied: func(sessionID, requestID string, _ []question.Answer) {
			publishAsk(ctx, bus, session.QuestionSettled, sessionID, requestID)
		},
		OnRejected: func(sessionID, requestID string) {
			publishAsk(ctx, bus, session.QuestionSettled, sessionID, requestID)
		},
	}, nil)

	// Language servers are started lazily, on the first file that needs one, so
	// boot stays fast and a project with none pays nothing.
	lspService := lsp.New(workdir, cfg)

	tools := tool.NewRegistry()
	// The agent switcher is bound after the session service exists; plan mode
	// is registered below once it does.
	builtins.RegisterWith(tools, workdir, builtins.Options{
		Database:  database,
		Skills:    skills,
		Asker:     questions,
		Diagnoser: lspService,
	})
	// Plugin tools register after the built-ins so a plugin can replace one
	// by name, matching the record merge on the TypeScript side.
	plugin.RegisterTools(tools, plugins, workdir, workdir)

	// Slash commands are assembled after skills are discovered, since a skill
	// is one of their sources.
	commands := command.Load(cfg, workdir, skills, []string{
		filepath.Join(workdir, ".gocode"),
		filepath.Join(global.Resolve().Config, "gocode"),
	})

	mcpServers, _ := mcp.ParseServers(cfg.MCP)
	mcpService := mcp.NewService(workdir)
	mcpService.SetRegistry(tools)
	mcpService.LoadAsync(mcpServers)

	// Markdown-defined agents (.gocode/agent/*.md) merge into the config's
	// agent map before the registry is built, so both definition styles flow
	// through one code path. JSON config wins on a name collision.
	cfg.DiscoverAgents(
		filepath.Join(workdir, ".gocode"),
		filepath.Join(global.Resolve().Config, "gocode"),
	)

	agents := agent.NewRegistry()
	// defaultPermissions mirrors agent.ts's `defaults` object, merged into
	// every agent below via permission.Merge(defaultPermissions, explicit) —
	// last-match-wins, so an agent's own explicit rules still override the
	// "*": allow baseline, exactly like TS's Permission.merge(defaults, user).
	defaultPermissions := permission.Defaults()
	var userRules permission.Ruleset
	if cfg.Permission.Raw != nil || cfg.Permission.IsFlat {
		if configured, err := cfg.Permission.Ruleset(); err == nil && len(configured) > 0 {
			userRules = configured
		}
	}
	agents.Update(agent.Info{
		ID:   "build",
		Mode: "primary",
		// plan_enter is denied by default (permission.Defaults) and re-allowed
		// only here, so the suggestion to plan comes from the implementation
		// agent and nowhere else — a subagent, or the plan agent itself,
		// proposing the switch is noise at best and a loop at worst.
		Permissions: permission.Merge(defaultPermissions, permission.Ruleset{
			{Action: "plan_enter", Resource: "*", Effect: permission.Allow},
		}, userRules),
	})
	registerPlanAgent(agents, defaultPermissions, userRules)
	registerBuiltinSubagents(agents, defaultPermissions)
	if cfg.DefaultAgent != "" {
		agents.SetDefault(cfg.DefaultAgent)
	}
	for agentID, agentConfig := range cfg.Agent {
		info := agent.Info{
			ID:          agentID,
			System:      agentConfig.Prompt,
			Description: agentConfig.Description,
			Mode:        agentConfig.Mode,
			Hidden:      agentConfig.Hidden,
			Color:       agentConfig.Color,
			Steps:       agentConfig.EffectiveSteps(),
			Permissions: defaultPermissions,
		}
		if providerID, modelID, ok := config.ParseModelRef(agentConfig.Model); ok {
			info.Model = &agent.ModelRef{ProviderID: providerID, ID: modelID, Variant: agentConfig.Variant}
		}
		if agentConfig.Permission.Raw != nil || agentConfig.Permission.IsFlat {
			if rules, err := agentConfig.AgentRuleset(); err == nil {
				info.Permissions = permission.Merge(defaultPermissions, rules)
			}
		}
		agents.Update(info)
	}
	// Bound late: the rules provider needs the session service, which is not
	// constructed until below. Subagent sessions store a derived ruleset that
	// must override their agent's stock one.
	agentRules := &session.AgentRulesProvider{Agents: agents}
	// "Allow always" is only meaningful with somewhere to put the grant.
	// Scoped to the project, so approving a directory survives the session
	// and every later one in the same worktree.
	savedPermissions := session.NewSavedPermissions(database, workdir)
	// Same nudge for permissions, which had the same tick-latency problem: an
	// approval raised mid-turn sat invisible for up to 10 seconds.
	permissionEngine := permission.NewEngine(agentRules, savedPermissions, permission.Hooks{
		OnAsked: func(request permission.Request) {
			publishAsk(ctx, bus, session.PermissionAsked, request.SessionID, request.ID)
		},
	}, nil)

	runner := &session.Runner{
		DB:                database,
		Bus:               bus,
		Messages:          session.NewMessageStore(database),
		Provider:          streamClient,
		Tools:             tools,
		Agents:            agents,
		Agent:             "build",
		Model:             session.ModelRef{ProviderID: providerID, ID: modelID},
		Permissions:       &session.EnginePermissionGate{Engine: permissionEngine},
		Plugins:           plugins,
		ContextLimit:      defaultContextLimit,
		ReasoningVariants: reasoningVariantsResolver(catalog),
		Pricing:           pricingResolver(catalog),
		Compactor: &session.Compactor{
			Bus:      bus,
			Provider: streamClient,
			Settings: session.DefaultCompactionSettings(),
		},
	}
	execution := session.NewExecution(&session.DBSessionLookup{DB: database}, runner)
	execution.ErrorLogger = logDrainError
	// Turn-level busy/idle for clients. See session/run_events.go for why the
	// step events are not enough.
	execution.OnStatus = session.PublishRunStatus(ctx, bus)
	catalog.StartBackgroundRefresh(ctx)
	service := session.NewService(database, bus)
	// Plan mode needs both the question service and the session service, so it
	// is registered here rather than in the builtins block above.
	tools.Register(builtins.NewPlanEnterTool(questions, service))
	tools.Register(builtins.NewPlanExitTool(questions, service))
	agentRules.Sessions = service
	service.Execution = execution
	service.Compactor = &session.Compactor{
		Bus:      bus,
		Provider: streamClient,
		Settings: session.DefaultCompactionSettings(),
	}
	service.DefaultModel = session.ModelRef{ProviderID: providerID, ID: modelID}
	// The task tool closes the loop between the tool layer and the session
	// layer through the tool.Spawner seam: builtins cannot import session
	// (session imports tool), so the concrete spawner is injected here.
	var jobs *background.Registry
	subagentDepth := session.DefaultSubagentDepth
	if cfg.SubagentDepth != nil {
		subagentDepth = *cfg.SubagentDepth
	}
	if subagentDepth > 0 {
		spawner := session.NewSpawner(service, execution, agents, subagentDepth)
		// Background subagents stay opt-in, matching the TypeScript gate.
		if os.Getenv("GOCODE_EXPERIMENTAL_BACKGROUND_SUBAGENTS") == "true" ||
			os.Getenv("GOCODE_EXPERIMENTAL") == "true" {
			jobs = background.NewRegistry()
			tools.Register(builtins.NewBackgroundTaskTool(spawner, jobs))
		} else {
			tools.Register(builtins.NewTaskTool(spawner))
		}
	}
	return &stack{
		Service:     service,
		Bus:         bus,
		Permissions: permissionEngine,
		Models:      catalog,
		Agents:      agents,
		Config:      cfg,
		ProviderID:  providerID,
		ModelID:     modelID,
		Runner:      runner,
		MCP:         mcpService,
		Jobs:        jobs,
		Skills:      skills,
		Questions:   questions,
		LSP:         lspService,
		Commands:    commands,
		Database:    database,
		Plugins:     plugins,
		Memory:      memory.New(database),
		ProjectID:   projectID,
	}, nil
}

// listenAddr binds addr, exiting the process on failure. Shared by every
// command that starts an HTTP server (serve, web, acp, tui).
func listenAddr(addr string) net.Listener {
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	return listener
}

func resolveProvider(ctx context.Context, providerID string, cfg *config.Config) (llm.StreamClient, error) {
	return provider.FromConfig(ctx, providerID, cfg)
}

// logDrainError reports an advisory drain failure.
//
// Two things it deliberately does not do. It does not report a cancellation:
// an interrupted turn returns the very context error the user's own escape
// produced, and calling that a failure is wrong. And it never writes to the
// terminal — this runs on a background goroutine, and while the TUI is up it
// owns the alternate screen, so a stray write is painted straight over the
// rendered frame (it is how "drain failed: context canceled" ended up on top
// of the footer). See internal/global/diag.go.
func logDrainError(sessionID string, err error) {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return
	}
	global.LogBackground("session %s drain failed: %v", sessionID, err)
}
