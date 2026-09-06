# rag-plugin

A gocode **process plugin** (see [examples/plugin-echo](../../examples/plugin-echo))
that indexes a project's files for semantic search and exposes five tools:

- `rag_index` — (re)index a directory. Incremental: only embeds chunks whose
  content changed since the last index.
- `rag_search` — embed a natural-language query and return the most similar
  indexed chunks, each labeled `path:startLine-endLine` for direct citation.
- `rag_status` — report every indexed project, its size, and its health.
- `rag_clean` — delete indexed chunks: one subtree, one project, or all.
- `rag_vacuum` — reclaim index data nothing can reach.

The last three are covered under [Maintenance](#maintenance) below.

## Install

```sh
make install-rag-plugin
```

Or build it in place and point at the directory:

```sh
make rag-plugin
```

```json
{ "plugin": [["./cmd/rag-plugin", { "embeddingProvider": "openai" }]] }
```

## Embeddings

Vectors come from a remote OpenAI-compatible `/embeddings` endpoint — the
same credential chain every provider in this Go port uses: the models.dev
catalog's `env[]` names, then `{PROVIDER}_API_KEY`, then `auth.json`. No local
embedding model is used.

Plugin options (all optional):

| Option | Default | Meaning |
|---|---|---|
| `embeddingProvider` | `openai` | models.dev provider id |
| `embeddingModel` | `text-embedding-3-small` | embedding model id |
| `embeddingBaseURL` | (resolved from the catalog) | override the endpoint |
| `include` | known code/doc extensions only (see below) | glob patterns, e.g. `["**/*.go"]` |
| `exclude` | `node_modules`, `vendor`, `dist`, `.venv` | glob patterns |
| `disableGitignore` | `false` | stop honoring `.gitignore`/`.ignore` files (see below) |
| `chunkLines` | `60` | chunk size in source lines |
| `chunkOverlap` | `10` | overlap between adjacent chunks |
| `topK` | `8` | default result count for `rag_search` |
| `dbPath` | `<data dir>/rag.db` | chromem-go persistence directory |

With no `include` set, only recognized code, documentation, and small
structured-config file extensions are indexed (`.go`, `.ts`, `.py`, `.md`,
`.json`, `.yaml`, and the like — see `textExtensions` in
`internal/rag/chunk/chunk.go` for the full list), plus a handful of
well-known extensionless files (`Makefile`, `Dockerfile`, `README`, ...).
Icon/asset formats (`.svg`, images, fonts), lockfiles (`.lock`), and other
binary formats are never indexed by default — they're not code or prose, and
some (a `.svg` sprite sheet, say) can be enormous single-line files that are
pure noise for semantic search. `.json` gets an extra size cap (64KB) on top
of that, since the extension covers both small hand-written config and large
generated data dumps (a serialized lockfile, a recorded API fixture) with no
way to tell them apart by extension alone. Set `include` explicitly to
override this default and index exactly your own patterns instead — it's an
opt-in escape hatch, not an addition to the default list.

Every directory walked during indexing also honors that directory's
`.gitignore` and `.ignore` files (same syntax as `.gitignore`; a
git-independent convention some tools use for extra excludes), the same way
`git ls-files` or `rg` would — on top of, not instead of, `exclude` above.
This is what actually keeps large generated/vendored trees (build output,
lockfile-managed dependencies, etc.) out of the index without having to
hand-list every project's own conventions; `exclude` remains useful for
excludes that don't belong in `.gitignore` itself. `rag_index`'s `path`
argument still gets indexed even if some ancestor `.gitignore` would have
excluded it — an explicit request to index a directory wins. Set
`disableGitignore: true` to fall back to `include`/`exclude` alone.

## One-shot indexing outside the host

`internal/plugin`'s host bounds a tool call with no deadline of its own to
30 seconds. A large repo's first index can take longer than that, so
rag-plugin also runs as a plain CLI, independent of the JSON-RPC protocol:

```sh
./rag-plugin index -root . -embedding-provider openai
```

Run this once before first use on a large project; `rag_search` and later,
smaller `rag_index` calls stay fast enough for the tool-call path.

If indexing fails with the embeddings endpoint's "maximum input length"
error, `embed.Client` already clamps each chunk to `DefaultMaxInputChars`
bytes before sending it — but that error's own message only names a
batch-relative input index, not a file, so tracking down which chunk (still)
crossed the line means bisecting the whole tree by hand. `rag-plugin scan`
does that lookup instead: it chunks a tree exactly like `index` would, but
never calls the embeddings endpoint (no provider or API key needed), and
prints the largest chunks by byte size, flagging any still over the clamp:

```sh
./rag-plugin scan -root . -top 20
```

## Maintenance

Indexing prunes as it goes: `rag_index` diffs the chunk IDs a fresh walk
produces against the ones already stored and deletes the difference, which
covers deleted files, newly-excluded files, and files whose line count
shifted enough to move a chunk boundary. That is the whole of the automatic
cleanup, and it only ever reaches **one project, in the subtree you re-walk,
with working embeddings credentials**.

Three kinds of data fall outside it:

- **Whole projects you no longer index.** The project id is the worktree
  path, so a repo you moved, deleted, or once indexed from a parent
  directory keeps a collection of its own forever. Indexing
  `~/src/app/backend` never touches the collection left behind by an earlier
  `~/src/app`, and both stay in the database.
- **Collections whose bookkeeping rows were lost** (`orphan`). The
  incremental diff reads the manifest, so a collection the manifest doesn't
  know about is never re-embedded *and* never pruned.
- **Bookkeeping rows whose collection was lost** (`dangling`). The mirror
  image: the diff believes chunks are stored that no search can return.

This matters more than it sounds, because `store.Open` decodes *every*
collection in the database into memory eagerly — so one abandoned project
costs startup time and RAM on every run, for every other project sharing the
database. And because chromem-go names each collection directory after a
hash of the project id, none of it is identifiable, let alone deletable, by
hand.

```sh
rag-plugin list                                  # what is in there, largest first
rag-plugin vacuum -dry-run                       # what is unreachable
rag-plugin vacuum -yes                           # reclaim it
rag-plugin clean -path internal/legacy -yes      # drop one subtree of this project
rag-plugin clean -project /old/worktree -yes     # drop one project
rag-plugin clean -all -yes                       # drop everything, every project
```

`list` prints a project per row with its chunk and file counts, its size on
disk, and a status of `ok`, `orphan`, `dangling`, or `root missing`; `-json`
emits the same data for scripting. `clean` targets the project derived from
`-root` (default `.`) unless `-project` names another, and narrows to a
subtree with `-path`. `vacuum` removes only the unreachable cases above —
add `-prune-missing` to also drop projects whose root directory is gone,
which is off by default because an absent directory is not corruption (an
unmounted volume, a worktree you will check out again).

Every destructive command takes `-dry-run` to preview and prompts for
confirmation otherwise; `-yes` answers the prompt in advance, and is
required when stdin cannot answer, so a scripted cleanup never silently
turns into a no-op. Deletion is irreversible in the sense that matters:
restoring the chunks means re-embedding them, at the provider's price.

None of these commands embed anything, so unlike `index` and `search` they
need no provider, no API key, and no network. That is deliberate — the index
most in need of cleaning up is often the one whose provider config or
credentials have since gone away.

The same operations are available to the model as `rag_status`, `rag_clean`
and `rag_vacuum`. The destructive two require `confirm: true` and accept
`dryRun: true`. Be aware of what that guard is and isn't: the process-plugin
protocol has no permission field, so nothing between the model and the
plugin can prompt you, and `confirm` is an argument the model sets itself.
It prevents an accidental call, not a determined one.

## Vector storage

Chunks and their embeddings are stored with
[chromem-go](https://github.com/philippgille/chromem-go) — pure Go, zero
third-party dependencies, one collection per project, where "project" is the
worktree path. That identity is why indexing a parent directory forks a
second collection that never converges with the first, and why
[Maintenance](#maintenance) exists. Two other pure-Go
vector libraries were tried first and rejected: `coder/hnsw` panics on a
same-key replace and corrupts its own graph on delete, and both it and
`DotNetAge/govector` (which wraps `coder/hnsw` for its own HNSW mode)
unconditionally import a Windows-incompatible dependency, so neither even
compiles for `GOOS=windows`. chromem-go has none of these problems, verified
directly against this project's own replace/delete/reopen/cross-compile
scenarios — see `internal/rag/store/store.go`'s package doc for the specifics.

The trade-off: chromem-go's brute-force search is O(n) per query rather than
an ANN graph's sub-linear cost. For a single project's indexed files (tens of
thousands of chunks), this stays well under 50ms per query — the practical
ceiling for an interactive coding agent, not a bottleneck.
