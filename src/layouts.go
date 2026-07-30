package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ---------------------------------------------------------------------------
// Vault layouts
//
// A brand new vault is an empty directory, and an empty directory is the worst
// thing you can hand an agent: with no convention to follow it will invent
// paths, and a week later the same idea lives in three places under three
// names.
//
// So a new vault comes with a shape and, more importantly, with an
// instructions.md that describes that shape. The instructions travel to the
// client in the MCP initialize response, which means the convention arrives
// before the first tool call rather than after the first mess.
//
// None of it is enforced in code. Delete a directory and it is gone; rewrite
// instructions.md and the agent follows the new rules. The layout is an
// opinion, not a schema.
// ---------------------------------------------------------------------------

type Layout string

const (
	LayoutWikiRaw      Layout = "wiki-raw"
	LayoutZettelkasten Layout = "zettelkasten"
	LayoutPARA         Layout = "para"
	LayoutEmpty        Layout = "empty"
)

func ParseLayout(s string) (Layout, error) {
	switch Layout(strings.ToLower(strings.TrimSpace(s))) {
	case "", LayoutWikiRaw:
		return LayoutWikiRaw, nil
	case LayoutZettelkasten:
		return LayoutZettelkasten, nil
	case LayoutPARA:
		return LayoutPARA, nil
	case LayoutEmpty:
		return LayoutEmpty, nil
	default:
		return "", fmt.Errorf("unknown layout %q: use wiki-raw, zettelkasten, para or empty", s)
	}
}

func AllLayouts() []string {
	return []string{string(LayoutWikiRaw), string(LayoutZettelkasten), string(LayoutPARA), string(LayoutEmpty)}
}

type layoutSpec struct {
	dirs         []string
	instructions string
	templates    map[string]string
}

func (l Layout) spec() layoutSpec {
	switch l {
	case LayoutZettelkasten:
		return layoutSpec{
			dirs:         []string{"zettel", "literature", "fleeting", "journal", "attachments", "templates"},
			instructions: instrZettelkasten,
			templates:    map[string]string{"zettel.md": tplZettel, "literature.md": tplLiterature},
		}
	case LayoutPARA:
		return layoutSpec{
			dirs:         []string{"projects", "areas", "resources", "archive", "journal", "attachments", "templates"},
			instructions: instrPARA,
			templates:    map[string]string{"project.md": tplProject, "meeting.md": tplMeeting},
		}
	case LayoutEmpty:
		return layoutSpec{
			dirs:         []string{"attachments", "templates"},
			instructions: instrEmpty,
		}
	default:
		return layoutSpec{
			dirs:         []string{"inbox", "raw", "wiki", "journal", "projects", "people", "attachments", "templates"},
			instructions: instrWikiRaw,
			templates:    map[string]string{"note.md": tplNote, "meeting.md": tplMeeting, "source.md": tplSource},
		}
	}
}

func (l Layout) apply(root, vaultName string) error {
	s := l.spec()
	for _, d := range s.dirs {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			return err
		}
		// Git does not track empty directories, and an agent that lists a
		// vault and sees nothing concludes the vault is unused. A .gitkeep
		// costs nothing and keeps the shape visible after a clone.
		keep := filepath.Join(root, d, ".gitkeep")
		if err := os.WriteFile(keep, nil, 0o644); err != nil {
			return err
		}
	}
	for name, body := range s.templates {
		p := filepath.Join(root, "templates", name)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(filepath.Join(root, internalDir), 0o755); err != nil {
		return err
	}
	instr := strings.ReplaceAll(s.instructions, "{{vault}}", vaultName)
	return os.WriteFile(filepath.Join(root, instrFile), []byte(instr), 0o644)
}

// ---------------------------------------------------------------------------
// Instruction texts
// ---------------------------------------------------------------------------

const instrWikiRaw = `# Conventions for the "{{vault}}" vault

Layout: **wiki-raw**. The organising idea is that source material and
understanding are different kinds of thing and must not share a directory.

## Where things go

- ` + "`inbox/`" + ` - anything captured in a hurry. Unsorted by design. Filenames are
  ` + "`YYYY-MM-DD-HHMM-slug.md`" + `. Nothing stays here longer than a week.
- ` + "`raw/`" + ` - source material: transcripts, article excerpts, quotes, pasted
  documentation. **Never rewritten.** Correct a typo and you have destroyed the
  evidence. Cite it from the wiki instead.
- ` + "`wiki/`" + ` - distilled, evergreen notes in your own words. One idea per note.
  This is the part that gets edited, refined and reorganised. If a note here
  cannot be understood without opening another one, it is not finished.
- ` + "`journal/`" + ` - daily notes as ` + "`journal/YYYY/YYYY-MM-DD.md`" + `.
- ` + "`projects/`" + ` - things with an end date. When a project finishes, move what
  is worth keeping into ` + "`wiki/`" + ` and archive the rest.
- ` + "`people/`" + ` - one note per person; conversations, context, preferences.
- ` + "`attachments/`" + ` - images and PDFs. Never anything textual.

## Rules

1. Prefer ` + "`note_section_edit`" + ` and ` + "`note_edit`" + ` over ` + "`note_write`" + `.
   Rewriting a whole note to add a sentence is how paragraphs quietly vanish.
2. Every wiki note gets at least one ` + "`[[link]]`" + ` to another note. An
   unconnected note is one you will never find again.
3. Tags describe *kind*, folders describe *topic*. Do not mirror the folder
   tree in tags.
4. Before creating a note, search for it. Duplicates are the main failure mode
   of a knowledge base.
5. When you distil raw material into the wiki, link back to the raw note.
`

const instrZettelkasten = `# Conventions for the "{{vault}}" vault

Layout: **zettelkasten**. Atomic notes, densely linked, no folder hierarchy
beyond the four top-level buckets.

## Where things go

- ` + "`fleeting/`" + ` - unprocessed thoughts. Reviewed and emptied regularly.
- ` + "`literature/`" + ` - notes *about a source*, one per source, with the citation
  in the frontmatter. Your words, not the author's.
- ` + "`zettel/`" + ` - permanent notes. One idea each, stated as a claim rather than
  a topic ("Rate limits belong at the edge", not "Rate limiting").
- ` + "`journal/`" + ` - daily notes as ` + "`journal/YYYY/YYYY-MM-DD.md`" + `.

## Rules

1. A zettel that says two things is two zettels. Use ` + "`note_split`" + `.
2. Every zettel links to at least two others, and says *why* in the link line -
   a bare link carries no argument.
3. Filenames are descriptive slugs, not numbers. The index does the finding.
4. Literature notes never quote at length. Quote once, then explain.
5. Use ` + "`note_related`" + ` before writing: the connection you were about to
   make may already exist.
`

const instrPARA = `# Conventions for the "{{vault}}" vault

Layout: **PARA**, organised by actionability rather than by subject.

## Where things go

- ` + "`projects/`" + ` - a goal with a deadline. One directory per project.
- ` + "`areas/`" + ` - ongoing responsibilities with no end date.
- ` + "`resources/`" + ` - reference material, by topic.
- ` + "`archive/`" + ` - anything from the three above that is no longer active.
- ` + "`journal/`" + ` - daily notes as ` + "`journal/YYYY/YYYY-MM-DD.md`" + `.

## Rules

1. Ask "is this actionable?" before "what is this about?". The answer decides
   the directory.
2. Finished projects are moved to ` + "`archive/`" + ` with ` + "`note_move`" + `, never
   deleted - link rewriting keeps the references intact.
3. Open tasks live as ` + "`- [ ]`" + ` checkboxes inside project notes so that
   ` + "`task_list`" + ` can find them.
4. A resource note that has not been touched in a year is a candidate for the
   archive. ` + "`vault_review`" + ` will point them out.
`

const instrEmpty = `# Conventions for the "{{vault}}" vault

No layout was chosen for this vault, so there are no conventions yet.

Write them here. Everything in this file is sent to the agent when it
connects, which makes it the cheapest way to keep an assistant on the rails:
say where notes belong, how they are named, and what must never happen.

A useful starting point:

- Which directories exist and what belongs in each
- The filename convention
- Which frontmatter fields you rely on
- Whether the agent may reorganise files on its own
`

// ---------------------------------------------------------------------------
// Templates
// ---------------------------------------------------------------------------

const tplNote = `---
title: "{{title}}"
tags: []
created: {{date}}
---

# {{title}}

## Summary

## Detail

## Related
`

const tplZettel = `---
title: "{{title}}"
tags: [zettel]
created: {{date}}
---

# {{title}}

<!-- One claim, stated plainly, in your own words. -->

## Why this matters

## Connections

- [[ ]] - because ...
`

const tplLiterature = `---
title: "{{title}}"
tags: [literature]
author: ""
year: ""
url: ""
created: {{date}}
---

# {{title}}

## What it argues

## What I take from it

## Quotes worth keeping
`

const tplProject = `---
title: "{{title}}"
tags: [project]
status: active
created: {{date}}
---

# {{title}}

## Goal

## Open

- [ ] 

## Decisions

## Notes
`

const tplMeeting = `---
title: "{{title}}"
tags: [meeting]
date: {{date}}
attendees: []
created: {{date}}
---

# {{title}}

## Context

## Discussed

## Decided

## Actions

- [ ] 
`

const tplSource = `---
title: "{{title}}"
tags: [raw]
source: ""
retrieved: {{date}}
created: {{date}}
---

# {{title}}

<!-- Source material. Do not edit. Cite from the wiki instead. -->
`
