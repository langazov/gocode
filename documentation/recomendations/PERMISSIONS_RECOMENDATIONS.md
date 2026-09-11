# Permissions Recommendations

[← Design recommendations](README.md) · [Documentation index](../README.md) · [Tools & permissions (architecture)](../05-tools-and-permissions.md)

---

A design specification for the permission system: what a rule means, where
rules come from, what a tool must declare when it asks, what each answer
commits the user to, and what every surface — TUI, HTTP, plugins, subagents —
must show and must never do.

This document is **normative**. Where it describes current behaviour it is
because that behaviour is the reference; where it says *should*, that is a rule
for new work. It complements [chapter 5, Tools & permissions](../05-tools-and-permissions.md)
(which explains the architecture) by specifying the *policy and interaction
contract*. The upstream TypeScript reference is `specs/permissions.md` at the
repository root of the opencode workspace.

Port gaps are marked **PORT GAP** with the upstream file to port from. They are
rules, not suggestions: closing one is work, leaving one open is a defect.

---

## Table of contents

1. [Principles](#1-principles)
2. [The model](#2-the-model)
3. [Policy sources](#3-policy-sources)
4. [The ask lifecycle](#4-the-ask-lifecycle)
5. [`external_directory`](#5-external_directory)
6. [Tool advertisement](#6-tool-advertisement)
7. [Subagents](#7-subagents)
8. [The prompt surface contract](#8-the-prompt-surface-contract)
9. [HTTP API surface](#9-http-api-surface)
10. [Auto-accept](#10-auto-accept)
11. [Bypass tiers](#11-bypass-tiers)
12. [Persistence and revocation](#12-persistence-and-revocation)
13. [Testing requirements](#13-testing-requirements)
14. [Anti-patterns](#14-anti-patterns)
15. [Checklists](#15-checklists)
16. [File map](#16-file-map)

---

## 1. Principles

These are the invariants every later section derives from.

| # | Principle | Why |
|---|---|---|
| P1 | **Unknown means ask.** `Evaluate` falls back to `Ask` when nothing matches (`permission.go:84-86`). | A tool nobody wrote a rule for must never run silently. |
| P2 | **A misconfigured agent is inert, not omnipotent.** Unknown agents get `MissingAgentPermissions` (deny-all). | Fail closed where the user expressed no intent. |
| P3 | **A configured deny is not a question.** It is checked before saved grants and before the `permission.ask` plugin hook (`runner.go:833-844`). | A rule that already says no is not something a plugin or a stale approval may overrule — that once switched off every deny in plan mode with nothing in the transcript to say it had. |
| P4 | **Saved approvals widen only what was asked.** A grant may not silently expand scope between sessions. | "Allow always" is trust with a stated boundary; moving the boundary is a new question. |
| P5 | **A denial fails the tool call, not the turn.** The model reads the reason and tries another approach. | The point is steering, not stopping. |
| P6 | **The user must be able to see what each answer commits them to** before answering. | An "allow always" whose scope is hidden is not consent. |
| P7 | **Every ask carries enough to be answered honestly** — the exact command, the diff, the URL, the directory, and who is asking. | The only safe reply to an unknown is reject. |
| P8 | **Pending asks are live, grants are durable.** Ask events are live-only (`ask_events.go:9-16`); the `permission` table survives restarts. | A replayed ask nobody can answer is noise; a grant that evaporates re-asks forever. |

---

## 2. The model

### 2.1 Rules and effects

```go
type Rule struct {
    Action   string  // what kind of access: "edit", "bash", "external_directory", an MCP tool name…
    Resource string  // what specifically: a path, path glob, URL, command text, subagent name, "*"
    Effect   Effect  // Allow | Deny | Ask
}
```

Keep this shape. It is the V2 upstream shape (`packages/schema/src/permission.ts`)
and the on-disk shape of saved grants.

### 2.2 Evaluation: last match wins, with a deny-first gate

```
Evaluate(action, resource, rulesets...) → last rule matching BOTH fields, else Ask
```

Evaluation order inside the engine (`permission.go:358-384`) is load-bearing and
must not be reordered:

1. Resolve the configured ruleset (session-stored, else the agent's, else
   deny-all — `AgentRulesProvider.Configured`).
2. **Deny gate**: if any resource evaluates `Deny` against configured rules
   *alone* → `BlockedError`. Saved grants and plugin hooks are never consulted.
3. Merge saved grants **after** configured rules; they may resolve an `Ask`
   into `Allow`, never a `Deny`.
4. Any remaining resource evaluating `Ask` → ask. All allow → allow.

Wildcard matching (`wildcard.go`) keeps both special cases: `\` normalises to
`/`, case-insensitive on Windows, and a pattern ending in `" *"` (note the
space) also matches the bare prefix — `git commit *` matches `git commit`. That
suffix rule exists for one consumer: arity-based bash saves (§4.4).

### 2.3 The `*` resource is a literal on the input side

`Evaluate("edit", "*", rules)` matches neither `*.env`-scoped allows nor denies.
Tools must pass real values; `"*"` is only legitimate as a *pattern in a rule*
or as the resource of a tool with genuinely nothing to scope (`todowrite`).
See §4.2 for how this is enforced.

---

## 3. Policy sources

Precedence, weakest to strongest. Later overrides earlier because evaluation is
last-match-wins:

```
built-in Defaults() → agent-specific rules → user global config →
agent config/markdown → session-stored ruleset → (runtime) saved grants
```

### 3.1 Built-in defaults

`permission.Defaults()` (`permission.go:42-65`) is the permissive baseline with
carve-outs. Current:

```go
{Action: "*",                    Resource: "*",             Effect: Allow},
{Action: "plan_enter",           Resource: "*",             Effect: Deny},
{Action: "plan_exit",            Resource: "*",             Effect: Deny},
{Action: ExternalDirectoryAction, Resource: "*",            Effect: Ask},
{Action: "read",                 Resource: "*.env",         Effect: Ask},
{Action: "read",                 Resource: "*.env.*",       Effect: Ask},
{Action: "read",                 Resource: "*.env.example", Effect: Allow},
```

**Rules:**

- Defaults apply to *every* agent, then per-agent rules narrow or re-allow
  (build re-allows `plan_enter`, plan re-allows `plan_exit` —
  `builtin_agents.go`). A subagent must never be able to move the session out
  from under the user, which is why the denies sit in the shared baseline.
- New carve-outs need a reason a reviewer can check. `*.env` asks because
  secrets live there; `.env.example` is allowed because it is committed and
  contains none.

**PORT GAP — `question` should be denied by default and re-allowed only for
build.** The `Defaults()` comment claims the `question` tool is unimplemented,
but `builtins/question.go` exists and registers — so today *any* agent,
subagents included, can interrupt the user with a question. Upstream denies it
in the baseline (`packages/opencode/src/agent/agent.ts:126-127`) and build
re-allows it (`agent.ts:148-150`). Add:

```go
{Action: "question", Resource: "*", Effect: Deny},
```

and `{Action: "question", Resource: "*", Effect: Allow}` to build only. The
stale comment goes in the same commit.

**PORT GAP — `doom_loop`.** After a run of identical consecutive tool calls the
turn should park on a `doom_loop` permission before continuing (upstream
threshold 3, `packages/opencode/src/session/processor.ts:29,356-380`). Default
`Ask`; "always" saves the tool name. The TUI tips file already advertises this
(`tips.go:86`) — today it advertises something that does not exist.

### 3.2 User config

```jsonc
{
  "permission": {
    "bash": "ask",                                  // whole action
    "external_directory": "ask",
    "read": { "*.env": "ask", "*.env.example": "allow" },  // per-resource
    "webfetch": "allow"
  }
}
```

`config/permission.go` converts this to rules. Keep the eager validation and
the **sorted key expansion**: Go map iteration is randomised, and an unsorted
`"read"` before/after `"*"` once made rule precedence nondeterministic — a
`"*": deny` key could randomly clobber a specific allow. `"*"` sorts first, so
specific rules correctly override it.

### 3.3 The `tools` map must actually work — PORT GAP (bug level)

`config.go:29` parses `"tools": {"bash": false}` and `tips.go:80` tells users
to set it — but nothing translates it into rules, so the tip advertises a
nonfunctional setting. Upstream converts it to permission rules merged *under*
the explicit `permission` block (so `permission` wins), collapsing
`write`/`edit`/`patch` onto `edit`
(`packages/opencode/src/config/config.ts:570-577`):

```
tools: { "write": false }  →  { Action: "edit", Resource: "*", Effect: Deny }
```

Port that translation into `bootStack` next to `userRules`, merge it *before*
`cfg.Permission` rules. Either the tip becomes true or the tip goes.

### 3.4 `GOCODE_PERMISSION` — PORT GAP

Upstream deep-merges `OPENCODE_PERMISSION` (JSON) into the permission config
for CI and one-shot runs (`config.ts:559-565`), skipping invalid JSON with a
warning. Port as `GOCODE_PERMISSION`. This is the prerequisite that makes
`--auto` (§11) safe to use, because it is how a pipeline states its denies.

### 3.5 Agent definitions

JSON config and markdown agents both feed `cfg.Agent`; markdown uses the same
`Permission` decoder (`agentmd.go:59-63`), so both definition styles get
identical semantics. Keep: one decoder, no parallel path. `agent create
--permissions read,grep` writing the same shape is correct.

### 3.6 Session-stored rulesets

A session may carry its own ruleset (`session.permission` column) which wins
over the agent's stock one. Two producers today: subagent derivation (§7) and
the parent-fallback in `spawn.go:79-94`. The parent fallback must keep its
current shape — reading only the stored column once handed plan-mode children
an empty floor, and every restriction plan mode exists to impose stopped at the
first `task` call.

**PORT GAP (optional)** — upstream also writes per-prompt tool toggles into the
session ruleset (`packages/opencode/src/session/prompt.ts:1060-1067`), so a
prompt sent with `tools: {bash: false}` narrows that session. Adopt only if a
UI for it ships; a wire field with no producer or consumer is worse than none.

---

## 4. The ask lifecycle

The runner's `executeTool` (`runner.go:790-855`) is the canonical sequence and
its order is normative:

```
rewrite tool arguments (plugins)
→ extra permissions declared by the tool (external_directory, edit-from-shell)
→ deny gate (configured rules only)
→ permission.ask plugin hook (may allow/deny explicitly; default defers)
→ Assert (ask = park on the user)
→ execute
```

### 4.1 Extra permissions are asked first

A tool that implies approvals beyond its own action declares them via
`tool.PermissionScoped` (`registry.go:127`). Bash is the reason the seam
exists: it can reach outside the root and can edit in-repo files, so
`ExtraPermissions` (`bash.go:160-193`) yields `external_directory` globs and
`edit` resources *before* the `bash` action is asked. Asking extras first means
a denial stops the command before the model sees a partial approval. Any new
tool with implicit reach must use the same seam — never ask inside `Execute`,
where a refusal reads as a tool failure instead of a decision.

### 4.2 Resources: a tool declares its extractor, or fails registration

`permissionResources` (`runner.go:937-975`) maps each tool to the input field
that carries its resource; `apply_patch` implements `tool.PermissionResourced`
because its targets are inside the patch text. Four tools once read
`input["path"]` — a field they do not have — so every URL-, query- and
path-scoped rule silently stopped applying to them, and for `apply_patch` that
was a bypass: `"edit": {"*.env": "deny"}` stopped the edit tool while the
identical change went through as a patch.

**Rules:**

- Every tool with a scorable input exposes its extractor (field mapping or
  `PermissionResourced`). The silent `"*"` fallback in `permissionResources`
  should exist only for tools that explicitly register as resourceless
  (`todowrite`, `question`) — a tool whose extractor is missing fails
  registration with a named error instead of escaping every scoped rule.
- A move reports both paths; the destination is as much a write as the source.

### 4.3 Metadata: every ask says what it is about — PORT GAP

`ToolPermissionInput` has no `Metadata`, so the engine's `Request.Metadata` is
always empty and the TUI renders `"No diff provided"` for edits
(`views.go:830`). Upstream tools attach display metadata to the ask
(`ctx.ask({... metadata: { filepath, diff }})` — `packages/opencode/src/tool/edit.ts:102-110`):

| Action | Metadata should carry |
|---|---|
| `edit` / `write` / `apply_patch` | `filepath` and the unified `diff` (per file for multi-file patches) |
| `bash` | `command` |
| `webfetch` | `url` |
| `websearch` | `query`, `provider` |
| `task` | `subagent_type`, `description` |
| `external_directory` | `directories` (the readable form of the globs) |

Add `Metadata map[string]any` to `ToolPermissionInput`, populate it in the
runner's input assembly (tools already know their inputs), and let it flow
through `Engine` to the wire (§8.3). This is what makes an edit prompt
answerable: a diff the user can read before approving.

### 4.4 Save granularity: what "always" means per action

`permissionSave` (`runner.go:977-983`) currently returns `["*"]` for almost
everything and the raw resource for `bash` and `skill`. The contract, per
upstream (the table in `05-tools-and-permissions.md:222-243` is the reference):

| Action | Resource asked | Saved by "always" |
|---|---|---|
| `read` · `edit` · `write` | the path | `*` |
| `apply_patch` | every file in the patch | `*` |
| `bash` | the full command | **command prefix + `" *"` (arity)** |
| `external_directory` | `dir/*` | `dir/*` |
| `glob` · `grep` | the pattern | `*` |
| `webfetch` | the URL | `*` |
| `websearch` | the query | `*` |
| `skill` | the skill name | that skill |
| `task` | the subagent type | `*` |
| `todowrite` | `*` | `*` |
| `doom_loop` | tool name | tool name |

The asymmetry is the point: for file tools the question a person answers is
"may you edit files", not "may you edit this path"; for `bash` and `skill` one
approval must not become "run anything" or "load any skill".

**PORT GAP — bash saves the exact command today.** `permissionResources`
returns `input["command"]` and `permissionSave("bash", …)` returns it verbatim,
so "allow always" on `git commit -m "x"` covers *only that literal command* and
the next commit re-prompts. Upstream truncates the command to its arity prefix
from a generated dictionary and appends `" *"`:
`BashArity.prefix(["git","commit","-m","x"])` → `"git commit"` → saved as
`"git commit *"`, which the wildcard suffix rule (§2.2) matches against every
variant of that subcommand (`packages/opencode/src/permission/arity.ts`,
`packages/opencode/src/tool/shell.ts:409`). Port the dictionary (and its
generator prompt, in the file header) as `internal/permission/arity.go`; keep
the *asked* resource as the full command so the prompt stays honest, and change
only `Save`.

### 4.5 Replies and cascades

`Engine.Reply` (`permission.go:290-324`) implements the upstream semantics and
must keep them:

- **`once`** — resolve; nothing remembered.
- **`always`** — write each `Save` pattern as an allow rule to the store, then
  re-evaluate *every other* pending request and auto-resolve those now fully
  allowed (`cascadeAllow`), skipping any whose configured rules deny it. This
  is why approving one request clears a queue of similar ones.
- **`reject`** — fail the deferred with `CorrectedError` when a message was
  given, else `ErrDeclined`, **and cascade-reject every other pending request
  in the same session** (`cascadeReject`). One rejection ends the turn's asks;
  a half-rejected turn is not a thing the model should steer around.

The error the model sees matters as much as the failure:

- **`BlockedError` should list the rules, not count them.** Today it renders
  `permission: blocked by 2 rule(s)`; upstream embeds the relevant rules so the
  model can read *why* and adapt (`PermissionV1.DeniedError` message,
  `packages/core/src/v1/permission.ts:21-27`). Render `relevant()`'s rules
  (action, resource, effect) into the error string.
- `CorrectedError.Feedback` must reach the model verbatim — it is the user
  telling it what to do differently. The engine supports it; the TUI cannot
  send it yet (§8.5).

### 4.6 Interruption

Cancelling the ask's context removes the pending request and returns
`ctx.Err()` (`permission.go:239-243`). No cascade on interruption — the turn's
abort settles its own tools (`failInterruptedTools`). Keep: an interrupted
turn is not a user rejection and must not be recorded as one.

---

## 5. `external_directory`

The guard that took real work; the rules that made it correct are now
contracts:

1. **Parse, never pattern-match.** `shellscan.go` walks a real shell AST
   (`mvdan.cc/sh/v3/syntax`): redirects, subshells, `&&`/`|` chains. Regexes
   cannot decide what a path even is.
2. **Canonicalise both sides before comparing.** On macOS `/var` is a symlink
   to `/private/var`; a naive prefix check let `/var/...` escape a
   `/private/var/...` root. This was a real reported bug.
3. **Unresolvable is unreported.** A path built from a variable or command
   substitution is not flagged — this is a guard against the common case, not a
   sandbox. The permission system, not the scan, is the boundary, and the
   docstring saying so stays.
4. **Grants are subtrees, never files.** The resource is `dir/*`, and since `*`
   compiles to `.*` it covers everything under `dir`. Approving one file would
   re-ask for the next; approving sideways is impossible — the grant widens
   down only.
5. **Shell writes inside the repo are held to the edit rules.** `ScanWrites`
   yields `edit`-action extras so `edit: deny` (plan mode) survives `echo x >
   file`. Before it, a plan-mode session could and did rewrite files through
   the shell. It changes nothing under the default ruleset — only where a rule
   already restricts editing does the shell start being held to it too.
6. **`writeCommands` is a subset of `pathCommands`.** `cat foo` and `cd foo`
   name paths without changing them; reporting those as writes made plan mode
   refuse to read. Keep the subsets separate.

**PORT GAP — file tools should ask, not refuse.** `Resolver.Resolve`
(`builtins.go:31-51`) hard-fails any path outside the root (minus the plans
allowlist) with `path escapes working directory`. Upstream file tools instead
raise an `external_directory` ask and proceed on approval
(`packages/opencode/src/tool/external-directory.ts:35-44`): the model can *ask
to read* `/etc/hosts`, the user can say yes, and the grant is remembered as a
subtree. Today the only route to an outside file is bash, which means the
policy that most needs the ask (deliberate external access) is the one that
never gets it. Add an `ExternalPaths` seam to the resolver-aware tools: resolve
fails → tool returns the paths it wanted → runner asks `external_directory`
for their directory globs via the extra-permission path, then re-resolves with
the approved directory added to `Allow`.

---

## 6. Tool advertisement

**PORT GAP.** Today every registered tool is advertised regardless of rules
(`runner.go:417-427`): in plan mode the model is still offered `edit`, calls
it, and burns a round-trip on a denial. Upstream removes a tool from the
advertisement when the *last* rule for its action is `{resource: "*",
deny}` (`Permission.disabled` / `visibleTools`,
`packages/opencode/src/permission/index.ts:204-219`):

```
edit/write/apply_patch collapse to "edit"; MCP resource tools collapse to "read";
hide the tool iff the collapsed action's last matching rule is {"*", deny}
```

Rules:

- Hide only on the **blanket** deny (`resource: "*"`, last match). A
  path-scoped deny (`"edit": {"*.env": "deny"}`) keeps the tool advertised —
  the model should call it and learn the boundary from the `BlockedError`.
- Compute per resolved agent, at request-assembly time, the same way the
  permission gate does — advertisement and enforcement must read one ruleset,
  or the model will be offered tools the gate then refuses.
- The `task` tool's description lists available subagents; filter that list by
  `Evaluate("task", name, …)` so a denied subagent is not suggested
  (upstream `describeTask`, `packages/opencode/src/tool/registry.ts:265-278`).
- Hidden is not removed: the tool still executes through the same gate if
  called anyway (defence in depth, and it keeps MCP registrations stable).

---

## 7. Subagents

`DeriveSubagentPermissions` (`subagent_permissions.go:33-43`) and
`parentFloor` (`:54-62`) implement the inheritance contract. Order is the
whole point, and it used to be the other way round — subagent-last let its own
`"*": allow` baseline re-grant everything the parent had denied:

```
subagent's own rules → parent's denies + external_directory allows (the floor) →
explicit denies for SubagentDeniedTools (task, todowrite) unless the subagent opted in
```

**Rules:**

- Only **restrictions** travel down, plus `external_directory` *allows* — they
  are the parent's answer to "which directories may be touched at all", which
  a child cannot sensibly re-derive. A parent's other allows are its own.
- The floor must come from the parent's **effective** ruleset (stored session
  ruleset first, agent rules as fallback — `spawn.go:79-94`). Reading only the
  stored column handed plan-mode children an empty floor.
- A subagent opts into `task`/`todowrite` by naming the action explicitly; a
  `"*": allow` baseline does not count as an opt-in (`mentions`,
  `subagent_permissions.go:68-70`).
- Add new subagent-denied actions (upstream adds `memory_write`,
  `memory_delete` — gocode already has) to `SubagentDeniedTools`, never to the
  shared `Defaults()`, so only subagents are narrowed.

---

## 8. The prompt surface contract

The TUI banner (`permissionBanner`, `views.go:606-671`) is the primary
answering surface. Visual specifics (borders, budget, glyphs) belong to the
[TUI recommendations](TUI_RECOMENDATIONS.md); this section fixes the
*content and interaction* contract.

### 8.1 Deterministic order — PORT GAP

`Engine.List`/`ForSession` return map-iteration order, so "the first pending
request" — which the banner shows — is random among concurrent asks. Upstream
sorts by id (`packages/opencode/src/cli/cmd/run/session-data.ts:301`). Return
pending requests in creation order (a slice alongside the map, or sort by id)
so the oldest ask is always answered first.

### 8.2 The three answers, and what each commits to

| Answer | Commits | Persistence |
|---|---|---|
| Allow once | this one call | none |
| Allow always | every call matching the **Save** patterns (§4.4), this project | durable (`permission` table) |
| Reject | this call, plus every other pending ask in the session | none |

The current banner text for `external_directory` ("Allow always grants these
directories and everything under them, for this project") is the standard every
action must meet. **"Allow always" on an edit currently grants `edit: *` —
every file, forever, in the project — and the banner does not say so.** That
violates P6 and is the single most important surface fix.

### 8.3 What the banner must show

**PORT GAP** — the wire type must carry it first: `tui/client.PermissionRequest`
decodes only `id/sessionID/agent/action/resources`. The server already
serialises the full `permission.Request` (including `Metadata`, `Save`,
`Source`); the client struct is the gap. Decode all three, then:

- **The Save patterns, verbatim**, under the always option —
  `Allow always → git commit *`, `→ edit *`, `→ /srv/data/*`. Derived from
  `Save`, never inferred from the resources (they differ by design, §4.4).
- **The diff for edit/write/apply_patch**, from `Metadata.diff` (§4.3), budgeted
  like any body with a `… N more lines` clamp. A file-change approval the user
  cannot read is a rubber stamp.
- **The full command for bash** (`Metadata.command`), not the first resource —
  one command may reach several directories and the approval covers all.
- **Subagent attribution** — the existing `permissionTitle` behaviour (child
  session title + `(@agent)` suffix) is the reference; keep it. With several
  sessions asking concurrently, an unlabeled prompt is ambiguous about who is
  blocked.
- **Source correlation**: `Source.CallID` lets the banner attach the ask to the
  running tool call in the timeline. Optional to render, mandatory to carry.

### 8.4 "Allow always" requires a confirmation step — PORT GAP

Today `enter` on *Allow always* grants immediately. Upstream's TUI inserts a
confirmation stage whenever the reply is `always`
(`packages/opencode/src/cli/cmd/run/permission.shared.ts:174-224`), and for good
reason: `always` is the only answer with durable consequences. Add a stage
after selecting *Allow always* that shows exactly what will be saved (the
patterns from §8.3) with **Confirm / Cancel**, `esc`/`cancel` returning to the
option bar. Do not gate `once` or `reject` behind confirmation — friction
belongs where the commitment is.

### 8.5 Reject should accept a reason — PORT GAP

The engine and HTTP body already carry `message`
(`permission.go:290`, `server.go:184-187`); only the TUI never sends one. Port
the upstream reject stage: choosing *Reject* opens a small text input ("Tell
the agent what to do differently"), submit sends
`{"reply":"reject","message":…}`, `esc` rejects without a reason
(`permission.shared.ts:226-232`). The text becomes `CorrectedError.Feedback`
and is shown to the model verbatim — it is steering, and it is the difference
between the model learning "not this way" and merely learning "no".

### 8.6 Keyboard

Existing bindings are the reference: `←/→/h/l` shift, `enter` confirm, `esc`
reject/back, `y/a/n` quick aliases (`app.go:2124-2140`). The new stages extend
rather than replace: `esc` inside always-confirmation returns to the option
bar; `esc` inside the reject input rejects without a reason; `enter` submits.

### 8.7 Settled asks must be announced — PORT GAP

`Hooks.OnAsked` is wired in `bootStack` but `OnReplied` is not
(`main.go:441-445`), so a cascade rejection or a sibling auto-resolve is
invisible to every client until the next poll. The question flow already has
both events (`question.asked`/`question.settled`, `ask_events.go:17-21`).
Publish `session.next.permission.settled` from `OnReplied` — live-only, like
its sibling — covering cascaded resolutions (the engine calls the hook for
each victim, `permission.go:461-463`; keep that).

---

## 9. HTTP API surface

Existing routes (`server.go:81-84`):

```
GET  /api/permission/request                          list all pending
GET  /api/session/{sessionID}/permission              list session's pending
POST /api/permission/{requestID}/reply                reply
POST /api/session/{sessionID}/permission/{requestID}/reply
```

**Rules and gaps:**

- **The session-scoped reply route must verify ownership.** Today
  `replyPermission` reads only `requestID` and ignores the `sessionID` path
  value — the route implies a scoping it does not enforce. Resolve the request,
  404 when its `SessionID` differs from the path.
- **Add the saved-grant surface — PORT GAP.** `SavedPermissions.Forget` exists
  but nothing calls it, and there is no way to see or revoke a single grant.
  Upstream exposes both
  (`packages/protocol/src/groups/permission.ts:36-59`):

  ```
  GET    /api/permission/saved[?projectID=…]   → [{id, action, resource, …}]
  DELETE /api/permission/saved/{id}
  ```

  Add `Remove(id)` to the store (single-row delete) and keep `Forget` for the
  "revoke everything" command. See §12.
- **Replies must be idempotent-safe.** Replying to an already-settled request
  returns 404 (`NotFoundError`); clients treat that as success, not an error —
  the TUI already clears the banner optimistically on keypress
  (`app.go:2147-2150`). Document it, don't "fix" it.
- **The permission reply endpoints are the most sensitive routes on the
  server** — answering one is consenting to agent side effects. When the server
  stops being loopback-only (`gocode serve`, `attach`), they require the same
  auth token as every other mutating route. A world where any local process can
  POST an "allow always" is a world where the permission system decorates
  rather than gates.

---

## 10. Auto-accept

**PORT GAP.** Upstream clients can opt a session (and its subagent lineage) or
a whole directory into automatic approval
(`packages/app/src/context/permission.tsx`, `permission-auto-respond.ts`).
The design rules, before the feature:

1. **Auto-accept answers `once`, never `always`.** A toggle that writes durable
   grants would turn "stop asking me" into "approve everything forever" —
   scope creep the user never consented to (P4).
2. **It is client-side and persisted per directory + session**, not a server
   rule: it is an answering policy, not a policy change. Upstream keys it
   `base64(directory)/sessionID` and `base64(directory)/*`, with subagent
   lineage inheriting the parent's setting.
3. **It must respect the deny gate** — auto-accept operates on pending asks;
   a configured deny never produces an ask, so no client can auto-accept past
   it. This falls out of the engine; state it so nobody "fixes" it.
4. **The status bar shows the state** whenever it is on (the TUI
   recommendations own the placement). An invisible auto-accepter is a trap.
5. A toggle command (`/permissions auto`, or a keybind following the keyboard
   model) enables/disables for the active session+directory; enabling drains
   currently-pending asks immediately, versioned so a rapid off/on/off cannot
   race late replies into the new epoch (upstream `enableVersion`,
   `permission.tsx:327-332`).

## 11. Bypass tiers

One knob today: `--auto`/`--yolo`/`--dangerously-skip-permissions` sets
`Runner.Permissions = nil` — no gate, **and no deny enforcement**. That is a
correct reading of the flag's name and the wrong default for the future. The
tiers, weakest to strongest:

| Tier | Flag | Semantics |
|---|---|---|
| Default | — | ask/allow/deny per rules |
| Config injection | `GOCODE_PERMISSION='{"edit":"deny"}'` (§3.4) | still default flow; rules deep-merged under file config |
| Auto-answer asks | `--auto` | **asks are answered `once` automatically; configured denies still enforced** |
| Full bypass | `--dangerously-skip-permissions` alone | gate removed entirely, current behaviour |

Rules:

- Moving `--auto` to auto-answer semantics requires the deny gate to keep
  running: implement as a `PermissionGate` whose `Assert` resolves to nil on
  `Ask` after the deny check — not as `Permissions = nil`. A CI pipeline that
  states `"edit": {"*.env": "deny"}` via `GOCODE_PERMISSION` must be able to
  trust it under `--auto`; that combination is the entire point of the tier.
- `--yolo` remains an alias of full bypass and keeps its reputation. Do not
  soften it; do not document it without the word *dangerous*.
- Full bypass should print one line saying the gate is off. Silent security
  downgrades are how flags end up in every Makefile.

## 12. Persistence and revocation

The store (`session/permission_saved.go`) is the reference; keep:

- **Project scope, not session.** A directory approved once stays approved in
  every later session in the worktree, across restarts. The unique index on
  `(project_id, action, resource)` makes re-approval a no-op — two concurrent
  tool calls can be approved with the same rule.
- **The read cache.** Evaluation reads the table several times per tool call
  and this process is the only writer; cache and invalidate on `Add`
  (`Add` sets `loaded=false`). Any new writer (the HTTP remove route, §9)
  invalidates the same way.
- **Saved grants are allow rules only**, merged after configured rules (§2.2).
  Never write a deny or an ask into the table — it is a grant store.

**Gaps:**

- **Per-grant revoke** — add `Remove(id)`; `Forget()` (all-or-nothing) stays as
  the nuclear option. Both back the HTTP surface (§9) and the TUI command
  below.
- **A `/permissions` command** in the TUI listing saved grants for the current
  project — action, resource, granted-when — each revocable. Today the only way
  out of a bad grant is deleting the row by hand or forgetting everything.
  Persisting a mistake with no UI to see it is how projects end up with
  `bash: *` granted in 2024 and nobody remembering why.

## 13. Testing requirements

Every rule above that can decay gets a test that fails when it does. The
existing suite (`internal/permission/permission_test.go`, 15 tests) is the
base. Required additions, each named for the invariant it guards:

| Test | Guards |
|---|---|
| `TestConfiguredDenyBeatsSavedAllow` | §2.2 ordering: saved grants resolve asks, never denies (upstream equivalent: `core/test/permission.test.ts:232-251`) |
| `TestBashAlwaysSavesArityPrefix` | §4.4: `git commit -m a` approved-always, `git commit -m b` does not re-ask, `git push` does |
| `TestRejectCascadesSameSession` | §4.5: one rejection settles every sibling ask of that session, none of other sessions |
| `TestAlwaysCascadesCoveredSiblings` | §4.5: approving covers pending requests now fully allowed; skips ones whose configured rules deny |
| `TestUnmappedResourceFailsRegistration` | §4.2: a tool with no extractor cannot register (once the fallback is removed) |
| `TestShellWriteHeldToEditDeny` | §5.5: `edit: deny` survives `echo x > file` in-repo |
| `TestExternalPathCanonicalised` | §5.2: `/var` vs `/private/var` (regression for the real bug) |
| `TestBlanketDenyHidesAdvertisement` | §6: `{"*", deny}` removes the tool; path-scoped deny keeps it |
| `TestSubagentFloorRestrictsPlanChild` | §7: plan-mode parent's denies reach the child; child's `"*": allow` does not lift them |
| `TestReplyRouteRejectsForeignSession` | §9: session-scoped reply 404s on mismatch |
| `TestSavedRemoveInvalidatesCache` | §12: a revoked grant stops applying on the next evaluation |
| `TestListIsCreationOrdered` | §8.1: concurrent asks present oldest-first |
| `TestDeniedErrorListsRules` | §4.5: the model-facing error names action/resource/effect |

E2E shape (existing pattern): real files in `t.TempDir()`,
`builtins.Register(registry, workdir, nil)`, a scripted provider that calls the
tool — `TestExternalDirectoryAlwaysIsAskedOnce` is the exemplar and its
two-defects history is why it exists.

## 14. Anti-patterns

Each of these happened. Cite them.

| Anti-pattern | What broke |
|---|---|
| **Asking inside `Execute` instead of `ExtraPermissions`** | A refusal read as a tool failure; the model retried instead of steering. Extras are asked before the action for exactly this reason (`runner.go:796-821`). |
| **Reading a resource field the tool does not have** | `webfetch`/`websearch`/`skill`/`apply_patch` all fell through to `"*"`, silently escaping every scoped rule; for `apply_patch` it was a working bypass of `"edit": {"*.env": "deny"}`. |
| **Saving the exact bash command as the grant** | "Allow always" covered one literal command; the next commit re-prompted. Users read it as the prompt being broken. (§4.4) |
| **Runner never setting `Save` / `nil` SavedStore** | Two independent defects, either sufficient: "allow always" did nothing at all. `TestExternalDirectoryAlwaysIsAskedOnce` fails if either is reverted. |
| **Prefix-checking paths without canonicalising** | On macOS `/var` is a symlink to `/private/var`; bash wrote outside the working directory without prompting. (§5.2) |
| **Unsorted map iteration building a ruleset** | Go randomises map order; `"*": deny` randomly landed before or after specific allows, making policy nondeterministic per run. (§3.2) |
| **Subagent rules merged last** | The subagent's `"*": allow` baseline re-granted everything the parent denied; plan mode's restrictions stopped at the first `task` call. (§7) |
| **Plugin `permission.ask` allowed to answer a configured deny** | Any plugin returning "allow" switched off every deny in the ruleset — plan mode's read-only constraint included — with nothing in the transcript. Hence the deny gate runs first. (P3) |
| **Advertise every tool, deny at call time** | Plan mode offered `edit`; the model called it and burned a round-trip. Hide blanket-denied tools instead. (§6) |
| **`OnReplied` unwired** | Cascade rejections invisible until the next poll; clients show a parked session that has already moved on. (§8.7) |
| **A tip advertising a setting that does nothing** | `tips.go:80` tells users to set `"tools": {"bash": false}`; nothing reads it. (§3.3) |
| **Writing a durable grant from a toggle** | Not yet done here — upstream keeps auto-accept as `once`-only answers for this reason. Do not be the first. (§10.1) |

## 15. Checklists

### Adding a tool

- [ ] Name the action (defaults to the tool name; collapse only true aliases like `edit`/`write`/`apply_patch`).
- [ ] Declare the resource extractor — a field mapping in `permissionResources`, or `PermissionResourced` when the targets are nested (patches, multi-file).
- [ ] Declare `Save` following the §4.4 table; if the action needs a new row, justify the granularity in the table's commit.
- [ ] Attach `Metadata` per §4.3 so the banner can show the command/diff/URL.
- [ ] If the tool can reach outside the root or imply edits, implement `PermissionScoped.ExtraPermissions`.
- [ ] Add the registration-failure test if the extractor could be missing (§13).
- [ ] Update the §4.4 table in `05-tools-and-permissions.md` and here in the same commit.

### Adding an action or changing defaults

- [ ] State the carve-out's reason in a comment next to the rule (`permission.go`'s existing comments are the standard).
- [ ] Subagent-only restrictions go in `SubagentDeniedTools`, never shared `Defaults()`.
- [ ] Check both directions: does any agent need to *re-allow* it (`question`, `plan_enter`)?
- [ ] Add the rule to `availablePermissions` in `cmd_agent.go` if users can grant it.

### Touching the prompt surface

- [ ] Every new answer path states what it commits to (§8.2).
- [ ] The always-confirmation shows the Save patterns, not the resources (§8.4).
- [ ] `esc` always retreats one stage; it never grants (§8.6).

## 16. File map

| Concern | File |
|---|---|
| Engine: evaluate, ask/assert/reply, cascades | `internal/permission/permission.go` |
| Wildcard matcher (incl. the `" *"` suffix rule) | `internal/permission/wildcard.go` |
| In-memory store/rules fakes for tests | `internal/permission/memory.go` |
| **Arity dictionary (PORT GAP, §4.4)** | `internal/permission/arity.go` — port of `packages/opencode/src/permission/arity.ts` |
| Saved-grant store (SQLite, project-scoped, cached) | `internal/session/permission_saved.go` |
| Gate adapter + rules provider | `internal/session/runner_permission.go` |
| Ask lifecycle in the runner (extras → deny gate → hook → assert) | `internal/session/runner.go` (`executeTool`, `permissionResources`, `permissionSave`) |
| Subagent derivation + floor | `internal/session/subagent_permissions.go`, `spawn.go` |
| Shell AST scan (external paths, in-repo writes) | `internal/tool/builtins/shellscan.go` |
| Tool seams: `PermissionScoped`, `PermissionResourced` | `internal/tool/registry.go` |
| Defaults + built-in agents | `internal/permission/permission.go` (`Defaults`), `cmd/gocode/builtin_agents.go`, `cmd/gocode/main.go` |
| Config → rules (sorted expansion) | `internal/config/permission.go` |
| HTTP routes | `internal/server/server.go` |
| Ask events (live-only) | `internal/session/ask_events.go`, `cmd/gocode/ask_events.go` |
| TUI banner, title/body, keybinds | `internal/tui/views.go`, `internal/tui/app.go`, `internal/tui/components.go` |
| TUI client wire type (**PORT GAP, §8.3**) | `internal/tui/client/client.go` |
| Plugin hook | `internal/plugin/hooks.go` (`permission.ask`), `internal/session/runner_plugins.go` |
| Upstream reference | `specs/permissions.md` (opencode workspace root) |
