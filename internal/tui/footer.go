package tui

// Footer surfaces, ported from the TypeScript TUI.
//
// The original spreads "the footer" across four separate components, and this
// file ports the three that belong to the session view:
//
//   - `component/prompt/index.tsx`'s hint row — the row glued under the prompt
//     box. It is the session's real footer: left is either the busy spinner
//     with the two-press interrupt hint or the session directory; right is the
//     usage meter (or the `tab agents` hint when there is no usage yet) plus
//     the `ctrl+p commands` hint. Ported as `chatFooter`.
//   - `routes/session/subagent-footer.tsx` — the bordered strip shown above the
//     prompt while a subagent session is open: agent label, position among its
//     siblings, usage, and Parent/Prev/Next navigation. Ported as
//     `subagentFooter`.
//   - `cli/cmd/run/footer.width.ts` — the *shared* responsive width policy
//     (its own comment calls it that). opentui reflows the hint row with
//     flexbox; lipgloss has no flex, so this port uses the shared policy's
//     breakpoints to decide which segments survive a narrow terminal. Ported
//     as `footerWidthPolicy`.
//
// The home and sidebar footers (`feature-plugins/{home,sidebar}/footer.tsx`)
// live in views.go as `statusBar` and `sidebarView`'s bottom rows.
//
// Not ported, and why: `routes/session/footer.tsx` is dead upstream (nothing
// imports it, and no slot renders it), so its permission/LSP/MCP counters are
// not a live surface to match — the MCP counter it shares with the home footer
// is already in `statusBar`. Shell mode (`!` at an empty prompt, which turns
// the hint row's right half into `esc exit shell mode`) needs a shell prompt
// delivery the Go server does not implement.

import (
	"fmt"
	"image/color"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/langazov/gocode-go/internal/tui/client"
)

// Breakpoints from cli/cmd/run/footer.width.ts. Kept as named constants in
// the same order so a change upstream is a one-line diff here.
const (
	footerBreakpointCompact     = 80
	footerBreakpointCommandHint = 66
	footerBreakpointModel       = 120
	footerBreakpointSpacious    = 150
)

// footerPolicy is footerWidthPolicy's return value. `dialog.narrow` is carried
// for completeness (nothing reads it yet) so the port stays a straight mirror
// of the shared policy rather than a subset that drifts.
type footerPolicy struct {
	DialogNarrow bool
	// ShowActivityMeta gates the usage segment.
	ShowActivityMeta bool
	// ShowCommandHint gates the trailing `ctrl+p commands` hint.
	ShowCommandHint bool
	// ShowContextHints gates the optional middle hints.
	ShowContextHints bool
	// ContextHintLimit caps how many context hints render.
	// -1 is TS's `undefined` (unlimited).
	ContextHintLimit int
	// ShowModel gates the model name segment.
	ShowModel bool
}

// footerWidthPolicy ports footerWidthPolicy from cli/cmd/run/footer.width.ts.
func footerWidthPolicy(width int) footerPolicy {
	compact := width >= footerBreakpointCompact
	model := width >= footerBreakpointModel
	spacious := width >= footerBreakpointSpacious

	limit := 1
	switch {
	case !compact:
		limit = 0
	case spacious:
		limit = -1
	case model:
		limit = 2
	}

	return footerPolicy{
		DialogNarrow:     !compact,
		ShowActivityMeta: compact,
		ShowCommandHint:  width >= footerBreakpointCommandHint,
		ShowContextHints: compact,
		ContextHintLimit: limit,
		ShowModel:        model,
	}
}

// localeNumber ports util/locale.ts's number(): 1.5K / 2.3M, plain below a
// thousand. TS's toFixed rounds half away from zero and Go's %.1f rounds half
// to even, which can differ in the last digit for an exact .x5 — immaterial
// for a token counter, and not worth reimplementing the rounding for.
func localeNumber(n int) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	case n >= 1000:
		return fmt.Sprintf("%.1fK", float64(n)/1000)
	default:
		return fmt.Sprintf("%d", n)
	}
}

// formatMoney matches the original's Intl.NumberFormat("en-US", currency USD).
func formatMoney(value float64) string {
	return fmt.Sprintf("$%.2f", value)
}

// footerUsage is prompt/index.tsx's usage() memo: the token/percentage half
// and the cost half, each empty when unavailable. The caller joins them with
// " · " exactly like `[item().context, item().cost].filter(Boolean).join(" · ")`.
type footerUsage struct {
	Context string
	Cost    string
}

func (u footerUsage) empty() bool { return u.Context == "" && u.Cost == "" }

func (u footerUsage) String() string {
	parts := make([]string, 0, 2)
	if u.Context != "" {
		parts = append(parts, u.Context)
	}
	if u.Cost != "" {
		parts = append(parts, u.Cost)
	}
	return strings.Join(parts, " · ")
}

// buildUsage ports usage()'s body: find the last assistant message that
// actually produced output tokens, total every bucket, and turn that into
// "159.6K (16%)" using the model's own context limit — with the percentage
// dropped entirely when the catalog has no limit for that model, as upstream
// does. cost comes from the session, not the message, so a session's running
// total keeps showing after a zero-cost turn.
func buildUsage(tokens *client.AssistantTokens, contextLimit int, cost float64) footerUsage {
	out := footerUsage{}
	if cost > 0 {
		out.Cost = formatMoney(cost)
	}
	total := tokens.Total()
	if total <= 0 {
		return out
	}
	out.Context = localeNumber(total)
	if contextLimit > 0 {
		pct := int(math.Round(float64(total) / float64(contextLimit) * 100))
		out.Context = fmt.Sprintf("%s (%d%%)", out.Context, pct)
	}
	return out
}

// sessionUsage resolves buildUsage's inputs from the loaded timeline: the last
// assistant message with output tokens (findLast over `role === "assistant" &&
// tokens.output > 0`) and the running session cost.
func (a *App) sessionUsage() footerUsage {
	for i := len(a.timeline) - 1; i >= 0; i-- {
		if a.timeline[i].Type != "assistant" {
			continue
		}
		data, err := client.DecodeAssistant(a.timeline[i].Data)
		if err != nil || data.Tokens == nil || data.Tokens.Output <= 0 {
			continue
		}
		return buildUsage(data.Tokens, a.contextLimitFor(data.Model.ProviderID, data.Model.ID), a.sessionCost())
	}
	return footerUsage{}
}

// sessionCost is the `session?.cost` half of usage(). The Go session record
// carries no cost field; the stats endpoint is the same accumulation, so the
// already-loaded stats snapshot stands in for it.
func (a *App) sessionCost() float64 {
	if a.stats == nil {
		return 0
	}
	return a.stats.Cost
}

// contextLimitFor is the port's `provider.models[modelID].limit.context`
// lookup, fed by the catalog the app loads at startup. Zero means unknown,
// which buildUsage renders as a bare token count.
func (a *App) contextLimitFor(providerID, modelID string) int {
	return a.contextLimits[providerID+"/"+modelID]
}

// chatFooter ports the prompt hint row (component/prompt/index.tsx, the
// `width="100%" justifyContent="space-between"` box under the prompt box's
// shadow line).
//
// Left half is a Switch: while a turn is running, the spinner plus the
// two-press interrupt hint; otherwise the session directory, indented one
// column (the original's marginLeft={1}).
//
// Right half is a gap-2 row: in normal mode the usage meter *replaces* the
// `tab agents` hint (they are two arms of one Switch — an earlier version of
// this port rendered usage in addition to both hints), followed by the
// `ctrl+p commands` hint.
func (a *App) chatFooter() string {
	width := a.chatWidth()
	// The policy is computed against the *terminal* width, matching how the
	// original reads it (`useTerminalDimensions()` in footer.view.tsx), while
	// the fit check below keeps the row inside the chat column.
	policy := footerWidthPolicy(a.width)

	left := a.footerLeft()
	right := a.footerRight(policy)

	// Drop the lowest-priority right-hand segments before letting the row
	// overflow the chat column into the sidebar. opentui shrinks these with
	// flexShrink; lipgloss cannot, so the segments come back in priority
	// order until one fits.
	for len(right) > 0 && lipgloss.Width(left)+1+footerSegmentsWidth(right) > width {
		right = right[:len(right)-1]
	}
	if len(right) == 0 {
		return truncateRunes(left, width)
	}

	rendered := strings.Join(right, "  ")
	gap := width - lipgloss.Width(left) - lipgloss.Width(rendered)
	if gap < 1 {
		left = truncateRunes(left, width-lipgloss.Width(rendered)-1)
		gap = width - lipgloss.Width(left) - lipgloss.Width(rendered)
	}
	return left + strings.Repeat(" ", gap) + rendered
}

// footerSegmentsWidth measures segments joined by the row's gap={2}.
func footerSegmentsWidth(segments []string) int {
	total := 0
	for i, segment := range segments {
		if i > 0 {
			total += 2
		}
		total += lipgloss.Width(segment)
	}
	return total
}

// footerLeft renders the hint row's left half.
func (a *App) footerLeft() string {
	if a.busy {
		// `<box marginLeft={1}>` around the spinner, then gap={1} to the
		// interrupt hint.
		armed := a.interruptIsArmed()
		key, label := a.styles().Text, a.styles().Muted
		if armed {
			// `store.interrupt > 0` recolors *both* spans to primary.
			key = a.styles().Primary
			label = a.styles().Primary
		}
		text := "interrupt"
		if armed {
			text = "again to interrupt"
		}
		// The hint row's spinner is the Knight Rider scanner, not the inline
		// braille one — see spinner.go. Its color is the prompt's own
		// highlight (upstream: the running agent's color, which is also what
		// tints the prompt box's border).
		return " " + a.scannerSpinner(a.theme.Primary, a.theme.Background) + " " +
			key.Render("esc") + " " + label.Render(text)
	}
	// The notice arm. Upstream's hint row has one of these ahead of the
	// directory (`<Match when={workspace.notice()}>`, paddingLeft 3), used
	// there for workspace progress; this port drives it with the state the
	// directory has nothing to say about — that the last turn was interrupted
	// rather than finished.
	if notice := a.footerNotice(); notice != "" {
		return "   " + a.styles().Muted.Render(notice)
	}
	// The idle arm: the session's own directory, falling back to the working
	// directory (`location()?.directory ?? paths.cwd`).
	directory := a.homeDirectory()
	if a.active != nil && a.active.Directory != "" {
		directory = abbreviateHome(a.active.Directory, a.homeDir)
	}
	return " " + a.styles().Muted.Render(directory)
}

// footerNotice is the hint row's notice text, or "" for the directory arm.
//
// It reports an interrupted turn and, unlike the run footer's timed notices
// (`setNotice`/NOTICE_DURATION in cli/cmd/run/footer.ts), it is derived rather
// than scheduled: it stands until the next turn starts, and needs no timer to
// clear. A cancellation is worth more than three seconds of the footer, and
// the marker is the same muted "interrupted" the settlement line uses.
func (a *App) footerNotice() string {
	if a.busy {
		return ""
	}
	for i := len(a.timeline) - 1; i >= 0; i-- {
		if a.timeline[i].Type != "assistant" {
			continue
		}
		data, err := client.DecodeAssistant(a.timeline[i].Data)
		if err != nil {
			continue
		}
		if messageAborted(data) {
			return "interrupted"
		}
		return ""
	}
	return ""
}

// footerRight renders the hint row's right half, gated by the shared width
// policy (see this file's header comment for why the policy is applied here).
func (a *App) footerRight(policy footerPolicy) []string {
	segments := make([]string, 0, 2)
	usage := a.sessionUsage()
	switch {
	case !usage.empty() && policy.ShowActivityMeta:
		segments = append(segments, a.styles().Muted.Render(usage.String()))
	default:
		segments = append(segments, a.styles().Text.Render("tab")+" "+a.styles().Muted.Render("agents"))
	}
	if policy.ShowCommandHint {
		segments = append(segments, a.styles().Text.Render("ctrl+p")+" "+a.styles().Muted.Render("commands"))
	}
	return segments
}

// subagentLabelPattern is subagent-footer.tsx's `/@(\w+) subagent/` title
// probe: subagent sessions are titled "@explore subagent …" by the task tool,
// and the footer shows the titlecased agent name from it.
var subagentLabelPattern = regexp.MustCompile(`@(\w+) subagent`)

// subagentLabel ports subagentInfo()'s label half.
func subagentLabel(title string) string {
	if match := subagentLabelPattern.FindStringSubmatch(title); match != nil {
		return titlecase(match[1])
	}
	return "Subagent"
}

// subagentPosition ports subagentInfo()'s index/total: the session's 1-based
// place among its siblings ordered by creation time. Zero total means the
// position is not rendered at all.
func subagentPosition(siblings []client.Session, sessionID string) (index, total int) {
	ordered := append([]client.Session(nil), siblings...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].TimeCreated < ordered[j].TimeCreated })
	for i, session := range ordered {
		if session.ID == sessionID {
			return i + 1, len(ordered)
		}
	}
	return 0, len(ordered)
}

// subagentFooter ports routes/session/subagent-footer.tsx: a backgroundPanel
// strip with a left split border, shown above the prompt whenever the open
// session has a parent. Left is the agent label, its position among siblings,
// and the usage meter; right is the Parent/Prev/Next navigation with the
// keybinds that drive it (session_parent / session_child_cycle_reverse /
// session_child_cycle default to up / left / right in config/keybind.ts).
//
// The hover highlight on each button is not reproduced: lipgloss renders one
// frame at a time with no hover state, the same limitation noted on the dialog
// backdrop in dialogs.go.
func (a *App) subagentFooter() string {
	if a.active == nil || a.active.ParentID == "" {
		return ""
	}

	// Rendered on the panel background so the strip tints uniformly — every
	// styled segment resets the enclosing span (see modelMeta).
	label := a.onPanel(a.theme.Text, true).Render(subagentLabel(a.active.Title))

	parts := []string{label}
	if index, total := subagentPosition(a.subagentSiblings, a.active.ID); total > 0 {
		parts = append(parts, a.onPanel(a.theme.TextMuted, false).Render(fmt.Sprintf("(%d of %d)", index, total)))
	}
	if usage := a.sessionUsage(); !usage.empty() {
		parts = append(parts, a.onPanel(a.theme.TextMuted, false).Render(usage.String()))
	}
	left := strings.Join(parts, " ")

	button := func(title, key string) string {
		return a.onPanel(a.theme.Text, false).Render(title) + " " +
			a.onPanel(a.theme.TextMuted, false).Render(key)
	}
	right := strings.Join([]string{
		button("Parent", "up"),
		button("Prev", "left"),
		button("Next", "right"),
	}, "  ")

	// paddingLeft 2 / paddingRight 1, inside a box declared at the prompt
	// box's width so the two stack flush (the left border renders outside
	// that declared width).
	boxWidth := a.sessionPromptBoxWidth()
	inner := boxWidth - 3
	gap := inner - lipgloss.Width(left) - lipgloss.Width(right)
	row := left
	if gap >= 1 {
		row += strings.Repeat(" ", gap) + right
	} else {
		// Too narrow for one row: stack the navigation under the label
		// instead of overflowing the column.
		row += "\n" + right
	}

	return lipgloss.NewStyle().
		Border(splitBorder(), false, false, false, true).
		BorderForeground(a.theme.Border).
		BorderBackground(a.theme.BackgroundPanel).
		Background(a.theme.BackgroundPanel).
		PaddingTop(1).
		PaddingBottom(1).
		PaddingLeft(2).
		PaddingRight(1).
		Width(borderBoxWidth(boxWidth)).
		Render(row)
}

// interruptArmWindow is the follow-up window on the two-press interrupt
// gesture, matching prompt/index.tsx's `setTimeout(..., 5000)`.
const interruptArmWindow = 5 * time.Second

// armInterrupt ports the session.interrupt command's body. The first press
// only arms the gesture (the footer's hint row switches to "again to
// interrupt"); a second press inside interruptArmWindow actually aborts the
// turn. Upstream increments unconditionally and fires at `>= 2`, which is what
// makes the *second* press the one that aborts.
func (a *App) armInterrupt() tea.Cmd {
	if a.interruptIsArmed() {
		a.interruptArmed = 0
		c, sessionID := a.client, a.active.ID
		return func() tea.Msg {
			if err := c.Interrupt(a.ctx, sessionID); err != nil {
				return statusMsg{text: "interrupt failed: " + err.Error()}
			}
			return nil
		}
	}
	a.interruptArmed = 1
	a.interruptExpires = time.Now().Add(interruptArmWindow)
	return nil
}

// interruptIsArmed reports whether a follow-up press would abort the turn.
// The window expires lazily rather than through a scheduled tick: the hint
// row only shows while a turn is running, and the spinner already repaints it
// every frame, so a timer would buy nothing and would keep the program awake
// for five seconds after every stray escape.
func (a *App) interruptIsArmed() bool {
	return a.interruptArmed > 0 && time.Now().Before(a.interruptExpires)
}

// loadSubagentSiblings fetches the open session's siblings (its parent's
// children) so the subagent footer can render its position. It is a no-op for
// a root session, which never shows that footer.
func (a *App) loadSubagentSiblings() tea.Cmd {
	if a.active == nil || a.active.ParentID == "" {
		return nil
	}
	c, parentID := a.client, a.active.ParentID
	return func() tea.Msg {
		siblings, err := c.Children(a.ctx, parentID)
		if err != nil {
			return nil
		}
		return subagentSiblingsMsg{parentID: parentID, siblings: siblings}
	}
}

// openParentSession ports the session.parent command behind the footer's
// "Parent" button. handled is false when there is no parent to go to, so the
// caller can fall through to whatever else the key does.
func (a *App) openParentSession() (tea.Cmd, bool) {
	if a.active == nil || a.active.ParentID == "" {
		return nil, false
	}
	c, parentID := a.client, a.active.ParentID
	return func() tea.Msg {
		parent, err := c.Session(a.ctx, parentID)
		if err != nil {
			return statusMsg{text: "failed to open parent: " + err.Error()}
		}
		return sessionOpenedMsg{session: parent}
	}, true
}

// cycleSubagentSibling ports session.child.next / session.child.previous (the
// footer's Next/Prev buttons): move to the adjacent sibling in creation order,
// wrapping at both ends like subagent-footer.tsx's cycleTab.
func (a *App) cycleSubagentSibling(direction int) (tea.Cmd, bool) {
	if a.active == nil || a.active.ParentID == "" || len(a.subagentSiblings) < 2 {
		return nil, false
	}
	ordered := append([]client.Session(nil), a.subagentSiblings...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].TimeCreated < ordered[j].TimeCreated })
	current := -1
	for i, session := range ordered {
		if session.ID == a.active.ID {
			current = i
			break
		}
	}
	if current == -1 {
		return nil, false
	}
	next := ordered[((current+direction)%len(ordered)+len(ordered))%len(ordered)]
	return staticMsg(sessionOpenedMsg{session: &next}), true
}

// --- sidebar footer (feature-plugins/sidebar/footer.tsx) --------------------

// paidProviderAvailable ports the sidebar footer plugin's has() memo:
//
//	provider.some(item => item.id !== "opencode" ||
//	  Object.values(item.models).some(model => model.cost?.input !== 0))
//
// i.e. anything beyond the bundled free "opencode" provider counts, and so
// does a priced model *inside* it. False means the user has only free models
// and has not connected a provider yet, which is who the card is for.
func (a *App) paidProviderAvailable() bool {
	for _, provider := range a.providers {
		if provider.ID != freeProviderID {
			return true
		}
	}
	for key, cost := range a.modelCosts {
		if strings.HasPrefix(key, freeProviderID+"/") && cost != 0 {
			return true
		}
	}
	return false
}

// freeProviderID is the bundled provider whose models are free, the one the
// getting-started card discounts when deciding whether to greet.
const freeProviderID = "opencode"

// gettingStartedCard ports the sidebar footer plugin's dismissible card. It
// renders above the path/version lines whenever no priced provider is
// reachable and the card has not been dismissed.
//
// Upstream persists the dismissal in its KV store (`dismissed_getting_started`);
// this port keeps it in memory for the process lifetime, the same choice
// already made for thinkingMode and timestamps (see App's fields).
func (a *App) gettingStartedCard(width int) []string {
	if a.paidProviderAvailable() || a.dismissedGettingStarted {
		return nil
	}
	bg := a.theme.BackgroundElement
	on := func(fg color.Color, bold bool) lipgloss.Style {
		return lipgloss.NewStyle().Foreground(fg).Background(bg).Bold(bold)
	}
	// The card fills the sidebar's content column (the panel's own
	// paddingLeft/Right 2 are already taken out of width), and inside that it
	// has its own paddingLeft/Right 2 plus the "⬖" marker column and the
	// gap={1} beside it.
	card := width - 4
	body := card - 4 - 2
	if body < 12 {
		return nil
	}

	// Both of these rows are a justifyContent="space-between" pair.
	spread := func(left, right string) string {
		gap := body - lipgloss.Width(left) - lipgloss.Width(right)
		if gap < 1 {
			gap = 1
		}
		return left + on(bg, false).Render(strings.Repeat(" ", gap)) + right
	}

	lines := []string{
		spread(on(a.theme.Text, true).Render("Getting started"), on(a.theme.TextMuted, false).Render("✕")),
	}
	lines = append(lines, wrapOnBackground(a, "GoCode includes free models so you can start immediately.", body)...)
	lines = append(lines, wrapOnBackground(a,
		"Connect from 75+ providers to use other models, including Claude, GPT, Gemini etc", body)...)
	lines = append(lines, spread(
		on(a.theme.Text, false).Render("Connect provider"),
		on(a.theme.TextMuted, false).Render("/connect")))

	blank := on(bg, false).Render(strings.Repeat(" ", card))
	out := make([]string, 0, len(lines)+3)
	out = append(out, blank)
	for i, line := range lines {
		marker := " "
		if i == 0 {
			marker = "⬖"
		}
		filled := line + on(bg, false).Render(strings.Repeat(" ", max(0, body-lipgloss.Width(line))))
		out = append(out,
			on(bg, false).Render("  ")+on(a.theme.Text, false).Render(marker)+
				on(bg, false).Render(" ")+filled+on(bg, false).Render("  "))
	}
	// The trailing blank is the card's paddingBottom; the empty string after
	// it is the gap={1} between the card and the path line.
	return append(out, blank, "")
}

// wrapOnBackground word-wraps text to width, rendering every line on the
// card's background so the block tints uniformly (each styled segment resets
// the enclosing span — same reason modelMeta paints every segment).
func wrapOnBackground(a *App, text string, width int) []string {
	style := lipgloss.NewStyle().Foreground(a.theme.TextMuted).Background(a.theme.BackgroundElement)
	if width < 1 {
		width = 1
	}
	out := []string{}
	line := ""
	for _, word := range strings.Fields(text) {
		// A token wider than the strip (long command, URL) gets chunked in
		// place; no later word will ever make room for it.
		for lipgloss.Width(word) > width {
			head, tail := chunkToWidth(word, width)
			if line != "" {
				out = append(out, style.Render(line+strings.Repeat(" ", max(0, width-lipgloss.Width(line)))))
				line = ""
			}
			out = append(out, style.Render(head))
			word = tail
		}
		candidate := word
		if line != "" {
			candidate = line + " " + word
		}
		if lipgloss.Width(candidate) > width && line != "" {
			out = append(out, style.Render(line+strings.Repeat(" ", max(0, width-lipgloss.Width(line)))))
			line = word
			continue
		}
		line = candidate
	}
	if line != "" {
		out = append(out, style.Render(line+strings.Repeat(" ", max(0, width-lipgloss.Width(line)))))
	}
	return out
}

// --- sidebar context (feature-plugins/sidebar/context.tsx) ------------------

// sidebarContextState is what the sidebar's Context section reports: the last
// assistant turn's own context size and its share of that model's window.
type sidebarContextState struct {
	tokens  int
	percent int
}

// sidebarContext ports context.tsx's state() memo. It deliberately shares
// nothing but its inputs with the footer's usage meter: the same findLast over
// assistant messages with output tokens and the same five-bucket sum, but the
// sidebar renders the raw count (toLocaleString, comma-grouped) and the
// percentage as separate lines rather than one "159.6K (16%)" string, so the
// formatting cannot be shared.
func (a *App) sidebarContext() sidebarContextState {
	for i := len(a.timeline) - 1; i >= 0; i-- {
		if a.timeline[i].Type != "assistant" {
			continue
		}
		data, err := client.DecodeAssistant(a.timeline[i].Data)
		if err != nil || data.Tokens == nil || data.Tokens.Output <= 0 {
			continue
		}
		out := sidebarContextState{tokens: data.Tokens.Total()}
		// `model?.limit.context ? … : null` — an unknown limit renders 0%,
		// not a percentage of a guessed window.
		if limit := a.contextLimitFor(data.Model.ProviderID, data.Model.ID); limit > 0 {
			out.percent = int(math.Round(float64(out.tokens) / float64(limit) * 100))
		}
		return out
	}
	return sidebarContextState{}
}

// groupDigits is Number.prototype.toLocaleString for the en-US grouping the
// sidebar uses ("159,600"). The footer's meter uses Locale.number's compact
// form ("159.6K") instead — the two surfaces genuinely differ upstream.
func groupDigits(value int) string {
	digits := strconv.Itoa(value)
	sign := ""
	if strings.HasPrefix(digits, "-") {
		sign, digits = "-", digits[1:]
	}
	var out strings.Builder
	for i, r := range digits {
		if i > 0 && (len(digits)-i)%3 == 0 {
			out.WriteByte(',')
		}
		out.WriteRune(r)
	}
	return sign + out.String()
}
