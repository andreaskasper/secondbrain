# secondbrain

An MCP server that gives an LLM agent structured access to a knowledge base
kept as plain Markdown files on disk — searchable, versioned, and still
readable without this program.


### Status & Stats

![Last Commit](https://img.shields.io/github/last-commit/andreaskasper/secondbrain.svg)
![Commit Activity](https://img.shields.io/github/commit-activity/m/andreaskasper/secondbrain.svg)
[![Issues](https://img.shields.io/github/issues/andreaskasper/secondbrain.svg)](https://github.com/andreaskasper/secondbrain/issues)
![Repo Size](https://img.shields.io/github/repo-size/andreaskasper/secondbrain.svg)
![Stars](https://img.shields.io/github/stars/andreaskasper/secondbrain.svg?style=social)

---

## The notes are the product

secondbrain stores nothing of its own inside your notes. A vault is a
directory of `.md` files with optional YAML frontmatter — the format Obsidian,
Logseq, Foam, `grep` and `git` already understand. Everything the server keeps
for itself lives in `<vault>/.secondbrain/`: a SQLite index, a trash
directory, and a file of conventions. All three are disposable. Delete the
container and you are left with a directory you can open in an editor, commit,
rsync or read with your eyes.

That is deliberate. A knowledge base you cannot read without its software is a
hostage, not an asset. This program is a lens over your notes, not a container
for them.

## Why the tools look the way they do

An agent editing prose fails differently from an agent editing code. Wrong
code does not compile. A wrong edit to a note is a paragraph that quietly
disappears, and you find out months later when you go looking for it.

Almost every design decision here follows from that asymmetry:

- **Section edits** and **exact-string edits**, so that changing a sentence
  does not mean rewriting a file.
- **Content hashes**, so that a note changed since it was read cannot be
  silently overwritten.
- **Dry runs** returning a unified diff, on every tool that writes.
- **Trash**, so that a deletion is a move rather than an erasure.
- **Git**, so that "what did that edit actually change" is answerable.

None of these make an agent careful. They make carelessness recoverable.

```
┌─────────┐   MCP (OAuth 2.1)   ┌──────────────┐   plain Markdown   ┌──────────┐
│   LLM   │ ──────────────────► │ secondbrain  │ ─────────────────► │  /data   │
│  agent  │ ◄────────────────── │    :2020     │ ◄───────────────── │  vaults  │
└─────────┘   notes, diffs      └──────────────┘   files on disk    └──────────┘
                                       │                                  ▲
                                 34 MCP tools                             │
                            search · read · write                    Obsidian,
                            curate · history                       git, rsync
```

- **Language:** Go 1.25, standard library plus six direct dependencies,
  `CGO_ENABLED=0`, distroless runtime
- **Transport:** MCP over Streamable HTTP on port `2020`, protocol `2025-06-18`
- **Auth:** OAuth 2.1 with Dynamic Client Registration and PKCE; secondbrain
  is its own authorization server with a login screen
- **Search:** SQLite FTS5 with BM25 ranking, pure Go, one index per vault
- **State:** notes and git on disk under `/data`; tokens and sessions in
  memory only

## Quick start

```bash
cp .env.example .env      # set at least USERNAME, PASSWORD and PUBLIC_URL
docker compose up -d
```

Or without the compose file:

```bash
docker run -d --name secondbrain -p 2020:2020 \
  -v secondbrain-data:/data \
  -e SECONDBRAIN_USERNAME=andreas \
  -e SECONDBRAIN_PASSWORD='a-long-password' \
  -e SECONDBRAIN_PUBLIC_URL=https://notes.example.com \
  ghcr.io/andreaskasper/secondbrain:latest
```

Three variables are all that is required. Everything else has a working
default, and a first start with no vaults creates `default` for you with the
`wiki-raw` layout.

Then point an MCP client at `https://notes.example.com/mcp`. The client
discovers the OAuth endpoints, registers itself, opens the login page in a
browser, and you sign in. The tool list and the vault's own conventions arrive
with the `initialize` response.

secondbrain speaks plain HTTP and does **not** terminate TLS. Put a reverse
proxy in front of it in production — see `deploy/traefik` and
`deploy/cloudflared`.

> **If a bind mount is used instead of a named volume:** the container runs as
> the distroless `nonroot` user, UID **65532**, and will fail to start on a
> directory it cannot write.
>
> ```bash
> mkdir -p ./data && sudo chown -R 65532:65532 ./data
> ```
>
> This is the single most common way a first deployment fails.

## Configuration

Environment first. A config file is optional and only worth mounting for more
than one user, or for restricting a user to some vaults.

| Variable | Default | Purpose |
| --- | --- | --- |
| `SECONDBRAIN_USERNAME` | — | Login name. Required unless a config file defines users. |
| `SECONDBRAIN_PASSWORD` | — | Literal, `bcrypt:<hash>`, `env:NAME` or `file:/path`. A literal must be at least 8 characters. |
| `SECONDBRAIN_PUBLIC_URL` | — | **Required.** External base URL, no trailing slash. Every OAuth endpoint is derived from it. |
| `SECONDBRAIN_LISTEN` | `:2020` | Listen address inside the container. |
| `SECONDBRAIN_DATA` | `/data` | Vault root. One directory per vault below it. |
| `SECONDBRAIN_DEFAULT_VAULT` | `default` | The vault a tool call means when it names none. |
| `SECONDBRAIN_CONFIG` | `/etc/secondbrain/config.yaml` | Optional config file. Absence is normal, not an error. |
| `SECONDBRAIN_GIT` | `true` | Commit every write to `<vault>/.git`. |
| `SECONDBRAIN_GIT_REMOTE` | — | Push after every commit when set. |
| `SECONDBRAIN_GIT_TOKEN` | — | Credential for that push. |
| `SECONDBRAIN_GIT_AUTHOR` | `secondbrain` | Commit author name. |
| `SECONDBRAIN_GIT_EMAIL` | `secondbrain@localhost` | Commit author email. |
| `SECONDBRAIN_MAX_RESPONSE_BYTES` | `262144` | Ceiling on one tool result before truncation. Minimum 4096. |
| `SECONDBRAIN_TOKEN_TTL` | `12h` | Access token lifetime. |
| `SECONDBRAIN_CODE_TTL` | `60s` | Authorization code lifetime. |
| `SECONDBRAIN_TRASH_RETENTION` | `720h` | How long a trashed copy is kept. 30 days. |
| `SECONDBRAIN_ALLOWED_ORIGINS` | — | Comma separated. Empty means no `Origin` check. |
| `SECONDBRAIN_LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error`. |
| `SECONDBRAIN_METRICS` | `false` | Expose the Prometheus endpoint. Off unless you ask for it. |
| `SECONDBRAIN_METRICS_PATH` | `/metrics` | Where on the listener. Must start with a slash and may not collide with `/mcp`, `/healthz` or `/`. |
| `SECONDBRAIN_METRICS_KEY` | — | Shared key a scraper must present. `env:NAME` and `file:/path` work; minimum 16 characters. |
| `SECONDBRAIN_METRICS_LISTEN` | — | For example `:9090`. Serves the metrics on their own port, and only there. |

### The optional config file

Mount a `config.yaml` at `/etc/secondbrain/config.yaml` when you need more
than one account. Every server setting above has a lower-case key here too;
the environment takes precedence for those. Users are the exception: when the
file defines a `users:` list, that list wins outright and
`SECONDBRAIN_USERNAME` / `SECONDBRAIN_PASSWORD` are ignored entirely. The
alternative would be a rule about which password takes precedence, and a rule
like that in an authentication path is a bug waiting to be written.

```yaml
public_url: "https://notes.example.com"
data_dir: "/data"
default_vault: "default"
git: true

users:
  - name: andreas
    password: "bcrypt:$2a$12$…"
    vaults: []            # empty means every vault, including ones created later
    read_only: false

  - name: research
    password: "env:RESEARCH_PASSWORD"
    vaults: ["research"]  # this user cannot address any other vault
    read_only: true       # the mutating tools are not offered at all
```

`read_only` is not a check applied late. A read-only user is never shown the
eighteen mutating tools, so the model does not learn they exist and cannot
propose an edit that would then be refused.

Generate a password hash with `docker run --rm -it
ghcr.io/andreaskasper/secondbrain hashpw`, and check a file before deploying
with `secondbrain validate /etc/secondbrain/config.yaml` — it reports every
problem at once and never prints a password.

### CLI

| Command | Purpose |
| --- | --- |
| `secondbrain` | Run the server |
| `secondbrain validate [path]` | Parse and validate a configuration, print what it means |
| `secondbrain reindex [path]` | Rebuild the search index for every vault |
| `secondbrain hashpw` | Read a password and print a bcrypt hash |
| `secondbrain version` | Version, commit, build date |

## Storage

```
/data/
└── <vault>/                    name matches ^[a-z0-9][a-z0-9_-]{0,63}$
    ├── inbox/  raw/  wiki/ …   your notes, plain Markdown
    ├── attachments/
    ├── templates/
    ├── .git/                   one repository per vault, when versioning is on
    └── .secondbrain/           everything the server keeps for itself
        ├── index.db            SQLite + FTS5. A cache. Delete it and it rebuilds.
        ├── trash/              timestamped copies of anything overwritten or deleted
        └── instructions.md     the vault's own conventions, sent to the client on connect
```

Notes are `.md` files with optional YAML frontmatter, and the Obsidian
conventions are understood as written: `[[wikilinks]]`, `[[link#anchor|alias]]`,
Markdown links, inline `#tags`, frontmatter `tags` and `aliases`, and `- [ ]`
checkboxes. `created` and `updated` are maintained automatically, but only for
notes that already have frontmatter or are being created — adding metadata to
somebody's hand-written file because a tool ran over it would be rude.

Path safety is structural rather than a blacklist. After cleaning, a path must
be relative, must stay inside the vault, and no component may begin with a
dot. That single rule also makes `.git`, `.obsidian` and `.secondbrain`
unreachable through every tool, with no per-directory special case to keep in
sync.

## Vault layouts

`vault_create` populates a new vault with a shape and, more importantly, with
an `instructions.md` describing that shape.

| Layout | Directories | The organising idea |
| --- | --- | --- |
| `wiki-raw` *(default)* | `inbox/ raw/ wiki/ journal/ projects/ people/ attachments/ templates/` | Source material is never rewritten; distilled wiki notes are rewritten constantly. Mixing them ruins a vault. |
| `zettelkasten` | `fleeting/ literature/ zettel/ journal/ attachments/ templates/` | Atomic notes, densely linked, no hierarchy beyond the buckets. |
| `para` | `projects/ areas/ resources/ archive/ journal/ attachments/ templates/` | Organised by actionability rather than by subject. |
| `empty` | `attachments/ templates/` | Directories only. Write your own conventions. |

Every layout uses `journal/YYYY/YYYY-MM-DD.md` for daily notes.

The contents of `<vault>/.secondbrain/instructions.md` are sent to the client
in the MCP `initialize` response, alongside the generic ground rules. That is
how a vault teaches an agent its own conventions without anything living in a
system prompt somewhere else — and how you change those conventions by editing
a file in the vault rather than reconfiguring a client.

None of it is enforced in code. Delete a directory and it is gone; rewrite
`instructions.md` and the agent follows the new rules. The layout is an
opinion, not a schema.

## The tools

Thirty-four tools is a lot for an MCP server, and the count is deliberate. A
model picks a tool by reading its name and description, so tools with one
clear purpose each get chosen correctly; a single tool with a `mode` parameter
and conditional arguments gets chosen incorrectly, and the failure shows up as
a mangled note rather than an error.

Every tool except `vault_list` and `vault_create` takes an optional `vault`
argument. Omitting it uses the default vault.

### Discovery

| Tool | Writes | What it does |
| --- | :---: | --- |
| `vault_list` | | The vaults available to you, with note count, size and last change. |
| `vault_create` | ● | Create a vault and populate it with one of the four layouts. |
| `vault_stats` | | Notes, words, tags, links, broken links, orphans and open tasks. |
| `note_list` | | Directory browsing by prefix or glob. Not search. |
| `note_search` | | Ranked full-text search with a snippet per hit. FTS5 syntax. |
| `vault_grep` | | Regular expression search over raw text, with context lines. |

### Reading

| Tool | Writes | What it does |
| --- | :---: | --- |
| `note_read` | | Content, frontmatter and the `content_hash` you pass back when writing. |
| `note_outline` | | The shape of a note without its text. A fraction of the cost of reading it. |
| `note_backlinks` | | What links here, what this links to, and which links point at nothing. |
| `note_related` | | Notes probably about the same thing: shared tags, links, co-citation. |
| `tag_list` | | Every tag with the number of notes carrying it, most used first. |

### Writing

| Tool | Writes | What it does |
| --- | :---: | --- |
| `note_create` | ● | Create a note. Fails on an existing path unless `overwrite` is set. |
| `note_write` | ● | Replace a whole body. `content_hash` is required. |
| `note_edit` | ● | Replace an exact string. Refused if it is not unique. |
| `note_section_edit` | ● | Change a note relative to one of its headings. Eight modes. |
| `note_frontmatter` | ● | Set fields, add or remove tags and aliases, without touching the body. |
| `note_move` | ● | Move or rename, rewriting every link that pointed at it. |
| `note_delete` | ● | Move a note to the trash and report what still links to it. |
| `note_from_template` | ● | Create from `templates/`, expanding `{{title}}`, `{{date}}` and friends. |

### Capture

| Tool | Writes | What it does |
| --- | :---: | --- |
| `daily_note` | ● | Open, create or append to `journal/YYYY/YYYY-MM-DD.md`. |
| `daily_range` | | Read a span of daily notes in one call, for weekly reviews. |
| `inbox_capture` | ● | Write something down without deciding where it belongs. |

### Curation

| Tool | Writes | What it does |
| --- | :---: | --- |
| `vault_review` | | Stubs, orphans, broken links, stale notes, open tasks. |
| `note_merge` | ● | Absorb one note into another, keeping the old title as an alias. |
| `note_split` | ● | One note per heading, each section replaced by a link. |

### Tasks

| Tool | Writes | What it does |
| --- | :---: | --- |
| `task_list` | | Every `- [ ]` in the vault, with file and line number. |
| `task_toggle` | ● | Tick a checkbox by line number or exact text. |

### Refactoring

| Tool | Writes | What it does |
| --- | :---: | --- |
| `tag_rename` | ● | Rename or merge a tag everywhere. `dry_run` defaults to true. |
| `vault_replace` | ● | Literal or regex replace across every note. `dry_run` defaults to true. |
| `attachment_list` | | Non-Markdown files with size and which notes reference them. |
| `attachment_put` | ● | Store a base64 file. Fixed extension allow-list, 32 MiB ceiling. |

### History

| Tool | Writes | What it does |
| --- | :---: | --- |
| `note_history` | | The commits that touched a note, with the tool that caused each. |
| `note_diff` | | Diff between two revisions, or a revision and the working copy. |
| `note_restore` | ● | Bring a note back to an earlier revision. Nothing is lost doing it. |

`note_history`, `note_diff` and `note_restore` need versioning enabled; with
`SECONDBRAIN_GIT=false` they report that plainly rather than failing obscurely.

## Safety properties

- **Optimistic locking.** `note_write` requires the `content_hash` returned by
  `note_read`. `note_edit`, `note_section_edit`, `note_frontmatter` and
  `note_delete` accept one optionally. A note changed since you read it cannot
  be silently overwritten.
- **Dry runs.** Every tool that rewrites note text takes `dry_run` and returns
  a unified diff instead of writing. `vault_replace` and `tag_rename` default
  it to **true**, because a vault-wide replace is the single easiest way to
  damage a knowledge base.
- **Trash.** An overwritten or deleted note is first copied to
  `<vault>/.secondbrain/trash/` with a timestamp, and purged only after the
  retention window. Recovery does not depend on git being on.
- **Versioning.** With git enabled, every write is a commit whose message
  names the tool and the path.
- **Atomic writes.** A temporary file in the same directory, then a rename. A
  crash mid-write leaves the old note or the new one, never half of each.
- **An audit log without note text.** One structured line per tool call on
  stdout: user, vault, tool, path, byte count, duration, and whether it was a
  dry run. Never note content, at any log level — an audit log that quoted
  your notes would be a second copy of the vault in the container's stdout.
- **Structural path safety.** No component of a path may begin with a dot, and
  the resolved path is verified to be inside the vault even after symlinks are
  followed. There is no pattern to outsmart, only an invariant.
- **Refusal over guessing.** `note_edit` refuses an `old_string` that appears
  twice. `note_create` refuses an existing path. `task_toggle` refuses
  ambiguous text. In each case the caller did not know what it was editing.

## Security

- OAuth 2.1 with Dynamic Client Registration; PKCE with `S256` is mandatory
  and there is no consent screen to click through.
- Passwords are compared in constant time, bcrypt when hashed, and an unknown
  username still pays the cost of a comparison so that timing does not
  enumerate accounts.
- Access and refresh tokens are stored as SHA-256 hashes in memory only.
  Refresh tokens rotate; reusing a rotated one invalidates the whole family.
- The login page is served with a strict CSP, a single-use CSRF token bound to
  the request parameters, and no external assets.
- Rate limits: failed logins per source IP, client registrations per source
  IP, and tool calls per user.
- The container does not terminate TLS. Terminate it upstream.

The full threat model, including what secondbrain deliberately does not
defend against, is on the docs site:
<https://andreaskasper.github.io/secondbrain/security.html>

## Audit log

One JSON object per line on stdout, for every tool call, successful or not:

```json
{"ts":"2026-07-30T09:14:02.183Z","level":"info","event":"tool_call",
 "tool":"note_section_edit","user":"andreas","vault":"default",
 "path":"wiki/rate-limiting.md","duration_ms":7,"results":0,"bytes":412}
```

Other events are `startup`, `shutdown`, `vault_created`, `index_reconciled`,
`config_reloaded`, `login_success`, `login_failed`, `client_registered`,
`token_issued` and `token_reuse_detected`. Note content appears in none of
them.

## Metrics

Off by default. An endpoint that reports how many notes somebody has and when
they last wrote one is not something to expose unasked, so `SECONDBRAIN_METRICS`
starts at `false` and the route does not exist until it is turned on.

When it is on there are two ways to keep it private, and they compose:

- **A shared key.** `SECONDBRAIN_METRICS_KEY` is presented as
  `Authorization: Bearer <key>` or as `X-API-Key`, compared in constant time,
  and refused below 16 characters.
- **A separate listener.** `SECONDBRAIN_METRICS_LISTEN=:9090` serves the
  metrics — and a `/healthz` — on their own port and nowhere else; the main
  listener then answers a genuine `404` rather than a `401`. A port bound
  inside the Docker network and never published cannot be reached from
  outside whatever the key is, which makes this the stronger of the two.
  `deploy/prometheus/` is that setup, ready to run.

With neither of them set the server logs `metrics_unprotected` once at startup
and carries on. The endpoint has its own rate limit bucket, 60 requests per
minute per source IP, so a scraper with the wrong key cannot be used as a
timing oracle and cannot exhaust the login limiter. Because the route is
mounted at startup, turning metrics on is a restart, not a reload.

The exposition is written by hand — there is no Prometheus client library in
the dependency list — and no metric carries a note path, a title, a tag or any
content. What there is: build info and uptime; users, OAuth clients, tokens
and MCP sessions; per vault the notes, words, bytes, tags, links, broken
links, orphans, open tasks and attachments; and counters for tool calls,
duration and result bytes, HTTP requests, logins, writes, dry runs, truncated
results, index work, watcher events, git commits and failures, and trashed
copies purged. The full list with types and labels is on the docs site:
<https://andreaskasper.github.io/secondbrain/docs.html#metrics>

The vault gauges are the ones worth a dashboard. `secondbrain_vault_broken_links`,
`secondbrain_vault_orphans` and `secondbrain_vault_open_tasks` climbing over
weeks and months is a knowledge base decaying, and that is the thing nobody
usually measures — a slow disk shows up in a graph, a rotting wiki shows up
when you go looking for something and it is not there.

## Build from source

```bash
cd src
go build -o secondbrain .
go test ./...
```

Or build the image from this checkout instead of pulling it:

```bash
docker compose -f docker-compose.yml -f docker-compose.build.yml up --build
```

An end-to-end check against a running server — OAuth discovery, registration,
the PKCE flow, and a pass over the tool surface:

```bash
python3 test/e2e.py
```

## Project layout

```
.
├── Dockerfile
├── docker-compose.yml
├── docker-compose.build.yml
├── config.example.yaml
├── .env.example
├── README.md
├── projektbeschreibung.md    # what and why (German)
├── spezifikation.md          # the full specification (German)
├── deploy/                   # Traefik and Cloudflare Tunnel examples
├── docs/                     # the documentation site
├── test/e2e.py
└── src/
    ├── main.go               # entry point, CLI, routing, shutdown
    ├── config.go             # environment and YAML, validation
    ├── reload.go             # config watcher and SIGHUP
    ├── oauth.go              # discovery, DCR, /token, PKCE
    ├── login.go              # login page, password verification
    ├── session.go            # in-memory clients, codes, tokens
    ├── mcp.go                # Streamable HTTP, JSON-RPC, sessions
    ├── tools.go              # registry, schemas, dispatch, arguments
    ├── tools_read.go         # discovery and reading
    ├── tools_write.go        # creating and editing
    ├── tools_maint.go        # curation, refactoring, history
    ├── vault.go              # vaults, path resolution, walking
    ├── note.go               # Markdown and frontmatter parsing
    ├── edit.go               # the write path, section edits, templates
    ├── index.go              # SQLite FTS5 index and queries
    ├── watch.go              # fsnotify watcher with debouncing
    ├── layouts.go            # vault layouts and instruction texts
    ├── gitstore.go           # go-git versioning
    ├── diff.go               # unified diff
    ├── ratelimit.go          # token buckets
    ├── metrics.go            # the metric registry and exposition format
    ├── metrics_http.go       # the endpoint, its key check and second listener
    └── audit.go              # structured logging
```

## Documentation

- Docs site: <https://andreaskasper.github.io/secondbrain/>
- `projektbeschreibung.md` — the reasoning, in German
- `spezifikation.md` — the technical specification, in German
- Sibling project: [aegis](https://github.com/andreaskasper/aegis), a secrets
  firewall for LLM agents. Same author, same architecture, same look.

## Contributing

Contributions are welcome. Open an issue or submit a pull request.

## License

MIT License, (c) 2026 Andreas Kasper. See `LICENSE`.

## Support the project

If this project saves you time, consider supporting its development:

[![donate via Patreon](https://img.shields.io/badge/Donate-Patreon-green.svg)](https://www.patreon.com/AndreasKasper)
[![donate via PayPal](https://img.shields.io/badge/Donate-PayPal-green.svg)](https://www.paypal.me/AndreasKasper)
[![donate via Ko-fi](https://img.shields.io/badge/Donate-Ko--fi-green.svg)](https://ko-fi.com/andreaskasper)
[![Sponsors](https://img.shields.io/github/sponsors/andreaskasper)](https://github.com/sponsors/andreaskasper)

---

Made by [Andreas Kasper](https://github.com/andreaskasper)
