package tui

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/langazov/gocode-go/internal/tui/client"
)

// These tests guard the session-view fixes made to match
// packages/tui/src/routes/session/index.tsx more closely: settlement-line
// visibility while streaming, per-tool spinner rules, the compaction
// marker's color, and contentWidth no longer double-subtracting padding.

func TestSettlementLineShowsOnLastMessageWhileStreaming(t *testing.T) {
	app := &App{width: 100, height: 30, busy: true}
	msg := client.Message{ID: "m1", Type: "assistant", TimeCreated: 1000}
	data := client.AssistantData{Agent: "build"} // no Finish yet: still streaming

	block, _, _ := app.renderAssistant(msg, data, true) // isLast = true
	if !strings.Contains(block, "▣") {
		t.Fatalf("the last message should show the settlement line while streaming, got %q", block)
	}
	if strings.Contains(block, "·") && strings.Count(block, "·") > 1 {
		// Only the model segment's "·" should be present; no duration
		// segment (finish is empty, so final() is false) or interrupted tag.
		t.Fatalf("settlement line should not show a duration before the turn finishes, got %q", block)
	}
}

func TestSettlementLineHiddenOnEarlierUnfinishedMessage(t *testing.T) {
	app := &App{width: 100, height: 30, busy: true}
	msg := client.Message{ID: "m1", Type: "assistant", TimeCreated: 1000}
	data := client.AssistantData{Agent: "build"}

	block, _, _ := app.renderAssistant(msg, data, false) // isLast = false
	if strings.Contains(block, "▣") {
		t.Fatalf("an earlier, unfinished message should not show the settlement line, got %q", block)
	}
}

func TestSettlementLineShowsDurationOnlyWhenFinal(t *testing.T) {
	app := &App{width: 100, height: 30}
	msg := client.Message{ID: "m1", Type: "assistant", TimeCreated: 1000}
	data := client.AssistantData{Agent: "build", Finish: "end_turn"}
	data.Time.Completed = 2500

	line := app.settlementLine(msg, data)
	if !strings.Contains(line, "1.5s") {
		t.Fatalf("a final message should show its duration, got %q", line)
	}
}

func TestToolRowOnlySpinsForBashAndRead(t *testing.T) {
	app := &App{width: 100, height: 30, spinnerFrame: 0, animationsEnabled: true}
	msg := client.Message{}
	running := &struct {
		Status string         `json:"status"`
		Input  map[string]any `json:"input"`
		Output string         `json:"output"`
		Error  string         `json:"error"`
	}{Status: "running", Input: map[string]any{}}

	// A rendered block holds spinnerPlaceholder where the glyph goes;
	// renderMessageCached substitutes the frame on its way out, so the block
	// can be cached and still animate. Assert on both forms.
	spinner := spinnerFrames[0]

	for _, name := range []string{"bash", "read"} {
		got, _ := app.toolRow(msg, "", name, running)
		if !strings.Contains(got, spinnerPlaceholder) {
			t.Fatalf("%s while running should carry the spinner slot, got %q", name, got)
		}
		if !strings.Contains(app.substituteSpinner(got), spinner) {
			t.Fatalf("%s while running should resolve to the glyph %q, got %q", name, spinner, got)
		}
	}
	for _, name := range []string{"write", "glob", "grep", "webfetch", "edit"} {
		got, _ := app.toolRow(msg, "", name, running)
		if strings.Contains(got, spinnerPlaceholder) || strings.Contains(got, spinner) {
			t.Fatalf("%s should not spin while running (TS keeps it static), got %q", name, got)
		}
	}
}

// The color/weight fix itself (styles().Title -> plain theme.BorderActive)
// is a one-line change reviewed by hand; lipgloss no-ops all styling under
// `go test` (stdout isn't a TTY), so there's no SGR code here to assert on
// without forcing a color profile globally, which has broader side effects
// on unrelated rendering (see markdown_test.go's note). This just guards
// the title text survives.
func TestCompactionSeparatorHasTitle(t *testing.T) {
	app := &App{width: 100, height: 30, theme: themeResolve("gocode-dark")}
	line := app.compactionSeparator()
	if !strings.Contains(line, "Compaction") {
		t.Fatalf("compaction separator missing title, got %q", line)
	}
}

func TestContentWidthMatchesChatWidth(t *testing.T) {
	app := &App{width: 120, height: 30}
	if got, want := app.contentWidth(), app.chatWidth(); got != want {
		t.Fatalf("contentWidth() = %d, want it to match chatWidth() = %d (no extra padding subtraction)", got, want)
	}
}

// TestAssistantTextBlockMatchesMessageBlockWidth is the regression for
// "queries in the history are not the same width as the markdown document
// blocks": a fenced code block is the one construct in an assistant text
// part that actually fills its full wrap width with a solid background
// (via padToWidth in markdown.go), so its total rendered width — indent(3) +
// filled content (contentWidth()-4) = contentWidth()-1 — must match every
// bordered panel's total (userBlock, errBlock, blockToolStyle:
// border(1)+Width(contentWidth()-2) = contentWidth()-1 too). Fixed by
// shrinking the bordered panels to match the markdown side (not by widening
// renderMarkdown's wrap width, which needs its existing spare column of
// margin — see renderMarkdown's and assistantTextBlock's doc comments).
func TestAssistantTextBlockMatchesMessageBlockWidth(t *testing.T) {
	app := &App{width: 100, height: 30, theme: themeResolve("gocode-dark")}
	want := app.contentWidth() - 1

	msg := client.Message{ID: "m1"}
	userWidth := lipgloss.Width(app.userBlock(msg, client.UserData{Text: "hi"}))
	if userWidth != want {
		t.Fatalf("userBlock width = %d, want contentWidth()-1 = %d", userWidth, want)
	}

	// A single unbroken "word" longer than the wrap width forces renderMarkdown
	// to hard-wrap at exactly the width limit, so the fence's padded line
	// reaches its full declared width instead of stopping short at a word
	// boundary (see wrapText's greedy word-wrap).
	long := strings.Repeat("x", app.contentWidth()*2)
	fenced := "```\n" + long + "\n```"
	block := app.assistantTextBlock(fenced)
	maxWidth := 0
	for _, line := range strings.Split(block, "\n") {
		if w := lipgloss.Width(line); w > maxWidth {
			maxWidth = w
		}
	}
	if maxWidth != want {
		t.Fatalf("assistantTextBlock's fenced code block reaches width %d, want it to match userBlock/contentWidth()-1 = %d", maxWidth, want)
	}
}

func TestPermissionEditIconMatchesOriginal(t *testing.T) {
	icon, title := (&App{}).permissionTitle(&client.PermissionRequest{Action: "edit", Resources: []string{"foo.go"}})
	if icon != "→" {
		t.Fatalf("edit permission icon = %q, want %q", icon, "→")
	}
	if title != "Edit foo.go" {
		t.Fatalf("edit permission title = %q, want %q", title, "Edit foo.go")
	}
}

func TestPermissionWriteFallsBackToGenericLikeTS(t *testing.T) {
	icon, title := (&App{}).permissionTitle(&client.PermissionRequest{Action: "write", Resources: []string{"foo.go"}})
	if icon != "⚙" || title != "Call tool write" {
		t.Fatalf("write permission = (%q, %q), want the generic fallback (\"⚙\", \"Call tool write\") — TS has no write-specific case", icon, title)
	}
}

// --- bash/edit/todowrite block rendering + task label ----------------------

func TestBashBlockShowsCommandAndOutputOnceDone(t *testing.T) {
	app := &App{width: 100, height: 30}
	state := &toolState{
		Status: "done",
		Input:  map[string]any{"command": "echo hi"},
		Output: "hi\n",
	}
	got, _ := app.toolRow(client.Message{}, "t1", "bash", state)
	if !strings.Contains(got, "$ echo hi") {
		t.Fatalf("bash block missing command line, got %q", got)
	}
	if !strings.Contains(got, "hi") {
		t.Fatalf("bash block missing output, got %q", got)
	}
}

// Long bash output no longer gets cut down to a fixed number of lines —
// it collapses to just its first line plus a "click to expand" hint (see
// bashBlock/toolOutputHeaderRef), keyed by the tool part's own id so two
// concurrent bash calls collapse/expand independently.
func TestBashBlockCollapsesToFirstLineByDefault(t *testing.T) {
	app := &App{width: 100, height: 30, expandedToolOutput: map[string]bool{}}
	var lines []string
	for i := 0; i < 20; i++ {
		lines = append(lines, fmt.Sprintf("line %d", i))
	}
	state := &toolState{
		Status: "done",
		Input:  map[string]any{"command": "seq"},
		Output: strings.Join(lines, "\n"),
	}
	got, ref := app.toolRow(client.Message{}, "t1", "bash", state)
	if !strings.Contains(got, "line 0") {
		t.Fatalf("collapsed bash output should still show its first line, got %q", got)
	}
	if strings.Contains(got, "line 19") {
		t.Fatalf("collapsed bash output should hide everything past the first line, got %q", got)
	}
	if !strings.Contains(got, "click to expand") {
		t.Fatalf("collapsed bash output should hint that it can be expanded, got %q", got)
	}
	if ref == nil || ref.id != "t1" {
		t.Fatalf("expected a tool-output header ref for t1, got %+v", ref)
	}
}

// Once expanded, the output must render in full — no line/char cap, however
// long — unlike the fixed 10-line/char truncation this replaces. And since
// there's no single header left to re-click (unlike the collapsed summary,
// or reasoningBlock's header), the whole block must be a click target so
// clicking anywhere on it collapses it again.
func TestBashBlockExpandedShowsFullOutputUntruncated(t *testing.T) {
	app := &App{width: 100, height: 30, expandedToolOutput: map[string]bool{"t1": true}}
	var lines []string
	for i := 0; i < 500; i++ {
		lines = append(lines, fmt.Sprintf("line %d", i))
	}
	state := &toolState{
		Status: "done",
		Input:  map[string]any{"command": "seq"},
		Output: strings.Join(lines, "\n"),
	}
	got, ref := app.toolRow(client.Message{}, "t1", "bash", state)
	if !strings.Contains(got, "line 0") || !strings.Contains(got, "line 499") {
		t.Fatalf("expanded bash output must show every line, got %q", got)
	}
	if strings.Contains(got, "…") || strings.Contains(got, "(truncated)") {
		t.Fatalf("expanded bash output must never truncate, got %q", got)
	}
	if ref == nil || ref.id != "t1" {
		t.Fatalf("expected a whole-block click target for t1, got %+v", ref)
	}
	if ref.lineStart != 0 {
		t.Fatalf("expected the expanded block's click target to start at its very first row, got %+v", ref)
	}
	wantRows := strings.Count(got, "\n") + 1
	if got := ref.lineEnd - ref.lineStart + 1; got != wantRows {
		t.Fatalf("expected the click target to span the whole %d-row block, got %d rows (%+v)", wantRows, got, ref)
	}
}

// TestClickAnywhereOnExpandedBashOutputCollapsesIt is the concrete
// regression: expand a bash block, then click somewhere in the middle of
// its now-open output — not the row that used to be the collapsed
// summary — and confirm it still collapses. An expanded block has no
// single header left to re-click, so every row of it must respond.
func TestClickAnywhereOnExpandedBashOutputCollapsesIt(t *testing.T) {
	app := newTestApp(t, "http://example.invalid")
	app.active = &client.Session{ID: "ses_1"}
	app.view = viewChat
	var outputLines []string
	for i := 0; i < 20; i++ {
		outputLines = append(outputLines, fmt.Sprintf("line %d", i))
	}
	data, err := json.Marshal(map[string]any{
		"agent": "build", "finish": "end_turn",
		"content": []map[string]any{{
			"type": "tool", "id": "t1", "name": "bash",
			"state": map[string]any{
				"status": "done",
				"input":  map[string]any{"command": "seq"},
				"output": strings.Join(outputLines, "\n"),
			},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	app.timeline = []client.Message{{ID: "m1", Type: "assistant", Data: data}}
	app.expandedToolOutput["t1"] = true

	_ = app.viewChat() // populates chatToolOutputRows for the *expanded* layout

	rows := map[int]bool{}
	for absRow, id := range app.chatToolOutputRows {
		if id == "t1" {
			rows[absRow+app.chatWindowPad-app.chatWindowStart] = true
		}
	}
	if len(rows) < 2 {
		t.Fatalf("expected the expanded block to expose more than one clickable row, got %v", rows)
	}

	// A row past the block's very first one, to prove the whole block — not
	// just a single header line — responds to a click.
	midRow := -1
	for row := range rows {
		if row > 0 {
			midRow = row
			break
		}
	}
	if midRow == -1 {
		t.Fatalf("expected a clickable row past the block's first line, got %v", rows)
	}

	app.handleClick(5, midRow)
	if app.expandedToolOutput["t1"] {
		t.Fatal("clicking anywhere on the expanded block should collapse it again")
	}
}

// TestClickOnBashOutputSummaryTogglesExpansion drives the full mouse
// pipeline (viewChat caching the layout, then handleClick hit-testing it)
// the way Bubble Tea would — the bash-output equivalent of
// TestClickOnReasoningHeaderTogglesExpansion in reasoning_test.go.
func TestClickOnBashOutputSummaryTogglesExpansion(t *testing.T) {
	app := newTestApp(t, "http://example.invalid")
	app.active = &client.Session{ID: "ses_1"}
	app.view = viewChat
	var outputLines []string
	for i := 0; i < 5; i++ {
		outputLines = append(outputLines, fmt.Sprintf("line %d", i))
	}
	data, err := json.Marshal(map[string]any{
		"agent": "build", "finish": "end_turn",
		"content": []map[string]any{{
			"type": "tool", "id": "t1", "name": "bash",
			"state": map[string]any{
				"status": "done",
				"input":  map[string]any{"command": "seq"},
				"output": strings.Join(outputLines, "\n"),
			},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	app.timeline = []client.Message{{ID: "m1", Type: "assistant", Data: data}}

	_ = app.viewChat() // populates chatToolOutputRows/chatWindowPad/chatWindowStart

	row := -1
	for absRow, id := range app.chatToolOutputRows {
		if id == "t1" {
			row = absRow + app.chatWindowPad - app.chatWindowStart
		}
	}
	if row == -1 {
		t.Fatalf("expected a cached tool-output row for t1, got %v", app.chatToolOutputRows)
	}

	if app.expandedToolOutput["t1"] {
		t.Fatal("should start collapsed")
	}
	app.handleClick(5, row)
	if !app.expandedToolOutput["t1"] {
		t.Fatal("expected the click to expand t1")
	}
	app.handleClick(5, row)
	if app.expandedToolOutput["t1"] {
		t.Fatal("expected a second click to collapse t1 again")
	}
}

// The regression for "very-long-line-test-XXXX... that goes outside the
// view": a tool-call line whose single token is wider than the terminal
// (a long regex, a minified path) used to land on its own line past the
// right edge, because wrapText only broke on spaces. Every rendered line
// has to stay inside contentWidth.
func TestWrapTextChunksOversizedTokens(t *testing.T) {
	for _, width := range []int{20, 60, 100} {
		input := "short " + strings.Repeat("X", 1000) + " tail"
		for _, line := range strings.Split(wrapText(input, width), "\n") {
			if w := lipgloss.Width(line); w > width {
				t.Fatalf("width %d: wrapped line is %d columns, want <= %d (line %q…)", width, w, width, line[:20])
			}
		}
	}
}

func TestWrapTextKeepsUnicodeAndANSIIntact(t *testing.T) {
	// Wide runes and ANSI runs must not be split mid-cell or mid-sequence.
	got := wrapText("ab "+strings.Repeat("日本", 20)+" cd", 10)
	if stripped := ansi.Strip(got); !strings.Contains(stripped, "cd") {
		t.Fatalf("tail after the oversized run was lost, got %q", got)
	}
	for _, line := range strings.Split(got, "\n") {
		if w := lipgloss.Width(ansi.Strip(line)); w > 10 {
			t.Fatalf("line is %d cells after stripping, want <= 10, got %q", w, line)
		}
	}

	styled := "\x1b[31m" + strings.Repeat("a", 50) + "\x1b[0m"
	chunked := wrapText(styled, 10)
	if n := strings.Count(chunked, "\n"); n < 3 {
		t.Fatalf("50-column styled run at width 10 should chunk into 4+ lines, got %d (%q)", n+1, chunked)
	}
}

func TestChunkToWidthSplitsOnRunesNotBytes(t *testing.T) {
	// 5 Japanese runes = 10 display cells. A width-7 head must hold 3 runes
	// (6 cells), never slice through the 4th rune mid-sequence.
	head, tail := chunkToWidth(strings.Repeat("日", 5), 7)
	if head != "日日日" {
		t.Fatalf("head = %q, want 日日日", head)
	}
	if tail != "日日" {
		t.Fatalf("tail = %q, want 日日", tail)
	}
}

func TestBashRowPendingStaysInline(t *testing.T) {
	app := &App{width: 100, height: 30}
	state := &toolState{Status: "pending", Input: map[string]any{}}
	got, _ := app.toolRow(client.Message{}, "t1", "bash", state)
	if !strings.Contains(got, "~ Writing command...") {
		t.Fatalf("pending bash should stay a one-line \"~ \" summary, got %q", got)
	}
	if strings.Contains(got, "\n") {
		t.Fatalf("pending bash should not become a multi-line block, got %q", got)
	}
}

func TestEditDiffBlockShowsColoredDiffWhenPresent(t *testing.T) {
	app := &App{width: 100, height: 30}
	output := "Edited file successfully: foo.go\nReplacements: 1\n```diff\n-old line\n+new line\n```"
	state := &toolState{Status: "done", Input: map[string]any{"filePath": "foo.go"}, Output: output}
	got, _ := app.toolRow(client.Message{}, "t1", "edit", state)
	if !strings.Contains(got, "← Edit foo.go") {
		t.Fatalf("edit block missing title, got %q", got)
	}
	// Markers are separated from content by the line-number gutter, so assert
	// on the marker and the content rather than the concatenation.
	if !strings.Contains(got, "- old line") || !strings.Contains(got, "+ new line") {
		t.Fatalf("edit block missing diff lines, got %q", got)
	}
	// The header carries the add/remove counts computed from the parsed diff.
	if !strings.Contains(got, "+1 -1") {
		t.Fatalf("edit block missing diff stats, got %q", got)
	}
}

// TestEditDiffBlockRendersHunkHeadersAndLineNumbers covers the structured
// path: a real unified diff (as the edit tool now emits) must render its @@
// header and gutter line numbers, which the old prefix-scanning renderer
// could not produce.
func TestEditDiffBlockRendersHunkHeadersAndLineNumbers(t *testing.T) {
	app := &App{width: 120, height: 40}
	unified := strings.Join([]string{
		"```diff",
		"@@ -1,4 +1,4 @@",
		" package main",
		" ",
		"-const answer = 41",
		"+const answer = 42",
		" ",
		"```",
	}, "\n")
	state := &toolState{Status: "done", Input: map[string]any{"filePath": "main.go"}, Output: unified}
	got, _ := app.toolRow(client.Message{}, "t1", "edit", state)

	if !strings.Contains(got, "@@ -1,4 +1,4 @@") {
		t.Fatalf("hunk header missing, got %q", got)
	}
	if !strings.Contains(got, "- const answer = 41") || !strings.Contains(got, "+ const answer = 42") {
		t.Fatalf("changed lines missing, got %q", got)
	}
	// Line 3 is where the change lands on both sides.
	if !strings.Contains(got, " 3") {
		t.Fatalf("gutter line numbers missing, got %q", got)
	}
}

func TestEditFallsBackToInlineWithoutADiff(t *testing.T) {
	app := &App{width: 100, height: 30}
	state := &toolState{Status: "done", Input: map[string]any{"filePath": "foo.go"}, Output: "no diff here"}
	got, _ := app.toolRow(client.Message{}, "t1", "edit", state)
	if !strings.Contains(got, "← Edit foo.go") {
		t.Fatalf("edit fallback should still show the summary line, got %q", got)
	}
	if strings.Contains(got, "\n") {
		t.Fatalf("without a diff, edit should stay a one-line summary, got %q", got)
	}
}

func TestTodoWriteBlockRendersDecodedList(t *testing.T) {
	app := &App{width: 100, height: 30}
	output := `[{"content":"first","status":"completed"},{"content":"second","status":"in_progress"}]`
	state := &toolState{Status: "done", Input: map[string]any{}, Output: output}
	got, _ := app.toolRow(client.Message{}, "t1", "todowrite", state)
	if !strings.Contains(got, "# Todos") {
		t.Fatalf("todowrite block missing title, got %q", got)
	}
	if !strings.Contains(got, "first") || !strings.Contains(got, "second") {
		t.Fatalf("todowrite block missing items, got %q", got)
	}
}

func TestTodoWriteFallsBackToInlineWithoutDecodableList(t *testing.T) {
	app := &App{width: 100, height: 30}
	state := &toolState{Status: "done", Input: map[string]any{}, Output: ""}
	got, _ := app.toolRow(client.Message{}, "t1", "todowrite", state)
	if !strings.Contains(got, "Updating todos...") {
		t.Fatalf("todowrite fallback should show the summary line, got %q", got)
	}
}

func TestTaskLabelPendingWithoutDescription(t *testing.T) {
	icon, label := toolLabel("task", map[string]any{})
	if icon != "│" || label != "Delegating..." {
		t.Fatalf("task label with no description = (%q, %q), want (\"│\", \"Delegating...\")", icon, label)
	}
}

func TestTaskLabelFormatsSubagentTitle(t *testing.T) {
	icon, label := toolLabel("task", map[string]any{
		"subagent_type": "general",
		"description":   "find the bug",
		"background":    true,
	})
	if icon != "│" {
		t.Fatalf("task icon = %q, want │ (toolRow upgrades to ✓ once completed)", icon)
	}
	if label != "General Task (background) — find the bug" {
		t.Fatalf("task label = %q, want %q", label, "General Task (background) — find the bug")
	}
}

func TestTaskRowShowsCheckOnceCompleted(t *testing.T) {
	app := &App{width: 100, height: 30}
	state := &toolState{Status: "done", Input: map[string]any{"description": "find the bug"}}
	got, _ := app.toolRow(client.Message{}, "t1", "task", state)
	if !strings.Contains(got, "✓") {
		t.Fatalf("completed task should show ✓, got %q", got)
	}
}
