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

	"github.com/anomalyco/opencode-go/internal/global"
	"github.com/anomalyco/opencode-go/internal/tui/client"
	"github.com/anomalyco/opencode-go/internal/tui/theme"

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
		windowTitle:       "OpenCode",
		thinkingMode:      "hide",
		expandedReasoning: map[string]bool{},
	}
}

type tickMsg time.Time

type leaderTimeoutMsg struct{}

func (a *App) Init() tea.Cmd {
	a.windowTitle = a.desiredWindowTitle()
	cmds := []tea.Cmd{a.loadSessionsCmd(), a.loadCatalogCmd(), a.loadMCPCmd(), a.loadAgentsCmd(0), a.tick()}
	if a.resumeSessionID != "" {
		cmds = append(cmds, a.resumeSessionCmd(a.resumeSessionID))
	}
	return tea.Batch(cmds...)
}

// desiredWindowTitle mirrors app.tsx's terminal-title effect: "OpenCode" on
// the home route or while a session's title is still its creation
// placeholder, else "OC | <title>" (truncated at 40 chars). TS's default-
// title check is a regex over its own "New session - <ISO time>" format;
// this port's placeholder is `filepath.Base(directory)` instead (see
// internal/session/service.go), so the check is against that.
func (a *App) desiredWindowTitle() string {
	if a.view != viewChat || a.active == nil {
		return "OpenCode"
	}
	title := strings.TrimSpace(a.active.Title)
	if title == "" || title == filepath.Base(a.active.Directory) {
		return "OpenCode"
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
	a.busy = node.Busy
	a.streaming = node.Text
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
}
type mcpMsg struct{ servers []client.MCPServer }

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
type statusMsg struct{ text string }

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

// loadCatalogCmd fetches display names for the current model's meta row.
// Both calls are best-effort; the meta row falls back to raw IDs.
func (a *App) loadCatalogCmd() tea.Cmd {
	c := a.client
	return func() tea.Msg {
		models, _ := c.Models(a.ctx)
		providers, _ := c.Providers(a.ctx)
		return catalogMsg{models: models, providers: providers}
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
	cmd := a.update(msg)
	a.syncWindowTitle()
	return cmd
}

func (a *App) update(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		a.width, a.height = msg.Width, msg.Height
		a.input.SetWidth(a.inputWidth())
		return nil
	case tickMsg:
		cmds := []tea.Cmd{a.tick(), a.loadMCPCmd()}
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
		a.modelNames = map[string]string{}
		a.contextLimits = map[string]int{}
		a.modelCosts = map[string]float64{}
		for _, model := range msg.models {
			a.modelNames[model.ProviderID+"/"+model.ID] = model.Name
			a.contextLimits[model.ProviderID+"/"+model.ID] = model.ContextLimit
			a.modelCosts[model.ProviderID+"/"+model.ID] = model.CostInput
		}
		a.providerNames = map[string]string{}
		a.providers = msg.providers
		// settlementLine resolves a model's display name through this
		// catalog, and messages rendered before it arrived cached the raw
		// model ID.
		a.invalidateRenderCache()
		for _, provider := range msg.providers {
			a.providerNames[provider.ID] = provider.Name
		}
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
			a.busy = hasUnfinishedAssistant(a.timeline)
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
	case statusMsg:
		a.statusMsg = msg.text
		isError := strings.HasPrefix(msg.text, "failed") ||
			strings.Contains(msg.text, "failed:") ||
			strings.Contains(msg.text, "error")
		return a.showToast(msg.text, isError)
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
	if msg.String() == "@" {
		a.input.InsertString("@")
		a.openFileMentions()
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
	case "enter":
		text := strings.TrimSpace(a.input.Value())
		if text == "" {
			return nil
		}
		a.input.Reset()
		if strings.HasPrefix(text, "/") {
			return a.runSlashCommand(strings.TrimPrefix(text, "/"))
		}
		a.history.Append(text)
		if a.view == viewHome {
			return a.createAndPrompt(text)
		}
		return a.sendPrompt(a.active.ID, text)
	}

	var cmd tea.Cmd
	a.input, cmd = a.input.Update(msg)
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
	return func() tea.Msg {
		session, err := c.CreateSession(a.ctx, client.CreateInput{Directory: dir})
		if err != nil {
			return statusMsg{text: "failed to create session: " + err.Error()}
		}
		return sessionOpenedMsg{session: session}
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
	return func() tea.Msg {
		session, err := c.CreateSession(a.ctx, client.CreateInput{Directory: dir})
		if err != nil {
			return statusMsg{text: "failed to create session: " + err.Error()}
		}
		if _, err := c.Prompt(a.ctx, session.ID, text); err != nil {
			return statusMsg{text: "prompt failed: " + err.Error()}
		}
		return openedWithPrompt{session: session, text: text}
	}
}

func (a *App) sendPrompt(sessionID, text string) tea.Cmd {
	c := a.client
	return func() tea.Msg {
		if _, err := c.Prompt(a.ctx, sessionID, text); err != nil {
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

// hasUnfinishedAssistant reports whether the last assistant message is still
// streaming, which is how this port infers `busy` from a message refetch (TS
// syncs an explicit `session_status` projection instead; there is no Go
// equivalent yet).
//
// A finish reason is not the only way a turn settles, and keying on it alone
// was a real bug: projectStepFailed — the settlement for an *interrupted* or
// provider-failed turn — records `error` and `time.completed` but never a
// `finish`. So after a double-escape interrupt the aggregator correctly
// cleared busy, the dirty snapshot refetched the messages, and this said the
// turn was still running, turning busy straight back on. The spinner never
// stopped. All three of these mark a settled turn.
func hasUnfinishedAssistant(timeline []client.Message) bool {
	for i := len(timeline) - 1; i >= 0; i-- {
		if timeline[i].Type != "assistant" {
			continue
		}
		data, err := client.DecodeAssistant(timeline[i].Data)
		if err != nil {
			continue
		}
		return data.Finish == "" && data.Time.Completed == 0 && data.Error == nil
	}
	return false
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
func (a *App) runSlashCommand(name string) tea.Cmd {
	name = strings.TrimSpace(strings.TrimPrefix(name, "/"))
	commands := a.commandsRegistry()
	for _, command := range commands {
		if command.label == name {
			return command.action
		}
	}
	for _, command := range commands {
		if strings.HasPrefix(command.label, name+".") {
			return command.action
		}
	}
	return staticMsg(statusMsg{text: "unknown command: /" + name})
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

// inputWidth keeps the shared editor inside both the home prompt box and the
// session prompt box: each box's own *declared* width (promptMaxWidth(a.width)-1
// for home, chatWidth()-1 for the session — see their promptBox call sites)
// minus promptBox's paddingLeft(2)+paddingRight(2) (the 1-column left border
// renders outside the declared width, so it isn't subtracted again here).
func (a *App) inputWidth() int {
	w := min(promptMaxWidth(a.width)-5, a.chatWidth()-5)
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
