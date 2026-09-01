package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/anomalyco/opencode-go/internal/agent"
	"github.com/anomalyco/opencode-go/internal/background"
	"github.com/anomalyco/opencode-go/internal/config"
	"github.com/anomalyco/opencode-go/internal/db"
	"github.com/anomalyco/opencode-go/internal/event"
	"github.com/anomalyco/opencode-go/internal/global"
	"github.com/anomalyco/opencode-go/internal/llm"
	"github.com/anomalyco/opencode-go/internal/mcp"
	"github.com/anomalyco/opencode-go/internal/modelsdev"
	"github.com/anomalyco/opencode-go/internal/modelstate"
	"github.com/anomalyco/opencode-go/internal/permission"
	"github.com/anomalyco/opencode-go/internal/provider"
	"github.com/anomalyco/opencode-go/internal/question"
	"github.com/anomalyco/opencode-go/internal/session"
	"github.com/anomalyco/opencode-go/internal/skill"
	"github.com/anomalyco/opencode-go/internal/tool"
	"github.com/anomalyco/opencode-go/internal/tool/builtins"
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
		err = fmt.Errorf("provider %q unavailable: %w (set provider.%q.options.apiKey in opencode.json, or the provider's API key env var)",
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
	// project first so a local skill overrides a global one of the same name.
	skills := skill.Discover(
		filepath.Join(workdir, ".opencode"),
		filepath.Join(global.Resolve().Config, "opencode"),
	)
	questions := question.NewService(question.Hooks{}, nil)

	tools := tool.NewRegistry()
	// The agent switcher is bound after the session service exists; plan mode
	// is registered below once it does.
	builtins.RegisterWith(tools, workdir, builtins.Options{
		Database: database,
		Skills:   skills,
		Asker:    questions,
	})

	mcpServers, _ := mcp.ParseServers(cfg.MCP)
	mcpService := mcp.NewService(workdir)
	mcpService.SetRegistry(tools)
	mcpService.LoadAsync(mcpServers)

	// Markdown-defined agents (.opencode/agent/*.md) merge into the config's
	// agent map before the registry is built, so both definition styles flow
	// through one code path. JSON config wins on a name collision.
	cfg.DiscoverAgents(
		filepath.Join(workdir, ".opencode"),
		filepath.Join(global.Resolve().Config, "opencode"),
	)

	agents := agent.NewRegistry()
	// defaultPermissions mirrors agent.ts's `defaults` object, merged into
	// every agent below via permission.Merge(defaultPermissions, explicit) —
	// last-match-wins, so an agent's own explicit rules still override the
	// "*": allow baseline, exactly like TS's Permission.merge(defaults, user).
	defaultPermissions := permission.Defaults()
	buildPermissions := defaultPermissions
	if cfg.Permission.Raw != nil || cfg.Permission.IsFlat {
		if configured, err := cfg.Permission.Ruleset(); err == nil && len(configured) > 0 {
			buildPermissions = permission.Merge(defaultPermissions, configured)
		}
	}
	agents.Update(agent.Info{
		ID:          "build",
		Mode:        "primary",
		Permissions: buildPermissions,
	})
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
	permissionEngine := permission.NewEngine(agentRules, nil, permission.Hooks{}, nil)

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
		ContextLimit:      defaultContextLimit,
		ReasoningVariants: reasoningVariantsResolver(catalog),
		Compactor: &session.Compactor{
			Bus:      bus,
			Provider: streamClient,
			Settings: session.DefaultCompactionSettings(),
		},
	}
	execution := session.NewExecution(&session.DBSessionLookup{DB: database}, runner)
	execution.ErrorLogger = func(sessionID string, err error) {
		fmt.Fprintf(os.Stderr, "session %s drain failed: %v\n", sessionID, err)
	}
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
		if os.Getenv("OPENCODE_EXPERIMENTAL_BACKGROUND_SUBAGENTS") == "true" ||
			os.Getenv("OPENCODE_EXPERIMENTAL") == "true" {
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
