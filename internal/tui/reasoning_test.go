package tui

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/langazov/gocode-go/internal/tui/client"
)

func assistantWithReasoning(t *testing.T, json_ string) client.AssistantData {
	t.Helper()
	data, err := client.DecodeAssistant(json.RawMessage(json_))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	return data
}

func plain(s string) string { return ansi.Strip(s) }

func TestReasoningSummaryExtractsBoldTitleOnly(t *testing.T) {
	cases := []struct {
		name      string
		text      string
		wantTitle string
		wantBody  string
	}{
		{"titled", "**Investigating bug**\n\nRoot cause is X", "Investigating bug", "Root cause is X"},
		{"titled no body", "**Just a title**", "Just a title", ""},
		{"plain first line is not a title", "First line\nSecond line", "", "First line\nSecond line"},
		{"bold not at start", "intro **Title**\n\nbody", "", "intro **Title**\n\nbody"},
		{"title with newline inside is not a title", "**Ti\ntle**\n\nbody", "", "**Ti\ntle**\n\nbody"},
		{"single blank line only, no body text", "**Title**\n\n", "Title", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			title, body := reasoningSummary(c.text)
			if title != c.wantTitle {
				t.Errorf("title = %q, want %q", title, c.wantTitle)
			}
			if body != c.wantBody {
				t.Errorf("body = %q, want %q", body, c.wantBody)
			}
		})
	}
}

func TestNextThinkingMode(t *testing.T) {
	if nextThinkingMode("show") != "hide" {
		t.Fatal("show -> hide")
	}
	if nextThinkingMode("hide") != "show" {
		t.Fatal("hide -> show")
	}
	if nextThinkingMode("") != "show" {
		t.Fatal("unknown -> show")
	}
}

func TestReasoningBlockRunningShowsSpinnerAndTitle(t *testing.T) {
	app := &App{width: 100, height: 30, busy: true, theme: themeResolve("gocode-dark"), thinkingMode: "hide", expandedReasoning: map[string]bool{}}
	data := assistantWithReasoning(t, `{"agent":"build","content":[{"type":"reasoning","id":"r1","text":"**Investigating**\n\nlooking into it"}]}`)
	block, _, _ := app.renderAssistant(client.Message{Type: "assistant"}, data, true)
	got := plain(block)
	if !strings.Contains(got, "Thinking: Investigating") {
		t.Fatalf("expected running spinner with title, got %q", got)
	}
	if strings.Contains(got, "looking into it") {
		t.Fatalf("body should not show while running (only the header does), got %q", got)
	}
}

func TestReasoningBlockCollapsedByDefault(t *testing.T) {
	app := &App{width: 100, height: 30, theme: themeResolve("gocode-dark"), thinkingMode: "hide", expandedReasoning: map[string]bool{}}
	data := assistantWithReasoning(t, `{"agent":"build","finish":"end_turn","content":[
		{"type":"reasoning","id":"r1","text":"**Investigating bug**\n\nRoot cause is X","time":{"created":1000,"completed":3500}}
	]}`)
	block, refs, _ := app.renderAssistant(client.Message{Type: "assistant"}, data, false)
	got := plain(block)
	if !strings.Contains(got, "+ Thought: Investigating bug · 2.5s") {
		t.Fatalf("expected collapsed header with title+duration, got %q", got)
	}
	if strings.Contains(got, "Root cause is X") {
		t.Fatalf("body should stay hidden while collapsed, got %q", got)
	}
	if len(refs) != 1 || refs[0].id != "r1" {
		t.Fatalf("expected one reasoning header ref for r1, got %+v", refs)
	}
}

// The token figure is an estimate (chars/4, see estimateReasoningTokens) and
// is printed behind a "~": the collapsed header is all a hidden block shows,
// so it carries the one fact the reader otherwise loses — how much thinking
// is behind it.
func TestReasoningBlockCollapsedShowsTokenEstimate(t *testing.T) {
	app := &App{width: 100, height: 30, theme: themeResolve("gocode-dark"), thinkingMode: "hide", expandedReasoning: map[string]bool{}}
	body := strings.Repeat("word ", 1000) // 5000 chars -> ~1.3K estimated tokens
	data := assistantWithReasoning(t, `{"agent":"build","finish":"end_turn","content":[
		{"type":"reasoning","id":"r1","text":"**Investigating bug**\n\n`+body+`","time":{"created":1000,"completed":3500}}
	]}`)
	block, _, _ := app.renderAssistant(client.Message{Type: "assistant"}, data, false)
	got := plain(block)
	if !strings.Contains(got, "+ Thought: Investigating bug · 2.5s · ~1.3K tokens") {
		t.Fatalf("expected collapsed header to carry a token estimate, got %q", got)
	}
}

// Expanding keeps the same header: the count is a property of the part, and
// having it disappear on toggle would read as the block having changed.
func TestReasoningBlockExpandedKeepsTokenEstimate(t *testing.T) {
	app := &App{width: 100, height: 30, theme: themeResolve("gocode-dark"), thinkingMode: "hide", expandedReasoning: map[string]bool{"r1": true}}
	data := assistantWithReasoning(t, `{"agent":"build","finish":"end_turn","content":[
		{"type":"reasoning","id":"r1","text":"**Investigating bug**\n\nRoot cause is X","time":{"created":1000,"completed":3500}}
	]}`)
	block, _, _ := app.renderAssistant(client.Message{Type: "assistant"}, data, false)
	got := plain(block)
	if !strings.Contains(got, "· ~10 tokens") {
		t.Fatalf("expected the same estimate once expanded, got %q", got)
	}
}

// The running header carries the estimate too, climbing with the stream: in
// the default hide mode the header is the whole of a live block, so it is the
// only progress a long think shows.
func TestReasoningBlockRunningShowsGrowingTokenEstimate(t *testing.T) {
	app := &App{width: 100, height: 30, busy: true, theme: themeResolve("gocode-dark"), thinkingMode: "hide", expandedReasoning: map[string]bool{}}
	short := plain(app.reasoningBlock("r1", true, "**Investigating**\n\n"+strings.Repeat("word ", 100), nil))
	long := plain(app.reasoningBlock("r1", true, "**Investigating**\n\n"+strings.Repeat("word ", 1000), nil))
	if !strings.Contains(short, "Thinking: Investigating · ~130 tokens") {
		t.Fatalf("expected a live estimate in the running header, got %q", short)
	}
	if !strings.Contains(long, "Thinking: Investigating · ~1.3K tokens") {
		t.Fatalf("expected the estimate to grow with the stream, got %q", long)
	}
}

func TestReasoningBlockExpandedShowsBodyWithExtraIndent(t *testing.T) {
	app := &App{width: 100, height: 30, theme: themeResolve("gocode-dark"), thinkingMode: "hide", expandedReasoning: map[string]bool{"r1": true}}
	data := assistantWithReasoning(t, `{"agent":"build","finish":"end_turn","content":[
		{"type":"reasoning","id":"r1","text":"**Investigating bug**\n\nRoot cause is X","time":{"created":1000,"completed":3500}}
	]}`)
	block, _, _ := app.renderAssistant(client.Message{Type: "assistant"}, data, false)
	got := plain(block)
	if !strings.Contains(got, "- Thought: Investigating bug · 2.5s") {
		t.Fatalf("expected expanded (open) header with '-' prefix, got %q", got)
	}
	if !strings.Contains(got, "Root cause is X") {
		t.Fatalf("expected body visible once expanded, got %q", got)
	}
	// inMinimal (hide mode) + open adds paddingLeft=2 on top of the shared
	// paddingLeft=3, so the body line starts 5 spaces in.
	for _, line := range strings.Split(got, "\n") {
		if strings.Contains(line, "Root cause is X") {
			if !strings.HasPrefix(line, "     Root cause is X") {
				t.Fatalf("expected body indented 5 spaces (3+2) in hide mode, got %q", line)
			}
		}
	}
}

func TestReasoningBlockShowModeAlwaysOpenNoPrefix(t *testing.T) {
	app := &App{width: 100, height: 30, theme: themeResolve("gocode-dark"), thinkingMode: "show", expandedReasoning: map[string]bool{}}
	data := assistantWithReasoning(t, `{"agent":"build","finish":"end_turn","content":[
		{"type":"reasoning","id":"r1","text":"**Investigating bug**\n\nRoot cause is X","time":{"created":1000,"completed":3500}}
	]}`)
	block, _, _ := app.renderAssistant(client.Message{Type: "assistant"}, data, false)
	got := plain(block)
	if !strings.Contains(got, "Thought: Investigating bug · 2.5s") {
		t.Fatalf("expected header, got %q", got)
	}
	if strings.Contains(got, "+ Thought") || strings.Contains(got, "- Thought") {
		t.Fatalf("show mode is not toggleable, should have no +/- prefix, got %q", got)
	}
	if !strings.Contains(got, "Root cause is X") {
		t.Fatalf("show mode always shows the body, got %q", got)
	}
	// Not inMinimal: body gets no extra indent (3 only).
	for _, line := range strings.Split(got, "\n") {
		if strings.Contains(line, "Root cause is X") {
			if strings.HasPrefix(line, "    Root cause is X") {
				t.Fatalf("body should NOT have the extra hide-mode indent in show mode, got %q", line)
			}
		}
	}
}

func TestReasoningBlockRedactedPlaceholderStripped(t *testing.T) {
	app := &App{width: 100, height: 30, theme: themeResolve("gocode-dark"), thinkingMode: "hide", expandedReasoning: map[string]bool{}}
	data := assistantWithReasoning(t, `{"agent":"build","finish":"end_turn","content":[{"type":"reasoning","id":"r1","text":"[REDACTED]"}]}`)
	block, _, _ := app.renderAssistant(client.Message{Type: "assistant"}, data, false)
	if block != "" && strings.Contains(block, "Thought") {
		t.Fatalf("a fully redacted block with no other text should render nothing, got %q", block)
	}
}

// TestBuildTimelineLocatesReasoningHeaderRow guards the line-offset
// bookkeeping renderAssistant/buildTimeline share: the header ref must land
// on the exact line that reads "Thought: ..." once everything is flattened
// into the full timeline (spacer row + per-message blank-line separators),
// not just within reasoningBlock's own isolated output.
func TestBuildTimelineLocatesReasoningHeaderRow(t *testing.T) {
	app := &App{width: 100, height: 30, theme: themeResolve("gocode-dark"), thinkingMode: "hide", expandedReasoning: map[string]bool{}}
	app.timeline = []client.Message{
		{ID: "m1", Type: "assistant", Data: json.RawMessage(`{"agent":"build","finish":"end_turn","content":[
			{"type":"reasoning","id":"r1","text":"**Investigating bug**\n\nRoot cause is X","time":{"created":1000,"completed":3500}}
		]}`)},
	}
	lines, rows, _ := app.buildTimeline()
	found := -1
	for i, line := range lines {
		if strings.Contains(plain(line), "Thought: Investigating bug") {
			found = i
			break
		}
	}
	if found == -1 {
		t.Fatalf("expected a header line in the timeline, got %v", lines)
	}
	if got, ok := rows[found]; !ok || got != "r1" {
		t.Fatalf("reasoningRows[%d] = (%q, %v), want (\"r1\", true)", found, got, ok)
	}
}

// TestClickOnReasoningHeaderTogglesExpansion drives the full mouse pipeline
// (viewChat caching the layout, then handleClick hit-testing it) exactly
// the way Bubble Tea would: render, then dispatch the next input against
// what was just rendered.
func TestClickOnReasoningHeaderTogglesExpansion(t *testing.T) {
	app := newTestApp(t, "http://example.invalid")
	app.thinkingMode = "hide"
	app.active = &client.Session{ID: "ses_1"}
	app.view = viewChat
	app.timeline = []client.Message{
		{ID: "m1", Type: "assistant", Data: json.RawMessage(`{"agent":"build","finish":"end_turn","content":[
			{"type":"reasoning","id":"r1","text":"**Investigating bug**\n\nRoot cause is X","time":{"created":1000,"completed":3500}}
		]}`)},
	}

	_ = app.viewChat() // populates chatReasoningRows/chatWindowPad/chatWindowStart

	row := -1
	for absRow, id := range app.chatReasoningRows {
		if id == "r1" {
			row = absRow + app.chatWindowPad - app.chatWindowStart
		}
	}
	if row == -1 {
		t.Fatalf("expected a cached reasoning row for r1, got %v", app.chatReasoningRows)
	}

	if app.expandedReasoning["r1"] {
		t.Fatal("should start collapsed")
	}
	app.handleClick(5, row)
	if !app.expandedReasoning["r1"] {
		t.Fatal("expected the click to expand r1")
	}
	app.handleClick(5, row)
	if app.expandedReasoning["r1"] {
		t.Fatal("expected a second click to collapse r1 again")
	}
}

// TestThinkingSlashCommandCyclesMode is the regression for the global
// toggle TS exposes as "/thinking" (session.toggle.thinking): the same
// slash name here must cycle thinkingMode show <-> hide.
func TestThinkingSlashCommandCyclesMode(t *testing.T) {
	_, server := newMockAPI(t)
	app := newTestApp(t, server.URL)
	if app.thinkingMode != "hide" {
		t.Fatalf("default thinkingMode = %q, want %q", app.thinkingMode, "hide")
	}

	driveCmd(t, app, app.runSlashCommand("/thinking"))
	if app.thinkingMode != "show" {
		t.Fatalf("after /thinking, thinkingMode = %q, want %q", app.thinkingMode, "show")
	}

	driveCmd(t, app, app.runSlashCommand("/thinking"))
	if app.thinkingMode != "hide" {
		t.Fatalf("after a second /thinking, thinkingMode = %q, want %q", app.thinkingMode, "hide")
	}
}

// TestReasoningHeaderColorFadesOnceOpen mirrors ReasoningHeader's fg(): the
// header dims to thinkingOpacity once its body is showing (whether via
// thinkingMode "show" or an individually expanded block), and stays full
// warning brightness while collapsed — inverted from what you'd expect, but
// that's the original's exact behavior.
func TestReasoningHeaderColorFadesOnceOpen(t *testing.T) {
	app := &App{width: 100, height: 30, theme: themeResolve("gocode-dark"), thinkingMode: "hide", expandedReasoning: map[string]bool{}}
	data := assistantWithReasoning(t, `{"agent":"build","finish":"end_turn","content":[{"type":"reasoning","id":"r1","text":"plain body","time":{"created":1000,"completed":1500}}]}`)

	closedBlock, _, _ := app.renderAssistant(client.Message{Type: "assistant"}, data, false)
	app.expandedReasoning["r1"] = true
	openBlock, _, _ := app.renderAssistant(client.Message{Type: "assistant"}, data, false)

	closedANSI := strings.SplitN(closedBlock, "Thought", 2)[0]
	openANSI := strings.SplitN(openBlock, "Thought", 2)[0]
	if closedANSI == openANSI {
		t.Fatalf("expected the header's color escape to differ between collapsed and expanded, both were %q", closedANSI)
	}
}

// builders is the shape applySnapshot hands the renderer: the aggregator's
// live buffers, keyed by part ID.
func builders(texts map[string]string) map[string]*strings.Builder {
	out := map[string]*strings.Builder{}
	for id, text := range texts {
		builder := &strings.Builder{}
		builder.WriteString(text)
		out[id] = builder
	}
	return out
}

func liveApp(mode string, reasoning map[string]string) *App {
	return &App{
		width: 100, height: 30, busy: true,
		theme:              themeResolve("gocode-dark"),
		thinkingMode:       mode,
		expandedReasoning:  map[string]bool{},
		streamingReasoning: builders(reasoning),
	}
}

// The point of the live buffer: thinking shows up while the model is still in
// it, without waiting for the refetch that a settled part rides in on.
func TestStreamingReasoningRendersLiveBlock(t *testing.T) {
	app := liveApp("hide", map[string]string{"msg_a1-reasoning": "**Investigating bug**\n\nRoot cause is X"})
	lines, rows, _ := app.buildTimeline()
	got := plain(strings.Join(lines, "\n"))
	if !strings.Contains(got, "Thinking: Investigating bug") {
		t.Fatalf("expected the live thinking header in the timeline, got %q", got)
	}
	if strings.Contains(got, "Root cause is X") {
		t.Fatalf("hide mode shows the header only until the block is opened, got %q", got)
	}
	found := false
	for _, id := range rows {
		if id == "msg_a1-reasoning" {
			found = true
		}
	}
	if !found {
		t.Fatalf("live header should be clickable to expand, rows = %v", rows)
	}
}

// In show mode (and for an expanded part) the body itself streams: each frame
// renders whatever the buffer holds at that moment.
func TestStreamingReasoningBodyGrowsInShowMode(t *testing.T) {
	app := liveApp("show", map[string]string{"msg_a1-reasoning": "**Investigating bug**\n\nfirst thought"})
	first := plain(strings.Join(app.timelineLines(), "\n"))
	if !strings.Contains(first, "first thought") {
		t.Fatalf("expected the live body, got %q", first)
	}
	app.streamingReasoning["msg_a1-reasoning"].WriteString(" then another")
	// The memo is rate-limited by wall clock (streamRenderFloor), so drop it
	// to see the next frame rather than sleeping through the duty cycle.
	app.streamRender = nil
	second := plain(strings.Join(app.timelineLines(), "\n"))
	if !strings.Contains(second, "first thought then another") {
		t.Fatalf("expected the body to grow with the stream, got %q", second)
	}
}

// Every delta is also written into the stored message, so once any refetch
// lands mid-part the timeline holds a copy of what is streaming. Only one of
// them may render.
func TestStreamingReasoningSuppressesStoredCopy(t *testing.T) {
	app := liveApp("hide", map[string]string{"msg_a-reasoning": "**Investigating bug**\n\nRoot cause is X"})
	app.timeline = []client.Message{{
		ID: "msg_a", SessionID: "ses_1", Type: "assistant", Seq: 1,
		Data: json.RawMessage(`{"agent":"build","content":[{"type":"reasoning","id":"msg_a-reasoning","text":"**Investigating bug**\n\nRoot ca"}]}`),
	}}
	got := plain(strings.Join(app.timelineLines(), "\n"))
	if n := strings.Count(got, "Investigating bug"); n != 1 {
		t.Fatalf("expected the part to render exactly once, got %d in %q", n, got)
	}

	// Once the step settles the buffer is gone and the stored part — now with
	// its duration — is what renders.
	app.streamingReasoning = map[string]*strings.Builder{}
	app.busy = false
	app.timeline[0].Data = json.RawMessage(`{"agent":"build","finish":"stop","content":[{"type":"reasoning","id":"msg_a-reasoning","text":"**Investigating bug**\n\nRoot cause is X","time":{"created":1000,"completed":3500}}]}`)
	app.invalidateRenderCache()
	settled := plain(strings.Join(app.timelineLines(), "\n"))
	if n := strings.Count(settled, "Investigating bug"); n != 1 {
		t.Fatalf("expected one block after settling, got %d in %q", n, settled)
	}
	if !strings.Contains(settled, "+ Thought: Investigating bug · 2.5s") {
		t.Fatalf("expected the settled header once the buffer cleared, got %q", settled)
	}
}
