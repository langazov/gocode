# 5. Tools & permissions

[← Providers](04-providers.md) · [Index](README.md) · [Next: The TUI →](06-tui.md)

---

Tools are how the agent touches the world. Permissions are what stop it.

## The tool contract

Four methods (`internal/tool/registry.go`):

```go
type Tool interface {
    Name() string
    Description() string
    InputSchema() map[string]any     // JSON Schema, sent to the model
    Execute(ctx context.Context, input map[string]any) (string, error)
}
```

`Description` and `InputSchema` are **prompt engineering**, not documentation —
they are literally what the model reads to decide whether and how to call the
tool. A vague description produces a misused tool.

Tools needing to know which session called them implement an optional second
interface:

```go
type SessionAware interface {
    ExecuteWithContext(ctx context.Context, input map[string]any, exec ExecContext) (string, error)
}
```

`todowrite` uses this — a todo list belongs to a session, and the base
interface deliberately doesn't leak session identity to tools that don't need
it.

The registry advertises tools in **sorted order** (`Names()`), so the prompt
sent to the model is byte-identical across runs. That matters for provider-side
prompt caching: unstable ordering would miss the cache on every request.

## The built-ins

13 tools, registered by `builtins.RegisterWith`:

```mermaid
mindmap
  root((tools))
    Read
      read
      glob
      grep
    Write
      write
      edit
      apply_patch
    Execute
      bash
    Reach out
      webfetch
      websearch
    Coordinate
      task
      todowrite
      skill
      question
```

| Tool | Does | Notes |
|---|---|---|
| `read` | read a file | line-numbered; reports LSP diagnostics after |
| `write` | create/overwrite | diagnostics after |
| `edit` | exact string replacement | fails if the target isn't unique |
| `apply_patch` | multi-hunk structured patch | for larger coordinated edits |
| `glob` | find files by pattern | `doublestar`, honours ignore rules |
| `grep` | search file contents | |
| `bash` | run a command | permission-gated, see below |
| `webfetch` | fetch a URL | |
| `websearch` | search the web | |
| `task` | spawn a sub-agent | its own session; see [runner](03-session-runner.md) |
| `todowrite` | maintain a task list | session-scoped |
| `skill` | load a skill's instructions | |
| `question` | ask you a question | blocks the turn on your answer |

Registration is conditional — `todowrite` needs a database, `skill` needs a
skill registry, `question` needs an asker, and the plan tools need an agent
switcher. A headless run without a UI simply doesn't advertise `question`,
rather than advertising a tool that would hang.

## The permission engine

Three effects, and a default that matters:

```go
Allow  Effect = "allow"
Deny   Effect = "deny"
Ask    Effect = "ask"
```

Evaluation (`permission.Evaluate`) walks every ruleset and returns the **last**
match — later rules override earlier ones — falling back to **`Ask`** when
nothing matches:

```go
if match == nil {
    return Rule{Action: action, Resource: "*", Effect: Ask}
}
```

Unknown means ask. A tool nobody wrote a rule for prompts rather than silently
proceeding.

```mermaid
flowchart TD
  A["tool call"] --> B["Evaluate(action, resource)"]
  B --> C{"effect"}
  C -->|allow| D["execute"]
  C -->|deny| E["refuse, tell the model why"]
  C -->|ask| F["publish permission request"]
  F --> G["client shows dialog"]
  G --> H{"your answer"}
  H -->|once| D
  H -->|always| I["persist rule"] --> D
  H -->|reject| E

  style D fill:#065f46,stroke:#047857,color:#ecfdf5
  style E fill:#7f1d1d,stroke:#dc2626,color:#fee2e2
```

A denial fails *the tool call*, not *the turn*. `Engine.Assert` returns an
error, `executeTool` propagates it, and the runner publishes `tool.failed`
carrying the reason. The model reads that as a failed tool and can try another
approach — the session does not abort.

### Defaults

```go
{Action: "*",                  Resource: "*",             Effect: Allow},
{Action: ExternalDirectoryAction, Resource: "*",          Effect: Ask},
{Action: "read",               Resource: "*.env",         Effect: Ask},
{Action: "read",               Resource: "*.env.*",       Effect: Ask},
{Action: "read",               Resource: "*.env.example", Effect: Allow},
```

Read as a sequence: permissive by default, then carve out the dangerous cases.
Leaving the working directory asks. Reading `.env` asks — but `.env.example`,
which is committed and contains no secrets, doesn't.

An agent with no permission configuration at all gets
`MissingAgentPermissions` — deny everything. A misconfigured agent is inert,
not omnipotent.

### `external_directory`

This is the guard that took real work to get right. A tool reaching outside the
working directory must ask — but "outside" is harder than it looks:

```sh
cat > /tmp/x.go        # obviously outside
cd /tmp && cat > x.go  # the cd hides it
cat > ../../etc/hosts  # relative escape
```

`internal/tool/builtins/shellscan.go` parses the command with
[`mvdan.cc/sh/v3/syntax`](https://pkg.go.dev/mvdan.cc/sh/v3/syntax) — a real
shell parser, not a regex — and walks the AST for path arguments to commands in
`pathCommands`.

Both the root and the candidate path are **canonicalised** before comparison,
which on macOS matters more than you'd expect: `/var` is a symlink to
`/private/var`, so a naive prefix check lets `/var/...` escape a
`/private/var/...` root.

> This was a real reported bug: bash wrote outside the working directory
> without prompting. Regexes cannot fix it, because the shell grammar decides
> what a path even is.

#### The grant is a subtree, not a file

The scan reports **directories**, and the permission resource is a glob over
one — `/srv/data/*`, never `/srv/data/one.txt`. Since `*` compiles to `.*`,
which crosses `/`, approving `/srv/data/*` also covers `/srv/data/sub/deeper/`
and everything in it. A sibling like `/srv/other/` is not covered: the grant
widens down, never sideways.

> A second reported bug lived here: the prompt reappeared on almost every
> command, because "allow always" was doing nothing. Two independent defects,
> either one sufficient to cause it — the runner never set `Save` on the
> permission input, so `Engine.Reply`'s `len(Save) > 0` guard skipped the
> write; and `main.go` passed a `nil` `SavedStore`, so there was nowhere to
> write to. `TestExternalDirectoryAlwaysIsAskedOnce` fails if either is
> reverted.

### Rules from config

```jsonc
{
  "permission": {
    "bash": "ask",
    "external_directory": "ask",
    "read": { "*.env": "ask", "*.env.example": "allow" },
    "webfetch": "allow"
  }
}
```

### Rules saved at runtime

Answering **"allow always"** writes to the `permission` table
(`internal/session/permission_saved.go`), scoped to the **project**, not the
session — so a directory approved once stays approved in every later session in
the same worktree, across restarts.

What gets saved is the request's `Save` list, which is deliberately not the
same as the resources being asked about. Both columns match what the
TypeScript tool passes to `permission.assert`:

| Action | Resource (asked about) | Saved by "always" |
|---|---|---|
| `read` · `edit` · `write` | the path | `*` |
| `apply_patch` | **every file the patch touches** | `*` |
| `bash` | the command | that command |
| `external_directory` | `dir/*` | `dir/*` |
| `glob` · `grep` | the pattern | `*` |
| `webfetch` | the URL | `*` |
| `websearch` | the query | `*` |
| `skill` | the skill's name | that skill |
| `task` | the subagent type | `*` |
| `todowrite` | `*` | `*` |

The asymmetry is the point. For the file tools the question a person answers is
"may you read files", not "may you read this one path" — saving the path would
re-ask for the next file. For `bash` and `skill` it inverts: one approval must
not become "run anything" or "load any skill".

> Getting the **resource** column wrong is not cosmetic. An unmapped field
> falls through to `"*"`, and `"*"` on the *input* side of a match is a
> literal, not a wildcard: `Evaluate("edit", "*", rules)` matches neither an
> allow nor a deny written against a path pattern. Four tools were reading
> `input["path"]`, a field they do not have — `webfetch`, `websearch`, `skill`
> and `apply_patch` — so every URL-, query- and path-scoped rule silently
> stopped applying to them. For `apply_patch` that was a bypass: `"edit":
> {"*.env": "deny"}` stopped the edit tool while the identical change went
> through as a patch.

`apply_patch` is the one tool whose resources are not one input field away —
its targets are inside the patch text — so it implements
`tool.PermissionResourced` and parses them out. A move reports both paths;
the destination is as much a write as the source.

`permission.Rule`s built from the table are merged in after the agent's
configured rules, and `Evaluate` takes the **last** match, so a saved allow
beats a configured ask — but never a configured `deny`, which is checked first
and on its own.

## The permission round trip

Permissions are asynchronous. The runner publishes a request and waits; some
client answers over HTTP:

```mermaid
sequenceDiagram
  participant R as Runner
  participant DB as SQLite
  participant S as Server
  participant T as TUI
  participant U as You

  R->>DB: permission request
  R->>R: block this tool call
  DB-->>S: event
  S-->>T: SSE
  T->>U: dialog
  U->>T: "allow once"
  T->>S: POST /api/permission/{id}/reply
  S->>DB: record reply
  DB-->>R: unblock
  R->>R: execute the tool
```

Because it round-trips through the store, **any** client can answer — the TUI,
a script hitting the API, a web UI. And the request survives a client
disconnect: reconnect and the dialog is still there.

`--auto` (aliases `--yolo`, `--dangerously-skip-permissions`) drops the gate
entirely by setting `stack.Runner.Permissions = nil`. It is exactly as
dangerous as it sounds and is meant for throwaway containers.

## Writing a tool

```go
type MyTool struct{}

func (t *MyTool) Name() string        { return "my_tool" }
func (t *MyTool) Description() string { return "Does the thing. Use when..." }

func (t *MyTool) InputSchema() map[string]any {
    return map[string]any{
        "type": "object",
        "properties": map[string]any{
            "target": map[string]any{
                "type": "string",
                "description": "What to operate on",
            },
        },
        "required": []string{"target"},
    }
}

func (t *MyTool) Execute(ctx context.Context, input map[string]any) (string, error) {
    target, _ := input["target"].(string)
    return "did the thing to " + target, nil
}
```

Then `registry.Register(&MyTool{})` in `builtins.go`.

Three things to get right:

1. **Return errors as errors.** The runner turns them into `tool.failed` events
   and shows the model the message — so write messages *for the model*.
2. **Respect `ctx`.** Cancellation is how interrupt works; a tool ignoring it
   blocks Ctrl-C.
3. **Go through `Resolver`** for filesystem access, so path restrictions and
   the `external_directory` gate apply. Bypassing it bypasses permissions.

---

[← Providers](04-providers.md) · [Index](README.md) · [Next: The TUI →](06-tui.md)
