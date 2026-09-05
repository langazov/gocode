package tui

import (
	"image/color"
	"os"
	"os/exec"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/langazov/gocode-go/internal/tui/client"
)

// spinnerFrames is component/spinner.tsx's SPINNER_FRAMES, the inline
// braille spinner beside a running tool row.
var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// spinnerTick drives both spinners at the finer of their two upstream rates:
// the hint row's scanner is `interval={40}` (spinner.go), the inline braille
// spinner is `interval={80}`. This port had one 120ms loop for both, which
// left the scanner — 54 frames to a full sweep — far too slow to read as an
// animation at all.
const spinnerTick = 40 * time.Millisecond

// spinnerBrailleEvery is how many spinnerTicks make up one braille frame,
// reproducing <Spinner>'s own 80ms interval off the shared 40ms loop.
const spinnerBrailleEvery = 2

// spinnerPlaceholder stands in for the inline braille glyph inside a message
// block while it is being rendered, so the block can be cached across frames
// and still animate: renderMessageCached substitutes the current frame into
// the cached string on the way out (see substituteSpinner).
//
// It has to be exactly one cell wide, because the block's width math — style
// padding, border widths, wrapping — runs while the placeholder is still in
// place. U+E000 is private use, so no rendered content can contain it, and
// both lipgloss.Width and ansi.StringWidth measure it as 1, same as every
// braille frame and the "⋯" fallback it is replaced with.
const spinnerPlaceholder = ""

// spinnerGlyph ports Spinner's <Show when={kv.get("animations_enabled")}>:
// the animated frame, or a static "⋯" when animations are disabled — same
// fallback glyph as the TSX's `fallback={<text>⋯ {children}</text>}`.
func (a *App) spinnerGlyph() string {
	if !a.animationsEnabled {
		return "⋯"
	}
	return spinnerFrames[(a.spinnerFrame/spinnerBrailleEvery)%len(spinnerFrames)]
}

// substituteSpinner swaps the current frame into a rendered block. Cheap
// enough to run on every frame over the whole visible timeline, which is the
// point: it is what lets a block containing a spinner still be cached.
func (a *App) substituteSpinner(block string) string {
	if !strings.Contains(block, spinnerPlaceholder) {
		return block
	}
	return strings.ReplaceAll(block, spinnerPlaceholder, a.spinnerGlyph())
}

func (a *App) spinnerLabel() string {
	return a.styles().Warning.Render(a.spinnerGlyph() + " working…")
}

type spinnerTickMsg struct{}

// startSpinner keeps exactly one tick loop alive while a turn is running.
//
// The guard is load-bearing in both directions. Without `a.spinning` the
// several places that set a.busy each start their own loop, and the frames
// advance once per loop per tick — a spinner running at 2-3x speed. And
// because a loop only ever restarts from inside its own tick, every place
// that can set a.busy must call this: applySnapshot (the aggregator path) is
// the one that actually reports a live turn, and when it set a.busy without
// starting a loop the spinner sat frozen on frame 0 for the whole turn.
func (a *App) startSpinner() tea.Cmd {
	if !a.busy || a.spinning {
		return nil
	}
	a.spinning = true
	return tea.Tick(spinnerTick, func(time.Time) tea.Msg { return spinnerTickMsg{} })
}

// toastVariant mirrors ToastOptions.variant.
type toastVariant int

const (
	toastInfo toastVariant = iota
	toastSuccess
	toastWarning
	toastError
)

// defaultToastDuration mirrors toast.tsx's `options.duration ?? 5000`.
const defaultToastDuration = 5 * time.Second

// toast is a transient notification, mirroring ui/toast.tsx's ToastOptions.
type toast struct {
	title   string
	text    string
	variant toastVariant
	expires time.Time
}

// toastOptions mirrors ToastInput (ToastOptions with an optional duration).
type toastOptions struct {
	title    string
	message  string
	variant  toastVariant
	duration time.Duration
}

// showToastOptions mirrors useToast().show(options): full control over
// title/variant/duration.
func (a *App) showToastOptions(opts toastOptions) tea.Cmd {
	duration := opts.duration
	if duration <= 0 {
		duration = defaultToastDuration
	}
	a.toast = &toast{title: opts.title, text: opts.message, variant: opts.variant, expires: time.Now().Add(duration)}
	return tea.Tick(duration, func(time.Time) tea.Msg { return toastExpiredMsg{} })
}

// showToast is the common case used by statusMsg/copy handlers: a plain
// message with no title, mirroring the many `toast.show({variant, message})`
// / `toast.error(err)` call sites that don't need a title or custom
// duration. isError selects the "error"/"info" variant.
func (a *App) showToast(text string, isError bool) tea.Cmd {
	variant := toastInfo
	if isError {
		variant = toastError
	}
	return a.showToastOptions(toastOptions{message: text, variant: variant})
}

type toastExpiredMsg struct{}

// toastBorder mirrors SplitBorder's ┃ accent bars on left AND right
// (border.ts) — every other split-border panel in this port (userBlock,
// errBlock, bashBlock, promptBox) is left-only; the toast is the one place
// TS uses both sides.
func toastBorder() lipgloss.Border {
	return lipgloss.Border{Left: "┃", Right: "┃"}
}

// toastVariantColor mirrors `theme[current().variant]`.
func (a *App) toastVariantColor(v toastVariant) color.Color {
	switch v {
	case toastSuccess:
		return a.theme.Success
	case toastWarning:
		return a.theme.Warning
	case toastError:
		return a.theme.Error
	default:
		return a.theme.Info
	}
}

// viewToastPanel renders the active toast panel and, if its message embeds a
// bare URL, the (row, col) span of that link within the panel (relative to
// the panel's own top-left, before compositeToast places it on screen) —
// the toast-side counterpart of dialogs.go's overlayHits, ported for ui/
// link.tsx's "click the link text to open it" behavior.
func (a *App) viewToastPanel() (panel string, link *linkHit) {
	if a.toast == nil || time.Now().After(a.toast.expires) {
		return "", nil
	}
	totalWidth := min(60, a.width-6)
	if totalWidth < 12 {
		totalWidth = 12
	}
	const borderCols = 2  // one ┃ each side
	const paddingCols = 4 // paddingLeft(2) + paddingRight(2)
	contentWidth := totalWidth - borderCols - paddingCols

	var lines []string
	msgStart := 0
	if a.toast.title != "" {
		lines = append(lines, lipgloss.NewStyle().Bold(true).Foreground(a.theme.Text).Render(wrapText(a.toast.title, contentWidth)))
		lines = append(lines, "") // marginBottom={1}
		msgStart = len(lines)
	}
	msgLines := strings.Split(wrapText(a.toast.text, contentWidth), "\n")
	for i, line := range msgLines {
		if loc := urlPattern.FindStringIndex(line); loc != nil && link == nil {
			href := line[loc[0]:loc[1]]
			pre, post := line[:loc[0]], line[loc[1]:]
			const leftInset = 1 + 2 // ┃ border + paddingLeft(2)
			link = &linkHit{
				row:      msgStart + i,
				colStart: leftInset + len([]rune(pre)),
				href:     href,
			}
			link.colEnd = link.colStart + len([]rune(href))
			line = a.styles().Text.Render(pre) + renderLink(href, href, a.styles().Text) + a.styles().Text.Render(post)
		} else {
			line = a.styles().Text.Render(line)
		}
		msgLines[i] = line
	}
	lines = append(lines, msgLines...)

	style := lipgloss.NewStyle().
		Border(toastBorder(), false, true, false, true).
		BorderForeground(a.toastVariantColor(a.toast.variant)).
		Background(a.theme.BackgroundPanel).
		PaddingTop(1).
		PaddingBottom(1).
		PaddingLeft(2).
		PaddingRight(2).
		Width(totalWidth)
	return style.Render(strings.Join(lines, "\n")), link
}

// compositeToast splices the toast panel into base at top=2, right=2,
// mirroring ui/toast.tsx's `position="absolute" top={2} right={2}`, and
// records its link's absolute screen position (if any) for handleClick.
// Only called with no dialog overlay active — the original nests Toast
// inside each route's own box rather than the dialog layer.
//
// base (viewChat()/viewHome()) has already been through a.frame() once, so
// this splices directly onto it rather than wrapping the result in a.frame()
// again — a second Padding(0,1) would shift every cell one column right of
// where it actually appears on screen, one column off from where linkHits
// records the link (recorded in this same, unwrapped coordinate space).
func (a *App) compositeToast(base string) string {
	panel, link := a.viewToastPanel()
	if panel == "" {
		a.linkHits = nil
		return base
	}
	top, left := 2, a.width-lipgloss.Width(panel)-2
	if left < 0 {
		left = 0
	}
	if link != nil {
		a.linkHits = []linkHit{{row: top + link.row, colStart: left + link.colStart, colEnd: left + link.colEnd, href: link.href}}
	} else {
		a.linkHits = nil
	}
	return a.spliceAt(base, panel, top, left)
}

// timelineOverlayItems lists user prompts for the timeline dialog
// (ctrl+x g), newest first like DialogTimeline, with the created time as the
// footer annotation.
func (a *App) timelineOverlayItems() []overlayItem {
	items := make([]overlayItem, 0, len(a.timeline))
	for i := len(a.timeline) - 1; i >= 0; i-- {
		message := a.timeline[i]
		if message.Type != "user" {
			continue
		}
		data, err := client.DecodeUser(message.Data)
		if err != nil || data.Text == "" {
			continue
		}
		messageID := message.ID
		items = append(items, overlayItem{
			label:  strings.ReplaceAll(data.Text, "\n", " "),
			value:  messageID,
			footer: time.UnixMilli(message.TimeCreated).Format("3:04 PM"),
			action: func() tea.Msg {
				return a.forkFrom(messageID)
			},
		})
	}
	return items
}

// forkFrom forks the active session at a message and opens the child.
func (a *App) forkFrom(messageID string) tea.Cmd {
	if a.active == nil {
		return staticMsg(statusMsg{text: "open a session first"})
	}
	c := a.client
	parent := a.active.ID
	return func() tea.Msg {
		child, err := c.Fork(a.ctx, parent, messageID)
		if err != nil {
			return statusMsg{text: "fork failed: " + err.Error()}
		}
		return sessionOpenedMsg{session: child}
	}
}

// childrenOverlay lists forked child sessions (the subagent dialog).
func (a *App) childrenOverlay() tea.Cmd {
	if a.active == nil {
		return staticMsg(statusMsg{text: "open a session first"})
	}
	c := a.client
	parent := a.active.ID
	return func() tea.Msg {
		children, err := c.Children(a.ctx, parent)
		if err != nil {
			return statusMsg{text: "failed to load children: " + err.Error()}
		}
		items := make([]overlayItem, 0, len(children))
		for i := range children {
			child := children[i]
			// Live status comes from the aggregated snapshot rather than a
			// fetch: subagent sessions are children too, and their activity
			// is already streaming in. See aggregator.go.
			hint := relativeTime(child.TimeUpdated)
			if node := a.agents.Sessions[child.ID]; node != nil && node.Busy {
				hint = "running · " + hint
			}
			items = append(items, overlayItem{
				label: sessionTitleOf(child),
				hint:  hint,
				value: child.ID,
				action: func() tea.Msg {
					a.active = &child
					a.view = viewChat
					a.timeline = nil
					a.scrollOffset = 0
					return reloadMsg{}
				},
			})
		}
		if len(items) == 0 {
			items = append(items, overlayItem{label: "(no forked or subagent sessions)"})
		}
		a.openList("Forked & subagent sessions", items)
		return nil
	}
}

// compactNow triggers immediate compaction (leader+c / session.compact).
func (a *App) compactNow() tea.Cmd {
	if a.active == nil {
		return staticMsg(statusMsg{text: "open a session first"})
	}
	c := a.client
	sessionID := a.active.ID
	return func() tea.Msg {
		compacted, err := c.Compact(a.ctx, sessionID)
		if err != nil {
			return statusMsg{text: "compact failed: " + err.Error()}
		}
		if !compacted {
			return statusMsg{text: "nothing to compact"}
		}
		return statusMsg{text: "context compacted"}
	}
}

// copyTranscript writes the whole conversation to the terminal clipboard via
// OSC52 (session.copy).
func (a *App) copyTranscript() tea.Cmd {
	var builder strings.Builder
	for _, message := range a.timeline {
		switch message.Type {
		case "user":
			if data, err := client.DecodeUser(message.Data); err == nil {
				builder.WriteString("you: " + data.Text + "\n\n")
			}
		case "assistant":
			if data, err := client.DecodeAssistant(message.Data); err == nil {
				for _, part := range data.Content {
					switch part.Type {
					case "text":
						if part.Text != "" {
							builder.WriteString("assistant: " + part.Text + "\n\n")
						}
					case "tool":
						builder.WriteString("assistant: [tool " + part.Name + "]\n")
					}
				}
			}
		}
	}
	text := builder.String()
	if strings.TrimSpace(text) == "" {
		return staticMsg(statusMsg{text: "nothing to copy"})
	}
	return copyToClipboard(a, text, "transcript copied")
}

// exportToEditor opens the current prompt in $EDITOR (session.export,
// ctrl+x e), suspending the interface for the edit session.
func (a *App) exportToEditor() tea.Cmd {
	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = "vi"
	}
	tmp, err := os.CreateTemp("", "gocode-prompt-*.md")
	if err != nil {
		return staticMsg(statusMsg{text: err.Error()})
	}
	if _, err := tmp.WriteString(a.input.Value()); err != nil {
		tmp.Close()
		return staticMsg(statusMsg{text: err.Error()})
	}
	tmp.Close()
	execCmd := exec.Command(editor, tmp.Name())
	editorPath := tmp.Name()
	return tea.ExecProcess(execCmd, func(err error) tea.Msg {
		os.Remove(editorPath)
		if err != nil {
			return statusMsg{text: "editor failed: " + err.Error()}
		}
		if content, readErr := os.ReadFile(editorPath); readErr == nil {
			a.input.SetValue(strings.TrimSpace(string(content)))
		}
		return nil
	})
}
