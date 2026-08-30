package tui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/anomalyco/opencode-go/internal/tui/client"
)

// These tests guard the session-view fixes made to match
// packages/tui/src/routes/session/index.tsx more closely: settlement-line
// visibility while streaming, per-tool spinner rules, the compaction
// marker's color, and contentWidth no longer double-subtracting padding.

func TestSettlementLineShowsOnLastMessageWhileStreaming(t *testing.T) {
	app := &App{width: 100, height: 30, busy: true}
	msg := client.Message{ID: "m1", Type: "assistant", TimeCreated: 1000}
	data := client.AssistantData{Agent: "build"} // no Finish yet: still streaming

	block, _ := app.renderAssistant(msg, data, true) // isLast = true
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

	block, _ := app.renderAssistant(msg, data, false) // isLast = false
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
	app := &App{width: 100, height: 30, spinnerFrame: 0}
	msg := client.Message{}
	running := &struct {
		Status string         `json:"status"`
		Input  map[string]any `json:"input"`
		Output string         `json:"output"`
		Error  string         `json:"error"`
	}{Status: "running", Input: map[string]any{}}

	spinner := spinnerFrames[0]

	for _, name := range []string{"bash", "read"} {
		got := app.toolRow(msg, name, running)
		if !strings.Contains(got, spinner) {
			t.Fatalf("%s while running should show the spinner glyph %q, got %q", name, spinner, got)
		}
	}
	for _, name := range []string{"write", "glob", "grep", "webfetch", "edit"} {
		got := app.toolRow(msg, name, running)
		if strings.Contains(got, spinner) {
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
	app := &App{width: 100, height: 30, theme: themeResolve("opencode-dark")}
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
	app := &App{width: 100, height: 30, theme: themeResolve("opencode-dark")}
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
	icon, title := permissionTitle(&client.PermissionRequest{Action: "edit", Resources: []string{"foo.go"}})
	if icon != "→" {
		t.Fatalf("edit permission icon = %q, want %q", icon, "→")
	}
	if title != "Edit foo.go" {
		t.Fatalf("edit permission title = %q, want %q", title, "Edit foo.go")
	}
}

func TestPermissionWriteFallsBackToGenericLikeTS(t *testing.T) {
	icon, title := permissionTitle(&client.PermissionRequest{Action: "write", Resources: []string{"foo.go"}})
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
	got := app.toolRow(client.Message{}, "bash", state)
	if !strings.Contains(got, "$ echo hi") {
		t.Fatalf("bash block missing command line, got %q", got)
	}
	if !strings.Contains(got, "hi") {
		t.Fatalf("bash block missing output, got %q", got)
	}
}

func TestBashBlockTruncatesLongOutput(t *testing.T) {
	app := &App{width: 100, height: 30}
	var lines []string
	for i := 0; i < 20; i++ {
		lines = append(lines, "line")
	}
	state := &toolState{
		Status: "done",
		Input:  map[string]any{"command": "seq"},
		Output: strings.Join(lines, "\n"),
	}
	got := app.toolRow(client.Message{}, "bash", state)
	if !strings.Contains(got, "(truncated)") {
		t.Fatalf("20-line output should be truncated, got %q", got)
	}
	if strings.Count(got, "line") > 11 { // 10 kept + safety margin
		t.Fatalf("truncated output kept too many lines, got %q", got)
	}
}

func TestBashRowPendingStaysInline(t *testing.T) {
	app := &App{width: 100, height: 30}
	state := &toolState{Status: "pending", Input: map[string]any{}}
	got := app.toolRow(client.Message{}, "bash", state)
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
	got := app.toolRow(client.Message{}, "edit", state)
	if !strings.Contains(got, "← Edit foo.go") {
		t.Fatalf("edit block missing title, got %q", got)
	}
	if !strings.Contains(got, "-old line") || !strings.Contains(got, "+new line") {
		t.Fatalf("edit block missing diff lines, got %q", got)
	}
}

func TestEditFallsBackToInlineWithoutADiff(t *testing.T) {
	app := &App{width: 100, height: 30}
	state := &toolState{Status: "done", Input: map[string]any{"filePath": "foo.go"}, Output: "no diff here"}
	got := app.toolRow(client.Message{}, "edit", state)
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
	got := app.toolRow(client.Message{}, "todowrite", state)
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
	got := app.toolRow(client.Message{}, "todowrite", state)
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
	got := app.toolRow(client.Message{}, "task", state)
	if !strings.Contains(got, "✓") {
		t.Fatalf("completed task should show ✓, got %q", got)
	}
}
