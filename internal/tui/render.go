package tui

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/langazov/gocode-go/internal/diff"
	"github.com/langazov/gocode-go/internal/tui/client"
	"github.com/langazov/gocode-go/internal/tui/theme"
)

// toolState is an alias (not a new type) for the anonymous tool-state shape
// client.AssistantData's Content items carry, so the render functions below
// can name it without touching that decode struct's call sites.
type toolState = struct {
	Status string         `json:"status"`
	Input  map[string]any `json:"input"`
	Output string         `json:"output"`
	Error  string         `json:"error"`
}

// timelineLines renders the message timeline like the session scrollbox:
// blocks separated by one blank line (marginTop=1). A thin wrapper over
// buildTimeline for callers that only need the lines (existing tests
// included); handleClick needs the reasoning row map too.
func (a *App) timelineLines() []string {
	lines, _ := a.buildTimeline()
	return lines
}

// buildTimeline is timelineLines' real implementation, additionally
// returning which absolute line (by index into the returned lines) is a
// reasoning part's clickable header row — see reasoningHeaderRef.
func (a *App) buildTimeline() (lines []string, reasoningRows map[int]string) {
	var blocks []string
	var blockRefs [][]reasoningHeaderRef
	messages := a.timeline
	if len(messages) > 60 {
		messages = messages[len(messages)-60:]
	}
	for i, message := range messages {
		if block, refs := a.renderMessageCached(message, i == len(messages)-1); block != "" {
			blocks = append(blocks, block)
			blockRefs = append(blockRefs, refs)
		}
	}
	for _, builder := range a.streaming {
		if builder.Len() == 0 {
			continue
		}
		blocks = append(blocks, a.assistantTextBlock(builder.String()))
		blockRefs = append(blockRefs, nil)
	}
	// TS's scrollbox opens with a `<box height={1}/>` spacer above the first
	// message (index.tsx ~1199); only visible once scrolled to the top, but
	// part of the scrollback content the same way here.
	out := []string{""}
	reasoningRows = map[int]string{}
	for i, block := range blocks {
		if i > 0 {
			out = append(out, "") // blank line between messages (marginTop=1)
		}
		blockLines := strings.Split(block, "\n")
		// assistantTextBlock/reasoningBlock bake in their own leading blank
		// line (needed to separate a part from whatever came before it
		// *within* the same message); when a whole message's block happens
		// to start with one too — most commonly a text- or reasoning-first
		// message — drop the duplicate so messages never end up with two
		// blank lines between them instead of one.
		dropped := 0
		if len(blockLines) > 0 && blockLines[0] == "" {
			blockLines = blockLines[1:]
			dropped = 1
		}
		base := len(out)
		for _, ref := range blockRefs[i] {
			reasoningRows[base+ref.line-dropped] = ref.id
		}
		out = append(out, blockLines...)
	}
	return out, reasoningRows
}

// renderMessage returns the message's rendered block plus any reasoning
// header rows within it (relative to the block's own first line — see
// reasoningHeaderRef and buildTimeline, which re-bases them into the full
// timeline).
// renderMessageCached memoizes renderMessage per message.
//
// Rendering a message is not cheap: an assistant message runs its markdown
// through glamour, which parses it and syntax-highlights every fenced block
// through chroma. buildTimeline does that for up to 60 messages, and it runs
// on *every* frame — so every keystroke re-highlighted the whole visible
// history. On a realistic session (60 messages with code blocks) that was 84ms
// per frame, which is exactly the lag you feel when a key repeats.
//
// A settled message's render only changes when something outside it does, so
// the cache key is the message data plus everything else the render reads:
// the content width, whether it is the last message, whether a turn is
// running, and renderEpoch — bumped by the rarer inputs (theme, thinking mode,
// an expanded reasoning block) rather than tracked individually.
func (a *App) renderMessageCached(message client.Message, isLast bool) (string, []reasoningHeaderRef) {
	// The live message is never cached. Its block carries the inline spinner
	// (a running tool row, a streaming reasoning header), which advances every
	// tick — caching it would freeze the one thing on screen that has to move.
	// It is a single message per frame, so re-rendering it costs nothing next
	// to the history behind it.
	if isLast && a.busy {
		return a.renderMessage(message, isLast)
	}
	signature := a.renderSignature(message, isLast)
	if hit, ok := a.messageCache[message.ID]; ok && hit.signature == signature {
		return hit.block, hit.refs
	}
	block, refs := a.renderMessage(message, isLast)
	if a.messageCache == nil {
		a.messageCache = map[string]cachedRender{}
	}
	// The timeline is capped at 60 messages, but a long-lived session cycles
	// through many more; drop the cache wholesale rather than grow forever.
	if len(a.messageCache) > 256 {
		a.messageCache = map[string]cachedRender{}
	}
	a.messageCache[message.ID] = cachedRender{signature: signature, block: block, refs: refs}
	return block, refs
}

// renderSignature hashes everything renderMessage's output depends on. The
// inputs it does *not* hash directly — the theme, the thinking mode, expanded
// reasoning blocks, and the model-name catalog — all fold into renderEpoch,
// which their own mutation sites bump.
func (a *App) renderSignature(message client.Message, isLast bool) uint64 {
	h := fnv.New64a()
	h.Write(message.Data)
	var scalars [8]byte
	binary.LittleEndian.PutUint64(scalars[:], uint64(a.contentWidth()))
	h.Write(scalars[:])
	binary.LittleEndian.PutUint64(scalars[:], a.renderEpoch)
	h.Write(scalars[:])
	binary.LittleEndian.PutUint64(scalars[:], uint64(message.TimeCreated))
	h.Write(scalars[:])
	flags := byte(0)
	if isLast {
		flags |= 1
	}
	if a.busy {
		flags |= 2
	}
	if a.timestamps {
		flags |= 4
	}
	h.Write([]byte{flags})
	h.Write([]byte(message.Type))
	return h.Sum64()
}

// invalidateRenderCache bumps the epoch every cached render is keyed against.
// Used for the inputs that are cheaper to invalidate wholesale than to track:
// the theme, the thinking mode, and per-part reasoning expansion.
func (a *App) invalidateRenderCache() { a.renderEpoch++ }

func (a *App) renderMessage(message client.Message, isLast bool) (string, []reasoningHeaderRef) {
	switch message.Type {
	case "user":
		data, err := client.DecodeUser(message.Data)
		if err != nil || data.Text == "" {
			return "", nil
		}
		return a.userBlock(message, data), nil
	case "assistant":
		data, err := client.DecodeAssistant(message.Data)
		if err != nil {
			return "", nil
		}
		return a.renderAssistant(message, data, isLast)
	case "compaction":
		return a.compactionSeparator(), nil
	}
	return "", nil
}

// userBlock mirrors UserMessage: a ┃ left border in the agent color around a
// backgroundPanel block (padding 1/1/2) with the plain message text, plus a
// muted timestamp when enabled.
//
// Width is borderBoxWidth(contentWidth()-2) so the rendered total lands at
// contentWidth()-1, matching assistantTextBlock's own max reach (indent(3) +
// renderMarkdown wrap width contentWidth()-4 = contentWidth()-1) — every
// bordered timeline panel (userBlock, errBlock, blockToolStyle) and the
// session prompt box shrink to that same total instead of widening the
// markdown side, since renderMarkdown's wrap decisions run on raw source
// width and need that spare column of margin (see markdown.go's doc
// comment) rather than being pushed out to fill a wider box.
func (a *App) userBlock(message client.Message, data client.UserData) string {
	body := wrapText(data.Text, a.contentWidth()-4)
	lines := []string{a.styles().Text.Render(body)}
	if len(data.Files) > 0 {
		lines = append(lines, "")
		lines = append(lines, a.fileAttachmentRows(data.Files, a.contentWidth()-4)...)
	}
	if a.timestamps && message.TimeCreated > 0 {
		lines = append(lines, a.styles().Muted.Render(todayTimeOrDateTime(message.TimeCreated)))
	}
	style := lipgloss.NewStyle().
		Border(splitBorder(), false, false, false, true).
		BorderForeground(a.theme.Primary).
		Background(a.theme.BackgroundPanel).
		PaddingTop(1).
		PaddingBottom(1).
		PaddingLeft(2).
		Width(borderBoxWidth(a.contentWidth() - 2))
	return style.Render(strings.Join(lines, "\n"))
}

// fileAttachmentRows mirrors UserMessage's file pills: a " Directory "/" File
// " badge (theme.secondary bg) followed by " <name> " (theme.backgroundElement
// bg), wrapped (flexWrap) at width without breaking a pill mid-render.
func (a *App) fileAttachmentRows(files []client.FileAttachment, width int) []string {
	badge := lipgloss.NewStyle().Background(a.theme.Secondary).Foreground(a.theme.Background)
	name := lipgloss.NewStyle().Background(a.theme.BackgroundElement).Foreground(a.theme.TextMuted)
	pills := make([]string, 0, len(files))
	for _, file := range files {
		label := " File "
		if file.Mime == "application/x-directory" {
			label = " Directory "
		}
		pills = append(pills, badge.Render(label)+name.Render(" "+file.Name+" "))
	}
	return wrapPills(pills, width)
}

// wrapPills packs items onto lines separated by one space, never splitting
// an item across lines.
func wrapPills(pills []string, width int) []string {
	var rows []string
	var current []string
	currentWidth := 0
	for _, p := range pills {
		pw := lipgloss.Width(p)
		sep := 0
		if len(current) > 0 {
			sep = 1
		}
		if len(current) > 0 && currentWidth+sep+pw > width {
			rows = append(rows, strings.Join(current, " "))
			current = nil
			currentWidth = 0
			sep = 0
		}
		current = append(current, p)
		currentWidth += sep + pw
	}
	if len(current) > 0 {
		rows = append(rows, strings.Join(current, " "))
	}
	return rows
}

// compactionSeparator mirrors the compaction marker: a full-width horizontal
// rule with the centered " Compaction " title in borderActive.
func (a *App) compactionSeparator() string {
	title := " Compaction "
	w := a.contentWidth()
	side := (w - len(title)) / 2
	if side < 0 {
		side = 0
	}
	rule := strings.Repeat("─", side) + title + strings.Repeat("─", w-side-len(title))
	return lipgloss.NewStyle().Foreground(a.theme.BorderActive).Render(rule)
}

// messageAborted approximates TS's `error?.name === "MessageAbortedError"`:
// the wire schema this port reads doesn't carry an error name, only a
// message string, so this matches on the text the TS abort path produces.
// messageAborted reports whether a settled assistant message was interrupted
// rather than failed — the port's `error.name === "MessageAbortedError"`.
//
// The runner tags this explicitly (session.ErrorTypeAborted). The message
// probe behind it is a fallback for rows written before that tagging existed;
// note that it never matched the runner's own wording ("context canceled"),
// which is why an interrupted turn used to render as a plain error with no
// "· interrupted" marker at all.
func messageAborted(data client.AssistantData) bool {
	if data.Error == nil {
		return false
	}
	if data.Error.Type == "aborted" {
		return true
	}
	return strings.Contains(data.Error.Message, "aborted") ||
		strings.Contains(data.Error.Message, "interrupted") ||
		strings.Contains(data.Error.Message, "context canceled")
}

// renderAssistant mirrors AssistantMessage: reasoning, text, and tool parts,
// then the error block, then the "▣ Agent · model · duration" settlement
// line. The second return value locates each reasoning part's clickable
// header line within the joined block this function returns (relative to
// its own line 0) — see reasoningHeaderRef.
func (a *App) renderAssistant(message client.Message, data client.AssistantData, isLast bool) (string, []reasoningHeaderRef) {
	var blocks []string
	var refs []reasoningHeaderRef
	lineOffset := 0
	appendBlock := func(block string) {
		blocks = append(blocks, block)
		lineOffset += strings.Count(block, "\n") + 1
	}
	running := a.busy && data.Finish == ""
	for _, part := range data.Content {
		switch part.Type {
		case "reasoning":
			var partTime *reasoningPartTime
			if part.Time != nil {
				partTime = &reasoningPartTime{Created: part.Time.Created, Completed: part.Time.Completed}
			}
			if block := a.reasoningBlock(part.ID, running, part.Text, partTime); block != "" {
				// reasoningBlock's leading "\n" (marginTop=1) puts the header
				// on the block's line 1, not line 0.
				refs = append(refs, reasoningHeaderRef{id: part.ID, line: lineOffset + 1})
				appendBlock(block)
			}
		case "text":
			if part.Text != "" {
				appendBlock(a.assistantTextBlock(part.Text))
			}
		case "tool":
			if block := a.toolRow(message, part.Name, part.State); block != "" {
				appendBlock(block)
			}
		}
	}

	// An interruption is not an error to report: upstream guards this block
	// with `error.name !== "MessageAbortedError"` and lets the settlement
	// line's "· interrupted" marker carry it instead.
	if data.Error != nil && data.Error.Message != "" && !messageAborted(data) {
		errBlock := lipgloss.NewStyle().
			Border(splitBorder(), false, false, false, true).
			BorderForeground(a.theme.Error).
			Background(a.theme.BackgroundPanel).
			PaddingTop(1).
			PaddingBottom(1).
			PaddingLeft(2).
			Width(borderBoxWidth(a.contentWidth() - 2))
		appendBlock(errBlock.Render(a.styles().Muted.Render(data.Error.Message)))
	}

	final := data.Finish != "" && data.Finish != "tool-calls" && data.Finish != "unknown"
	if isLast || final || messageAborted(data) {
		appendBlock("")
		appendBlock(a.settlementLine(message, data))
	}
	return strings.Join(blocks, "\n"), refs
}

// settlementLine mirrors the assistant's final row: ▣ (muted once aborted,
// otherwise the agent color — TS actually colors this per-agent via
// local.agent.color(), which this port doesn't have yet) then two spaces,
// the titlecased mode (TS keys this off a distinct message.mode field this
// port's AssistantData doesn't carry; agent is the closest available), then
// muted model name and duration segments.
func (a *App) settlementLine(message client.Message, data client.AssistantData) string {
	agent := data.Agent
	if agent == "" {
		agent = "build"
	}
	aborted := messageAborted(data)
	final := data.Finish != "" && data.Finish != "tool-calls" && data.Finish != "unknown"
	icon := a.theme.Primary
	if aborted {
		icon = a.theme.TextMuted
	}
	segments := []string{
		lipgloss.NewStyle().Foreground(icon).Render("▣ "),
		" ",
		a.styles().Text.Render(titlecase(agent)),
	}
	model := a.modelName(data.Model.ProviderID, data.Model.ID)
	segments = append(segments, a.styles().Muted.Render(" · "+model))
	if final && message.TimeCreated > 0 && data.Time.Completed > 0 {
		segments = append(segments, a.styles().Muted.Render(
			" · "+durationLabel(data.Time.Completed-message.TimeCreated)))
	}
	if aborted {
		segments = append(segments, a.styles().Muted.Render(" · interrupted"))
	}
	return strings.Join([]string{"   ", strings.Join(segments, "")}, "")
}

// reasoningPartTime is the subset of a reasoning content part's Time this
// port needs — its own package-level type (rather than reusing
// client.AssistantData's anonymous Content[].Time) just for a readable
// reasoningBlock signature.
type reasoningPartTime struct {
	Created   int64
	Completed int64
}

// reasoningHeaderRef marks the line (relative to the start of the assistant
// message's own rendered block, i.e. before renderMessage/timelineLines
// re-bases it into the full timeline) that is a reasoning part's clickable
// header row — mirrors ReasoningPart's `<box onMouseUp={toggle}>`. Threaded
// back up through renderAssistant/renderMessage/timelineLines so
// handleClick (mouse.go) can hit-test it against what's actually on screen.
type reasoningHeaderRef struct {
	id   string
	line int
}

// nextThinkingMode mirrors nextThinkingMode() in context/thinking.ts: the
// slash command / palette action cycles show -> hide -> show.
func nextThinkingMode(current string) string {
	if current == "show" {
		return "hide"
	}
	return "show"
}

// thinkingToggleHint mirrors the dynamic palette title next to
// "session.toggle.thinking" in index.tsx: named for what the action is
// about to *do*, not the mode it's about to enter.
func thinkingToggleHint(current string) string {
	if nextThinkingMode(current) == "hide" {
		return "Collapse thinking"
	}
	return "Expand thinking"
}

// reasoningTitleRe implements reasoningSummary's title extraction exactly:
// OpenAI's Responses API surfaces reasoning summaries that start with a
// bolded title block ("**Inspecting PR workflow**\n\n<body>"); this is the
// one shape treated as a title separate from the body, matching
// context/thinking.ts's regex byte for byte (no title for anything else,
// including a plain first line — the previous port took *any* first line as
// the title, which this replaces).
var reasoningTitleRe = regexp.MustCompile(`^\*\*([^*\n]+)\*\*(?:\r?\n\r?\n|$)`)

// reasoningSummary mirrors reasoningSummary() in context/thinking.ts.
func reasoningSummary(text string) (title, body string) {
	content := strings.TrimSpace(text)
	loc := reasoningTitleRe.FindStringSubmatchIndex(content)
	if loc == nil {
		return "", content
	}
	title = strings.TrimSpace(content[loc[2]:loc[3]])
	body = strings.TrimRight(content[loc[1]:], " \t\r\n")
	return title, body
}

// reasoningBlock mirrors ReasoningPart/ReasoningHeader exactly:
//
//   - running: a warning-colored spinner, "Thinking" or "Thinking: <title>".
//   - done, thinkingMode "show" (or this id individually expanded): a
//     "Thought[: title · duration]" header, faded to thinkingOpacity once
//     the body is showing, followed by the muted markdown body.
//   - done, thinkingMode "hide" and not expanded (the default): the same
//     header collapsed to one line, prefixed "+ " (toggleable, closed) —
//     full warning brightness so the one-line summary still stands out.
//
// Not ported: TS's "opaque" (encrypted-with-no-text-but-metadata) case —
// this port's wire schema carries no per-part metadata to detect it (no
// provider populates one either), so a fully redacted block with literally
// no visible text renders nothing, rather than TS's bare "Thought" line.
func (a *App) reasoningBlock(id string, running bool, rawText string, partTime *reasoningPartTime) string {
	// TS's `.replace("[REDACTED]", "")` (a plain-string, non-global pattern)
	// only ever removes the first occurrence; strings.Replace's count=1
	// matches that exactly (Replace-all would over-strip a block containing
	// more than one placeholder).
	content := strings.TrimSpace(strings.Replace(rawText, "[REDACTED]", "", 1))
	if content == "" {
		return ""
	}
	title, body := reasoningSummary(content)

	inMinimal := a.thinkingMode != "show"
	open := !inMinimal || a.expandedReasoning[id]

	fg := a.theme.Warning
	if open {
		fg = theme.FadeColor(a.theme.Background, a.theme.Warning, a.theme.ThinkingOpacity)
	}
	headerStyle := lipgloss.NewStyle().Foreground(fg)

	var header string
	if running {
		frame := a.spinnerGlyph()
		label := "Thinking"
		if title != "" {
			label = "Thinking: " + title
		}
		header = headerStyle.Render(frame + " " + label)
	} else {
		prefix := ""
		if inMinimal {
			if open {
				prefix = "- "
			} else {
				prefix = "+ "
			}
		}
		var detail []string
		if title != "" {
			detail = append(detail, title)
		}
		if partTime != nil && partTime.Completed > 0 {
			detail = append(detail, durationLabel(partTime.Completed-partTime.Created))
		}
		label := "Thought"
		if len(detail) > 0 {
			label += ": " + strings.Join(detail, " · ")
		}
		header = headerStyle.Render(prefix + label)
	}

	// marginTop=1 on ReasoningPart's outer box: a leading blank line so this
	// block never sticks directly to whatever the previous part rendered —
	// see assistantTextBlock's identical convention (its own doc comment).
	out := "\n" + strings.Repeat(" ", 3) + header
	if open && body != "" {
		extraIndent := 0
		if inMinimal {
			extraIndent = 2
		}
		out += "\n" + a.reasoningBody(body, extraIndent)
	}
	return out
}

func (a *App) reasoningBody(body string, extraIndent int) string {
	if body == "" {
		return ""
	}
	return aIndent(a.styles().Muted.Render(wrapText(body, a.contentWidth()-4-extraIndent)), 3+extraIndent)
}

// assistantTextBlock mirrors TextPart: markdown-rendered (see markdown.go),
// indented by 3. The wrap width stays contentWidth()-4 (renderMarkdown's own
// doc comment explains why: wrap decisions run on raw markdown source width,
// so a span whose markers are wider than its rendered form — "**bold**" is 8
// columns of source for 4 rendered — needs that spare column of margin to
// never overflow). Every bordered timeline panel (userBlock, errBlock,
// blockToolStyle) is instead sized to match this block's own max reach —
// indent(3) + wrap width contentWidth()-4 = contentWidth()-1 — rather than
// widening the markdown side to fill a wider box.
func (a *App) assistantTextBlock(text string) string {
	body := indent(a.renderMarkdown(text, a.contentWidth()-4), 3)
	return "\n" + body
}

// toolRow mirrors the InlineTool renderers: a muted icon row per tool with
// per-tool labels derived from the tool input. Pending tools render as a
// "~ " line, running tools attach the spinner, and failed tools turn error
// colored. bash/edit/todowrite switch to a bordered BlockTool-style panel
// once they have something to show beyond the one-line summary.
func (a *App) toolRow(message client.Message, name string, state *toolState) string {
	if state == nil {
		return a.styles().Muted.Render("   ⚙ " + name)
	}
	if state.Status != "pending" {
		switch name {
		case "bash":
			return a.bashBlock(state)
		case "edit":
			if block := a.editDiffBlock(state); block != "" {
				return block
			}
		case "todowrite":
			if block := a.todoWriteBlock(state); block != "" {
				return block
			}
		}
	}
	icon, label := toolLabel(name, state.Input)
	if name == "task" && state.Status != "pending" && state.Status != "running" && state.Status != "error" {
		icon = "✓" // TS: state.status === "completed" ? "✓" : "│"
	}
	switch state.Status {
	case "pending":
		return a.styles().Text.Render(strings.Repeat(" ", 6) + "~ " + label)
	case "running":
		// TS only swaps in the live spinner glyph for bash/read/task; every
		// other tool sits static in the muted icon the whole time, so
		// running and done render identically for them.
		if name == "bash" || name == "read" || name == "task" {
			frame := a.spinnerGlyph()
			return a.styles().Muted.Render("   " + frame + " " + label)
		}
		return a.styles().Muted.Render("   " + icon + " " + label)
	case "error":
		return a.styles().Error.Render("   " + icon + " " + label)
	default:
		return a.styles().Muted.Render("   " + icon + " " + label)
	}
}

// blockToolStyle mirrors BlockTool's chrome: a border colored to match the
// background (invisible — only there for the corner shape opentui's
// customBorderChars draws), backgroundPanel fill, and the same
// padding/width every other timeline panel (errBlock, userBlock) uses.
func (a *App) blockToolStyle() lipgloss.Style {
	return lipgloss.NewStyle().
		Border(splitBorder(), false, false, false, true).
		BorderForeground(a.theme.Background).
		Background(a.theme.BackgroundPanel).
		PaddingTop(1).
		PaddingBottom(1).
		PaddingLeft(2).
		Width(borderBoxWidth(a.contentWidth() - 2))
}

// bashBlock mirrors Shell's BlockTool: the command line (spinner while
// running, "$ " once settled) followed by the collapsed output, once
// there's more to show than the one-line summary.
func (a *App) bashBlock(state *toolState) string {
	command, _ := state.Input["command"].(string)
	if command == "" {
		command = "Writing command..."
	}
	var lines []string
	if state.Status == "running" {
		frame := a.spinnerGlyph()
		lines = append(lines, a.styles().Text.Render(frame+" "+command))
	} else {
		lines = append(lines, a.styles().Text.Render("$ "+command))
	}
	if output := strings.TrimSpace(ansi.Strip(state.Output)); output != "" {
		maxChars := 10 * max(20, a.contentWidth()-6)
		limited, overflow := collapseToolOutput(output, 10, maxChars)
		lines = append(lines, "", a.styles().Text.Render(limited))
		if overflow {
			lines = append(lines, a.styles().Muted.Render("(truncated)"))
		}
	}
	if state.Status == "error" && state.Error != "" {
		lines = append(lines, a.styles().Error.Render(state.Error))
	}
	return a.blockToolStyle().Render(strings.Join(lines, "\n"))
}

// editDiffBlock renders a tool's unified diff.
//
// The diff is parsed with internal/diff (sourcegraph/go-diff underneath)
// rather than classified by string prefix, which is what gives us hunk
// headers and real line numbers in the gutter. The diff text itself comes
// from the fenced ```diff block that edit.go and apply_patch.go embed in
// their output, matching how TS carries a unified diff in tool metadata.
// Returns "" (falling back to the one-line summary) when there is nothing
// to show.
func (a *App) editDiffBlock(state *toolState) string {
	block := parseDiffPreview(state.Output)
	if block == "" {
		return ""
	}
	files := diff.Parse(block)
	if len(files) == 0 {
		return ""
	}

	path, _ := state.Input["filePath"].(string)
	title := "← Edit"
	if path != "" {
		title = "← Edit " + path
	}
	var additions, deletions int
	for _, file := range files {
		additions += file.Stat.Additions
		deletions += file.Stat.Deletions
	}
	if additions > 0 || deletions > 0 {
		title += fmt.Sprintf("  +%d -%d", additions, deletions)
	}

	styles := a.styles()
	added := lipgloss.NewStyle().Foreground(a.theme.Success)
	removed := lipgloss.NewStyle().Foreground(a.theme.Error)
	lines := []string{styles.Muted.Render(title)}

	// Line numbers are right-aligned to a width derived from the largest one
	// on show, so the gutter does not jitter between hunks.
	width := gutterWidth(files)
	rendered := 0
	for _, file := range files {
		if len(files) > 1 {
			lines = append(lines, styles.Muted.Render(file.Name()))
		}
		for _, line := range file.Lines {
			if rendered >= maxRenderedDiffLines {
				lines = append(lines, styles.Muted.Render("  … diff truncated"))
				return a.finishDiffBlock(lines, state)
			}
			rendered++
			switch line.Kind {
			case diff.LineHunk:
				lines = append(lines, styles.Muted.Render(line.Content))
			case diff.LineAdded:
				lines = append(lines, added.Render(gutter(0, line.NewLine, width)+"+ "+line.Content))
			case diff.LineRemoved:
				lines = append(lines, removed.Render(gutter(line.OldLine, 0, width)+"- "+line.Content))
			case diff.LineMeta:
				lines = append(lines, styles.Muted.Render(line.Content))
			default:
				lines = append(lines, styles.Muted.Render(gutter(line.OldLine, line.NewLine, width)+"  "+line.Content))
			}
		}
	}
	return a.finishDiffBlock(lines, state)
}

// maxRenderedDiffLines bounds a single diff block so one large edit cannot
// push the rest of the conversation off screen.
const maxRenderedDiffLines = 40

func (a *App) finishDiffBlock(lines []string, state *toolState) string {
	if state.Status == "error" && state.Error != "" {
		lines = append(lines, a.styles().Error.Render(state.Error))
	}
	return a.blockToolStyle().Render(strings.Join(lines, "\n"))
}

// gutterWidth sizes the line-number column from the largest number shown.
func gutterWidth(files []diff.File) int {
	largest := 0
	for _, file := range files {
		for _, line := range file.Lines {
			largest = max(largest, line.OldLine, line.NewLine)
		}
	}
	width := len(strconv.Itoa(largest))
	if width < 2 {
		width = 2
	}
	return width
}

// gutter renders the old/new line-number pair, blanking the side a line does
// not exist on.
func gutter(oldLine, newLine, width int) string {
	return pad(oldLine, width) + " " + pad(newLine, width) + " "
}

func pad(value, width int) string {
	if value == 0 {
		return strings.Repeat(" ", width)
	}
	text := strconv.Itoa(value)
	if len(text) >= width {
		return text
	}
	return strings.Repeat(" ", width-len(text)) + text
}

// parseDiffPreview extracts the text inside the fenced ```diff block that the
// edit and apply_patch tools embed in their output.
func parseDiffPreview(output string) string {
	start := strings.Index(output, "```diff")
	if start == -1 {
		return ""
	}
	rest := output[start+len("```diff"):]
	end := strings.Index(rest, "```")
	if end == -1 {
		end = len(rest)
	}
	return strings.Trim(rest[:end], "\n")
}

// todoWriteBlock mirrors TodoWrite's "# Todos" BlockTool: internal/tool/
// builtins/todo.go returns the replaced list as its Output (JSON-encoded,
// the same shape as client.Todo), so this decodes and renders it with the
// same rows the sidebar uses. Returns "" (falling back to the one-line
// "Updating todos..." summary) until the output decodes to a non-empty list.
func (a *App) todoWriteBlock(state *toolState) string {
	var todos []client.Todo
	if err := json.Unmarshal([]byte(state.Output), &todos); err != nil || len(todos) == 0 {
		return ""
	}
	lines := []string{a.styles().Muted.Render("# Todos")}
	for _, todo := range todos {
		lines = append(lines, "  "+a.todoRow(todo))
	}
	return a.blockToolStyle().Render(strings.Join(lines, "\n"))
}

// collapseToolOutput ports util/collapse-tool-output.ts: caps at maxLines
// lines or maxChars runes, whichever is hit first. The "…" lands inline if
// the char cap bites within the line-capped preview, otherwise as its own
// trailing line.
func collapseToolOutput(output string, maxLines, maxChars int) (string, bool) {
	lines := strings.Split(output, "\n")
	if len(lines) <= maxLines && utf8.RuneCountInString(output) <= maxChars {
		return output, false
	}
	take := maxLines
	if take > len(lines) {
		take = len(lines)
	}
	preview := strings.Join(lines[:take], "\n")
	previewRunes := []rune(preview)
	if len(previewRunes) > maxChars {
		cut := maxChars - 1
		if cut < 0 {
			cut = 0
		}
		return string(previewRunes[:cut]) + "…", true
	}
	if len(lines) > maxLines {
		return preview + "\n…", true
	}
	return preview, true
}

// toolLabel maps a tool name plus its input to the InlineTool icon and
// label, mirroring the per-tool renderers (Shell, Read, Edit, …).
func toolLabel(name string, input map[string]any) (icon, label string) {
	text := func(key string) string {
		value, _ := input[key].(string)
		return value
	}
	switch name {
	case "bash":
		if command := text("command"); command != "" {
			return "$", command
		}
		return "$", "Writing command..."
	case "read":
		if path := text("filePath"); path != "" {
			return "→", "Read " + path
		}
		return "→", "Reading file..."
	case "edit":
		if path := text("filePath"); path != "" {
			return "←", "Edit " + path
		}
		return "←", "Preparing edit..."
	case "write":
		if path := text("filePath"); path != "" {
			return "←", "Write " + path
		}
		return "←", "Preparing write..."
	case "glob":
		if pattern := text("pattern"); pattern != "" {
			return "✱", fmt.Sprintf("Glob %q", pattern)
		}
		return "✱", "Finding files..."
	case "grep":
		if pattern := text("pattern"); pattern != "" {
			return "✱", fmt.Sprintf("Grep %q", pattern)
		}
		return "✱", "Searching content..."
	case "webfetch":
		if url := text("url"); url != "" {
			return "%", url
		}
		return "%", "Fetching from the web..."
	case "todowrite":
		return "⚙", "Updating todos..."
	case "task":
		// Forward-looking: Go has no "task" (subagent spawn) tool yet
		// (go-port-gaps.md P2), so this can't be exercised end to end today,
		// but mirrors formatSubagentTitle's label shape (icon is finished by
		// toolRow, which knows the status: ✓ once completed, │ otherwise).
		description := text("description")
		if description == "" {
			return "│", "Delegating..."
		}
		subagentType := text("subagent_type")
		if subagentType == "" {
			subagentType = "General"
		}
		title := titlecase(subagentType) + " Task"
		if background, _ := input["background"].(bool); background {
			title += " (background)"
		}
		return "│", title + " — " + description
	}
	if len(input) > 0 {
		encoded, _ := json.Marshal(input)
		return "⚙", name + " " + string(encoded)
	}
	return "⚙", name
}

// durationLabel mirrors Locale.duration.
func durationLabel(millis int64) string {
	switch {
	case millis < 1000:
		return fmt.Sprintf("%dms", millis)
	case millis < 60000:
		return fmt.Sprintf("%.1fs", float64(millis)/1000)
	case millis < 3600000:
		return fmt.Sprintf("%dm %ds", millis/60000, millis%60000/1000)
	case millis < 86400000:
		return fmt.Sprintf("%dh %dm", millis/3600000, millis%3600000/60000)
	default:
		return fmt.Sprintf("%dd %dh", millis/86400000, millis%86400000/3600000)
	}
}

// todayTimeOrDateTime mirrors Locale.todayTimeOrDateTime.
func todayTimeOrDateTime(millis int64) string {
	t := time.UnixMilli(millis)
	if t.Format("Mon Jan 2 2006") == time.Now().Format("Mon Jan 2 2006") {
		return t.Format("3:04 PM")
	}
	return t.Format("Mon Jan 2, 3:04 PM")
}

func titlecase(value string) string {
	if value == "" {
		return value
	}
	return strings.ToUpper(value[:1]) + value[1:]
}

func indent(value string, spaces int) string {
	pad := strings.Repeat(" ", spaces)
	lines := strings.Split(value, "\n")
	for i, line := range lines {
		if lines[i] != "" {
			lines[i] = pad + line
		}
	}
	return strings.Join(lines, "\n")
}

// aIndent indents every line of a possibly styled block (ANSI-safe).
func aIndent(value string, spaces int) string {
	pad := strings.Repeat(" ", spaces)
	lines := strings.Split(value, "\n")
	for i := range lines {
		lines[i] = pad + lines[i]
	}
	return strings.Join(lines, "\n")
}

func wrapText(value string, width int) string {
	if width <= 0 {
		return value
	}
	var out []string
	for _, paragraph := range strings.Split(value, "\n") {
		if paragraph == "" {
			out = append(out, "")
			continue
		}
		words := strings.Fields(paragraph)
		line := ""
		for _, word := range words {
			switch {
			case line == "":
				line = word
			case len(line)+1+len(word) <= width:
				line += " " + word
			default:
				out = append(out, line)
				line = word
			}
		}
		if line != "" {
			out = append(out, line)
		}
	}
	return strings.Join(out, "\n")
}

// splitBorder mirrors SplitBorder: the ┃ vertical bar.
func splitBorder() lipgloss.Border {
	return lipgloss.Border{Left: "┃"}
}

// borderBoxWidth converts a "content+padding width, with the single left
// border column rendered outside it" total — what every single-left-border
// panel in this file (userBlock, errBlock, blockToolStyle, promptBox) was
// tuned against under lipgloss v1's Style.Width(), which excluded the
// border — into what lipgloss v2's Width() needs: v2's Width() is true
// border-box (the declared value IS the total rendered size, border
// included), so reaching the same on-screen total now needs the border
// column added back into the argument instead of left for the border to add
// on top. One left border column, hence +1.
func borderBoxWidth(contentAndPadding int) int {
	return contentAndPadding + 1
}
