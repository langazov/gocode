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
| `include` | everything (subject to `exclude`) | glob patterns, e.g. `["**/*.go"]` |
| `exclude` | `node_modules`, `vendor`, `dist`, `.venv` | glob patterns |
| `chunkLines` | `60` | chunk size in source lines |
| `chunkOverlap` | `10` | overlap between adjacent chunks |
| `topK` | `8` | default result count for `rag_search` |
| `dbPath` | `<data dir>/rag.db` | chromem-go persistence directory |

## One-shot indexing outside the host

`internal/plugin`'s host bounds a tool call with no deadline of its own to
30 seconds. A large repo's first index can take longer than that, so
rag-plugin also runs as a plain CLI, independent of the JSON-RPC protocol:

```sh
./rag-plugin index -root . -embedding-provider openai
```

Run this once before first use on a large project; `rag_search` and later,
smaller `rag_index` calls stay fast enough for the tool-call path.

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
