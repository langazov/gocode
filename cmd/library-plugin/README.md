# library-plugin

A gocode **process plugin** (see [examples/plugin-echo](../../examples/plugin-echo))
that exposes the user's [gocoder.org](https://gocoder.org) **Library** — a
personal, cross-machine collection of uploaded `.md`/`.txt`/`.pdf` documents,
semantically searchable — as four tools:

- `library_search` — search the library by meaning. Returns ranked
  `path:startLine-endLine` snippets with the **complete matched chunk text**,
  ready to cite (see [Full chunks, not previews](#full-chunks-not-previews)).
- `library_list` — browse the library's folder/file tree.
- `library_get` — fetch one node's metadata by id or path, optionally its
  full document content.
- `library_upload` — push a local file into the library for indexing.

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

| Option          | Default                         | Meaning                                  |
| --------------- | -------------------------------- | ----------------------------------------- |
| `baseURL`       | the stored account's site, else `https://gocoder.org` | override gocoder.org's base URL |
| `topK`          | `10`                             | default result count for `library_search` |
| `uploadTimeout` | `120`                            | seconds `library_upload` polls before returning a still-processing node |

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

Upload is non-destructive — it never overwrites or deletes anything already
in the library — so, unlike rag-plugin's `rag_clean`, it needs no `confirm`
gate.

## CLI mode

For manual testing without a host:

```sh
./library-plugin search -query "..." -k 10 -path docs
./library-plugin list -path docs
./library-plugin get -id <id> [-content]
./library-plugin upload -file ./report.pdf -path docs/report.pdf [-wait]
```

## Scope

Search, browse, and upload — not full CRUD. Renaming, moving, deleting, and
folder management beyond what `library_upload` creates automatically stay on
the gocoder.org web UI.
