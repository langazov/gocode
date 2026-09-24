---
name: configure-gocode
description: "Configure gocode itself — config files and precedence, providers and models, permissions, LSP, MCP, plugins, agents, commands and skills. Load when the task is to set up or change gocode's own configuration."
slash: true
---

# Configuring gocode

Config is **JSONC** — JSON with comments and trailing commas — deep-merged
from several sources, later sources winning key by key. This skill is the
map: where each setting lives, which file wins, and the CLI subcommands that
edit the global config safely.

## Step 1 — read the current state

```sh
gocode debug config    # every source in merge order + the merged result (secrets redacted)
gocode debug paths     # the XDG paths this install resolves to
gocode models          # providers and models reachable with stored credentials
gocode debug lsp       # which language servers would start, and why
```

Run these before editing. Guessing key names is the main way to get this
wrong: a key the loader does not know is silently dropped on merge, so a
typo produces a config that looks right and does nothing.

## Step 2 — pick the right file

| # | Source | Path |
|---|--------|------|
| 1 | global | `~/.config/gocode/gocode.json` (or `gocode.jsonc`, `config.json`) |
| 2 | env file | `$GOCODE_CONFIG` — path to a config file |
| 3 | project | `gocode.json(c)` here or in any directory above, up to the worktree root |
| 4 | config dirs | `.gocode/gocode.json(c)` and `$GOCODE_CONFIG_DIR` |
| 5 | env inline | `$GOCODE_CONFIG_CONTENT` — raw JSONC text |

- The merge is **deep**: setting one key under `permission` in the project
  config *refines* the global block, it does not replace it.
- A broken **global** config is tolerated — a warning, the session still
  boots. A broken **project** config is an error: this project's config is
  expected to work.
- `GOCODE_DISABLE_PROJECT_CONFIG=1` skips project sources entirely;
  `GOCODE_PERMISSION` (a JSON object) deep-merges a permission block for a
  single invocation — how a CI pipeline states its denies without touching
  the repo.
- Machine-wide settings (providers, credentials, theme) go in the global
  file; anything a repository should carry (model choice, permissions,
  agents, commands) goes in the project file next to the code.

## Step 3 — secrets

```jsonc
"provider": {
  "mine": {
    "options": {
      "apiKey": "{env:MY_API_KEY}",       // from the environment, "" when unset
      "baseURL": "{file:./endpoint.txt}"  // from a file, ERROR when missing
    }
  }
}
```

`{file:...}` resolves relative to the config file's own directory, with `~/`
expanded. The asymmetry is deliberate: an unset environment variable is a
normal condition, a config pointing at a file that is not there is a
mistake. Keeping secrets in `{env:...}` is what lets `gocode.json` be
committed.

## The common blocks

```jsonc
{
  // models — "provider/model"; small_model covers cheap background work
  "model": "anthropic/claude-sonnet-5",
  "small_model": "anthropic/claude-haiku-4-5",
  "default_agent": "build",

  // extra files appended to the system prompt (pair with AGENTS.md)
  "instructions": ["./docs/conventions.md"],

  // a provider the catalog does not know, or an override of one it does
  "provider": {
    "my-gateway": {
      "options": { "baseURL": "https://gateway.internal/v1", "apiKey": "{env:GATEWAY_KEY}" },
      "models": { "my-model": { "name": "My Model", "limit": { "context": 128000 } } }
    }
  },

  // permission: "ask" | "allow" | "deny", whole tool or per resource
  "permission": {
    "bash": "ask",
    "read": { "*.env": "ask", "*.env.example": "allow" }
  },

  // turn a tool off entirely
  "tools": { "websearch": false },

  // language servers: most just need to be on PATH; this is for the rest
  "lsp": {
    "gopls": { "disabled": false },
    "my-server": { "command": ["my-lsp", "--stdio"], "extensions": [".mylang"] }
  },

  // MCP servers, local (stdio) or remote
  "mcp": {
    "github": { "type": "local", "command": ["gh-mcp"], "env": { "TOKEN": "{env:GH}" } },
    "stripe": { "type": "remote", "url": "https://mcp.stripe.com", "timeout": 120000 }
  },

  // plugins — order is meaningful, and listing one is what enables it
  "plugin": [
    "my-plugin",
    ["review", { "strict": true }]
  ],

  // agents — named model+prompt+permission+tools configurations
  "agent": {
    "reviewer": {
      "prompt": "You review code. Never edit files.",
      "tools": { "write": false, "edit": false, "bash": false },
      "permission": { "*": "deny", "read": "allow" }
    }
  },

  // slash commands
  "command": {
    "commit": { "template": "Commit the staged changes. Message hint: $ARGUMENTS" }
  }
}
```

Facts that are easy to get wrong:

- **MCP timeouts** are milliseconds, per server: 60 s to connect, 5 minutes
  per tool call by default.
- **An agent with no `permission` block gets deny-all**, not a permissive
  default — always give an agent a permission block.
- **A plugin reference** resolves: built-in registry → a path relative to
  the session directory → `~/.config/gocode/plugin/<name>`. There is no
  fetch step; a name that is nowhere fails with "not installed".
- **Command templates**: `$1`…`$n` (the highest-numbered one is greedy),
  `$ARGUMENTS` for the raw string, `` !`cmd` `` for a command's output
  (bounded at 30 s, empty on failure).

## Extension points on disk

| Kind | Project | Global |
|------|---------|--------|
| skill | `.gocode/skill/<name>/SKILL.md` | `~/.config/gocode/gocode/skill/<name>/SKILL.md` |
| agent | `.gocode/agent/<name>.md` | same layout under the global dir |
| command | `.gocode/command/**.md` (nesting namespaces: `command/git/commit.md` → `/git/commit`) | same |

`.agents/skills/` (project) and `~/.agents/skills/` (global) are scanned
too — a cross-tool convention other agent CLIs write into. A project skill
beats a global one of the same name, and both beat the built-ins compiled
into the binary.

A `SKILL.md` carries `name` and `description` in YAML frontmatter; the body
is the instruction the `skill` tool injects. Skills cost tokens only when
loaded, so they are the right place for long procedure — not the system
prompt.

## The config-editing CLI (global config only)

```sh
gocode lsp enable mdlsp --command mdlsp --extensions .md,.markdown
gocode lsp disable mdlsp
gocode plugin enable rag-plugin --options '{"embeddingProvider":"openai"}'
gocode plugin disable rag-plugin
gocode plugin list            # configured / built-in / installed-but-not-enabled
gocode providers login        # store a credential in the keyring
```

These refuse to touch a project config — it is version-controlled, and an
installer or agent silently committing machine-specific paths into it is
worse than a hand edit. Edit project config by hand.

## Verify

After any change, `gocode debug config` shows the merged result — confirm
the edit landed where you intended, and that nothing else moved.
