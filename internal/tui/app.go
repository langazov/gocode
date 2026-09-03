// Package tui implements the terminal user interface, the Go rewrite of
// packages/tui: logo home screen with an always-available prompt, the session
// timeline, leader-key shortcuts, and the session list overlay.
package tui

import (
	"context"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/langazov/gocode-go/internal/command"
	"github.com/langazov/gocode-go/internal/global"
	"github.com/langazov/gocode-go/internal/tui/client"
	"github.com/langazov/gocode-go/internal/tui/theme"

	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"charm.land/glamour/v2"
	"charm.land/lipgloss/v2"
)

const (
	viewHome = iota
	viewChat
)

const leaderKey = "ctrl+x"

type App struct {
	client *client.Client
	theme  theme.Theme
	ctx    context.Context

	// models holds the recent/favorite lists behind the model dialog's
	// sections, shared with the TypeScript client via <state>/model.json.
	models *modelStore

	view      int
	width     int
	height    int
	quitting  bool
	statusMsg string

	sessions []client.Session
	active   *client.Session
	timeline []client.Message
	// streaming holds the ACTIVE session's live assistant text, projected out
	// of the latest snapshot. It is never written from an event directly:
	// with subagents running, a child's deltas must not land here.
	streaming map[string]*strings.Builder // assistantMessageID -> live text
	// agents is the latest aggregated snapshot: one node per session,
	// including subagent sessions. See aggregator.go.
	agents       Snapshot
	dropped      int
	busy         bool
	input        textarea.Model
	scrollOffset int // lines kept off the bottom when scrolled up

	leaderArmed  bool
	spinnerFrame int
	// spinning reports whether a spinnerTickMsg loop is in flight, so the
	// several call sites that set busy can all call startSpinner without
	// stacking duplicate loops. See startSpinner.
	spinning bool
	toast    *toast
	linkHits []linkHit // clickable regions recorded by the last render, see link.go

	overlay           *overlay
	sidebar           bool
	sidebarTodos      []client.Todo
	timestamps        bool
	activeModel       string
	activeAgent       string
	defaultModelLabel string

	permission       *client.PermissionRequest
	permissionChoice int // selected option: 0 once, 1 always, 2 reject
	stats            *client.Stats

	// interruptArmed ports the prompt's `store.interrupt` counter: the
	// session.interrupt command is a two-press gesture, and the footer's hint
	// row reads this to switch between "esc interrupt" and "esc again to
	// interrupt". A press that is not followed up within
	// interruptArmWindow decays back to 0 (setTimeout(..., 5000) upstream).
	interruptArmed   int
	interruptExpires time.Time

	// contextLimits maps "provider/model" to the catalog's limit.context, the
	// denominator behind the footer usage segment's percentage.
	contextLimits map[string]int

	// subagentSiblings holds the children of the open session's parent, which
	// is what the subagent footer counts to render "(2 of 5)". Loaded when a
	// session with a parent is opened.
	subagentSiblings []client.Session

	tip           string
	mcpServers    []client.MCPServer
	cwd           string
	homeDir       string
	gitBranch     string
	modelNames    map[string]string // "provider/model" -> display name
	providerNames map[string]string // provider id -> display name
	// providers is the raw catalog list, kept because the sidebar footer's
	// getting-started card asks whether any provider beyond the bundled free
	// one is reachable.
	providers []client.Provider
	// pastes holds the collapsed large pastes currently standing in the
	// prompt as placeholders, restored on submit. See paste.go.
	pastes []pastedText
	// attachments holds files pasted by path, standing in the prompt as
	// "[Image 1]" placeholders and sent with the message. See paste_attach.go.
	attachments []pastedAttachment
	// promptWidthSet is the last value handed to the editor's SetWidth. The
	// editor reports back a smaller number (it reserves a column for its
	// prompt), so the value passed has to be remembered to tell a real change
	// from that adjustment.
	promptWidthSet int
	// autocomplete is the inline "/" and "@" popup above the prompt.
	autocomplete autocompleteState
	// commands is the cached slash-command list, for completion and dispatch.
	commands []client.Command
	// lsp is the latest language-server status, refreshed on the tick like
	// mcpServers. nil until the first fetch lands.
	lsp *client.LSPState
	// agentList is the cached agent list, so the agent dialog opens without a
	// round trip. (Distinct from `agents`, the subagent aggregator snapshot.)
	agentList []client.Agent
	// catalogModels is the last-fetched model list, the port of TS's
	// sync.data.provider: the dialogs render from it immediately instead of
	// waiting on a request, and a refresh lands through catalogMsg.
	catalogModels []client.Model
	// catalogLoaded records that a catalog reply actually arrived. Without it
	// the dialogs cannot tell "the request has not come back yet" from "it
	// came back empty because no provider is connected" — on a fresh install
	// both are an empty slice, and the model dialog rendered the second as a
	// "Fetching the model catalog..." that never resolved.
	catalogLoaded bool
	// catalogErr is the last catalog fetch failure. loadCatalogCmd used to
	// discard both errors, so an unreachable API was indistinguishable from
	// an empty catalog; the dialog shows this instead.
	catalogErr string
	// allProviders is the *unfiltered* provider list (?all=true), every entry
	// in the catalog including ones holding no credential. That is what the
	// connect dialog offers, and it is deliberately a separate field from
	// `providers`: that one is the reachable subset, and paidProviderAvailable
	// reads it to decide whether the user has connected anything at all, so
	// widening it there would permanently suppress the getting-started card.
	allProviders       []client.Provider
	allProvidersLoaded bool
	allProvidersErr    string
	// modelCosts maps "provider/model" to the catalog's cost.input, the other
	// half of that same question.
	modelCosts map[string]float64
	// dismissedGettingStarted is the in-memory stand-in for the original's
	// `kv.get("dismissed_getting_started")`.
	dismissedGettingStarted bool

	// animationsEnabled and the fades port util/signal.ts's createFadeIn,
	// applied to the prompt's agent/model meta segments (see modelMeta).
	// There is no config toggle wired yet (go-port-gaps.md P0 follow-up), so
	// this defaults true like the TS kv.get("animations_enabled", true).
	animationsEnabled bool
	agentMetaFade     *fadeAnim
	modelMetaFade     *fadeAnim

	// history ports prompt/history.tsx: up/down at the input's start/end
	// recall submitted prompts (see historyKey below).
	history *promptHistory

	// selection is the mouse drag-to-select range (see mouse.go), the port's
	// stand-in for opentui's renderer-level text selection.
	selection textSelection

	// windowTitle is the last title sent via tea.SetWindowTitle, so Update
	// only re-emits it when desiredWindowTitle() actually changes.
	windowTitle string

	// resumeSessionID, when set by RunOptions, is opened at startup instead
	// of showing the home screen.
	resumeSessionID string

	// thinkingMode mirrors context/thinking.ts's ThinkingMode ("show"|"hide",
	// default "hide" — TS persists this in its KV store; this port keeps it
	// in-memory for the process lifetime, like timestamps below). "hide"
	// collapses every reasoning block to its one-line header by default;
	// "show" always renders the full body.
	thinkingMode string
	// expandedReasoning tracks reasoning parts individually toggled open
	// while thinkingMode is "hide" (ReasoningPart's per-instance `expanded`
	// signal), keyed by the part's ID.
	expandedReasoning map[string]bool

	// chatReasoningRows/chatWindowPad/chatWindowStart cache the layout
	// viewChat() last computed, so handleClick (run on the next Update(),
	// against the frame viewChat() just produced) can hit-test a reasoning
	// header row without re-deriving the same scroll/pad arithmetic — see
	// viewChat's doc comment.
	chatReasoningRows map[int]string
	chatWindowPad     int
	chatWindowStart   int
	// chatColumnEnd is the screen column where the chat column stops and the
	// docked sidebar begins, recorded by the same render that laid them out.
	// A drag-selection is held inside whichever of the two it started in —
	// see selectionColumnBounds.
	chatColumnEnd int

	// agents is the agent roster, cached so agent_cycle (tab) can step
	// through it without a fetch.
	agents2 []client.Agent
	// tipsHidden is tips_toggle's state (<leader>h on the home screen).
	tipsHidden bool

	// messageCache memoizes each message's rendered block (see
	// renderMessageCached); renderEpoch is the generation counter its keys
	// carry, bumped for the inputs cheaper to invalidate wholesale than to
	// track per message.
	messageCache map[string]cachedRender
	renderEpoch  uint64

	// mdRenderer caches the glamour renderer built for the current
	// theme+width (see markdown.go): constructing one loads chroma's lexer/
	// style registries, too costly to redo on every streamed delta.
	mdRenderer      *glamour.TermRenderer
	mdRendererWidth int
	mdRendererTheme string
}

// cachedRender is one memoized message block and the reasoning-header
// offsets that go with it.
type cachedRender struct {
	signature uint64
	block     string
	refs      []reasoningHeaderRef
}

// placeholders mirrors the Home route's rotating prompt suggestions.
var placeholders = []string{
	"Fix a TODO in the codebase",
	"What is the tech stack of this project?",
	"Fix broken tests",
}

func New(ctx context.Context, c *client.Client, themeName string) *App {
	resolved := theme.Resolve(themeName)
	input := textarea.New()
	input.Placeholder = `Ask anything... "` + placeholders[rand.Intn(len(placeholders))] + `"`
	input.Prompt = " "
	input.Focus()
	input.CharLimit = 0
	input.SetHeight(1)
	input.ShowLineNumbers = false
	input.KeyMap.InsertNewline.SetEnabled(false)
	// The textarea's own styles would paint over the prompt box's
	// backgroundElement tint: the default cursor line has a solid background
	// and the viewport pads the row with unstyled spaces. Give the cursor line
	// the box tint instead and mute the placeholder like the original.
	element := lipgloss.NewStyle().Background(resolved.BackgroundElement)
	muted := lipgloss.NewStyle().Foreground(resolved.TextMuted)
	taStyles := textarea.DefaultStyles(resolved.Dark)
	taStyles.Focused.CursorLine = element
	taStyles.Blurred.CursorLine = element
	taStyles.Focused.Placeholder = muted
	taStyles.Blurred.Placeholder = muted
	input.SetStyles(taStyles)
	cwd, _ := os.Getwd()
	home, _ := os.UserHomeDir()
	return &App{
		ctx:               ctx,
		client:            c,
		models:            newModelStore(),
		theme:             resolved,
		view:              viewHome,
		sidebar:           true, // the original shows the sidebar by default
		streaming:         map[string]*strings.Builder{},
		input:             input,
		tip:               randomTip(),
		cwd:               cwd,
		homeDir:           home,
		gitBranch:         gitBranch(cwd),
		animationsEnabled: true,
		agentMetaFade:     newFadeAnim(false),
		modelMetaFade:     newFadeAnim(false),
		contextLimits:     map[string]int{},
		modelCosts:        map[string]float64{},
		history:           loadPromptHistory(filepath.Join(global.Resolve().State, promptHistoryFile)),
		windowTitle:       "GoCode",
		thinkingMode:      "hide",
		expandedReasoning: map[string]bool{},
	}
}

type tickMsg time.Time

type leaderTimeoutMsg struct{}

func (a *App) Init() tea.Cmd {
	a.windowTitle = a.desiredWindowTitle()
	cmds := []tea.Cmd{a.loadSessionsCmd(), a.loadCatalogCmd(), a.loadMCPCmd(), a.loadLSPCmd(), a.loadCommandsCmd(), a.loadAgentsCmd(0), a.tick()}
	if a.resumeSessionID != "" {
		cmds = append(cmds, a.resumeSessionCmd(a.resumeSessionID))
	}
	return tea.Batch(cmds...)
}

// desiredWindowTitle mirrors app.tsx's terminal-title effect: "GoCode" on
// the home route or while a session's title is still its creation
// placeholder, else "OC | <title>" (truncated at 40 chars). TS's default-
// title check is a regex over its own "New session - <ISO time>" format;
// this port's placeholder is `filepath.Base(directory)` instead (see
// internal/session/service.go), so the check is against that.
func (a *App) desiredWindowTitle() string {
	if a.view != viewChat || a.active == nil {
		return "GoCode"
	}
	title := strings.TrimSpace(a.active.Title)
	if title == "" || title == filepath.Base(a.active.Directory) {
		return "GoCode"
	}
	if len(title) > 40 {
		title = title[:37] + "..."
	}
	return "OC | " + title
}

// syncWindowTitle recomputes the title and reports whether it changed since
// the last sync, so Update only emits tea.SetWindowTitle on real changes.
func (a *App) syncWindowTitle() (string, bool) {
	title := a.desiredWindowTitle()
	if title == a.windowTitle {
		return title, false
	}
	a.windowTitle = title
	return title, true
}

// tick is now only a slow reconciliation safety net. Live updates arrive as
// coalesced snapshots from the aggregator, so the old 2-second full reload is
// no longer the refresh mechanism — it exists to recover from dropped events.
func (a *App) tick() tea.Cmd {
	return tea.Tick(10*time.Second, func(time.Time) tea.Msg { return tickMsg{} })
}

// applySnapshot folds one aggregated snapshot into the model, reporting
// whether the active session's durable timeline needs a refetch.
//
// All per-event work happens on the aggregator goroutine (aggregator.go); this
// runs on the main goroutine and is therefore deliberately cheap — a map
// lookup and a pointer swap, regardless of how many agents are running.
func (a *App) applySnapshot(snapshot Snapshot) bool {
	a.agents = snapshot
	a.dropped += snapshot.Dropped
	if a.active == nil {
		return false
	}
	node := snapshot.Sessions[a.active.ID]
	if node == nil {
		// Only subagent sessions reported activity this frame.
		return false
	}
	a.streaming = node.Text
	// node.Busy alone is not the status: the aggregator only learns a turn
	// started once the runner publishes step.started, which it does lazily,
	// when the model's first token arrives. See sessionBusy.
	a.busy = node.Busy || sessionBusy(a.timeline)
	if snapshot.Dirty[a.active.ID] {
		a.scrollOffset = 0
		return true
	}
	return false
}

// activeSubagents lists the child sessions that reported activity, so the UI
// can show live subagent rows without a per-event refetch.
func (a *App) activeSubagents() []*SessionNode {
	if a.active == nil {
		return nil
	}
	var out []*SessionNode
	for id, node := range a.agents.Sessions {
		if id == a.active.ID || !node.Busy {
			continue
		}
		out = append(out, node)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

type eventMsg struct{ event client.Event }

type reloadMsg struct{}

type sessionsMsg struct{ sessions []client.Session }
type messagesMsg struct {
	sessionID string
	messages  []client.Message
}
type catalogMsg struct {
	models    []client.Model
	providers []client.Provider
	// providersOK separates "the provider request failed" from "it returned
	// nothing", so a failed refresh does not blank the cached list — which
	// paidProviderAvailable reads, and would misreport as "nothing connected".
	providersOK bool
	// err is the model-list failure. The handler then leaves the cached lists
	// alone rather than blanking them on a transient hiccup.
	err error
}
type mcpMsg struct{ servers []client.MCPServer }

// lspMsg carries the language-server status for the sidebar.
type lspMsg struct{ state *client.LSPState }

// commandsMsg carries the slash-command list.
type commandsMsg struct{ commands []client.Command }

// subagentSiblingsMsg carries the children of the open session's parent, the
// list the subagent footer counts for its "(2 of 5)" position.
type subagentSiblingsMsg struct {
	parentID string
	siblings []client.Session
}

type sessionOpenedMsg struct{ session *client.Session }
type permissionsMsg struct{ pending []client.PermissionRequest }
type promptSentMsg struct {
	sessionID string
	text      string
}
type sessionRefreshedMsg struct{ session *client.Session }
type statusMsg struct {
	text string
	// isErr marks a message as an error explicitly. Without it the toast
	// variant is inferred from the wording, which misses messages that are
	// errors without saying "failed".
	isErr bool
}

// staticMsg turns a ready message into a command.
func staticMsg(msg tea.Msg) tea.Cmd {
	return func() tea.Msg { return msg }
}

// refreshSessionCmd re-fetches a session's server-side state (title, model,
// timestamps) into a.active — best-effort; a failure just skips the
// refresh rather than surfacing an error toast.
func (a *App) refreshSessionCmd(sessionID string) tea.Cmd {
	c := a.client
	return func() tea.Msg {
		session, err := c.Session(a.ctx, sessionID)
		if err != nil {
			return nil
		}
		return sessionRefreshedMsg{session: session}
	}
}

func (a *App) loadSessionsCmd() tea.Cmd {
	c := a.client
	return func() tea.Msg {
		sessions, err := c.Sessions(a.ctx)
		if err != nil {
			return statusMsg{text: "failed to load sessions: " + err.Error()}
		}
		return sessionsMsg{sessions: sessions}
	}
}

// rebuildProviderNames rebuilds the id -> display-name map from both provider
// lists. It reads the unfiltered list too, so a provider the user has not
// connected still renders under its real name rather than its bare id, and so
// a catalog refresh does not drop names the connect dialog had already
// supplied.
func (a *App) rebuildProviderNames() {
	a.providerNames = map[string]string{}
	for _, provider := range a.allProviders {
		a.providerNames[provider.ID] = provider.Name
	}
	for _, provider := range a.providers {
		a.providerNames[provider.ID] = provider.Name
	}
}

// loadCatalogCmd fetches the reachable model and provider lists, which supply
// display names for the current model's meta row and back the model dialog.
//
// The errors are carried rather than dropped: an empty list is a legitimate
// answer here (nothing connected yet), so silently turning a failed request
// into one left the dialog claiming the user had no models when the real
// problem was that the API could not be reached.
func (a *App) loadCatalogCmd() tea.Cmd {
	c := a.client
	return func() tea.Msg {
		models, err := c.Models(a.ctx)
		if err != nil {
			// The model list is what the dialog renders; with it missing there
			// is nothing to apply, so report the failure and keep the cache.
			return catalogMsg{err: err}
		}
		// The provider list only feeds display names and the getting-started
		// card, so a failure there must not throw away the models just
		// fetched — it reports itself through providersOK instead.
		providers, providersErr := c.Providers(a.ctx)
		return catalogMsg{models: models, providers: providers, providersOK: providersErr == nil}
	}
}

// loadMCPCmd fetches live MCP server status (GET /api/mcp), the real
// connected/failed/needs_auth/... state sidebar.tsx's `props.api.state.mcp()`
// reads — not just the configured server names. Re-run on every tickMsg
// (see update()'s tickMsg case), not just once at Init, since the backend
// now reconnects servers in the background after a boot-time failure or a
// later drop (go-port-gaps.md's MCP reconnect entry): without polling, the
// TUI would keep showing a server's status as it was at Init forever. On a
// fetch error, returns nil (no message) rather than an empty server list,
// so a transient hiccup talking to the local API doesn't blank out the
// last-known-good status.
// loadLSPCmd refreshes the language-server status. Re-run on the tick, like
// MCP: servers start lazily as files are read, so the list grows during a
// session rather than being fixed at boot.
// loadCommandsCmd fetches the slash commands. Run once at Init: the set only
// changes when config or skill files change, which needs a restart anyway.
func (a *App) loadCommandsCmd() tea.Cmd {
	c := a.client
	return func() tea.Msg {
		commands, err := c.Commands(a.ctx)
		if err != nil {
			return nil
		}
		return commandsMsg{commands: commands}
	}
}

func (a *App) loadLSPCmd() tea.Cmd {
	c := a.client
	return func() tea.Msg {
		state, err := c.LSP(a.ctx)
		if err != nil {
			return nil
		}
		return lspMsg{state: state}
	}
}

func (a *App) loadMCPCmd() tea.Cmd {
	c := a.client
	return func() tea.Msg {
		servers, err := c.MCPServers(a.ctx)
		if err != nil {
			return nil
		}
		return mcpMsg{servers: servers}
	}
}

func (a *App) loadMessages(sessionID string) tea.Cmd {
	c := a.client
	return func() tea.Msg {
		messages, err := c.Messages(a.ctx, sessionID)
		if err != nil {
			return statusMsg{text: "failed to load messages: " + err.Error()}
		}
		return messagesMsg{sessionID: sessionID, messages: messages}
	}
}

func (a *App) loadPermissions(sessionID string) tea.Cmd {
	c := a.client
	return func() tea.Msg {
		pending, err := c.Permissions(a.ctx, sessionID)
		if err != nil {
			return nil
		}
		return permissionsMsg{pending: pending}
	}
}

// Update dispatches msg then syncs the terminal window title, mirroring
// app.tsx's createEffect over the current route/session — rather than
// hooking every place a.view/a.active changes, this just recomputes the
// desired title every tick; the program adapter's View() surfaces
// a.windowTitle in the returned tea.View, which bubbletea v2 applies as the
// declarative replacement for v1's tea.SetWindowTitle command.
func (a *App) Update(msg tea.Msg) tea.Cmd {
	// Bracket the update: edit at full height so the editor never scrolls, then
	// trim back to the content. See expandPromptForInput.
	a.expandPromptForInput()
	cmd := a.update(msg)
	a.syncPromptSize()
	a.syncWindowTitle()
	return cmd
}

func (a *App) update(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		a.width, a.height = msg.Width, msg.Height
		// syncPromptSize (called from Update) resizes the editor; the width
		// and height both depend on the new dimensions.
		return nil
	case tickMsg:
		cmds := []tea.Cmd{a.tick(), a.loadMCPCmd(), a.loadLSPCmd()}
		if a.active != nil {
			cmds = append(cmds, a.loadPermissions(a.active.ID))
			cmds = append(cmds, a.loadStats(a.active.ID))
			if a.sidebar {
				cmds = append(cmds, a.loadSidebarTodos())
			}
		}
		return tea.Batch(cmds...)
	case spinnerTickMsg:
		a.spinning = false
		if a.busy {
			a.spinnerFrame++
			return a.startSpinner()
		}
		return nil
	case toastExpiredMsg:
		if a.toast != nil && time.Now().After(a.toast.expires) {
			a.toast = nil
		}
		return nil
	case quitMsg:
		a.quitting = true
		return tea.Quit
	case leaderTimeoutMsg:
		a.leaderArmed = false
		return nil
	case sessionsMsg:
		a.sessions = msg.sessions
		return nil
	case catalogMsg:
		if msg.err != nil {
			// Keep whatever was already cached: a failed refresh must not
			// blank a list the user is looking at. The dialog reads this to
			// explain itself instead of showing an endless "Loading".
			a.catalogErr = msg.err.Error()
			a.refreshOpenCatalogDialog()
			return nil
		}
		a.catalogErr = ""
		a.catalogLoaded = true
		a.modelNames = map[string]string{}
		a.contextLimits = map[string]int{}
		a.modelCosts = map[string]float64{}
		for _, model := range msg.models {
			a.modelNames[model.ProviderID+"/"+model.ID] = model.Name
			a.contextLimits[model.ProviderID+"/"+model.ID] = model.ContextLimit
			a.modelCosts[model.ProviderID+"/"+model.ID] = model.CostInput
		}
		if msg.providersOK {
			a.providers = msg.providers
		}
		a.catalogModels = msg.models
		a.rebuildProviderNames()
		// A dialog open before the catalog arrived is showing a stale or empty
		// list; refresh it in place now that there is one.
		a.refreshOpenCatalogDialog()
		// settlementLine resolves a model's display name through this
		// catalog, and messages rendered before it arrived cached the raw
		// model ID.
		a.invalidateRenderCache()
		// The catalog resolving is this port's analogue of TS's
		// local.agent.current() becoming defined: the prompt's meta row was
		// unrenderable before this and fades in now, exactly once
		// (createFadeIn's `revealed` latch means later catalog reloads
		// never re-animate).
		return tea.Batch(
			a.agentMetaFade.Sync(true, a.animationsEnabled),
			a.modelMetaFade.Sync(true, a.animationsEnabled),
		)
	case mcpMsg:
		a.mcpServers = msg.servers
		return nil
	case lspMsg:
		a.lsp = msg.state
		return nil
	case commandsMsg:
		a.commands = msg.commands
		return nil
	case agentsLoadedMsg:
		a.agents2 = msg.agents
		if msg.cycle != 0 {
			return a.cycleAgent(msg.cycle)
		}
		return nil
	case fadeTickMsg:
		if cmd := a.agentMetaFade.Advance(msg); cmd != nil {
			return cmd
		}
		return a.modelMetaFade.Advance(msg)
	case sessionOpenedMsg:
		a.active = msg.session
		// Consumed (applyPendingModel already pinned it to this session if
		// it was created for that purpose) or stale (a leftover home-view
		// pick that belongs to no session): either way it must not leak
		// into whatever session is open next, which reads a.active.Model
		// as its own source of truth from here on.
		a.activeModel = ""
		a.view = viewChat
		a.timeline = nil
		a.subagentSiblings = nil
		a.input.Reset()
		a.input.Focus()
		return tea.Batch(a.loadMessages(a.active.ID), a.loadSubagentSiblings())
	case subagentSiblingsMsg:
		if a.active != nil && a.active.ParentID == msg.parentID {
			a.subagentSiblings = msg.siblings
		}
		return nil
	case openedWithPrompt:
		a.active = msg.session
		a.activeModel = ""
		a.view = viewChat
		a.timeline = nil
		a.input.Reset()
		a.input.Focus()
		a.busy = true
		return a.startSpinner()
	case messagesMsg:
		if a.active != nil && msg.sessionID == a.active.ID {
			a.timeline = msg.messages
			wasBusy := a.busy
			a.busy = a.recomputeBusy()
			// The sidebar's "spent" total is server-side state that only the
			// stats endpoint reports, so it has to be refetched alongside the
			// timeline. Without this it moved only on the 10-second
			// reconciliation tick, which is what made the sidebar look frozen
			// while a turn streamed.
			cmds := []tea.Cmd{a.loadStats(msg.sessionID)}
			if a.busy && !wasBusy {
				cmds = append(cmds, a.startSpinner())
			}
			return tea.Batch(cmds...)
		}
		return nil
	case permissionsMsg:
		a.applyPermissions(msg.pending)
		return nil
	case promptSentMsg:
		if msg.sessionID == "" || a.active == nil {
			return nil
		}
		a.busy = true
		cmds := []tea.Cmd{a.loadPermissions(a.active.ID), a.startSpinner(), a.refreshSessionCmd(a.active.ID)}
		return tea.Batch(cmds...)
	case sessionRefreshedMsg:
		// The server auto-titles a session from its first prompt
		// (maybeSetTitle, synchronous within the prompt call), but the TUI's
		// local a.active snapshot is otherwise never refreshed — without
		// this it would show the directory-derived placeholder title for
		// the rest of the process's life (see go-port-gaps.md).
		if a.active != nil && msg.session != nil && msg.session.ID == a.active.ID {
			*a.active = *msg.session
		}
		return nil
	case reloadMsg:
		if a.active == nil {
			return nil
		}
		return a.loadMessages(a.active.ID)
	case sidebarTodosMsg:
		a.sidebarTodos = msg.todos
		return nil
	case statsMsg:
		a.stats = msg.stats
		return nil
	case snapshotMsg:
		// applySnapshot is where a live turn's busy state actually lands (the
		// aggregator is the only thing watching the event stream), so this is
		// the path that has to keep the spinner running.
		dirty := a.applySnapshot(msg.snapshot)
		cmds := []tea.Cmd{a.startSpinner()}
		if dirty && a.active != nil {
			cmds = append(cmds, a.loadMessages(a.active.ID))
		}
		return tea.Batch(cmds...)
	case agentListMsg:
		a.agentList = msg.agents
		if o := a.overlay; o != nil && o.kind == overlayList && o.title == "Select agent" {
			filter, selected := o.filter, a.selectedOverlayValue()
			a.openAgentDialog(a.agentList)
			a.restoreOverlaySelection(filter, selected)
		}
		return nil
	case providerListMsg:
		if msg.err != nil {
			a.allProvidersErr = msg.err.Error()
			a.refreshOpenCatalogDialog()
			return nil
		}
		a.allProvidersErr = ""
		a.allProvidersLoaded = true
		// Deliberately not `a.providers`. This is the unfiltered list; writing
		// it there is what used to make the connect dialog fill and then empty
		// again, because the slower catalogMsg would land afterwards and
		// overwrite it with the reachable-only subset.
		a.allProviders = msg.providers
		a.rebuildProviderNames()
		a.refreshOpenCatalogDialog()
		return nil
	case providerConnectedMsg:
		a.closeOverlay()
		return tea.Batch(
			a.showToast("Connected "+msg.name, false),
			a.modelsOverlay(),
		)
	case oauthPollMsg:
		return a.pollOAuth(msg)
	case oauthDoneMsg:
		a.closeOverlay()
		return tea.Batch(
			a.showToast("Connected "+msg.name, false),
			a.modelsOverlay(),
		)
	case oauthFailedMsg:
		a.closeOverlay()
		text := "login failed"
		if msg.err != "" {
			text += ": " + msg.err
		}
		return a.showToast(text, true)
	case statusMsg:
		a.statusMsg = msg.text
		isError := msg.isErr ||
			strings.HasPrefix(msg.text, "failed") ||
			strings.Contains(msg.text, "failed:") ||
			strings.Contains(msg.text, "error")
		return a.showToast(msg.text, isError)
	case tea.PasteMsg:
		// Bracketed paste. Without this case the message falls through the
		// switch and the pasted text is silently dropped.
		return a.handlePaste(msg)
	case tea.KeyMsg:
		return a.handleKey(msg)
	case tea.MouseMsg:
		return a.handleMouse(msg)
	}
	return nil
}

func (a *App) handleKey(msg tea.KeyMsg) tea.Cmd {
	// A pending mouse selection intercepts ctrl+c (copy instead of quit) and
	// escape (clear instead of whatever escape would otherwise do), ahead of
	// everything else — mirrors util/selection.ts's handleSelectionKey, which
	// runs as a global key hook regardless of dialog state.
	if a.selection.hasRange() {
		switch msg.String() {
		case "ctrl+c":
			return a.copySelectionCmd()
		case "esc", "escape":
			a.selection.clear()
			return nil
		}
	}
	// A dialog owns the keyboard while open (modal mode in the original).
	if a.overlay != nil {
		return a.handleOverlayKey(msg.String())
	}
	switch msg.String() {
	case "ctrl+c", "ctrl+d":
		a.quitting = true
		return tea.Quit
	}

	if a.leaderArmed {
		a.leaderArmed = false
		switch msg.String() {
		case "l":
			drive := a.loadSessionsCmd()
			a.sessionsOverlay()
			return drive
		case "n":
			return a.newSession()
		case "m":
			return a.modelsOverlay()
		case "a":
			return a.agentsOverlay()
		case "t":
			a.themesOverlay()
			return nil
		case "s":
			a.overlay = &overlay{kind: overlayStatus, title: "Status"}
			return nil
		case "g":
			a.openList("Timeline", a.timelineOverlayItems())
			return nil
		case "down":
			// session_child_first
			return a.childrenOverlay()
		case "h":
			// tips_toggle
			a.tipsHidden = !a.tipsHidden
			return nil
		case "e":
			return a.exportToEditor()
		case "b":
			a.sidebar = !a.sidebar
			if a.sidebar {
				return a.loadSidebarTodos()
			}
			return nil
		case "c":
			return a.compactNow()
		case "q":
			a.quitting = true
			return tea.Quit
		}
		return nil
	}
	if msg.String() == leaderKey {
		a.leaderArmed = true
		return tea.Tick(time.Second, func(time.Time) tea.Msg { return leaderTimeoutMsg{} })
	}

	switch msg.String() {
	case "ctrl+p":
		// command_list
		a.commandPalette()
		return nil
	case "tab":
		// agent_cycle — the hint row has always advertised this ("tab
		// agents"); nothing was bound to it.
		return a.cycleAgent(1)
	case "shift+tab":
		// agent_cycle_reverse
		return a.cycleAgent(-1)
	case "ctrl+z":
		// terminal_suspend
		return tea.Suspend
	case "shift+enter", "ctrl+enter", "alt+enter", "ctrl+j":
		// input_newline. bubbles' textarea only treats a bare enter as a
		// newline, and this handler claims that for input_submit, so the
		// aliases have to insert one explicitly.
		a.input.InsertString("\n")
		return nil
	}
	if msg.String() == "ctrl+r" {
		if a.active == nil {
			return staticMsg(statusMsg{text: "open a session first"})
		}
		current := sessionTitleOf(*a.active)
		a.openInput("Rename Session", current, func(value string) tea.Msg {
			if err := a.client.Rename(a.ctx, a.active.ID, value); err != nil {
				return statusMsg{text: "rename failed: " + err.Error()}
			}
			a.active.Title = value
			return statusMsg{text: "renamed"}
		})
		return nil
	}
	// The inline popup takes the navigation keys while it is open; everything
	// else falls through to the editor so typing keeps narrowing the list.
	if a.autocomplete.visible() {
		if cmd, handled := a.handleAutocompleteKey(msg); handled {
			return cmd
		}
	}
	if msg.String() == "@" {
		a.input.InsertString("@")
		a.openMentionAutocomplete()
		return nil
	}
	// "/" opens command completion, but only at the very start of the prompt:
	// a slash anywhere else is ordinary text (a path, a date, a fraction).
	// Ports autocomplete.tsx's "/ at position 0" trigger.
	if msg.String() == "/" && a.input.Value() == "" {
		a.input.InsertString("/")
		a.openSlashAutocomplete()
		return nil
	}

	if a.permission != nil {
		return a.handlePermissionKey(msg)
	}

	if msg.String() == "up" || msg.String() == "down" {
		if cmd, handled := a.historyKey(msg.String()); handled {
			return cmd
		}
		// Not at the input's boundary: fall through to the textarea's own
		// multi-line cursor movement below.
	}

	// Subagent navigation (session_parent / session_child_cycle_reverse /
	// session_child_cycle: up / left / right in config/keybind.ts). These are
	// the commands the subagent footer's Parent/Prev/Next buttons dispatch.
	// Upstream binds them on the session route and lets the focused textarea
	// win; this port has no focus tree, so they only fire on an empty prompt
	// and otherwise fall through to the textarea's own cursor movement.
	if strings.TrimSpace(a.input.Value()) == "" {
		switch msg.String() {
		case "up":
			if cmd, handled := a.openParentSession(); handled {
				return cmd
			}
		case "left":
			if cmd, handled := a.cycleSubagentSibling(-1); handled {
				return cmd
			}
		case "right":
			if cmd, handled := a.cycleSubagentSibling(1); handled {
				return cmd
			}
		}
	}

	switch msg.String() {
	case "esc", "escape":
		if a.view == viewChat && a.busy && a.active != nil {
			return a.armInterrupt()
		}
		return nil
	// The messages_* family from config/keybind.ts. pageup/pagedown are a
	// *full* page upstream; this port scrolled half a page for both and had
	// no half-page, line or first/last bindings at all.
	case "pgup", "pageup", "ctrl+alt+b":
		return a.scrollMessages(a.viewportHeight())
	case "pgdown", "pagedown", "ctrl+alt+f":
		return a.scrollMessages(-a.viewportHeight())
	case "ctrl+alt+u":
		return a.scrollMessages(a.viewportHeight() / 2)
	case "ctrl+alt+d":
		return a.scrollMessages(-a.viewportHeight() / 2)
	case "ctrl+alt+y":
		return a.scrollMessages(1)
	case "ctrl+alt+e":
		return a.scrollMessages(-1)
	case "ctrl+g":
		// messages_first. `home` is the other binding upstream, but it is
		// also input_buffer_home and the prompt holds focus here, so it stays
		// with the input.
		a.scrollOffset = a.maxScrollOffset()
		return nil
	case "ctrl+alt+g":
		// messages_last (`end` likewise stays with the input).
		a.scrollOffset = 0
		return nil
	case "ctrl+v":
		// prompt.paste. Most terminals turn their paste shortcut into a
		// bracketed paste, which arrives as tea.PasteMsg and never reaches
		// here; this is for the ones that send the key through instead.
		return a.pasteFromClipboard()
	case "enter":
		text := strings.TrimSpace(a.input.Value())
		if text == "" {
			return nil
		}
		a.input.Reset()
		// Restore any collapsed pastes before the text goes anywhere: what is
		// sent is the real content, not the "[Pasted ~N lines]" stand-in.
		pastes := a.takePastes()
		files := a.takeAttachments()
		if strings.HasPrefix(text, "/") {
			return a.runSlashCommand(strings.TrimPrefix(text, "/"))
		}
		text = expandPastes(text, pastes)
		a.history.Append(text)
		if a.view == viewHome {
			return a.createAndPrompt(text)
		}
		return a.sendPromptWith(a.active.ID, text, files)
	}

	var cmd tea.Cmd
	a.input, cmd = a.input.Update(msg)
	// The prompt is the popup's query: re-filter after every keystroke, and
	// close it once the trigger no longer applies.
	a.syncAutocomplete()
	return cmd
}

// historyKey ports prompt/index.tsx's prompt.history.previous /
// prompt.history.next commands, which are a *two-stage* gesture:
//
//	if (input.cursorOffset !== 0) {
//	  if (on the first visual row) input.cursorOffset = 0
//	  return false
//	}
//	… recall …
//	input.cursorOffset = 0
//
// So an arrow first moves the cursor to the far end of the draft, and only a
// second press with the cursor already parked there recalls. That matters
// because a recall leaves the cursor at the *start* (for up) — which is
// exactly why "down" appeared dead in this port. It required the cursor to be
// at the end before it would do anything, the recall had just put it at the
// start, and bubbles' CursorDown on a single-row document does not move it,
// so no amount of pressing down ever reached the forward branch.
//
// handled=false lets the key fall through to the textarea's own cursor
// movement; upstream returns false in the snap case too, but its own movement
// is a no-op on the boundary row it just snapped to, so consuming the key here
// is equivalent.
func (a *App) historyKey(key string) (tea.Cmd, bool) {
	if key == "up" {
		if !a.inputAtStart() {
			if a.inputOnFirstRow() {
				moveCursorToDocumentStart(&a.input)
				return nil, true
			}
			return nil, false
		}
		return a.recallHistory(-1)
	}
	if !a.inputAtEnd() {
		if a.inputOnLastRow() {
			moveCursorToDocumentEnd(&a.input)
			return nil, true
		}
		return nil, false
	}
	return a.recallHistory(1)
}

// recallHistory swaps the draft for the neighbouring history entry, parking
// the cursor at the end the arrow came from so the next press continues in the
// same direction.
func (a *App) recallHistory(direction int) (tea.Cmd, bool) {
	text, ok := a.history.Move(direction, a.input.Value())
	if !ok {
		return nil, false
	}
	a.input.SetValue(text)
	if direction < 0 {
		moveCursorToDocumentStart(&a.input)
	} else {
		moveCursorToDocumentEnd(&a.input)
	}
	return nil, true
}

// inputAtStart/inputAtEnd are TS's `cursorOffset === 0` /
// `cursorOffset === plainText.length`: the absolute document boundary.
// textarea.Column() is the cursor's rune offset within its *logical* line,
// which is what these need — LineInfo().CharOffset, which this port used
// before, is a column count relative to the current *visual* row, so both
// checks silently failed the moment a draft wrapped.
func (a *App) inputAtStart() bool {
	return a.input.Line() == 0 && a.input.Column() == 0
}

func (a *App) inputAtEnd() bool {
	lines := strings.Split(a.input.Value(), "\n")
	last := []rune(lines[len(lines)-1])
	return a.input.Line() == a.input.LineCount()-1 && a.input.Column() == len(last)
}

// inputOnFirstRow/inputOnLastRow are the *visual* boundaries the snap stage
// keys off (TS's `scrollY + visualCursor.visualRow === 0` and its
// last-virtual-line counterpart), so a wrapped draft still walks row by row
// before the recall takes over.
func (a *App) inputOnFirstRow() bool {
	return a.input.Line() == 0 && a.input.LineInfo().RowOffset == 0
}

func (a *App) inputOnLastRow() bool {
	info := a.input.LineInfo()
	return a.input.Line() == a.input.LineCount()-1 && info.RowOffset == info.Height-1
}

// moveCursorToDocumentStart/End walk line by line since textarea.Model only
// exposes per-line CursorStart/CursorEnd.
func moveCursorToDocumentStart(m *textarea.Model) {
	for m.Line() > 0 {
		m.CursorUp()
	}
	m.CursorStart()
}

func moveCursorToDocumentEnd(m *textarea.Model) {
	for m.Line() < m.LineCount()-1 {
		m.CursorDown()
	}
	m.CursorEnd()
}

// handlePermissionKey mirrors the PermissionPrompt keybinds: left/right (or
// h/l) move between the options, enter confirms, escape rejects. The y/a/n
// quick answers from the earlier port are kept as aliases.
func (a *App) handlePermissionKey(msg tea.KeyMsg) tea.Cmd {
	request := a.permission
	answer := ""
	switch msg.String() {
	case "left", "h":
		a.permissionChoice = (a.permissionChoice + 2) % 3
		return nil
	case "right", "l":
		a.permissionChoice = (a.permissionChoice + 1) % 3
		return nil
	case "enter":
		answer = []string{"once", "always", "reject"}[a.permissionChoice]
	case "esc", "escape":
		answer = "reject"
	case "y":
		answer = "once"
	case "a":
		answer = "always"
	case "n":
		answer = "reject"
	}
	if answer == "" {
		return nil
	}
	a.permission = nil
	a.permissionChoice = 0
	c := a.client
	sessionID, requestID := request.SessionID, request.ID
	return func() tea.Msg {
		if err := c.Reply(a.ctx, sessionID, requestID, answer); err != nil {
			return statusMsg{text: "reply failed: " + err.Error()}
		}
		return nil
	}
}

func (a *App) applyPermissions(pending []client.PermissionRequest) {
	a.permission = nil
	if a.active == nil {
		return
	}
	for i := range pending {
		if pending[i].SessionID == a.active.ID {
			if a.permission == nil || a.permission.ID != pending[i].ID {
				a.permissionChoice = 0
			}
			a.permission = &pending[i]
			return
		}
	}
}

func (a *App) newSession() tea.Cmd {
	dir, err := os.Getwd()
	if err != nil {
		return staticMsg(statusMsg{text: err.Error()})
	}
	c := a.client
	pendingModel := a.activeModel
	return func() tea.Msg {
		session, err := c.CreateSession(a.ctx, client.CreateInput{Directory: dir})
		if err != nil {
			return statusMsg{text: "failed to create session: " + err.Error()}
		}
		applyPendingModel(a.ctx, c, session, pendingModel)
		return sessionOpenedMsg{session: session}
	}
}

// applyPendingModel pins a model chosen from the home view (before any
// session existed) onto a session just created for it, setting session.Model
// in place so the caller's sessionOpenedMsg/openedWithPrompt already carries
// the right value — sessionOpenedMsg clears a.activeModel once this has run,
// consumed or not, so it never leaks into a later, unrelated session.
func applyPendingModel(ctx context.Context, c *client.Client, session *client.Session, pending string) {
	if pending == "" {
		return
	}
	providerID, modelID, ok := strings.Cut(pending, "/")
	if !ok {
		return
	}
	if err := c.SetModel(ctx, session.ID, providerID, modelID); err == nil {
		session.Model = &client.ModelRef{ProviderID: providerID, ID: modelID}
	}
}

type openedWithPrompt struct {
	session *client.Session
	text    string
}

func (a *App) createAndPrompt(text string) tea.Cmd {
	dir, err := os.Getwd()
	if err != nil {
		return staticMsg(statusMsg{text: err.Error()})
	}
	c := a.client
	pendingModel := a.activeModel
	return func() tea.Msg {
		session, err := c.CreateSession(a.ctx, client.CreateInput{Directory: dir})
		if err != nil {
			return statusMsg{text: "failed to create session: " + err.Error()}
		}
		applyPendingModel(a.ctx, c, session, pendingModel)
		if _, err := c.Prompt(a.ctx, session.ID, text); err != nil {
			return statusMsg{text: "prompt failed: " + err.Error()}
		}
		return openedWithPrompt{session: session, text: text}
	}
}

func (a *App) sendPrompt(sessionID, text string) tea.Cmd {
	return a.sendPromptWith(sessionID, text, nil)
}

// sendPromptWith sends a message that may carry pasted attachments.
func (a *App) sendPromptWith(sessionID, text string, files []client.FileAttachment) tea.Cmd {
	c := a.client
	return func() tea.Msg {
		if _, err := c.PromptWith(a.ctx, sessionID, text, files); err != nil {
			return statusMsg{text: "prompt failed: " + err.Error()}
		}
		return promptSentMsg{sessionID: sessionID, text: text}
	}
}

// View renders the active route, mirroring the TS route switch in app.tsx.
// Dialogs composite over the underlying route like the Dialog backdrop.
func (a *App) View() string {
	if a.quitting {
		return ""
	}
	if a.width == 0 {
		return "loading…"
	}
	content := a.currentFrame()
	if a.selection.hasRange() {
		content = a.applySelectionHighlight(content)
	}
	return content
}

// sessionBusy ports context/sync.tsx's status():
//
//	const last = messages.at(-1)
//	if (!last) return "idle"
//	if (last.role === "user") return "working"
//	return last.time.completed ? "idle" : "working"
//
// The user arm is the load-bearing one and this port did not have it: it only
// ever inspected the last *assistant* message, so in the window between a
// prompt being admitted and the model producing its first token -- the whole
// time-to-first-token, commonly a second or more -- the timeline ended with
// the user's own message and this reported idle. That is what put a visible
// delay between pressing enter and the spinner appearing.
//
// A settled turn is `time.completed` upstream. The finish and error checks
// alongside it are this port's own belt-and-braces: projectStepEnded and
// projectStepFailed both stamp time.completed, but an interrupted turn records
// no finish at all (see messageAborted), and rows written before that stamping
// existed have neither.
func sessionBusy(timeline []client.Message) bool {
	if len(timeline) == 0 {
		return false
	}
	last := timeline[len(timeline)-1]
	switch last.Type {
	case "user":
		return true
	case "assistant":
		data, err := client.DecodeAssistant(last.Data)
		if err != nil {
			return false
		}
		return data.Finish == "" && data.Time.Completed == 0 && data.Error == nil
	}
	return false
}

// recomputeBusy combines the two signals this port has for a running turn: the
// aggregator's live step tracking, and the timeline-derived status upstream
// uses. They agree once a turn is underway; the timeline covers the gap before
// the first step event, and the aggregator covers events arriving faster than
// the timeline is refetched.
func (a *App) recomputeBusy() bool {
	if a.active != nil {
		if node := a.agents.Sessions[a.active.ID]; node != nil && node.Busy {
			return true
		}
	}
	return sessionBusy(a.timeline)
}

// program adapts App to bubbletea's immutable Model interface.
type program struct{ app *App }

func (p program) Init() tea.Cmd { return p.app.Init() }

func (p program) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	return p, p.app.Update(msg)
}

// View builds the declarative tea.View bubbletea v2 replaced v1's plain
// string + tea.WithAltScreen()/tea.WithMouseAllMotion() program options
// with. AllMotion (not just CellMotion) so dialog rows preselect on hover
// with no button held, matching dialog-select.tsx's onMouseMove/onMouseOver
// (same rationale as the removed Run() options it replaces).
func (p program) View() tea.View {
	v := tea.NewView(p.app.View())
	v.AltScreen = true
	v.MouseMode = tea.MouseModeAllMotion
	v.WindowTitle = p.app.windowTitle
	return v
}

type quitMsg struct{}

func themeResolve(name string) theme.Theme { return theme.Resolve(name) }

// runSlashCommand executes "/name" from the editor. Names may be the full
// command ("session.new") or a namespace prefix ("/help" -> "help.show").
// runSlashCommand dispatches "/name args".
//
// Two kinds of command share the syntax. A *prompt* command — the ones from
// config, markdown files and skills, plus the built-in init and review — is a
// template: it expands with the arguments and is sent as the prompt. An
// *interface* command (session.new, model.list, ...) drives the interface and
// takes no arguments. Prompt commands are matched first, because they are the
// ones a user defines and would be surprised to see shadowed.
func (a *App) runSlashCommand(input string) tea.Cmd {
	input = strings.TrimSpace(strings.TrimPrefix(input, "/"))
	name, arguments, _ := strings.Cut(input, " ")
	arguments = strings.TrimSpace(arguments)

	for _, entry := range a.commands {
		if entry.Name == name {
			return a.runPromptCommand(entry, arguments)
		}
	}

	for _, entry := range a.commandsRegistry() {
		if entry.matchesSlash(name) {
			return runItemAction(entry)
		}
	}
	return staticMsg(statusMsg{text: "unknown command: /" + name})
}

// runPromptCommand expands a command template and sends it as the prompt.
func (a *App) runPromptCommand(entry client.Command, arguments string) tea.Cmd {
	text := command.Expand(a.ctx, entry.Template, arguments, "")
	if text == "" {
		return staticMsg(statusMsg{text: "/" + entry.Name + " produced an empty prompt"})
	}
	if a.view == viewHome {
		return a.createAndPrompt(text)
	}
	if a.active == nil {
		return staticMsg(statusMsg{text: "open a session first"})
	}
	return a.sendPrompt(a.active.ID, text)
}

// openSlashAutocomplete opens the inline "/" popup.
func (a *App) openSlashAutocomplete() {
	items := a.slashAutocompleteItems()
	if len(items) == 0 {
		return
	}
	a.autocomplete = autocompleteState{kind: autocompleteSlash, all: items, trigger: 0}
	a.autocomplete.filter("")
}

// openMentionAutocomplete opens the inline "@" popup for workspace files.
func (a *App) openMentionAutocomplete() {
	var items []autocompleteItem
	for _, item := range fileMentions("") {
		path := item.label
		items = append(items, autocompleteItem{
			display: "@" + path,
			value:   path,
			action: func() tea.Cmd {
				a.replaceAutocompleteToken(path + " ")
				return nil
			},
		})
	}
	if len(items) == 0 {
		return
	}
	a.autocomplete = autocompleteState{
		kind:    autocompleteMention,
		all:     items,
		trigger: strings.LastIndex(a.input.Value(), "@"),
	}
	a.autocomplete.filter("")
}

// hideAutocomplete closes the popup, clearing the half-typed command it was
// completing.
//
// Ports hide() in autocomplete.tsx:
//
//	if (store.visible === "/" && !text.endsWith(" ") && text.startsWith("/")) {
//	  props.input().deleteRange(0, 0, cursor.row, cursor.col)
//	}
//
// Without it, running a command that opens a dialog leaves "/models" sitting
// in the prompt after the dialog closes. The trailing-space guard is what
// keeps a completed command with arguments ("/deploy staging") intact — that
// text is the user's, not a leftover.
func (a *App) hideAutocomplete() {
	if a.autocomplete.kind == autocompleteSlash {
		text := a.input.Value()
		if strings.HasPrefix(text, "/") && !strings.HasSuffix(text, " ") {
			a.input.SetValue("")
		}
	}
	a.autocomplete.close()
}

// handleAutocompleteKey takes the keys the popup owns. Everything else falls
// through so typing keeps going into the prompt and narrowing the list, which
// is how the original behaves — it has no input field of its own.
func (a *App) handleAutocompleteKey(msg tea.KeyMsg) (tea.Cmd, bool) {
	switch msg.String() {
	case "up", "ctrl+p":
		a.autocomplete.move(-1)
		return nil, true
	case "down", "ctrl+n":
		a.autocomplete.move(1)
		return nil, true
	case "esc", "escape":
		a.hideAutocomplete()
		return nil, true
	case "enter", "tab":
		item, ok := a.autocompleteSelection()
		if !ok {
			// Nothing matched. The original's select() returns early here and
			// the keypress is swallowed, which leaves the user with no way to
			// find out why. Closing without clearing and letting the key fall
			// through submits what they typed, so an unrecognised command
			// reports itself instead of doing nothing.
			a.autocomplete.close()
			return nil, false
		}
		// hide() runs before the action in the original, so the command text
		// is gone by the time a dialog opens over it — and a prompt command's
		// own insert then writes into an empty prompt.
		a.hideAutocomplete()
		if item.action == nil {
			return nil, true
		}
		return item.action(), true
	}
	return nil, false
}

func (a *App) autocompleteSelection() (autocompleteItem, bool) {
	state := a.autocomplete
	if state.selected < 0 || state.selected >= len(state.items) {
		return autocompleteItem{}, false
	}
	return state.items[state.selected], true
}

// syncAutocomplete re-filters the popup against the prompt after a keystroke,
// and closes it when the trigger no longer applies.
func (a *App) syncAutocomplete() {
	state := &a.autocomplete
	if !state.visible() {
		return
	}
	value := a.input.Value()
	if state.trigger < 0 || state.trigger >= len(value) {
		// The trigger character itself is gone (backspaced away), so there is
		// nothing left to clear — close without touching the prompt.
		state.close()
		return
	}
	query := value[state.trigger+1:]
	// A space ends the token: "/new " has been chosen, and "@a b" is no longer
	// one path.
	if strings.ContainsAny(query, " \t\n") {
		state.close()
		return
	}
	state.filter(query)
}

// replaceAutocompleteToken swaps the trigger token for the chosen value.
func (a *App) replaceAutocompleteToken(replacement string) {
	value := a.input.Value()
	trigger := a.autocomplete.trigger
	if trigger < 0 || trigger > len(value) {
		a.input.InsertString(replacement)
		return
	}
	a.input.SetValue(value[:trigger] + replacement)
	a.input.MoveToEnd()
}

// slashAutocompleteItems lists everything "/" can reach: the prompt commands
// from the server, then the interface commands under their typeable names.
func (a *App) slashAutocompleteItems() []autocompleteItem {
	items := make([]autocompleteItem, 0, len(a.commands)+16)
	for _, entry := range a.commands {
		entry := entry
		description := entry.Description
		if description == "" && len(entry.Hints) > 0 {
			description = strings.Join(entry.Hints, " ")
		}
		items = append(items, autocompleteItem{
			display:     "/" + entry.Name,
			description: description,
			value:       entry.Name,
			action: func() tea.Cmd {
				// Insert rather than run, so arguments can be typed. Matches
				// the original setting the text to "/" + name + " ".
				a.input.SetValue("/" + entry.Name + " ")
				a.input.MoveToEnd()
				return nil
			},
		})
	}
	for _, entry := range a.commandsRegistry() {
		entry := entry
		if entry.slash == "" {
			continue
		}
		description := entry.hint
		if len(entry.slashAliases) > 0 {
			description = strings.TrimSpace(description + " (" + strings.Join(entry.slashAliases, ", ") + ")")
		}
		items = append(items, autocompleteItem{
			display:     "/" + entry.slash,
			description: description,
			value:       entry.slash,
			action:      func() tea.Cmd { return runItemAction(entry) },
		})
	}
	return items
}

// slashCommandItems lists everything "/" can reach: the prompt commands from
// the server, then the interface commands, matching the order the original's
// autocomplete builds (its own slashes first, then server commands — inverted
// here because a user's own commands are the ones they are looking for).
func (a *App) slashCommandItems() []overlayItem {
	items := make([]overlayItem, 0, len(a.commands)+8)
	for _, entry := range a.commands {
		entry := entry
		hint := entry.Description
		if hint == "" && len(entry.Hints) > 0 {
			hint = strings.Join(entry.Hints, " ")
		}
		category := "Commands"
		if entry.Source == "skill" {
			category = "Skills"
		}
		items = append(items, overlayItem{
			label:    entry.Name,
			hint:     hint,
			value:    entry.Name,
			category: category,
			action: func() tea.Msg {
				a.input.SetValue("/" + entry.Name + " ")
				a.input.MoveToEnd()
				return nil
			},
		})
	}
	for _, entry := range a.commandsRegistry() {
		entry := entry
		// Listed under the name a user actually types. An interface command
		// with no slash name is palette-only, exactly as upstream drops any
		// entry whose slashName is unset.
		if entry.slash == "" {
			continue
		}
		hint := entry.hint
		if len(entry.slashAliases) > 0 {
			hint = strings.TrimSpace(hint + "  (" + strings.Join(entry.slashAliases, ", ") + ")")
		}
		items = append(items, overlayItem{
			label:    entry.slash,
			hint:     hint,
			value:    entry.slash,
			category: "Interface",
			footer:   entry.footer,
			action:   entry.action,
		})
	}
	return items
}

// openFileMentions shows @-completion for workspace files; selecting inserts
// the path into the editor.
func (a *App) openFileMentions() {
	query := ""
	items := fileMentions(query)
	for i := range items {
		path := items[i].label
		items[i].action = func() tea.Msg {
			a.input.InsertString(path + " ")
			return nil
		}
	}
	a.openList("Files", items)
}

func (a *App) sessionTitle() string {
	if a.active == nil {
		return ""
	}
	return sessionTitleOf(*a.active)
}

// currentModelParts resolves the active provider and model IDs: the session's
// pinned model, else the user-selected label, else the default.
func (a *App) currentModelParts() (providerID, modelID string, ok bool) {
	if a.active != nil && a.active.Model != nil {
		return a.active.Model.ProviderID, a.active.Model.ID, true
	}
	if a.activeModel != "" {
		if provider, model, found := strings.Cut(a.activeModel, "/"); found {
			return provider, model, true
		}
	}
	return strings.Cut(a.defaultModelLabel, "/")
}

func (a *App) modelName(providerID, modelID string) string {
	if name := a.modelNames[providerID+"/"+modelID]; name != "" {
		return name
	}
	return modelID
}

func (a *App) providerName(providerID string) string {
	if name := a.providerNames[providerID]; name != "" {
		return name
	}
	return providerID
}

// inputWidth sizes the shared editor to the prompt box of the view it is
// currently in: promptMaxWidth(a.width) on home, sessionPromptBoxWidth() in a
// session, each minus promptBox's paddingLeft(2)+paddingRight(2) and the
// border column.
//
// It used to take the *minimum* of the two, so one editor could sit in either
// box. That left the text 20-odd columns short of the box's right edge in a
// session on a wide terminal, because the home box is capped at 75 columns
// while the session box is not. The view is known here, so it sizes for the
// box it is actually in; syncPromptSize re-runs this when the view changes.
func (a *App) inputWidth() int {
	box := promptMaxWidth(a.width)
	if a.view == viewChat {
		box = a.sessionPromptBoxWidth()
	}
	w := box - 4
	if w < 20 {
		w = 20
	}
	return w
}

func (a *App) currentModelLabel() string {
	if a.activeModel != "" {
		return a.activeModel
	}
	if a.active != nil && a.active.Model != nil {
		return a.active.Model.ProviderID + "/" + a.active.Model.ID
	}
	return a.defaultModelLabel
}

// SetDefaultModel sets the model label shown in the sidebar before a session
// pins its own model.
func (a *App) SetDefaultModel(label string) { a.defaultModelLabel = label }

// resumeSessionCmd fetches the session RunOptions.SessionID named and opens
// it exactly like picking it from the sessions overlay would (sessionOpenedMsg
// switches to the chat view and loads its history). A lookup failure surfaces
// as a status message rather than aborting startup, so the user still lands
// on a working home screen instead of a blank program.
func (a *App) resumeSessionCmd(sessionID string) tea.Cmd {
	c := a.client
	return func() tea.Msg {
		session, err := c.Session(a.ctx, sessionID)
		if err != nil || session == nil {
			return statusMsg{text: "session not found: " + sessionID}
		}
		return sessionOpenedMsg{session: session}
	}
}

// RunOptions customizes Run.
type RunOptions struct {
	DefaultModel string
	// SessionID, when set, opens directly into that session's chat view
	// instead of the home screen (the CLI's --session/--continue/--fork).
	SessionID string
}

// Run executes the interface until the user quits. A persistent goroutine
// pumps server events into the program: a tea.Cmd may only return once, so
// the event stream cannot be a command.
func Run(ctx context.Context, c *client.Client, themeName string, opts RunOptions) error {
	app := New(ctx, c, themeName)
	app.SetDefaultModel(opts.DefaultModel)
	app.resumeSessionID = opts.SessionID
	// AltScreen/MouseMode are now declared per-frame on the returned tea.View
	// (see program.View()) rather than as ProgramOptions here.
	program := tea.NewProgram(program{app: app})

	// Subscribe to every session, not just the active one: subagents run in
	// their own sessions and their activity has to reach the UI too.
	events, err := c.Events(ctx, "")
	if err != nil {
		return err
	}

	// Two goroutines sit between the wire and the main goroutine:
	//
	//   SSE ──> Aggregate ──(coalesced snapshots)──> pumpSnapshots ──> program
	//
	// Aggregate does all per-event work and emits at most one snapshot per
	// frame, so a burst of subagent traffic cannot stall a redraw. The main
	// goroutine only swaps in the snapshot. See MULTI_AGENTS.md phase 4.
	snapshots := make(chan Snapshot, 8)
	aggregatorCtx, stopAggregator := context.WithCancel(ctx)
	defer stopAggregator()
	go func() {
		defer close(snapshots)
		Aggregate(aggregatorCtx, events, snapshots, DefaultFrame)
	}()
	go pumpSnapshots(program, snapshots, nil)

	_, err = program.Run()
	return err
}

func (a *App) activeModelSet() bool { return a.activeModel != "" }

// loadStats fetches session usage for the sidebar and footer.
func (a *App) loadStats(sessionID string) tea.Cmd {
	if sessionID == "" {
		return nil
	}
	c := a.client
	return func() tea.Msg {
		stats, err := c.Stats(a.ctx, sessionID)
		if err != nil {
			return nil
		}
		return statsMsg{stats: stats}
	}
}

type statsMsg struct{ stats *client.Stats }
