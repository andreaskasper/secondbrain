# secondbrain 🧠

A fast, path-based REST document store for Markdown files — built so that
Claude and other agents (and you) can quickly create, read, update, delete and
**search** a "second brain" knowledge base.

### Status & Stats

[![Go Version](https://img.shields.io/github/go-mod/go-version/andreaskasper/secondbrain?filename=src%2Fgo.mod)](https://go.dev/)
![Last Commit](https://img.shields.io/github/last-commit/andreaskasper/secondbrain.svg)
![Commit Activity](https://img.shields.io/github/commit-activity/m/andreaskasper/secondbrain.svg)
[![Issues](https://img.shields.io/github/issues/andreaskasper/secondbrain.svg)](https://github.com/andreaskasper/secondbrain/issues)
![Repo Size](https://img.shields.io/github/repo-size/andreaskasper/secondbrain.svg)
![Stars](https://img.shields.io/github/stars/andreaskasper/secondbrain.svg?style=social)

---

Files are stored as plain `.md` files in a real directory tree on a mounted
volume, so the data stays transparent, portable and git-friendly. The path in
the URL maps directly to the path on disk: `GET /projects/notes/idea.md` reads
`/<data>/projects/notes/idea.md`.

- **Language:** Go (standard library only — no external dependencies)
- **Storage:** plain `.md` files on a data volume
- **Auth:** single API key (`X-API-Key` header or `Authorization: Bearer`)
- **Goal:** be small, fast and trivially scriptable for LLM agents

## Quick start

```bash
# Build & run with Docker Compose (set a real API key first!)
SECONDBRAIN_API_KEY=secret docker compose up --build
```

Or run locally:

```bash
cd src
SECONDBRAIN_API_KEY=secret SECONDBRAIN_DATA_DIR=./brain go run .
```

Then:

```bash
KEY=secret
BASE=http://localhost:8080

# Create a note (PUT = create or overwrite)
curl -X PUT "$BASE/notes/hello.md" -H "X-API-Key: $KEY" \
  --data-binary $'---\ntitle: Hello\ntags: [demo, intro]\n---\n# Hello\nWorld'

# Read it
curl "$BASE/notes/hello.md" -H "X-API-Key: $KEY"

# List a directory
curl "$BASE/notes/" -H "X-API-Key: $KEY"

# Search the whole tree
curl "$BASE/?search=World" -H "X-API-Key: $KEY"
```

## Configuration

All configuration is via environment variables:

| Variable                     | Default   | Description                                            |
| ---------------------------- | --------- | ------------------------------------------------------ |
| `SECONDBRAIN_API_KEY`        | *(none)*  | **Required.** Shared secret for every request.         |
| `SECONDBRAIN_DATA_DIR`       | `/data`   | Root directory of the markdown tree.                   |
| `SECONDBRAIN_ADDR`           | `:8080`   | Listen address.                                        |
| `SECONDBRAIN_MAX_BODY_BYTES` | `10485760`| Max request body size for writes (10 MiB).             |

The server refuses to start without an API key.

## Authentication

Every endpoint except `GET /healthz` requires the API key, sent either way:

```
X-API-Key: <key>
Authorization: Bearer <key>
```

A missing or wrong key returns `401`.

## API

The URL path is the document path. A path that resolves to a **directory**
returns a listing; a path that resolves to a **`.md` file** returns its content.
Only `.md` files are stored and served.

### `GET /<path>` — read a file or list a directory

**File** → returns the raw Markdown (`text/markdown`). Modifiers:

| Query param        | Example                  | Effect                                            |
| ------------------ | ------------------------ | ------------------------------------------------- |
| `head=N`           | `?head=20`               | First N lines.                                    |
| `tail=N`           | `?tail=100`              | Last N lines.                                     |
| `lines=A-B`        | `?lines=10-40`           | Inclusive 1-based line range (`A-`, `-B` allowed).|
| `grep=REGEX`       | `?grep=TODO`             | Only lines matching the (case-insensitive) regex. |
| `nofrontmatter=1`  | `?nofrontmatter=1`       | Strip the YAML frontmatter block.                 |
| `json=1`           | `?json=1`                | Structured JSON: path, size, frontmatter, body.   |

Modifiers compose in this order: `grep` → `lines` → `head` → `tail`.
Sending `Accept: application/json` is equivalent to `?json=1`.

**Directory** → returns a JSON listing. Modifiers:

| Query param     | Effect                                                         |
| --------------- | -------------------------------------------------------------- |
| `recursive=1`   | Return the whole subtree, not just the immediate children.     |
| `meta=1`        | Annotate each `.md` entry with its frontmatter `title`/`tags`. |

Send `Accept: text/markdown` to get a human-readable Markdown index instead of
JSON.

Example listing response:

```json
{
  "path": "/notes",
  "type": "dir",
  "count": 1,
  "entries": [
    {
      "name": "hello.md",
      "path": "/notes/hello.md",
      "type": "file",
      "size": 42,
      "modified": "2026-06-15T10:00:00Z",
      "title": "Hello",
      "tags": ["demo", "intro"]
    }
  ]
}
```

### `GET /<path>?search=<query>` — full-text search

Searches all `.md` files under `<path>` (use `/` for the whole store) and
returns matching lines, including frontmatter.

| Query param   | Effect                                              |
| ------------- | --------------------------------------------------- |
| `search=...`  | The query (required to trigger search).             |
| `regex=1`     | Treat the query as a regular expression.            |
| `case=1`      | Case-sensitive (default is case-insensitive).       |
| `limit=N`     | Max number of matches (default 200).                |

```json
{
  "query": "World",
  "regex": false,
  "count": 1,
  "truncated": false,
  "matches": [
    { "path": "/notes/hello.md", "line": 5, "content": "World" }
  ]
}
```

### `PUT /<path>.md` — create or overwrite

Body = raw Markdown. Parent directories are created automatically.
Returns `201` if newly created, `200` if it replaced an existing file.

### `POST /<path>.md` — create only

Like `PUT`, but returns `409 Conflict` if the file already exists.

### `PATCH /<path>.md` — partial edit

`PATCH` edits an existing file in place. The mode is chosen by query
parameters; with no parameters it falls back to **append**. All modes accept an
optional `If-Match` header (see *Concurrency & ETags*) and return the new
`ETag` of the file.

Line numbers count **every** line in the file, including the frontmatter block,
exactly like the `lines=` read modifier — so a read with `?lines=10-12` and a
write with `?lines=10-12` address the same lines.

**Append (default).** Appends the body, inserting a single newline separator if
the file does not already end with one.

```bash
curl -X PATCH "$BASE/notes/x.md" -H "X-API-Key: $KEY" --data-binary 'one more line'
```

**Replace / insert lines.** The body becomes the replacement (an empty body
deletes the selected lines):

| Query param   | Effect                                                       |
| ------------- | ------------------------------------------------------------ |
| `lines=A-B`   | Replace the inclusive 1-based range (`A-`, `-B` allowed).    |
| `head=N`      | Replace the first N lines.                                   |
| `tail=N`      | Replace the last N lines.                                    |
| `insert=N`    | Insert the body **before** line N (1-based); nothing removed.|
| `prepend=1`   | Insert the body at the very top.                             |

```bash
# Replace lines 10-12 with the request body
curl -X PATCH "$BASE/notes/x.md?lines=10-12" -H "X-API-Key: $KEY" \
  --data-binary @snippet.md
```

**Find & replace.** Operates on the whole file; the data travels in the query,
so the body is ignored:

| Query param | Effect                                              |
| ----------- | --------------------------------------------------- |
| `replace=`  | The text (or pattern) to search for (required).     |
| `with=`     | The replacement (default: empty = delete).          |
| `regex=1`   | Treat `replace` as a regular expression (`$1` refs).|
| `case=1`    | Case-sensitive (default: case-insensitive).         |
| `all=1`     | Replace every occurrence (default: first only).     |

```bash
curl -X PATCH "$BASE/notes/x.md?replace=TODO&with=DONE&all=1" -H "X-API-Key: $KEY"
```

The response reports how many occurrences were replaced:
`{"replaced": 3, "size": 1234, "etag": "..."}`.

**Merge frontmatter.** With `?frontmatter=1` the body is parsed as
`key: value` lines (same subset as the frontmatter parser) and merged into the
file's frontmatter, creating the block if absent. A value of `null` (or `~`)
deletes the key:

```bash
curl -X PATCH "$BASE/notes/x.md?frontmatter=1" -H "X-API-Key: $KEY" \
  --data-binary $'tags: [done, archived]\nauthor: andreas'
```

All `PATCH` modes return `404` if the file does not exist and `400` on an
invalid range, pattern or empty search string.

### `DELETE /<path>` — delete

Deletes a file. For directories, pass `?recursive=true` to delete a non-empty
directory; an empty directory is removed without it.

### Concurrency & ETags

Every response for a single file carries an `ETag` header derived from the
file's full content. Two patterns build on it:

- **Conditional reads** — send `If-None-Match: <etag>` on a plain `GET`; the
  server replies `304 Not Modified` if the file is unchanged.
- **Optimistic locking** — send `If-Match: <etag>` on `PUT`, `PATCH` or
  `DELETE`. If the file changed since you read it (the ETag no longer matches),
  the write is rejected with `412 Precondition Failed` and nothing is modified.
  Use `If-Match: *` to require that the file merely exists.

A typical read-modify-write loop for an agent:

```bash
# Read and capture the current ETag
ETAG=$(curl -sD - -o body.md "$BASE/notes/x.md" -H "X-API-Key: $KEY" \
  | awk 'tolower($1)=="etag:"{print $2}' | tr -d '\r')

# Write back only if nobody else touched it in the meantime
curl -X PATCH "$BASE/notes/x.md?lines=10-12" -H "X-API-Key: $KEY" \
  -H "If-Match: $ETAG" --data-binary @snippet.md
```

Partial edits are serialised by an internal write lock, so concurrent writers
cannot corrupt a file or slip past an `If-Match` check.

### `GET /healthz` — health check

Returns `{"status":"ok"}` with no authentication. Used by the Docker
healthcheck.

## Errors

Errors are returned as JSON: `{"error": "...", "status": <code>}`.

| Status | Meaning                                              |
| ------ | ---------------------------------------------------- |
| 400    | Not a `.md` file / bad parameter / wrong node type.  |
| 401    | Missing or invalid API key.                          |
| 403    | Path escapes the data directory.                     |
| 404    | File or directory not found.                         |
| 409    | Already exists (POST) / directory not empty (DELETE).|
| 412    | `If-Match` precondition failed (file changed).       |
| 413    | Request body too large.                              |

## Security notes

- **Path traversal** is blocked: paths are cleaned and verified to stay within
  the data directory; `..` escapes are rejected with `403`.
- Only `.md` files can be written or read; dotfiles are hidden from listings.
- The API key is compared in constant time.
- Run behind TLS (a reverse proxy) in production.

## Project layout

```
.
├── Dockerfile
├── docker-compose.yml
├── README.md
└── src/
    ├── go.mod
    ├── main.go          # entry point, routing, graceful shutdown
    ├── config.go        # env configuration
    ├── middleware.go    # auth + request logging
    ├── handlers.go      # HTTP handlers for all methods
    ├── store.go         # path-safe filesystem document store
    ├── search.go        # content search + line modifiers
    └── frontmatter.go   # minimal YAML frontmatter parser
```

## Notes on frontmatter

A leading `--- ... ---` block is parsed as frontmatter. The parser supports the
common flat cases: `key: value`, inline lists (`tags: [a, b]`) and block lists
(`tags:` followed by `  - a`). `title` and `tags` are surfaced in listings and
the structured JSON view.

## 🤝 Contributing

Contributions are welcome! Feel free to open an issue or submit a Pull Request.

## 📝 License

MIT License — feel free to use this in your own projects!

## 💰 Support the project

If this project saves you time, consider supporting its development:

[![donate via Patreon](https://img.shields.io/badge/Donate-Patreon-green.svg)](https://www.patreon.com/AndreasKasper)
[![donate via PayPal](https://img.shields.io/badge/Donate-PayPal-green.svg)](https://www.paypal.me/AndreasKasper)
[![donate via Ko-fi](https://img.shields.io/badge/Donate-Ko--fi-green.svg)](https://ko-fi.com/andreaskasper)
[![Sponsors](https://img.shields.io/github/sponsors/andreaskasper)](https://github.com/sponsors/andreaskasper)

---

**Made with ❤️ by [Andreas Kasper](https://github.com/andreaskasper)**
