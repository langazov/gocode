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
	lines, _, _ := a.buildTimeline()
	return lines
}

// buildTimeline is timelineLines' real implementation, additionally
// returning which absolute line (by index into the returned lines) is a
// reasoning part's clickable header row (see reasoningHeaderRef) or a
// collapsed tool-output's clickable summary row (see toolOutputHeaderRef).
func (a *App) buildTimeline() (lines []string, reasoningRows map[int]string, toolOutputRows map[int]string) {
	var blocks []string
	var blockRefs [][]reasoningHeaderRef
	var blockToolRefs [][]toolOutputHeaderRef
	messages := a.timeline
	if len(messages) > 60 {
		messages = messages[len(messages)-60:]
	}
	for i, message := range messages {
		if block, refs, toolRefs := a.renderMessageCached(message, i == len(messages)-1); block != "" {
			blocks = append(blocks, block)
			blockRefs = append(blockRefs, refs)
			blockToolRefs = append(blockToolRefs, toolRefs)
		}
	}
	// Live thinking, above the live text: within a step the model reasons
	// before it answers, and a reasoning part ID is its message's ID plus a
	// fixed suffix, so sorting by ID sorts by step.
	for _, id := range sortedKeys(a.streamingReasoning) {
		builder := a.streamingReasoning[id]
		if builder.Len() == 0 {
			continue
		}
		block := a.streamingReasoningBlock(id, builder.String())
		if block == "" {
			continue
		}
		blocks = append(blocks, block)
		// reasoningBlock's leading blank line is dropped below, which puts
		// its header on the block's line 1 — same offset renderAssistant
		// records for a stored part, so a live block is clickable too.
		blockRefs = append(blockRefs, []reasoningHeaderRef{{id: id, line: 1}})
		blockToolRefs = append(blockToolRefs, nil)
	}
	// Live assistant text, in message-ID order. The map iteration this used
	// to do reordered concurrent live messages between frames.
	for _, id := range sortedKeys(a.streaming) {
		builder := a.streaming[id]
		if builder.Len() == 0 {
			continue
		}
		blocks = append(blocks, a.streamingTextBlock(id, builder.String()))
		blockRefs = append(blockRefs, nil)
		blockToolRefs = append(blockToolRefs, nil)
	}
	// Prompts sent while the turn was still running, below everything the
	// assistant has produced so far — the position upstream's queued user
	// messages occupy, since they are the newest messages in the session.
	for _, block := range a.queuedBlocks() {
		blocks = append(blocks, block)
		blockRefs = append(blockRefs, nil)
		blockToolRefs = append(blockToolRefs, nil)
	}
	// TS's scrollbox opens with a `<box height={1}/>` spacer above the first
	// message (index.tsx ~1199); only visible once scrolled to the top, but
	// part of the scrollback content the same way here.
	out := []string{""}
	reasoningRows = map[int]string{}
	toolOutputRows = map[int]string{}
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
		for _, ref := range blockToolRefs[i] {
			for row := ref.lineStart; row <= ref.lineEnd; row++ {
				toolOutputRows[base+row-dropped] = ref.id
			}
		}
		out = append(out, blockLines...)
	}
	return out, reasoningRows, toolOutputRows
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
//
// The live message is cached alongside the settled ones. It used to be
// exempt, because its block carries the inline spinner (a running tool row, a
// streaming reasoning header) and caching it would have frozen the one thing
// on screen that has to move. That exemption was the single most expensive
// thing the interface did: the live message is also the *longest* one — it is
// the response still growing — so every spinner tick and every keystroke
// re-parsed and re-highlighted the whole of it. Measured at 60 settled
// messages behind a 16KB live one, a frame cost 107ms against 1.8ms for the
// same timeline with the turn settled, and a keypress cost the same 107ms
// (BenchmarkBusyFrame16KB / BenchmarkBusyKeypress16KB).
//
// The spinner keeps animating because renderMessage emits spinnerPlaceholder
// rather than the glyph, and the frame is substituted in below — on the
// cached string, after the cache lookup.
func (a *App) renderMessageCached(message client.Message, isLast bool) (string, []reasoningHeaderRef, []toolOutputHeaderRef) {
	signature := a.renderSignature(message, isLast)
	if hit, ok := a.messageCache[message.ID]; ok && hit.signature == signature {
		return a.substituteSpinner(hit.block), hit.refs, hit.toolRefs
	}
	block, refs, toolRefs := a.renderMessage(message, isLast)
	if a.messageCache == nil {
		a.messageCache = map[string]cachedRender{}
	}
	// The timeline is capped at 60 messages, but a long-lived session cycles
	// through many more; drop the cache wholesale rather than grow forever.
	if len(a.messageCache) > 256 {
		a.messageCache = map[string]cachedRender{}
	}
	a.messageCache[message.ID] = cachedRender{signature: signature, block: block, refs: refs, toolRefs: toolRefs}
	return a.substituteSpinner(block), refs, toolRefs
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

func (a *App) renderMessage(message client.Message, isLast bool) (string, []reasoningHeaderRef, []toolOutputHeaderRef) {
	switch message.Type {
	case "user":
		data, err := client.DecodeUser(message.Data)
		if err != nil || data.Text == "" {
			return "", nil, nil
		}
		return a.userBlock(message, data), nil, nil
	case "assistant":
		data, err := client.DecodeAssistant(message.Data)
		if err != nil {
			return "", nil, nil
		}
		return a.renderAssistant(message, data, isLast)
	case "compaction":
		return a.compactionSeparator(), nil, nil
	}
	return "", nil, nil
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
	return a.userBlockOf(data, message.TimeCreated, false)
}

// userBlockOf is userBlock's body, also used for a prompt that is still
// waiting its turn — which has no message row of its own to pass in.
//
// queued swaps the timestamp for UserMessage's ` QUEUED ` badge (bold, on the
// same color as the block's border, with the theme's selected-item
// foreground). Upstream shows the badge *instead of* the timestamp and adds
// the blank line under the file pills that the timestamp would otherwise get
// (`metadataVisible` in index.tsx).
func (a *App) userBlockOf(data client.UserData, created int64, queued bool) string {
	body := wrapText(data.Text, a.contentWidth()-4)
	lines := []string{renderLines(a.styles().Text, body)}
	metadata := queued || (a.timestamps && created > 0)
	if len(data.Files) > 0 {
		lines = append(lines, "")
		lines = append(lines, a.fileAttachmentRows(data.Files, a.contentWidth()-4)...)
		if metadata {
			lines = append(lines, "")
		}
	}
	switch {
	case queued:
		badge := lipgloss.NewStyle().
			Background(a.theme.Primary).
			Foreground(a.theme.SelectedListItemText).
			Bold(true)
		lines = append(lines, badge.Render(" QUEUED "))
	case a.timestamps && created > 0:
		lines = append(lines, a.styles().Muted.Render(todayTimeOrDateTime(created)))
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

// queuedBlocks renders the prompts waiting behind the running turn, oldest
// first.
//
// These are inbox rows, not messages: a prompt admitted while a turn is
// running has no session_message row until the runner promotes it, which is
// what keeps it out of the history the model is given. So where upstream
// derives "queued" from message order — any user message past the last
// unfinished assistant one (`pending` in session/index.tsx) — this port reads
// the queue the server actually holds. Once a prompt is promoted it leaves
// the queue and arrives as a normal user message in the timeline above.
func (a *App) queuedBlocks() []string {
	if len(a.queued) == 0 {
		return nil
	}
	blocks := make([]string, 0, len(a.queued))
	for _, prompt := range a.queued {
		if strings.TrimSpace(prompt.Text) == "" && len(prompt.Files) == 0 {
			continue
		}
		// Promotion reuses the inbox row's ID for the message it projects, so
		// this is the same prompt arriving as a real message. The queue and
		// the timeline are refetched by separate commands, and for the frame
		// between the two landing a promoted prompt is in both.
		if a.inTimeline(prompt.ID) {
			continue
		}
		blocks = append(blocks, a.userBlockOf(
			client.UserData{Text: prompt.Text, Files: prompt.Files}, prompt.TimeCreated, true))
	}
	return blocks
}

func (a *App) inTimeline(messageID string) bool {
	for i := range a.timeline {
		if a.timeline[i].ID == messageID {
			return true
		}
	}
	return false
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
// its own line 0) — see reasoningHeaderRef. The third does the same for a
// collapsed tool-output's clickable summary line — see toolOutputHeaderRef.
func (a *App) renderAssistant(message client.Message, data client.AssistantData, isLast bool) (string, []reasoningHeaderRef, []toolOutputHeaderRef) {
	var blocks []string
	var refs []reasoningHeaderRef
	var toolRefs []toolOutputHeaderRef
	lineOffset := 0
	appendBlock := func(block string) {
		blocks = append(blocks, block)
		lineOffset += strings.Count(block, "\n") + 1
	}
	running := a.busy && data.Finish == ""
	for _, part := range data.Content {
		switch part.Type {
		case "reasoning":
			// A part still being streamed is rendered from the live buffer
			// instead, as a block below this message (see buildTimeline).
			// The projection writes every delta into the stored message too,
			// so without this the same thinking would show twice from the
			// first refetch that lands mid-part.
			if _, live := a.streamingReasoning[part.ID]; live {
				continue
			}
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
			block, toolRef := a.toolRow(message, part.ID, part.Name, part.State)
			if block != "" {
				if toolRef != nil {
					toolRefs = append(toolRefs, toolOutputHeaderRef{
						id:        toolRef.id,
						lineStart: lineOffset + toolRef.lineStart,
						lineEnd:   lineOffset + toolRef.lineEnd,
					})
				}
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
		appendBlock(errBlock.Render(renderLines(a.styles().Muted, data.Error.Message)))
	}

	final := data.Finish != "" && data.Finish != "tool-calls" && data.Finish != "unknown"
	if isLast || final || messageAborted(data) {
		appendBlock("")
		appendBlock(a.settlementLine(message, data))
	}
	return strings.Join(blocks, "\n"), refs, toolRefs
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

// toolOutputHeaderRef marks the lines (relative to the start of the block
// bashBlock returns, inclusive) that toggle a tool call's output — the
// bash-output equivalent of reasoningHeaderRef. While collapsed this is a
// single row, the one-line summary; while expanded it spans the whole
// block, since there's no single header to re-click once the output has
// grown to fill it — clicking anywhere on an open block collapses it again.
// Threaded back up through renderAssistant/renderMessage/buildTimeline the
// same way reasoningHeaderRef is, so handleClick (mouse.go) can hit-test it
// against what's actually on screen.
type toolOutputHeaderRef struct {
	id                 string
	lineStart, lineEnd int
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
//   - running: a warning-colored spinner, "Thinking[: title] · ~tokens"; the
//     body follows in show mode or once this part is expanded. While a part
//     is still streaming this is called with the aggregator's live buffer
//     rather than the stored text — see streamingReasoningBlock.
//   - done, thinkingMode "show" (or this id individually expanded): a
//     "Thought[: title · duration · ~tokens]" header, faded to
//     thinkingOpacity once the body is showing, followed by the muted
//     markdown body.
//   - done, thinkingMode "hide" and not expanded (the default): the same
//     header collapsed to one line, prefixed "+ " (toggleable, closed) —
//     full warning brightness so the one-line summary still stands out.
//
// The token estimate is this port's own addition, not upstream's: collapsed
// is the default, so the header is all most blocks ever show, and the size of
// the hidden body is the one thing it could not say. See
// estimateReasoningTokens for why it is approximate.
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
		frame := spinnerPlaceholder
		label := "Thinking"
		if title != "" {
			label = "Thinking: " + title
		}
		// The count climbs with the stream. In the default hide mode the
		// header is the whole of a live block, so without it a long think
		// shows nothing but a spinner for however long it lasts.
		if tokens := estimateReasoningTokens(content); tokens > 0 {
			label += " · ~" + localeNumber(tokens) + " tokens"
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
		if tokens := estimateReasoningTokens(content); tokens > 0 {
			detail = append(detail, "~"+localeNumber(tokens)+" tokens")
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

// estimateReasoningTokens approximates how much thinking a finished reasoning
// part carries, so the collapsed header can say something about a body the
// reader cannot see. It is an estimate because no per-part count exists to
// report: only OpenAI's Responses API returns reasoning tokens at all
// (openairesponses.go's output_tokens_details), that figure covers a whole
// response rather than one part, and StepEnded overwrites the message's
// tokens each step — so a message with several reasoning parts could not
// attribute it. This is therefore the same coarse chars/4 heuristic
// session.estimateTokens uses for compaction budgeting, and the header prints
// it behind a "~" to keep that honest.
func estimateReasoningTokens(text string) int {
	return (len(text) + 3) / 4
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

// streamRenderFloor is the shortest gap between two glamour passes over the
// same live message, and streamRenderDutyCycle how much longer than the last
// pass took the next one has to wait on top of that.
//
// Together they bound what a streaming response can take from the update
// goroutine. A live message re-renders on *content* change, not on every
// frame — but content changes with every delta, which for a fast model is as
// often as frames arrive, and the pass gets more expensive the longer the
// response grows. Without a ceiling a long answer starves keystrokes: the
// editor only sees a key once View returns, so a 100ms pass is 100ms of
// unresponsive prompt, over and over.
//
// Making the wait proportional to the last pass is what keeps that bounded as
// the message grows: at 1/3 duty cycle a 100ms pass buys a 300ms wait, so two
// of every three frames stay cheap however long the response gets. The text
// shown lags by at most that wait, and the spinner tick guarantees the frame
// that catches it up.
const (
	streamRenderFloor     = 60 * time.Millisecond
	streamRenderDutyCycle = 3
)

// streamedBlock is one live message's last rendered block, with everything
// needed to decide whether it can be reused.
type streamedBlock struct {
	text  string
	width int
	epoch uint64
	block string
	at    time.Time
	cost  time.Duration
}

// streamingTextBlock is assistantTextBlock for text that is still arriving:
// same output, but memoized per live message and rate-limited as described
// above.
func (a *App) streamingTextBlock(id, text string) string {
	width := a.contentWidth()
	previous, ok := a.streamRender[id]
	if ok && previous.text == text && previous.width == width && previous.epoch == a.renderEpoch {
		return previous.block
	}
	if ok && previous.width == width && previous.epoch == a.renderEpoch {
		wait := max(streamRenderFloor, previous.cost*streamRenderDutyCycle)
		if time.Since(previous.at) < wait {
			return previous.block
		}
	}
	start := time.Now()
	block := a.assistantTextBlock(text)
	a.putStreamRender(id, streamedBlock{
		text:  text,
		width: width,
		epoch: a.renderEpoch,
		block: block,
		at:    start,
		cost:  time.Since(start),
	})
	return block
}

// streamRenderCacheMax bounds the memo. A session cycles through many
// assistant messages; only the ones still streaming are ever read, and
// applySnapshot replaces the live buffers wholesale when a step settles, so
// the rest are dropped rather than left to grow. The ceiling allows for a
// handful of concurrent live parts — a running turn contributes up to two
// entries of its own (its text and its thinking), and subagents stream
// alongside it.
const streamRenderCacheMax = 16

func (a *App) putStreamRender(key string, entry streamedBlock) {
	if a.streamRender == nil {
		a.streamRender = map[string]streamedBlock{}
	}
	if len(a.streamRender) > streamRenderCacheMax {
		a.streamRender = map[string]streamedBlock{}
	}
	a.streamRender[key] = entry
}

// streamingReasoningBlock is reasoningBlock for thinking that is still
// arriving: the running header — and, in show mode or once this part has been
// clicked open, the body — rendered from the aggregator's live buffer instead
// of the fetched timeline. Memoized and rate-limited exactly like
// streamingTextBlock, for the same reason: the body is re-wrapped on every
// content change, and content changes with every delta.
//
// The spinner is substituted in after the memo lookup rather than baked into
// the stored string, the way renderMessageCached does it for a cached message
// block, so a memo entry held back by the duty cycle still animates.
func (a *App) streamingReasoningBlock(id, text string) string {
	width := a.contentWidth()
	key := "reasoning:" + id
	previous, ok := a.streamRender[key]
	if ok && previous.text == text && previous.width == width && previous.epoch == a.renderEpoch {
		return a.substituteSpinner(previous.block)
	}
	if ok && previous.width == width && previous.epoch == a.renderEpoch {
		wait := max(streamRenderFloor, previous.cost*streamRenderDutyCycle)
		if time.Since(previous.at) < wait {
			return a.substituteSpinner(previous.block)
		}
	}
	start := time.Now()
	block := a.reasoningBlock(id, true, text, nil)
	a.putStreamRender(key, streamedBlock{
		text:  text,
		width: width,
		epoch: a.renderEpoch,
		block: block,
		at:    start,
		cost:  time.Since(start),
	})
	return a.substituteSpinner(block)
}

// toolRow mirrors the InlineTool renderers: a muted icon row per tool with
// per-tool labels derived from the tool input. Pending tools render as a
// "~ " line, running tools attach the spinner, and failed tools turn error
// colored. bash/edit/todowrite switch to a bordered BlockTool-style panel
// once they have something to show beyond the one-line summary.
//
// id is the tool part's own ID (client.AssistantData's Content[].ID),
// threaded through so bashBlock can key its collapsed/expanded output state
// per tool call rather than per message — a message can hold more than one
// tool part. The second return value is non-nil only for bash, and only
// while its output is collapsed (see toolOutputHeaderRef).
func (a *App) toolRow(message client.Message, id, name string, state *toolState) (string, *toolOutputHeaderRef) {
	if state == nil {
		return a.styles().Muted.Render("   ⚙ " + name), nil
	}
	if state.Status != "pending" {
		switch name {
		case "bash":
			return a.bashBlock(id, state)
		case "read":
			if block, ref := a.readBlock(id, state); block != "" {
				return block, ref
			}
		case "write":
			if block, ref := a.writeBlock(id, state); block != "" {
				return block, ref
			}
		case "edit":
			if block := a.editDiffBlock(state); block != "" {
				return block, nil
			}
		case "todowrite":
			if block := a.todoWriteBlock(state); block != "" {
				return block, nil
			}
		}
	}
	icon, label := toolLabel(name, state.Input, a.displayPath)
	if name == "task" && state.Status != "pending" && state.Status != "running" && state.Status != "error" {
		icon = "✓" // TS: state.status === "completed" ? "✓" : "│"
	}
	width := a.contentWidth()
	switch state.Status {
	case "pending":
		return wrapToolLine(a.styles().Text, strings.Repeat(" ", 6)+"~ ", label, width), nil
	case "running":
		// TS only swaps in the live spinner glyph for bash/read/task; every
		// other tool sits static in the muted icon the whole time, so
		// running and done render identically for them.
		if name == "bash" || name == "read" || name == "task" {
			frame := spinnerPlaceholder
			return wrapToolLine(a.styles().Muted, "   "+frame+" ", label, width), nil
		}
		return wrapToolLine(a.styles().Muted, "   "+icon+" ", label, width), nil
	case "error":
		return wrapToolLine(a.styles().Error, "   "+icon+" ", label, width), nil
	default:
		return wrapToolLine(a.styles().Muted, "   "+icon+" ", label, width), nil
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

// renderLines applies style to each line of text independently rather than
// handing the whole (possibly multi-line) string to a single Style.Render
// call. lipgloss v2's Render pads every line of multi-line content out to
// the block's own longest line so they measure evenly — necessary chrome
// for width/border math — but that padding is only colored when the style
// being rendered has its own Background set (see colorWhitespace's use in
// style.go: it only reaches the padding when `bg != noColor`); a
// foreground-only style like Text or Muted leaves it as bare, uncolored
// spaces. That is invisible on its own, but every caller here immediately
// embeds the result inside a *different* style's own Background (bashBlock's
// blockToolStyle, userBlock's panel, errBlock) — and that outer fill can't
// retroactively color cells an inner Render already emitted plain, so each
// shorter line inside the block showed a stray patch of the page background
// punched through its own panel. Rendering line by line — each one on its
// own single-line Render call — never triggers that padding pass at all.
func renderLines(style lipgloss.Style, text string) string {
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		lines[i] = style.Render(line)
	}
	return strings.Join(lines, "\n")
}

// bashBlock mirrors Shell's BlockTool: the command line (spinner while
// running, "$ " once settled) followed by its output. Output beyond one line
// collapses to just that first line plus a "click to expand" hint by
// default (see toolOutputHeaderRef, mouse.go's toolOutputClickTarget) —
// mirroring reasoningBlock's collapsed/expanded toggle, keyed by id (the
// tool part's own ID) rather than truncating: once expanded, the full
// output always renders, however long, and a click anywhere on the open
// block collapses it again (there's no single header left to re-click, the
// way reasoningBlock or the collapsed summary below have one).
func (a *App) bashBlock(id string, state *toolState) (string, *toolOutputHeaderRef) {
	command, _ := state.Input["command"].(string)
	if command == "" {
		command = "Writing command..."
	}
	innerWidth := max(20, a.contentWidth()-6)
	prefix := "$ "
	if state.Status == "running" {
		prefix = spinnerPlaceholder + " "
	}
	head := renderLines(a.styles().Text, strings.Join(wrapPrefixed(prefix, command, innerWidth), "\n"))
	body := strings.TrimSpace(ansi.Strip(state.Output))
	return a.collapsibleBlock(id, head, body, innerWidth, a.wrappedBody, state)
}

// collapsibleBlock is the shared body of every BlockTool that pairs a fixed
// head line with content long enough to be worth hiding: bash's command and
// its stdout, read's path and the file it returned, write's path and the
// content it stored.
//
// Beyond one line the body collapses to just its first line plus a "click to
// expand" hint (see toolOutputHeaderRef, mouse.go's toolOutputClickTarget),
// mirroring reasoningBlock's toggle and keyed by id — the tool part's own ID,
// so two calls in one message toggle independently. Once expanded the whole
// body renders however long it is, and a click anywhere on the open block
// collapses it again: there is no single header row left to re-click, the way
// the collapsed summary has one.
//
// render draws the body: shell output wraps (wrappedBody), file contents are
// highlighted and truncated (codeBody). It is called only on the rows that
// will actually be shown, so a collapsed block tokenises one line rather than
// the whole file.
func (a *App) collapsibleBlock(id, head, body string, innerWidth int, render bodyRenderer, state *toolState) (string, *toolOutputHeaderRef) {
	lines := []string{head}
	var ref *toolOutputHeaderRef
	expandedBlock := false
	if body != "" {
		bodyLines := strings.Split(body, "\n")
		switch {
		case a.expandedToolOutput[id] && len(bodyLines) > 1:
			lines = append(lines, "", strings.Join(render(body, innerWidth), "\n"))
			expandedBlock = true
		case len(bodyLines) == 1:
			// Nothing was ever collapsed, so nothing to toggle back.
			lines = append(lines, "", strings.Join(render(body, innerWidth), "\n"))
		default:
			// The click target lands on the last row the first body line
			// actually occupies, not the row the summary starts on:
			// blockToolStyle's PaddingTop(1) row, plus the head's own
			// (possibly wrapped) rows, plus the blank separator, plus the
			// first body line's own rows.
			headRows := strings.Count(head, "\n") + 1
			hint := fmt.Sprintf(" (+%d lines — click to expand)", len(bodyLines)-1)
			// The hint shares the summary's row, so the body gets the width
			// left over. Without the reservation a full-width first line
			// pushes the hint onto a row of its own — one the click target
			// below does not cover, so clicking the visible "click to expand"
			// did nothing.
			first := render(bodyLines[0], max(innerWidth-lipgloss.Width(hint), 10))
			summary := strings.Join(first, "\n") + a.styles().Muted.Render(hint)
			lines = append(lines, "", summary)
			row := 1 + headRows + 1 + len(first) - 1
			ref = &toolOutputHeaderRef{id: id, lineStart: row, lineEnd: row}
		}
	}
	if state.Status == "error" && state.Error != "" {
		lines = append(lines, a.styles().Error.Render(state.Error))
	}
	if expandedBlock {
		// The whole rendered block, padding included (blockToolStyle's
		// PaddingTop(1) + this content + PaddingBottom(1)) — every row of it
		// is a valid click target to collapse back.
		contentRows := strings.Count(strings.Join(lines, "\n"), "\n") + 1
		ref = &toolOutputHeaderRef{id: id, lineStart: 0, lineEnd: contentRows + 1}
	}
	return a.blockToolStyle().Render(strings.Join(lines, "\n")), ref
}

// readBlock mirrors bashBlock for the read tool: the path that was read, then
// the file's contents behind the same collapse toggle, syntax-highlighted for
// the extension. The one-line row it replaces named neither the file nor a
// single line of what came back.
func (a *App) readBlock(id string, state *toolState) (string, *toolOutputHeaderRef) {
	path := filePathArg(state.Input)
	// A running read has nothing to show yet, and the one-line row it falls
	// back to carries the spinner. The path is on that row too, now that the
	// label reads the right key.
	if path == "" || state.Status == "running" {
		return "", nil
	}
	innerWidth := max(20, a.contentWidth()-6)
	title := "→ Read " + a.displayPath(path)
	if lines := countLines(state.Output); lines > 0 {
		title += fmt.Sprintf("  (%s)", plural(lines, "line"))
	}
	head := renderLines(a.styles().Muted, wrapText(title, innerWidth))
	return a.collapsibleBlock(id, head, trimBlankLines(state.Output), innerWidth, a.fileBody(path), state)
}

// writeBlock is readBlock's counterpart for write. The body is the content the
// model sent, not the tool's output — the output is a one-line "Wrote file
// successfully", while what was actually put in the file is the thing worth
// being able to look at.
func (a *App) writeBlock(id string, state *toolState) (string, *toolOutputHeaderRef) {
	path := filePathArg(state.Input)
	if path == "" || state.Status == "running" {
		return "", nil
	}
	content, _ := state.Input["content"].(string)
	innerWidth := max(20, a.contentWidth()-6)
	title := "← Write " + a.displayPath(path)
	if lines := countLines(content); lines > 0 {
		title += fmt.Sprintf("  (%s)", plural(lines, "line"))
	}
	head := renderLines(a.styles().Muted, wrapText(title, innerWidth))
	return a.collapsibleBlock(id, head, trimBlankLines(content), innerWidth, a.fileBody(path), state)
}

// trimBlankLines drops leading and trailing blank lines without touching the
// indentation of the lines that remain — strings.TrimSpace would eat the
// first line's leading whitespace, which in a file is structure, not padding.
func trimBlankLines(text string) string {
	lines := strings.Split(text, "\n")
	start, end := 0, len(lines)
	for start < end && strings.TrimSpace(lines[start]) == "" {
		start++
	}
	for end > start && strings.TrimSpace(lines[end-1]) == "" {
		end--
	}
	if start == end {
		return ""
	}
	return strings.Join(lines[start:end], "\n")
}

// countLines counts the lines of a block of text, ignoring surrounding blank
// space so a trailing newline does not read as an extra line.
func countLines(text string) int {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return 0
	}
	return strings.Count(trimmed, "\n") + 1
}

func plural(count int, noun string) string {
	if count == 1 {
		return fmt.Sprintf("%d %s", count, noun)
	}
	return fmt.Sprintf("%d %ss", count, noun)
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

	title := "← Edit"
	if path := filePathArg(state.Input); path != "" {
		title = "← Edit " + a.displayPath(path)
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

// filePathArg reads the file path out of a tool's input.
//
// The builtins declare it as "path" (internal/tool/builtins/read.go,
// write.go, edit.go); the TypeScript tools it was ported from call the same
// field "filePath", which is what an MCP server or a plugin tool written
// against the upstream schema still sends. Reading only the upstream spelling
// is what left every read/write/edit row stuck on its "Reading file..." /
// "Preparing write..." placeholder — the path was in hand the whole time,
// under the other name.
func filePathArg(input map[string]any) string {
	for _, key := range []string{"path", "filePath"} {
		if value, _ := input[key].(string); value != "" {
			return value
		}
	}
	return ""
}

// toolLabel maps a tool name plus its input to the InlineTool icon and
// label, mirroring the per-tool renderers (Shell, Read, Edit, …).
//
// display shortens a file path for the terminal (App.displayPath); nil shows
// paths exactly as the tool reported them.
func toolLabel(name string, input map[string]any, display func(string) string) (icon, label string) {
	text := func(key string) string {
		value, _ := input[key].(string)
		return value
	}
	path := func() string {
		value := filePathArg(input)
		if value == "" || display == nil {
			return value
		}
		return display(value)
	}
	switch name {
	case "bash":
		if command := text("command"); command != "" {
			return "$", command
		}
		return "$", "Writing command..."
	case "read":
		if file := path(); file != "" {
			return "→", "Read " + file
		}
		return "→", "Reading file..."
	case "edit":
		if file := path(); file != "" {
			return "←", "Edit " + file
		}
		return "←", "Preparing edit..."
	case "write":
		if file := path(); file != "" {
			return "←", "Write " + file
		}
		return "←", "Preparing write..."
	case "glob":
		if pattern := text("pattern"); pattern != "" {
			// The optional "path" narrows the search to a subtree, and which
			// subtree was searched is as much of the answer as the pattern.
			if file := path(); file != "" {
				return "✱", fmt.Sprintf("Glob %q in %s", pattern, file)
			}
			return "✱", fmt.Sprintf("Glob %q", pattern)
		}
		return "✱", "Finding files..."
	case "grep":
		if pattern := text("pattern"); pattern != "" {
			if file := path(); file != "" {
				return "✱", fmt.Sprintf("Grep %q in %s", pattern, file)
			}
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

// wrapPrefixed wraps label to fit width once prefix is accounted for,
// indenting any wrapped continuation lines to line up under the label (past
// the icon/spinner prefix) rather than back at column 0 — and, more to the
// point, rather than letting a long command or path run off the right edge
// of the terminal unwrapped.
func wrapPrefixed(prefix, label string, width int) []string {
	pad := strings.Repeat(" ", lipgloss.Width(prefix))
	wrapped := wrapText(label, max(width-lipgloss.Width(prefix), 1))
	lines := strings.Split(wrapped, "\n")
	for i, line := range lines {
		if i == 0 {
			lines[i] = prefix + line
		} else {
			lines[i] = pad + line
		}
	}
	return lines
}

// wrapToolLine is wrapPrefixed for a row rendered on its own (not folded
// into a further Background fill), so a single Style.Render call over the
// joined lines is safe — see renderLines' doc comment for why that stops
// being true once a Background enters the picture.
func wrapToolLine(style lipgloss.Style, prefix, label string, width int) string {
	return style.Render(strings.Join(wrapPrefixed(prefix, label, width), "\n"))
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
			// A single token (a long path, regex, or echo'd run of filler
			// characters) can be wider than the whole line. No space will
			// ever arrive to break at, so chunk it in place — otherwise it
			// lands on its own line and runs off the right edge of the
			// terminal.
			for lipgloss.Width(word) > width {
				head, tail := chunkToWidth(word, width)
				if line != "" {
					out = append(out, line)
					line = ""
				}
				out = append(out, head)
				word = tail
			}
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

// chunkToWidth splits value into a head of exactly width display columns and
// the remainder. It is rune- and ANSI-aware via lipgloss.Width, so a break
// never lands inside a wide rune or an escape sequence.
func chunkToWidth(value string, width int) (head, tail string) {
	if lipgloss.Width(value) <= width {
		return value, ""
	}
	i := 0
	// Walk runes until adding another would pass width. lipgloss.Width on
	// each prefix is quadratic in the worst case, but the input here is one
	// oversized token from a tool call — a few hundred cells, never the
	// whole timeline.
	for i < len(value) && lipgloss.Width(value[:i+1]) <= width {
		i++
	}
	for i > 0 && !utf8.RuneStart(value[i]) {
		i--
	}
	return value[:i], value[i:]
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
