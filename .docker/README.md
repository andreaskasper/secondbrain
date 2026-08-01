# secondbrain 🧠

**A knowledge base your agent can use and you can still read without it.**

secondbrain is an [MCP](https://modelcontextprotocol.io) server that gives an LLM
agent structured access to a vault of plain Markdown files — full-text search,
backlinks, tags, daily notes, tasks and git history.

[![Source](https://img.shields.io/badge/source-github-181717?logo=github)](https://github.com/andreaskasper/secondbrain)
[![Docs](https://img.shields.io/badge/docs-website-0b7285)](https://andreaskasper.github.io/secondbrain/)
[![License](https://img.shields.io/badge/license-MIT-blue)](https://github.com/andreaskasper/secondbrain/blob/main/LICENSE)
[![Image size](https://img.shields.io/docker/image-size/andreaskasper/secondbrain/latest)](https://hub.docker.com/r/andreaskasper/secondbrain/tags)
[![Pulls](https://img.shields.io/docker/pulls/andreaskasper/secondbrain)](https://hub.docker.com/r/andreaskasper/secondbrain)

---

## The notes are the product

secondbrain stores nothing of its own inside your notes. A vault is a directory
of `.md` files with optional YAML frontmatter — the format Obsidian, Logseq,
Foam, `grep` and `git` already understand. Everything the server keeps for itself
lives in `<vault>/.secondbrain/`: a SQLite index, a trash directory, and a file
of conventions. All three are disposable.

Delete the container and you are left with a directory you can open in an editor,
commit, rsync or read with your eyes. A knowledge base you cannot read without
its software is a hostage, not an asset.

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

## Quick start

```bash
docker run -d --name secondbrain -p 2020:2020 \
  -v secondbrain-data:/data \
  -e SECONDBRAIN_USERNAME=andreas \
  -e SECONDBRAIN_PASSWORD='a-long-password' \
  -e SECONDBRAIN_PUBLIC_URL=https://notes.example.com \
  andreaskasper/secondbrain:latest
```

Three variables are all that is required. Everything else has a working default,
and a first start with no vaults creates `default` for you.

Then point an MCP client at `https://notes.example.com/mcp`. The client discovers
the OAuth endpoints, registers itself, opens the login page in a browser, and you
sign in. The tool list and the vault's own conventions arrive with the
`initialize` response.

secondbrain speaks plain HTTP and does **not** terminate TLS. Put a reverse proxy
in front of it in production — Traefik and Cloudflare Tunnel examples are
[in the repository](https://github.com/andreaskasper/secondbrain/tree/main/deploy).

### ⚠️ Using a bind mount instead of a named volume?

The container runs as the distroless `nonroot` user, **UID 65532**, and will
refuse to start on a directory it cannot write.

```bash
mkdir -p ./data && sudo chown -R 65532:65532 ./data
```

This is the single most common way a first deployment fails.

### With Compose

```yaml
services:
  secondbrain:
    image: andreaskasper/secondbrain:latest
    ports: ["2020:2020"]
    volumes:
      - secondbrain-data:/data
    environment:
      SECONDBRAIN_USERNAME: andreas
      SECONDBRAIN_PASSWORD: a-long-password
      SECONDBRAIN_PUBLIC_URL: https://notes.example.com
    restart: unless-stopped

volumes:
  secondbrain-data:
```

## Configuration

Environment first. A config file is optional and only worth mounting for more
than one user, or to restrict a user to some vaults.

| Variable                         | Default                        | Purpose                                                                       |
| -------------------------------- | ------------------------------ | ----------------------------------------------------------------------------- |
| `SECONDBRAIN_USERNAME`           | —                              | Login name. Required unless a config file defines users.                       |
| `SECONDBRAIN_PASSWORD`           | —                              | Literal, `bcrypt:<hash>`, `env:NAME` or `file:/path`. A literal needs 8+ chars. |
| `SECONDBRAIN_PUBLIC_URL`         | —                              | **Required.** External base URL, no trailing slash.                            |
| `SECONDBRAIN_LISTEN`             | `:2020`                        | Listen address inside the container.                                           |
| `SECONDBRAIN_DATA`               | `/data`                        | Vault root. One directory per vault below it.                                  |
| `SECONDBRAIN_DEFAULT_VAULT`      | `default`                      | The vault a tool call means when it names none.                                |
| `SECONDBRAIN_GIT`                | `true`                         | Commit every write to `<vault>/.git`.                                          |
| `SECONDBRAIN_TRASH_RETENTION`    | `720h`                         | How long a trashed copy is kept.                                               |
| `SECONDBRAIN_TOKEN_TTL`          | `12h`                          | Access token lifetime.                                                         |
| `SECONDBRAIN_METRICS`            | `false`                        | Expose the Prometheus endpoint. Off unless you ask for it.                     |
| `SECONDBRAIN_LOG_LEVEL`          | `info`                         | `debug`, `info`, `warn`, `error`.                                              |
| `SECONDBRAIN_CONFIG`             | `/etc/secondbrain/config.yaml` | Optional config file. Absence is normal, not an error.                         |

The [full table](https://andreaskasper.github.io/secondbrain/docs.html) includes
git remotes, metrics keys, a separate metrics listener, rate limits and response
ceilings.

Generate a password hash with `docker run --rm -it andreaskasper/secondbrain
hashpw`. Check a config before deploying with `secondbrain validate` — it reports
every problem at once and never prints a password.

## Why the tools look the way they do

An agent editing prose fails differently from an agent editing code. Wrong code
does not compile. A wrong edit to a note is a paragraph that quietly disappears,
and you find out months later when you go looking for it.

Almost every design decision follows from that asymmetry:

- **Section edits** and **exact-string edits**, so changing a sentence does not
  mean rewriting a file.
- **Content hashes**, so a note changed since it was read cannot be silently
  overwritten.
- **Dry runs** returning a unified diff, on every tool that writes —
  defaulting to *true* for the vault-wide ones.
- **Trash**, so a deletion is a move rather than an erasure.
- **Git**, so "what did that edit actually change" is answerable.
- **Refusal over guessing.** An `old_string` that appears twice is refused, not
  guessed at.

None of these make an agent careful. They make carelessness recoverable.

## The tools

Thirty-four, grouped: discovery and search (`note_search` with FTS5 ranking,
`vault_grep`, `note_list`), reading (`note_read`, `note_outline`,
`note_backlinks`, `note_related`), writing (`note_create`, `note_edit`,
`note_section_edit`, `note_frontmatter`, `note_move`, `note_delete`), capture
(`daily_note`, `inbox_capture`), curation (`vault_review`, `note_merge`,
`note_split`), tasks, tag and vault-wide refactoring, attachments, and git
history (`note_history`, `note_diff`, `note_restore`).

The count is deliberate. A model picks a tool by reading its name and
description; tools with one clear purpose each get chosen correctly, and a single
tool with a `mode` parameter gets chosen incorrectly — with the failure showing
up as a mangled note rather than an error.

A `read_only` user is never shown the eighteen mutating tools at all, so the
model does not learn they exist.

## Vault layouts

`vault_create` populates a new vault with a shape *and* an `instructions.md`
describing it, which is sent to the client on connect. That is how a vault
teaches an agent its own conventions without anything living in a system prompt
somewhere else.

| Layout                 | The organising idea                                                     |
| ---------------------- | ----------------------------------------------------------------------- |
| `wiki-raw` *(default)* | Source material is never rewritten; distilled wiki notes constantly are. |
| `zettelkasten`         | Atomic notes, densely linked, no hierarchy beyond the buckets.           |
| `para`                 | Organised by actionability rather than by subject.                       |
| `empty`                | Directories only. Write your own conventions.                            |

None of it is enforced in code. The layout is an opinion, not a schema.

## Storage

```
/data/
└── <vault>/
    ├── inbox/  raw/  wiki/ …   your notes, plain Markdown
    ├── attachments/  templates/
    ├── .git/                   one repository per vault
    └── .secondbrain/
        ├── index.db            SQLite + FTS5. A cache. Delete it, it rebuilds.
        ├── trash/              timestamped copies of anything overwritten
        └── instructions.md     the vault's conventions, sent to the client
```

Path safety is structural, not a blacklist: after cleaning, a path must be
relative, stay inside the vault, and no component may begin with a dot. That one
rule also makes `.git`, `.obsidian` and `.secondbrain` unreachable through every
tool, with no special case to keep in sync.

## Security

- OAuth 2.1 with Dynamic Client Registration; PKCE `S256` mandatory.
- Tokens stored as SHA-256 hashes, in memory only. Refresh tokens rotate; reusing
  a rotated one invalidates the whole family.
- Constant-time password comparison, and an unknown username still pays the cost
  so timing cannot enumerate accounts.
- Strict CSP on the login page, single-use CSRF token, no external assets.
- Rate limits on failed logins, client registrations and tool calls.
- An audit log that never contains note text, at any log level.

The full threat model, including what secondbrain deliberately does not defend
against: <https://andreaskasper.github.io/secondbrain/security.html>

## The image

- Built `FROM gcr.io/distroless/static-debian12:nonroot` — no shell, no package
  manager, one static Go binary. Pure-Go SQLite, so no libc.
- Runs as `nonroot`, UID 65532. `/data` must be writable by that UID.
- `linux/amd64` and `linux/arm64` — arm64 is a first-class target, a good deal of
  this runs on small home servers.
- Every release carries an SBOM and a signed
  [build provenance attestation](https://github.com/andreaskasper/secondbrain/attestations).

### Tags

| Tag            | Points at                      |
| -------------- | ------------------------------ |
| `latest`       | the most recent release        |
| `1.0.260731`   | that exact release             |
| `1.0`, `1`     | the newest release of that line |
| `sha-1a2b3c4`  | one exact commit               |

### Also on GitHub Container Registry

The identical image — same digest, same build:

```bash
docker pull ghcr.io/andreaskasper/secondbrain:latest
```

Static binaries for Linux and macOS, if you would rather not run a container, are
attached to each [GitHub release](https://github.com/andreaskasper/secondbrain/releases).

## Links

- **Source & issues:** <https://github.com/andreaskasper/secondbrain>
- **Documentation:** <https://andreaskasper.github.io/secondbrain/>
- **Sibling project:** [aegis](https://hub.docker.com/r/andreaskasper/aegis) — a
  secrets firewall for LLM agents. Same author, same architecture.
- **License:** MIT

Made by [Andreas Kasper](https://github.com/andreaskasper)
