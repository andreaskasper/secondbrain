# Vault layouts

A brand new vault is an empty directory, and an empty directory is the worst
thing you can hand an agent: with no convention to follow it invents paths, and
a week later the same idea lives in three places under three names. So
`vault_create` installs a shape and, more importantly, an
`.secondbrain/instructions.md` describing that shape. The instructions travel
to the client in the MCP `initialize` response, which means the convention
arrives before the first tool call rather than after the first mess.

None of it is enforced in code. Delete a directory and it is gone; rewrite
`instructions.md` and the agent follows the new rules. A layout is an opinion,
not a schema.

```json
{"tool": "vault_create", "args": {"name": "notes", "layout": "wiki-raw"}}
```

Vault names must match `^[a-z0-9][a-z0-9_-]{0,63}$`. Every layout also creates
`attachments/` and `templates/`, puts a `.gitkeep` in each directory so the
shape survives a clone, and uses the same journal convention:
`journal/YYYY/YYYY-MM-DD.md`.

---

## wiki-raw (the default)

```
inbox/  raw/  wiki/  journal/  projects/  people/  attachments/  templates/
```

Templates installed: `note.md`, `meeting.md`, `source.md`.

### The organising idea

Source material and understanding are different kinds of thing and must not
share a directory.

- **`raw/` is evidence.** Transcripts, article excerpts, quotes, pasted
  documentation, the email someone sent you. It is a record of what a source
  actually said. **It is never rewritten.** Correcting a typo in a quote
  destroys the evidence; summarising a transcript in place destroys the
  transcript. If a raw note is wrong, that is a fact about the source, and the
  fact is the point. The `source.md` template says so in a comment at the top
  of every raw note it creates.
- **`wiki/` is understanding.** Distilled, evergreen notes in your own words.
  One idea per note. This is the part that is edited, refined, split, merged
  and reorganised constantly, because your understanding changes and the
  source's words do not. A wiki note that cannot be understood without opening
  another one is not finished.

The link between them runs one way in practice: a wiki note cites the raw note
it came from. That gives you provenance without contaminating either side. You
can always answer "where did I get this?" and you can always answer "what do I
now think?", and the two answers are stored separately so that revising one
never silently revises the other.

### Why the split earns its keep

Consider the alternative: one `notes/` directory holding both the transcript
and your reading of it. Three months later you edit the note to sharpen your
argument. You tighten a sentence that turns out to have been a quotation. Now
your knowledge base contains a misattribution, and nothing in the file records
that it was ever different. There is no error, no conflict, no test that fails.
That is the exact failure mode this entire program is built around, and the
raw/wiki split is the cheapest defence against it: an edit to `raw/` is
obviously wrong, so it does not happen.

The secondary benefit is that distillation becomes a visible act. Material sits
in `raw/` until somebody has understood it well enough to write something in
`wiki/`. Unprocessed sources are countable — `note_list` with
`prefix: "raw/"` against backlinks tells you which sources nobody has read yet.

### What breaks when it is ignored

- Quotes drift. Paraphrase and quotation blur until you cannot cite anything.
- Notes become append-only logs: a transcript with commentary interleaved, too
  long to read and impossible to distil, because distilling now means deleting
  someone else's words.
- `note_search` starts returning the source's vocabulary instead of yours, so
  searches for what you think surface what someone else said.
- The vault stops being rewritable. If every note might contain evidence, no
  note is safe to edit, and a knowledge base you cannot edit stops growing.

### The other directories

- **`inbox/`** — anything captured in a hurry, unsorted by design.
  `inbox_capture` writes `inbox/YYYY-MM-DD-HHMM-slug.md` with a `captured`
  timestamp and an `inbox` tag. Nothing should stay here longer than a week;
  processing the inbox means moving each item to `raw/`, `wiki/`, `projects/`
  or the trash.
- **`journal/`** — daily notes, `daily_note` and `daily_range`.
- **`projects/`** — things with an end date. Open tasks live here as `- [ ]`
  checkboxes so `task_list` can find them. When a project finishes, move what
  is worth keeping into `wiki/` and archive the rest.
- **`people/`** — one note per person: conversations, context, preferences.
  Also the natural anchor for `[[Name]]` links from meeting notes.
- **`attachments/`** — images and PDFs. Never anything textual; a `.txt` in
  `attachments/` is a note that FTS will index but nobody will find.

### Choose wiki-raw when

The user reads things — papers, articles, documentation, meeting transcripts —
and wants their own understanding to be separable from the source. This is the
right default for most people and the reason it is the default here.

---

## zettelkasten

```
fleeting/  literature/  zettel/  journal/  attachments/  templates/
```

Templates installed: `zettel.md`, `literature.md`.

- **`fleeting/`** — unprocessed thoughts. Reviewed and emptied regularly; the
  equivalent of `inbox/`.
- **`literature/`** — one note *about* a source, with the citation in the
  frontmatter (`author`, `year`, `url`). Your words, not the author's. Quote
  once, then explain. This is a deliberate contrast with wiki-raw's `raw/`:
  zettelkasten does not keep the source text at all, only your reading of it.
- **`zettel/`** — permanent notes. One idea each, stated as a claim rather than
  a topic: "Rate limits belong at the edge", not "Rate limiting". The title is
  the argument.
- Filenames are descriptive slugs, not Luhmann numbers. The index does the
  finding; folgezettel numbering exists to solve a problem paper had.

The rules that matter: a zettel that says two things is two zettels — use
`note_split`. Every zettel links to at least two others and says *why* in the
link line, because a bare link carries no argument. Run `note_related` before
writing; the connection you were about to make may already exist.

What breaks when it is ignored: zettel notes grow into topic pages, links
degenerate into an undifferentiated "see also" list, and the network stops
producing surprise — which is the only thing a zettelkasten is for.

**Choose it when** the user's output is writing and argument, and they want
notes that combine into drafts. It costs more per note than wiki-raw and pays
back only if the linking discipline is kept.

---

## para

```
projects/  areas/  resources/  archive/  journal/  attachments/  templates/
```

Templates installed: `project.md`, `meeting.md`.

Organised by actionability rather than by subject. Ask "is this actionable?"
before "what is this about?"; the answer picks the directory.

- **`projects/`** — a goal with a deadline. One directory per project.
- **`areas/`** — ongoing responsibilities with no end date: health, a team, a
  system you maintain.
- **`resources/`** — reference material, by topic.
- **`archive/`** — anything from the three above that is no longer active.

Finished projects are moved with `note_move`, never deleted; link rewriting
keeps every reference intact. Open tasks live as `- [ ]` inside project notes.
A resource note untouched for a year is an archive candidate, and
`vault_review` with `only: "stale"` will point them out.

What breaks when it is ignored: `archive/` stays empty, `projects/` fills with
finished work, and the distinction that makes PARA useful — a small set of
things you are actually doing — disappears. PARA is a filing system, not a
thinking system: it will keep the vault tidy and will not, on its own, make
connections. Pair it with disciplined linking or accept that it is an
organiser.

**Choose it when** the user's notes are mostly in service of work with
deadlines, and the main question they ask their vault is "what is going on with
X" rather than "what do I think about X".

---

## empty

```
attachments/  templates/
```

No conventions, and `instructions.md` says so explicitly along with a prompt to
write them: which directories exist and what belongs in each, the filename
convention, which frontmatter fields the user relies on, and whether the agent
may reorganise files on its own.

**Choose it when** an existing vault is being imported and already has a shape,
or when the user has a strong opinion. Do not choose it to avoid a decision: an
empty vault with no instructions is the case the layout system exists to
prevent, and the first thing you should do in one is help write
`instructions.md`.

---

## Migrating between layouts

Nothing in the server ties a vault to its layout — the layout only ran once, at
creation. Migration is therefore just moving notes and rewriting the
instructions. It is safe if done with `note_move`, which rewrites links, and
dangerous if done any other way.

General procedure:

1. `vault_stats` and `note_list` to see what is actually there.
2. Create the new directories implicitly by moving the first note into them.
3. `note_move` one note at a time, or in small batches, always with
   `dry_run: true` first. Never `vault_replace` on paths.
4. Rewrite `.secondbrain/instructions.md` to describe the new shape (it is
   inside `.secondbrain/` and therefore not reachable through the note tools —
   the user edits it on disk, or you tell them exactly what to put in it).
5. `vault_review` with `only: "broken_link"` afterwards.

Specific paths:

- **wiki-raw → zettelkasten.** `wiki/` → `zettel/`, `raw/` → `literature/`
  *only if* you first replace each raw note's body with a summary in the user's
  own words — otherwise you have imported source text into a directory whose
  rule is "your words, not the author's". If that rewriting is not going to
  happen, keep `raw/` as an extra directory and say so in the instructions.
  `inbox/` → `fleeting/`.
- **zettelkasten → wiki-raw.** `zettel/` → `wiki/`, `literature/` → `wiki/`
  too (literature notes are already distilled), `fleeting/` → `inbox/`. `raw/`
  starts empty and that is correct.
- **wiki-raw → para.** `projects/` maps directly. `wiki/` splits between
  `areas/` and `resources/` and there is no mechanical rule for it: sort by
  whether the user has an ongoing responsibility for the subject.
  `raw/` → `resources/`, and you lose the never-rewrite guarantee, so keep a
  `raw` tag on those notes and say in the instructions that `#raw` notes are
  not to be edited.
- **para → anything.** `archive/` is the easy part: leave it alone. Migrating
  the active third is the work.
- **anything → empty.** There is nothing to do but rewrite the instructions.

A migration that stalls halfway is worse than either layout, because now
nothing describes where things go. If it cannot be finished in one sitting,
write the target shape into `instructions.md` first and note which directories
are still being emptied.
