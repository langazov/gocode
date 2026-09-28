# library-plugin

A gocode **process plugin** (see [examples/plugin-echo](../../examples/plugin-echo))
that exposes the user's [gocoder.org](https://gocoder.org) **Library** — a
personal, cross-machine collection of uploaded `.md`/`.txt`/`.pdf` documents,
semantically searchable — as four document tools:

- `library_search` — search the library by meaning. Returns ranked
  `path:startLine-endLine` snippets with the **complete matched chunk text**,
  ready to cite (see [Full chunks, not previews](#full-chunks-not-previews)).
- `library_list` — browse the library's folder/file tree.
- `library_get` — fetch one node's metadata by id or path, optionally its
  full document content.
- `library_upload` — push a local file into the library for indexing.

It also stores **gocode skills** in the Library and makes them usable from
any machine without installing them — see [Skills](#skills) for the eight
`library_skill_*` tools.

Unlike [rag-plugin](../rag-plugin), this plugin does no chunking, embedding,
or vector search itself — gocoder.org's Library service does all of that
server-side. This is an authenticated HTTP client wrapped as gocode tools,
nothing more.

## Install

```sh
make install-library-plugin
```

Or build it in place and point at the directory:

```sh
make library-plugin
```

```json
{ "plugin": ["./cmd/library-plugin"] }
```

## Auth

The Library lives on gocoder.org, so this plugin needs the same account
rag-plugin's `gocoder` embedding provider uses: register or log in to
gocoder.org on gocode's first start, and the stored `gk_` API key
(`<data dir>/gocoder.json`) authenticates every call here too. No separate
sign-in step, and no options need setting for the common case.

Plugin options (all optional):

| Option                    | Default                         | Meaning                                  |
| ------------------------- | -------------------------------- | ----------------------------------------- |
| `baseURL`                 | the stored account's site, else `https://gocoder.org` | override gocoder.org's base URL |
| `topK`                    | `10`                             | default result count for `library_search` |
| `uploadTimeout`           | `120`                            | seconds `library_upload` / `library_skill_store` wait for indexing |
| `skillsAdvertise`         | `true`                           | show library skills in gocode's skill list, or the system prompt as a fallback ([advertising](#advertising-remote-skills)) |
| `skillsAdvertiseLimit`    | `20`                             | max library skills registered / listed |
| `skillsAdvertiseMaxChars` | `2000`                           | size cap for the injected block |
| `skillsAdvertiseTTL`      | `300`                            | seconds the list is cached between turns |
| `skillsDefaultScope`      | `"project"`                      | default `scope` for `library_skill_load` |

## Full chunks, not previews

gocoder.org's own search UI shows a 280-character preview per hit. This
plugin always asks the API for the complete, untruncated chunk (`?full=true`
on `GET /library/search`) instead — a caller citing a chunk needs the whole
thing, not a fragment, the same as rag-plugin's `rag_search` does against its
local index.

Against an older gocoder.org deployment that predates the `full` parameter
(and silently ignores it), `library_search` falls back automatically: it
fetches the matched document's full content and slices out the matched line
range itself, one extra request per node that needed it (cached across hits
from the same document within one search). Either way, results are always
complete chunks, never truncated previews.

## Uploading

`library_upload` takes a local file path (`.md`, `.txt`, or `.pdf`, up to
25MB — the same limits the website's own upload form enforces) and a
destination path in the library. Missing ancestor folders in the destination
are created automatically (`mkdir -p` style) — the Library's tree is a flat
set of nodes matched by exact parent path, so uploading straight to
`research/report.pdf` with no `research` folder node would otherwise make the
file invisible when browsing from the root via `library_list`.

Indexing (convert → chunk → embed) happens server-side after upload, same
async shape as gocode-infra's own pipeline:

```json
{ "localPath": "./report.pdf", "path": "research/report.pdf", "wait": true, "timeout": 120 }
```

- `wait` (default `true`) polls the uploaded node's status until it reaches
  `ready` or `failed`, or `timeout` seconds elapse — whichever first. Unlike
  rag-plugin's `rag_index`, there's no plugin-side job registry: the
  indexing work happens on gocoder.org's servers, not in this process, so
  polling is just repeated `GET /library/nodes/{id}` calls, and there's
  nothing here to cancel.
- `wait: false` returns immediately with the node in `uploading` status; poll
  it later with `library_get`.

`library_upload` is non-destructive — it never overwrites or deletes
anything already in the library — so, unlike rag-plugin's `rag_clean`, it
needs no `confirm` gate. (The skill tools that can replace or delete are
gated; see [Skills](#skills).)

## Skills

gocode skills (a folder with a `SKILL.md`, plus `references/`, `scripts/`
and any other files) can live in the Library under a reserved prefix that
mirrors the local folder exactly:

```
.skills/<name>/SKILL.md
.skills/<name>/references/*.md
.skills/<name>/scripts/*          (any file under the skill folder)
```

so relative links inside `SKILL.md` stay valid wherever the skill is read
from. Indexing, embedding and search happen server-side, as for documents;
under `.skills/` gocoder.org also accepts scripts and assets (code and other
text is indexed, binaries are stored byte-for-byte), up to 5MB per file and
50MB per skill. Plain `library_search` leaves skill content out unless its
`path` targets `.skills/`.

### Use without installing

| Tool | What it does |
| --- | --- |
| `library_skill_search` | `mode: "discover"` (default) — which skill fits a task, ranked by each skill's card (name, description, headings). `mode: "content"` — search inside skill files; results are grouped per skill and cite `.skills/<name>/<file>:L-L` with the complete matched chunk. Filters: `skill`, `role` (`skill_md`, `reference`, `script`, `asset`), `k`. |
| `library_skill_use` | Returns the skill's `SKILL.md` body inline plus an index of its files. **Writes nothing to disk.** The model follows the skill in the current turn. |
| `library_skill_show` | One `file` of the skill (e.g. a reference `SKILL.md` links to), or `tree: true` for the listing. With `materialize: true`, writes that single file into the cache and returns its local path — how a script runs without downloading the whole skill. |

The intended flow, which the tool descriptions steer the model toward: when
no local skill fits, `library_skill_search` → `library_skill_use` → fetch
references with `library_skill_show` and scripts with
`library_skill_show materialize=true` as needed → `library_skill_load` only
when the user wants the skill kept.

**Cache.** Materialized files go to
`<data dir>/library-skills/<name>@<hash>/<file>`, keyed by the skill's
content hash, so an in-place update in the library never runs a stale cached
script. Older `<name>@<hash>` directories are pruned when a newer one is
written. Scripts (anything under `scripts/` or starting with `#!`) are made
executable.

### Manage and keep

| Tool | What it does |
| --- | --- |
| `library_skill_list` | Every remote skill with description, file count, size, last update, and local status: `not installed`, `installed (project)` / `installed (global)`, `outdated` (the library changed), `local changes` (you edited it), `local changes, outdated` (both), or `differs` (changed, direction unknown). |
| `library_skill_load` | Downloads the whole skill (raw bytes, verified against the library's sha256) into the chosen skill root, preserving structure, and returns `SKILL.md` inline. `scope`: `project` (`.gocode/skills/<name>/`, the default) or `global` (the global gocode skills folder). Refuses to replace differing local files unless `overwrite: true` — and a refused load writes nothing. `files` fetches a subset. |
| `library_skill_store` | Uploads a local skill, by `name` (looked up in the project and global skill folders) or `dir`. Requires `name` and `description` frontmatter. Skips `.git`, `node_modules`, `.env*`, symlinks, compiled binaries and oversized files; warns on anything that looks like a secret; checks every relative link in the skill's markdown resolves to a file inside the skill (reporting broken links and links that escape the folder). Updating an existing remote skill needs `overwrite: true`, which replaces changed files **and deletes remote files that no longer exist locally**, so the library copy matches exactly. `dryRun: true` reports all of that without changing anything. `wait` as in `library_upload`. |
| `library_skill_diff` | File-by-file local vs library comparison: added, removed, changed. |
| `library_skill_delete` | Deletes a skill from the library. Requires `confirm: true`. Local copies are untouched. |

`load` and `store` leave a `.library-sync.json` in the local skill folder
recording the content hash both sides last agreed on; that is what lets
`library_skill_list` tell `outdated` from `local changes`. It is never
uploaded.

**In gocode's skill list.** Once gocode's HTTP API is up (the plugin gets
its URL at handshake), the plugin registers the library's skills with it
(`PUT /api/skill/external/library-plugin`) — name, description, and the
`SKILL.md` content with its file index. They then appear everywhere a local
skill does: the `/skills` dialog (under **Library**), `<available_skills>`,
the `skill` tool, and as `/<name>` slash commands. A skill installed on disk
under the same name always wins over its library copy. The registration is
refreshed every `skillsAdvertiseTTL` seconds (downloading only `SKILL.md`s
that changed) and right after `library_skill_store` / `library_skill_delete`.

`library_skill_load` writes the files and asks gocode to rescan
(`POST /api/skill/rescan`), so the local copy takes over immediately. Without
a reachable host API (non-interactive `gocode run`) nothing is registered;
remote skills are then advertised through the system-prompt block below, and
a loaded skill appears from the next session.

### Advertising remote skills

`skillsAdvertise` controls both mechanisms: registering with the host
(above), and — the fallback when there is no host API to register with —
the `experimental.chat.system.transform` hook, which appends a short
`<library_skills>` block — name and description of each
remote skill — to every turn's system prompt, so remote skills are
discoverable the way `<available_skills>` entries are. Skills already
installed locally (or built into gocode) are left out. The list is cached
for `skillsAdvertiseTTL` seconds and capped by `skillsAdvertiseLimit` and
`skillsAdvertiseMaxChars`. It never blocks or fails a turn: the fetch times
out after 3 seconds, and offline, signed out, or on any error the block is
simply omitted (and not retried until the TTL passes).

Turn it off, or tune it, any of three ways:

- **Env, one run:** `GOCODE_LIBRARY_SKILLS_ADVERTISE=0` (or `1`) wins over
  the option.
- **CLI:** edits the plugin's options in the global config (the same
  mechanism `gocode plugin enable` uses); takes effect on the next start:

  ```sh
  ./library-plugin skill-config advertise off
  ./library-plugin skill-config limit 10
  ./library-plugin skill-config chars 1500
  ./library-plugin skill-config ttl 600
  ./library-plugin skill-config scope global
  ```

- **Config:** set the options directly on the plugin entry.

## CLI mode

For manual testing without a host:

```sh
./library-plugin search -query "..." -k 10 -path docs
./library-plugin list -path docs
./library-plugin get -id <id> [-content]
./library-plugin upload -file ./report.pdf -path docs/report.pdf [-wait]

./library-plugin skill-search -query "fill a pdf form" [-mode content -skill pdf-tools -role script -k 5]
./library-plugin skill-use -name pdf-tools
./library-plugin skill-show -name pdf-tools -file scripts/fill.py [-materialize] | -tree
./library-plugin skill-list
./library-plugin skill-load -name pdf-tools [-scope global] [-overwrite] [-files SKILL.md,scripts/fill.py]
./library-plugin skill-store -name pdf-tools | -dir ./my-skill [-dry-run] [-overwrite] [-wait=false]
./library-plugin skill-diff -name pdf-tools
./library-plugin skill-delete -name pdf-tools -yes
./library-plugin skill-config advertise on|off
```

## Scope

For documents: search, browse, and upload — not full CRUD. Renaming,
moving, deleting, and folder management beyond what `library_upload` creates
automatically stay on the gocoder.org web UI.

Skills are the exception: they support replace-in-place (`library_skill_store
overwrite`) and deletion (`library_skill_delete`), both explicit opt-ins.
Remote skills are overwritten in place — there is no version history.
