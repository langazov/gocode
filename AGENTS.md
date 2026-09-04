# AGENTS.md — gocode

A Go port of the [opencode](https://github.com/anomalyco/opencode) coding agent. One statically linked binary, zero runtime deps.

## Quick commands

| Command | What it does |
|---|---|
| `make build` | Build `./gocode` binary |
| `make test` | Run full test suite |
| `make check` | fmt-check + vet + test — **what CI runs** |
| `make fmt` | Format with `go fmt ./...` |
| `make vet` | Static analysis |
| `make release` | Optimized build into `dist/` |

Run `make check` before pushing. It mirrors CI exactly.

## Single test/package

```sh
go test ./internal/session/...                      # one package
go test -run TestRunnerInterrupt ./internal/session/  # one test
go test -race ./...                                  # CI mode (with -race)
```

## Architecture

### The HTTP server split

The TUI is always an HTTP client — even locally. `bootStack()` starts an ephemeral loopback server, then the TUI connects to it over HTTP+SSE. `gocode attach http://host:port` runs the same code path pointed elsewhere. **If a feature isn't in the HTTP API, the TUI can't use it.**

Entry point: `cmd/gocode/main.go` → `bootStack()` assembles DB, event bus, tools, providers, agents, permissions, LSP, MCP, skills, commands in dependency order.

### Event-sourced core

Everything the agent does becomes a durable event in SQLite before visible state. A turn is a sequence of `step.started → text.delta* → tool.called → tool.success → step.ended` events committed atomically with projections. The event bus (`internal/event/`) is the source of truth — projections run inside the commit transaction.

**Adding a durable event has an ordering requirement**: define the event → register a projector → publish it. A published event with no registered projector commits successfully and updates nothing. The divergence check only detects projections that disagree, not ones that never ran.

### Package boundaries

| Package | Owns |
|---|---|
| `cmd/gocode/` | CLI entrypoint, subcommands, `bootStack()` |
| `internal/session/` | Agent loop: runner, coordinator, compaction, event definitions |
| `internal/llm/` | Provider clients (anthropic, openai, gemini, openairesponses) |
| `internal/tool/` | Tool registry + 13 builtins in `builtins/` |
| `internal/permission/` | Allow/deny/ask rules engine |
| `internal/event/` | Event store, bus, replay, projections |
| `internal/db/` | SQLite schema, migrations, connection pool |
| `internal/server/` | HTTP API routes |
| `internal/tui/` | Bubble Tea interface |
| `internal/lsp/` | 27 built-in language servers, lazy-started |
| `internal/mdlsp/` | Markdown language server (`cmd/mdlsp`): actor-based, goldmark-backed |
| `internal/jsonrpc/` | Shared Content-Length JSON-RPC connection (client + server) |
| `internal/lspprotocol/` | Shared LSP wire types |
| `internal/mddoc/` | Markdown document model: headings, links, UTF-16 positions |
| `internal/mcp/` | MCP server connections, reconnect, tool import |
| `internal/plugin/` | Plugin host: hook catalog, native + subprocess tiers, loader |
| `internal/skill/` | Skill discovery from markdown files |
| `internal/agent/` | Agent registry, default selection |
| `internal/config/` | JSONC config merge, agent markdown parsing |
| `internal/provider/` | Catalog, auth, transforms |
| `internal/modelsdev/` | models.dev catalog (snapshot baked in at build) |

### Runner loop

`Runner.Run()` in `internal/session/runner.go` drains eligible durable work for one session. `Coordinator[string]` serializes execution per session ID while allowing different sessions to run concurrently. `Execution` routes session-ID keyed execution to a process-local runner.

### SQLite and concurrency

- Uses `modernc.org/sqlite` (pure Go, no CGO). Database runs in WAL mode.
- `DB.write` is a buffered channel acting as a write semaphore — only one writer at a time. `Query`/`QueryRow` never acquire it; `Exec`/`Transaction` always do.
- Tests use `db.OpenAndMigrate(ctx, filepath.Join(t.TempDir(), "test.db"))` — never `:memory:` with a pool.
- On Windows, an open SQLite handle blocks `t.TempDir` cleanup; tests that open DB handles must call `database.Close()` in `t.Cleanup`.

### Permission system

Last-match-wins evaluation across merged rulesets. `permission.Defaults()` returns the `"*": allow` baseline every agent gets. `.env` files ask; `.env.example` is allowed. `external_directory` action gates any tool reaching outside the working directory.

### LSP integration

27 built-in language servers, started lazily on the first file that needs one. Only starts servers already on PATH — no runtime `npm install`. `StrictRoot` servers (like ruff) decline to run without a marker file. All server configs are in `internal/lsp/servers.go`.

### Skills and commands

Skills are markdown files with frontmatter discovered from `.gocode/` (project) and `~/.config/gocode/` (global). A skill marked `slash: true` also appears as a slash command. Commands are assembled from: config entries, markdown definitions, and skills.

### Agent definitions

Agents can be defined two ways (JSON config wins on name collision):
1. In `gocode.json`'s `agent` map
2. As `.gocode/agent/<name>.md` — YAML frontmatter for settings, body for system prompt

### Model catalog

`internal/modelsdev/catalog.json.gz` is a snapshot of models.dev baked into the binary. Run `script/generate-catalog.sh` to refresh. The service refreshes in the background after boot.

## Testing patterns

- Tests use `fakeProvider` (scripted `[][]llm.StreamEvent`) and `fakeTool` (record inputs, return preset output).
- The test helper `setup(t)` opens a temp SQLite DB, creates an event bus, and registers projectors. It seeds a session `ses_1` in project `prj_1`.
- `newRunnerFixture(t, provider, tools)` builds a fully wired `Runner` from the setup.
- `admitPrompt(t, bus, runner, text)` is the standard way to queue work before calling `runner.Run()`.
- E2E tests create real files in `t.TempDir()` and use `builtins.Register(registry, workdir, nil)` for real tool execution.
- Server tests use `newTestServer(t)` → `doJSON(t, server, method, path, body)`.
- TUI tests are the slowest package — expect ~30s for `internal/tui/...`.

## Porting conventions

This is a port from TypeScript. Non-obvious behavior should cite its TypeScript source in comments. When behavior looks wrong, check the cited file before "fixing" — it is usually faithful.

Deliberate divergences are documented in `documentation/10-development.md` (no runtime npm install, no MCP prompts, unmatched commands report instead of swallow).

## CI

`.github/workflows/ci.yml` runs on push to `main`/`dev` and on PRs: gofmt (Ubuntu only) → `go vet` → `go test -race` → `go build`, across Ubuntu/macOS/Windows. Actions are pinned to commit SHAs, not tags.

## Releasing

```sh
git tag v0.1.0 && git push origin v0.1.0
```

Cross-compiles 6 targets (macOS/Linux/Windows × arm64/x64), smoke-tests each on its native runner (asserts binary doesn't report `local`), then publishes. `workflow_dispatch` builds everything without tagging for dry runs.

## Gotchas

- **Version ldflags target `internal/installation`**, not `main`. The Makefile has a comment about a bug where `-X main.version` silently failed because the symbol doesn't exist.
- **Background goroutines must never write to stderr** when the TUI is up — it owns the alternate screen. Use `global.LogBackground()` which appends to a log file instead.
- **`bootStack()` must be called before any global init** — `global.Init()` creates XDG directories first, then `bootStack()` builds the runtime.
- **Model resolution precedence**: explicit flag > last-used model (persisted per directory) > config `"model"` > built-in default `anthropic/claude-sonnet-4-5`.
- **`GOCODE_PURE=1`** and `AGENT=1`/`GOCODE=1` are set in `runMain()`. Background subagents require `GOCODE_EXPERIMENTAL_BACKGROUND_SUBAGENTS=true` or `GOCODE_EXPERIMENTAL=true`.
