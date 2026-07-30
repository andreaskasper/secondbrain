# Workflows

## The lifecycle

Five stages. Most damage happens when two of them are collapsed into one — most
often capture and distil, where a passing remark is written straight into the
wiki as if it were understood.

1. **Capture.** Get it down, decide nothing. `inbox_capture`, or `daily_note`
   with `append` when it belongs to the day rather than to a subject. Speed
   matters more than placement; a thought you stopped to file is a thought you
   interrupted.
2. **Clarify.** Later, ask what each captured item actually is: a source, a
   task, an idea, a reference, or noise. Noise is deleted — an inbox that is
   never emptied stops being read.
3. **Distil.** Write it in your own words, one idea per note. This is the only
   stage that creates lasting value and the only one that cannot be skipped by
   pasting harder.
4. **Connect.** Every new note earns at least one link, and the link line says
   *why*. `note_related` and `note_backlinks` find the neighbours.
5. **Review.** Weekly and monthly. `daily_range`, `vault_review`, `task_list`,
   `tag_list`. A knowledge base decays quietly; review is the only thing that
   makes the decay visible.

---

## Processing the inbox

Run this weekly, or whenever `inbox/` has more than a dozen items.

```json
{"tool": "note_list", "args": {"prefix": "inbox/", "sort": "created", "limit": 50}}
```

For each item, read it (they are short — `note_read` is fine here), then decide
one of five outcomes:

- **Noise.** `note_delete`. It goes to the trash, nothing is erased.
- **A task.** Append a `- [ ]` line to the relevant project note with
  `note_section_edit`, then delete the inbox note.
- **Source material.** `note_move` it into `raw/` (wiki-raw) or write a
  literature note from it (zettelkasten), then delete the capture.
- **An idea that already has a home.** Search first, then
  `note_section_edit` it into the existing note. This is the common case and
  the one that prevents duplicates.
- **A new idea.** Search, confirm nothing covers it, then `note_create` in
  `wiki/` or `zettel/` — and link it to something before you move on.

```json
{"tool": "note_search", "args": {"query": "backpressure OR \"queue depth\"", "limit": 10}}
```
```json
{"tool": "note_related", "args": {"path": "wiki/queue-depth-as-a-signal.md", "limit": 8}}
```
```json
{"tool": "note_section_edit", "args": {
  "path": "wiki/queue-depth-as-a-signal.md", "mode": "append_to_section",
  "heading": "Detail",
  "content": "Depth is a lagging signal: by the time it moves, the producer has already overshot."}}
```
```json
{"tool": "note_delete", "args": {"path": "inbox/2026-07-24-0912-queue-depth-thought.md"}}
```

The inbox is empty when it is empty. Leaving three "I'll deal with these later"
items is how it becomes forty.

---

## Meeting notes

Before the meeting, create the note from the template so the structure exists
and you are not composing headings while listening:

```json
{"tool": "note_from_template", "args": {
  "template": "meeting.md",
  "path": "projects/website/2026-07-30-launch-review.md",
  "title": "Launch review",
  "tags": ["meeting", "project/website"]}}
```

The `meeting.md` template gives `## Context`, `## Discussed`, `## Decided`,
`## Actions` with a `- [ ]` stub, plus `attendees` in the frontmatter.

During, append to sections rather than rewriting the note:

```json
{"tool": "note_section_edit", "args": {
  "path": "projects/website/2026-07-30-launch-review.md",
  "mode": "append_to_section", "heading": "Decided",
  "content": "- Ship without the import step. [[wiki/ship-the-thin-slice]]"}}
```

After, three things:

1. Fill `attendees` with `note_frontmatter` and link each person:
   `[[people/maria-lang]]`. Person notes are what make "what did we agree with
   Maria" answerable later.
2. Actions go in as `- [ ]` so `task_list` finds them.
3. Anything decided that is *general* — a principle, not a scheduling detail —
   gets distilled into `wiki/` with a link back to the meeting note. The
   meeting note is a record; the wiki note is the knowledge.

---

## Reading a paper or an article into the vault

Two notes, always. One is what the source said; one is what you took from it.

```json
{"tool": "note_search", "args": {"query": "\"differential privacy\"", "limit": 10}}
```

Check first whether the vault already has a note on this source or this idea.
If it does, you are extending, not creating.

**wiki-raw.** Store the excerpt verbatim:

```json
{"tool": "note_from_template", "args": {
  "template": "source.md",
  "path": "raw/2026-07-30-dwork-differential-privacy.md",
  "title": "Dwork, Differential Privacy (2006) - excerpts",
  "tags": ["raw", "source/paper"]}}
```
```json
{"tool": "note_frontmatter", "args": {
  "path": "raw/2026-07-30-dwork-differential-privacy.md",
  "set": {"source": "https://doi.org/...", "retrieved": "2026-07-30"}}}
```

Paste the quotations into the body with `note_section_edit`
(`mode: "append_to_note"`), and then never touch that file again.

Now the understanding note, in your own words:

```json
{"tool": "note_create", "args": {
  "path": "wiki/privacy-is-a-budget-not-a-property.md",
  "title": "Privacy is a budget, not a property",
  "tags": ["privacy", "statistics"],
  "content": "A dataset is not private or non-private; each query spends from a fixed budget...\n\nFrom [[raw/2026-07-30-dwork-differential-privacy|Dwork 2006]].\nRelated: [[wiki/anonymisation-fails-at-scale]] - why the property framing broke.\n"}}
```

**zettelkasten.** One literature note in `literature/` — your reading, with the
citation in frontmatter, quoting once and then explaining — and one or more
zettel per distinct claim, each linked to the literature note and to at least
two other zettel.

The test either way: read the distilled note a month later without opening the
source. If it does not stand alone, it was transcription, not distillation.

---

## Running a project from a project note

One note is the spine. Everything else hangs off it.

```json
{"tool": "note_from_template", "args": {
  "template": "project.md", "path": "projects/website.md",
  "title": "Website relaunch", "tags": ["project"]}}
```

`project.md` gives `## Goal`, `## Open` (with checkboxes), `## Decisions`,
`## Notes`, and `status: active` in the frontmatter.

- Open work: `- [ ]` under `## Open`, added with `note_section_edit`, ticked
  with `task_toggle`.
- Decisions: append one line each under `## Decisions`, dated. This is the part
  people are grateful for six months later, when the question is "why did we do
  it that way".
- Meeting notes, research and sub-documents live in `projects/website/` and
  link back to `projects/website.md`.

Status check at any time:

```json
{"tool": "task_list", "args": {"prefix": "projects/website", "limit": 100}}
```
```json
{"tool": "note_backlinks", "args": {"path": "projects/website.md"}}
```

When it ends:

```json
{"tool": "note_frontmatter", "args": {"path": "projects/website.md", "set": {"status": "done"}}}
```

Then distil: whatever was learned that outlives the project moves to `wiki/`
(or `resources/` in PARA) as its own note, with a link back. The project note
is then moved to `archive/` (PARA) or left in place with `status: done`.
`note_move` rewrites the links, so nothing breaks — run it with `dry_run` first
anyway.

---

## The weekly review

```json
{"tool": "daily_range", "args": {"from": "7d"}}
```

Read the week in one call. Summarise it into this week's daily note or into a
`journal/` weekly note, and pull out anything that turned out to matter.

```json
{"tool": "vault_review", "args": {"limit": 15}}
```

Five categories come back. Handle them in this order, because each is cheaper
than the next:

- **`broken_link`** — cheapest and always worth fixing. Either the target was
  renamed (fix the link with `note_edit`) or it never existed (write the note,
  or remove the link).
- **`open_task`** — cross-check against `task_list`; close what is done, delete
  what is dead.
- **`stub`** — a note with a title and almost nothing else. Finish it or delete
  it. A stub is a promise the vault is not keeping.
- **`orphan`** — no inbound links. Give it one, from the most closely related
  note `note_related` can find. An unlinked note is a note you will never
  arrive at.
- **`stale`** — untouched for a long time. Read it, and either it is still true
  (leave it, perhaps note that you checked) or it is not (update it, or archive
  it). Staleness is not a defect by itself; an evergreen note that is still
  correct should be old.

```json
{"tool": "vault_review", "args": {"only": "orphan", "limit": 20}}
```
```json
{"tool": "note_related", "args": {"path": "wiki/some-orphan.md", "limit": 5}}
```
```json
{"tool": "note_section_edit", "args": {
  "path": "wiki/the-obvious-neighbour.md", "mode": "append_to_section",
  "heading": "Related", "create": true,
  "content": "- [[some-orphan]] - the case where this does not hold."}}
```

Finish with `vault_stats` and note the numbers in the journal. Broken links and
orphans trending up over a few weeks means the capture rate has outrun the
distillation rate.

---

## The monthly cleanup

Tag hygiene, and vault-wide text corrections. Both rewrite many files at once,
so both are `dry_run` by default.

```json
{"tool": "tag_list", "args": {"limit": 200}}
```

Read the whole list. What you are looking for:

- **Singular/plural and case pairs.** `#meeting` and `#meetings`, `#Idea` and
  `#idea` (case is normalised, so this pair cannot actually occur — but
  `#idee` and `#idea` can).
- **Tags used once.** A tag on one note classifies nothing. Either it is the
  start of a scheme or it is noise.
- **Tags that duplicate a folder.** `#wiki` on notes in `wiki/`. Remove them.
- **Flat tags that want a hierarchy.** `#paper`, `#talk`, `#thread` →
  `#source/paper`, `#source/talk`, `#source/thread`.

```json
{"tool": "tag_rename", "args": {"from": "meetings", "to": "meeting", "dry_run": true}}
```
→ `{"message": "dry run: #meetings appears in 14 note(s). Set dry_run false to apply."}`

Read the affected list, then repeat with `"dry_run": false`. `tag_rename` also
merges: renaming into an existing tag is how you collapse a pair.

For text rather than tags:

```json
{"tool": "vault_replace", "args": {
  "pattern": "https://old.example.com/", "replace": "https://docs.example.com/",
  "prefix": "wiki/", "dry_run": true}}
```

`dry_run` defaults to true and returns a diff per affected note. Narrow with
`prefix` whenever you can; if it would touch more than `limit` notes the server
refuses rather than proceeding (`this would touch more than N notes. Narrow it
with prefix, or raise limit deliberately`) — that refusal is usually correct,
and raising the limit should be a deliberate act, not a reflex.

Use `regex: true` only when a literal will not do, and test the pattern with
`vault_grep` first — same syntax, no writes:

```json
{"tool": "vault_grep", "args": {"pattern": "https?://old\\.example\\.com/\\S*", "context": 1}}
```

Also monthly: `attachment_list` for files nothing references, and
`vault_stats` for the trend.

---

## Onboarding an existing Obsidian vault

The server is built for this — the fsnotify watcher means Obsidian, a git pull
and rsync editing the same directory is normal, not a hazard.

1. **Put the vault at `/data/<name>/`.** The directory name must match
   `^[a-z0-9][a-z0-9_-]{0,63}$`. Do not run `vault_create` over an existing
   vault; it would install a layout the vault does not use.
2. **Check ownership.** The distroless runtime user is UID 65532. A
   bind-mounted `/data` needs `chown -R 65532:65532`. This is the single most
   common way a first deployment fails.
3. **Let it index.** The index is reconciled against the filesystem at start.
   If anything looks wrong later, `secondbrain reindex` rebuilds it and loses
   nothing — it is a cache.
4. **Learn the shape before writing anything.**

```json
{"tool": "vault_stats", "args": {}}
```
```json
{"tool": "note_list", "args": {"limit": 100, "sort": "path"}}
```
```json
{"tool": "tag_list", "args": {"limit": 100}}
```

   Read the top-level directories and the most-used tags. The vault already has
   conventions; your job is to find them, not to impose the ones in this skill.

5. **Write `.secondbrain/instructions.md`.** An imported vault has none, so the
   `initialize` response carries nothing and every future session starts by
   guessing again. Draft the conventions you observed — directories and what
   belongs in each, the filename pattern, the frontmatter fields in use,
   whether you may reorganise — show it to the user, and have them save it (the
   file is inside `.secondbrain/` and therefore not writable through the note
   tools, by design).
6. **Run `vault_review` once and report, do not act.** An old vault will have
   hundreds of orphans and broken links. Fixing them unasked is a large,
   surprising diff in somebody's personal notes. Show the counts, ask what they
   want cleaned.
7. **Confirm `.obsidian/` is untouched.** It is a hidden directory, so it is
   unreachable through every tool. Plugins, themes and workspace layout are
   safe.
