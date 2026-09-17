# rag-plugin

A gocode **process plugin** (see [examples/plugin-echo](../../examples/plugin-echo))
that indexes a project's files for semantic search and exposes seven tools:

- `rag_index` — (re)index a directory. Incremental: only embeds chunks whose
  content changed since the last index. Runs as a background job — see
  [Background indexing](#background-indexing).
- `rag_index_status` / `rag_index_cancel` — poll or stop a background
  `rag_index` job by the `jobId` it returned.
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

Vectors come from a remote embeddings endpoint. No local embedding model is
used.

**gocoder.org (default once signed in).** When you register or log in to
gocoder.org on gocode's first start, gocode stores an API key in
`<data dir>/gocoder.json`. With that account present and neither
`embeddingProvider` nor `embeddingBaseURL` set, rag-plugin embeds through
gocoder.org's `/api/embeddings` with that key — no provider API key of your
own needed. Requests use OpenAI's `text-embedding-3-small` (override with
`embeddingModel`) through gocoder.org's `openrouter` provider, or its `openai`
provider when OpenRouter isn't enabled there — the same model either way,
and the same as the direct-OpenAI default, so an existing index stays usable.
If the site has neither enabled, indexing fails naming the providers it does
have, rather than silently switching to a model with vectors of another size.
Set `embeddingProvider: "gocoder"` to require it (and fail clearly when not
signed in).

**Any OpenAI-compatible provider.** Otherwise vectors come from a remote
OpenAI-compatible `/embeddings` endpoint, resolved through the same credential
chain every provider in this Go port uses: the models.dev catalog's `env[]`
names, then `{PROVIDER}_API_KEY`, then `auth.json`. Setting
`embeddingProvider` or `embeddingBaseURL` always takes precedence over a
stored gocoder.org account.

Switching between models with different vector sizes needs a re-index with
`force` (or `-force` on the CLI); an index mixing two models cannot be
searched.

Plugin options (all optional):

| Option | Default | Meaning |
|---|---|---|
| `embeddingProvider` | `gocoder` when signed in, else `openai` | models.dev provider id, or `gocoder` |
| `embeddingModel` | `text-embedding-3-small` | embedding model id |
| `embeddingBaseURL` | (resolved from the catalog) | override the endpoint |
| `include` | known code/doc extensions only (see below) | glob patterns, e.g. `["**/*.go"]` |
| `exclude` | `node_modules`, `vendor`, `dist`, `.venv` | glob patterns |
| `disableGitignore` | `false` | stop honoring `.gitignore`/`.ignore` files (see below) |
| `chunkLines` | `60` | chunk size in source lines |
| `chunkOverlap` | `10` | overlap between adjacent chunks |
| `topK` | `8` | default result count for `rag_search` |
| `dbPath` | `<data dir>/rag.db` | chromem-go persistence directory |
| `callTimeout` | `300` (from the manifest) | per-call timeout in seconds, overriding the host's 30s default |

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

## Background indexing

`rag_index` always runs as a background job — the same pattern
[go-codegraph](https://github.com/langazov/go-codegraph)'s MCP server uses
for `index_repository`: the call starts (or joins, if one for the same path
is already running) a job that keeps going after the call returns, so
indexing a large project never has to hold the plugin's single-threaded
request loop — and with it, every other tool call — hostage for minutes.

```json
{"path": "src", "wait": false}
```

- `wait` (default `true`) controls only how long *this call* blocks watching
  the job, never how long the job itself is allowed to run. With the
  default, a small project that finishes quickly behaves exactly like a
  synchronous call always did — same JSON summary, no `jobId` in sight.
  `wait: false` returns immediately with a `jobId`, freeing the model to do
  other things (including `rag_search` over whatever is already indexed)
  while a large first index runs.
- `timeout` (seconds, default 300) bounds that wait, not the indexing work.
  A `wait: true` call that hits the timeout gets back the job's still-running
  status — including its `jobId` — rather than an error; the job keeps
  running regardless. This is what the manifest's `callTimeoutSeconds: 300`
  (which raises the host's own 30-second `DefaultCallTimeout` for this
  plugin's calls) also matches, so a caller that left `timeout` at its
  default gets the status back just before the host would have given up
  waiting for the reply.
- `rag_index_status {"jobId": "..."}` polls a job: current stage
  (`walking`/`embedding`/`storing`) and progress while running, or the final
  summary/error once finished.
- `rag_index_cancel {"jobId": "..."}` stops a running job. Chunks it already
  embedded and stored before that stay indexed — only further embedding
  work is stopped.

Nothing here is bounded by the host's call timeout at all when `wait` is
`false`: a first index that would otherwise run for tens of minutes just
keeps running, polled at whatever cadence the model chooses. For a repo
where even watching it via polling is inconvenient, rag-plugin also runs as
a plain CLI, independent of the JSON-RPC protocol and its background-job
machinery (indexing there is a plain synchronous call, with no `jobId`):

```sh
./rag-plugin index -root . -embedding-provider openai
```

Run this once before first use on a very large project; `rag_search` and
later, smaller `rag_index` calls stay fast either way.

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

## Evaluation

Whether chunking splits files sensibly and whether `rag_search` finds the
right thing are both measurable, not just spot-checkable. `rag-plugin eval`
(CLI only — it needs no host, and one of its two modes needs no embeddings
provider either) covers both, backed by `internal/rag/eval`:

```sh
rag-plugin eval chunks -root .
```

Walks the tree once with the plain sliding window and once with syntax-aware
splitting, and for both scores the resulting chunks against real
function/class/method boundaries (from the same LSP resolver `rag_index`
itself uses) — reporting what fraction of symbols land inside a single chunk
(`boundary integrity`) and the containment-ratio distribution for the rest.
Syntax-aware splitting should read at or near 1.0 by construction; the
sliding window's number is the real one, and moving `-chunk-lines`/
`-chunk-overlap` should move it measurably. Needs no embeddings provider —
only an LSP server for the languages present.

```sh
rag-plugin eval retrieval -root . -k 8
```

Mines (query, relevant-region) pairs from the project's own commit history —
a commit's subject line stands in for a query, the lines it touched stand in
for the answer — then runs `rag_search` against each and reports
**Recall@K**, **MRR**, and **NDCG@K**, each with a bootstrap 95% confidence
interval. The interval matters more than the point estimate: it is what
tells you whether a change to `chunkLines`, `chunkOverlap`, or the embedding
model actually moved retrieval quality, versus noise from which queries
happened to be easy. Recall@K and MRR both judge only the *first* relevant
hit; NDCG@K also credits finding more of a multi-file commit's relevant
regions, discounted by how far down the ranking each one took, and decays
more gently by rank than MRR's raw `1/rank` — so a Recall@K that looks
unchanged alongside a moved NDCG@K is often relevant hits shifting rank
without crossing the top-K threshold either way. This mode needs the
project already indexed and a working embeddings provider, same as
`rag_search` itself. `-v` prints every gold pair's outcome (hit rank or
miss) for spot-checking which kinds of queries the index still
misses.

`script/rag-eval.sh` (or `make rag-eval`) wraps both: it builds the plugin,
runs both modes with `-json`, saves a timestamped snapshot of each under
`reports/rag-eval/` (gitignored), and diffs the new run's headline numbers
against the most recent previous snapshot — including whether a retrieval
confidence interval actually moved or just overlaps the last one. Set
`RAG_EVAL_SKIP_RETRIEVAL=1` to run the chunking half only (no index or
provider needed); pass extra `rag-plugin eval retrieval` flags with
`make rag-eval EVAL_ARGS="-k 20"` or `script/rag-eval.sh -- -k 20`.

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

This matters more than it sounds: `rag-plugin list`/`vacuum` open every
project's data to report on it, so an abandoned project still costs decode
time whenever one of those runs, even though a search or index call for a
different project never touches it (see [Vector storage](#vector-storage)).
And because chromem-go names each collection directory after a hash of the
project id, none of it is identifiable, let alone deletable, by hand.

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

Each project also gets its own persistence directory under `<dbPath>/projects/`,
opened only when that project is actually referenced — not one shared
directory holding every project chromem-go decodes in full on every open.
That's a deliberate departure from chromem-go's own single-directory
examples: without it, a `rag_search` call for one small project would still
pay to decode every other project sharing the database first. An existing
database on the old shared layout migrates to this one automatically, once,
the first time it's opened — a plain directory rename per project, so it
costs nothing proportional to how much is stored. `list` and `vacuum` are the
exception: answering "what does this whole database hold" means opening
everything, so they do, on demand, rather than paying for it on every run.

The trade-off: chromem-go's brute-force search is O(n) per query rather than
an ANN graph's sub-linear cost. For a single project's indexed files (tens of
thousands of chunks), this stays well under 50ms per query — the practical
ceiling for an interactive coding agent, not a bottleneck.
