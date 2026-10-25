package main

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/langazov/gocode-go/internal/config"
	"github.com/langazov/gocode-go/internal/global"
	"github.com/langazov/gocode-go/internal/gocoder"
	"github.com/langazov/gocode-go/internal/harness"
	"github.com/langazov/gocode-go/internal/server"
	"github.com/langazov/gocode-go/internal/session"
)

// harnessOptions are the knobs a Harness² run takes from the CLI or the API.
type harnessOptions struct {
	// Library overrides config harness.library (auto|local|cloud).
	Library       string
	Model         string
	ImproverModel string
	Variant       string
	Width         int
	CrossDomain   bool
	KeepWorkspace bool
	// RolloutTimeout, SessionTimeout and IdleTimeout override config
	// harness.rolloutTimeout, harness.sessionTimeout and harness.idleTimeout.
	RolloutTimeout time.Duration
	SessionTimeout time.Duration
	IdleTimeout    time.Duration
}

// liveHarnessStacks is st's registry of running Harness² stacks, created
// once and hooked into its session service, so an interrupt sent to st
// (Esc in the TUI) reaches a rollout running on a stack of its own.
func (s *stack) liveHarnessStacks() *liveStacks {
	s.harnessLiveOnce.Do(func() {
		s.harnessLive = &liveStacks{}
		if s.Service != nil {
			s.Service.ExternalInterrupt = s.harnessLive.Interrupt
		}
	})
	return s.harnessLive
}

// harnessTimeouts resolves the per-session limits — rollout, session, idle:
// flag, then config, then the defaults (zero).
func harnessTimeouts(cfg *config.Config, opts harnessOptions) (time.Duration, time.Duration, time.Duration, error) {
	rollout, session, idle := opts.RolloutTimeout, opts.SessionTimeout, opts.IdleTimeout
	if cfg != nil && cfg.Harness != nil {
		for _, pick := range []struct {
			raw string
			dst *time.Duration
			key string
		}{
			{cfg.Harness.RolloutTimeout, &rollout, "rolloutTimeout"},
			{cfg.Harness.SessionTimeout, &session, "sessionTimeout"},
			{cfg.Harness.IdleTimeout, &idle, "idleTimeout"},
		} {
			if *pick.dst != 0 || pick.raw == "" {
				continue
			}
			d, err := time.ParseDuration(pick.raw)
			if err != nil || d <= 0 {
				return 0, 0, 0, fmt.Errorf("harness.%s must be a duration like \"30m\", got %q", pick.key, pick.raw)
			}
			*pick.dst = d
		}
	}
	return rollout, session, idle, nil
}

// harnessRoot is where Harness² keeps run directories and the local
// experience library.
func harnessRoot() string { return filepath.Join(global.Resolve().Data, "harness") }

// libraryPluginState reports whether library-plugin is loaded in st and which
// tools it offers.
func libraryPluginState(st *stack) (bool, []string) {
	if st.Plugins == nil {
		return false, nil
	}
	for _, status := range st.Plugins.Status() {
		if (status.ID == "library-plugin" || strings.Contains(status.Spec, "library-plugin")) && status.State == "loaded" {
			return true, status.Tools
		}
	}
	return false, nil
}

// libraryBaseURL is the gocoder.org site library-plugin talks to: its
// baseURL option when configured, else the signed-in account's site.
func libraryBaseURL(cfg *config.Config, account *gocoder.Account) string {
	if cfg != nil {
		for _, spec := range cfg.Plugin {
			if strings.Contains(spec.Ref, "library-plugin") {
				if url, ok := spec.Options["baseURL"].(string); ok && url != "" {
					return url
				}
			}
		}
	}
	if account != nil {
		return account.URL
	}
	return ""
}

// harnessBackend detects the experience-library backend for a run and builds
// the store and the Library handle (nil on the local backend).
func harnessBackend(ctx context.Context, st *stack, setting string) (harness.ExperienceStore, *harness.Cloud, harness.Detection, error) {
	if setting == "" && st.Config != nil && st.Config.Harness != nil {
		setting = st.Config.Harness.Library
	}
	loaded, tools := libraryPluginState(st)
	account, _ := gocoder.LoadAccount()
	var cloud *harness.Cloud
	if account != nil && account.Key != "" {
		cloud = &harness.Cloud{Client: gocoder.NewClient(libraryBaseURL(st.Config, account)), Bearer: account.Key}
	}
	detection, err := harness.Detect(ctx, harness.DetectInput{
		Setting:      setting,
		PluginLoaded: loaded,
		PluginTools:  tools,
		HasKey:       cloud != nil,
		CheckKey: func(ctx context.Context) error {
			return gocoder.NewClient(libraryBaseURL(st.Config, account)).CheckKey(ctx, account.Key)
		},
		Features: func(ctx context.Context) ([]string, error) {
			return gocoder.NewClient(libraryBaseURL(st.Config, account)).LibraryFeatures(ctx, account.Key)
		},
	})
	if err != nil {
		return nil, nil, detection, err
	}
	local := &harness.LocalStore{Root: filepath.Join(harnessRoot(), "library")}
	switch detection.Backend {
	case harness.BackendCloud:
		return &harness.CloudStore{Cloud: cloud, Cache: local}, cloud, detection, nil
	case harness.BackendSplit:
		return harness.SplitStore{LocalStore: local}, cloud, detection, nil
	}
	return local, nil, detection, nil
}

// newHarnessEngine wires an Engine for runs launched from st's project.
func newHarnessEngine(ctx context.Context, st *stack, opts harnessOptions) (*harness.Engine, harness.Detection, error) {
	store, cloud, detection, err := harnessBackend(ctx, st, opts.Library)
	if err != nil {
		return nil, detection, err
	}
	improverModel := opts.ImproverModel
	if improverModel == "" && st.Config != nil && st.Config.Harness != nil {
		improverModel = st.Config.Harness.Model
	}
	model := opts.Model
	if model == "" {
		model = st.ProviderID + "/" + st.ModelID
	}
	rolloutTimeout, sessionTimeout, idleTimeout, err := harnessTimeouts(st.Config, opts)
	if err != nil {
		return nil, detection, err
	}
	stacks := &harnessStacks{
		Database:       st.Database,
		Catalog:        st.Models,
		Model:          model,
		ImproverModel:  improverModel,
		Variant:        opts.Variant,
		Relay:          st.Bus,
		Live:           st.liveHarnessStacks(),
		RolloutTimeout: rolloutTimeout,
		SessionTimeout: sessionTimeout,
		IdleTimeout:    idleTimeout,
	}
	engine := &harness.Engine{
		Exec:           stacks,
		Improver:       stacks,
		Store:          store,
		Root:           filepath.Join(harnessRoot(), "runs"),
		RolloutWidth:   opts.Width,
		ProposeWidth:   opts.Width,
		KeepWorkspaces: opts.KeepWorkspace,
		Validate:       harness.ValidateOptions{CheckScript: harness.SyntaxCheck},
	}
	if cloud != nil {
		engine.Environment = cloud.SkillSnapshot
		crossDomain := opts.CrossDomain || (st.Config != nil && st.Config.Harness != nil && st.Config.Harness.CrossDomain)
		engine.Extra = func(ctx context.Context, task harness.Task) ([]harness.ExtraEvidence, error) {
			skills, err := cloud.SkillEvidence(ctx, task)
			if err != nil {
				return nil, err
			}
			extra := []harness.ExtraEvidence{{
				Dir: "cloud-skills",
				Description: "gocoder.org Library skills matching this task (the rollouts can load them too) -- " +
					"read-only; copy one into context/harness/skills/ to adapt it",
				Files: skills,
			}}
			if crossDomain && detection.Backend == harness.BackendCloud {
				related, err := cloud.RelatedLessons(ctx, task)
				if err == nil {
					extra = append(extra, harness.ExtraEvidence{
						Dir:         "related",
						Description: "lessons from OTHER areas of work -- hypotheses here, never facts",
						Files:       related,
					})
				}
			}
			return extra, nil
		}
	}
	return engine, detection, nil
}

// baseHarness is the tree a run starts from: an explicit directory, else
// the project's installed harness, else none (gocode as it ships).
func baseHarness(projectDir, explicit string) (harness.Tree, string, error) {
	dir := explicit
	if dir == "" {
		dir = filepath.Join(projectDir, filepath.FromSlash(harness.InstalledDir))
		if !harness.IsTree(dir) {
			return harness.Tree{}, "", nil
		}
	}
	tree, err := harness.ReadTree(dir, nil)
	return tree, dir, err
}

// harnessService is the stack's Harness² service for the HTTP API: runs it
// launches boot their own stacks over this one's database, and their progress
// is published on this stack's bus as live (non-durable) `harness.<type>`
// events carrying the run's state, so no projector is involved.
func (s *stack) harnessService() *harness.Service {
	s.harnessOnce.Do(func() {
		// Hook the interrupt path in now, before the server serves its first
		// request, rather than racing one when the first run starts.
		s.liveHarnessStacks()
		s.harness = &harness.Service{
			Root: filepath.Join(harnessRoot(), "runs"),
			NewEngine: func(ctx context.Context, launch harness.Launch) (*harness.Engine, harness.Detection, error) {
				return newHarnessEngine(ctx, s, harnessOptions{
					Library: launch.Library, Model: launch.Model, ImproverModel: launch.ImproverModel,
					CrossDomain: launch.CrossDomain, Width: launch.Width,
				})
			},
			Store: func(ctx context.Context) (harness.ExperienceStore, harness.Detection, error) {
				store, _, detection, err := harnessBackend(ctx, s, "")
				return store, detection, err
			},
			Install: func(ctx context.Context, summary *harness.Summary, req harness.InstallRequest) (harness.InstallResult, error) {
				return installHarness(ctx, s.workdir, summary, req, func() (*harness.Cloud, error) { return harnessCloud(ctx, s) })
			},
			Prune: func(ctx context.Context, dir string) (int64, error) {
				return session.DeleteUnder(ctx, s.Database, dir)
			},
			Base: func() harness.Tree {
				tree, _, _ := baseHarness(s.workdir, "")
				return tree
			},
		}
		if s.Bus != nil {
			s.harness.Publish = server.HarnessPublisher(s.Bus)
		}
	})
	return s.harness
}
