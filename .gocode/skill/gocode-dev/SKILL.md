---
name: gocode-dev
description: Develop on the gocode codebase itself — build, test, add features across packages (tools, events, HTTP API, TUI), write tests with the repo's fixtures, and follow porting conventions. Use when the task is changing Go code in /Users/emilo/Work/GitHub/opencode/go, as opposed to configuring gocode (that is the gocode-config skill).
---

Workflow reference for changing code in this repository (the Go port of
opencode). AGENTS.md is the summary; this is the working depth behind it.

## Build and verify

- `make check` — fmt-check + vet + test. This is what CI runs; run it before
  pushing, not just `go test`.
- `go test ./internal/session/...` — one package.
- `go test -run TestRunnerInterrupt ./internal/session/` — one test.
- `go test -race ./...` — CI mode. `internal/tui/...` is the slow package
  (~30s); scope the run while iterating.

Full CI matrix: Ubuntu/macOS/Windows × fmt/vet/test/build
(`.github/workflows/ci.yml`). Windows matters here: an open SQLite handle
blocks `t.TempDir` cleanup, so any test that opens a `*db.DB` must register
`database.Close()` in `t.Cleanup`.

## Adding a feature end-to-end

The stack boots in `cmd/gocode/main.go` `bootStack()` in strict dependency
order. A feature that spans layers usually touches, in this order:

1. **Event** (`internal/session/run_events.go` and friends): define the event
   type. Ordering requirement: define → register a projector → publish. A
   published event with no projector commits fine and updates nothing — the
   divergence check cannot catch it.
2. **Session/runner** (`internal/session/`): the agent loop lives in
   `runner.go`; `Coordinator[string]` serializes per session ID, sessions run
   concurrently.
3. **Tool** (`internal/tool/builtins/`): implement `tool.Tool`
   (`Name/Description/InputSchema/Execute`), register in
   `builtins.RegisterWith` (called from `bootStack`). Tools cannot import
   `internal/session` (session imports tool) — cross-layer seams go through
   interfaces like `tool.Spawner`, wired in `bootStack`.
4. **HTTP API** (`internal/server/`): add the route. Rule: if it is not in
   the HTTP API, the TUI cannot use it — the TUI is always a client of the
   loopback server `bootStack()` starts, even locally.
5. **TUI** (`internal/tui/`): Bubble Tea; talk to the server, never to
   services directly.

Background goroutines must never write to stderr while the TUI is up (it
owns the alternate screen) — use `global.LogBackground()`.

## Testing patterns (the repo's fixtures)

Session-layer tests (`internal/session/*_test.go`):

- `setup(t)` → `(*event.Bus, *db.DB)`: temp SQLite DB, bus, projectors
  registered, session `ses_1` seeded in project `prj_1`.
- `newRunnerFixture(t, provider, tools)` → `(*Runner, *event.Bus)`: fully
  wired Runner.
- `admitPrompt(t, bus, runner, text)`: queue work before `runner.Run()`.
- Providers are faked with `fakeProvider` (scripted `[][]llm.StreamEvent`),
  tools with `fakeTool`.

Server tests (`internal/server/*_test.go`): `newTestServer(t)` then
`doJSON(t, server, method, path, body)`.

DB in tests: `db.OpenAndMigrate(ctx, filepath.Join(t.TempDir(), "test.db"))`.
Never `:memory:` with a pool.

E2E: create real files under `t.TempDir()` and use real tool execution
(`builtins.Register(registry, workdir, nil)`).

## Where to look things up

| Question | File |
|---|---|
| Package ownership | AGENTS.md package table |
| Event model / projections | `documentation/02-data-model.md`, `internal/event/` |
| Runner loop | `documentation/03-session-runner.md` |
| Providers/transforms | `documentation/04-providers.md` |
| Tools & permissions | `documentation/05-tools-and-permissions.md` |
| HTTP API | `documentation/08-http-api.md` |
| Config precedence | `internal/config/loader.go` |
| LSP servers | `internal/lsp/servers.go` |

## Porting conventions

This is a port from TypeScript (see `packages/opencode/src` sibling tree).
Non-obvious behavior should cite its TS source file in a comment. When
behavior looks wrong, check the cited file before "fixing" — it is usually
faithful. Deliberate divergences are listed in
`documentation/10-development.md` (no runtime npm install, no MCP prompts,
unmatched commands report instead of swallow).

## Gotchas that cost real time

- Version ldflags target `internal/installation`, not `main` — a `-X
  main.version` silently does nothing (the symbol does not exist).
- `bootStack()` runs after `global.Init()` has created XDG dirs; do not
  reorder them.
- Model resolution precedence: explicit flag > last-used per-directory >
  config `"model"` > built-in `anthropic/claude-sonnet-4-5`.
- Permission rules are last-match-wins over merged rulesets; `permission.Defaults()`
  is the `"*": allow` baseline every agent gets.
- Plugin tools register after built-ins (a plugin may replace one by name);
  plugins load before config-derived objects are built so their config hook
  can mutate the config.
