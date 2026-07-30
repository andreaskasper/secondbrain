# Troubleshooting

Every error the server can return, what it actually means, and what to do next.
The general shape: this server refuses rather than guesses. An error here is
almost always the safety mechanism working, not a bug to route around.

---

## `the note changed since it was read: <path> has content_hash X, not Y - read it again before writing`

**What happened.** You passed a `content_hash` that no longer matches the file
on disk. Something wrote to it between your read and your write: Obsidian, a
git pull, rsync, another agent, or an earlier tool call of your own.

**What to do.** Do not retry with the same payload — you would be reapplying an
edit computed against text that no longer exists.

1. `note_read` the note again.
2. Look at what actually changed. If versioning is on, `note_diff` with no
   `from`/`to` compares the previous commit against the working copy.
3. Decide whether your edit still applies. Often it does, unchanged; sometimes
   the other writer already did it.
4. Re-apply against the new text, with the new hash.

**Related variant.** If you pass a `content_hash` for a note that does not
exist, the error is `note not found`, not a hash mismatch — the file was
deleted or moved out from under you.

**Prevention.** Keep the read-to-write gap short. Do not read ten notes, think,
and then write ten times.

---

## `old_string appears N times; include more surrounding text to make it unique, or set replace_all`

**What happened.** `note_edit` found your snippet more than once. It refuses
rather than picking one, because a caller who supplied an ambiguous snippet did
not know which occurrence they meant.

**What to do.** Extend `old_string` upward or downward until it is unique —
include the preceding line, the heading, or the sentence before. Anchoring to a
heading line is usually enough.

Only set `replace_all: true` when you genuinely mean every occurrence, and
prefer `vault_replace` with `dry_run` if the change spans notes.

**Sibling errors from the same tool.**

- `old_string must not be empty` — you passed `""`. To add text, use
  `note_section_edit` with `append_to_note` or `append_to_section`.
- `old_string and new_string are identical` — nothing to do; usually a sign
  that you reconstructed the "new" text from a stale read.
- If `old_string` is not found at all, the edit fails because the text is not
  there. Whitespace is the usual culprit: `note_edit` matches character for
  character, including indentation and trailing spaces. `note_read` the section
  and copy the exact bytes rather than retyping them.

---

## `no heading matching "X" in <path> - call note_outline to see the headings that exist`

**What happened.** `note_section_edit` (or `note_read` with `heading`) matches
headings by their text. Yours does not exist — wrong wording, wrong case,
trailing punctuation, or the heading was renamed.

**What to do.**

```json
{"tool": "note_outline", "args": {"path": "wiki/observability.md"}}
```

Use the exact heading text from the outline. If the section is genuinely meant
to be new, pass `create: true` and `note_section_edit` will add it:

```json
{"tool": "note_section_edit", "args": {
  "path": "wiki/observability.md", "mode": "append_to_section",
  "heading": "Related", "create": true, "content": "- [[sampling-is-lossy]]"}}
```

**Remember the section definition.** A section runs from its heading to the
next heading of the same or higher level, subsections included. So
`replace_section` on a level-2 heading replaces its level-3 children too. That
is intentional, and it is the most common surprise in this tool.

**Also from this tool.**

- `heading is required for mode <mode>` — every mode except `append_to_note`
  and `prepend_to_note` needs one.
- `content is required for mode <mode>` — every mode except `delete_section`
  needs one.

---

## `unknown vault`

**What happened.** The `vault` argument names a vault that does not exist, or
one this user is not permitted to see (config.yaml can restrict a user to a
list of vaults).

**What to do.**

```json
{"tool": "vault_list", "args": {}}
```

Use a name from that list, or omit `vault` entirely to get the server default.
If the vault should exist but does not, it was never created — `vault_create`
makes it, and `references/vault-layouts.md` covers choosing a layout. Do not
run `vault_create` over a directory that already holds notes.

**Related.** `you may not create a vault named "..."` from `vault_create` means
the name fails `^[a-z0-9][a-z0-9_-]{0,63}$` or is reserved. Lower-case,
alphanumeric, hyphens and underscores, starting with a letter or digit.

---

## `invalid path: path escapes the vault` and `invalid path: "..." is a hidden path and is not reachable through the API`

**What happened.** Path safety is structural, not a blacklist. After cleaning,
a path must be relative, must stay inside the vault root, and no component may
begin with a dot.

**What to do.** There is no workaround, and looking for one is the wrong
instinct. The consequence of that single rule is that `.git`, `.obsidian` and
`.secondbrain` are unreachable through every tool — which is why the search
index, the trash and the vault's instructions cannot be damaged by a note
operation, and why an agent cannot write into a git hook.

If the user needs `.secondbrain/instructions.md` changed, tell them exactly
what to put in it and let them edit it on disk. If they want a note whose name
starts with a dot, they do not.

---

## `a note already exists at that path`

**Returned by** `note_create`, `note_from_template`, `note_move`, `note_split`
and `attachment_put`.

**What happened.** The duplicate guard fired. From `note_create` this is the
single most useful error the server produces: you searched badly (or not at
all) and were about to create a second note on an existing subject.

**What to do, in order of preference.**

1. `note_read` the existing note. Nine times out of ten you should be adding to
   it with `note_section_edit`, not creating anything.
2. If it is genuinely a different subject, pick a different path — a more
   specific title, usually.
3. `overwrite: true` only after you have read what you are about to replace and
   are sure it should go. The old content goes to the trash and, with
   versioning on, remains in git, so it is recoverable — but recovering it
   requires knowing you destroyed it.

For `note_move`, `overwrite` behaves the same way and the destination's content
is what is at risk.

---

## `"<ext>" is not an accepted attachment type. Allowed: ...`

**What happened.** `attachment_put` enforces a fixed allow-list:
`.csv .gif .jpeg .jpg .json .m4a .mp3 .pdf .png .svg .txt .wav .webp .yaml
.yml`. An agent that can write arbitrary bytes into a mounted volume is a
bigger problem than the convenience is worth.

**What to do.** Convert it, or do not store it. A zip, an executable or an
office document does not belong in a knowledge base; a link to where it lives
does.

**Sibling errors.**

- `data is not valid base64` — the payload must be standard base64. Strip data
  URL prefixes (`data:image/png;base64,`) before sending.
- `attachment is N bytes; the limit is 32 MiB` — no override exists.
- Textual content in `attachments/` is legal (`.txt`, `.csv`) but is a mistake:
  it has no frontmatter, no tags and no backlinks. Prose goes in `raw/`.

---

## `versioning is off for this vault: start the server with SECONDBRAIN_GIT=true`

**Returned by** `note_history`, `note_diff` and `note_restore`.

**What happened.** Git versioning is disabled for this deployment. Those three
tools have nothing to read.

**What to do.** Recovery is still possible: nothing is ever `os.Remove`d.
Overwritten and deleted notes go to `<vault>/.secondbrain/trash/` with a
timestamp, kept for the retention window (720h / 30 days by default). That
directory is not reachable through the tools — the user retrieves from it on
disk or in the container.

Turning versioning on is a server restart with `SECONDBRAIN_GIT=true`; it does
not retroactively create history.

**Related.** `no history for <path> yet` means versioning is on but this note
has never been committed — it was created outside the server and not yet
touched by a write.

---

## `[truncated: the result was N bytes, the limit is M. Narrow the query, or use limit/offset.]`

**What happened.** The response exceeded `SECONDBRAIN_MAX_RESPONSE_BYTES`
(262144 by default) and was cut.

**What to do.** Do not re-issue the same call hoping for a different result.

- `note_search`, `note_list`, `tag_list`, `task_list`, `attachment_list`,
  `note_history`: use `limit` and `offset` to page. Search caps `limit` at 200
  and defaults to 25.
- `vault_grep`: reduce `context`, lower `limit`, set `include_content: false`,
  or narrow with `prefix`. Its own result carries a `truncated` flag and a
  `notes_scanned` count, so you can tell a truncated scan from an exhausted
  one.
- `note_read` on a very long note: read by `heading` instead, after
  `note_outline`.
- `daily_range`: shorten the span, or set `empty: false` to skip days with no
  note.

---

## Search returns nothing but the note clearly exists

Two different causes, and they are distinguishable.

**Cause 1: tokenisation.** `note_search` is SQLite FTS5. It indexes words. It
cannot match a URL, a code fragment, a partial identifier, a hyphenated
compound in the middle, or punctuation. Searching for
`https://example.com/docs` or `getUserByID` or `--dry-run` will find nothing
even though the text is right there.

Use `vault_grep`, which runs a regular expression over the raw text:

```json
{"tool": "vault_grep", "args": {
  "pattern": "getUserBy[A-Za-z]+", "context": 2, "prefix": "wiki/", "limit": 50}}
```

It is slower than `note_search` and is the right tool here, not a fallback for
laziness. Note that `pattern` is a Go regular expression; an invalid one
returns `invalid regular expression: ...`.

Other FTS5 things worth knowing: bare words are ANDed, `"quoted phrases"` match
exactly, `prefix*` works, `OR` and `NOT` are available. A query that is too
specific returns nothing for ordinary reasons — try one word before concluding
anything is broken.

**Cause 2: the index is stale.** Confirm with `vault_stats`: if the note count
does not match reality, or a note you just created is missing, the index and
the filesystem have diverged.

The index is a **cache**. It lives in `<vault>/.secondbrain/index.db`, holds no
authoritative data, and can be deleted. It is reconciled against the filesystem
at startup and kept live by an fsnotify watcher debounced at 400ms — so
Obsidian, a git pull or rsync writing into the same directory is normal, not a
hazard. Divergence usually means a bulk change landed while the server was down
or the watcher missed a large batch.

```
secondbrain reindex [path]
```

rebuilds it from the Markdown files and loses nothing. `secondbrain validate
[path]` checks a vault without changing it. Neither touches a note.

---

## `this would touch more than N notes. Narrow it with prefix, or raise limit deliberately`

**Returned by** `vault_replace`.

**What happened.** The replacement matched more notes than `limit` allows. A
vault-wide replace is the single easiest way to damage a knowledge base, so the
ceiling is a refusal rather than a warning.

**What to do.** Narrow with `prefix` first — most replacements are actually
scoped to one directory. Preview the match set with `vault_grep` using the same
pattern. Raise `limit` only when you have seen the list and mean it, and read
the `dry_run` diff (which is the default) before setting `dry_run: false`.

The same instinct applies to `tag_rename`, which also defaults `dry_run` to
true and reports `dry run: #<tag> appears in N note(s). Set dry_run false to
apply.`

---

## Task errors

From `task_toggle`:

- `pass either line or text to identify the task` — you passed neither.
- `<path> has only N lines` / `line N of <path> is not a checkbox - call
  task_list for current line numbers` — line numbers move as soon as anything
  above them changes. Always toggle from a **fresh** `task_list`, never from
  one taken before an edit.
- `no task with that exact text in <path>` — `text` matches exactly, including
  the checkbox marker's surroundings. Copy it from `task_list` output.
- `N tasks in <path> have that text; use line instead` — the same ambiguity
  refusal as `note_edit`, for the same reason.

---

## Merge, split and date errors

- `a note cannot be merged into itself` — `path` and `from` are the same.
- `merged the content but could not remove <path>: ...` — partial failure. The
  target now has the content and the source still exists. Verify with
  `note_read`, then delete the source yourself with `note_delete`.
- `<path> has no level N headings to split at - call note_outline to see its
  structure` — you guessed the level. `note_split` defaults to 2; check the
  outline.
- `no template named "X" - call note_from_template without arguments to list
  them` — templates live in `templates/` in the vault, and each layout installs
  a different set.
- `date must be YYYY-MM-DD`, `from must be YYYY-MM-DD or a span such as 7d`,
  `that range is longer than a year; narrow it`, `stale_after must be a span
  such as 365d or 6m` — the date parsers accept `YYYY-MM-DD`, RFC3339 where
  documented, and relative spans like `7d`, `6m`, `365d`.

---

## Nothing is failing, but a write did not appear

Three things to check before assuming a bug.

1. **`dry_run` was set.** The result carries `"dry_run": true` and
   `"message": "dry run: nothing was written"`. `vault_replace` and
   `tag_rename` default it to true.
2. **The result says `"no change"`.** The transform produced exactly the
   current content — usually because the edit had already been applied.
3. **This connection is read-only.** A user configured with `read_only: true`
   is not offered the mutating tools at all, so they are absent from
   `tools/list` rather than failing when called. If you cannot find
   `note_create`, that is why.

---

## Deployment problems that look like tool problems

- **Permission denied writing anything.** The distroless runtime user is UID
  65532. A bind-mounted `/data` needs `chown -R 65532:65532`. This is the
  single most common way a first deployment fails.
- **The container will not start read-only.** It cannot run with a read-only
  root filesystem; unlike its sibling project it writes notes, an index and a
  git repository. It does drop all capabilities and run non-root.
- **TLS errors.** The container does not terminate TLS. Put Traefik or a
  Cloudflare Tunnel in front, and set `SECONDBRAIN_PUBLIC_URL` to the external
  base URL with no trailing slash — OAuth discovery is built from it, so a
  wrong value breaks authentication in ways that look like client bugs.
