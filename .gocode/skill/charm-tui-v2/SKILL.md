---
name: charm-tui-v2
description: Build, debug, or extend terminal user interfaces with Bubble Tea v2, Lip Gloss v2, Bubbles v2, and Glamour v2 — the charm.land vanity-domain ecosystem this project's internal/tui package is built on. Covers the Elm Architecture (Model-Update-View), declarative tea.View fields, command batching, key/mouse/paste message handling, SSE event pumping into a TUI, layout with Lip Gloss, markdown rendering with Glamour, spinners, autocomplete popups, dialog overlays, themes, headless testing, and v1→v2 migration. Use when writing or modifying any file under internal/tui, adding a new TUI component, fixing a rendering or input bug, or migrating v1 Bubble Tea code.
---

# Bubble Tea v2 TUI Skill

A comprehensive guide for building terminal user interfaces with the Charm
ecosystem v2 — **Bubble Tea v2**, **Lip Gloss v2**, **Bubbles v2**, and
**Glamour v2**. All import paths use the `charm.land` vanity domain.

This project's TUI lives in `internal/tui` (~11 000 lines, 30 files). It is a
Go port of a TypeScript TUI, built entirely on Bubble Tea v2, and talks to
the core only over HTTP. Use this skill whenever you touch that package or
write new TUI code.

## Import paths

```go
// v2 (current) — charm.land vanity domain
import tea "charm.land/bubbletea/v2"
import "charm.land/lipgloss/v2"
import "charm.land/bubbles/v2/textarea"   // or viewport, list, spinner, etc.
import "charm.land/glamour/v2"

// v1 (legacy — do NOT use in new code)
import tea "github.com/charmbracelet/bubbletea"
import "github.com/charmbracelet/lipgloss"
```

If you see `github.com/charmbracelet/bubbletea` (v1) in existing code, treat
it as a migration target — see the v1→v2 migration checklist at the end.

---

## The Elm Architecture

Bubble Tea follows The Elm Architecture: **state** (Model), **messages**
(Msg), **update** (handles events, returns new model + commands), and
**view** (renders the UI from the model).

```
Init() tea.Cmd → Update(msg) (tea.Model, tea.Cmd) → View() tea.View → render
                        ↑                                          |
                        └── keypress, tick, HTTP result, SSE event ┘
                        └── tea.Cmd runs off the loop ──────────────┘
```

### The golden rule: Update must not block

`Update` runs on the main goroutine. Anything slow — an HTTP call, a
clipboard read, a file write — must be returned as a `tea.Cmd`, which the
runtime executes on its own goroutine and delivers back as a message.
Blocking in `Update` freezes the interface, including the key handler.

---

## The Model interface

A Bubble Tea program needs three methods:

```go
type model struct {
    // your application state
}

// Init returns the initial command(s) to run at startup.
func (m model) Init() tea.Cmd {
    return tea.Batch(
        loadSessions(),
        loadCatalog(),
        tick(),
    )
}

// Update handles incoming events and returns an updated model plus commands.
func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
    switch msg := msg.(type) {
    case tea.WindowSizeMsg:
        m.width, m.height = msg.Width, msg.Height
    case tea.KeyPressMsg:
        switch msg.String() {
        case "q", "ctrl+c":
            return m, tea.Quit
        }
    }
    return m, nil
}

// View renders the UI declaratively.
func (m model) View() tea.View {
    v := tea.NewView("Hello, world!")
    v.AltScreen = true
    v.MouseMode = tea.MouseModeAllMotion
    return v
}
```

### Program adapter pattern

For a large app, wrap your app struct in a thin adapter that satisfies the
immutable `tea.Model` interface, letting your `App` use internal methods on
`*App` without the immutability constraint:

```go
// internal/tui/app.go
type program struct{ app *App }

func (p program) Init() tea.Cmd { return p.app.Init() }

func (p program) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
    return p, p.app.Update(msg)
}

func (p program) View() tea.View {
    v := tea.NewView(p.app.View())
    v.AltScreen = true
    v.MouseMode = tea.MouseModeAllMotion
    v.WindowTitle = p.app.windowTitle
    // Do NOT force background on transparent themes (lipgloss.NoColor).
    if _, transparent := p.app.theme.Background.(lipgloss.NoColor); !transparent {
        v.BackgroundColor = p.app.theme.Background
    }
    v.ForegroundColor = p.app.theme.Text
    return v
}
```

---

## View() returns tea.View (not string)

In v2, `View()` returns a `tea.View` struct, not a `string`. This is the
single biggest API change from v1.

```go
// v1:
func (m model) View() string { return "Hello, world!" }

// v2:
func (m model) View() tea.View { return tea.NewView("Hello, world!") }
```

### Declarative View fields

The `tea.View` struct has fields for everything that used to be controlled
by program options and imperative commands. The big idea: **no more startup
option flags, no more toggle commands — just declare what you want and
Bubble Tea makes it so.**

| Field | What it does |
|---|---|
| `Content` (set via `SetContent()` or `NewView()`) | The rendered string |
| `AltScreen` | Enter/exit the alternate screen buffer |
| `MouseMode` | `MouseModeNone`, `MouseModeCellMotion`, or `MouseModeAllMotion` |
| `ReportFocus` | Enable focus/blur event reporting |
| `DisableBracketedPasteMode` | Disable bracketed paste |
| `WindowTitle` | Set the terminal window title |
| `Cursor` | Control cursor position, shape, color, and blink |
| `ForegroundColor` | Set the terminal foreground color |
| `BackgroundColor` | Set the terminal background color |
| `ProgressBar` | Show a native terminal progress bar |
| `KeyboardEnhancements` | Request keyboard enhancement features |
| `OnMouse` | Intercept mouse messages based on view content |

### Background/Foreground color gotcha

`BackgroundColor`/`ForegroundColor` set the terminal's own default colors
(an OSC 11/10 the renderer sends once per change, not a per-cell fill). This
matters for themes: cells no component's style explicitly touches are left
to whatever the terminal's own default colors are. A light theme with a
dark terminal default will look wrong unless you set these. But do NOT
force them on a theme that uses `lipgloss.NoColor` to stay transparent.

---

## Commands

A `tea.Cmd` is `func() tea.Msg` — a function the runtime runs on its own
goroutine. The returned message is fed back into `Update`.

### tea.Batch — run commands concurrently

```go
func (m model) Init() tea.Cmd {
    return tea.Batch(loadSessions(), loadCatalog(), loadPermissions(), tick())
}
```

Nil commands are silently dropped. `Batch` runs all commands concurrently
with no ordering guarantees.

### tea.Sequence — run commands in order

```go
// renamed from v1's tea.Sequentially
return tea.Sequence(stepOne(), stepTwo(), stepThree())
```

### tea.Tick — timer (fires once)

```go
func tickCmd() tea.Cmd {
    return tea.Tick(10*time.Second, func(time.Time) tea.Msg {
        return tickMsg{}
    })
}

// In Update, schedule the next tick to loop:
case tickMsg:
    return tickCmd() // schedule next tick
```

`Tick` fires once. To repeat, return another `Tick` from the handler.

### tea.Every — clock-synced tick

```go
return tea.Every(time.Second, func(t time.Time) tea.Msg {
    return tickMsg{t}
})
```

`Every` snaps to the system clock boundary (e.g. a 1-second every fires at
:00, :01, :02 — not 1.3s, 2.3s).

### tea.Quit — stop the program

```go
case "q", "ctrl+c":
    m.quitting = true
    return m, tea.Quit
```

### tea.Suspend — suspend the process

```go
case "ctrl+z":
    return m, tea.Suspend
```

### Custom commands — HTTP calls, I/O

```go
func (a *App) loadMessages(sessionID string) tea.Cmd {
    c := a.client
    return func() tea.Msg {
        messages, err := c.GetMessages(a.ctx, sessionID)
        if err != nil {
            return errMsg{err: err}
        }
        return messagesMsg{sessionID: sessionID, messages: messages}
    }
}
```

The command closure captures what it needs and returns a message. The
message flows back into `Update` where it is folded into state.

### Static message (no I/O)

```go
func staticMsg(msg tea.Msg) tea.Cmd {
    return func() tea.Msg { return msg }
}
```

---

## Messages

Any Go type can be a `tea.Msg`. Convention: name them with a `Msg` suffix.

### Built-in messages

| Message | When it fires |
|---|---|
| `tea.WindowSizeMsg{Width, Height}` | Terminal resized (also sent at startup) |
| `tea.KeyPressMsg` | A key was pressed |
| `tea.KeyReleaseMsg` | A key was released (Kitty protocol only) |
| `tea.PasteMsg{Content}` | Bracketed paste content arrived |
| `tea.PasteStartMsg` | Bracketed paste started |
| `tea.PasteEndMsg` | Bracketed paste ended |
| `tea.MouseMsg` (interface) | Any mouse event — type-switch to the concrete type |
| `tea.FocusMsg` | Terminal gained focus (if `ReportFocus`) |
| `tea.BlurMsg` | Terminal lost focus (if `ReportFocus`) |

### Custom messages

```go
type sessionsMsg struct{ sessions []client.Session }
type catalogMsg struct {
    models    []client.Model
    providers []client.Provider
    err       error
}
type statusMsg struct {
    text  string
    isErr bool
}
```

---

## Key handling

### tea.KeyPressMsg (not tea.KeyMsg)

In v2, `tea.KeyMsg` is an **interface**. For key presses, match on
`tea.KeyPressMsg`:

```go
func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
    switch msg := msg.(type) {
    case tea.KeyPressMsg:
        switch msg.String() {
        case "q", "ctrl+c":
            return m, tea.Quit
        case "space":       // v2: "space", not " "
            m.count++
        case "enter":
            return m, m.submit()
        case "up", "k":
            m.cursor--
        case "down", "j":
            m.cursor++
        }
    }
    return m, nil
}
```

To handle both presses and releases, match on `tea.KeyMsg` and
type-switch inside.

### Key field reference (v1 → v2)

| v1 | v2 | Notes |
|---|---|---|
| `msg.Type` | `msg.Code` | A rune: `tea.KeyEnter`, `'a'`, etc. |
| `msg.Runes` | `msg.Text` | Now a `string`, not `[]rune` |
| `msg.Alt` | `msg.Mod.Contains(tea.ModAlt)` | Modifier bitmask |
| `tea.KeyRune` | check `len(msg.Text) > 0` | — |
| `tea.KeyCtrlC` | `msg.Code == 'c' && msg.Mod == tea.ModCtrl` or `msg.String() == "ctrl+c"` | — |
| `case " ":` (space) | `case "space":` | `String()` returns `"space"` now |

### New key fields in v2

- `key.ShiftedCode` — shifted key code (e.g. `'B'` when pressing shift+b)
- `key.BaseCode` — key on a US PC-101 layout (for international keyboards)
- `key.IsRepeat` — auto-repeating key (Kitty protocol / Windows Console only)
- `key.Keystroke()` — like `String()` but always includes modifier info

### Key matching in tests

```go
// Press a named key
app.Update(tea.KeyPressMsg{Text: "x", Code: 'x'})

// Press ctrl+c
p.Send(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})

// Press enter
drive(t, app, tea.KeyPressMsg{Code: tea.KeyEnter})
```

### Leader key pattern

A leader key enables chord bindings like `<leader> s` without exhausting
Ctrl combinations. The `tea.Tick` serves as a timeout — if no follow-up key
arrives in time, `leaderTimeoutMsg` disarms the leader:

```go
const leaderKey = "ctrl+x"

func (a *App) handleKey(msg tea.KeyMsg) tea.Cmd {
    if a.leaderArmed {
        a.leaderArmed = false
        switch msg.String() {
        case "n": return a.newSession()
        case "m": return a.modelsOverlay()
        case "l":
            drive := a.loadSessionsCmd()
            a.sessionsOverlay()
            return drive
        }
        return nil
    }
    if msg.String() == leaderKey {
        a.leaderArmed = true
        return tea.Tick(time.Second, func(time.Time) tea.Msg {
            return leaderTimeoutMsg{}
        })
    }
    // ...normal key handling...
}
```

---

## Mouse handling

### Mouse events are split by type (v2)

v2 splits what v1 encoded as a single `MouseMsg` + `Action` field into four
concrete message types — the message's own type now says what v1's `Action`
field used to:

```go
func (a *App) handleMouse(msg tea.MouseMsg) tea.Cmd {
    switch msg := msg.(type) {
    case tea.MouseWheelMsg:
        return a.handleWheel(msg.Mouse())
    case tea.MouseClickMsg:
        if msg.Button == tea.MouseLeft {
            return a.handleClick(msg.Mouse().X, msg.Mouse().Y)
        }
    case tea.MouseMotionMsg:
        return a.handleMouseMotion(msg.Mouse())
    case tea.MouseReleaseMsg:
        return a.handleMouseRelease(msg.Mouse())
    }
    return nil
}
```

### Getting coordinates

```go
// v1: msg.X, msg.Y directly
// v2: call msg.Mouse() to get the Mouse struct
case tea.MouseClickMsg:
    mouse := msg.Mouse()
    x, y := mouse.X, mouse.Y
    button := mouse.Button
    mod := mouse.Mod
```

### Button constants (v1 → v2)

| v1 | v2 |
|---|---|
| `tea.MouseButtonLeft` | `tea.MouseLeft` |
| `tea.MouseButtonRight` | `tea.MouseRight` |
| `tea.MouseButtonMiddle` | `tea.MouseMiddle` |
| `tea.MouseButtonWheelUp` | `tea.MouseWheelUp` |
| `tea.MouseButtonWheelDown` | `tea.MouseWheelDown` |

### Mouse mode is a View field

```go
// v1: program option
p := tea.NewProgram(model{}, tea.WithMouseCellMotion())

// v2: View field
func (m model) View() tea.View {
    v := tea.NewView("...")
    v.MouseMode = tea.MouseModeAllMotion  // or MouseModeCellMotion
    return v
}
```

Use `MouseModeAllMotion` if you need hover/motion events without a button
held (e.g. dialog row preselection on hover). Use `MouseModeCellMotion`
for click+drag only.

### Drag-to-select (text selection)

Bubble Tea has no per-renderable text-selection primitive. To rebuild
drag-to-select, operate on the plain rendered frame string using
`charmbracelet/x/ansi` to slice/wrap styled lines without corrupting
escape sequences:

```go
type textSelection struct {
    active               bool
    anchorRow, anchorCol int
    row, col             int
}

func (a *App) handleMousePress(msg tea.Mouse) tea.Cmd {
    if msg.Button != tea.MouseLeft { return nil }
    a.selection.begin(msg.Y, msg.X)
    // ...preselect dialog row under cursor...
    return nil
}

func (a *App) handleMouseRelease(msg tea.Mouse) tea.Cmd {
    dragged := a.selection.active && a.selection.hasRange()
    a.selection.active = false
    if dragged {
        cmd := a.copySelectionCmd()  // copy real range to clipboard
        a.selection.clear()
        return cmd
    }
    a.selection.clear()
    return a.handleClick(msg.X, msg.Y) // plain click
}
```

---

## Paste handling

Paste events are their own message types in v2 (not `tea.KeyMsg` with a
`Paste` flag):

```go
case tea.PasteMsg:
    // msg.Content is the pasted text
    return a.handlePaste(msg)
case tea.PasteStartMsg:
    // paste started
case tea.PasteEndMsg:
    // paste ended
```

**Gotcha**: if `tea.PasteMsg` falls through your `Update` switch without a
case, the pasted text is silently dropped.

### Paste normalization patterns

- **Line endings**: Windows terminals and ConPTY send `\r` or `\r\n` inside
  a paste; normalize to `\n`.
- **Large paste collapse**: over 150 chars or 3 lines, show
  `[Pasted ~40 lines]` instead of the content. Store the real text in a
  map and restore it on submit. Deleting the placeholder drops the content.

---

## Program startup

```go
func Run(ctx context.Context, c *client.Client, themeName string, opts RunOptions) error {
    app := New(ctx, c, themeName)
    program := tea.NewProgram(program{app: app})
    // AltScreen/MouseMode are declared per-frame in View(), not here.
    _, err := program.Run()
    return err
}
```

### New program options in v2

```go
// Force a color profile (great for testing)
p := tea.NewProgram(model, tea.WithColorProfile(lipgloss.TrueColor))

// Set initial window size (great for testing)
p := tea.NewProgram(model, tea.WithWindowSize(120, 40))
```

### Removed program options (moved to View fields)

| v1 option | v2 replacement |
|---|---|
| `tea.WithAltScreen()` | `view.AltScreen = true` |
| `tea.WithMouseCellMotion()` | `view.MouseMode = tea.MouseModeCellMotion` |
| `tea.WithMouseAllMotion()` | `view.MouseMode = tea.MouseModeAllMotion` |
| `tea.WithReportFocus()` | `view.ReportFocus = true` |
| `tea.WithoutBracketedPaste()` | `view.DisableBracketedPasteMode = true` |
| `tea.WithInputTTY()` | Removed — v2 always opens the TTY automatically |
| `tea.WithANSICompressor()` | Removed — new renderer handles optimization |

### Removed program methods

| v1 method | v2 replacement |
|---|---|
| `p.Start()` / `p.StartReturningModel()` | `p.Run()` |
| `p.EnterAltScreen()` | `view.AltScreen = true` |
| `p.SetWindowTitle(...)` | `view.WindowTitle = "..."` |

---

## SSE / background event streaming

A persistent goroutine pumps server events into the program. A `tea.Cmd`
may only return once, so the event stream **cannot** be a command — use a
goroutine + `p.Send()`:

```go
func Run(ctx context.Context, c *client.Client) error {
    app := New(ctx, c)
    program := tea.NewProgram(program{app: app})

    events, err := c.Events(ctx, "")
    if err != nil { return err }

    // Two goroutines: aggregate → pump
    snapshots := make(chan Snapshot, 8)  // buffered — drop, don't block
    go func() {
        defer close(snapshots)
        Aggregate(ctx, events, snapshots, DefaultFrame)
    }()
    go pumpSnapshots(program, snapshots)

    _, err = program.Run()
    return err
}

func pumpSnapshots(p *tea.Program, snapshots <-chan Snapshot) {
    for snap := range snapshots {
        p.Send(snapshotMsg{snap: snap})
    }
}
```

### Dropping frames, not back-pressure

```go
snapshots := make(chan Snapshot, 8)  // buffered
```

A slow terminal must degrade by **dropping frames**, never by applying
back-pressure to the event source. The aggregator emits at most one
coalesced snapshot per frame; a full channel drops the stale one. `dropped`
counts events lost to a full channel.

---

## Layout with Lip Gloss v2

```go
import "charm.land/lipgloss/v2"
```

### Basic styling

```go
style := lipgloss.NewStyle().
    Bold(true).
    Foreground(lipgloss.Color("205")).
    Background(lipgloss.Color("235")).
    PaddingLeft(2).
    PaddingRight(2)

rendered := style.Render("Hello!")
```

### Layout — joining

```go
// Horizontal — side by side
row := lipgloss.JoinHorizontal(lipgloss.Top, leftCol, rightCol)

// Vertical — stacked
col := lipgloss.JoinVertical(lipgloss.Left, header, body, footer)
```

### Width and measurement

```go
w := lipgloss.Width(styledString)  // display width (handles ANSI + wide chars)
```

`lipgloss.Width` is border-box: the declared `Width` is the **total**
rendered size including padding. It computes the wrap width as
`declaredWidth - leftPad - rightPad` internally. `Height` only pads a
shorter render — it never truncates a taller one.

### The per-line render gotcha

`lipgloss.Style.Render` pads every line of multi-line content to the
block's own longest line. That padding is only colored when the style has
its own `Background` set. A foreground-only style leaves padding as bare,
uncolored spaces. When embedded inside a different style's `Background`,
each shorter line shows a stray patch of the page background punched
through the panel.

**Fix**: render each line individually:

```go
func renderLines(style lipgloss.Style, text string) string {
    lines := strings.Split(text, "\n")
    for i, line := range lines {
        lines[i] = style.Render(line)
    }
    return strings.Join(lines, "\n")
}
```

### Borders

```go
// Full border
style := lipgloss.NewStyle().Border(lipgloss.NormalBorder())

// Partial border (left only)
style := lipgloss.NewStyle().
    Border(lipgloss.Border{Left: "┃"}, false, false, false, true).
    BorderForeground(theme.Border)
```

---

## Themes

Use **semantic** colors, not literal ones. A component asks for "muted
text" and the theme decides the actual color:

```go
type Theme struct {
    Text              color.Color
    TextMuted         color.Color
    Primary           color.Color
    Accent            color.Color
    Border            color.Color
    BorderActive      color.Color
    Background        color.Color
    BackgroundElement color.Color
    BackgroundPanel   color.Color
    BackgroundMenu    color.Color
    Success           color.Color
    Warning           color.Color
    Error             color.Color
}
```

### Applying theme colors

```go
func (a *App) onPanel(fg color.Color, bold bool) lipgloss.Style {
    s := lipgloss.NewStyle().Foreground(fg).Background(a.theme.BackgroundPanel)
    if bold { s = s.Bold(true) }
    return s
}
```

### Tint — alpha blending

```go
// Linear blend: base at alpha 0, overlay at alpha 1
mixed := theme.Tint(baseColor, overlayColor, 0.5)
```

Terminal cells have no real alpha channel. Use `Tint` to pre-compute the
blended color against the surface the element sits on.

### Transparent backgrounds

```go
if _, transparent := theme.Background.(lipgloss.NoColor); !transparent {
    v.BackgroundColor = theme.Background
}
```

`lipgloss.NoColor` marks a theme that deliberately leaves the background
transparent. Do not force a background color on such themes.

---

## Markdown rendering with Glamour v2

```go
import "charm.land/glamour/v2"

func (a *App) markdownRenderer(width int) *glamour.TermRenderer {
    r, _ := glamour.NewTermRenderer(
        glamour.WithStyles(glamourStyleConfig(a.theme)),
        glamour.WithWordWrap(width),
        // Truecolor to match every other truecolor hex in the app —
        // chroma's default downsamples to 256-color otherwise.
        glamour.WithChromaFormatter("terminal16m"),
    )
    return r
}
```

Build `ansi.StyleConfig` from your theme colors so markdown blends into the
rest of the UI — including custom light themes that have no bundled glamour
equivalent. Cache the renderer per width+theme; rebuilding it every frame
is expensive.

---

## Bubbles v2 components

```go
import "charm.land/bubbles/v2/textarea"
import "charm.land/bubbles/v2/viewport"
import "charm.land/bubbles/v2/list"
import "charm.land/bubbles/v2/spinner"
```

### Textarea

```go
a.input = textarea.New()
a.input.Focus()

// In Update, delegate key events to the textarea:
var cmd tea.Cmd
a.input, cmd = a.input.Update(msg)

// Insert text programmatically:
a.input.InsertString("\n")

// Read value:
text := a.input.Value()
```

### Prompt sizing pattern (the expand-before-key trick)

When a prompt textarea grows as the user types, expand it to max height
**before** the textarea sees the key, then trim back to content height:

```go
func (a *App) Update(msg tea.Msg) tea.Cmd {
    a.expandPromptForInput()  // grow to max FIRST
    cmd := a.update(msg)       // textarea handles the key
    a.syncPromptSize()         // trim back to content
    return cmd
}
```

If the box is still one row tall when the key lands, the textarea scrolls
its viewport to keep the cursor visible — and that scroll **hides the first
line**. Growing first means there is always room, so no scroll ever
happens. Height is capped at `max(6, height/3)`.

### Viewport / scrolling

```go
func (a *App) scrollMessages(delta int) tea.Cmd {
    a.scrollOffset += delta
    if max := a.maxScrollOffset(); a.scrollOffset > max {
        a.scrollOffset = max
    }
    if a.scrollOffset < 0 { a.scrollOffset = 0 }
    return nil
}
```

---

## Spinners

Drive multiple spinners off a single shared tick loop at the finest rate:

```go
const spinnerTick = 40 * time.Millisecond

type spinnerTickMsg struct{}

func (a *App) startSpinner() tea.Cmd {
    if a.spinning { return nil }
    a.spinning = true
    return tea.Tick(spinnerTick, func(time.Time) tea.Msg {
        return spinnerTickMsg{}
    })
}

// In Update:
case spinnerTickMsg:
    a.spinning = false
    if a.busy {
        a.spinnerFrame++
        return a.startSpinner()
    }
    return nil
```

For a second spinner at a coarser rate, advance it every N ticks:

```go
const spinnerBrailleEvery = 2  // 80ms off a 40ms loop
glyph := spinnerFrames[(a.spinnerFrame/spinnerBrailleEvery)%len(spinnerFrames)]
```

The spinner placeholder must be exactly one cell wide (use U+E000, private
use) so the block's width math runs correctly while the placeholder is in
place, then substitute the real glyph on the way out.

---

## Dialog overlays

A dialog owns the keyboard while open (modal mode):

```go
func (a *App) handleKey(msg tea.KeyMsg) tea.Cmd {
    if a.overlay != nil {
        return a.handleOverlayKey(msg.String())
    }
    // ...normal key handling...
}
```

### Overlay structure

```go
type overlay struct {
    kind     int
    title    string
    items    []overlayItem
    selected int
}
```

### Arm-then-confirm delete pattern

For destructive actions (session delete, memory delete), require two
presses — first to arm (shows "Press again to confirm"), second to commit.
Memories are permanent by design, so an accidental keystroke should not be
able to end one.

### Opening dialogs from the background

Open the dialog from cached data, then refresh in the background — so it
is never left staring at a stale "Loading":

```go
func (a *App) modelsOverlay() tea.Cmd {
    a.openModelDialog(a.modelList)
    return a.loadCatalogCmd()
}
```

---

## Autocomplete popup

An autocomplete popup is **not a dialog** — it is an inline box anchored
directly above the prompt, with no separate filter field. What you type
keeps going into the prompt, and the list narrows against the text after
the trigger.

```go
// "/" opens command completion at position 0 only:
if msg.String() == "/" && a.input.Value() == "" {
    a.input.InsertString("/")
    a.openSlashAutocomplete()
    return nil
}

// "@" opens mention completion:
if msg.String() == "@" {
    a.input.InsertString("@")
    a.openMentionAutocomplete()
    return nil
}

// Re-filter after every keystroke (falls through to textarea):
a.input, cmd = a.input.Update(msg)
a.syncAutocomplete()
```

The popup takes the navigation keys (up/down/enter/esc) while open;
everything else falls through to the editor so typing keeps narrowing the
list.

---

## Rendering patterns

### Frame composition

Build the screen as a single string. The top-level `View()` composes
screens (home, chat), each of which builds a string from styled segments:

```go
func (a *App) View() string {
    switch a.view {
    case viewHome: return a.viewHome()
    case viewChat: return a.viewChat()
    }
    return ""
}
```

### Caching rendered frames

Rendering is expensive — markdown parsing, syntax highlighting, width
measurement (grapheme segmentation is the single most expensive thing in
the profile). Cache the rendered frame and only re-render when state
changes. Before a render cache, each keystroke re-parsed the whole visible
history (~84ms), which made a held key visibly lag.

### Cropping to terminal height

```go
func (a *App) frame(content string) string {
    lines := strings.Split(content, "\n")
    if a.height > 0 && len(lines) > a.height {
        lines = lines[:a.height]
    }
    for i, line := range lines {
        lines[i] = " " + line + " "  // 1-column side margin
    }
    return strings.Join(lines, "\n")
}
```

Avoid `lipgloss.NewStyle().Padding(0,1).MaxHeight(h)` for this — it pays
to measure the display width of every line in a ~90KB fully-styled frame,
which is a third of the render budget. Manual string manipulation is far
cheaper.

### Sticky-bottom layout

A short conversation's messages (and the prompt/footer glued below it)
sit at the **bottom** of the column, with unused space above — not at the
top with unused space below. Pad with leading blank lines to reproduce
this anchor:

```go
pad := a.height - strings.Count(main, "\n") - 1
if pad > 0 {
    main = strings.Repeat("\n", pad) + main
}
```

---

## Testing TUI apps

### Headless program test

Boot the real Bubble Tea program without a TTY, render frames, and quit —
catches startup/render panics:

```go
func TestHeadlessProgramRun(t *testing.T) {
    app := New(context.Background(), client.New(server.URL), "gocode-dark")
    p := tea.NewProgram(program{app: app}, tea.WithInput(nil), tea.WithOutput(io.Discard))
    done := make(chan error, 1)
    go func() { _, err := p.Run(); done <- err }()
    deadline := time.After(5 * time.Second)
    for quit := false; !quit; {
        select {
        case err := <-done:
            if err != nil { t.Fatal(err) }
            quit = true
        case <-deadline:
            t.Fatal("program did not quit in time")
        default:
            p.Send(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
            time.Sleep(10 * time.Millisecond)
        }
    }
}
```

### Drive the update path (like a user)

```go
// drive feeds a message into Update and executes returned commands,
// flattening batches, the way the bubbletea runtime would.
func drive(t *testing.T, app *App, msg tea.Msg) {
    t.Helper()
    runCmds(t, app, app.Update(msg), 0)
}

// press feeds one keypress through the app
func press(t *testing.T, app *App, key string) {
    t.Helper()
    r := []rune(key)[0]
    drive(t, app, tea.KeyPressMsg{Text: key, Code: r})
}
```

### Test helpers

```go
func newTestApp(t *testing.T, baseURL string) *App {
    t.Helper()
    t.Setenv("XDG_STATE_HOME", filepath.Join(t.TempDir(), "state"))
    app := New(context.Background(), client.New(baseURL), "gocode-dark")
    app.width, app.height = 100, 30
    return app
}

func typingApp(t *testing.T, width, height int) *App {
    t.Helper()
    app := newTestApp(t, "http://example.invalid")
    app.width, app.height = width, height
    app.view = viewChat
    app.active = &client.Session{ID: "ses_1", Title: "Test"}
    app.Update(tea.WindowSizeMsg{Width: width, Height: height})
    return app
}
```

### The "drive the real path" lesson

Tests that use `SetValue` to construct state skip the component's own key
handling — and the scroll that handling performs was a real bug. A test
that builds state directly instead of driving the real input path can pass
exhaustively against broken code. **Always drive `Update` with real
messages.**

### Benchmarks

```go
func BenchmarkKeypress(b *testing.B) {
    app := benchApp(b, 60)
    app.View() // warm the cache, as a running session's would be
    b.ResetTimer()
    for range b.N {
        app.Update(tea.KeyPressMsg{Text: "x", Code: 'x'})
        _ = app.View()
    }
}
```

A keystroke is an `Update` plus the `View` that follows it; a key held
down repeats this ~30 times a second.

---

## Debugging

### Logging

You cannot log to stdout — your TUI is occupying it. Log to a file:

```go
if len(os.Getenv("DEBUG")) > 0 {
    f, err := tea.LogToFile("debug.log", "debug")
    if err != nil {
        fmt.Println("fatal:", err)
        os.Exit(1)
    }
    defer f.Close()
}
```

Watch with `tail -f debug.log` while the program runs in another terminal.

### Delve

Since Bubble Tea apps control stdin/stdout, run delve in headless mode:

```bash
dlv debug --headless --api-version=2 --listen=127.0.0.1:43000 .
# Connect from another terminal:
dlv connect 127.0.0.1:43000
```

---

## v1 → v2 migration checklist

1. **Update import paths**: `github.com/charmbracelet/bubbletea` → `charm.land/bubbletea/v2`; `github.com/charmbracelet/lipgloss` → `charm.land/lipgloss/v2`
2. **Change `View() string` → `View() tea.View`**: use `tea.NewView(s)` or `v.SetContent(s)`
3. **Replace `tea.KeyMsg` with `tea.KeyPressMsg`** for key presses
4. **Update key fields**: `msg.Type` → `msg.Code`, `msg.Runes` → `msg.Text`, `msg.Alt` → `msg.Mod`
5. **Space bar**: `case " ":` → `case "space":`
6. **Mouse messages**: `tea.MouseMsg` is now an interface; use `msg.Mouse()` for coordinates
7. **Mouse button constants**: `tea.MouseButtonLeft` → `tea.MouseLeft`, etc.
8. **Remove program options**: `tea.WithAltScreen()` → `view.AltScreen = true`, etc.
9. **Remove imperative commands**: `tea.EnterAltScreen` → `view.AltScreen = true`, etc.
10. **Rename**: `tea.Sequentially(...)` → `tea.Sequence(...)`, `tea.WindowSize()` → `tea.RequestWindowSize`
11. **Paste**: `tea.KeyMsg` with `Paste` flag → `tea.PasteMsg{Content}`
12. **Removed methods**: `p.Start()` / `p.StartReturningModel()` → `p.Run()`

---

## Companion libraries

| Library | Import | Purpose |
|---|---|---|
| [Bubbles](https://github.com/charmbracelet/bubbles) | `charm.land/bubbles/v2/...` | UI components: textarea, viewport, list, spinner, textinput, table, paginator |
| [Lip Gloss](https://github.com/charmbracelet/lipgloss) | `charm.land/lipgloss/v2` | Style, layout, borders, color |
| [Glamour](https://github.com/charmbracelet/glamour) | `charm.land/glamour/v2` | Markdown rendering with syntax highlighting |
| [Harmonica](https://github.com/charmbracelet/harmonica) | — | Spring animation for smooth motion |
| [x/ansi](https://github.com/charmbracelet/x) | `github.com/charmbracelet/x/ansi` | ANSI escape sequence utilities (slicing styled strings without corrupting them) |

---

## Common pitfalls

1. **Blocking in Update**: Anything slow must be a `tea.Cmd`. Blocking freezes the UI.
2. **Missing PasteMsg case**: If `tea.PasteMsg` falls through the switch, paste text is silently dropped.
3. **Per-line lipgloss padding**: Multi-line `Style.Render` pads to the longest line; only colored when `Background` is set. Render line-by-line when embedding in another style's background.
4. **Prompt sizing**: Expand textarea to max **before** the key lands, then trim. Otherwise the textarea scrolls and hides the first line.
5. **Dropped frames, not back-pressure**: A slow terminal degrades by dropping snapshots from a buffered channel, never by stalling the event source.
6. **Test by driving Update, not by setting state**: Tests that skip the real input path can pass against broken code.
7. **Transparent themes**: Do not force `BackgroundColor` on a theme using `lipgloss.NoColor`.
8. **AltScreen/MouseMode in View, not NewProgram**: v2 moved these from program options to declarative View fields.
9. **Dialog work in Cmd, not Update**: Opening a dialog that fetches data should start the fetch as a `tea.Cmd` — doing the work inside `Update` causes a visible delay on Ctrl-key combinations.
10. **Failed refresh must not blank a cached list**: Keep whatever was already cached; the dialog reads `catalogErr` to explain itself instead of showing an endless "Loading".
