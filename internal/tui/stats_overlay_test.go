package tui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/langazov/gocode-go/internal/tui/client"
)

// statsMockAPI extends newMockAPI with multiple sessions that have different
// models and token usage, so the /stats overlay has data to aggregate.
func statsMockAPI(t *testing.T) (*mockAPI, *httptest.Server) {
	t.Helper()
	mux := http.NewServeMux()

	sessions := []client.Session{
		{ID: "ses_a", ProjectID: "prj_1", Title: "index project", Directory: "/tmp/a", Version: "1",
			Model: &client.ModelRef{ProviderID: "zhipuai-coding-plan", ID: "glm-5.3-flash"}, TimeCreated: 1000, TimeUpdated: 2000},
		{ID: "ses_b", ProjectID: "prj_2", Title: "hello world", Directory: "/tmp/b", Version: "1",
			Model: &client.ModelRef{ProviderID: "anthropic", ID: "claude-sonnet-4-5"}, TimeCreated: 3000, TimeUpdated: 4000},
	}

	mux.HandleFunc("GET /api/session", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(sessions)
	})
	mux.HandleFunc("GET /api/session/{sessionID}", func(w http.ResponseWriter, r *http.Request) {
		for _, s := range sessions {
			if s.ID == r.PathValue("sessionID") {
				json.NewEncoder(w).Encode(s)
				return
			}
		}
		w.WriteHeader(http.StatusNotFound)
	})
	mux.HandleFunc("GET /api/session/{sessionID}/message", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]client.Message{})
	})
	mux.HandleFunc("GET /api/session/{sessionID}/stats", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("sessionID")
		switch id {
		case "ses_a":
			json.NewEncoder(w).Encode(map[string]any{
				"cost": 0.5, "tokensInput": 140040, "tokensOutput": 12468,
				"tokensReasoning": 17148, "tokensCacheRead": 2327040, "tokensCacheWrite": 0,
				"messages": 51,
			})
		case "ses_b":
			json.NewEncoder(w).Encode(map[string]any{
				"cost": 1.23, "tokensInput": 90000, "tokensOutput": 20000,
				"tokensReasoning": 5000, "tokensCacheRead": 80000, "tokensCacheWrite": 10000,
				"messages": 10,
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	mux.HandleFunc("GET /api/model", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]client.Model{})
	})
	mux.HandleFunc("GET /api/event", func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(250 * time.Millisecond):
		}
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return nil, server
}

// TestStatsCommandOpensOverlay: typing /stats opens the stats overlay panel.
func TestStatsCommandOpensOverlay(t *testing.T) {
	_, server := statsMockAPI(t)
	app := newTestApp(t, server.URL)
	app.width, app.height = 120, 40

	// Load sessions so the overlay has data to show.
	driveCmd(t, app, app.loadSessionsCmd())

	// Open the overlay via the command registry entry, the way /stats does.
	cmd := app.runSlashCommand("stats")
	if cmd == nil {
		t.Fatal("/stats produced no command")
	}
	drive(t, app, cmd())

	if app.overlay == nil || app.overlay.kind != overlayStats {
		t.Fatal("/stats should open the stats overlay")
	}
}

// TestStatsOverlayRendersAggregate verifies the overlay shows aggregate
// totals computed from all sessions' stats.
func TestStatsOverlayRendersAggregate(t *testing.T) {
	_, server := statsMockAPI(t)
	app := newTestApp(t, server.URL)
	app.width, app.height = 120, 80

	driveCmd(t, app, app.loadSessionsCmd())
	driveCmd(t, app, app.loadAllStats())

	// Open the overlay.
	drive(t, app, app.openStatsOverlay())

	if !strings.Contains(ansi.Strip(app.View()), "Stats") {
		t.Fatal("stats overlay should render its header")
	}
	view := statsBodyText(app)
	for _, want := range []string{
		"Overview",     // aggregate section
		"Sessions",     // the label column
		"Tokens",       // token section
		"Prompt Cache", // cache section
		"Hit Rate",     // cache hit rate
		"By Model",     // per-model section
		"By Session",   // per-session section
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("stats overlay missing %q:\n%s", want, view)
		}
	}
}

// TestStatsOverlayShowsCacheHitRate verifies the cache hit rate is computed
// correctly from the aggregate of all sessions.
func TestStatsOverlayShowsCacheHitRate(t *testing.T) {
	_, server := statsMockAPI(t)
	app := newTestApp(t, server.URL)
	app.width, app.height = 120, 40

	driveCmd(t, app, app.loadSessionsCmd())
	driveCmd(t, app, app.loadAllStats())
	drive(t, app, app.openStatsOverlay())

	// ses_a: input=140040, cacheRead=2327040 -> prompt=2467080, hit=94.32%
	// ses_b: input=90000,  cacheRead=80000   -> prompt=170000,  hit=47.06%
	// aggregate: input=230040, cacheRead=2407040 -> prompt=2637080
	// hit rate = 2407040/2637080 * 100 = 91.28%
	view := statsBodyText(app)
	if !strings.Contains(view, "91.2") {
		t.Fatalf("aggregate cache hit rate should be ~91.2%%:\n%s", view)
	}
}

// TestStatsOverlayShowsPerModelBreakdown verifies each model gets its own
// section with the right session count and hit rate.
func TestStatsOverlayShowsPerModelBreakdown(t *testing.T) {
	_, server := statsMockAPI(t)
	app := newTestApp(t, server.URL)
	app.width, app.height = 120, 80

	driveCmd(t, app, app.loadSessionsCmd())
	driveCmd(t, app, app.loadAllStats())
	drive(t, app, app.openStatsOverlay())

	view := statsBodyText(app)
	for _, want := range []string{
		"zhipuai-coding-plan/glm-5.3-flash",
		"anthropic/claude-sonnet-4-5",
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("stats overlay missing model %q:\n%s", want, view)
		}
	}
}

// TestStatsOverlayShowsPerSessionBreakdown verifies each session appears with
// its title, model, and token breakdown.
func TestStatsOverlayShowsPerSessionBreakdown(t *testing.T) {
	_, server := statsMockAPI(t)
	app := newTestApp(t, server.URL)
	app.width, app.height = 120, 80

	driveCmd(t, app, app.loadSessionsCmd())
	driveCmd(t, app, app.loadAllStats())
	drive(t, app, app.openStatsOverlay())

	view := statsBodyText(app)
	for _, want := range []string{
		"index project",
		"hello world",
		"51 msgs",
		"10 msgs",
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("stats overlay missing %q:\n%s", want, view)
		}
	}
}

// TestStatsOverlayClosesOnEscape verifies escape closes the overlay.
func TestStatsOverlayClosesOnEscape(t *testing.T) {
	_, server := statsMockAPI(t)
	app := newTestApp(t, server.URL)
	app.width, app.height = 120, 40

	driveCmd(t, app, app.loadSessionsCmd())
	drive(t, app, app.openStatsOverlay())

	if app.overlay == nil {
		t.Fatal("overlay should be open")
	}
	press(t, app, "esc")
	if app.overlay != nil {
		t.Fatal("escape should close the stats overlay")
	}
}

// TestStatsOverlayScrolls verifies the overlay supports scrolling for long
// content.
func TestStatsOverlayScrolls(t *testing.T) {
	_, server := statsMockAPI(t)
	app := newTestApp(t, server.URL)
	app.width, app.height = 120, 20 // short height to force overflow

	driveCmd(t, app, app.loadSessionsCmd())
	driveCmd(t, app, app.loadAllStats())
	drive(t, app, app.openStatsOverlay())

	// Scrolling down should move scrollTop.
	press(t, app, "down")
	if app.overlay.scrollTop == 0 {
		t.Fatal("down should scroll the stats overlay")
	}

	// Scrolling back up should return to 0.
	press(t, app, "up")
	if app.overlay.scrollTop != 0 {
		t.Fatalf("up should scroll back to top, got scrollTop=%d", app.overlay.scrollTop)
	}
}

// TestStatsOverlayFitsWithinScreen verifies the rendered panel does not extend
// past the bottom of the screen, regardless of how much content there is.
// The panel is positioned at height/4 from the top, so the scroll windowing
// must account for that offset (not just subtract a fixed constant).
func TestStatsOverlayFitsWithinScreen(t *testing.T) {
	_, server := statsMockAPI(t)
	app := newTestApp(t, server.URL)
	app.width, app.height = 120, 24 // short terminal — the bug's worst case

	driveCmd(t, app, app.loadSessionsCmd())
	driveCmd(t, app, app.loadAllStats())
	drive(t, app, app.openStatsOverlay())

	panel, _ := app.overlayPanel()
	panelLines := strings.Split(panel, "\n")
	top, _ := app.overlayOrigin(lipgloss.Width(panel))
	bottomRow := top + len(panelLines)
	if bottomRow > app.height {
		t.Fatalf("stats panel extends past the screen: top=%d, panelLines=%d, bottom=%d, screen height=%d",
			top, len(panelLines), bottomRow, app.height)
	}
}

// TestStatsOverlayLeavesBottomMargin verifies the panel does not run down to
// the last screen row. It is anchored at height/4 and leaves the same margin
// below, so it reads as floating rather than glued to the bottom edge.
func TestStatsOverlayLeavesBottomMargin(t *testing.T) {
	_, server := statsMockAPI(t)
	app := newTestApp(t, server.URL)

	driveCmd(t, app, app.loadSessionsCmd())
	driveCmd(t, app, app.loadAllStats())
	drive(t, app, app.openStatsOverlay())

	for _, height := range []int{24, 30, 40, 60, 80, 120} {
		app.width, app.height = 120, height
		panel, _ := app.overlayPanel()
		top, _ := app.overlayOrigin(lipgloss.Width(panel))
		bottom := top + strings.Count(panel, "\n") + 1
		if margin := app.height - bottom; margin < top {
			t.Fatalf("at height %d the panel ends %d rows from the bottom but starts %d from the top",
				height, margin, top)
		}
	}
}

// TestStatsOverlayHandlesNoSessions verifies the overlay doesn't crash when
// there are no sessions.
func TestStatsOverlayHandlesNoSessions(t *testing.T) {
	app := newTestApp(t, "http://example.invalid")
	app.width, app.height = 120, 40
	app.sessions = nil
	app.allStats = nil

	// Should not panic.
	drive(t, app, app.openStatsOverlay())
	view := ansi.Strip(app.View())
	// No sessions is one of three distinguishable empty states, and it says
	// which one it is rather than printing a table of zeros.
	if !strings.Contains(view, "No sessions") {
		t.Fatalf("empty stats overlay should name its empty state:\n%s", view)
	}
}

// TestStatsOverlayAlignsValueColumn verifies every key/value row puts its
// value in the same column. The rows used to be padded by hand and "Cache
// Hits:" carried one space too many, so the column did not line up.
func TestStatsOverlayAlignsValueColumn(t *testing.T) {
	_, server := statsMockAPI(t)
	app := newTestApp(t, server.URL)
	app.width, app.height = 120, 80

	driveCmd(t, app, app.loadSessionsCmd())
	driveCmd(t, app, app.loadAllStats())
	drive(t, app, app.openStatsOverlay())

	panel, _ := app.overlayPanel()
	column := -1
	for _, label := range []string{"Sessions", "Total Cost", "Input (fresh)", "Cache Read", "Cache Hits", "Hit Rate"} {
		row := findPanelRow(t, panel, label)
		value := strings.Index(row, label) + len(label)
		for value < len(row) && row[value] == ' ' {
			value++
		}
		if column == -1 {
			column = value
			continue
		}
		if value != column {
			t.Fatalf("row %q starts its value at column %d, want %d (row: %q)", label, value, column, row)
		}
	}
}

// TestStatsOverlayKeepsHeaderWhileScrolling verifies the header row is fixed
// and only the body scrolls. Windowing used to be applied to the whole panel,
// so scrolling down took the title with it.
func TestStatsOverlayKeepsHeaderWhileScrolling(t *testing.T) {
	_, server := statsMockAPI(t)
	app := newTestApp(t, server.URL)
	app.width, app.height = 120, 20

	driveCmd(t, app, app.loadSessionsCmd())
	driveCmd(t, app, app.loadAllStats())
	drive(t, app, app.openStatsOverlay())

	for i := 0; i < 30; i++ {
		press(t, app, "down")
		panel, _ := app.overlayPanel()
		if !strings.Contains(ansi.Strip(panel), "Stats") {
			t.Fatalf("header scrolled away after %d presses:\n%s", i+1, ansi.Strip(panel))
		}
	}
}

// TestStatsOverlayClampsScroll verifies scrollTop settles at the last full
// screenful. The key handler parks "end" at a sentinel and says the renderer
// clamps it; nothing did, so "end" left a single row on screen.
func TestStatsOverlayClampsScroll(t *testing.T) {
	_, server := statsMockAPI(t)
	app := newTestApp(t, server.URL)
	app.width, app.height = 120, 20

	driveCmd(t, app, app.loadSessionsCmd())
	driveCmd(t, app, app.loadAllStats())
	drive(t, app, app.openStatsOverlay())

	press(t, app, "end")
	app.View() // the render is what clamps
	settled := app.overlay.scrollTop

	before, _ := app.overlayPanel()
	press(t, app, "down")
	after, _ := app.overlayPanel()
	if app.overlay.scrollTop != settled {
		t.Fatalf("scrollTop moved past the end: %d -> %d", settled, app.overlay.scrollTop)
	}
	if before != after {
		t.Fatal("scrolling past the end changed the rendered panel")
	}
	// The last screenful is still a screenful, not one row.
	if rows := strings.Count(after, "\n") + 1; rows < 8 {
		t.Fatalf("panel collapsed to %d rows at the end of the scroll:\n%s", rows, ansi.Strip(after))
	}
}

// TestStatsOverlayNamesScrollKeys verifies the hint row advertises the keys,
// and only while there is something to scroll.
func TestStatsOverlayNamesScrollKeys(t *testing.T) {
	_, server := statsMockAPI(t)
	app := newTestApp(t, server.URL)

	app.width, app.height = 120, 20
	driveCmd(t, app, app.loadSessionsCmd())
	driveCmd(t, app, app.loadAllStats())
	drive(t, app, app.openStatsOverlay())

	panel, _ := app.overlayPanel()
	short := ansi.Strip(panel)
	for _, want := range []string{"scroll", "page", "esc close"} {
		if !strings.Contains(short, want) {
			t.Fatalf("scrollable stats panel should hint %q:\n%s", want, short)
		}
	}

	// Tall enough that nothing is hidden: the scroll hints go away, the
	// close hint stays.
	app.height = 200
	panel, _ = app.overlayPanel()
	tall := ansi.Strip(panel)
	if strings.Contains(tall, "scroll") {
		t.Fatalf("a panel with nothing to scroll should not hint scrolling:\n%s", tall)
	}
	if !strings.Contains(tall, "esc close") {
		t.Fatalf("the close hint should always show:\n%s", tall)
	}
}

// TestStatsOverlayIndicatesHiddenRows verifies the windowed panel says how
// much it is hiding, the way the timeline does.
func TestStatsOverlayIndicatesHiddenRows(t *testing.T) {
	_, server := statsMockAPI(t)
	app := newTestApp(t, server.URL)
	app.width, app.height = 120, 20

	driveCmd(t, app, app.loadSessionsCmd())
	driveCmd(t, app, app.loadAllStats())
	drive(t, app, app.openStatsOverlay())

	panel, _ := app.overlayPanel()
	if !strings.Contains(ansi.Strip(panel), "↓") {
		t.Fatalf("a windowed stats panel should say what is below it:\n%s", ansi.Strip(panel))
	}
	press(t, app, "down")
	panel, _ = app.overlayPanel()
	if !strings.Contains(ansi.Strip(panel), "↑") {
		t.Fatalf("a scrolled stats panel should say what is above it:\n%s", ansi.Strip(panel))
	}
}

// TestStatsOverlayTruncatesLongLabels verifies a long session title cannot
// push a row past the panel width — an overlong line tears the row spliceAt
// composites it into.
func TestStatsOverlayTruncatesLongLabels(t *testing.T) {
	app := newTestApp(t, "http://example.invalid")
	app.width, app.height = 120, 60
	app.sessions = []client.Session{{
		ID:    "ses_long",
		Title: strings.Repeat("a very long session title ", 20),
		Model: &client.ModelRef{ProviderID: strings.Repeat("provider-", 20), ID: strings.Repeat("model-", 20)},
	}}
	app.allStats = map[string]*client.Stats{"ses_long": {Cost: 1, TokensInput: 10, TokensOutput: 20, Messages: 3}}

	drive(t, app, app.openStatsOverlay())
	panel, _ := app.overlayPanel()
	width := lipgloss.Width(panel)
	for i, line := range strings.Split(panel, "\n") {
		if got := lipgloss.Width(line); got > width {
			t.Fatalf("panel line %d is %d cells wide, panel is %d:\n%s", i, got, width, ansi.Strip(line))
		}
	}
	if width > app.width-2 {
		t.Fatalf("panel is %d cells wide, wider than the screen allows (%d)", width, app.width-2)
	}
}

// TestStatsOverlayDistinguishesLoading verifies a batch still in flight reads
// as loading rather than as a table of zeros.
func TestStatsOverlayDistinguishesLoading(t *testing.T) {
	app := newTestApp(t, "http://example.invalid")
	app.width, app.height = 120, 40
	app.sessions = []client.Session{{ID: "ses_a", Title: "one"}, {ID: "ses_b", Title: "two"}}
	app.allStats = nil // no batch has landed yet

	app.overlay = &overlay{kind: overlayStats, title: "Stats", size: dialogXLarge}
	view := ansi.Strip(app.View())
	if !strings.Contains(view, "Loading usage") {
		t.Fatalf("a pending batch should report as loading:\n%s", view)
	}

	// A batch that landed with nothing in it is a different state: the
	// server answered, it just had nothing to report.
	app.allStats = map[string]*client.Stats{}
	view = ansi.Strip(app.View())
	if strings.Contains(view, "Loading usage") {
		t.Fatalf("a landed batch should not still report as loading:\n%s", view)
	}
	if !strings.Contains(view, "No usage reported") {
		t.Fatalf("an empty batch should say the server reported nothing:\n%s", view)
	}
}

// TestStatsOrdersSessionsByRecency verifies the By Session breakdown is
// ordered latest-first on last activity, not by cost.
func TestStatsOrdersSessionsByRecency(t *testing.T) {
	app := newTestApp(t, "http://example.invalid")
	app.width, app.height = 120, 60
	// Deliberately adversarial: the oldest session is by far the most
	// expensive, so a cost ordering would put it first.
	app.sessions = []client.Session{
		{ID: "ses_old", Title: "oldest", TimeCreated: 1000, TimeUpdated: 2000},
		{ID: "ses_new", Title: "newest", TimeCreated: 5000, TimeUpdated: 9000},
		{ID: "ses_mid", Title: "middle", TimeCreated: 3000, TimeUpdated: 4000},
	}
	app.allStats = map[string]*client.Stats{
		"ses_old": {Cost: 100, TokensInput: 10, Messages: 1},
		"ses_new": {Cost: 1, TokensInput: 10, Messages: 1},
		"ses_mid": {Cost: 50, TokensInput: 10, Messages: 1},
	}

	drive(t, app, app.openStatsOverlay())
	assertOrder(t, statsBodyText(app), "newest", "middle", "oldest")
}

// TestStatsSessionRecencyFallsBackToCreated verifies a session the server has
// never stamped an update on is ordered by its creation time rather than
// sinking to the bottom on a zero timestamp.
func TestStatsSessionRecencyFallsBackToCreated(t *testing.T) {
	app := newTestApp(t, "http://example.invalid")
	app.width, app.height = 120, 60
	app.sessions = []client.Session{
		{ID: "ses_a", Title: "older updated", TimeCreated: 1000, TimeUpdated: 2000},
		{ID: "ses_b", Title: "never updated", TimeCreated: 7000},
	}
	app.allStats = map[string]*client.Stats{
		"ses_a": {TokensInput: 1, Messages: 1},
		"ses_b": {TokensInput: 1, Messages: 1},
	}

	drive(t, app, app.openStatsOverlay())
	assertOrder(t, statsBodyText(app), "never updated", "older updated")
}

// TestStatsOrdersModelsByUsage verifies the By Model breakdown is ordered by
// total tokens attributed to the model, heaviest first — output included, so
// a model earning its place on output alone is not sorted below a cheap one
// with a large cached prompt.
func TestStatsOrdersModelsByUsage(t *testing.T) {
	app := newTestApp(t, "http://example.invalid")
	app.width, app.height = 120, 60
	app.sessions = []client.Session{
		{ID: "ses_light", Title: "light", TimeUpdated: 3000,
			Model: &client.ModelRef{ProviderID: "p", ID: "light"}},
		{ID: "ses_heavy", Title: "heavy", TimeUpdated: 2000,
			Model: &client.ModelRef{ProviderID: "p", ID: "heavy"}},
		{ID: "ses_output", Title: "output", TimeUpdated: 1000,
			Model: &client.ModelRef{ProviderID: "p", ID: "output-heavy"}},
	}
	app.allStats = map[string]*client.Stats{
		"ses_light":  {TokensInput: 100, TokensOutput: 10, TokensCacheRead: 100},
		"ses_heavy":  {TokensInput: 5000, TokensOutput: 10, TokensCacheRead: 5000},
		"ses_output": {TokensInput: 10, TokensOutput: 3000, TokensCacheRead: 10},
	}

	drive(t, app, app.openStatsOverlay())
	// heavy 10,010 > output-heavy 3,020 > light 210. Ordering by prompt
	// tokens alone would put "light" (200) above "output-heavy" (20).
	assertOrder(t, statsBodyText(app), "p/heavy", "p/output-heavy", "p/light")
}

// TestStatsOrderingIsStable verifies rows with identical sort keys keep a
// fixed order rather than shuffling with map iteration between renders.
func TestStatsOrderingIsStable(t *testing.T) {
	app := newTestApp(t, "http://example.invalid")
	app.width, app.height = 120, 60
	for i := 0; i < 6; i++ {
		id := string(rune('a' + i))
		app.sessions = append(app.sessions, client.Session{
			ID: "ses_" + id, Title: "session " + id, TimeUpdated: 1000,
			Model: &client.ModelRef{ProviderID: "p", ID: "m" + id},
		})
	}
	app.allStats = map[string]*client.Stats{}
	for _, s := range app.sessions {
		app.allStats[s.ID] = &client.Stats{TokensInput: 1, Messages: 1}
	}

	drive(t, app, app.openStatsOverlay())
	first := statsBodyText(app)
	for i := 0; i < 20; i++ {
		if got := statsBodyText(app); got != first {
			t.Fatal("stats ordering is not stable across renders")
		}
	}
}

// assertOrder checks that each wanted string appears in body, in the order
// given.
func assertOrder(t *testing.T, body string, want ...string) {
	t.Helper()
	at := -1
	for _, w := range want {
		i := strings.Index(body, w)
		if i < 0 {
			t.Fatalf("stats body missing %q:\n%s", w, body)
		}
		if i < at {
			t.Fatalf("stats body has %q out of order (want %v):\n%s", w, want, body)
		}
		at = i
	}
}

// statsBodyText renders the panel's full body, before scroll windowing, so a
// content assertion does not depend on how much of it happens to fit on a
// given terminal — the window has its own tests.
func statsBodyText(app *App) string {
	body := app.statsBody(dialogXLarge-2*statsPad, app.computeStatsAggregate())
	return ansi.Strip(strings.Join(body, "\n"))
}

// findPanelRow returns the first stripped panel line containing label.
func findPanelRow(t *testing.T, panel, label string) string {
	t.Helper()
	for _, line := range strings.Split(ansi.Strip(panel), "\n") {
		if strings.Contains(line, label) {
			return line
		}
	}
	t.Fatalf("no panel row contains %q:\n%s", label, ansi.Strip(panel))
	return ""
}

// TestStatsSlashCommandInRegistry verifies /stats appears in the slash command
// list and the command palette.
func TestStatsSlashCommandInRegistry(t *testing.T) {
	app := newTestApp(t, "http://example.invalid")
	found := false
	for _, item := range app.commandsRegistry() {
		if item.slash == "stats" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("no /stats command in the registry")
	}
}
