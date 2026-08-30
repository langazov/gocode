package tui

import (
	"encoding/json"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/anomalyco/opencode-go/internal/tui/client"
	"github.com/charmbracelet/x/ansi"
)

// These tests guard the additional session-view fidelity fixes: the
// wide()-gated docked-vs-overlay sidebar, the timeline's leading spacer
// line, and file-attachment pills on user messages.

func TestWideThreshold(t *testing.T) {
	app := &App{width: 120}
	if app.wide() {
		t.Fatalf("width=120 should not be wide (TS: width > 120)")
	}
	app.width = 121
	if !app.wide() {
		t.Fatalf("width=121 should be wide")
	}
}

func chatViewWithSidebar(t *testing.T, width int) string {
	t.Helper()
	app := newTestApp(t, "http://example.invalid")
	app.width, app.height = width, 40
	app.view = viewChat
	app.active = &client.Session{ID: "ses_1", Title: "Sidebar Test Session"}
	app.sidebar = true
	return app.View()
}

// assertRectangular checks every line of view renders at the same width —
// both the docked JoinHorizontal path and the overlay compositing path
// should produce a well-formed rectangle, just via different code paths.
func assertRectangular(t *testing.T, view string) {
	t.Helper()
	lines := strings.Split(view, "\n")
	want := lipgloss.Width(lines[0])
	for i, line := range lines {
		if w := lipgloss.Width(line); w != want {
			t.Fatalf("line %d width = %d, want %d (first line's width) — the frame is jagged: %q", i, w, want, line)
		}
	}
}

func TestSidebarDockedWhenWide(t *testing.T) {
	view := chatViewWithSidebar(t, 140)
	assertRectangular(t, view)
	if !strings.Contains(view, "Sidebar Test Session") {
		t.Fatalf("docked sidebar should show the session title, got:\n%s", view)
	}
}

func TestSidebarOverlayWhenNarrow(t *testing.T) {
	view := chatViewWithSidebar(t, 100)
	assertRectangular(t, view)
	if !strings.Contains(view, "Sidebar Test Session") {
		t.Fatalf("overlaid sidebar should show the session title, got:\n%s", view)
	}
}

func TestTimelineHasLeadingSpacer(t *testing.T) {
	app := newTestApp(t, "http://example.invalid")
	app.timeline = []client.Message{{
		ID: "msg_a", SessionID: "ses_1", Type: "user", Seq: 0,
		Data: json.RawMessage(`{"text":"hello"}`),
	}}
	lines := app.timelineLines()
	if len(lines) == 0 || lines[0] != "" {
		t.Fatalf("timeline should open with a blank spacer line (TS's <box height={1}/>), got first line %q", firstOrEmpty(lines))
	}
}

func firstOrEmpty(lines []string) string {
	if len(lines) == 0 {
		return "<empty>"
	}
	return lines[0]
}

func TestFileAttachmentPillsRenderNameAndKind(t *testing.T) {
	app := &App{width: 100, height: 30, theme: themeResolve("opencode-dark")}
	message := client.Message{ID: "m1", Type: "user"}
	data := client.UserData{
		Text: "see attached",
		Files: []client.FileAttachment{
			{Mime: "text/plain", Name: "notes.txt"},
			{Mime: "application/x-directory", Name: "src"},
		},
	}
	block := app.userBlock(message, data)
	if !strings.Contains(block, "notes.txt") || !strings.Contains(block, "src") {
		t.Fatalf("attachment names missing, got %q", block)
	}
	if !strings.Contains(block, "File") {
		t.Fatalf("file badge missing, got %q", block)
	}
	if !strings.Contains(block, "Directory") {
		t.Fatalf("directory badge missing, got %q", block)
	}
}

// --- session title refresh (fixes the sidebar/window-title stuck on the
// directory-derived placeholder after the server auto-titles a session) ---

func TestSessionRefreshedUpdatesActiveTitle(t *testing.T) {
	app := newTestApp(t, "http://example.invalid")
	app.active = &client.Session{ID: "ses_1", Title: "go", Directory: "/x/go"}

	app.Update(sessionRefreshedMsg{session: &client.Session{ID: "ses_1", Title: "hello", Directory: "/x/go"}})

	if app.active.Title != "hello" {
		t.Fatalf("active.Title = %q after refresh, want %q", app.active.Title, "hello")
	}
}

func TestSessionRefreshedIgnoresMismatchedSession(t *testing.T) {
	app := newTestApp(t, "http://example.invalid")
	app.active = &client.Session{ID: "ses_1", Title: "go"}

	app.Update(sessionRefreshedMsg{session: &client.Session{ID: "ses_other", Title: "hello"}})

	if app.active.Title != "go" {
		t.Fatalf("active.Title = %q, a refresh for a different session should not apply", app.active.Title)
	}
}

// --- sidebar MCP/LSP/footer -------------------------------------------------

func TestSidebarShowsConfiguredMCPServers(t *testing.T) {
	app := newTestApp(t, "http://example.invalid")
	app.width, app.height = 140, 40
	app.view = viewChat
	app.active = &client.Session{ID: "ses_1", Title: "Test"}
	app.sidebar = true
	app.mcpServers = []client.MCPServer{
		{Name: "chrome-devtools", Status: "connected"},
		{Name: "github", Status: "failed", Error: "connection refused"},
	}

	view := app.sidebarView()
	if !strings.Contains(view, "MCP") {
		t.Fatalf("sidebar missing MCP section, got:\n%s", view)
	}
	if !strings.Contains(view, "chrome-devtools") || !strings.Contains(view, "github") {
		t.Fatalf("sidebar missing configured MCP server names, got:\n%s", view)
	}
}

func TestSidebarHasNoMCPSectionWhenNoneConfigured(t *testing.T) {
	app := newTestApp(t, "http://example.invalid")
	app.width, app.height = 140, 40
	app.view = viewChat
	app.active = &client.Session{ID: "ses_1", Title: "Test"}
	app.sidebar = true
	app.mcpServers = nil

	if view := app.sidebarView(); strings.Contains(view, "MCP") {
		t.Fatalf("sidebar should omit the MCP section with no servers configured, got:\n%s", view)
	}
}

func TestSidebarAlwaysShowsLSPDisabled(t *testing.T) {
	app := newTestApp(t, "http://example.invalid")
	app.width, app.height = 140, 40
	app.view = viewChat
	app.active = &client.Session{ID: "ses_1", Title: "Test"}
	app.sidebar = true

	view := app.sidebarView()
	if !strings.Contains(view, "LSP") || !strings.Contains(view, "LSPs are disabled") {
		t.Fatalf("sidebar should always show the LSP-disabled line (this port has no LSP client), got:\n%s", view)
	}
}

func TestSidebarFooterShowsPathAndVersion(t *testing.T) {
	app := newTestApp(t, "http://example.invalid")
	app.width, app.height = 140, 40
	app.view = viewChat
	app.active = &client.Session{ID: "ses_1", Title: "Test"}
	app.sidebar = true
	app.cwd = "/home/tester/project"
	app.homeDir = "/home/tester"
	app.gitBranch = "" // isolate from this actual repo's real branch/name length

	// lipgloss v2's Style.Render always emits real ANSI (v1 no-op'd styling
	// off-TTY, which is what let these plain-substring checks work
	// unmodified) — strip it back to plain text for a structural check.
	view := ansi.Strip(app.sidebarView())
	if !strings.Contains(view, "~/project") {
		t.Fatalf("sidebar footer should show the abbreviated path, got:\n%s", view)
	}
	if !strings.Contains(view, "OpenCode") {
		t.Fatalf("sidebar footer should still show the OpenCode version line, got:\n%s", view)
	}
}

// --- window title -----------------------------------------------------------

func TestWindowTitleHomeIsPlainOpenCode(t *testing.T) {
	app := &App{view: viewHome}
	if got := app.desiredWindowTitle(); got != "OpenCode" {
		t.Fatalf("home window title = %q, want %q", got, "OpenCode")
	}
}

func TestWindowTitlePlaceholderSessionIsPlainOpenCode(t *testing.T) {
	app := &App{view: viewChat, active: &client.Session{Title: "go", Directory: "/x/go"}}
	if got := app.desiredWindowTitle(); got != "OpenCode" {
		t.Fatalf("placeholder-titled session window title = %q, want %q", got, "OpenCode")
	}
}

func TestWindowTitleRealSessionShowsOCPrefix(t *testing.T) {
	app := &App{view: viewChat, active: &client.Session{Title: "Greeting and intro", Directory: "/x/go"}}
	if got := app.desiredWindowTitle(); got != "OC | Greeting and intro" {
		t.Fatalf("window title = %q, want %q", got, "OC | Greeting and intro")
	}
}

func TestWindowTitleTruncatesLongTitles(t *testing.T) {
	long := strings.Repeat("a", 50)
	app := &App{view: viewChat, active: &client.Session{Title: long, Directory: "/x/go"}}
	got := app.desiredWindowTitle()
	if !strings.HasPrefix(got, "OC | ") || !strings.HasSuffix(got, "...") {
		t.Fatalf("long title should be truncated with '...', got %q", got)
	}
	if len(got) > len("OC | ")+40 {
		t.Fatalf("truncated title too long: %q", got)
	}
}

func TestSyncWindowTitleOnlyReportsRealChanges(t *testing.T) {
	app := &App{view: viewHome, windowTitle: "OpenCode"}
	if _, changed := app.syncWindowTitle(); changed {
		t.Fatalf("syncWindowTitle reported a change when the title didn't move")
	}
	app.view = viewChat
	app.active = &client.Session{Title: "Real Title", Directory: "/x/go"}
	title, changed := app.syncWindowTitle()
	if !changed || title != "OC | Real Title" {
		t.Fatalf("syncWindowTitle = (%q, %v), want (\"OC | Real Title\", true)", title, changed)
	}
}

func TestFileAttachmentPillsWrapAtWidth(t *testing.T) {
	app := &App{width: 100, height: 30, theme: themeResolve("opencode-dark")}
	files := []client.FileAttachment{
		{Mime: "text/plain", Name: "aaaaaaaaaaaaaaaaaaaa"},
		{Mime: "text/plain", Name: "bbbbbbbbbbbbbbbbbbbb"},
		{Mime: "text/plain", Name: "cccccccccccccccccccc"},
	}
	rows := app.fileAttachmentRows(files, 30)
	if len(rows) < 2 {
		t.Fatalf("wide pills should wrap onto multiple rows within width 30, got %d row(s): %v", len(rows), rows)
	}
	for i, row := range rows {
		if w := lipgloss.Width(row); w > 30 {
			t.Fatalf("row %d width = %d, exceeds 30: %q", i, w, row)
		}
	}
}

// --- sidebar/prompt-box/footer margins ------------------------------------
//
// lipgloss's Width/Height are border-box: the declared value is the TOTAL
// rendered size (padding included — it wraps content to
// declaredWidth-leftPad-rightPad internally), and Height only pads a
// shorter render, it never truncates a taller one. sidebarView() used to
// declare Width(width-4) (a content-box assumption) and undercounted its
// footer's row budget by one, so the panel rendered 4 columns narrower and
// 1-2 rows taller than its reserved 42x(a.height) column — an
// off-by-a-few gap around the sidebar. promptBox was also missing
// PaddingRight entirely (TS's Prompt has paddingLeft AND paddingRight, both
// 2).

func TestSidebarRendersAtItsExactReservedFootprint(t *testing.T) {
	for _, height := range []int{24, 30, 40, 53} {
		app := newTestApp(t, "http://example.invalid")
		app.width, app.height = 140, height
		app.active = &client.Session{ID: "ses_1", Title: "Test"}
		app.sidebar = true

		lines := strings.Split(app.sidebarView(), "\n")
		if len(lines) != height {
			t.Fatalf("height=%d: sidebar rendered %d lines, want exactly %d", height, len(lines), height)
		}
		for i, l := range lines {
			if w := lipgloss.Width(l); w != app.sidebarWidth() {
				t.Fatalf("height=%d line %d: width = %d, want %d (sidebarWidth's reservation)",
					height, i, w, app.sidebarWidth())
			}
		}
	}
}

func TestPromptBoxHasSymmetricLeftRightPadding(t *testing.T) {
	app := newTestApp(t, "http://example.invalid")
	app.width, app.height = 100, 30
	width := 60
	box := app.promptBox(width)
	for i, line := range strings.Split(box, "\n") {
		line = ansi.Strip(line) // see TestSidebarFooterShowsPathAndVersion's comment
		if !strings.HasSuffix(line, "  ") {
			t.Fatalf("line %d should end with the 2-column right padding TS's Prompt has, got %q", i, line)
		}
	}
}

func TestChatFooterNeverExceedsChatWidth(t *testing.T) {
	app := newTestApp(t, "http://example.invalid")
	app.width, app.height = 100, 30
	app.active = &client.Session{ID: "ses_1", Directory: "/a/very/deeply/nested/directory/tree/that/is/quite/long/indeed"}
	app.sidebar = true // narrows chatWidth() well below the path's length

	if w := lipgloss.Width(app.chatFooter()); w > app.chatWidth() {
		t.Fatalf("chatFooter width = %d, exceeds chatWidth() = %d (would overflow into the sidebar column)",
			w, app.chatWidth())
	}
}

// TestPromptBoxAlignsWithMessageBlocksWidth guards against a regression
// where promptBox rendered 4 columns narrower than every message/tool
// block above it (chatWidth()-5 as its declared Width, when — because
// lipgloss's border renders *outside* the declared width, verified
// separately — chatWidth()-1 is what actually makes its total footprint
// match contentWidth()/chatWidth(), the same total userBlock/bashBlock
// render at). The visible symptom: the input box's backgroundElement tint
// stopped 4 columns short of where the message/tool boxes' backgrounds did.
//
// This checks widths *before* they reach viewChat's outer frame(): lipgloss
// unconditionally pads every line of a multi-line Render to match that
// block's own widest line (confirmed directly against the align.go source),
// so by the time these strings are joined into the full view and re-rendered
// through frame(), the narrower box's short lines get silently padded with
// plain (unstyled) spaces to match — the character-cell *widths* end up
// equal either way, only the visible background-color extent differs. A
// full-view integration check comparing rendered-line widths would pass
// even with the bug back, so it isn't a substitute for this.
func TestPromptBoxAlignsWithMessageBlocksWidth(t *testing.T) {
	app := newTestApp(t, "http://example.invalid")
	app.width, app.height = 100, 30
	app.sidebar = false

	message := client.Message{TimeCreated: 1000}
	userWidth := lipgloss.Width(app.userBlock(message, client.UserData{Text: "hello"}))

	bashState := &toolState{Status: "done", Input: map[string]any{"command": "echo hi"}, Output: "hi"}
	bashWidth := lipgloss.Width(app.toolRow(message, "bash", bashState))

	// sessionPromptBoxWidth is the exact value the real viewChat call site
	// uses, so this also exercises the production wiring, not just the
	// width formula in isolation.
	promptWidth := lipgloss.Width(app.promptBox(app.sessionPromptBoxWidth()))

	if userWidth != promptWidth {
		t.Fatalf("userBlock width = %d, promptBox width = %d, want equal (aligned columns)", userWidth, promptWidth)
	}
	if bashWidth != promptWidth {
		t.Fatalf("bash tool block width = %d, promptBox width = %d, want equal (aligned columns)", bashWidth, promptWidth)
	}
}
