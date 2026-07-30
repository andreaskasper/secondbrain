# Best practices

None of this is about the software. A second brain is a habit with a file
format; the tools only make some habits cheaper than others. What follows is
what makes one worth having, and then the specific ways an agent ruins one.

## Atomic notes

One note, one idea. The test is whether the title can be a claim: "Rate limits
belong at the edge" is a note; "Rate limiting" is a directory that has not
admitted it yet.

Atomicity is not about length — a good note can be four sentences or forty. It
is about whether the note has a single point that could be agreed with or
disagreed with. A note about three things cannot be linked precisely, because a
link to it does not say which of the three you meant. That is the real cost:
non-atomic notes degrade every link that points at them.

When a note stops being atomic, split it:

```json
{"tool": "note_outline", "args": {"path": "wiki/caching.md"}}
```
```json
{"tool": "note_split", "args": {"path": "wiki/caching.md", "level": 2, "dry_run": true}}
```

The signal is in the outline: several level-2 headings that each read like a
title, and a word count over roughly a thousand.

## Write in your own words

Pasting is not capture, it is deferral. The paste has all the information and
none of the understanding, and understanding is the entire product. A vault
full of pasted text is a slower, worse search engine over material that is
still on the internet.

The rule that makes this concrete: **you may only write a note about something
you could explain out loud.** If you cannot, you do not have a note yet — you
have a source, and it goes in `raw/` (or `literature/`) where it is honestly
labelled as somebody else's words.

This applies with particular force to agent-generated notes. It is trivial to
produce four paragraphs restating a source. It is worth nothing.

## Every note earns at least one link

A note with no inbound link is reachable only by search, and search only works
when you already know the word. The whole value of a knowledge base over a
folder of files is arriving at something you were not looking for.

Make the link carry an argument. `- [[queue-depth-as-a-signal]]` says nothing;
`- [[queue-depth-as-a-signal]] - this is how the contract is measured` says why
someone following the link should care. A bare "see also" list is a link that
has already forgotten its own reason.

```json
{"tool": "note_related", "args": {"path": "wiki/new-note.md", "limit": 8}}
```

`note_related` scores by shared tags weighted so rare tags count for more, by
direct links in either direction, and by co-citation. It is the cheapest way to
find where a new note belongs.

## Reference material and thinking are different things

Reference material is what someone else said, and it is valuable because it is
accurate. Thinking is what you concluded, and it is valuable because it is
yours. They have opposite maintenance rules: reference is never edited,
thinking is edited constantly.

Storing them together means you can do neither safely. This is why `wiki-raw`
splits `raw/` from `wiki/`, why zettelkasten separates `literature/` from
`zettel/`, and why the split survives in PARA only if you enforce it with tags.
Everything in `references/vault-layouts.md` about the raw/wiki distinction is
this principle applied.

The practical marker: if you would be uncomfortable rewriting a note's
sentences freely, it is reference material and is in the wrong directory if it
sits with your thinking.

## Progressive summarisation

Notes are not written once. A useful note is read several times, and each pass
should leave it more usable than the last:

1. **Captured.** Raw, whole, unstructured.
2. **Marked.** The passages that matter are bolded or quoted; the rest stays.
3. **Summarised.** A `## Summary` section at the top, in your own words, that
   someone could read instead of the note.
4. **Distilled.** The summary has become its own atomic note, linked back to
   the source, and it is the note you actually use.

Do this lazily — only on notes you return to. Summarising something you never
reread is work done for nobody. The `note.md` template's `## Summary` /
`## Detail` / `## Related` shape is stage 3 made into a habit.

`note_section_edit` with `mode: "prepend_to_note"` or `replace_section` on
`## Summary` is how each pass is applied without touching the rest.

## An unlinked note is a lost note

Worth stating alone because it is the failure that produces no symptom. Nothing
errors. The note is still on disk, still indexed, still perfectly good. It just
never comes up again, and the effort that went into it is gone.

```json
{"tool": "vault_review", "args": {"only": "orphan", "limit": 25}}
```

Check this weekly. Orphans trending upward means notes are being produced
faster than they are being connected, which means the vault is growing and
getting less useful at the same time.

## Few folders, many tags

Folders are exclusive: a note is in exactly one. Every folder you add is a
decision you have to make correctly on the way in, and a note filed wrongly is
invisible. Tags are inclusive and cheap; a note can carry five.

So: a shallow folder tree that reflects a small number of stable distinctions
(source vs understanding, active vs archived), and tags for everything else.
Two levels of folders is usually enough. If you are three levels deep you are
encoding in the path something a tag should carry.

The complementary rule from `references/conventions.md`: **tags describe kind,
folders describe topic**, and no tag should be derivable from the path.

## The maintenance habit

A knowledge base decays quietly. Links break when notes move, tags fragment,
stubs accumulate, and the inbox fills. None of it produces an error, so none of
it forces attention.

- **Weekly**, ten minutes: `daily_range` for the week, `vault_review`,
  `task_list`, empty `inbox/`.
- **Monthly**, half an hour: `tag_list` end to end, `tag_rename` for the
  duplicates, `attachment_list` for unreferenced files, `vault_stats` for the
  trend.
- **Whenever a number moves the wrong way**: rising broken links means renames
  are being done without `update_links`; rising orphans means capture has
  outrun distillation; a tag list growing faster than the note count means tag
  inflation.

Ten minutes weekly is the whole habit. A vault that has not been reviewed in
six months is not a knowledge base, it is an archive.

---

## How agent-written knowledge bases fail specifically

These four are not the failures a human produces. They come from an agent
writing at machine speed with no memory of last week, and each has a tool that
catches it.

### Verbose restatement

The most common one. Asked to add a note, a model produces six well-organised
paragraphs that restate the input in more words. It reads like knowledge. It
contains none, because nothing was decided, compressed or connected.

The discipline: a note should be **shorter** than the material it came from,
and should contain at least one sentence that was not in the input — a claim, a
consequence, a disagreement. If you cannot write that sentence, write the raw
note and stop; do not pad `wiki/` with a paraphrase.

**Catches it:** `vault_review` with `only: "stub"` finds notes with a title and
no substance, but the real check is `note_outline` on what you just wrote. If
the word count exceeds the source and there is no `## Summary` worth reading,
delete it.

### Near-duplicate notes

The second most common, and the most damaging. The agent has no memory of the
note it wrote three sessions ago, searches for "backpressure", does not find
"Queue depth as a signal" because the vocabulary differs, and writes a second
note covering two thirds of the same ground. Now both are incomplete, neither
is authoritative, and a future search returns both.

The discipline: search before every single write, with more than one phrasing,
and then run `note_related` on the nearest hit. `note_create` failing with
`a note already exists at that path` is a feature — do not route around it with
`overwrite`.

**Catches it:** `note_search` and `note_related` before the fact; `note_merge`
after, which appends one note into another, rewrites the links and keeps the
source's title as an alias so old `[[links]]` still resolve.

```json
{"tool": "note_merge", "args": {
  "path": "wiki/queue-depth-as-a-signal.md",
  "from": "wiki/measuring-queue-pressure.md", "dry_run": true}}
```

### Orphan sprawl

An agent creates notes readily and links them rarely, because linking requires
knowing what else exists and creating does not. The result is a vault where the
note count rises steadily and the link count does not.

The discipline: no note is finished until it has one inbound link from an
existing note. Not an outbound link — outbound links are easy and do not help
you find the new note. Add the inbound one with `note_section_edit` on the
neighbour.

**Catches it:** `vault_review` with `only: "orphan"`, and `vault_stats`, where
links-per-note flat or falling over weeks is the trend that matters.

### Tag inflation

Every note gets a plausible new tag. Six months later there are four hundred
tags, most used once, several meaning the same thing, and the tag system
classifies nothing because no two notes share a vocabulary.

The discipline: **call `tag_list` before adding a tag** and reuse an existing
one. A new tag should be a deliberate act with at least three notes in mind. If
a tag would be used once, it is a word in the note body, not a tag.

**Catches it:** `tag_list` before the fact; `tag_rename` (with `dry_run`
defaulting to true) after, to merge the duplicates back together.

```json
{"tool": "tag_list", "args": {"limit": 200}}
```
```json
{"tool": "tag_rename", "args": {"from": "backpressure", "to": "reliability", "dry_run": true}}
```

---

## The single sentence version

Write less than you read, connect everything you write, edit your understanding
freely and your sources never, and look at the review list once a week.
