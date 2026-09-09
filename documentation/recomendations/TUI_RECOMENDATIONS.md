# TUI Recommendations

[← Design recommendations](README.md) · [Documentation index](../README.md) · [The TUI (architecture)](../06-tui.md)

---

A design specification for `internal/tui`: how the interface should look, how
views and dialogs are laid out, how controls are built, how themes work, and
what to do (and never do) when adding something new.

This document is **normative**. Where it describes current behaviour it is
because that behaviour is the reference; where it says *should*, it is a rule
for new work. It complements [chapter 6, The TUI](../06-tui.md) (which explains the
architecture) by specifying the *visual and interaction contract*.

---

## Table of contents

1. [The rendering model](#1-the-rendering-model)
2. [Design principles](#2-design-principles)
3. [Theme system](#3-theme-system)
4. [Geometry system](#4-geometry-system)
5. [Glyph vocabulary](#5-glyph-vocabulary)
6. [View layouts](#6-view-layouts)
7. [The timeline: message block specs](#7-the-timeline-message-block-specs)
8. [Prompt, footer and banners](#8-prompt-footer-and-banners)
9. [Dialogs](#9-dialogs)
10. [Controls catalogue](#10-controls-catalogue)
11. [Non-dialog overlays](#11-non-dialog-overlays)
12. [Keyboard model](#12-keyboard-model)
13. [Mouse and selection model](#13-mouse-and-selection-model)
14. [Responsiveness](#14-responsiveness)
15. [Motion and timing](#15-motion-and-timing)
16. [Performance rules](#16-performance-rules)
17. [Terminal compatibility and accessibility](#17-terminal-compatibility-and-accessibility)
18. [Anti-patterns](#18-anti-patterns)
19. [Checklists](#19-checklists)
20. [File map](#20-file-map)

---

## 1. The rendering model

The TUI is **Bubble Tea v2** (`charm.land/bubbletea/v2`) with **lipgloss v2**
for styling, **glamour v2** for markdown and **chroma** for syntax highlighting.
There is no widget tree, no layout engine, no flexbox. Every frame is a single
string built from styled segments and then cropped.

```
App.Update(msg) ──▶ mutate App state, return tea.Cmd
App.View()      ──▶ currentFrame() ──▶ one string
                     ├─ viewHome() | viewChat()      (route)
                     ├─ compositeToast(base)         (toast splice)
                     ├─ viewOverlay()                (canvas composite: dim + dialog layer)
                     └─ applySelectionHighlight()    (drag selection)
program.View()  ──▶ tea.View{AltScreen, MouseModeAllMotion, BackgroundColor…}
```

### Rules that follow from this

| Rule | Why |
|---|---|
| `Update` must never block | The key handler runs on the same goroutine; a 100 ms HTTP call is 100 ms of dead keyboard. Return a `tea.Cmd`. |
| `View` must be cheap and side-effect-light | It runs every frame. The only writes allowed are **layout caches** the mouse handler needs (`chatWindowStart`, `chatWindowPad`, `chatReasoningRows`, `linkHits`, `overlayHits`). |
| Modal composition is a **canvas composite** | `compositeDialog()` parses the base into a cell buffer, dims each cell, and layers the panel with lipgloss's `Compositor` — one pass. Non-modal overlays (toast, narrow sidebar) splice by *display cell* instead: `spliceAt()` / `sliceCells()`, never `line[a:b]`. |
| Anything absolutely positioned must record its hit-test spans | Dialogs return `*overlayHits`; toasts return a `linkHit`. A clickable thing with no recorded span is a bug. |
| Frames are cropped, never scrolled by the terminal | `frame()` truncates to `a.height`. If a block overflows its row budget, the *bottom* is lost — which is where the buttons are. Budget first, render second. |

### Where lipgloss primitives are used — and where they deliberately are not

Use lipgloss's layout primitives wherever they are equivalent:

- `lipgloss.PlaceHorizontal(w, lipgloss.Center, block)` for centering (not
  hand-rolled prefix padding).
- `lipgloss.PlaceHorizontal(w, lipgloss.Right, x)` for right alignment.
- `JoinHorizontal` / `JoinVertical` for side-by-side and stacked blocks.
- `Style.Width/Height/Padding` for boxes whose width you actually want padded.

Two patterns must stay manual, both for **measured** performance reasons
(`frame_bench_test.go` keeps the numbers current):

- **`frame()`** — the full-frame crop + side margins. `Style.Padding(0,1).MaxHeight(h)` measures the display width of every line of a ~90 KB styled frame: 18 ms against the manual form's 0.12 ms.
- **Sticky-bottom / home centering pads** — `PlaceVertical` emits space-filled
  filler lines where the manual form emits bare `\n`; same layout, more bytes,
  no gain.

lipgloss has **no space-between primitive**. `splitRow(width, left, right, minGap)` in `components.go` names that pattern once — do not re-derive the gap math at call sites.

### Border-box arithmetic

lipgloss v2's `Width()` is **true border-box**: the declared value is the total
rendered width, borders and padding included. Every single-left-border panel in
this codebase is declared as `Width(withLeftBorder(contentAndPadding))`, where
`withLeftBorder(n) = n + 1` adds the `┃` column back.

**Rule:** never pass a raw content width to `Style.Width()` on a bordered box.
Always go through `withLeftBorder()` (or the `splitBorderPanel` /
`splitBorderPanelCustom` helpers), and state the intended *total* in a comment.

### Multi-line rendering

`Style.Render()` on multi-line content pads every line to the block's longest
line — but only *colors* that padding when the style itself sets a background.
A foreground-only style leaves bare uncolored spaces that punch through the
enclosing panel's fill.

**Rule:** when text is embedded inside a panel's fill, render it with the
`onPanel…` helpers (`onPanelText`, `onPanelMuted`), which set the panel
background and render line by line. Raw `renderLines` is for cases where the
enclosing background genuinely is not a panel.

---

## 2. Design principles

1. **One visual language, three surfaces.** Everything is either the *page*
   (`Background`), a *panel* (`BackgroundPanel`) or an *element*
   (`BackgroundElement`). Nothing invents a fourth surface.
2. **Left-border accents instead of full boxes.** The house style is a `┃` bar
   in a semantic color down the left edge of a filled panel — not a four-sided
   frame. Full borders are visual noise at terminal density and cost two rows
   and two columns. The **only** two-sided exception is the toast.
3. **Color carries meaning, glyphs carry redundancy.** Every state that is
   signalled by color also has a glyph or a word (`△` for permission, `●` for
   current, `✓` for enabled, `· interrupted` in text). Never color-only.
4. **Say the key.** Any interactive affordance names its keybind next to it, in
   `Text` for the key and `TextMuted` for the label: `esc interrupt`,
   `ctrl+p commands`, `⇆ select`.
5. **Degrade by dropping, not by wrapping.** When a row does not fit, remove
   the lowest-priority segment; do not let it wrap or overflow into the sidebar.
   Only the ask-banner button bars fall back to a second row.
6. **Reserve the bottom.** The prompt box, its meta row and the hint row are
   pinned and must always be reachable. Everything above them is the flexible
   region.
7. **Dialogs sit balanced on screen.** A dialog is a thing floating over the
   page, so it must read as floating: anchored at `height/4`, with a margin
   below to match, and **never grown down to the last screen row**. A panel
   that reaches the bottom edge reads as a docked column, not a mode — the
   backdrop scrim stops doing its job when there is no page left visible
   underneath. See §4.5 for the arithmetic.
8. **Truncate the head, keep the tail** for paths (`…/project/dir`); truncate
   the tail for prose and labels (`long title…`).
9. **Empty states explain, they do not just say "empty".** Distinguish
   *loading*, *failed*, *disabled* and *genuinely nothing* — three of those look
   identical to a user and only one is worth waiting on.

---

## 3. Theme system

### 3.1 Semantic tokens

`theme.Colors` is the complete palette. **Never write a hex literal outside
`internal/tui/theme`.** Every color used in a render must come from
`a.theme.<Token>` or be derived from one with `theme.Tint`/`theme.FadeColor`.

| Token | Use it for |
|---|---|
| `Primary` | The agent accent. Prompt border (idle), current-item bullet, selected list row fill, focused action, question banner accent, `▣` settlement icon, logo highlight. |
| `Secondary` | File-attachment pill badges. Reserve for secondary categorisation. |
| `Accent` | Category headers inside list dialogs. |
| `Info` | Informational toast. |
| `Warning` | Permission banner, prompt border while busy, reasoning header, in-progress todo. |
| `Error` | Errors, failed MCP/plugin dots, armed destructive row fill, empty-state failure titles. |
| `Success` | Connected MCP/LSP dots, `✓` gutters, success toast. |
| `Text` | Primary foreground, key names in hints, titles. |
| `TextMuted` | Descriptions, labels in hints, timestamps, secondary metadata. |
| `Background` | The page. Also the foreground of text sitting on a `Warning`/`Primary` fill. |
| `BackgroundPanel` | Dialogs, sidebar, message blocks, toasts, subagent footer. |
| `BackgroundElement` | The prompt box, option-bar strips, unselected buttons, getting-started card, autocomplete fallback. |
| `BackgroundMenu` | The autocomplete popup surface (falls back to `BackgroundElement`). |
| `Border` | Autocomplete and subagent-footer accent bars. |
| `BorderActive` | The compaction rule. |
| `BorderSubtle` | Reserved; currently unused — prefer it over inventing a new token. |
| `SelectedListItemText` | Foreground on a `Primary` fill. Falls back to `Background` via `Normalize()`. |
| `ThinkingOpacity` | Alpha (default `0.6`) that reasoning headers and bodies fade to. |

**Rule:** if a new surface needs a color, map it onto an existing token. Adding
a token means touching 33 bundled theme assets and the `Normalize()` fallback
chain — do it only with a fallback that leaves existing themes unchanged.

### 3.2 There is no alpha channel

Terminal cells cannot composite. Two helpers stand in for alpha:

- `theme.Tint(base, overlay, alpha)` — linear RGB interpolation.
- `theme.FadeColor(background, c, alpha)` — `Tint(background, c, alpha)`, i.e.
  "this color, faded toward the surface it sits on".

**Rule:** a fade must name the surface it is fading into. `FadeColor(bg, fg, α)`
where `bg` is the *actual* background of that cell — using `theme.Background`
for text that sits on `BackgroundElement` produces a visibly wrong color.

### 3.3 Backdrop dimming

`composite.go` implements the modal scrim: the rendered frame is parsed into a
lipgloss **Canvas** (a cell buffer), every cell's foreground and background are
blended toward black at `150/255 ≈ 59%`, and the dialog panel is drawn on top
as a `Layer` through the `Compositor` — one pass over cells instead of a
string rewrite followed by a cell-splice.

Cells with no explicit color inherit terminal defaults that cannot be read
back, so a nil color resolves to the theme's own colors, pre-blended.

**Rule:** any new full-screen modal layer must reuse `compositeDialog()`. Do
not invent a second dimming strategy, and do not skip dimming — an undimmed
backdrop makes a dialog read as a panel rather than a mode. Non-modal
overlays (the toast, the narrow-terminal sidebar) deliberately use
`spliceAt()` instead: they do not dim, and for a small panel over an
unmodified base the string splice is ~3x cheaper than parsing the frame into
cells.

### 3.4 Theme catalog and selection

- 33 bundled JSON palettes in `theme/assets/`, each registered twice as
  `<id>-dark` and `<id>-light`.
- `theme.Resolve(name)` accepts `dark`/`light`/`gocode-dark`/`gocode-light` plus
  any catalog name; anything unknown falls back to dark.
- The theme picker previews live as the selection moves and **reverts on
  cancel** (`overlay.onCancel`).

**Rules:**
- Every theme reassignment goes through `a.setTheme(t)`, never a bare
  `a.theme = t`. Derived state (the textarea's baked-in `Styles`) is re-synced
  there; a bare assignment leaves the prompt box rendering the old palette.
- `program.View()` sets terminal default fg/bg via OSC so cells no component
  touches still match the theme — **except** when `Background` is
  `lipgloss.NoColor` (a deliberately transparent theme). Preserve that check.
- New themes are JSON assets, not Go code.

---

## 4. Geometry system

All dimensions in cells. `a.width`/`a.height` are the terminal size.

### 4.1 Global

| Quantity | Value | Notes |
|---|---|---|
| Screen side margin | `1` each side | `frame()` prefixes/suffixes a space; it does **not** use `Padding()` (measuring every line of a ~90 KB frame is a third of the render budget). |
| Vertical crop | `a.height` | `frame()` truncates extra lines. |
| Sidebar width | `42` | Reserved whenever the sidebar is visible and a session is open, docked or not. |
| Wide breakpoint | `a.width > 120` | Above: sidebar docks as a column. At or below: sidebar overlays as a right drawer. |
| `chatWidth()` | `width − sidebarWidth − 4`, min 20 | |
| `contentWidth()` | `chatWidth()`, min 20 | The message column width. |
| `sessionPromptBoxWidth()` | `chatWidth() − 2` | +1 border ⇒ total `chatWidth()−1`. |

### 4.2 The `chatWidth()−1` invariant

Every bordered timeline panel — user block, error block, block-tool, prompt box,
subagent footer — renders to a **total of `chatWidth()−1`**, matching the
maximum reach of an assistant text block (`indent(3)` + markdown wrap
`contentWidth()−4`).

**Rule:** new timeline panels are sized `Width(withLeftBorder(contentWidth()−2))`.
Do not widen a panel to fill the column; markdown wrap decisions run on *raw
source* width (`**bold**` is 8 source columns for 4 rendered) and need that spare
margin column.

### 4.3 Vertical budget for the chat view

```
viewportHeight = height − promptContentHeight() − 6
                        − (askBannerHeight() − 1)   when a banner is showing
                        − autocompletePopupHeight() when the popup is open
                 clamped to ≥ 3
```

Notes that are easy to get wrong:

- Use `promptContentHeight()`, **not** `input.Height()` — the editor is
  transiently inflated to max while a key is handled.
- The ask banner replaces the single blank separator row its slot always
  occupies, hence `− 1`.
- The autocomplete popup has no reserved slot; its rows come entirely out of
  this budget.
- Once scrolled, the `↑ N more lines` indicator costs one row **from the same
  budget** — it must not be added on top.

### 4.4 Prompt sizing

- `promptMaxHeight() = max(6, height/3)`.
- The editor grows with content between 1 and that max.
- `expandPromptForInput()` runs **before** `input.Update(msg)`; `syncPromptSize()`
  runs after. The textarea only ever scrolls *toward* the cursor and never back,
  so growing first is what keeps the first line visible.
- Measure at `input.Width()`, not `inputWidth()` — they differ by the prompt
  column, and measuring at the wider value makes the box one row short.

### 4.5 Dialog geometry

| Quantity | Value |
|---|---|
| Panel widths | `dialogMedium = 60`, `dialogLarge = 88`, `dialogXLarge = 116` |
| Width clamp | `min(size, a.width − 2)` |
| Origin | `top = a.height/4`, `left = (a.width − panelW)/2` |
| Panel chrome | `PaddingTop(1)`, `BackgroundPanel`, **no border** |
| Vertical extent (any growing dialog) | **≤ `a.height − 2·(a.height/4)` rows**, so the margin below equals the `height/4` offset above |
| List viewport | `max(3, a.height/2 − 6)` rows |
| Read-only scroll panel body | `max(1, min(height − 2·(height/4) − chrome, height − (height/4) − chrome))` rows |
| Header pad | `4` for select dialogs; `2` for prompt/help/status/stats/alert/confirm |
| Row gutter | `6` columns before the title; `3` columns right padding |
| Title truncation | `61` runes (`dialogTitleWidth`), applied **before** layout |
| Action row | `padLeft 4`, `padRight 2`, `gap 2` between actions |

#### Balance

Every dialog is anchored at `top = height/4`. What happens below that depends on
whether the panel grows:

- **Growing dialogs** (lists, read-only scroll panels) must cap their growth so
  the margin below matches the offset above. The list viewport cap already does
  this exactly: `height/2 − 6` body rows plus 8 rows of chrome lands the panel at
  precisely `height/2` rows, leaving `height/4` above and `height/4` below.
  Measured at width 120: h=24 → 6 above / 6 below, h=40 → 10/10, h=60 → 15/15,
  h=80 → 20/20. A read-only scroll panel computes the same bound explicitly.
- **Fixed-size dialogs** (alert, confirm, input, help — 8 rows) stay at
  `height/4` and simply sit high, with the rest of the gap below them. That is
  deliberate optical centering: a small panel placed at true vertical centre
  reads as sagging. **Do not vertically centre them.**

The screen fit is the hard bound and the bottom margin is the preference, so on
a terminal too short for both, the margin is what gives way — never the fit, and
never the hint row that names the way out.

---

## 5. Glyph vocabulary

Use these; do not introduce synonyms. All are single-cell except where noted.

| Glyph | Meaning | Where |
|---|---|---|
| `┃` | Panel accent bar (left; both sides only on toasts) | every panel |
| `╹` | Prompt box bottom-left corner (home only) | home prompt |
| `▀` | Prompt shadow rule (home) | home prompt |
| `●` | Current/selected item marker | list dialogs, in-progress todo |
| `○` / `●` | Unticked / ticked multi-select option | question banner |
| `✓` | Enabled / completed | plugin & provider gutters, completed todo, completed task tool |
| `•` | Status dot (colored by state) | MCP, LSP, plugins, version line |
| `⊙` | MCP indicator | home status bar |
| `△` | Permission required | permission banner |
| `?` | Question | question banner |
| `▣` | Assistant settlement line | timeline |
| `⬖` | Card marker | getting-started card |
| `✕` | Dismiss | getting-started card |
| `$` | Shell command | bash block |
| `→` / `←` | Read / Write | file tool blocks |
| `⚙` | Generic tool | fallback tool row |
| `~` | Pending tool | tool row |
| `⇆` | Move selection | banner hints |
| `↑ N more lines` | Scrollback indicator | timeline |
| `─` + ` Compaction ` | Compaction rule | timeline |
| `▸` / `▾` | Collapsed / expanded directory | diff viewer file tree |
| `│  ` / `   `, `├─ ` / `└─ ` | Tree connectors (indent run, branch) | diff viewer file tree |
| `✓` / `A` / `M` / `D` / `?` | Reviewed / added / modified / deleted / unknown file status | diff viewer file tree status column |
| `+ ` / `- ` | Collapsed / expanded reasoning | reasoning header |
| `⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏` | Inline braille spinner | running tool rows |
| `■` / `⬝` | Scanner spinner active/inactive cell | prompt hint row |
| `…` | Truncation | everywhere |

**Rules:** no emoji (width is unreliable across terminals). No box-drawing
corners except the two named above. Any new glyph must be BMP and single-width.

---

## 6. View layouts

### 6.1 Home view

Logo, prompt and tip centered as a block biased slightly upward; status bar
pinned to the bottom.

```
┌ terminal ───────────────────────────────────────────────────────────┐
│                                                                     │
│                     ██▀▀▀ █▀▀█   █▀▀▀ █▀▀█ █▀▀█ █▀▀█                │  logo: "Go" muted,
│                     █_^█  █__█   █___ █__█ █__█ █^^^                │  "Code" text+bold
│                     ▀▀▀▀  ▀▀▀▀   ▀▀▀▀ ▀▀▀▀ ▀▀▀▀ ▀▀▀▀                │
│                                                                     │
│              ┃                                                      │  autocomplete popup
│              ┃  ▸ /models   Switch model                            │  (only when open)
│              ┃                                                      │
│              ┃                                                      │  prompt box:
│              ┃  Ask anything…                                       │  BackgroundElement
│              ┃                                                      │  PaddingTop 1, L/R 2
│              ┃  Build · claude-sonnet-4-5 anthropic                 │  meta row
│              ╹▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀         │  corner + shadow
│                 tab agents  ctrl+p commands                         │  hints
│                                                                     │
│                 Press ctrl+p to see all available actions           │  tip (leader+h hides)
│                                                                     │
│ ~/Work/project:main  ⊙ 2 MCP /status                        v0.1.0  │  status bar
└─────────────────────────────────────────────────────────────────────┘
```

- Prompt width: `min(75, a.width − 6)`, min 20.
- Vertical placement: `top = available/2 + 2`, biased upward.
- The tip row keeps its slot when hidden so the logo does not jump.
- Status bar: abbreviated directory + branch (muted), MCP dot + connected count
  + `/status` hint (shown whenever any server is *configured*; count is
  *connected*; dot is error if any failed, success if any connected, else muted),
  version right-aligned.

### 6.2 Session view — wide (`width > 120`)

```
┌ terminal ─────────────────────────────────────────────────────────────────────────────┐
│                                                                                       │
│   (blank padding — the timeline is bottom-anchored)          │ Session title          │
│                                                              │                        │
│  ┃                                                           │ Context                │
│  ┃  Refactor the runner loop                                 │ 84,120 tokens          │
│  ┃                                                           │ 42% used               │
│  ┃  ▐ File ▌ runner.go                                       │ $0.34 spent            │
│  ┃                                                           │                        │
│                                                              │ MCP                    │
│     + Thought: planning the change · 4s · ~820 tokens        │ • github connected     │
│                                                              │ • linear failed        │
│     I'll start by reading the runner…                        │                        │
│                                                              │ LSP                    │
│     → Read internal/session/runner.go  (412 lines)           │ • gopls ~/Work/project │
│                                                              │                        │
│  ┃                                                           │ Plugins                │
│  ┃  $ go test ./internal/session/                            │ • rag  native · ready  │
│  ┃                                                           │                        │
│  ┃  ok  internal/session  1.4s (+18 lines — click to expand) │ Todo                   │
│                                                              │ [✓] Read the runner    │
│     ▣  Build · claude-sonnet-4-5 · 12s                       │ [•] Extract the loop   │
│                                                              │                        │
│                                                              │ ⬖ Getting started      │
│  ┃                                                           │   GoCode includes …    │
│  ┃  ▸ /compact   Compact session                             │   Connect provider     │
│  ┃                                                           │              /connect  │
│  ┃  ┃                                                        │                        │
│  ┃  ┃                                                        │ ~/Work/gocode:main     │
│  ┃  ┃  Now extract the loop                                  │ • GoCode 0.1.0         │
│  ┃  ┃                                                        │                        │
│  ┃  ┃  Build · claude-sonnet-4-5 anthropic                   │                        │
│   ■⬝⬝⬝⬝⬝⬝⬝ esc interrupt            159.6K (16%) · $0.34  ctrl+p commands│         │
└───────────────────────────────────────────────────────────────────────────────────────┘
```

Column order, top to bottom, in `viewChat()`:

1. Timeline window (bottom-anchored with leading blank padding).
2. Blank separator row.
3. Ask banner slot (permission or question; empty = the blank row).
4. Subagent footer (only for a child session; appended, so a root session's row
   budget is unchanged).
5. Autocomplete popup (only when open).
6. Prompt box.
7. Hint row (`chatFooter`).

Every one of items 3–7 is passed through `indentBlock()` (one extra column of
`paddingLeft`, on top of `frame()`'s margin).

### 6.3 Session view — narrow (`width ≤ 120`)

The sidebar still reserves its 42 columns in `chatWidth()`, but renders as a
**right-aligned full-height drawer spliced over** the chat via
`compositeSidebarOverlay()`. The chat underneath is not dimmed (a known
divergence — dimming it would require running the whole chat render through
`compositeDialog`'s dim pass, which the dialog layer already pays for and this one should not).

### 6.4 Sidebar

Fixed 42 columns, full terminal height, `BackgroundPanel`, `PaddingTop/Bottom 1`,
`PaddingLeft/Right 2`. Sections in fixed order:

| Order | Section | Empty state |
|---|---|---|
| — | Session title (bold, truncated to `width−6`) | — |
| 100 | **Context** — tokens, % used, cost | always shown |
| 200 | **MCP** — dot + name + status | section omitted when none configured |
| 300 | **LSP** | `Loading...` / `LSPs are disabled` / `No language servers found on PATH` / `LSPs will activate as files are read` |
| 350 | **Plugins** | `No plugins loaded` |
| 400 | **Todo** — `[✓]`/`[•]`/`[ ]` rows | omitted when no open todos |
| bottom | Getting-started card, path line, version line | card omitted once a paid provider exists or it is dismissed |

**Critical rule:** the sidebar's content rows must total **exactly `height − 2`**.
lipgloss `Height()` pads a short render but never truncates a tall one, so one
row too many silently makes the panel taller than its column and misaligns it
against the chat. Every entry is **one line** — truncate, never wrap. Paths
truncate from the left (`…/project:branch`); todos truncate to 32 chars; plugin
IDs to 24.

**Context** reports the *last assistant turn's own* context against the model's
limit — not a session total. Only "spent" is cumulative.

### 6.5 Diff viewer (`/diff`)

A **full-screen route**, not a dialog: it replaces the base view entirely,
owns the keyboard while open, and `q`/`esc` return to the view it opened
from. The TS source is
`packages/tui/src/feature-plugins/system/diff-viewer.tsx`; this section is
the contract for its port in `diffviewer.go`.

```
┌ terminal ──────────────────────────────────────────────────────────────────┐
│  Diff working tree                                              3 files   │ header
│                                                                             │
│  ┃ ▾ internal/       ✓M │  ← file tree (32 cols, ┃ in Border,             │ body
│  ┃ │  └─ tui         A  │     BackgroundPanel)   ← patch pane             │
│  ┃ │    ├─ app.go  +12 -3                       │                         │
│  ┃ │    │  12  12    I'll start by reading…                             │
│  ┃ │    │  12  12  - ...old line…                                        │
│  ┃ │    │  12  12  + ...new line…                                        │
│  ┃ │    │  @@ -40,3 +40,3 @@                                             │
│  ┃ ▴ docs           M  │                                                 │
│                                                                             │
│  tab focus file tree  n next file  ] next hunk  [ previous hunk  …        │ footer
└─────────────────────────────────────────────────────────────────────────────┘
```

**Geometry**

| Quantity | Value | Notes |
|---|---|---|
| Tree width | `32` | TS `FILE_TREE_WIDTH`; hidden entirely when toggled off (`b`) or there are no files |
| Patch pane width | `width − (tree? 33 : 0) − 4` | TS `patchPaneWidth` |
| Split threshold | pane ≥ `100` | TS `MIN_SPLIT_WIDTH`; below it the view is unified regardless of the persisted choice |
| Body height | `height − 4` | header row + blank, footer row + blank |
| Tree status column | `2` cells, right-aligned | TS `FILE_TREE_STATUS_WIDTH` |

**Rules**

- **Three empty states, three messages** (principle 9): `Loading diff…`
  (muted), `Failed to load diff` (Error), `No diff!` (muted). The fetch
  opens the route synchronously and lands in the background — the keypress
  never waits on HTTP.
- **Diff rows truncate, never wrap.** A wrapped `-` row reads as another
  removal. Every row is `ansi.Truncate`d to the pane's interior; in split
  view each half truncates to half the room. (Same rule as §19.3's gutter
  rows.)
- **One layout pass serves render and navigation.** Hunk anchors (`[`/`]`)
  are computed by the same `buildDiffLayout()` the renderer runs, so a jump
  can never land somewhere the screen disagrees with.
- **Colors:** added `Success`, removed `Error`, context and hunk headers
  `TextMuted`, all on `BackgroundPanel`; a reviewed file mutes its header
  *and* rows to `TextMuted` (the TS viewer's reviewed treatment).
- **Tree rows:** highlight is a `Primary` row fill with `Background` text,
  edge to edge (§9.2); the selected file's name renders `Primary`; reviewed
  and directory names `TextMuted`; connectors fade toward the panel via
  `theme.FadeColor(BackgroundPanel, TextMuted, 0.75)`.
- **Windowing:** only rows in `[scroll, scroll+bodyHeight)` render (§16.5),
  with a muted `↑/↓ N more lines` row that comes **out of the body budget**.
- **Footer hints** drop from the end when the row does not fit (§8.2), and
  the `tab focus file tree` pair disappears while the tree is hidden.
- **Divergences** (see `documentation/10-development.md`): no "last turn"
  source (no snapshot system), no `diff_style: "stacked"` config.

**Keys** (all scoped to the route; a dialog opened inside it — `d`'s source
picker, `?`'s help — still wins per §12's ladder):

| Key | Action |
|---|---|
| `q`, `esc` | close, restoring the opening view |
| `j`/`k`, `up`/`down`, `pgup`/`pgdn` | move the focused pane (tree rows / pane rows) |
| `enter`, `space` | tree: open file or toggle directory |
| `right` / `left` | expand / collapse (with move-to-child / move-to-parent fallbacks) |
| `E` | expand all folders |
| `tab` | switch focus between tree and pane |
| `]` / `[` | next / previous hunk (sticky: `] ] [` returns exactly) |
| `n` / `p` | next / previous file in tree order |
| `m` | toggle reviewed on the focused file |
| `d` | switch source (working tree / main branch, gated on `/api/vcs`) |
| `v` | toggle split / unified (persisted; no-op below the threshold) |
| `s` | single-patch mode (persisted) |
| `b` | toggle the file tree (persisted) |
| `g` / `G` | top / bottom of the pane |
| `?` | shortcut sheet (the help overlay, extended with these rows) |

Preferences persist in `diffstate.json` beside `theme.json`, best-effort,
never touching the config file.

---

## 7. The timeline: message block specs

Blocks are separated by exactly one blank line. The timeline opens with one
blank spacer row. Only the last **60** messages render.

### 7.1 User message

```
┃
┃  Refactor the runner loop so the coordinator owns the queue
┃
┃  ▐ File ▌ runner.go   ▐ Directory ▌ internal/
┃
┃   QUEUED            ← or a muted timestamp when `timestamps` is on
```

`splitBorder()` left, `BorderForeground(Primary)`, `BackgroundPanel`,
`PaddingTop/Bottom 1`, `PaddingLeft 2`, `Width(withLeftBorder(contentWidth()−2))`.
Text wraps at `contentWidth()−4`. File pills: `Secondary`-background badge +
`BackgroundElement` name, wrapped without splitting a pill. The `QUEUED` badge
(bold, `Primary` bg, `SelectedListItemText` fg) replaces the timestamp.

### 7.2 Assistant text

Markdown via glamour, wrapped at `contentWidth()−4`, indented 3, preceded by one
blank line. No border, no panel — assistant prose is the page.

### 7.3 Reasoning

```
   + Thought: planning the approach · 4s · ~820 tokens      (collapsed)

   - Thought: planning the approach · 4s · ~820 tokens      (expanded)
     I need to look at how the coordinator serialises…
```

- Header color: `Warning`, faded to `ThinkingOpacity` once open.
- `+ `/`- ` prefix appears **only** in minimal thinking mode.
- Running: `<spinner> Thinking: <title> · ~N tokens` — the live token count is
  the only thing a collapsed live block shows, so it must be there.
- Body: rendered with the **dimmed** glamour palette (`markdownRendererDim`),
  wrapped at `contentWidth()−4−extraIndent`, indented `3 + extraIndent`
  (extra 2 in minimal mode).
- The header row is a click target; record it via `reasoningRef()`.

### 7.4 Tool rows (one-line form)

```
      ~ Reading internal/session/runner.go        pending  (Text, 6-space indent)
   ⠹ Running go test ./…                          running  (Muted, spinner for bash/read/task only)
   ✓ Task explore                                 done     (Muted)
   ⚙ mcp_github_search                            error    (Error)
```

Indent 3 (pending: 6), icon, space, label. Wrapped with `wrapToolLine`.

### 7.5 Block tools (bash / read / write)

An invisible-bordered `BackgroundPanel` block (border colored to the *page*
background so only the corner shape shows):

```
┃
┃  $ go test ./internal/session/
┃
┃  ok  internal/session  1.412s (+18 lines — click to expand)
┃
```

- Collapsed: first body line + `(+N lines — click to expand)` hint, the hint's
  width **reserved out of the first line's budget** so it never wraps onto a row
  outside the click target.
- Expanded: full body, and **every row of the block** becomes a collapse target.
- Body renderers: `wrappedBody` for shell output, `codeBody(path)` (chroma) for
  files, `markdownBody` for `.md`.
- `read` shows `→ Read <path>  (N lines)`; `write` shows `← Write <path>` and
  renders the **input content**, not the tool's one-line output.

### 7.6 Edit diff

Unified diff with a line-number gutter, capped at `maxRenderedDiffLines = 40`.

### 7.7 Compaction separator

A full-width `─` rule in `BorderActive` with a centered ` Compaction ` title.

### 7.8 Settlement line

```
   ▣  Build · claude-sonnet-4-5 · 12s · interrupted · stopped at the output limit
```

`▣` in `Primary` (muted once aborted), agent titlecased in `Text`, everything
else muted — except `stopped at the output limit`, which is `Error`. Rendered
when the message is last, final, or aborted.

**Rule:** an interruption is *not* an error block. Suppress the error panel and
let the settlement line's `· interrupted` marker carry it.

---

## 8. Prompt, footer and banners

### 8.1 Prompt box

```
┃
┃  Ask anything…
┃
┃  Build · claude-sonnet-4-5 anthropic
```

- Left `┃` in `Primary`, switching to `Warning` while busy. This is the single
  most important state signal in the interface.
- `BackgroundElement`, `PaddingTop 1`, `PaddingLeft/Right 2`.
- The editor's viewport pads rows with **plain unstyled spaces**; trim each
  line's trailing spaces and let `Width()` refill them, or the tint breaks.
- Meta row: agent (titlecased, `Primary`) `·` model (`Text`) provider (muted),
  every segment painting the box background explicitly, each fading in via
  `agentMetaFade` / `modelMetaFade`.

### 8.2 Hint row (`chatFooter`)

```
busy:      ■⬝⬝⬝⬝⬝⬝⬝ esc interrupt   ⇡42 tok/s  159.6K (16%) · $0.34  ctrl+p commands
armed:     ■⬝⬝⬝⬝⬝⬝⬝ esc again to interrupt          (both spans recolor to Primary)
offline:   ■⬝⬝⬝⬝⬝⬝⬝ waiting for network  esc interrupt
notice:       interrupted
idle:      ~/Work/project                          tab agents  ctrl+p commands
```

Right-hand segments are joined with `gap 2` and dropped **from the end** until
the row fits `chatWidth()`. Left is truncated only after every right segment is
gone.

### 8.3 Permission banner

```
┃
┃ △ Permission required
┃   ⚡ bash — rm -rf build/
┃
┃   rm -rf build/
┃   … 12 more lines
┃
   Allow once   Allow always   Reject          ⇆ select  enter confirm
```

- Panel: `splitBorder()` in `Warning`, `BackgroundPanel`, `PaddingLeft 1`,
  `PaddingRight 3`, total `contentWidth()−1`.
- Option bar: a separate `BackgroundElement` strip at `contentWidth()−1`,
  `PaddingTop/Bottom 1`, `PaddingLeft 2`, `PaddingRight 3`.
- Selected button: `Warning` fill, `Background` text. Unselected:
  `BackgroundElement`, `TextMuted`.
- **Height contract:** `permissionBudget() = min(15, height − input.Height() − 5)`.
  The bar's height, panel chrome (2), header rows (2) and the separator (1) come
  out first; the body gets what is left and is clamped with a
  `… N more lines` tail. The buttons must never be croppable.
- Narrow terminals stack the hints under the buttons instead of overflowing.

### 8.4 Question banner

Same slot and shape, keyed on **`Primary`** rather than `Warning` — a question is
a choice, not a risk. Adds `○`/`●` marks for multi-select, a `(n/N)` progress
suffix in the header when a request carries several questions, the wrapped
question body, and the **highlighted option's description** below it.

Hints: `⇆ select` · `space toggle` (multi only) · `enter confirm` · `esc skip`.

**Rule:** permission and question share one slot and never stack — a permission
is asked before its tool runs, so answering it is what lets a question happen.

### 8.5 Subagent footer

Shown above the prompt only for a child session, on `BackgroundPanel` with a
`Border`-colored `┃`:

```
┃
┃  Explore  (2 of 3)  159.6K (16%) · $0.34   Parent up  Prev left  Next right
┃
```

Stacks the navigation on a second row when too narrow.

---

## 9. Dialogs

### 9.1 What a dialog is

A dialog is a **centered, borderless `BackgroundPanel` block spliced over a
dimmed frame**, at `top = height/4`, sized so it sits balanced between that
offset and a matching margin below (§4.5). It owns the keyboard completely while
open.
There is exactly one `overlay` at a time (`a.overlay`), of one of eight kinds:

| Kind | Purpose | Constructor |
|---|---|---|
| `overlayList` | Pick one thing from a filtered, grouped list | `openList(title, items)` |
| `overlayInput` | Type one value | `openInput(title, placeholder, onSubmit)` |
| `overlayConfirm` | Two-button destructive/irreversible confirmation | `openConfirm(title, msg, cancelLabel, onConfirm, onCancel)` |
| `overlayAlert` | One-button acknowledgement | `openAlert(title, msg, onConfirm)` |
| `overlayHelp` | Static help paragraph + ok | `&overlay{kind: overlayHelp, title: "Help"}` |
| `overlayStatus` | Read-only MCP/formatter/plugin status | `&overlay{kind: overlayStatus, …}` |
| `overlayStats` | Read-only scrollable usage report | `openStatsOverlay()` |

**Rule:** do not add a ninth kind unless the interaction genuinely differs. A new
*screen* is almost always an `overlayList` with custom items, actions and an
empty view — that is how models, providers, agents, themes, sessions, skills,
plugins, memories, timeline and files are all built.

### 9.2 List dialog anatomy

```
        ← w = 60 / 88 / 116, clamped to a.width−2 →
┌──────────────────────────────────────────────────────────┐  ← PaddingTop(1)
│    Select model                                     esc  │  header: pad 4, title bold
│                                                          │
│    Search▏                                               │  filter: pad 4, block cursor
│                                                          │
│                                                          │
│       Favorites                                          │  category: Accent bold, col 4
│    ●  Claude Sonnet 4.5  fast, balanced          $3/$15  │  current row
│       Claude Opus 4.1    most capable            $15/$75 │
│                                                          │
│       Anthropic                                          │
│ ▓▓▓▓▓▓ Claude Haiku 4.5  cheapest                 $1/$5 ▓│  selected row (Primary fill)
│    ✓  GPT-5              connected                       │  gutter row
│                                                          │
│    connect ctrl+n  refresh ctrl+r          close esc     │  actions: pad 4/2, gap 2
│                                                          │
└──────────────────────────────────────────────────────────┘
```

**Row geometry** (`listRow`) — this is exact and has been wrong before:

```
col: 0  1  2  3  4  5  6 ................................ w-3  w
     ┌──┬──┬──┬─────┬───────────────────────────┬────────┬─────┐
     │pad│●│gap│ title paddingLeft 3            │ footer │ pad │
     └──┴──┴──┴─────┴───────────────────────────┴────────┴─────┘
      1  1  1      3                              1+wid     3
```

- A current row spends its first three cells on `␣●␣`; a plain row on three pad
  cells. **The title starts at column 6 either way** — the bullet occupies the
  gutter without shifting the title.
- The **background belongs to the row box**: a highlighted row is filled edge to
  edge including both paddings. Use the `fill(n)` helper, never bare spaces.
- The scrollbox itself pads 1 on each side *outside* the row box, so the
  highlight stops one column short of the panel edge and category headers land
  at column 4.
- Title and hint live in one clipped span — they are truncated *together*, the
  hint is never simply dropped.
- Title is `truncateEllipsis(label, 61)` **before** any layout, so a long title
  carries its ellipsis even in a wide dialog.

**Row colors:**

| State | Background | Title fg | Secondary fg |
|---|---|---|---|
| plain | `BackgroundPanel` | `Text` | `TextMuted` |
| current (`●`) | `BackgroundPanel` | `Primary` | `TextMuted` |
| selected | `Primary` | `SelectedListItemText` (bold) | `SelectedListItemText` |
| selected, action focused | `BackgroundElement` | `TextMuted` | `TextMuted` |
| armed (destructive) | `Error` | `SelectedListItemText` | — |

**Scrolling:** viewport `max(3, height/2 − 6)`. Arrow/page keys pass
`center = true` (recenter the selection); `home`/`end`/mouse hover pass `false`
(scroll the minimum needed).

### 9.3 Filter behaviour

The filter is **not a focusable field**. What you type goes into it directly;
there is no way to move focus to it and no cursor to place. Consequences:

- **Never bind `j`/`k` to movement.** They are characters to type. Use
  `up`/`down` and `ctrl+p`/`ctrl+n`.
- `typedText(key)` is the only way to turn a key name into a character — it
  handles `"space"` and multi-byte runes, which naive `len(key) == 1` drops.
- Cursor is a `Primary` block after the text, or resting on the placeholder's
  first character when empty.
- Suppress it with `hideFilter` only for lists that are never long.

### 9.4 Footer actions

`dialogAction{title, keys, right, standalone, onTrigger}`.

- Rendered as `Title` in `Text` + `keys` in `TextMuted`; focused actions invert
  to a `Primary` fill.
- `tab`/`shift+tab` cycle focus; while an action is focused the selected row
  dims and `enter` triggers the action instead of the item.
- `standalone: true` for actions that do not operate on the selected row (a
  "new" action must still work on an empty list).
- `right: true` places the action in the right-aligned group.
- Actions with a `keys` binding fire directly from that key without focusing.

### 9.5 States a list dialog must handle

| State | Field | Rendering |
|---|---|---|
| Loading | items empty, no error | set `emptyTitle`/`emptyBody` to say so |
| Failed | `emptyTitle` + `emptyBody`, `locked: true` | error title + muted body, no interaction but `esc` |
| Genuinely empty | `emptyTitle`/`emptyBody` | explain what would fill it |
| Default | — | `No results found` |

**Rule:** a dialog opens **synchronously from cache** and refreshes in the
background. Fetching first and opening after reads as the dialog lagging the
keypress (measured at 140 ms+ for the model dialog).

### 9.6 Input / alert / confirm

- **Input**: header (pad 2) + blank + value rows with a block cursor at the end
  + filler to a minimum of 4 rows + `enter submit  shift+enter newline`. Render
  the value **line by line** — a raw newline spliced into a composited row tears
  the panel.
- **Alert**: header + muted wrapped message + right-aligned `   ok   ` on a
  `Primary` fill (pad 3). `esc` and `enter` both resolve.
- **Confirm**: same, with `Cancel` and `Confirm` (pad 1), `left`/`right` to move,
  `Confirm` active initially. `esc` runs neither branch.

**Rule:** use `openConfirm` for anything irreversible reached from the command
palette. Inside a list, prefer the **two-press arm** (`armValue`/`armKeys`,
which turns the row `Error`-red and relabels it `Press <keys> again to confirm`)
— it keeps the user in place.

### 9.7 Read-only panels (help, status, stats)

These have no list and no selection, so they follow a reduced contract:

- Header pad 2; `esc` hint in the header; content in `onPanel`-styled rows.
- Section headings in **`Text` bold**, a blank row under each — the sidebar's
  idiom, not the list dialog's `Accent` category headers.
- Key/value rows go through **one row control** with a fixed label column, so
  values align. Never hand-count the padding per row.
- Every value is truncated to the panel's content column. An overlong line does
  not wrap — it overflows the panel width and tears the row `spliceAt`
  composites it into.
- If the content can overflow, **only the body scrolls**: the header and the
  hint row are fixed, `scrollTop` is clamped to `len(body) − rows` during the
  render (as `listBody` does), and a `↑ N more` / `↓ N more` indicator says what
  is hidden.
- A hint row names the keys that are actually live — scroll keys only while
  there is something to scroll, `esc close` always.
- **Ordering is part of the spec, and it is visible.** Every repeated section
  states its sort key in a named function, breaks ties deterministically (map
  iteration order is not an order), and shows the value it sorts on — a list
  ordered by recency prints the recency. An ordering the reader cannot see is
  indistinguishable from an arbitrary one. Sort by what the section is *for*:
  a usage breakdown by usage, a session list by recency.
- The three empty reasons (nothing yet / still loading / the server reported
  nothing) get three different messages, in the list dialog's `emptyView`
  colors.

### 9.8 Building a new dialog

```go
func (a *App) thingsOverlay() tea.Cmd {
    a.openThingList(a.cachedThings)   // 1. open synchronously from cache
    return a.loadThingsCmd()          // 2. refresh in the background
}

func (a *App) openThingList(things []client.Thing) {
    a.openList("Select thing", a.thingItems(things))
    o := a.overlay
    o.size = dialogLarge              // 3. widest column decides the size
    o.current = a.currentThingID()    // 4. ● marks what is already in effect
    o.placeholder = "Search things..."
    o.actions = []dialogAction{       // 5. actions name their keys
        {title: "refresh", keys: "ctrl+r", standalone: true,
            onTrigger: func(overlayItem) tea.Cmd { return a.loadThingsCmd() }},
        {title: "close", keys: "esc", right: true,
            onTrigger: func(overlayItem) tea.Cmd { a.closeOverlay(); return nil }},
    }
    if len(o.items) == 0 {            // 6. distinguish the empty states
        switch {
        case a.thingErr != "":
            o.emptyTitle, o.emptyBody, o.locked = "Could not load things", a.thingErr, true
        default:
            o.emptyTitle, o.emptyBody = "No things", "Add one with `gocode thing add`."
        }
    }
}
```

Then:

7. Register it in `commandsRegistry()` with `label` (the title the row
   shows — the original's `command.title`), `value` (the dotted command
   name), `slash` (+ aliases), `hint` (the original's `desc`, usually
   empty), `category`, and `footer` if it has a keybind, formatted like
   the original ("<leader>x" → "ctrl+x x", bindings joined ", ").
8. If it previews live (like themes), set `onCancel` to revert.
9. If it toggles rather than picks, set `onActivate` so the dialog stays open.
10. Add a layout test in `dialogs_layout_test.go`.

**Rule:** dialogs must not compute their own hit-test spans separately from
their rendering. `overlayPanel()` builds content and `overlayHits` in the same
pass so a click always matches what is on screen.

---

## 10. Controls catalogue

Reuse these; do not re-implement them.

| Control | Helper | Spec |
|---|---|---|
| **Panel** | `lipgloss` + `splitBorder()` | `┃` left in a semantic color, `BackgroundPanel`, `PaddingTop/Bottom 1`, `PaddingLeft 2`, `Width(withLeftBorder(…))` |
| **Panel text** | `a.onPanel(fg, bold)` | Any text on a dialog/sidebar panel. Never a bare `lipgloss.NewStyle().Foreground(…)` on a panel — the fill drops. |
| **List row** | `listRow` | See §9.2 |
| **Filter input** | `filterRow` | Muted text + `Primary` block cursor; placeholder with cursor on its first cell |
| **Text input** | `inputOverlay` | Value in `Text` + inverted block cursor; `shift+enter` for a newline |
| **Button (dialog)** | `a.button(label, active)` | pad 1; active = `Primary` fill + `SelectedListItemText`; inactive = panel + `TextMuted` |
| **Button (ok)** | `alertOverlay` / `helpOverlay` | pad 3, `Primary` fill, right-aligned |
| **Button (banner)** | inline in `permissionBanner`/`questionBanner` | ` label ` — selected: `Warning`/`Primary` fill on `Background` text; unselected: `BackgroundElement` + `TextMuted` |
| **Action / tab** | `actionRow` | `Title` + muted `keys`; focused inverts to `Primary` |
| **Toggle** | `gutter: "✓"` + `gutterOK: true` | `✓` in `Success` in the bullet gutter; the hint repeats the word so the filter can find it |
| **Status dot** | `mcpDotColor` / `pluginDotColor` | `•` — `Error` failed, `Success` connected, `TextMuted` otherwise |
| **Badge** | inline | ` LABEL ` on a semantic fill (`QUEUED`, ` File `, ` Directory `) |
| **Pill row** | `wrapPills` | Packs items one space apart, never splitting one across lines |
| **Usage meter** | `footerUsage.String()` | `159.6K (16%) · $0.34`, muted; the percentage is dropped when the catalog has no context limit for the model |
| **Key hint** | inline | `Text.Render(key) + " " + Muted.Render(label)`, pairs joined by two spaces |
| **Inline spinner** | `a.spinnerGlyph()` | Braille, 80 ms |
| **Scanner spinner** | `a.scannerSpinner(fg, bg)` | 8 cells, 40 ms, `Primary` (or `Warning` when waiting on the network) |
| **Link** | `renderLink(href, text, style)` | OSC 8 hyperlink; must also record a `linkHit` for the click |
| **Scroll indicator** | inline | `↑ N more lines (pagedown to return)`, muted, costs one budget row |
| **Collapse hint** | `collapsibleBlock` | `(+N lines — click to expand)`, muted, width reserved from the summary row |

**Rule for any new control:** it must (a) take its colors from theme tokens,
(b) paint its own background on every styled segment if it sits on a tinted
surface, (c) name its keybind if it is interactive, and (d) record a hit span if
it is clickable.

---

## 11. Non-dialog overlays

### 11.1 Autocomplete popup

**This is not a dialog.** It is anchored directly above the prompt, same left
edge, same width, at most **10 rows**, with a `┃` left border in `Border` and
the `BackgroundMenu` surface. It has **no title, no filter field and no footer**
— what you type keeps going into the prompt and the list narrows against the
text after the trigger.

```
┃  /models      Switch model
┃  /memory      Manage memories
┃▓ /new         New session          ← selected: Primary fill
┃  /compact     Compact session
```

Triggers: `@` anywhere; `/` **only at position 0** of an empty prompt (a slash
elsewhere is a path, a date, a fraction). Rows: ` display` in `Text` +
` description` in `TextMuted`, padded 1 each side; selected row fills with
`Primary` and both spans go `SelectedListItemText`. Empty: ` No matching items`.

Reusing the centered modal surface for this — which has a title, a search row and
a footer — reads as a completely different component. Matching *behaviour* is not
enough when the shape is the recognisable part.

### 11.2 Toast

Spliced at `top = 2`, `right = 2`, width `min(60, a.width − 6)` (min 12).
The **only** panel with `┃` bars on both sides. `BackgroundPanel`,
`PaddingTop/Bottom 1`, `PaddingLeft/Right 2`. Border color by variant:
`Info` / `Success` / `Warning` / `Error`. Optional bold title + blank row, then
the wrapped message. Default duration 5 s. A bare URL in the message becomes a
clickable OSC-8 link with a recorded `linkHit`.

Toasts render only when **no dialog is open** — the original nests them inside
each route rather than in the dialog layer.

**Rule:** toasts are for outcomes ("copied", "renamed", "interrupted"), not for
errors that need a decision. Those are alerts or empty states.

---

## 12. Keyboard model

### 12.1 Ownership ladder

```
1. active drag-selection  →  ctrl+c copies, esc clears
2. open dialog            →  owns everything; ctrl+c closes like esc (runs onCancel)
3. the diff viewer route  →  owns everything while open (q/esc close); dialogs it opens still sit above it
4. ctrl+c / ctrl+d        →  quit
5. armed leader (ctrl+x)  →  one-shot chord, 1 s timeout
6. global chords          →  ctrl+p, tab, shift+tab, ctrl+z, ctrl+r, newline aliases
7. autocomplete popup     →  navigation keys only; everything else falls through
8. trigger keys           →  @ , / (at position 0)
9. permission banner      →  then question banner
10. history recall        →  up/down at the input boundary
11. subagent navigation   →  up/left/right on an empty prompt only
12. scroll + esc + enter
13. the textarea
```

**Rule:** a new binding goes as *low* in this ladder as it can. Anything above
level 10 steals a key from the editor. (The diff viewer sits at level 3
because it is a mode, like a dialog: every key inside it is the viewer's
own command table, and none of the global chords apply.)

### 12.2 Bindings

| Key | Action |
|---|---|
| `ctrl+x` | Leader (armed for 1 s) |
| `ctrl+x` + `l n m a t s g b c e q h ↓` | sessions / new / models / agents / themes / status / timeline / sidebar / compact / export / quit / tips / children |
| `ctrl+p` | Command palette |
| `tab` / `shift+tab` | Cycle agent forward / back |
| `ctrl+r` | Rename session |
| `ctrl+z` | Suspend |
| `ctrl+t` | Cycle model variant (`variant.cycle`) — no-op when the model has none |
| `shift+enter`, `ctrl+enter`, `alt+enter`, `ctrl+j` | Newline |
| `enter` | Submit (or run a `/command`) |
| `esc` | Arm interrupt while busy (two-press, 5 s window) |
| `pgup`/`pgdown`, `ctrl+alt+b`/`f` | Page the timeline |
| `ctrl+alt+u`/`d` | Half page |
| `ctrl+alt+y`/`e` | One line |
| `ctrl+g` / `ctrl+alt+g` | First / last message |
| `ctrl+v` | Paste (for terminals that do not send bracketed paste) |
| `up`/`down` | Two-stage history recall at the input boundary |

`home`/`end` deliberately stay with the input, not the timeline.

### 12.3 `esc` semantics

`esc` always means "back out of the innermost thing", in this order: clear
selection → close dialog (running `onCancel`) → skip question → arm/confirm
interrupt → nothing. It must **never** quit the app and never discard prompt
text.

### 12.4 Destructive actions

Two-press arming inside lists (`armValue`/`armKeys`); `openConfirm` from the
palette. Never a single keypress. The interrupt gesture is also two-press, with
the hint changing to `again to interrupt` and both spans recoloring to `Primary`.

---

## 13. Mouse and selection model

`MouseModeAllMotion` is on so dialog rows preselect on hover with no button
held.

| Gesture | Effect |
|---|---|
| Wheel | Scroll the timeline (or the dialog list) |
| Click a reasoning header | Toggle that part |
| Click a collapsed tool summary | Expand; click anywhere on an open block to collapse |
| Hover a dialog row | Preselect (minimum scroll, not recenter) |
| Click a dialog row | Activate |
| Click a dialog action / button | Trigger it |
| Click the `esc` hint | Close |
| Click the backdrop | Close |
| Click a toast link | Open the URL |
| Drag | Text selection, clamped to the chat column (`selectionColumnBounds`) |
| `ctrl+c` with a selection | Copy |

**Rule:** every clickable region is recorded in **absolute screen cells** during
the same render that draws it. `View()` finishes before the next `Update()` sees
input, so the cached coordinates are always valid for the next click — but only
if they were produced by the same pass.

---

## 14. Responsiveness

### 14.1 Breakpoints

| Width | Effect |
|---|---|
| `> 120` | Sidebar docks as a column (below: drawer overlay) |
| `≥ 150` | Unlimited context hints |
| `≥ 120` | Model name segment in the footer; 2 context hints |
| `≥ 80` | "Compact" — usage meter and throughput shown; 1 context hint |
| `≥ 66` | `ctrl+p commands` hint |
| `< 66` | Bare minimum |

### 14.2 Degradation order

1. Drop footer right-hand segments from the end.
2. Truncate the footer's left side.
3. Stack banner hint rows under their buttons.
4. Stack the subagent footer's navigation under its label.
5. Clamp panel bodies with a `… N more lines` tail.
6. Never wrap into the sidebar; never let a block push the prompt off screen.

### 14.3 Minimums

Chat column floors at 20 columns; the timeline viewport at 3 rows; the prompt at
1 row. Below that the interface is expected to look cramped, not to break.

---

## 15. Motion and timing

| Animation | Rate | Notes |
|---|---|---|
| Scanner spinner (hint row) | 40 ms | 8 cells, `■`/`⬝`, trail 6, hold 30/9, inactive factor 0.6, min alpha 0.3 |
| Braille spinner (tool rows) | 80 ms | Every second tick of the same loop |
| Meta-row fades | `fadeAnim` | Agent and model segments fade in independently |
| Toast | 5 s | Default duration |
| Leader arm | 1 s | |
| Interrupt arm | 5 s | |
| Live markdown re-render | `max(60 ms, lastPass × 3)` | Duty-cycled so a long streaming response cannot starve keystrokes |

**Rules:** one tick loop at the finest rate, everything else derived from it.
Never animate anything that is not communicating progress. All alphas go through
`theme.Tint`/`FadeColor` against the correct surface.

---

## 16. Performance rules

1. **Cache rendered messages.** `renderMessageCached` keys on a `renderSignature`
   hash; bump `a.renderEpoch` via `invalidateRenderCache()` whenever a render
   input outside the message changes (theme, thinking mode, width).
2. **Cap the timeline** at the last 60 messages.
3. **Duty-cycle streaming renders** (§15). Cache at most
   `streamRenderCacheMax = 16` streamed blocks.
4. **Do not measure what you do not need.** `frame()` deliberately avoids
   `Padding()`/`MaxHeight()` — grapheme segmentation over a fully styled ~90 KB
   frame is the single most expensive thing in the profile.
5. **Render only visible rows.** `collapsibleBlock` calls its body renderer only
   on the rows that will actually show, so a collapsed block tokenises one line
   rather than a whole file.
6. **Cache glamour renderers per (width, variant).**
7. Drop SSE events rather than applying back-pressure to the runner
   (`dropped` counts them).

---

## 17. Terminal compatibility and accessibility

- **Never color-only.** Every state has a glyph or word alongside its color.
- **Never assume a background.** Cells no component paints inherit the
  terminal's defaults; `program.View()` sets those via OSC 10/11, and themes
  that deliberately opt out (`lipgloss.NoColor`) must be left transparent.
- **No emoji, no astral-plane glyphs, no zero-width joiners.** Width is
  unreliable. Stick to §5.
- **Measure with `lipgloss.Width`**, never `len()` — except where a *content*
  rule counts runes (`truncateEllipsis` mirrors a UTF-16 code-unit count).
- **Slice with `sliceCells`**, never byte offsets, or ANSI sequences corrupt.
- **Background goroutines must never write to stderr** while the TUI owns the
  alternate screen; use `global.LogBackground()`.
- Light and dark themes must both be checked — a light theme over a terminal
  still defaulting to black is how the OSC default-color bug was found.

---

## 18. Anti-patterns

Each of these has actually shipped and been fixed. Do not reintroduce them.

| Anti-pattern | What breaks |
|---|---|
| Hex literals outside `theme/` | Unthemeable; breaks in light mode |
| `a.theme = t` instead of `setTheme(t)` | Derived styles (the textarea) keep the old palette until restart |
| Raw content width in `Style.Width()` on a bordered box | Panel one column wider than every sibling |
| One `Render()` on multi-line foreground-only text inside a panel | Uncolored padding punches through the fill |
| A styled segment on a tinted surface without its own `Background()` | Tint drops after the first segment |
| Widening a markdown block to fill its box | Overflow — glamour wraps on raw source width |
| `line[a:b]` on styled text | Corrupt ANSI |
| A literal tab in any rendered block | Width-ambiguous: `lipgloss.Width` counts 1 cell, `JoinHorizontal`'s `getLines` expands it to 4 — a tab-indented code block pushed the docked sidebar right. Expand tabs on the *source* before glamour sizes it (see `renderMarkdownStyled`), never after |
| Fetch-then-open for a dialog | Reads as a 140 ms lag on the keypress |
| Binding `j`/`k` in a list dialog | Those letters become unsearchable |
| `len(key) == 1` to detect a typed character | Drops space and every non-ASCII rune |
| Reusing the modal surface for the autocomplete | Wrong component shape |
| Adding rows without taking them out of `viewportHeight` | The prompt or the banner's buttons get cropped |
| Sidebar rows that wrap | Panel taller than its column, whole layout misaligns |
| A fixed `maxHeight` with no floor for short terminals | Buttons unreachable at 14 rows |
| Reporting loading / failed / empty identically | A fresh install looks hung |
| Summing every message's tokens for "context used" | Grows without bound; the number means nothing |
| Colouring an interruption as an error | It is a user action, not a failure |
| Clickable region computed separately from its render | Hit test drifts from what is on screen |
| Blocking in `Update` | Dead keyboard |
| `fmt.Println`/stderr from a background goroutine | Corrupts the alternate screen |

---

## 19. Checklists

### 19.1 New dialog

- [ ] Is it really a new kind, or an `overlayList` with items + actions?
- [ ] Opens synchronously from cache; refreshes in the background.
- [ ] `size` matches the widest column (60 / 88 / 116).
- [ ] `current` set so `●` marks what is in effect.
- [ ] `placeholder` describes what the filter searches.
- [ ] Actions declared with visible keys; `standalone` where they do not need a row.
- [ ] `onCancel` reverts any live preview.
- [ ] `onActivate` set if the dialog should stay open.
- [ ] Loading / failed / empty states distinguished; `locked` on failure.
- [ ] Registered in `commandsRegistry()` with slash name, aliases, hint, category, footer key.
- [ ] Sits balanced: the panel's bottom margin matches its `height/4` top offset, and it never reaches the last screen row.
- [ ] Layout test added.

### 19.2 New read-only panel

- [ ] One row control for key/value rows; values aligned in a fixed column.
- [ ] Every value truncated to the content column.
- [ ] Header and hint row fixed; only the body scrolls.
- [ ] `scrollTop` clamped during the render; `end` settles on the last screenful.
- [ ] `↑/↓ N more` indicator when windowed.
- [ ] Hint row names the live keys.
- [ ] Loading / unavailable / empty told apart.
- [ ] Bottom margin matches the `height/4` top offset.

### 19.3 New timeline block

- [ ] Width `withLeftBorder(contentWidth()−2)`; total lands at `chatWidth()−1`.
- [ ] One leading blank line; multi-line content via `onPanelText`/`onPanelMuted`.
- [ ] Long content collapses with `collapsibleBlock` and records a
      `toolOutputHeaderRef`.
- [ ] **Nothing wider than `blockToolInterior()`**: the panel's own `Width()`
      soft-wraps, and a wrapped row's leading cells read as more content.
      Wrap prose against `blockToolInnerWidth()`; truncate rows whose columns
      carry meaning (diff gutters, code line numbers) with `ansi.Truncate`.
- [ ] Colors from theme tokens only.
- [ ] Render is cacheable — no state read that is not in `renderSignature`.
- [ ] Fidelity test added in `render_fidelity_test.go`, fit asserted in
      `render_blockfit_test.go`.

### 19.4 New footer / hint segment

- [ ] Assigned a priority and dropped from the end when the row does not fit.
- [ ] Gated by the right `footerWidthPolicy` field.
- [ ] Key in `Text`, label in `TextMuted`.
- [ ] Measured with `lipgloss.Width`.

### 19.5 Anything that consumes vertical space

- [ ] Subtracted from `viewportHeight()`.
- [ ] Cannot crop the prompt, its meta row, or any button.
- [ ] Has a floor behaviour for a 20-row terminal.

### 19.6 Before merging any TUI change

- [ ] `make check` passes (`gofmt`, `go vet`, `go test -race`).
- [ ] Checked at 60, 80, 100, 130 and 170 columns.
- [ ] Checked at ~20 rows.
- [ ] Checked in one light theme and one dark theme.
- [ ] Checked with the sidebar both docked and as a drawer.
- [ ] Checked with a dialog open over it (backdrop dim intact, no bright holes).
- [ ] Any dialog touched still sits balanced — page visible above *and* below it at 24, 40 and 60 rows.

---

## 20. File map

| File | Owns |
|---|---|
| `app.go` | `App` state, `Update`, key handling, `View`, program wiring |
| `views.go` | `frame`, geometry, `viewChat`, `viewHome`, sidebar, prompt box, status bar, ask banners |
| `render.go` | Timeline construction, message/tool/diff blocks, wrapping and indent helpers |
| `markdown.go` | Glamour renderers, normal and dimmed, plus the chroma code theme |
| `highlight.go` | File body renderers (code, markdown, wrapped) |
| `dialogs.go` | Overlay model, list/input rendering, compositing, hit tests, command registry |
| `dialogs_confirm.go` | Buttons, button rows, alert/confirm, filter row, empty view |
| `dialogs_{model,provider,plugins,memory,skill}.go` | Individual dialog content |
| `stats_overlay.go` | `/stats` panel |
| `diffviewer.go` | The `/diff` route: state, fetch, layout, navigation, keys, mouse (see §6.5) |
| `diffviewer_tree.go` | The diff viewer's file-tree logic: build, flatten, navigate |
| `diffstate.go` | The diff viewer's persisted preferences (diffstate.json) |
| `autocomplete.go` | The inline `/` and `@` popup |
| `footer.go` | Hint row, width policy, usage meter, subagent footer, getting-started card |
| `feature.go` | Toasts, timeline dialog, fork/compact/copy/export |
| `spinner.go` | Scanner and braille spinners |
| `animate.go` | Fades and debouncing |
| `composite.go` | Modal backdrop: Canvas + Compositor cell dimming |
| `mouse.go` | Wheel, click routing, drag selection |
| `link.go` | OSC 8 hyperlinks and hit spans |
| `promptsize.go` | Prompt growth and clamping |
| `styles.go` | Semantic style set derived from the theme |
| `components.go` | Reusable panel/button/hint/row style builders |
| `theme/` | `Colors`, `Tint`/`FadeColor`, catalog, 33 JSON palettes |
| `tips.go` | Home-screen tip rotation |
| `client/` | HTTP + SSE client |
