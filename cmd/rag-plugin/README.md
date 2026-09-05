# rag-plugin

A gocode **process plugin** (see [examples/plugin-echo](../../examples/plugin-echo))
that indexes a project's files for semantic search and exposes two tools:

- `rag_index` — (re)index a directory. Incremental: only embeds chunks whose
  content changed since the last index.
- `rag_search` — embed a natural-language query and return the most similar
  indexed chunks, each labeled `path:startLine-endLine` for direct citation.

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

## Vector storage

Chunks and their embeddings are stored with
[chromem-go](https://github.com/philippgille/chromem-go) — pure Go, zero
third-party dependencies, one collection per project. Two other pure-Go
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
