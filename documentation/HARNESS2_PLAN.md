# Harness² in gocode — implementation plan

[← Documentation index](README.md)

---

Status: **implemented** on `feature/harness2` — Phases 0–8 and §7. §10 records
where the code differs from this plan and what is left.

Source: [google-research/harness2](https://github.com/google-research/harness2)
(Apache-2.0, paper arXiv:2610.14224). File references below of the form
`harness2/core/*.py` point into that repository.

## 1. What Harness² is

A *harness* is everything around the model: prompts, tools, memory, skills,
subagents, scripts, plugins. Harness² improves the harness for a task **without
changing model weights and without ground truth** — only from execution
evidence.

### 1.1 The harness tree (`core/tree.py`, `substrate/opencode.py`)

Eight editable components:

| Path | Role | In context |
|---|---|---|
| `systemprompt.md` | role and procedure | always on |
| `guardrails.md` | standing rules | always on |
| `LongTermMEMORY.md` | durable domain facts | always on |
| `tool_descriptions/tool_guidance.md` | tool usage | always on |
| `skills/<n>/SKILL.md` | reusable workflow | on demand |
| `sub_agents/<n>/AGENT.md` | delegated subagent | own context |
| `scripts/<n>.py` | deterministic helper | run via bash |
| `plugin/<n>.ts` | middleware in the tool loop | always registered |

Always-on text is capped at 24 000 characters. Edits are
*omitted-is-inherited*: a proposal emits only what it changes.

### 1.2 Task-level recursion: Propose → Probe → Compose (`core/recursion.py`)

1. **Base**: roll the base harness `k` times on the task.
2. **Propose**: a harness editor reads the rollouts and proposes edits along two
   dimensions — **INV** (how the agent works: tools, procedure, completion) and
   **DOM** (what a practitioner knows: artifact contract, decision rules).
3. **Probe**: execute each candidate harness on the same task.
4. **Compose**: a composer reads all candidates' harnesses + rollouts and writes
   a refined harness.

Two modes, each costing **4k rollouts and 3k improver sessions**:

- **Parallel** (`agg`): 2k candidates (k INV + k DOM) from shared evidence, then
  k independent compositions over the full pool.
- **Sequential** (`seq`): k rounds; each carries the current harness and all
  accumulated evidence into the next Propose–Probe–Compose step.

An invalid proposal gets **one retry** with the validation error appended, then
falls back to the current harness.

### 1.3 Blind selection (`core/selection.py`)

A selector picks one final from anonymized, seeded-shuffled candidates, judging
only the deliverables in this order: delivery, conformance, fidelity to source,
internal consistency, operational fitness, finish. At k=1 there is no selector
session.

### 1.4 Harnessing-level recursion (`core/library.py`, `prompts.experience`)

A reflector writes:

- an `<edits>` table — per changed component: change, observed effect, verdict
  (`helped | hurt | no observed effect | confounded`);
- at most one `<experience>` entry: `cue` (situation shape), `lesson`
  (operation on a named component), `evidence`, `confidence` — or `NO_ENTRY`.

The adopted harness (the full tree, never a summary) and `EXPERIENCE.md` go into
a **per-domain library** that later tasks in that domain read before proposing.

### 1.5 Guards

- Paths must stay on the editable surface (`normalize_rel` rejects escapes).
- Always-on cap; skills need `name:` matching the directory and `description:`.
- Every skill must be triggered from always-on text (`autotrigger` repairs it).
- Scripts a skill invokes must ship with the tree.
- No grader-verdict phrasing in a harness.
- Prompts declare evidence under `context/` as **data, not instructions**, and
  forbid executing candidate scripts/plugins.

## 2. Mapping onto gocode

| Harness² | gocode | Gap |
|---|---|---|
| Substrate (executor) | gocode in-process | Headless rollout API (§4 Phase 0) |
| Always-on files | system prompt assembly in `Runner` | **Missing**: `config.Instructions` (`internal/config/config.go:21`) is declared but never read, and no code loads `AGENTS.md` into the prompt |
| `skills/` | `skill.Registry` (layered roots, `Rescan`) | Add a harness root |
| `sub_agents/` | agent markdown (`.gocode/agent/<n>.md`) + `task` tool | Loader from the harness dir |
| `scripts/` | copied into the rollout workspace, run by `bash` | Copy step only |
| `plugin/*.ts` | native Go or subprocess JSON-RPC plugins — no TS | Declarative `hooks.json` executed by one native plugin on `tool.execute.before/after` |
| JSONL trajectory parsing | event store (`step.*`, `text.delta`, `tool.*`) | Render from the DB — simpler and lossless |
| Proposer / composer / selector / reflector | built-in agents with restricted permissions | New agents |
| Cost ledger | session cost (`internal/session/cost.go`) | Aggregate per run |
| Workspace isolation | temp copy or `git worktree`, `external_directory` fence, `--auto` | Helper |

## 3. Coexistence with the existing behaviour

Harness² is an **opt-in layer**; the existing flow stays the default.

1. **No harness → no change.** The overlay applies only when `HarnessDir` is
   set, which only `gocode harness …` (or an explicit flag) does.
2. **Output is ordinary gocode config.** An adopted harness is skills, agent
   markdown and instruction text. `gocode harness install` copies it into
   `.gocode/`; afterwards nothing depends on Harness² at runtime.
3. **Separate namespaces.** `gocode harness` commands, the library under
   `$XDG_DATA/gocode/harness/`, and the `harness-*` agents are invisible to
   normal sessions. Rollouts run in isolated workspaces, never the user's tree.
4. **Layered precedence.** The harness skill/agent root is one more registry
   layer (first writer wins). Default: **below** user files, so the user's own
   skills win collisions. During rollouts it ranks **above**, so the candidate
   under test is what runs.

Touch points:

- **Phase 0 instruction injection changes defaults** (sessions start seeing
  `AGENTS.md` / `instructions`). This is opencode parity, but ship it with an
  opt-out (`GOCODE_DISABLE_INSTRUCTIONS=1`) and a release note.
- **Install collisions**: refuse to overwrite existing skills/agents without
  `--force`; always show a diff.
- **Always-on budget**: warn when installed always-on text plus `AGENTS.md`
  exceeds the 24k cap.
- **Kill switch**: `GOCODE_DISABLE_HARNESS=1` or `"harness": {"enabled": false}`.

## 4. Implementation phases

### Phase 0 — prerequisites (valuable on their own)

1. **Instruction injection**: load `AGENTS.md` and `config.instructions` into the
   system prompt, porting `packages/opencode/src/session/instruction.ts`.
2. **Harness overlay**: `RuntimeOptions.HarnessDir`, applied in `bootStack()`:
   always-on files appended after the agent prompt and before
   `<available_skills>`; a skill root; an agent root; a scripts path.
3. **In-process rollout API**, extracted from the non-interactive path of
   `cmd/gocode/cmd_run.go`:

   ```go
   harness.Rollout(ctx, RolloutSpec{TaskPrompt, Workdir, HarnessDir, Model}) (RunView, error)
   // RunView{SessionID, Workdir, OK, Delivered, Error}
   ```

### Phase 1 — `internal/harness/tree`

`Tree` (`map[string]string`), `Surface` (gocode's surface spec), `ReadTree`,
`WriteTree`, `NormalizeRel`, `Validate`, `AutoTrigger`, `DiffSummary`,
`Diversity`. Port the rules of `core/tree.py` together with its tests.

### Phase 2 — evidence

- `RenderTrajectory(sessionID)` from the event store into the shared grammar
  (`[ASSISTANT]`, `[THINKING]`, `[ACTION]`, `[OBSERVATION]`, `[ERROR]`), with
  the same truncation markers as `render_events`.
- Deliverable extractor: default = workspace `git diff` + final assistant
  message; configurable globs.
- `context.Build(dir, …)` producing
  `context/{task.md, rollouts/, deliverables/, harness/, library/, pool/}`.

### Phase 3 — prompts and improver agents

- Port `core/prompts.py` as embedded templates, citing the source per the
  porting conventions. Add the Apache-2.0 attribution (NOTICE).
- Built-in agents `harness-editor`, `harness-selector`, `harness-reflector`:
  workdir = the context dir; editor may write only `context/harness/**`;
  `bash` denied for all three.
- Reply parsers: `<notes>`, `` ```choice ``, `<why>`, `<edits>`, `<experience>`.

### Phase 4 — `internal/harness/recursion`

`RunParallel` / `RunSequential` with retry-then-fallback, a rollout semaphore
(each rollout is its own session, so `Coordinator` already allows concurrency),
and a ledger that asserts the 4k/3k identity.

### Phase 5 — selection, reflection, library

- Anonymized ids with a seeded shuffle (`assign_ids`).
- **Experience library** (named so to avoid confusion with the gocoder.org
  Library) behind an `ExperienceStore` interface with two backends, `local` and
  `cloud`, chosen per run as described in §7. The local layout, which is also
  the cache layout for `cloud` (improvers read files either way):
  `$XDG_DATA/gocode/harness/<domain>/{INDEX.md, harnesses/<task>/{harness/, EXPERIENCE.md}}`.

### Phase 5b — gocoder.org Library integration (§7)

Backend detection, cloud skills as frozen environment and as evidence, cloud
experience store, publish on install. Local-only behaviour from Phase 5 must
keep working unchanged when the plugin is absent.

### Phase 6 — CLI

```sh
gocode harness run --task task.md --dir ./ws --k 1 --mode parallel|sequential --domain X [--dry-run]
gocode harness library ls|show|forget|sync
gocode harness install <run> [--force] [--to project|library|both]
gocode harness run … [--library auto|local|cloud]
```

- Resume by re-running the same command: `summary.json` merged under an flock,
  namespace `<domain>-<mode><k>[-s<seed>]`.
- `--dry-run` prints the cost: 4k rollouts, 3k improver sessions, selector,
  reflector.

### Phase 7 — HTTP API and TUI (§5, §6)

### Phase 8 — plugin component

Declarative `hooks.json` rules (deny/require patterns on tool calls) executed by
a native plugin. Generated executable plugins are out of scope: letting an
improver model write code that runs inside the tool loop is a large security
surface.

## 5. HTTP API

The TUI is an HTTP client, so every TUI feature below needs an endpoint.

| Endpoint | Purpose |
|---|---|
| `POST /api/harness/run` | Start a run `{task, dir, k, mode, domain, model}` as a background job; returns run id |
| `GET /api/harness/run/{id}` | Phase, per-stage status, cost so far, finals |
| `POST /api/harness/run/{id}/cancel` | Stop; completed stages stay cached for resume |
| `GET /api/harness/run/{id}/stage/{label}` | Stage tree, diff vs base, notes, rollout session id |
| `POST /api/harness/run/{id}/install` | Install adopted harness; returns conflicts unless `force` |
| `GET /api/harness/library` · `DELETE /api/harness/library/{domain}/{task}` | Browse / forget |
| SSE `harness.stage.started`, `harness.stage.ended`, `harness.selected`, `harness.reflected` | Progress |

Rollouts are real sessions and already stream as session events. If harness
runs become durable events, follow the ordering rule: define the event →
register a projector → publish.

## 6. TUI

### 6.1 Entry points

- `/harness` — list dialog: Run · Runs · Library · Install.
- `/harness refine` — uses the **current session** as the task (first user
  prompt → `task.md`, session directory → workspace).
- Command palette: "Harness: run…", "Harness: library".

### 6.2 Views (reusing existing patterns from `TUI_RECOMENDATIONS.md`)

1. **Run dialog** (huh input form): task, domain (autocomplete from the
   library), k, mode, model, with a cost line before confirm:
   "k=2 parallel → 8 rollouts, 6 editor sessions".
2. **Progress panel**, modelled on the subagent footer (§8.5):

   ```
   ◐ harness · parallel k=2 · $1.84
     base    ✓ b1  ✓ b2
     probe   ✓ inv1 ◐ dom1 · inv2 · dom2
     compose · agg1 · agg2
   ```

   `enter` opens that rollout's session; `esc` returns.
3. **Stage detail**: the diff viewer (§6.5) fed the harness-tree diff against
   the base; component tree on the left, editor `<notes>` above the diff.
4. **Selection / reflection result**: read-only panel (§9.7) — chosen
   candidate, `<why>`, the edits/verdicts table, the lesson.
5. **Install confirm** (§9.6): files to be written, collisions flagged, each
   openable as a diff.
6. **Library browser**: list dialog of domains → tasks; an entry opens its
   lesson and full harness; `d` forgets. The title carries the active backend
   (`experience library · cloud` / `· local`); entries still in the upload
   spool are marked pending.

When the `cloud` backend is active, the run dialog's cost line also names it,
and the install confirm offers "also publish skills to Library", with each
skill shown as new / changed / unchanged against `.skills/`.

### 6.3 Typical flow

Work in a session → result is mediocre → `/harness refine` → confirm cost →
keep working or watch the panel → toast "harness adopted: agg2" → review diff
and lesson → Install → later sessions in the project use the new skills and
guardrails.

### 6.4 Order

API + SSE (shared engine with the CLI) → progress panel with rollout drill-in →
stage diff + install → library browser. New views and dialogs update
`TUI_RECOMENDATIONS.md` in the same commit.

## 7. gocoder.org Library integration (library-plugin)

Rule: **if library-plugin is available, Harness² uses it as a first-class
backend; if not, everything runs on the local experience library exactly as in
Phase 5.** The choice is made once per run, recorded in `summary.json`
(`"backend": "cloud" | "local"` plus the reason), and never changes mid-run.

### 7.1 Availability check

Run at the start of `gocode harness run` (and by `GET /api/harness/library`):

1. `plugin.Host.Status()` lists an instance with id `library-plugin` in state
   `loaded` whose tools include `library_skill_search` and `library_skill_store`.
2. `gocoder.LoadAccount()` returns a stored `gk_` key, and
   `Client.CheckKey` accepts it (one cheap call).
3. `"harness": {"library": "auto" | "local" | "cloud"}` in config, overridable
   by `--library`. Default `auto`:
   - `auto`: checks 1–2 pass → `cloud`; any fails → `local`, with one notice line
     naming the failed check ("experience library: local — library-plugin not
     loaded").
   - `local`: never touches gocoder.org, even when the plugin is loaded.
   - `cloud`: a failed check is a hard error before any rollout is paid for.

The engine talks to gocoder.org through `internal/gocoder.Client` directly —
structured types, same `gk_` key, the `baseURL` read from the plugin's options —
rather than parsing the plugin's text tool output. The plugin process stays what
the rollout and improver *agents* use as tools.

### 7.2 What changes when the backend is `cloud`

| Point | Local | Cloud (tight) |
|---|---|---|
| **Rollout environment** | user's on-disk skills only | Library skills advertised by the plugin are part of the fixed environment. At run start the engine snapshots `{name → SkillContentHash}`; at the end it re-checks, and a changed set marks the run `skills_drifted` so comparisons and resume can tell. Rollouts keep the plugin loaded — the adopted harness is tested against the skills actually used. |
| **Editor evidence** | `context/library/` (same-domain entries) | Additionally `context/cloud-skills/`: top-N `SearchLibrarySkills` (discover mode) hits for the task text, written read-only. The editor may copy one into `context/harness/skills/` — the layout (`<name>/SKILL.md`, `references/`, `scripts/`) is identical, so it validates like any other skill. |
| **Experience store** | `$XDG_DATA/gocode/harness/<domain>/` | Source of truth is the Library under the reserved prefix `.harness/<domain>/` (same layout). The local directory becomes a read-through cache, so improvers still read plain files and offline reads work. |
| **Writes after reflection** | write local entry | Write the cache entry, then upload. A failed upload goes to a local spool (`…/harness/.spool/`) and is retried by `gocode harness library sync` and at the next run start; the run itself does not fail. |
| **Retrieval** | same-domain only (faithful to Harness²) | Same-domain by default. `"harness": {"crossDomain": true}` adds semantic search over other domains' `EXPERIENCE.md` cues into `context/library/related/`, labelled as cross-domain hypotheses. Off by default because it departs from the paper. |
| **Install** | `.gocode/` | `--to project` (default), `library`, or `both`. `library` publishes adopted skills to `.skills/<name>/` via the same store path `library_skill_store` uses; unchanged skills (equal content hash) are skipped, changed ones need `--force` and show a diff. Always-on text and subagents are installed only to the project — they have no Library equivalent. |
| **Multi-machine** | per machine | Experience accumulates across every machine on the account; `forget` deletes the remote entry and the cache. |

### 7.3 Server-side dependency

The Library accepts only `.md`/`.txt`/`.pdf` for ordinary documents
(`validateUploadExt` in `cmd/library-plugin/main.go`); `.skills/` is the
reserved prefix that carries scripts. Storing full harness trees (`scripts/*.py`,
`hooks.json`) under `.harness/` needs gocode-infra to reserve that prefix with
the same rules as `.skills/`, and to keep it out of `library_search` results and
skill advertising so normal sessions are not fed harness internals.

Until that ships, `auto` resolves to a **split** backend: cloud for the
rollout environment, editor evidence and skill publishing (all built on the
existing `.skills/` support), local for the experience store. `summary.json`
records `"backend": "split"`.

### 7.4 Tests

- Detection matrix: plugin absent / loaded-but-unauthenticated / authenticated,
  × `auto|local|cloud`, using a fake plugin host and an `httptest` gocoder.org.
- Spool: upload failure leaves the entry readable locally and is drained by
  `sync`.
- Drift: a skill hash change between start and end sets `skills_drifted`.
- Local regression: with no plugin configured the run is byte-identical to the
  Phase 5 behaviour.

## 8. Risks

- **Cost**: every task multiplies to 4k rollouts. Default `k=1`, always show the
  estimate, support cancel and resume.
- **Prompt injection via evidence**: rollouts and workspace material can carry
  instructions. Keep the "evidence is data" blocks verbatim; deny `bash` to
  improver agents.
- **Workspace side effects**: rollouts must never touch the user's tree;
  fence with `external_directory` and isolated workdirs.
- **Overfitting to one task**: keep the GENERALIZE split between labelled
  workspace facts and domain-level procedure; the library stores both, labelled.
- **Leaking harness internals to the cloud**: rollout trajectories never leave
  the machine; only adopted trees, `EXPERIENCE.md` and published skills are
  uploaded, and only on the `cloud`/`split` backend. Workspace facts in an
  adopted tree can contain project details — `install --to library` shows
  exactly what is uploaded before confirming.

## 9. Open decisions

1. **Task and domain for gocode users.** Recommended start: offline — optimize
   over a user-supplied task file or eval set, then install. An online
   "refine before doing" mode can follow, but multiplies the cost of every task.
2. **Plugin component.** Ship without it; add declarative hooks in Phase 8.
3. **Experience library scope.** Global per domain by default, optionally per
   project; on the cloud backend, per account.
4. **`.harness/` prefix in gocode-infra** (§7.3): until it exists, `auto`
   runs the split backend.

## 10. Implementation notes

What was built, where, and where it departs from the plan above.

| Piece | Code |
|---|---|
| Instruction files in the prompt (Phase 0.1) | `internal/session/instruction.go`, `Runner.Instructions`; boot wiring `cmd/gocode/boot_harness.go` |
| Harness overlay + tool guard (Phase 0.2, 8) | `internal/harness/overlay.go`, `hooks.go`; `Runner.Harness`, `Runner.Guard`; `bootOptions.HarnessDir` |
| Rollouts and improver sessions (Phase 0.3, 3) | `cmd/gocode/harness_exec.go` — one stack per session over the shared DB; improver agents registered only in improver stacks |
| Tree, validation, diff (Phase 1) | `internal/harness/tree.go` |
| Evidence (Phase 2) | `internal/harness/trace.go`, `workspace.go`, `evidence.go` |
| Prompts (Phase 3) | `internal/harness/prompts.go`, `prompts/*.md` (attribution in `internal/harness/NOTICE`) |
| Recursion, selection, reflection, library (Phases 4–5) | `recursion.go`, `selection.go`, `library.go`, `run.go` |
| Library integration (Phase 5b, §7) | `internal/harness/cloud.go`; detection and wiring `cmd/gocode/harness_engine.go` |
| CLI (Phase 6) | `cmd/gocode/cmd_harness.go` — `run`, `sweep`, `runs`, `show`, `install`, `prune`, `validate`, `library ls\|show\|forget\|sync` |
| Sweeps over a task set | `internal/harness/sweep.go` (`run_sweep`/`run_domain`/`stream_order`): domains in parallel (`--workers`), each domain's tasks in stream order so each reads its predecessors' lessons; `--seed`, `--max-tasks`; record in `$XDG_DATA_HOME/gocode/harness/sweeps/` |
| API + TUI (Phase 7) | `internal/harness/service.go`, `internal/server/harness.go`; `internal/tui/dialogs_harness.go`, the aggregator's `harness.*` branch |

Departures from the plan:

- **Instructions opt-out** is the env var `GOCODE_DISABLE_INSTRUCTIONS=1`, not
  `"instructions": false` — the key is a list, and changing its type would
  break every config that sets it.
- **Surface layout** is gocode's own: `agent/<name>.md` (not
  `sub_agents/<name>/AGENT.md`) and flat `tool_guidance.md`, so a harness
  directory is directly a skill root and an agent root.
- **`hooks.json` runs in the runner** (`Runner.Guard`), not as a native
  plugin: a plugin's `tool.execute.before` can only rewrite arguments, and a
  deny rule has to be able to refuse the call.
- **Scripts are not copied into the workspace.** The overlay tells the agent
  where `scripts/<name>` resolves (the harness directory, readable through the
  file tools' allow-list and `external_directory`), so a rollout's change never
  contains harness files.
- **Rollouts are fenced harder than the plan said**: every agent in a rollout
  stack gets `external_directory: deny` except the harness directory, because
  `--auto` would otherwise approve the default `ask`.
- **Sweeps keep the library.** harness2 trims each domain's library to the
  entries preceding the current task (`Library.retain`) because its
  libraries are per-run; gocode's is the user's own and outlives a sweep, so a
  task also reads entries earlier sweeps left.
- **Script syntax** is checked through the interpreters (`python3 -I` running
  `ast.parse` on stdin, `sh -n`), which parse without executing; a missing
  interpreter skips the check instead of rejecting the proposal.
- **One mode per run.** harness2 runs every mode over shared bases in one
  call; here a run is one mode, and the bases are shared through the cache
  when the other mode runs for the same task and k.
- **Cross-domain lessons** land in `context/related/`, not
  `context/library/related/`, so the same-domain library stays exactly what
  harness2 shows.
- **Stage detail** is a read-only panel (§9.7) carrying the harness diff, not
  the diff viewer: the diff viewer is bound to `/api/vcs`.
- **Split backend.** `auto` with library-plugin available resolves to
  `cloud` when the Library service lists `harness` in `GET /library/version`
  `features`, and to `split` (skills in the Library, experience on disk)
  against an older deployment. `cloud` can be forced; uploads the server
  rejects stay pending and `sync` retries them.

Server side (gocode-infra, branch `feature/harness-prefix`, not yet
deployed): `internal/library/harness.go` reserves `.harness/` — any file type,
5 MB per file, segments validated, 409 instead of dedupe — and plain search
excludes it unless `path` targets it; `GET /library/version` now reports
`features: ["skills", "harness"]`.

Operational notes:

- Rollout and improver sessions are ordinary sessions in the shared database,
  under their run's directories. `gocode harness prune <task>|--all`, the
  runs list's `remove ctrl+d`, and `DELETE /api/harness/run/{id}` delete them
  with the run (`session.DeleteUnder`); `prune --all --sessions-only` drops the
  sessions but keeps every run's evidence and summary. Do not prune while a
  run is going.
- Workspace copies are copy-on-write where the filesystem allows
  (`clonefile(2)` on APFS for the whole tree, `FICLONE` per file on btrfs/XFS),
  else a plain copy — a very large repository on another filesystem still
  costs a full copy per rollout. Source is copied into evidence only up to
  64 MB, past which a listing is written instead.
