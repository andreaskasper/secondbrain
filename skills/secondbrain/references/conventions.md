# Conventions

## Filenames and slugs

Notes are `.md` files under the vault root. Paths are relative, must stay
inside the vault, and no path component may begin with a dot. That single rule
is what makes `.git`, `.obsidian` and `.secondbrain` unreachable through every
tool; it is structural, not a blacklist, so there is no clever path that gets
around it.

The server slugs a title when it needs a filename (`inbox_capture`,
`note_from_template`'s `{{slug}}`, `note_split`):

- lower-cased and trimmed
- `ä ö ü ß` → `ae oe ue ss` (German is handled explicitly; other diacritics are
  not transliterated)
- every run of non-alphanumeric characters becomes a single `-`
- leading and trailing `-` stripped, truncated to 80 characters
- an empty result becomes `note`

Follow the same style when you choose a path yourself: lower-case,
hyphen-separated, descriptive. `wiki/backpressure-is-a-contract.md`, not
`wiki/Backpressure Is A Contract.md` and not `wiki/note-17.md`. Spaces work but
make every later `[[link]]` and shell command more awkward.

Generated filename patterns:

- `inbox_capture` → `inbox/YYYY-MM-DD-HHMM-<slug>.md`
- `daily_note` / `daily_range` → `journal/YYYY/YYYY-MM-DD.md` in **every**
  layout. Do not invent `daily/2026-07-30.md` or `journal/2026-07-30.md`; the
  tools will not find them.
- `note_split` → one note per heading, in `dir` if given, otherwise alongside
  the original.

## Frontmatter

YAML between `---` fences at the top of the file. Two categories:

**Maintained by the server.** `created` and `updated`, both RFC3339 UTC.
`created` is set if absent; `updated` is set on every write. The important
detail: this only happens for notes that **already have frontmatter** or are
**being created**. A hand-written note with no frontmatter block is never given
one — the server will not add metadata to a file the user chose to keep plain.
So if you want timestamps on such a note, you have to add a frontmatter block
deliberately, and you should ask first.

**The user's.** `title`, `tags`, `aliases`, plus whatever the templates
establish: `source`, `retrieved`, `author`, `year`, `url`, `status`, `date`,
`attendees`, `captured`. Treat unknown fields as load-bearing; the user or a
plugin may rely on them.

Change metadata with `note_frontmatter`, never by rewriting the note:

```json
{"tool": "note_frontmatter", "args": {
  "path": "wiki/backpressure-is-a-contract.md",
  "set": {"status": "evergreen"},
  "add_tags": ["architecture", "reliability"],
  "add_aliases": ["Backpressure"],
  "remove_tags": ["draft"]
}}
```

`note_write` has `keep_frontmatter` so a body replacement does not clobber
metadata. If you find yourself reaching for it, check first whether
`note_section_edit` with `replace_section` does what you want.

## Tags describe kind, folders describe topic

This is the one rule that keeps both systems useful. Folders answer "what is
this about" and are few. Tags answer "what kind of thing is this" and are many:
`#meeting`, `#raw`, `#zettel`, `#project`, `#question`, `#reference`,
`#draft`, `#person`.

Do not mirror the folder tree in tags. A note in `wiki/architecture/` does not
need `#wiki` or `#architecture` — the path already says both, and now you have
two places to keep in sync and one of them will drift. The test: if a tag could
be derived from the path, it is redundant.

Tags are normalised: a leading `#` is stripped, surrounding `/` trimmed, and
the whole thing lower-cased. `#Meeting`, `meeting` and `#meeting/` are one tag.
Frontmatter `tags` and inline `#tags` in the body are merged by `tag_list` and
by `note_search`'s `tags` filter — there is no difference between them as far
as the index is concerned. Prefer frontmatter for tags that classify the whole
note and inline tags for something that applies to one paragraph.

### Hierarchies with `/`

`#source/paper`, `#source/talk`, `#source/thread`; `#project/website`,
`#project/website/launch`. The `/` is just a character in the tag name — there
is no tree in the index — but it groups usefully in `tag_list` (`prefix:
"source/"`) and in Obsidian's tag pane, and it gives you a rename path:

```json
{"tool": "tag_rename", "args": {"from": "source", "to": "source/paper", "dry_run": true}}
```

Keep hierarchies two levels deep at most. Three levels is a folder tree that
escaped into the tag namespace.

## Links

Two syntaxes, both indexed:

- `[[note]]`, `[[note|shown text]]`, `[[note#Heading]]`, `[[note#Heading|text]]`
- `[shown text](path/to/note.md)`, `[text](path/to/note.md#heading)`

Prefer wikilinks in note bodies. They survive `note_move` more legibly and they
are what Obsidian users expect.

### How link resolution actually works

The index maps every link target onto a real path so that backlinks and
broken-link reports are a query rather than a scan. The order mirrors Obsidian
and matches what a person expects. For a link with target `t` in note `src`:

1. If `t` starts with `./` or `../`, it is first resolved against the linking
   note's directory. Then any leading `/` is stripped.
2. **Exact path.** `t` matches a note's full path, including `.md`.
   `[[wiki/backpressure-is-a-contract.md]]`.
3. **Path without extension.** `t + ".md"` matches a note's path.
   `[[wiki/backpressure-is-a-contract]]`. This is the form to write.
4. **Unique basename**, case-insensitive. `[[backpressure-is-a-contract]]`
   resolves if exactly one note in the whole vault has that filename stem.
5. **Note title**, case-insensitive, from the frontmatter. `[[Backpressure is a
   contract, not a failure]]` resolves if exactly one note has that title.
6. **Alias**, case-insensitive, from frontmatter `aliases`.

Steps 4, 5 and 6 all require the match to be **unique**. Two notes named
`index.md` in different directories mean `[[index]]` resolves to neither and is
reported as a broken link — not to the nearest one, not to the first one. The
same is true for two notes sharing a title, and for an alias that collides with
another note's basename.

Consequences worth internalising:

- A bare `[[basename]]` link is fragile. It works until somebody creates a
  second note with that stem anywhere in the vault, and then it silently breaks
  — silently in the sense that nothing errors; `note_backlinks` and
  `vault_review` with `only: "broken_link"` will show it.
- Writing `[[dir/name]]` (step 3) is the robust form and is what `note_move`
  and `note_split` generate.
- Aliases are how you keep old links alive after a rename, and `note_merge`
  uses them automatically: the source note's title becomes an alias on the
  target, so every `[[old title]]` still resolves.
- Percent-encoded characters in markdown link targets are decoded before
  resolution, so `[text](wiki/a%20note.md)` finds `wiki/a note.md`.

Check a note's position with:

```json
{"tool": "note_backlinks", "args": {"path": "wiki/backpressure-is-a-contract.md"}}
```
→ inbound links, outbound links, and which outbound links point at nothing.

### Anchors

`[[note#Heading]]` and `[](note.md#heading)` point at a section. The anchor is
the heading text. It is not verified at index time — a link to a heading that
no longer exists still resolves to the note, so renaming a heading breaks
navigation without breaking the link report. If you rename a heading in a
well-linked note, `vault_grep` for `#Old Heading` before you move on.

`note_read` takes the same idea as an argument:

```json
{"tool": "note_read", "args": {"path": "wiki/observability.md", "heading": "Sampling"}}
```

A section runs from its heading to the next heading of the same or higher
level, subsections included. That definition is shared by `note_read`,
`note_section_edit` and `note_split`, so what you read is exactly what you edit.

## Attachments

Non-Markdown files live in `attachments/`. `attachment_put` takes base64 and
enforces an allow-list — `.png .jpg .jpeg .gif .webp .svg .pdf .txt .csv .json
.yaml .yml .mp3 .m4a .wav` — with a 32 MiB ceiling. An agent that can write
arbitrary bytes into a mounted volume is a bigger problem than the convenience
is worth.

Reference them from notes like any other link: `![[attachments/diagram.png]]`
or `[the paper](attachments/fowler-2024.pdf)`. `attachment_list` reports size
and which notes reference each file, so unreferenced attachments are visible.

Never put textual content in `attachments/`. A `.txt` or `.csv` there is
findable by `vault_grep` but is not a note, has no frontmatter, no tags and no
backlinks. If it is prose, it belongs in `raw/`.

## Checkbox tasks

`- [ ]` open, `- [x]` done, anywhere in any note. There is no separate task
system; the vault is the task system.

```json
{"tool": "task_list", "args": {"prefix": "projects/", "contains": "invoice", "limit": 50}}
```
→ each task with its path, line number and text.

```json
{"tool": "task_toggle", "args": {"path": "projects/website.md", "line": 34, "done": true}}
```

Identify by `line` from a **fresh** `task_list` — line numbers move as soon as
anything above them changes — or by exact `text` when it is unique within that
note. Ambiguous text is refused (`N tasks in <path> have that text; use line
instead`) rather than guessed, and a line that is not a checkbox is refused
with a pointer back to `task_list`.

Put tasks where the context is: inside the project note, the meeting note or
the daily note, under a heading like `## Actions`. A task in a note nobody
opens is not a task.

## The journal

`journal/YYYY/YYYY-MM-DD.md`, in every layout, without exception. The year
directory keeps the folder listable after a few years.

```json
{"tool": "daily_note", "args": {"append": "Shipped the importer.", "heading": "Log"}}
```

Creates the note if it does not exist. With no `heading`, the current time is
used as a heading, which gives a chronological log for free. With no `append`,
it just reads the note.

```json
{"tool": "daily_range", "args": {"from": "7d", "empty": false}}
```

Reads a span in one call — built for reviews, where fetching seven notes
individually is seven round trips and a lot of context spent on nothing.
`from` accepts `YYYY-MM-DD` or a span such as `7d`; the range is capped at a
year.
