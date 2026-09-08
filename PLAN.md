# Plan: Rework TUI to be Lip Gloss-centric

## Goal

Replace the manual string-building, ANSI-slinging, cell-splicing approach in
`internal/tui` with lipgloss v2's declarative layout, style, and compositing
primitives. The TUI already uses lipgloss for styling, but builds most of its
layout by hand: `strings.Repeat(" ", n)` for padding, manual gap math,
`sliceCells` for ANSI-safe splicing, `dimSGR` for backdrop dimming, `frame()`
for cropping, `centerBlock` for centering, `renderLines` for per-line rendering,
`borderBoxWidth` for border-box arithmetic, and `spliceAt`/`padPlain` for
overlay compositing.

lipgloss v2 (v2.0.6, already in go.mod) ships the primitives to replace almost
all of this:

| Manual pattern | lipgloss v2 replacement |
|---|---|
| `strings.Repeat(" ", gap)` between left/right | `lipgloss.PlaceHorizontal(w, lipgloss.Left, left+right)` or `Style.Width(w).AlignHorizontal(...)` |
| `centerBlock` (prefix every line with N spaces) | `lipgloss.PlaceHorizontal(width, lipgloss.Center, block)` |
| `frame()` crop to terminal height + side margins | `Style.MaxHeight(h).Padding(0, 1)` or `PlaceHorizontal` + `PlaceVertical` |
| `spliceAt` / `padPlain` / `sliceCells` (overlay compositing) | `lipgloss.NewCanvas(w, h)` + `Compositor` + `Layer{X, Y, Z}` |
| `compositeOverlay` / `compositeToast` / `compositeSidebarOverlay` | `NewCompositor(baseLayer, overlayLayer).Render()` |
| `dimBackdrop` / `dimSGR` (ANSI SGR rewriting) | Compositor with z-ordered layers (base behind, panel on top) — dimming becomes a style on the base layer or is applied via `Style.Foreground/Background` tinting |
| `renderLines` (per-line Style.Render to avoid padding gotcha) | lipgloss v2's per-line rendering is still needed in some cases, but `Style.Render` on multi-line content with `Background` set now handles padding correctly — re-evaluate case by case |
| `borderBoxWidth(n) = n + 1` arithmetic | lipgloss v2 `Width()` is already true border-box — pass the intended total directly |
| `indent()` / `aIndent()` | `Style.PaddingLeft(n)` or `Style.MarginLeft(n)` |
| Manual gap in `statusBar`, `chatFooter`, `actionRow`, `dialogHeader` | `lipgloss.JoinHorizontal` with `Style.Width` on segments, or `PlaceHorizontal` with `Left`/`Right` positions |
| `wrapOnBackground` / manual fill-to-width in getting-started card | `Style.Width(w).Background(bg)` (lipgloss pads multi-line content to width when Background is set) |

## Scope and constraints

### What changes

1. **Layout primitives** — Replace manual string math with `Place`,
   `PlaceHorizontal`, `PlaceVertical`, `JoinHorizontal`, `JoinVertical`,
   `Style.Width/Height/MaxWidth/MaxHeight/Padding/Margin/Align`.
2. **Compositing** — Replace `spliceAt`, `sliceCells`, `padPlain`,
   `compositeOverlay`, `compositeToast`, `compositeSidebarOverlay` with
   `lipgloss.Canvas` + `Compositor` + `Layer`.
3. **Backdrop dimming** — Replace `dim.go`'s SGR-rewriting with either
   Compositor z-ordering (no dim needed when panel is on its own layer) or
   `Style.Foreground/Background` tinting on the base layer content.
4. **Frame cropping** — Replace `frame()` with `Style.MaxHeight` or
   `PlaceVertical`.
5. **Centering** — Replace `centerBlock` with `PlaceHorizontal`.
6. **Per-line rendering** — Audit `renderLines` usage; lipgloss v2 with
   `Background` set handles multi-line padding correctly, so many callers can
   switch to a single `Style.Render` call.
7. **Border-box arithmetic** — Remove `borderBoxWidth()` now that lipgloss v2
   `Width()` is true border-box.
8. **Style reuse** — Extract reusable styles (panel, row, button, hint)
   into the `styles` struct or a new `component` layer, reducing inline
   `lipgloss.NewStyle()` calls scattered across view functions.

### What does NOT change

- **Bubble Tea v2 architecture** — `Update`, `View`, messages, commands stay
  the same. Only the rendering body changes.
- **The `App` struct** — No field changes needed (except removing layout
  caches like `chatWindowStart`/`chatWindowPad` if the Compositor replaces
  them, but those are read by `mouse.go` so they may stay as computed values).
- **Mouse hit-testing** — `overlayHits`, `linkHits`, `chatReasoningRows` etc.
  are still needed. The Compositor's `Hit(x, y)` can eventually replace some of
  this, but the initial rework keeps the existing hit-test maps and only
  changes how the *rendered string* is produced. Migrating hit-testing to
  `Compositor.Hit` is a follow-up, not part of this plan.
- **Render caching** — `renderMessageCached` / `streamRender` / the render
  epoch system stay. The cache stores rendered strings; swapping the rendering
  internals does not change the cache contract.
- **Glamour markdown rendering** — `renderMarkdown` / `renderMarkdownDim`
  stay as-is (they produce styled strings that the layout then places).
- **Performance** — The frame budget is 16ms. The Compositor + Canvas path
  must be benchmarked against the existing string-splicing path. If the
  Compositor is slower, we keep string splicing for the hot path and use the
  Compositor only for overlays. The existing `frame()` comment notes that
  `lipgloss.NewStyle().Padding(0,1).MaxHeight(h)` was too expensive — that
  decision must be re-evaluated with lipgloss v2's renderer, but the default
  assumption is that manual string manipulation stays faster for the
  full-frame crop and that the Compositor is a net win only for overlay
  compositing (where it replaces `sliceCells`).

### Test strategy

- Every existing test must pass unchanged. The tests assert on rendered output
  ( ANSI-stripped text, layout positions, colors), not on the internal
  rendering method, so swapping internals should be transparent.
- `layout_fidelity_test.go` and `render_fidelity_test.go` are the key
  regression guards.
- New benchmarks compare the old vs. new rendering paths for a full frame.

## Phases

The rework is split into phases that can land independently. Each phase is a
self-contained PR that leaves all tests green.

### Phase 1: Extract reusable style helpers (low risk)

**Files:** `styles.go` (expand), new `components.go`

**What:**
- Expand the `styles` struct to include the common panel, row, button, and
  hint styles that are currently built inline in every view function:
  - `Panel` — `Background(theme.BackgroundPanel).Padding(1, 2)`
  - `SplitBorderPanel` — `Border(splitBorder(), ...).BorderForeground(...).Background(...).Padding(1, 2).Width(total)`
  - `Row` — `Background(theme.BackgroundPanel)` (for list rows)
  - `SelectedRow` — `Background(theme.Primary).Foreground(theme.SelectedListItemText)`
  - `Button` — `Foreground(fg).Background(bg).Render(" " + label + " ")`
  - `Hint` — `Text.Render(key) + " " + Muted.Render(label)`
- Create a `components.go` file with reusable render helpers:
  - `splitBorderPanel(width int, fg color.Color) lipgloss.Style` — the
    `Border(splitBorder(), false, false, false, true).BorderForeground(fg).Background(theme.BackgroundPanel).Padding(1, 2).Width(width)` pattern used by `userBlock`, `errBlock`, `blockToolStyle`, `permissionBanner`, `questionBanner`.
  - `button(label string, selected bool, active, bg color.Color) string` —
    the button pattern repeated in `permissionBanner`, `questionBanner`,
    `dialogs_confirm.go`, `helpOverlay`.
  - `hintRow(pairs ...[2]string) string` — the `key + " " + label` pattern
    repeated everywhere.

**Why:** This phase only extracts and deduplicates; it does not change any
layout logic. It is the safest starting point and makes the subsequent phases
easier because the view functions become shorter and more declarative.

**Tests:** All existing tests pass. No behavior change.

---

### Phase 2: Replace manual centering and padding with lipgloss Place (low risk)

**Files:** `views.go`, `render.go`

**What:**
- Replace `centerBlock(width, block)` with `lipgloss.PlaceHorizontal(width, lipgloss.Center, block)`.
  - `centerBlock` prefixes every line with N spaces. `PlaceHorizontal` with
    `Center` does the same thing.
  - Audit: the comment says "lipgloss.Place would center each line
    independently" — but `PlaceHorizontal` *does* center each line independently
    too, which is actually what `centerBlock` does (it prefix-pads every line).
    So the replacement is correct.
- Replace `frame()` with a lipgloss-based equivalent:
  - The current `frame()` crops to terminal height and adds 1-column side
    margins. The performance comment says lipgloss's `Padding(0,1).MaxHeight(h)`
    was too expensive because it measures every line.
  - **Investigate:** lipgloss v2 may handle this better. If not, keep the
    manual `frame()` but document why. This is a measured decision, not an
    assumption.
  - If keeping `frame()`: at minimum, replace the side-margin loop with
    `Style.PaddingLeft(1).PaddingRight(1)` on the *outermost* style, applied
    once, rather than prefixing/suffixing every line.
- Replace `indentBlock(block)` / `aIndent(block, 1)` with
  `Style.PaddingLeft(1).Render(block)` or `lipgloss.PlaceHorizontal` with an
  offset. `aIndent` operates on ANSI-styled strings; `Style.PaddingLeft` on a
  multi-line block adds the indent to every line, which is the same effect.
- Replace `indent(value, spaces)` with `Style.PaddingLeft(spaces)` — but note
  that `indent` skips blank lines (`if lines[i] != ""`), while
  `Style.PaddingLeft` pads every line. Keep `indent` if the blank-line-skipping
  matters, or switch and accept that blank lines get padding (which is
  invisible anyway).

**Tests:** `viewHome` and `viewChat` output must be byte-identical (after ANSI
stripping) to the pre-change output. `layout_fidelity_test.go` guards this.

---

### Phase 3: Replace manual gap/spacing in status bars, footers, and dialog headers (low risk)

**Files:** `views.go` (`statusBar`, `questionBanner`, `permissionBanner`), `footer.go` (`chatFooter`, `subagentFooter`), `dialogs.go` (`dialogHeader`, `actionRow`, `inputOverlay`, `helpOverlay`)

**What:**
- Replace the `left + strings.Repeat(" ", gap) + right` pattern with
  `lipgloss.JoinHorizontal(lipgloss.Left, left, lipgloss.PlaceHorizontal(gap, lipgloss.Left, ""), right)`
  or, more idiomatically, with a `Style.Width(totalWidth)` on a container that
  uses `AlignHorizontal(lipgloss.Left)` for the left part and
  `AlignHorizontal(lipgloss.Right)` for the right part.
- For `statusBar`: use `lipgloss.PlaceHorizontal(width, lipgloss.Left, left)` +
  right-aligned version. Or build the whole row as a single
  `Style.Width(width)` with `AlignHorizontal` set, where left and right
  segments are composed with `JoinHorizontal`.
- For `dialogHeader`: `PlaceHorizontal(w - 2*pad, Center, title+gap+hint)`.
  Actually, `dialogHeader` is left-padded title + gap + right-aligned hint, so:
  `lipgloss.PlaceHorizontal(w-2*pad, lipgloss.Left, styled)` +
  `lipgloss.PlaceHorizontal(gap, lipgloss.Right, esc)` — or just use a single
  `Style.Width(w-2*pad).AlignHorizontal(lipgloss.Left)` for the title and
  `AlignHorizontal(lipgloss.Right)` for the hint, composed via `JoinHorizontal`.
- For `actionRow`: The left/right action groups with a flexible gap can use
  `PlaceHorizontal(innerWidth, lipgloss.Left, leftGroup)` and
  `PlaceHorizontal(innerWidth, lipgloss.Right, rightGroup)` composed with
  `JoinHorizontal`.

**Why:** Eliminates ~15 instances of manual `strings.Repeat(" ", gap)` gap
math. Makes the layout intent declarative.

**Tests:** `dialogs_layout_test.go`, `footer_test.go`, `permission_layout_test.go`.

---

### Phase 4: Remove `borderBoxWidth` and audit border-box arithmetic (low risk)

**Files:** `render.go` (`borderBoxWidth`), `views.go`, `dialogs.go`, `footer.go`

**What:**
- `borderBoxWidth(n) = n + 1` exists because the code was tuned under
  lipgloss v1 where `Width()` excluded the border. lipgloss v2's `Width()` is
  true border-box (the declared value IS the total rendered size, border
  included). So `borderBoxWidth(n)` is now just `n + 1` which adds the border
  column on top of what lipgloss already counts — double-counting.
- **Investigate:** Read every `borderBoxWidth` call site and determine if the
  argument is already the intended total (in which case `borderBoxWidth` should
  be removed and the raw value passed) or if it's the content+padding width
  (in which case `borderBoxWidth` adds the border column correctly, but the
  naming should be clearer).
- The `TUI_RECOMENDATIONS.md` doc says: "lipgloss v2's Width() is true
  border-box: the declared value is the total rendered width, borders and
  padding included. Every single-left-border panel is declared as
  `Width(borderBoxWidth(contentAndPadding))`, where `borderBoxWidth(n) = n + 1`
  adds the ┃ column back."
- So the current code is correct: `contentAndPadding` is the content+padding
  width, and `+1` adds the border. The function is fine but the name is
  confusing. Rename to `withLeftBorder(n int) int` for clarity, or inline the
  `+1` at call sites with a comment.
- This phase is really an audit and rename, not a behavior change.

**Tests:** All existing tests pass.

---

### Phase 5: Replace overlay compositing with lipgloss Canvas + Compositor (medium risk)

**Files:** `dialogs.go` (`spliceAt`, `padPlain`, `sliceCells`, `compositeOverlay`, `viewOverlay`, `dimFrame`), `feature.go` (`compositeToast`), `views.go` (`compositeSidebarOverlay`), `dim.go`

**What:**

This is the core of the rework. The manual ANSI-safe string splicing in
`spliceAt` / `sliceCells` / `padPlain` is replaced by lipgloss v2's
`Compositor` + `Layer` + `Canvas` system:

```go
// Before (dialogs.go):
func (a *App) compositeOverlay(base, panel string) string {
    top, left := a.overlayOrigin(lipgloss.Width(panel))
    return a.spliceAt(a.dimBackdrop(base), panel, top, left)
}

// After:
func (a *App) compositeOverlay(base, panel string) string {
    top, left := a.overlayOrigin(lipgloss.Width(panel))
    baseLayer := lipgloss.NewLayer(base).X(0).Y(0).Z(0)
    panelLayer := lipgloss.NewLayer(panel).X(left).Y(top).Z(1)
    return lipgloss.NewCompositor(baseLayer, panelLayer).Render()
}
```

Key changes:
1. **`spliceAt` → `Compositor`** — The Compositor handles z-ordered layer
   placement. The base is at Z=0, the panel at Z=1. The Compositor renders the
   composite to a Canvas and returns the string.
2. **`sliceCells` / `padPlain` → Canvas** — The Canvas handles cell-level
   compositing. Where the panel overlaps the base, the panel's cells win (it's
   drawn on top). No manual ANSI slicing needed.
3. **`dimBackdrop` → Compositor with dimmed base layer** — The backdrop dim
   can be applied as a style on the base layer's content before compositing,
   or the dim can be dropped entirely if the panel-on-top look is sufficient
   without dimming. If dimming is still wanted, apply it as a
   `Style.Foreground(dimmedColor)` / `Style.Background(dimmedColor)` pass on
   the base content before creating the base layer — which is what `dimBackdrop`
   already does, but it could be simplified to a `Style` call if the content
   is a lipgloss-rendered block rather than a raw string.
4. **`compositeToast` → Compositor** — Same pattern: base at Z=0, toast at
   Z=1 with X/Y offset for top-right placement.
5. **`compositeSidebarOverlay` → Compositor** — Base at Z=0, sidebar at Z=1,
   right-aligned.

**Risk:** The Compositor renders through a Canvas, which is a cell buffer.
This is a different code path than the current string splicing. Performance
must be benchmarked — a full-frame compositor render may be slower than the
current targeted splice (which only touches the rows the panel occupies).

**Mitigation:** If the Compositor is too slow for every-frame use:
- Keep string splicing for the hot path (every-frame compositing of the
  sidebar in `viewChat` when the terminal is narrow).
- Use the Compositor only for dialog overlays and toasts, which are less
  frequent (overlay open/close, toast show/expire) and are not on the per-
  keystroke hot path.
- Or: use the Compositor for all compositing, but cache the composite result
  and only re-render when the overlay state changes.

**Hit-testing follow-up (not in this phase):** The Compositor's `Hit(x, y)`
method can replace the manual `overlayHits` / `linkHits` system. This is a
follow-up that depends on giving every interactive layer an `ID`, which the
Compositor can then use to resolve clicks. This is a significant refactor of
`mouse.go` and is explicitly out of scope for the initial rework.

**Tests:** `dialogs_layout_test.go`, `dialogs_latency_test.go`,
`link_test.go`, `selection_test.go`, `sidebar_plugins_test.go`.

---

### Phase 6: Replace `renderLines` with single Style.Render where safe (low risk)

**Files:** `render.go` (`renderLines`), all callers

**What:**
- `renderLines` exists because of the per-line padding gotcha: lipgloss's
  `Style.Render` on multi-line content pads every line to the block's longest
  line, but that padding is only colored when `Background` is set. A
  foreground-only style leaves bare, uncolored spaces that punch through an
  enclosing panel's background.
- **Audit each caller:** If the style has a `Background` set, the padding is
  colored and `renderLines` is unnecessary — a single `Style.Render(text)`
  call works. If the style is foreground-only and the result is embedded in
  another style's `Background`, `renderLines` is still needed.
- Callers to audit:
  - `userBlockOf` — uses `renderLines(a.styles().Text, body)` then embeds in
    a `BackgroundPanel` style → still needed (Text has no Background).
  - `bashBlock` — uses `renderLines(a.styles().Text, ...)` then embeds in
    `blockToolStyle` → still needed.
  - `readBlock` / `writeBlock` — uses `renderLines(a.styles().Muted, ...)` →
    still needed.
  - `reasoningBody` — uses `renderLines(a.styles().Muted, md)` → still needed.
  - `errBlock` in `renderAssistant` — uses `renderLines(a.styles().Muted, ...)` →
    still needed.
- **Alternative fix:** Give the `Text` and `Muted` styles an explicit
  `Background(theme.BackgroundPanel)` when they're used inside a panel, then
  the single `Style.Render` call works. This is the more lipgloss-idiomatic
  approach — the style carries its own background, so padding is always
  colored. The `onPanel` helper already does this; extend it to cover these
  cases.
- So the fix is: replace `renderLines(a.styles().Text, body)` with
  `a.onPanel(a.theme.Text, false).Render(body)` (which has `BackgroundPanel`
  set) → then a single `Render` call is safe and `renderLines` can be removed
  from that call site.
- Not every caller can switch — some embed in `BackgroundElement` rather than
  `BackgroundPanel`, so the background must match the enclosing style. The
  audit must be per-caller.

**Tests:** `render_test.go`, `render_fidelity_test.go`, `render_bench_test.go`.

---

### Phase 7: Simplify `frame()` and sticky-bottom padding (low risk)

**Files:** `views.go` (`frame`, `viewChat`, `viewHome`)

**What:**
- Re-evaluate whether `frame()` can use lipgloss primitives:
  - The side margins: `Style.PaddingLeft(1).PaddingRight(1)` applied to the
    final output. The comment says this was too expensive because lipgloss
    measures every line's display width. **Test this with lipgloss v2** — the
    new renderer may be fast enough.
  - The height crop: `Style.MaxHeight(h)`. Same performance concern.
  - If lipgloss is still too slow: keep the manual `frame()` but extract it
    into a documented helper and add a benchmark that catches regressions.
- Replace the sticky-bottom padding in `viewChat`:
  ```go
  pad := a.height - strings.Count(main, "\n") - 1
  main = strings.Repeat("\n", pad) + main
  ```
  with `lipgloss.PlaceVertical(a.height, lipgloss.Bottom, main)`.
  - `PlaceVertical` with `Bottom` pads the top with blank lines to fill the
    height, which is exactly what the manual code does.
  - **Caveat:** `PlaceVertical` pads with `ws.render(width)` (whitespace
    strings), not bare `\n`. The manual code pads with `\n` only. The visual
    result is the same (blank lines either way), but the ANSI output differs.
    Tests that check the raw string will need updating, or the `PlaceVertical`
    whitespace option must be set to produce bare newlines. Check if
    `lipgloss.WithWhitespaceBackground` or a no-background whitespace option
    can produce the same output.
- Replace the home-screen vertical centering:
  ```go
  top := available/2 + 2
  bottom := available - top
  content = strings.Repeat("\n", top) + content + strings.Repeat("\n", bottom+1) + status + "\n"
  ```
  with `lipgloss.Place(a.width, a.height, lipgloss.Center, lipgloss.Center, content)`
  — but the home layout is more complex than simple centering (it has a status
  bar pinned to the bottom), so this may need to be composed as:
  `JoinVertical(Left, PlaceVertical(available, Center, content), status)`.

**Tests:** `viewHome` and `viewChat` fidelity tests.

---

### Phase 8: Clean up and document (no risk)

**Files:** All touched files, `TUI_RECOMENDATIONS.md`

**What:**
- Remove dead code: `sliceCells`, `padPlain`, `spliceAt` (if fully replaced by
  Compositor), `dimSGR` / `dimBackdrop` / `dimSGRParams` / `readColor` /
  `xterm256` / `dimRGB` / `dimChannels` (if dimming is handled differently),
  `centerBlock` (if replaced by `PlaceHorizontal`), `borderBoxWidth` (if
  inlined/renamed).
- Update `TUI_RECOMENDATIONS.md` to reflect the new rendering model:
  - Section 1 ("The rendering model") — replace the spliceAt/sliceCells
    description with the Compositor/Layer model.
  - Section on border-box arithmetic — update if `borderBoxWidth` is renamed
    or removed.
  - Anti-patterns section — add "don't use `strings.Repeat(" ", n)` for
    layout; use `PlaceHorizontal`/`Style.Width`/`JoinHorizontal`".
- Update `06-tui.md` if the rendering model description changes.
- Add a `LIPGLOSS_REWORK.md` or update the architecture doc with the new
  compositing approach.

**Tests:** Full test suite passes. No behavior changes.

## Performance budget

The TUI has a 16ms frame budget. The current rendering path has been
carefully optimized:

- `frame()` uses manual string manipulation instead of `Style.Padding` because
  lipgloss v1's `Padding(0,1).MaxHeight(h)` measured every line's display width
  (grapheme segmentation is the most expensive thing in the profile).
- `renderMessageCached` caches rendered message blocks to avoid re-parsing
  markdown on every frame.
- `streamingTextBlock` rate-limits live message re-rendering.

**The rework must not regress these.** Every phase that replaces a manual
string operation with a lipgloss primitive must be benchmarked. If the
lipgloss primitive is slower, keep the manual code and document why.

Specific concerns:
1. **Compositor + Canvas** — Creating a canvas, drawing layers, and rendering
   to a string is likely more expensive than the current `spliceAt` (which only
   touches the rows the panel occupies). Benchmark before committing. If
   slower, use the Compositor only for dialog overlays (less frequent) and
   keep `spliceAt` for the sidebar compositing (every frame when narrow).
2. **PlaceHorizontal / PlaceVertical** — These iterate lines and add
   whitespace. They should be comparable to the manual `strings.Repeat`
   approach. Benchmark to confirm.
3. **Style.PaddingLeft / PaddingRight** — These should be cheap (they just
   prepend/append spaces). But `Style.Render` on a multi-line block pads every
   line, which involves measuring the block's width. For the outermost `frame()`
   call on a ~90KB frame, this is the known hot spot.

**Benchmark plan:** Add benchmarks for each phase that compare the old vs. new
implementation of the specific function being replaced. Run with:
```
go test ./internal/tui/ -bench=BenchmarkFrame -benchmem
go test ./internal/tui/ -bench=BenchmarkKeypress -benchmem
```

## Dependency graph

```
Phase 1 (style helpers) ──┬──> Phase 2 (centering/padding)
                          ├──> Phase 3 (gap/spacing)
                          └──> Phase 4 (border-box audit)
                                    │
Phase 5 (Compositor) ────────────────┤
                                    │
Phase 6 (renderLines) ──────────────┤
                                    │
Phase 7 (frame/sticky-bottom) ──────┤
                                    │
Phase 8 (cleanup/docs) ────────────┘
```

Phases 1-4 can be done independently and in any order. Phase 5 is the
highest-risk phase and should be done after Phase 1 (so it can use the
extracted styles). Phases 6-7 can be done independently after Phase 1. Phase 8
is last.

## Estimated effort

| Phase | Risk | Files touched | Est. lines changed |
|---|---|---|---|
| 1: Style helpers | Low | 3-4 new, 10+ modified | ~200 new, ~300 modified |
| 2: Centering/padding | Low | 3 | ~100 |
| 3: Gap/spacing | Low | 5 | ~150 |
| 4: Border-box audit | Low | 5 | ~50 (rename + comments) |
| 5: Compositor | Medium | 5 + delete dim.go | ~300 new, ~400 deleted |
| 6: renderLines | Low | 5 | ~100 |
| 7: Frame/sticky-bottom | Low | 2 | ~80 |
| 8: Cleanup/docs | None | All + docs | ~200 (docs + dead code removal) |

Total: ~1,180 lines changed across 8 phases.
