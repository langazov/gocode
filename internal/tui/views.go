package tui

import (
	"fmt"
	"image/color"
	pathpkg "path"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/langazov/gocode-go/internal/tui/client"
	"github.com/langazov/gocode-go/internal/tui/theme"
)

// frame applies the screen's 1-column side margin and crops to the terminal
// height. It used to be `lipgloss.NewStyle().Padding(0,1).MaxHeight(h)`, which
// is the same thing but pays to measure the display width of every line in a
// ~90KB fully-styled frame — a third of the render budget, and grapheme
// segmentation is the single most expensive thing in the profile. Nothing
// downstream needs the uniform right edge that padding produced: the
// compositors (spliceAt, compositeSidebarOverlay) pad to a.width themselves,
// and no background is set here for a ragged edge to expose.
func (a *App) frame(content string) string {
	lines := strings.Split(content, "\n")
	if a.height > 0 && len(lines) > a.height {
		lines = lines[:a.height]
	}
	for i, line := range lines {
		lines[i] = " " + line + " "
	}
	return strings.Join(lines, "\n")
}

// sidebarWidth returns the columns reserved for the sidebar (42 when visible
// and a session is open — home never shows it, matching the Sidebar width).
// TS reserves this width whenever the sidebar is visible at all, docked or
// not — see wide()/viewChat for the docked-vs-overlay render split.
func (a *App) sidebarWidth() int {
	if a.sidebar && a.active != nil {
		return 42
	}
	return 0
}

// wide mirrors Session's `wide = width > 120`: above it the sidebar docks as
// a column; at or below it, the sidebar overlays the chat as a drawer with a
// dimmed backdrop instead of squeezing the chat column further.
func (a *App) wide() bool {
	return a.width > 120
}

// chatWidth is the width available to the main chat column.
func (a *App) chatWidth() int {
	width := a.width - a.sidebarWidth() - 4
	if width < 20 {
		width = 20
	}
	return width
}

func (a *App) viewportHeight() int {
	// promptContentHeight, not input.Height(): the editor is transiently
	// inflated to its maximum while a key is handled (see
	// expandPromptForInput), and the timeline's budget must reflect the height
	// the prompt will actually render at, not that intermediate value. Reading
	// the live height here made pageup scroll a short page.
	h := a.height - a.promptContentHeight() - 6
	// A permission banner occupies rows the fixed budget above does not
	// account for. Without this the column overflows and frame()'s MaxHeight
	// crops from the bottom — taking the banner's own buttons with it, which
	// is the one part of it the user has to reach.
	if banner := a.permissionBannerHeight(); banner > 0 {
		// The banner replaces the single blank row its slot always occupied.
		h -= banner - 1
	}
	if h < 3 {
		h = 3
	}
	return h
}

// permissionBannerHeight is the rendered height of the permission banner, or
// zero when none is showing.
func (a *App) permissionBannerHeight() int {
	banner := a.permissionBanner()
	if banner == "" {
		return 0
	}
	return strings.Count(banner, "\n") + 1
}

// permissionMaxHeight caps the collapsed permission prompt, porting
// `maxHeight: 15` on the non-expanded branch of permission.tsx's Prompt.
//
// The cap is what keeps the buttons reachable: in the original the body sits
// in a flexGrow box while the option bar is flexShrink={0}, so a long body is
// squeezed and the bar always survives. This port has no flexbox, so the body
// is truncated to the same effect.
const permissionMaxHeight = 15

// permissionBudget is how many rows the banner may occupy: the maxHeight cap,
// or less when the terminal cannot spare that much.
//
// maxHeight is a maximum, not a fixed height — in the original the flex
// container still shrinks below it when the column is short, which is what
// keeps the option bar on screen in a small terminal. Capping at a flat 15
// reintroduced the bug at 14 rows.
func (a *App) permissionBudget() int {
	budget := permissionMaxHeight
	// The prompt box and its hint row still have to fit beneath.
	if available := a.height - a.input.Height() - 5; available < budget {
		budget = available
	}
	return budget
}

// contentWidth is the message column width. TS's Session route computes one
// `contentWidth = dimensions().width - (sidebar?42:0) - 4` and uses it
// directly for message wrapping — the same formula chatWidth() already
// implements, so this used to (wrongly) subtract another 4 on top of that,
// making every message render ~4 columns narrower than the original and
// than this port's own prompt box (which is sized off chatWidth()).
func (a *App) contentWidth() int {
	w := a.chatWidth()
	if w < 20 {
		w = 20
	}
	return w
}

// viewChat mirrors the Session route: a scrollable message timeline sticky to
// the bottom inside a column padded 2/2/1 (paddingLeft, paddingRight,
// paddingBottom, gap 1), the permission prompt above the editor, then the
// editor and hint row, with the sidebar filling the remaining height.
// viewChat renders the session view. It also caches, on a, the exact
// scroll/pad layout it computes here (chatWindowStart, chatWindowPad,
// chatReasoningRows): handleClick (mouse.go) needs to map an absolute
// screen row from the *next* mouse event back to a timeline line — same
// coordinate space overlayMouseTarget already relies on for dialogs, valid
// because Bubble Tea always finishes a View() before the next Update() sees
// input, so nothing here changes between this render and that click.
func (a *App) viewChat() string {
	lines, reasoningRows := a.buildTimeline()
	start := 0

	viewportHeight := a.viewportHeight()
	total := len(lines)
	if total > viewportHeight {
		// A fixed-size window that shifts with scrollOffset, not one that
		// grows with it — otherwise more scrolling means more total lines,
		// which pushes the pinned prompt/input block past frame()'s
		// MaxHeight crop instead of keeping it anchored to the bottom.
		maxOffset := total - viewportHeight
		if a.scrollOffset > maxOffset {
			a.scrollOffset = maxOffset
		}
		// The "N more lines" indicator adds a row once scrolled, so it must
		// come out of the same viewportHeight budget rather than exceeding
		// it — otherwise the extra row pushes the prompt/footer below
		// frame()'s MaxHeight crop and the footer gets clipped off.
		keep := viewportHeight
		if a.scrollOffset > 0 {
			keep--
		}
		end := total - a.scrollOffset
		start = end - keep
		if start < 0 {
			start = 0
		}
		lines = lines[start:end]
		if a.scrollOffset > 0 {
			lines = append(lines, a.styles().Muted.Render(fmt.Sprintf("  ↑ %d more lines (pagedown to return)", a.scrollOffset)))
		}
	} else {
		a.scrollOffset = 0
	}
	a.chatReasoningRows = reasoningRows
	a.chatWindowStart = start

	// The chat column is inset by one more cell than the frame provides
	// (paddingLeft 2), and a blank line separates the timeline from the
	// editor block (gap 1).
	chat := make([]string, 0, len(lines)+8)
	for _, line := range lines {
		chat = append(chat, " "+line)
	}
	// Order matches session/index.tsx's bottom box: the permission prompt,
	// then the subagent footer for a child session, then the prompt with its
	// hint row. The permission banner keeps its unconditional slot (an empty
	// one is the blank separator row this column has always had); the
	// subagent footer is appended only when it renders, so a root session's
	// row budget — and with it frame()'s MaxHeight crop of the footer — is
	// unchanged.
	chat = append(chat, "", a.indentBlock(a.permissionBanner()))
	if footer := a.subagentFooter(); footer != "" {
		chat = append(chat, a.indentBlock(footer))
	}
	// The completion popup sits directly above the prompt and shares its
	// width, porting the autocomplete's absolute position anchored to the
	// prompt box.
	if popup := a.autocompleteView(a.sessionPromptBoxWidth()); popup != "" {
		chat = append(chat, a.indentBlock(popup))
	}
	chat = append(chat,
		a.indentBlock(a.promptBox(a.sessionPromptBoxWidth())),
		a.indentBlock(a.chatFooter()))
	main := strings.Join(chat, "\n")

	// TS's timeline is a flexGrow scrollbox with stickyScroll="bottom": a
	// short conversation's messages (and the prompt/footer glued below it)
	// sit at the bottom of the column, with any unused space above them —
	// not at the top with unused space below. Padding with leading blank
	// lines here reproduces that anchor.
	pad := a.height - strings.Count(main, "\n") - 1
	if pad < 0 {
		pad = 0
	}
	a.chatWindowPad = pad
	if pad > 0 {
		main = strings.Repeat("\n", pad) + main
	}
	a.chatColumnEnd = a.width
	if sidebar := a.sidebarView(); sidebar != "" {
		if a.wide() {
			// The chat column is already sized to the chat width; no
			// per-line truncation here — cutting styled lines corrupts ANSI
			// sequences.
			joined := lipgloss.JoinHorizontal(lipgloss.Top, main, sidebar)
			// Record where the chat column stops so a drag-selection can be
			// held inside it (see selectionColumnBounds). JoinHorizontal pads
			// every line to the block width, so one line measures the whole
			// thing, and frame() insets it by a column.
			a.chatColumnEnd = 1 + lipgloss.Width(firstLine(joined)) - a.sidebarWidth()
			return a.frame(joined)
		}
		// Narrow terminal: TS overlays the sidebar as a right-aligned drawer
		// over a dimmed backdrop instead of a docked column (the chat column
		// still reserves the same 42 columns either way — see
		// sidebarWidth). The backdrop dim has no lipgloss equivalent (same
		// limitation noted on the dialog backdrop in dialogs.go), so the
		// chat content underneath stays undimmed.
		return a.frame(a.compositeSidebarOverlay(main, sidebar))
	}
	return a.frame(main)
}

// firstLine returns s up to its first newline.
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// compositeSidebarOverlay splices the sidebar panel onto the right edge of
// the full terminal width, on top of base, mirroring compositeOverlay's
// ANSI-safe splicing (dialogs.go) but right-aligned and full height instead
// of centered.
func (a *App) compositeSidebarOverlay(base, panel string) string {
	baseLines := strings.Split(base, "\n")
	for i, line := range baseLines {
		baseLines[i] = padPlain(line, a.width)
	}
	for len(baseLines) < a.height {
		baseLines = append(baseLines, strings.Repeat(" ", a.width))
	}
	panelW := lipgloss.Width(panel)
	left := a.width - panelW
	if left < 0 {
		left = 0
	}
	for i, pline := range strings.Split(panel, "\n") {
		if i >= len(baseLines) {
			break
		}
		baseLines[i] = sliceCells(baseLines[i], 0, left) + pline
	}
	return strings.Join(baseLines, "\n")
}

// indentBlock prefixes each line of a block with the chat column's extra
// padding cell.
func (a *App) indentBlock(block string) string {
	if block == "" {
		return ""
	}
	return aIndent(block, 1)
}

// promptMaxWidth mirrors the Home route's prompt cap: 75 columns, or the
// available width (terminal minus frame and home padding) when narrower.
func promptMaxWidth(width int) int {
	w := width - 6
	if w > 75 {
		w = 75
	}
	if w < 20 {
		w = 20
	}
	return w
}

// sessionPromptBoxWidth is the Width promptBox is given in the chat view.
// It's chatWidth()-2, not chatWidth()-1: promptBox's own borderBoxWidth()
// call adds the 1-char left border's column back on top of this value to
// reach a total of chatWidth()-1 — matching every other bordered timeline
// panel (userBlock, errBlock, blockToolStyle), all sized to the same total
// as assistantTextBlock's own max reach (indent(3) + renderMarkdown's
// contentWidth()-4 wrap width = contentWidth()-1 = chatWidth()-1), rather
// than widening the markdown side to fill a wider box.
func (a *App) sessionPromptBoxWidth() int {
	return a.chatWidth() - 2
}

// promptBox mirrors the Prompt component: left border in the agent color
// (with the ╹ bottom-left corner below the box), backgroundElement tint, and
// the "Agent · model provider" meta row under the input.
func (a *App) promptBox(width int) string {
	borderColor := a.theme.Primary
	if a.busy {
		borderColor = a.theme.Warning
	}
	style := lipgloss.NewStyle().
		Border(lipgloss.Border{Left: "┃"}, false, false, false, true).
		BorderForeground(borderColor).
		Background(a.theme.BackgroundElement).
		PaddingTop(1).
		PaddingLeft(2).
		PaddingRight(2).
		Width(borderBoxWidth(width))
	content := strings.TrimRight(a.input.View(), "\n")
	// The editor's viewport pads its row with plain unstyled spaces, which
	// would break the box tint; drop the tail and let Width() refill it.
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimRight(line, " ")
	}
	return style.Render(strings.Join(lines, "\n") + "\n\n" + a.modelMeta())
}

// modelMeta mirrors the prompt meta row: titlecased agent name in the agent
// color, then the model display name and provider label in muted. Every
// segment carries the box background explicitly — each styled segment resets
// the enclosing span, so the tint would otherwise drop after the first one.
//
// The agent segment and the "· model provider" segment each fade in via
// agentMetaFade/modelMetaFade (util/signal.ts's createFadeIn, ported in
// animate.go), colors blended toward the box background with
// theme.FadeColor since terminal cells have no real alpha channel.
func (a *App) modelMeta() string {
	providerID, modelID, _ := a.currentModelParts()
	bg := a.theme.BackgroundElement
	agentAlpha := a.agentMetaFade.Alpha()
	modelAlpha := a.modelMetaFade.Alpha()
	return lipgloss.JoinHorizontal(lipgloss.Top,
		lipgloss.NewStyle().Foreground(theme.FadeColor(bg, a.theme.Primary, agentAlpha)).Background(bg).
			Render(titlecase(a.activeAgentOr("build"))),
		lipgloss.NewStyle().Foreground(theme.FadeColor(bg, a.theme.TextMuted, modelAlpha)).Background(bg).
			Render(" · "),
		lipgloss.NewStyle().Foreground(theme.FadeColor(bg, a.theme.Text, modelAlpha)).Background(bg).
			Render(a.modelName(providerID, modelID)),
		lipgloss.NewStyle().Foreground(theme.FadeColor(bg, a.theme.TextMuted, modelAlpha)).Background(bg).
			Render(" "+a.providerName(providerID)),
	)
}

// homePromptBlock is the home-screen prompt: the box, the ╹ corner row with
// the backgroundElement shadow line, and the shortcut hints underneath.
func (a *App) homePromptBlock(width int) string {
	corner := lipgloss.NewStyle().Foreground(a.theme.Primary).Render("╹")
	shadow := lipgloss.NewStyle().Foreground(a.theme.BackgroundElement).Render(strings.Repeat("▀", width))
	hints := a.styles().Text.Render("tab") + " " + a.styles().Muted.Render("agents") + "  " +
		a.styles().Text.Render("ctrl+p") + " " + a.styles().Muted.Render("commands")
	blocks := []string{}
	// Above the prompt, sharing its width — the popup's anchored position.
	if popup := a.autocompleteView(width); popup != "" {
		blocks = append(blocks, popup)
	}
	blocks = append(blocks, a.promptBox(width), corner+shadow, hints)
	return strings.Join(blocks, "\n")
}

// statusBar mirrors the home footer plugin: abbreviated directory with the
// git branch, MCP count with the /status hint, and the version on the right.
// Matches feature-plugins/home/footer.tsx's Mcp component exactly: shown
// whenever any server is *configured* (has()), the count is *connected*
// servers only, and the dot is red if any server failed, green if at least
// one is connected, else muted.
func (a *App) statusBar(width int) string {
	left := a.styles().Muted.Render(a.homeDirectory())
	if len(a.mcpServers) > 0 {
		connected := mcpConnectedCount(a.mcpServers)
		dotColor := a.theme.TextMuted
		switch {
		case mcpHasFailure(a.mcpServers):
			dotColor = a.theme.Error
		case connected > 0:
			dotColor = a.theme.Success
		}
		left += "  " + lipgloss.NewStyle().Foreground(dotColor).Render("⊙ ") +
			a.styles().Text.Render(fmt.Sprintf("%d MCP", connected)) + " " +
			a.styles().Muted.Render("/status")
	}
	right := a.styles().Muted.Render(appVersion)
	gap := width - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		left = truncateRunes(left, width-lipgloss.Width(right)-1)
		gap = width - lipgloss.Width(left) - lipgloss.Width(right)
		if gap < 1 {
			gap = 1
		}
	}
	return left + strings.Repeat(" ", gap) + right
}

func formatTokens(count int) string {
	if count >= 1000 {
		return fmt.Sprintf("%.1fK", float64(count)/1000)
	}
	return fmt.Sprintf("%d", count)
}

// permissionBanner mirrors the PermissionPrompt: a warning-bordered
// backgroundPanel block with the "△ Permission required" header, the tool
// body, and an option bar with selectable buttons.
func (a *App) permissionBanner() string {
	if a.permission == nil {
		return ""
	}
	request := a.permission
	once := "Allow once"
	always := "Allow always"
	reject := "Reject"

	// Option buttons: selected is warning-filled with background text,
	// unselected sit on backgroundElement in muted text (Prompt options bar).
	button := func(label string, selected bool) string {
		bg := a.theme.BackgroundElement
		fg := a.theme.TextMuted
		if selected {
			bg = a.theme.Warning
			fg = a.theme.Background
		}
		return lipgloss.NewStyle().Foreground(fg).Background(bg).Render(" " + label + " ")
	}
	buttons := []string{
		button(once, a.permissionChoice == 0),
		button(always, a.permissionChoice == 1),
		button(reject, a.permissionChoice == 2),
	}
	barLeft := strings.Join(buttons, " ")
	barRight := a.styles().Text.Render("⇆") + " " + a.styles().Muted.Render("select") +
		"  " + a.styles().Text.Render("enter") + " " + a.styles().Muted.Render("confirm")
	// Inner width after the bar's own padding (2 left, 3 right).
	inner := a.contentWidth() - 1 - 5
	gap := inner - lipgloss.Width(barLeft) - lipgloss.Width(barRight)
	bar := barLeft
	if gap >= 1 {
		bar += strings.Repeat(" ", gap) + barRight
	} else {
		// Narrow terminal: stack the hints under the buttons like the
		// original's column layout.
		bar += "\n" + barRight
	}
	barStyle := lipgloss.NewStyle().
		Background(a.theme.BackgroundElement).
		PaddingTop(1).
		PaddingBottom(1).
		PaddingLeft(2).
		PaddingRight(3).
		Width(a.contentWidth() - 1)

	header := lipgloss.JoinHorizontal(lipgloss.Top,
		a.styles().Warning.Render("△"),
		" ", a.styles().Text.Render("Permission required"))
	icon, title := a.permissionTitle(request)
	line2 := "  " + a.styles().Muted.Render(icon) + " " + a.styles().Text.Render(title)
	body := a.permissionBody(request)

	// Cap the panel so the option bar below it stays on screen, porting
	// maxHeight: 15. The bar's own height comes out of the budget first
	// (flexShrink={0}), then the panel's padding and its two header rows,
	// which are flexShrink={0} in the original too; whatever is left is what
	// the body may occupy.
	barHeight := strings.Count(bar, "\n") + 1 + 2                                 // + paddingTop/paddingBottom
	const panelChrome = 2                                                         // paddingTop + paddingBottom
	const headerRows = 2                                                          // "△ Permission required" + the title line
	bodyBudget := a.permissionBudget() - barHeight - panelChrome - headerRows - 1 // -1 for the blank separator
	body = clampPermissionBody(body, bodyBudget)

	content := []string{header, line2}
	if body != "" {
		content = append(content, "", body)
	}

	style := lipgloss.NewStyle().
		Border(splitBorder(), false, false, false, true).
		BorderForeground(a.theme.Warning).
		Background(a.theme.BackgroundPanel).
		PaddingTop(1).
		PaddingBottom(1).
		PaddingLeft(1).
		PaddingRight(3).
		Width(borderBoxWidth(a.contentWidth() - 2))
	return style.Render(strings.Join(content, "\n")) + "\n" + barStyle.Render(bar)
}

// clampPermissionBody truncates a body to budget rows, replacing the last one
// with a count of what was dropped.
//
// The original scrolls the body instead (a <scrollbox> for diffs) and offers a
// fullscreen toggle to see it whole; neither exists here, so the count is what
// tells the reader the text continues rather than ending where it was cut.
func clampPermissionBody(body string, budget int) string {
	if body == "" {
		return ""
	}
	if budget < 1 {
		// No room for any body at all: the header and the buttons are what
		// matter, and dropping the body is better than losing them.
		return ""
	}
	lines := strings.Split(body, "\n")
	if len(lines) <= budget {
		return body
	}
	kept := lines[:budget-1]
	hidden := len(lines) - len(kept)
	kept = append(kept, fmt.Sprintf("… %d more lines", hidden))
	return strings.Join(kept, "\n")
}

// permissionTitle derives the icon and title for a permission request,
// mirroring permission.tsx's info() cases exactly — including the absence
// of a "write" case: TS has none, so write falls to the same generic
// "Call tool <action>" every unhandled action gets. (TS also special-cases
// list/websearch/doom_loop, but Go's PermissionRequest carries only a flat
// Resources list — no provider or query — so those can't be reconstructed and
// are left to the generic fallback too.)
//
// A request from a subagent is attributed to it: with several sessions asking
// concurrently, an unlabeled prompt is ambiguous about who is blocked.
func (a *App) permissionTitle(request *client.PermissionRequest) (icon, title string) {
	icon, title = a.permissionAction(request)
	if agent := request.Agent; agent != "" && agent != "build" {
		title = title + " (@" + agent + ")"
	}
	return icon, title
}

// externalDirectoryTarget recovers the directory an external_directory request
// is about. The resources are globs over it (`/srv/data/*`), which is what the
// grant is saved as; the directory is what a person can actually answer about.
func externalDirectoryTarget(resources []string) string {
	if len(resources) == 0 {
		return ""
	}
	pattern := resources[0]
	if strings.Contains(pattern, "*") {
		return pathpkg.Dir(pattern)
	}
	return pattern
}

func (a *App) permissionAction(request *client.PermissionRequest) (icon, title string) {
	path := ""
	if len(request.Resources) > 0 {
		path = request.Resources[0]
	}
	switch request.Action {
	case "external_directory":
		// Without the directory in the title this prompt is unanswerable: it
		// says something outside the project is being touched but not what,
		// and the only safe reply to an unknown is "reject".
		target := externalDirectoryTarget(request.Resources)
		if target == "" {
			return "←", "Access external directory"
		}
		return "←", "Access external directory " + abbreviateHome(target, a.homeDir)
	case "task":
		if path == "" {
			return "│", "Launch subagent"
		}
		return "│", "Launch " + path + " subagent"
	case "edit":
		return "→", "Edit " + path
	case "read":
		return "→", "Read " + path
	case "glob":
		return "✱", fmt.Sprintf("Glob %q", path)
	case "grep":
		return "✱", fmt.Sprintf("Grep %q", path)
	case "bash":
		return "#", "Shell command"
	case "webfetch":
		return "%", "WebFetch " + path
	case "websearch":
		// TS titles this with the search provider, which this port's
		// PermissionRequest does not carry (no metadata is set on the ask), so
		// the query alone stands in.
		if path == "" {
			return "◈", "Web search"
		}
		return "◈", fmt.Sprintf("Web search %q", path)
	}
	return "⚙", "Call tool " + request.Action
}

// permissionBody renders the request body, mirroring each info() case's
// body: line. The generic fallback ("Tool: <action>") now also covers
// write, matching TS having no write-specific case.
func (a *App) permissionBody(request *client.PermissionRequest) string {
	pad := strings.Repeat(" ", 1)
	path := ""
	if len(request.Resources) > 0 {
		path = request.Resources[0]
	}
	switch request.Action {
	case "external_directory":
		// Listing every pattern, not just the first: one command can reach
		// into several directories, and approving covers all of them.
		if len(request.Resources) == 0 {
			return ""
		}
		lines := []string{pad + a.styles().Muted.Render("Patterns")}
		for _, resource := range request.Resources {
			lines = append(lines, pad+a.styles().Text.Render("- "+resource))
		}
		lines = append(lines, pad+a.styles().Muted.Render(
			"Allow always grants these directories and everything under them, for this project."))
		return strings.Join(lines, "\n")
	case "bash":
		if path == "" {
			return ""
		}
		return pad + a.styles().Text.Render("$ "+path)
	case "edit":
		return pad + a.styles().Muted.Render("No diff provided")
	case "read":
		if path == "" {
			return ""
		}
		return pad + a.styles().Muted.Render("Path: "+path)
	case "glob", "grep":
		if path == "" {
			return ""
		}
		return pad + a.styles().Muted.Render("Pattern: "+path)
	case "webfetch":
		if path == "" {
			return ""
		}
		return pad + a.styles().Muted.Render("URL: "+path)
	case "websearch":
		if path == "" {
			return ""
		}
		return pad + a.styles().Muted.Render("Query: "+path)
	}
	return pad + a.styles().Muted.Render("Tool: "+request.Action)
}

// mcpDotColor mirrors sidebar/mcp.tsx's dot()/dialog-status.tsx's inline
// color map exactly, plus a "connecting" case: a Go-port-only status (not
// part of TS's Status union) LoadAsync sets as a placeholder while a
// server's initial background connect is still in flight, so it never
// reads as simply missing during startup.
func mcpDotColor(t theme.Theme, status string) color.Color {
	switch status {
	case "connected":
		return t.Success
	case "failed":
		return t.Error
	case "disabled":
		return t.TextMuted
	case "needs_auth":
		return t.Warning
	case "needs_client_registration":
		return t.Error
	case "connecting":
		return t.TextMuted
	default:
		return t.TextMuted
	}
}

// mcpStatusLabel mirrors sidebar/mcp.tsx's <Switch> status label exactly
// (dialog-status.tsx additionally prefixes the needs_auth case with a "run:
// gocode mcp auth <name>" hint, folded in here too since both callers
// want it), plus a "connecting" case for the Go-port-only placeholder
// status (see mcpDotColor).
func mcpStatusLabel(s client.MCPServer) string {
	switch s.Status {
	case "connected":
		return "Connected"
	case "failed":
		if s.Error != "" {
			return s.Error
		}
		return "Failed"
	case "disabled":
		return "Disabled"
	case "needs_auth":
		return "Needs authentication (run: gocode mcp auth " + s.Name + ")"
	case "needs_client_registration":
		if s.Error != "" {
			return s.Error
		}
		return "Needs client ID"
	case "connecting":
		return "Connecting…"
	default:
		return s.Status
	}
}

// mcpConnectedCount/mcpHasFailure mirror footer.tsx's mcp()/mcpError()
// memos: the footer counts *connected* servers, not configured ones, and
// flags only the "failed" status (not needs_auth/needs_client_registration)
// as an error.
func mcpConnectedCount(servers []client.MCPServer) int {
	n := 0
	for _, s := range servers {
		if s.Status == "connected" {
			n++
		}
	}
	return n
}

func mcpHasFailure(servers []client.MCPServer) bool {
	for _, s := range servers {
		if s.Status == "failed" {
			return true
		}
	}
	return false
}

// sidebarView mirrors the Sidebar: a 42-column backgroundPanel filling the
// terminal height with the session title, Context usage, the todo list, and
// the "• GoCode version" footer pinned to the bottom.
func (a *App) sidebarView() string {
	if !a.sidebar || a.active == nil {
		return ""
	}
	width := 42
	inner := a.height
	title := truncateRunes(sessionTitleOf(*a.active), width-6)
	rows := []string{a.onPanel(a.theme.Text, true).Render(title)}

	// feature-plugins/sidebar/context.tsx. Note what it is *not*: a session
	// total. Upstream reports the last assistant turn's own context — the
	// same findLast/five-bucket sum the footer's usage meter uses — against
	// that model's context limit. This port used to sum every message's
	// tokens and divide by a hardcoded 200000, so the count grew without
	// bound and the percentage was meaningless. Only "spent" is a running
	// session total.
	context := a.sidebarContext()
	rows = append(rows, "", a.onPanel(a.theme.Text, true).Render("Context"),
		a.onPanel(a.theme.TextMuted, false).Render(groupDigits(context.tokens)+" tokens"),
		a.onPanel(a.theme.TextMuted, false).Render(fmt.Sprintf("%d%% used", context.percent)),
		a.onPanel(a.theme.TextMuted, false).Render(formatMoney(a.sessionCost())+" spent"),
	)
	// MCP (order 200 in the original's sidebar_content slots): live status
	// per server (feature-plugins/sidebar/mcp.tsx), fetched once at startup
	// via GET /api/mcp — see loadMCPCmd's doc comment for why once-at-startup
	// is the right fidelity level for this port (servers connect once at
	// boot, not per session/instance).
	if len(a.mcpServers) > 0 {
		rows = append(rows, "", a.onPanel(a.theme.Text, true).Render("MCP"))
		for _, server := range a.mcpServers {
			dot := lipgloss.NewStyle().Foreground(mcpDotColor(a.theme, server.Status)).Render("•")
			rows = append(rows, dot+" "+
				a.onPanel(a.theme.Text, false).Render(server.Name)+" "+
				a.onPanel(a.theme.TextMuted, false).Render(mcpStatusLabel(server)))
		}
	}

	// LSP (order 300), porting feature-plugins/sidebar/lsp.tsx's three states:
	// disabled outright, none started yet, or the live server list.
	rows = append(rows, "", a.onPanel(a.theme.Text, true).Render("LSP"))
	switch {
	case a.lsp == nil:
		rows = append(rows, a.onPanel(a.theme.TextMuted, false).Render("Loading..."))
	case !a.lsp.Enabled:
		rows = append(rows, a.onPanel(a.theme.TextMuted, false).Render("LSPs are disabled"))
	case len(a.lsp.Servers) == 0 && len(a.lsp.Available) == 0:
		// A deliberate addition to TS's two states. TS says "will activate as
		// files are read" whether or not any server could ever start, so a
		// missing binary — usually a PATH the process did not inherit — is
		// invisible and looks like the feature is broken.
		rows = append(rows, a.onPanel(a.theme.TextMuted, false).Render("No language servers found on PATH"))
	case len(a.lsp.Servers) == 0:
		rows = append(rows, a.onPanel(a.theme.TextMuted, false).Render("LSPs will activate as files are read"))
	default:
		for _, server := range a.lsp.Servers {
			dot := lipgloss.NewStyle().Foreground(a.theme.Success).Render("•")
			rows = append(rows, dot+" "+
				a.onPanel(a.theme.Text, false).Render(server.Name)+" "+
				a.onPanel(a.theme.TextMuted, false).Render(server.Root))
		}
	}

	if len(a.sidebarTodos) > 0 && a.hasOpenTodos() {
		rows = append(rows, "", a.onPanel(a.theme.Text, true).Render("Todo"))
		for _, todo := range a.sidebarTodos {
			rows = append(rows, a.todoRow(todo))
		}
	}

	// Footer pinned to the bottom, mirroring the sidebar-footer plugin: the
	// abbreviated directory (+ git branch) with its last segment brighter,
	// then the "• GoCode version" line.
	card := a.gettingStartedCard(width)
	pathLine := a.sidebarPathLine(width - 4)
	versionLine := a.onPanel(a.theme.Success, false).Render("•") + " " +
		a.onPanel(a.theme.Text, true).Render("Go") +
		a.onPanel(a.theme.Text, true).Render("Code") + " " +
		a.onPanel(a.theme.TextMuted, false).Render(appVersion)
	// lipgloss's Width/Height are border-box (the declared value is the
	// TOTAL rendered size, padding included — it computes the wrap width as
	// declaredWidth-leftPad-rightPad internally) and Height only pads a
	// shorter render, it never truncates a taller one — so the content rows
	// here must land at exactly inner-2 to make the two lines of
	// PaddingTop(1)+PaddingBottom(1) bring the total to exactly inner;
	// short by even one and the panel silently renders taller than its
	// column, misaligning it against the chat column's own height.
	content := inner - 2
	rows = append(rows, "")
	for len(rows) < content {
		rows = append(rows, "")
	}
	// The getting-started card sits directly above the path/version lines
	// (the plugin renders card, path, version inside one gap-1 column), so it
	// comes out of the same fixed row budget.
	tail := append(card, pathLine, versionLine)
	if len(tail) > content {
		tail = tail[len(tail)-content:]
	}
	rows = append(rows[:content-len(tail)], tail...)

	style := lipgloss.NewStyle().
		Background(a.theme.BackgroundPanel).
		PaddingTop(1).
		PaddingBottom(1).
		PaddingLeft(2).
		PaddingRight(2).
		Width(width).
		Height(inner)
	return style.Render(strings.Join(rows, "\n"))
}

// sidebarPathLine mirrors the sidebar-footer plugin's path text: the
// abbreviated directory (+ ":branch") with everything but the last segment
// muted and the last segment (which carries the branch suffix) brighter.
// sidebarPathLine mirrors the sidebar-footer plugin's path text (see the
// doc comment two functions up), truncated to maxWidth from the left — the
// sidebar's row budget assumes exactly one line per entry (see the
// content-row-count comment in sidebarView), and unlike TS's flexbox text
// this port has no wrapping layout to fall back on, so an untruncated long
// path/branch would silently overflow the panel by however many lines it
// wrapped into. Truncating from the left keeps the more useful tail (the
// project dir and branch) over the home-relative prefix.
func (a *App) sidebarPathLine(maxWidth int) string {
	full := a.homeDirectory()
	if runes := []rune(full); maxWidth > 1 && len(runes) > maxWidth {
		full = "…" + string(runes[len(runes)-(maxWidth-1):])
	}
	idx := strings.LastIndex(full, "/")
	if idx == -1 {
		return a.onPanel(a.theme.Text, false).Render(full)
	}
	return a.onPanel(a.theme.TextMuted, false).Render(full[:idx+1]) +
		a.onPanel(a.theme.Text, false).Render(full[idx+1:])
}

func (a *App) hasOpenTodos() bool {
	for _, todo := range a.sidebarTodos {
		if todo.Status != "completed" {
			return true
		}
	}
	return false
}

// todoRow mirrors TodoItem: [✓]/[•]/[ ] prefix with the content, warning
// colored while in progress and muted otherwise.
func (a *App) todoRow(todo client.Todo) string {
	mark := " "
	switch todo.Status {
	case "completed":
		mark = "✓"
	case "in_progress":
		mark = "•"
	}
	fg := a.theme.TextMuted
	if todo.Status == "in_progress" {
		fg = a.theme.Warning
	}
	content := truncateRunes(todo.Content, 32)
	return a.onPanel(fg, false).Render("[" + mark + "] " + content)
}

func (a *App) activeAgentOr(fallback string) string {
	if a.activeAgent != "" {
		return a.activeAgent
	}
	return fallback
}

// loadSidebarTodos fetches the todo list for the sidebar.
func (a *App) loadSidebarTodos() tea.Cmd {
	if a.active == nil {
		return nil
	}
	c := a.client
	sessionID := a.active.ID
	return func() tea.Msg {
		todos, err := c.Todos(a.ctx, sessionID)
		if err != nil {
			return nil
		}
		return sidebarTodosMsg{todos: todos}
	}
}

type sidebarTodosMsg struct{ todos []client.Todo }

// logoLeft is the "Go" half of the wordmark, using the same glyphs the
// original's bg-pulse "go" logo does (packages/tui/src/logo.ts).
var logoLeft = []string{
	"         ",
	"█▀▀▀ █▀▀█",
	"█_^█ █__█",
	"▀▀▀▀ ▀▀▀▀",
}

var logoRight = []string{
	"             ▄     ",
	"█▀▀▀ █▀▀█ █▀▀█ █▀▀█",
	"█___ █__█ █__█ █^^^",
	"▀▀▀▀ ▀▀▀▀ ▀▀▀▀ ▀▀▀▀",
}

// renderLogoLine renders one logo row: left half in muted, right half in
// text+bold, applying the original's character substitution (_ → shadowed
// space, ^ → ▀, ~ → shadowed ▀, , → ▄).
func (a *App) renderLogoLine(left, right string, line int) string {
	renderHalf := func(text string, fg color.Color, bold bool) string {
		style := lipgloss.NewStyle().Foreground(fg)
		if bold {
			style = style.Bold(true)
		}
		shadow := theme.Tint(a.theme.Background, fg, 0.25)
		shadowStyle := lipgloss.NewStyle().Foreground(shadow)
		if bold {
			shadowStyle = shadowStyle.Bold(true)
		}
		var out strings.Builder
		for _, char := range text {
			switch char {
			case '_':
				out.WriteString(style.Background(shadow).Render(" "))
			case '^':
				out.WriteString(style.Background(shadow).Render("▀"))
			case '~':
				out.WriteString(shadowStyle.Render("▀"))
			case ',':
				out.WriteString(shadowStyle.Render("▄"))
			default:
				out.WriteString(style.Render(string(char)))
			}
		}
		return out.String()
	}
	gap := lipgloss.NewStyle().Render(" ")
	return renderHalf(left, a.theme.TextMuted, false) + gap + renderHalf(right, a.theme.Text, true)
}

// viewHome mirrors the Home route: logo, prompt, and tip centered as a block
// (biased slightly upward like the original's fixed top spacer), with the
// directory/MCP/version status bar pinned to the bottom.
func (a *App) viewHome() string {
	area := a.width - 6
	if area < 24 {
		area = 24
	}

	var rows []string
	for i := range logoLeft {
		rows = append(rows, a.renderLogoLine(logoLeft[i], logoRight[i], i))
	}
	logo := centerBlock(area, strings.Join(rows, "\n"))
	prompt := centerBlock(area, a.homePromptBlock(promptMaxWidth(a.width)-1))
	// tips_toggle (<leader>h). The row keeps its place in the stack so the
	// logo and prompt do not jump when the tip is hidden.
	tip := centerBlock(area, a.tipLine(area))
	if a.tipsHidden {
		tip = ""
	}

	content := strings.Join([]string{logo, "", prompt, "", tip}, "\n")

	status := a.statusBar(a.width - 2)
	available := a.height - 3 - lipgloss.Height(content)
	top := available/2 + 2
	bottom := available - top
	if top < 0 {
		top = 0
	}
	if bottom < 0 {
		bottom = 0
	}
	return a.frame(strings.Repeat("\n", top) + content +
		strings.Repeat("\n", bottom+1) + status + "\n")
}

// centerBlock centers a multi-line block within width, preserving internal
// alignment (lipgloss.Place would center each line independently).
func centerBlock(width int, block string) string {
	pad := (width - lipgloss.Width(block)) / 2
	if pad < 0 {
		pad = 0
	}
	prefix := strings.Repeat(" ", pad)
	lines := strings.Split(block, "\n")
	for i, line := range lines {
		lines[i] = prefix + line
	}
	return strings.Join(lines, "\n")
}
