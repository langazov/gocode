package tui

import (
	"encoding/json"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/langazov/gocode-go/internal/tui/client"
)

// --- shared width policy (cli/cmd/run/footer.width.ts) ----------------------

// The expectations here are transcribed from the upstream test
// (test/cli/run/footer.width.test.ts, "preserves shared dialog and statusline
// breakpoints") so a drift in either implementation shows up as a diff.
func TestFooterWidthPolicyMatchesUpstreamBreakpoints(t *testing.T) {
	narrow := footerWidthPolicy(79)
	if !narrow.DialogNarrow || narrow.ShowActivityMeta || !narrow.ShowCommandHint ||
		narrow.ShowContextHints || narrow.ContextHintLimit != 0 || narrow.ShowModel {
		t.Fatalf("width 79: %+v", narrow)
	}

	if footerWidthPolicy(65).ShowCommandHint {
		t.Fatal("width 65 is below the command-hint breakpoint")
	}
	if !footerWidthPolicy(66).ShowCommandHint {
		t.Fatal("width 66 is the command-hint breakpoint")
	}

	compact := footerWidthPolicy(80)
	if compact.DialogNarrow || !compact.ShowActivityMeta || !compact.ShowContextHints ||
		compact.ContextHintLimit != 1 || compact.ShowModel {
		t.Fatalf("width 80: %+v", compact)
	}

	model := footerWidthPolicy(120)
	if model.ContextHintLimit != 2 || !model.ShowModel {
		t.Fatalf("width 120: %+v", model)
	}

	spacious := footerWidthPolicy(150)
	if spacious.ContextHintLimit != -1 || !spacious.ShowModel {
		t.Fatalf("width 150: %+v", spacious)
	}
}

// --- usage formatting (prompt/index.tsx's usage() memo) ---------------------

func TestLocaleNumberMatchesUpstreamThresholds(t *testing.T) {
	for _, tc := range []struct {
		in   int
		want string
	}{
		{0, "0"},
		{999, "999"},
		{1000, "1.0K"},
		{159_600, "159.6K"},
		{1_000_000, "1.0M"},
		{2_350_000, "2.4M"},
	} {
		if got := localeNumber(tc.in); got != tc.want {
			t.Fatalf("localeNumber(%d) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestBuildUsageJoinsContextAndCost(t *testing.T) {
	tokens := &client.AssistantTokens{Input: 100_000, Output: 20_000, Reasoning: 5_000}
	tokens.Cache.Read = 30_000
	tokens.Cache.Write = 4_600 // 159,600 total

	got := buildUsage(tokens, 1_000_000, 4.23).String()
	if got != "159.6K (16%) · $4.23" {
		t.Fatalf("usage = %q, want %q", got, "159.6K (16%) · $4.23")
	}
}

func TestBuildUsageOmitsPercentWithoutAContextLimit(t *testing.T) {
	tokens := &client.AssistantTokens{Input: 1200, Output: 300}
	if got := buildUsage(tokens, 0, 0).String(); got != "1.5K" {
		t.Fatalf("usage = %q, want %q (no limit means no percentage upstream)", got, "1.5K")
	}
}

func TestBuildUsageKeepsCostWithoutTokens(t *testing.T) {
	if got := buildUsage(nil, 200_000, 0.5).String(); got != "$0.50" {
		t.Fatalf("usage = %q, want %q", got, "$0.50")
	}
	if got := buildUsage(nil, 200_000, 0).String(); got != "" {
		t.Fatalf("usage = %q, want empty", got)
	}
}

// sessionUsage reads the LAST assistant message that produced output tokens,
// not simply the last assistant message (findLast over `tokens.output > 0`).
func TestSessionUsageSkipsAssistantMessagesWithoutOutputTokens(t *testing.T) {
	app := newTestApp(t, "http://example.invalid")
	app.contextLimits["anthropic/claude"] = 200_000
	app.stats = &client.Stats{Cost: 1.5}
	app.timeline = []client.Message{
		assistantWithTokens(t, "anthropic", "claude", 90_000, 10_000),
		assistantWithTokens(t, "anthropic", "claude", 0, 0),
	}

	if got := app.sessionUsage().String(); got != "100.0K (50%) · $1.50" {
		t.Fatalf("usage = %q, want %q", got, "100.0K (50%) · $1.50")
	}
}

func assistantWithTokens(t *testing.T, providerID, modelID string, input, output int) client.Message {
	t.Helper()
	data, err := json.Marshal(map[string]any{
		"model":  map[string]string{"providerID": providerID, "id": modelID},
		"tokens": map[string]any{"input": input, "output": output},
	})
	if err != nil {
		t.Fatal(err)
	}
	return client.Message{ID: "msg", Type: "assistant", Data: data}
}

// --- the prompt hint row (component/prompt/index.tsx) -----------------------

// Upstream renders usage and the `tab agents` hint as two arms of ONE Switch:
// once a turn has reported usage, the usage meter replaces that hint rather
// than joining it. Only the `ctrl+p commands` hint is unconditional.
func TestChatFooterUsageReplacesTheAgentsHint(t *testing.T) {
	app := newTestApp(t, "http://example.invalid")
	app.width, app.height = 160, 40
	app.active = &client.Session{ID: "ses_1", Directory: "/tmp/project"}
	app.contextLimits["anthropic/claude"] = 200_000
	app.timeline = []client.Message{assistantWithTokens(t, "anthropic", "claude", 90_000, 10_000)}

	footer := ansi.Strip(app.chatFooter())
	if !strings.Contains(footer, "100.0K (50%)") {
		t.Fatalf("footer should show the usage meter, got %q", footer)
	}
	if strings.Contains(footer, "agents") {
		t.Fatalf("the usage meter replaces the agents hint upstream, got %q", footer)
	}
	if !strings.Contains(footer, "ctrl+p commands") {
		t.Fatalf("the commands hint is unconditional, got %q", footer)
	}
}

func TestChatFooterShowsAgentsHintBeforeAnyUsage(t *testing.T) {
	app := newTestApp(t, "http://example.invalid")
	app.width, app.height = 160, 40
	app.active = &client.Session{ID: "ses_1", Directory: "/tmp/project"}

	footer := ansi.Strip(app.chatFooter())
	if !strings.Contains(footer, "tab agents") || !strings.Contains(footer, "ctrl+p commands") {
		t.Fatalf("idle footer should show both hints, got %q", footer)
	}
}

func TestChatFooterShowsTheSessionDirectory(t *testing.T) {
	app := newTestApp(t, "http://example.invalid")
	app.width, app.height = 160, 40
	app.homeDir = "/home/tester"
	app.active = &client.Session{ID: "ses_1", Directory: "/home/tester/project"}

	if footer := ansi.Strip(app.chatFooter()); !strings.Contains(footer, "~/project") {
		t.Fatalf("footer should show the session's own directory, got %q", footer)
	}
}

// While a turn runs the left half becomes the spinner plus the two-press
// interrupt hint, and arming it swaps the wording.
func TestChatFooterInterruptHintTracksTheArmedGesture(t *testing.T) {
	app := newTestApp(t, "http://example.invalid")
	app.width, app.height = 160, 40
	app.active = &client.Session{ID: "ses_1", Directory: "/tmp/project"}
	app.busy = true

	footer := ansi.Strip(app.chatFooter())
	if !strings.Contains(footer, "esc interrupt") {
		t.Fatalf("busy footer should offer the interrupt hint, got %q", footer)
	}

	app.armInterrupt()
	footer = ansi.Strip(app.chatFooter())
	if !strings.Contains(footer, "esc again to interrupt") {
		t.Fatalf("an armed interrupt should say so, got %q", footer)
	}
}

// The hint row lives in the chat column; a narrow terminal must drop segments
// rather than spill into the sidebar (the same invariant
// TestChatFooterNeverExceedsChatWidth guards for the directory).
func TestChatFooterDropsHintsBelowTheirBreakpoints(t *testing.T) {
	app := newTestApp(t, "http://example.invalid")
	app.height = 30
	app.active = &client.Session{ID: "ses_1", Directory: "/tmp/project"}
	app.contextLimits["anthropic/claude"] = 200_000
	app.timeline = []client.Message{assistantWithTokens(t, "anthropic", "claude", 90_000, 10_000)}

	// 64 columns is under both the command-hint breakpoint (66) and compact
	// (80), so neither the commands hint nor the usage meter survives.
	app.width = 64
	footer := ansi.Strip(app.chatFooter())
	if strings.Contains(footer, "commands") {
		t.Fatalf("the commands hint is gated at 66 columns, got %q", footer)
	}
	if strings.Contains(footer, "100.0K") {
		t.Fatalf("the usage meter is gated at 80 columns, got %q", footer)
	}
	if w := lipgloss.Width(app.chatFooter()); w > app.chatWidth() {
		t.Fatalf("footer width %d exceeds chatWidth %d", w, app.chatWidth())
	}
}

// --- subagent footer (routes/session/subagent-footer.tsx) -------------------

func TestSubagentLabelReadsTheAgentOutOfTheTitle(t *testing.T) {
	if got := subagentLabel("@explore subagent for auth flow"); got != "Explore" {
		t.Fatalf("label = %q, want %q", got, "Explore")
	}
	if got := subagentLabel("some other title"); got != "Subagent" {
		t.Fatalf("label = %q, want the %q fallback", got, "Subagent")
	}
}

func TestSubagentPositionOrdersSiblingsByCreation(t *testing.T) {
	siblings := []client.Session{
		{ID: "c", TimeCreated: 300},
		{ID: "a", TimeCreated: 100},
		{ID: "b", TimeCreated: 200},
	}
	index, total := subagentPosition(siblings, "b")
	if index != 2 || total != 3 {
		t.Fatalf("position = (%d of %d), want (2 of 3)", index, total)
	}
}

func TestSubagentFooterOnlyRendersForAChildSession(t *testing.T) {
	app := newTestApp(t, "http://example.invalid")
	app.width, app.height = 160, 40
	app.active = &client.Session{ID: "ses_1", Title: "root session"}

	if got := app.subagentFooter(); got != "" {
		t.Fatalf("a root session has no subagent footer, got %q", got)
	}
}

func TestSubagentFooterShowsLabelPositionUsageAndNavigation(t *testing.T) {
	app := newTestApp(t, "http://example.invalid")
	app.width, app.height = 160, 40
	app.active = &client.Session{ID: "ses_2", ParentID: "ses_1", Title: "@explore subagent", TimeCreated: 200}
	app.subagentSiblings = []client.Session{
		{ID: "ses_1a", ParentID: "ses_1", TimeCreated: 100},
		{ID: "ses_2", ParentID: "ses_1", TimeCreated: 200},
	}
	app.contextLimits["anthropic/claude"] = 200_000
	app.stats = &client.Stats{Cost: 4.23}
	app.timeline = []client.Message{assistantWithTokens(t, "anthropic", "claude", 90_000, 10_000)}

	footer := ansi.Strip(app.subagentFooter())
	for _, want := range []string{"Explore", "(2 of 2)", "100.0K (50%) · $4.23", "Parent up", "Prev left", "Next right"} {
		if !strings.Contains(footer, want) {
			t.Fatalf("subagent footer missing %q, got:\n%s", want, footer)
		}
	}
}

func TestSubagentSiblingCycleWrapsInCreationOrder(t *testing.T) {
	app := newTestApp(t, "http://example.invalid")
	app.active = &client.Session{ID: "ses_2", ParentID: "ses_1", TimeCreated: 200}
	app.subagentSiblings = []client.Session{
		{ID: "ses_1a", ParentID: "ses_1", TimeCreated: 100},
		{ID: "ses_2", ParentID: "ses_1", TimeCreated: 200},
	}

	cmd, handled := app.cycleSubagentSibling(1)
	if !handled {
		t.Fatal("cycling should be handled with siblings present")
	}
	opened, ok := cmd().(sessionOpenedMsg)
	if !ok || opened.session.ID != "ses_1a" {
		t.Fatalf("next from the last sibling should wrap to the first, got %#v", cmd())
	}

	cmd, _ = app.cycleSubagentSibling(-1)
	opened, ok = cmd().(sessionOpenedMsg)
	if !ok || opened.session.ID != "ses_1a" {
		t.Fatalf("previous from the second of two siblings is the first, got %#v", cmd())
	}
}

func TestSubagentNavigationIsInertOnARootSession(t *testing.T) {
	app := newTestApp(t, "http://example.invalid")
	app.active = &client.Session{ID: "ses_1"}

	if _, handled := app.openParentSession(); handled {
		t.Fatal("a root session has no parent to open")
	}
	if _, handled := app.cycleSubagentSibling(1); handled {
		t.Fatal("a root session has no siblings to cycle")
	}
}

// --- sidebar footer getting-started card -----------------------------------

func TestGettingStartedCardShowsWithOnlyFreeModels(t *testing.T) {
	app := newTestApp(t, "http://example.invalid")
	app.width, app.height = 140, 40
	app.active = &client.Session{ID: "ses_1", Title: "Test"}
	app.sidebar = true
	app.providers = []client.Provider{{ID: "opencode", Name: "opencode"}}
	app.modelCosts = map[string]float64{"opencode/free-model": 0}

	view := ansi.Strip(app.sidebarView())
	for _, want := range []string{"Getting started", "Connect provider", "/connect"} {
		if !strings.Contains(view, want) {
			t.Fatalf("sidebar footer missing %q, got:\n%s", want, view)
		}
	}
}

func TestGettingStartedCardHiddenOnceAPaidProviderExists(t *testing.T) {
	app := newTestApp(t, "http://example.invalid")
	app.width, app.height = 140, 40
	app.active = &client.Session{ID: "ses_1", Title: "Test"}
	app.sidebar = true
	app.providers = []client.Provider{{ID: "anthropic", Name: "Anthropic"}}

	if view := ansi.Strip(app.sidebarView()); strings.Contains(view, "Getting started") {
		t.Fatalf("the card is for users with no connected provider, got:\n%s", view)
	}
}

// A priced model inside the bundled free provider counts too — upstream's
// has() is `id !== "opencode" || some(model.cost?.input !== 0)`.
func TestGettingStartedCardHiddenWhenTheFreeProviderHasAPricedModel(t *testing.T) {
	app := newTestApp(t, "http://example.invalid")
	app.width, app.height = 140, 40
	app.active = &client.Session{ID: "ses_1", Title: "Test"}
	app.sidebar = true
	app.providers = []client.Provider{{ID: "opencode", Name: "opencode"}}
	app.modelCosts = map[string]float64{"opencode/free": 0, "opencode/priced": 3}

	if view := ansi.Strip(app.sidebarView()); strings.Contains(view, "Getting started") {
		t.Fatalf("a priced model counts as a real provider, got:\n%s", view)
	}
}

func TestGettingStartedCardHiddenOnceDismissed(t *testing.T) {
	app := newTestApp(t, "http://example.invalid")
	app.width, app.height = 140, 40
	app.active = &client.Session{ID: "ses_1", Title: "Test"}
	app.sidebar = true
	app.providers = []client.Provider{{ID: "opencode", Name: "opencode"}}
	app.dismissedGettingStarted = true

	if view := ansi.Strip(app.sidebarView()); strings.Contains(view, "Getting started") {
		t.Fatalf("a dismissed card stays dismissed, got:\n%s", view)
	}
}

// The card comes out of the sidebar's fixed row budget, so the panel must
// still land at exactly its reserved footprint with the card showing.
func TestSidebarKeepsItsFootprintWithTheGettingStartedCard(t *testing.T) {
	for _, height := range []int{24, 30, 40, 53} {
		app := newTestApp(t, "http://example.invalid")
		app.width, app.height = 140, height
		app.active = &client.Session{ID: "ses_1", Title: "Test"}
		app.sidebar = true
		app.providers = []client.Provider{{ID: "opencode", Name: "opencode"}}

		lines := strings.Split(app.sidebarView(), "\n")
		if len(lines) != height {
			t.Fatalf("height=%d: sidebar rendered %d lines, want exactly %d", height, len(lines), height)
		}
		for i, line := range lines {
			if w := lipgloss.Width(line); w != app.sidebarWidth() {
				t.Fatalf("height=%d line %d: width = %d, want %d", height, i, w, app.sidebarWidth())
			}
		}
	}
}

// --- the interrupted notice ------------------------------------------------

// The runner tags an interruption as its own error type (the port's
// MessageAbortedError), so the UI can tell it apart from a real failure. Before
// that tagging, the fallback probe never matched the runner's own wording.
func TestMessageAbortedRecognisesTheRunnersOwnWording(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  *client.AssistantError
		want bool
	}{
		{"tagged", &client.AssistantError{Type: "aborted", Message: "context canceled"}, true},
		{"legacy row, runner wording", &client.AssistantError{Type: "unknown", Message: "context canceled"}, true},
		{"legacy row, upstream wording", &client.AssistantError{Type: "unknown", Message: "request aborted"}, true},
		{"a real failure", &client.AssistantError{Type: "unknown", Message: "503 upstream unavailable"}, false},
		{"no error", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := messageAborted(client.AssistantData{Error: tc.err}); got != tc.want {
				t.Fatalf("messageAborted = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestFooterReportsAnInterruptedTurn(t *testing.T) {
	app := newTestApp(t, "http://example.invalid")
	app.width, app.height = 160, 40
	app.homeDir = "/home/tester"
	app.active = &client.Session{ID: "ses_1", Directory: "/home/tester/project"}
	app.timeline = []client.Message{abortedAssistant(t)}

	footer := ansi.Strip(app.chatFooter())
	if !strings.Contains(footer, "interrupted") {
		t.Fatalf("footer should report the interruption, got %q", footer)
	}
	// Upstream's hint row is a Switch: the notice arm replaces the directory.
	if strings.Contains(footer, "~/project") {
		t.Fatalf("the notice arm replaces the directory upstream, got %q", footer)
	}
}

// The notice stands until the next turn starts — that is the whole point of
// deriving it instead of running it off a timer.
func TestInterruptedNoticeClearsWhenTheNextTurnStarts(t *testing.T) {
	app := newTestApp(t, "http://example.invalid")
	app.width, app.height = 160, 40
	app.active = &client.Session{ID: "ses_1", Directory: "/tmp/project"}
	app.timeline = []client.Message{abortedAssistant(t)}

	app.busy = true
	if footer := ansi.Strip(app.chatFooter()); strings.Contains(footer, "interrupted") &&
		!strings.Contains(footer, "esc interrupt") {
		t.Fatalf("a running turn shows the interrupt hint, not the old notice, got %q", footer)
	}

	app.busy = false
	app.timeline = append(app.timeline, assistantWithTokens(t, "anthropic", "claude", 10, 5))
	if footer := ansi.Strip(app.chatFooter()); strings.Contains(footer, "interrupted") {
		t.Fatalf("a later completed turn should clear the notice, got %q", footer)
	}
}

// Upstream suppresses the error block for an abort (`error.name !==
// "MessageAbortedError"`) and lets the settlement line's marker carry it.
func TestAbortedMessageRendersTheMarkerNotAnErrorBlock(t *testing.T) {
	app := newTestApp(t, "http://example.invalid")
	app.width, app.height = 160, 40
	app.active = &client.Session{ID: "ses_1"}

	message := abortedAssistant(t)
	data, err := client.DecodeAssistant(message.Data)
	if err != nil {
		t.Fatal(err)
	}
	block, _, _ := app.renderAssistant(message, data, true)
	rendered := ansi.Strip(block)

	if strings.Contains(rendered, "context canceled") {
		t.Fatalf("an interruption is not an error to report, got:\n%s", rendered)
	}
	if !strings.Contains(rendered, "· interrupted") {
		t.Fatalf("the settlement line should carry the interrupted marker, got:\n%s", rendered)
	}
}

func abortedAssistant(t *testing.T) client.Message {
	t.Helper()
	data, err := json.Marshal(map[string]any{
		"model":   map[string]string{"providerID": "anthropic", "id": "claude"},
		"content": []map[string]any{{"type": "text", "text": "partial"}},
		"error":   map[string]any{"type": "aborted", "message": "context canceled"},
		"time":    map[string]any{"created": 1, "completed": 2},
	})
	if err != nil {
		t.Fatal(err)
	}
	return client.Message{ID: "msg_aborted", Type: "assistant", TimeCreated: 1, Data: data}
}

// --- sidebar Context section ------------------------------------------------

func TestGroupDigitsMatchesToLocaleString(t *testing.T) {
	for _, tc := range []struct {
		in   int
		want string
	}{{0, "0"}, {999, "999"}, {1000, "1,000"}, {159600, "159,600"}, {1234567, "1,234,567"}} {
		if got := groupDigits(tc.in); got != tc.want {
			t.Fatalf("groupDigits(%d) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// The Context section reports the *last* assistant turn's own context, not a
// session total — this port used to sum every message and divide by a
// hardcoded 200000 window.
func TestSidebarContextReportsTheLastTurnAgainstItsModelLimit(t *testing.T) {
	app := newTestApp(t, "http://example.invalid")
	app.contextLimits["anthropic/claude"] = 200_000
	app.timeline = []client.Message{
		assistantWithTokens(t, "anthropic", "claude", 150_000, 10_000),
		assistantWithTokens(t, "anthropic", "claude", 40_000, 10_000),
	}

	got := app.sidebarContext()
	if got.tokens != 50_000 {
		t.Fatalf("tokens = %d, want the last turn's 50000 (not the sum)", got.tokens)
	}
	if got.percent != 25 {
		t.Fatalf("percent = %d, want 25", got.percent)
	}
}

func TestSidebarContextIsZeroPercentWithoutAModelLimit(t *testing.T) {
	app := newTestApp(t, "http://example.invalid")
	app.timeline = []client.Message{assistantWithTokens(t, "anthropic", "unknown-model", 40_000, 10_000)}

	got := app.sidebarContext()
	if got.tokens != 50_000 {
		t.Fatalf("tokens = %d, want 50000", got.tokens)
	}
	if got.percent != 0 {
		t.Fatalf("an unknown window renders 0%%, not a guess, got %d", got.percent)
	}
}

func TestSidebarRendersLiveContextAndSpend(t *testing.T) {
	app := newTestApp(t, "http://example.invalid")
	app.width, app.height = 140, 40
	app.active = &client.Session{ID: "ses_1", Title: "Test"}
	app.sidebar = true
	app.contextLimits["anthropic/claude"] = 200_000
	app.timeline = []client.Message{assistantWithTokens(t, "anthropic", "claude", 90_000, 9_600)}
	app.stats = &client.Stats{Cost: 4.23}

	view := ansi.Strip(app.sidebarView())
	for _, want := range []string{"99,600 tokens", "50% used", "$4.23 spent"} {
		if !strings.Contains(view, want) {
			t.Fatalf("sidebar missing %q, got:\n%s", want, view)
		}
	}
}

// The sidebar's "spent" is server-side state the timeline does not carry, so a
// timeline refetch has to pull the stats with it. Without this it moved only
// on the 10-second reconciliation tick, which is what made the sidebar look
// frozen while a turn streamed.
func TestTimelineRefetchAlsoRefreshesStats(t *testing.T) {
	api, server := newMockAPI(t)
	app := newTestApp(t, server.URL)
	openSession(t, app)

	before := api.statsCalls
	drive(t, app, messagesMsg{sessionID: app.active.ID, messages: nil})
	if api.statsCalls <= before {
		t.Fatalf("a timeline refetch should refresh the stats, calls went %d -> %d", before, api.statsCalls)
	}
}
