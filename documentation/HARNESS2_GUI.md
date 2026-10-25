# Harness² in Gocode Desktop — implementation plan

[← Documentation index](README.md) · [Harness² plan](HARNESS2_PLAN.md) · [Gocode Desktop](11-desktop.md)

---

Status: **proposal**. Nothing here is built yet.

Goal: everything the TUI's `/harness` does (start a run for a task, watch it
live, inspect each stage, install or publish the result, browse the
experience library) available in Gocode Desktop (`gocode_gui/`). The
server side is done (`/api/harness/*` and live `harness.*` events,
[HTTP API → Harness²](08-http-api.md#harness)); this plan is the Flutter half
plus the few server additions the GUI needs that the TUI did not.

## 1. What already exists

### 1.1 Server (gocode, `feature/harness2`)

| Need | Route / event | Notes |
|---|---|---|
| Plan a run | `POST /api/harness/run` with `dryRun: true` | returns rows, budget line, backend, reason; spends nothing |
| Start a run | `POST /api/harness/run` | `{prompt, id?, domain?, workspace?, k?, mode?, model?, improverModel?, library?, crossDomain?}`; `409` while the same run is going |
| List runs | `GET /api/harness/run` | live and finished, newest first |
| One run | `GET /api/harness/run/{runID}` | status, phase, one row per label, cost, adopted, why, lesson |
| Stage detail | `GET /api/harness/run/{runID}/stage/{label}` | harness diff vs base (unified), file counts, notes, error, rollout session id, deliverable (final message + workspace diff in a fenced block) |
| Cancel / delete | `POST …/cancel`, `DELETE /api/harness/run/{runID}` | delete removes the run directory and its sessions; `409` while running |
| Install / publish | `POST …/install` `{to, force, dryRun}` | `409` with the result when an installed harness or Library skill differs |
| Library | `GET /api/harness/library`, `GET/DELETE …/{domain}/{task}`, `POST …/sync` | backend + reason, entries by domain |
| Live progress | `harness.*` on `/api/event` | every event carries `{run, event}` with the **whole** run state — a dropped event loses nothing |

Run ids are `<task>--k<k>-<mode>`; they survive restarts because they name the
run's directory.

### 1.2 The TUI as the behavioural reference

`internal/tui/dialogs_harness.go` and
[TUI_RECOMENDATIONS.md §6.6](recomendations/TUI_RECOMENDATIONS.md) define the
flows the GUI mirrors: runs list (running above finished), the run's plan of
rows grouped by phase (base → propose and probe → compose → select →
reflect), stage panel, budget picker, dry-run-then-confirm before spending,
install always previewed, experience library browser, a toast when a run
ends. The GUI should make the **same decisions** (what is confirmed, what is
armed, what is hidden) so the two clients never disagree about what is safe.

### 1.3 Gocode Desktop architecture (what the plan builds on)

| Concern | Existing pattern | File |
|---|---|---|
| State | Riverpod 3 `Notifier`/`NotifierProvider`, `FutureProvider.autoDispose`; build watches `connectionControllerProvider`/`apiClientProvider` and resubscribes | `core/connection/session_activity.dart`, `features/git/git_state.dart` |
| HTTP | hand-written `GocodeClient` methods over `_getJson`/`_send`, throwing `ApiException(status, message)` | `core/api/client.dart` |
| Models | hand-written immutable classes with `factory fromJson` (no codegen in practice) | `core/api/models.dart`, `git_models.dart` |
| Events | one `SseClient` on `/api/event` (no session filter), **lossy by design**, plus a `reconnectSignal` owners reconcile on | `core/api/sse.dart`, `ConnectionController.events` |
| Transports | `ConnectionMode.local` (spawned `gocode serve`), `remote` (attach over HTTP), `acp` (stdio ACP — **no HTTP API**) | `core/connection/controller.dart` |
| Routing | `GoRouter` with one `ShellRoute` (sidebar + page), cross-fade pages | `app/app.dart` |
| Navigation entry | `SidebarRow` items ("New session", "Source control") | `features/sidebar/sidebar.dart` |
| Session header actions | `PillHeader.actions` (the Source control button, gated on `apiClientProvider != null`) | `features/session/session_screen.dart` |
| Look | `GC` tokens (surfaces, accent, ok/warn/down, radii, motion), `GlassSurface`, `PillHeader`, `PageFrame`, `Caption` | `app/theme.dart`, `shared/widgets/glass.dart`, `page_frame.dart` |
| Diffs | `parseUnifiedDiff` + `DiffView` (and `GitDiffDialog` for git files) | `shared/widgets/diff_view.dart`, `features/git/diff_view.dart` |
| Markdown | `gpt_markdown` | `shared/widgets/message_parts.dart` |
| Model choice | `showModelPicker` (returns a `ModelChoice`) over `modelsProvider` | `shared/widgets/model_picker.dart` |
| Dialogs / notices | `showDialog` + `AlertDialog`, `ScaffoldMessenger` `SnackBar` (floating) | `features/git/git_widgets.dart` |
| Tests | widget tests with `http/testing.dart` `MockClient` fake servers (`_FakeGit`), opt-in integration test against a real `gocode serve` (`GOCODE_BIN`) | `test/git_screen_test.dart`, `integration_test/gocode_server_test.dart` |
| CI | `.github/workflows/gui-ci.yml`: `flutter analyze`, `flutter test test/` | |

## 2. Constraints that shape the design

1. **Server and Remote modes only.** ACP mode has no HTTP API and the
   protocol carries nothing for Harness², so every harness entry point hides
   in ACP mode, exactly as Source Control does (gate on
   `apiClientProvider != null`). The library-only screens hide too.
2. **Remote mode paths are the server's.** A run's workspace is a directory
   on the server's machine. The GUI never browses the local filesystem for a
   workspace; it offers the session's directory (already a server path) or
   the server's own working directory. No `file_picker` here.
3. **The event stream is lossy.** The notifier must reconcile from
   `GET /api/harness/run` on every `reconnectSignal`, and while any run is
   `running`, poll it at a slow interval (10 s) as a backstop — the same
   belt-and-braces `SessionActivityNotifier` uses for `busy`.
4. **A run is never started without its price on screen** and an install
   never writes without a preview — the TUI's rules (§6.6), not new ones.
5. **No new dependencies.** Everything above is enough; adding packages to
   the desktop app needs a reason this feature does not have.

## 3. Server prerequisites (gocode)

Small changes the GUI needs that the TUI did not. Each lands with tests in
the same PR as the GUI phase that first uses it.

| # | Change | Why the GUI needs it | Where |
|---|---|---|---|
| S1 | `InstallRequest.dir` (optional): install into that directory instead of the server's working directory; must exist and be a directory | the GUI has projects in many folders; the TUI only ever installs into the folder it was started in | `internal/harness/service.go`, `internal/server/harness.go`, `cmd/gocode/harness_engine.go` (`installHarness` already takes a dir) |
| S2 | `RunState.workspace` (and `prompt`, first 200 chars): returned by list/get | the runs screen filters by the selected project and shows what a run was about | `internal/harness/service.go` (`FromSummary` reads `task.json`) |
| S3 | Cross-process run lock (`<run dir>/.lock`, `flock`): `Start` and `Delete` refuse a run another process holds | a CLI run and a GUI run of one task in two processes would share a directory — the known gap in HARNESS2_PLAN §10 | `internal/harness/run.go`, `service.go` |
| S4 | `GET /api/harness/budgets`: the offered `(mode, k)` pairs with rollouts/edits | keeps the GUI's picker in step with the TUI's `harnessBudgets` instead of a second hard-coded copy | `internal/server/harness.go` (move `harnessBudgets` into `internal/harness`) |
| S5 (Phase 5) | `POST /api/harness/sweep` + `harness.sweep.*` events, `GET /api/harness/sweep` | sweeps from the GUI | `internal/harness/service.go` around `Engine.Sweep` |

S1–S4 are a few hours of server work; S5 is only needed for Phase 5.

## 4. Data layer (`gocode_gui/lib/core/api`, `lib/features/harness`)

### 4.1 Models — `core/api/harness_models.dart`

Hand-written, immutable, `factory fromJson`, mirroring the Go wire types
(the TUI's `internal/tui/client/client.go` mirror is the reference):

```dart
class HarnessRow      { label, phase, state, costUSD, detail }
class HarnessRun      { id, task, domain, k, mode, status, phase, backend, reason,
                        rows, costUSD, startedAt, adopted, why, lesson, error,
                        budget, workspace /*S2*/, prompt /*S2*/ }
class HarnessFiles    { added, removed, modified, alwaysOnCharsDelta }
class HarnessStage    { label, diff, files, notes, error, sessionID, ok,
                        delivered, deliverable, costUSD }
class HarnessPublish  { new_, changed, unchanged, blocked }
class HarnessInstall  { target, files, conflict, collisions, warnings, library, written }
class HarnessEntryInfo{ domain, task, cue, components, pending }
class HarnessLibrary  { backend, reason, domains: Map<String, List<HarnessEntryInfo>> }
class HarnessEntry    { domain, task, label, why, lesson: HarnessLesson, tree: Map<String,String> }
class HarnessBudget   { mode, k, rollouts, edits, description }   // S4
```

Derived getters keep widgets dumb: `HarnessRun.isRunning`, `.isFinished`,
`.rowsDone`, `.rowsByPhase` (ordered `base, probe, compose, select,
reflect`), `HarnessRow.isSettled`.

### 4.2 Client — `GocodeClient` additions

```dart
Future<List<HarnessRun>> harnessRuns();
Future<HarnessRun> harnessRun(String id);
Future<HarnessRun> startHarness(HarnessStartRequest req);      // dryRun inside req
Future<void> cancelHarness(String id);
Future<void> deleteHarness(String id);
Future<HarnessStage> harnessStage(String id, String label);
Future<HarnessInstall> installHarness(String id, {required String to,
    bool force = false, bool dryRun = false, String? dir});    // 409 → HarnessInstallConflict(result)
Future<HarnessLibrary> harnessLibrary();
Future<HarnessEntry> harnessEntry(String domain, String task);
Future<void> forgetHarnessEntry(String domain, String task);
Future<int> syncHarnessLibrary();
Future<List<HarnessBudget>> harnessBudgets();                  // S4
```

Path segments go through `Uri.encodeComponent`. `installHarness` maps a
`409` whose body carries `result` to a typed `HarnessInstallConflict`
exception holding the parsed `HarnessInstall`, so the dialog can show what
conflicted.

### 4.3 State — `features/harness/harness_state.dart`

| Provider | Kind | Behaviour |
|---|---|---|
| `harnessRunsProvider` | `NotifierProvider<HarnessRunsNotifier, HarnessRunsState>` | `build` watches `connectionControllerProvider` + `apiClientProvider`; loads the list; subscribes to `controller.events` and folds every `harness.*` event's `data.run` into the list (replace by id, insert if new — the aggregator's rule in the TUI); reconciles on `reconnectSignal`; while any run is running, a 10 s `Timer.periodic` reconcile; emits a `HarnessRunEnded` notice when a run leaves `running` (consumed once by the shell for the SnackBar). State: `runs`, `loading`, `error`, `lastEnded`. |
| `harnessRunProvider(id)` | `Provider.family` | selects one run out of `harnessRunsProvider` (no second fetch, no second subscription) |
| `harnessStageProvider((id,label))` | `FutureProvider.autoDispose.family` | fetches stage detail; **invalidated** by the run notifier when that row's state changes, so an open stage view refreshes as its rollout settles |
| `harnessLibraryProvider` | `FutureProvider.autoDispose` | library listing; invalidated after forget/sync and when a run stores a lesson (`harness.reflected` with `ok`) |
| `harnessEntryProvider((domain,task))` | `FutureProvider.autoDispose.family` | one entry |
| `harnessBudgetsProvider` | `FutureProvider` | S4, cached for the connection's life |
| `activeHarnessRunProvider` | `Provider<HarnessRun?>` | newest running run, for the sidebar badge and the session header |

Mutations (`start`, `cancel`, `delete`, `install`, `forget`, `sync`) live on
the notifier (or a small `HarnessActions` class reading the client), return
`Future<void>`, surface failures as `ApiException` text in a SnackBar, and
reconcile afterwards — never patch local state optimistically for a delete
or an install, matching the TUI's "refetch rather than patch" rule.

ACP gate: every provider returns an empty/`unavailable` state when
`apiClientProvider` is null, and the widgets read a single
`harnessAvailableProvider` (`apiClientProvider != null`).

## 5. Screens and flows

New feature folder `lib/features/harness/`:

```
harness_state.dart        providers + notifier (§4.3)
harness_screen.dart       /harness            runs list
harness_run_screen.dart   /harness/run/:id    one run: plan, outcome, actions
harness_stage_sheet.dart  stage detail (side sheet on wide windows, page on narrow)
harness_start_dialog.dart start flow: task → budget/model → plan → confirm
harness_install_dialog.dart install / publish preview and confirm
harness_library_screen.dart /harness/library  experience library
harness_widgets.dart      status chip, row tile, phase header, cost label
```

### 5.1 Routes and entry points

- `app/app.dart`: `route('/harness', …)`, `route('/harness/run/:id', …)`,
  `route('/harness/library', …)` inside the `ShellRoute`.
- **Sidebar**: a `SidebarRow` "Harness" under "Source control"
  (`SidebarKeys.harness`), icon `Icons.auto_fix_high_outlined`, selected on
  `/harness*`. While a run is going it shows a small accent dot and
  `phase 3/6` in `GC.textDim`. Hidden in ACP mode.
- **Session header**: an `IconButton` "Refine harness for this task"
  (`Icons.auto_fix_high_outlined`) beside Source control, opening the start
  dialog prefilled from this session (first user message, directory,
  model). Hidden in ACP mode.
- **Run end**: the shell listens to `harnessRunsProvider.select(lastEnded)`
  and shows a floating SnackBar — "Harness² · fix-login: adopted agg1 ·
  $1.25" with an **Open** action → `/harness/run/:id`; failures in
  `GC.downText`. One notice per run end, never repeated on rebuild.

### 5.2 Runs list — `/harness`

`PageFrame`-style column under a `PillHeader` titled "Harness".

- Header actions: **New run** (filled), **Library** (text button), refresh.
- Filter chip row: *All projects* / the sidebar's selected project
  (matches `run.workspace`, S2) — defaults to the selected project when
  there is one, like Source Control.
- Two sections, **Running** then **Finished**, each run a `GlassSurface`
  card: task id (title), prompt excerpt (body, one line), `mode k=N ·
  phase` or `adopted agg1`, status chip (`running` accent, `ok` `GC.ok`,
  `partial/cancelled` `GC.textDim`, failures `GC.down`), cost right-aligned
  in `GC.mono`, a thin progress bar of settled rows while running.
- Card tap → `/harness/run/:id`. Card `⋯` menu: Cancel (running) / Delete
  (finished) behind an `AlertDialog` confirm that says what goes (the run's
  sessions and directory; the library is kept).
- Empty state: "No harness runs yet" + "Refine the harness for a task: open
  a session and use ✦, or start one here." with a **New run** button.
  Loading: skeleton cards. Error: `loadError` text with Retry. Disconnected
  / ACP: the page explains Harness needs Server or Remote mode, with a
  button to Settings (mirrors Source Control's disconnected state).

### 5.3 Start flow — dialog

One `AlertDialog`-sized glass dialog with three steps, never leaving it:

1. **Task** — multi-line field prefilled from the session (or empty from
   `/harness`), task id and domain fields (prefilled: slug of the prompt,
   slug of the workspace folder; editable, validated client-side with the
   server's slug rule, errors from the server shown inline).
   Workspace: read-only line showing the session's directory or the
   server's working directory (Remote mode: "on the server").
2. **Budget** — a segmented list of the budgets (S4) with
   `N rollouts · M edits` and the one-line description; executor model via
   the existing `showModelPicker` (default: the session's model); an
   *Advanced* disclosure for editor model, library backend
   (`auto/local/cloud`) and cross-domain.
3. **Plan** — calls `startHarness(dryRun: true)` and shows the server's
   answer verbatim: budget line, backend and reason, planned rows grouped by
   phase, and "Rollouts run on copies of the workspace; your files are not
   touched." **Start** is the only button that spends; **Back** returns to
   step 2. A `409` (already running) offers **Open run** instead.

On start: close, navigate to `/harness/run/:id`.

### 5.4 Run view — `/harness/run/:id`

- Header: task id, status chip, cost, `mode k=N`, backend chip with the
  reason as tooltip; actions **Cancel** (running), **Install…** and
  **Publish skills…** (finished `ok`; Publish only when backend is `split`
  or `cloud`), `⋯` → Delete.
- Body: a vertical **phase timeline** — Base rollouts, Propose and probe,
  Compose, Select, Reflect — each a section with its rows as compact tiles:
  state glyph (pending ○, editing/rolling animated ◐ in `GC.accent`,
  edited ~, ok ✓ `GC.ok`, failed ✗ `GC.down`), label, detail (one line,
  ellipsized), cost. The adopted final carries an "adopted" accent badge.
  The current phase's header is highlighted while running.
- Outcome card (finished): adopted label, *why* (the selector's reasoning,
  markdown), the lesson (cue → lesson) or "no transferable lesson", and the
  run's error when it did not finish.
- Tile tap → stage detail (§5.5). Live: tiles update in place from the
  notifier; no spinner over the whole page after first load.

### 5.5 Stage detail — side sheet / page

Wide windows: a right-hand sheet (≈ 480 px) beside the run view, so the plan
stays visible while reading a stage; narrow: pushed page.

- Header: label, phase, state, cost; **Open session** (→
  `/session/:sessionID`, the rollout's own timeline — already a supported
  route) when the stage has a rollout session.
- Tabs: **Harness** — file list (+/~/−) and the harness diff rendered with
  the existing `parseUnifiedDiff` + `DiffView`; **Notes** — editor notes as
  markdown, or the fallback error; **Rollout** — the deliverable: the final
  message as markdown and the workspace diff (extracted from the
  deliverable's ```diff fence) through the same `DiffView`, with
  "nothing delivered" when not delivered.
- Refreshes when its row's state changes (provider invalidation, §4.3).

### 5.6 Install / publish — dialog

1. Calls `installHarness(dryRun: true, to, dir: <project dir>)` (S1).
2. Shows: target folder, file list (+/~/−), *Replaces the harness installed
   there now* when `conflict`, collisions ("your own skill x keeps
   precedence"), warnings (always-on cap) in `GC.warn`; for publish, Library
   skills new / changed / unchanged, with changed ones listed as *will be
   replaced*.
3. **Install** (or **Replace and install** when `conflict`) sends
   `force: conflict`; **Cancel**. Result: SnackBar "Installed into
   `<dir>/.gocode/harness` — new sessions use it."
4. "Already holds this harness" short-circuits to a SnackBar with no
   dialog — the TUI's behaviour.

### 5.7 Experience library — `/harness/library`

- Header: backend chip (`local` / `split` / `cloud` + reason tooltip),
  **Sync** button when the backend is `cloud`.
- Domains as collapsible sections (count in the header), entries as rows:
  task, cue (one line), components summary, "pending upload" badge.
- Entry tap → detail page/sheet: lesson card (cue, lesson, evidence,
  confidence), *why adopted* (markdown), *what each edit did* (monospace),
  and the harness tree as an expandable file list with each file's content
  (markdown files rendered, scripts/JSON in `GC.code`).
- **Forget** behind a confirm. Empty state: "No lessons yet — an entry is
  stored when a run adopts a harness and its record supports a lesson."

### 5.8 Accessibility and desktop polish

- Every interactive element keyed (`HarnessKeys.*`) for tests, with
  tooltips and semantic labels (state glyphs carry the state name).
- Keyboard: `⌘N`/`Ctrl+N` on `/harness` opens New run; `Esc` closes the
  stage sheet; `⌘↵` confirms the start and install dialogs (the composer's
  convention).
- Animated ◐ respects `MediaQuery.disableAnimations`.
- Narrow windows (< `sidebarWideBreakpoint`): sheet becomes a page; cards
  stack; nothing scrolls horizontally except diffs.

## 6. Phased delivery

Each phase is a PR that ships something usable, keeps `flutter analyze` and
`flutter test test/` green, and updates `documentation/11-desktop.md`.

| Phase | Scope | Server | Est. |
|---|---|---|---|
| **1. Read-only runs** | models, client reads, `HarnessRunsNotifier` (events + reconcile + poll), `/harness` list, `/harness/run/:id` plan + outcome, sidebar row with live badge, run-end SnackBar, ACP/disconnected states | S2, S3 | 2–3 days |
| **2. Start a run** | start dialog (task → budget/model → plan → confirm), session-header entry, `409` handling, cancel and delete | S4 | 2 days |
| **3. Stage detail** | stage sheet/page with harness diff, notes, rollout deliverable + diff, open-session link, live refresh | — | 1–2 days |
| **4. Install, publish, library** | install/publish dialog, library screen and entry detail, forget, sync | S1 | 2 days |
| **5. Sweeps (optional)** | "Run a sweep" from a folder of task files on the server, sweep progress view | S5 | 2–3 days |

## 7. Testing

- **Unit** (`test/harness_models_test.dart`): every model's `fromJson` from
  payloads captured from the Go server tests (keep them as fixtures under
  `test/fixtures/harness/` so a wire change breaks a test, not the app);
  `rowsByPhase` ordering; budget parsing.
- **Notifier** (`test/harness_state_test.dart`): a fake controller with a
  controllable event stream — `harness.*` folding replaces by id and inserts
  new runs; a run leaving `running` sets `lastEnded` once; reconnect triggers
  a reconcile; polling starts only while something runs and stops after;
  ACP (null client) yields the unavailable state.
- **Widget** (`test/harness_screen_test.dart`, `harness_start_test.dart`,
  `harness_install_test.dart`): a `_FakeHarness` `MockClient` server in the
  `_FakeGit` style recording every request — list grouping and empty /
  error states; the start flow sends `dryRun: true` first and spends only on
  Start, with the chosen mode/k/model; install sends `dryRun` then `force`
  only after the conflict was shown; delete/cancel/forget sit behind their
  confirms; the stage sheet renders the diff; the sidebar row hides in ACP
  mode.
- **Integration** (`integration_test/harness_flow_test.dart`, opt-in like
  `gocode_server_test.dart`): a real `gocode serve` with the fake
  OpenAI-compatible provider used by `cmd/gocode/harness_exec_test.go`
  (catalog via `GOCODE_MODELS_PATH`), a k=1 run started from the GUI, watched
  to `ok`, installed into a temp project.

## 8. Documentation

- `documentation/11-desktop.md`: a **Harness²** section after Source Control
  (entry points, what each screen does, Server/Remote only), with
  screenshots captured once Phase 4 lands.
- `gocode_gui/README.md` layout table: `lib/features/harness/`.
- `documentation/08-http-api.md`: the S1–S5 additions.
- `HARNESS2_PLAN.md` §10: the desktop client row.

## 9. Open questions

1. **Where the sidebar entry lives**: a top-level row (proposed) or inside
   the account menu with Usage/Settings. A top-level row makes a long
   background run discoverable; the menu keeps the sidebar quieter.
2. **Workspace choice in Remote mode**: offer only the session's directory
   and the server's directory (proposed), or add a server-side directory
   browser route — more capable, but a new filesystem surface on a remote
   server.
3. **Sweeps** (Phase 5) need task files on the server. Upload them from the
   GUI, or only point at a folder that already exists there?
4. **Notifications**: the in-app SnackBar only (proposed), or also the
   a system notification when the window is unfocused — useful for
   10-minute runs. The tray integration (`tray_manager`) only sets the icon
   and menu today, so this needs a notifications package or platform channel:
   a new dependency, against constraint 5.
