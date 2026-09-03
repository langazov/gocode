---
name: gocode-config
description: Configure the Go port of gocode — add or customize AI providers, models, MCP servers, LSP servers, agents, and permissions. Use when the user asks to connect a provider, set an API key, change the default model, add an MCP server, enable/disable a language server, or otherwise edit gocode.json / gocode.jsonc for this Go port (as opposed to the TypeScript opencode CLI, which uses a separate config file and command set).
---

Reference for configuring `gocode`, the Go port of opencode, at
`/Users/emilo/Work/GitHub/opencode/go`. Covers where config lives, and how to
add/change providers, models, MCP servers, and LSP servers through either
`gocode.json` or the CLI.

## Where config lives

Config merges from several sources, later ones overriding earlier fields
(`internal/config/loader.go`):

1. Global: `<config dir>/config.json` → `gocode.json` → `gocode.jsonc`, in
   that order. `<config dir>` is `$XDG_CONFIG_HOME/gocode` or
   `~/.config/gocode`.
2. `$GOCODE_CONFIG` — an explicit file path, if set.
3. Project: `gocode.json`/`gocode.jsonc` discovered walking up from the
   current directory to the git worktree root.
4. `.gocode/gocode.json` / `.gocode/gocode.jsonc` in the project (or
   `$GOCODE_CONFIG_DIR` if set).
5. `$GOCODE_CONFIG_CONTENT` — inline JSON, wins over everything (mainly for
   tests).

Both `.json` and commented `.jsonc` are accepted everywhere. When editing by
hand, prefer the project's `.gocode/gocode.json` for project-specific
settings and `~/.config/gocode/gocode.json` for user-wide defaults (provider
credentials, a personal default model).

To see exactly which files were found/merged: `gocode debug paths` shows the
resolved directories; `gocode debug info` dumps more startup detail.

## Providers

A provider is anything with a models.dev catalog entry (~212 of them) or a
custom one you describe yourself. Three ways to give gocode a credential, in
priority order (`internal/provider/fromconfig.go`, `provider.go`):

1. **`gocode.json`** — `provider.<id>.options.apiKey` (and/or
   `options.baseURL`). Wins outright over everything else below.
2. **Environment variable** — whatever names the catalog lists for that
   provider (e.g. `ANTHROPIC_API_KEY`), or the fallback convention
   `<PROVIDERID>_API_KEY` if the catalog lists none.
3. **Stored credential** (`~/.local/share/gocode/auth.json`) — set via the
   CLI:

   ```
   gocode providers login              # interactive: pick provider, then method
   gocode providers login --provider anthropic --method "API key"
   gocode providers list                # show configured providers + credential state
   gocode providers logout <provider>
   ```

   `gocode auth` is an alias for `gocode providers`. Login offers whatever
   methods that provider advertises: an env-var check, a pasted API key, or
   (for providers with one) an OAuth device/browser flow.

`enabled_providers` / `disabled_providers` (top-level `gocode.json` arrays of
provider ids) allow-list or block-list which providers show up at all.

### Custom / unlisted providers

A provider not in the models.dev catalog needs an explicit `api` (base URL)
in `gocode.json`, or provider resolution fails loudly rather than silently
guessing an endpoint (posting your key to the wrong company's API used to be
a real bug this port fixed):

```json
{
  "provider": {
    "my-proxy": {
      "npm": "@ai-sdk/openai-compatible",
      "api": "https://my-proxy.example.com/v1",
      "env": ["MY_PROXY_API_KEY"],
      "options": { "baseURL": "https://my-proxy.example.com/v1" },
      "models": {
        "my-model": { "name": "My Model", "limit": { "context": 128000, "output": 8192 } }
      }
    }
  }
}
```

`npm` picks the wire protocol this port actually speaks:
`@ai-sdk/openai-compatible` (Chat Completions, the default), `@ai-sdk/openai`
(OpenAI's Responses API), `@ai-sdk/anthropic` (Messages API), or
`@ai-sdk/google` (Gemini API) — anything else is reported as unsupported
rather than guessed at. A single provider's catalog can also declare a
**per-model** `provider: {npm, api}` override that routes just that model
through a different one of those four (opencode/Zen's own catalog does this
for its Claude/Gemini/GPT-5-family models) — see `internal/provider/model_route.go`.

An env-var override for any provider's base URL: `<PROVIDERID>_BASE_URL`
(e.g. `OPENAI_BASE_URL`).

## Models

```
gocode models                 # every provider's models
gocode models <provider>      # just one provider, e.g. gocode models anthropic
gocode models --verbose       # include cost/limit metadata
gocode models --refresh       # force-refetch the models.dev catalog now
```

Pick the default model in `gocode.json`:

```json
{ "model": "anthropic/claude-sonnet-4-5", "small_model": "anthropic/claude-haiku-4-5" }
```

`model` is the default; `small_model` is used for cheap background work
(session titling, etc.) when set. Format is always `provider/model-id`. A
per-agent override goes under `agent.<name>.model` in the same format (see
Agents below). The TUI's model dialog (`/models` or `ctrl+x m`) and its
`SetModel` API change a *session's* pinned model at runtime without touching
config.

The models.dev catalog itself (not your credentials) is fetched from
`https://models.opencode.ai/api.json`, cached to disk with a 5-minute TTL,
and refreshed automatically at the start of every `gocode` run if stale
(`internal/modelsdev`). Overrides:

- `GOCODE_MODELS_URL` — use a different catalog source.
- `GOCODE_MODELS_PATH` — read/write the cache at a specific file path.
- `GOCODE_DISABLE_MODELS_FETCH=1` — never fetch; use the disk cache or the
  snapshot embedded in the binary at build time.

## MCP servers

```
gocode mcp add <name> --url <url>                 # remote (streamable HTTP)
gocode mcp add <name> -- <command...>              # local (stdio subprocess)
gocode mcp list                                    # status of every configured server
gocode mcp auth <name>                             # OAuth login for a remote server
gocode mcp auth list                               # which servers support OAuth + their status
gocode mcp logout <name>                           # drop stored OAuth credentials
gocode mcp debug <name>                            # connection + auth diagnostics
```

`mcp add` writes into the **global** `gocode.json`'s `mcp` section. Hand-edit
either file's `mcp` block directly for more control
(`internal/mcp/config.go`):

```json
{
  "mcp": {
    "local-tool": {
      "type": "local",
      "command": ["my-mcp-server", "--stdio"],
      "cwd": "./tools/my-mcp-server",
      "environment": { "API_KEY": "..." }
    },
    "remote-tool": {
      "type": "remote",
      "url": "https://mcp.example.com",
      "headers": { "Authorization": "Bearer ..." },
      "oauth": true,
      "enabled": true,
      "timeout": 30000
    }
  }
}
```

`oauth` accepts `true`/absent (default OAuth flow), `false` (disable OAuth
entirely for that server), or an object to customize it (`clientId`,
`clientSecret`, `scope`, `callbackPort`, `redirectUri`). `enabled: false`
configures a server without connecting it. `timeout` is milliseconds.

## LSP servers

Language servers start automatically the first time a file of a matching
extension is opened, but only if the server binary is already on `PATH` —
nothing gets installed for you. The built-in registry
(`internal/lsp/servers.go`) covers ~20 languages out of the box: `gopls`
(.go), `typescript-language-server` (.ts/.tsx/.js/.jsx), `rust-analyzer`
(.rs), `pyright`/`ruff` (.py), `clangd` (C/C++), `zls` (.zig),
`lua-language-server`, `bash-language-server`, `terraform-ls`, `dart`,
`ocamllsp`, `gleam`, `nixd`, `clojure-lsp`, `elixir-ls`,
`haskell-language-server-wrapper`, `yaml-language-server`, and more.

`gocode.json`'s `lsp` section can disable everything, disable one server, or
add/override one (`internal/config/lsp.go`):

```json
{ "lsp": false }
```

```json
{
  "lsp": {
    "gopls": { "disabled": true },
    "my-lang": {
      "command": ["my-lang-server", "--stdio"],
      "extensions": [".mylang"],
      "env": { "SOME_VAR": "1" },
      "initialization": { "settings": {} }
    }
  }
}
```

A custom entry needs `command` and `extensions` at minimum; `initialization`
is passed through as the LSP `initialize` request's `initializationOptions`.

## Related, closely-adjacent settings

Not the focus of this skill, but in the same `gocode.json` and worth knowing
about while configuring the above:

- **Agents** — `agent.<name>` blocks (`model`, `variant`, `prompt`, `mode`,
  `permission`, ...). `default_agent` picks which one a new session starts
  with.
- **Permissions** — top-level `permission` block, merged with the default
  `"*": allow` baseline; can also be set per-agent.
- **Tools** — `tools.<name>: false` disables a built-in tool globally.
- **Keybinds** — `keybinds.<action>` in the TUI.
- **Commands** — `command.<name>` defines a custom `/name` slash command
  (templated markdown); skills under `.gocode/skill/` or `~/.agents/skills/`
  become slash commands too, automatically, by their `name` frontmatter.

## Verifying a change

- `gocode debug paths` — confirm which config files/dirs are actually being
  read.
- `gocode providers list` — confirm a provider now shows as configured.
- `gocode models <provider>` — confirm the model list looks right after a
  catalog or `provider.<id>.models` change.
- `gocode mcp list` / `gocode mcp debug <name>` — confirm an MCP server
  connects.
- Start the TUI and open `/models`, `/agents`, or `/skills` to see the live
  result of a config change without leaving the app.
