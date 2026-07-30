package main

import (
	"fmt"
	"os"
	"path"
	"sort"
	"strings"
	"time"
)

func init() {
	register(&Tool{
		Name:    "note_create",
		Title:   "Create a note",
		Mutates: true,
		Description: "Create a new note. Fails if one already exists at that path unless overwrite is " +
			"set - which is the point: search first, and let the failure catch the duplicate you " +
			"were about to make. Frontmatter with title, tags and created is written for you.",
		Props: map[string]any{
			"path":      str("Path relative to the vault root, e.g. \"wiki/rate-limiting.md\"."),
			"title":     str("Title. Defaults to the filename."),
			"content":   str("Markdown body, without frontmatter."),
			"tags":      strList("Tags for the frontmatter."),
			"overwrite": boolp("Replace an existing note. Default false."),
			"dry_run":   boolp("Return the diff without writing."),
		},
		Required: []string{"path"},
		Handler:  toolNoteCreate,
	})

	register(&Tool{
		Name:    "note_write",
		Title:   "Replace a note",
		Mutates: true,
		Description: "Replace the entire body of an existing note. Requires the content_hash you got " +
			"from note_read, so an edit made by somebody else in between cannot be silently lost. " +
			"Prefer note_edit or note_section_edit whenever you are changing part of a note rather " +
			"than all of it.",
		Props: map[string]any{
			"path":             str("Path relative to the vault root."),
			"content":          str("The complete new body."),
			"content_hash":     str("The hash returned by note_read. Required."),
			"keep_frontmatter": boolp("Preserve the existing frontmatter. Default true."),
			"dry_run":          boolp("Return the diff without writing."),
		},
		Required: []string{"path", "content", "content_hash"},
		Handler:  toolNoteWrite,
	})

	register(&Tool{
		Name:    "note_edit",
		Title:   "Replace text in a note",
		Mutates: true,
		Description: "Replace an exact string in a note. old_string must match the file character for " +
			"character, including indentation, and must be unique unless replace_all is set - if it " +
			"appears more than once the edit is refused rather than guessed. This is the safest way to " +
			"change a sentence.",
		Props: map[string]any{
			"path":         str("Path relative to the vault root."),
			"old_string":   str("Text to find. Must match exactly and be unique."),
			"new_string":   str("Replacement text. Empty string deletes."),
			"replace_all":  boolp("Replace every occurrence. Default false."),
			"content_hash": str("Optional. If given, the edit is refused when the note has changed."),
			"dry_run":      boolp("Return the diff without writing."),
		},
		Required: []string{"path", "old_string", "new_string"},
		Handler:  toolNoteEdit,
	})

	register(&Tool{
		Name:    "note_section_edit",
		Title:   "Edit a section",
		Mutates: true,
		Description: "Change a note relative to one of its headings. A section runs from its heading to " +
			"the next heading of the same or higher level, subsections included. Modes: " +
			"append_to_section, prepend_to_section, replace_section, insert_before_section, " +
			"insert_after_section, delete_section, append_to_note, prepend_to_note. " +
			"This is how to add to a note without reading and rewriting it.",
		Props: map[string]any{
			"path": str("Path relative to the vault root."),
			"mode": enum("What to do.",
				string(SectionAppend), string(SectionPrepend), string(SectionReplace),
				string(SectionInsertBefore), string(SectionInsertAfter), string(SectionDelete),
				string(SectionAppendEnd), string(SectionPrependTop)),
			"heading":      str("Heading text, with or without the leading #. Required for every mode except append_to_note and prepend_to_note."),
			"content":      str("Markdown to insert. Not used by delete_section."),
			"create":       boolp("Create the note if it does not exist. Default false."),
			"content_hash": str("Optional. If given, the edit is refused when the note has changed."),
			"dry_run":      boolp("Return the diff without writing."),
		},
		Required: []string{"path", "mode"},
		Handler:  toolNoteSectionEdit,
	})

	register(&Tool{
		Name:    "note_frontmatter",
		Title:   "Edit frontmatter",
		Mutates: true,
		Description: "Change a note's metadata without touching its body: set fields, add or remove tags " +
			"and aliases. Use this rather than rewriting the note when all you want is a tag.",
		Props: map[string]any{
			"path":         str("Path relative to the vault root."),
			"set":          map[string]any{"type": "object", "description": "Fields to set, e.g. {\"status\":\"done\"}.", "additionalProperties": true},
			"unset":        strList("Field names to remove."),
			"add_tags":     strList("Tags to add."),
			"remove_tags":  strList("Tags to remove."),
			"add_aliases":  strList("Aliases to add. Aliases make [[links]] resolve to this note."),
			"content_hash": str("Optional guard against a concurrent change."),
			"dry_run":      boolp("Return the diff without writing."),
		},
		Required: []string{"path"},
		Handler:  toolNoteFrontmatter,
	})

	register(&Tool{
		Name:    "note_move",
		Title:   "Move or rename a note",
		Mutates: true,
		Description: "Move or rename a note and, by default, rewrite every link in the vault that pointed " +
			"at it. Run it with dry_run first to see which notes would be touched - a rename in a well " +
			"linked vault can reach a surprising number of files.",
		Props: map[string]any{
			"path":         str("Current path."),
			"to":           str("New path."),
			"update_links": boolp("Rewrite links elsewhere in the vault. Default true."),
			"overwrite":    boolp("Allow replacing a note at the destination. Default false."),
			"dry_run":      boolp("Report what would change without doing it."),
		},
		Required: []string{"path", "to"},
		Handler:  toolNoteMove,
	})

	register(&Tool{
		Name:    "note_delete",
		Title:   "Delete a note",
		Mutates: true,
		Description: "Move a note to the vault's trash. Nothing is erased: a timestamped copy is kept in " +
			"the internal trash directory, and with versioning on the change is a commit that can be " +
			"reverted. Notes still linking to it are reported.",
		Props: map[string]any{
			"path":         str("Path relative to the vault root."),
			"content_hash": str("Optional guard against a concurrent change."),
			"dry_run":      boolp("Report what would happen without deleting."),
		},
		Required: []string{"path"},
		Handler:  toolNoteDelete,
	})

	register(&Tool{
		Name:    "note_from_template",
		Title:   "Create from a template",
		Mutates: true,
		Description: "Create a note from a file in the vault's templates/ directory, expanding " +
			"{{title}}, {{date}}, {{time}}, {{datetime}}, {{year}}, {{month}}, {{day}} and {{slug}}. " +
			"Call it without a template name to list the templates that exist.",
		Props: map[string]any{
			"template": str("Template filename in templates/, with or without .md. Omit to list them."),
			"path":     str("Destination path. Defaults to the template's own directory convention."),
			"title":    str("Title, substituted into the template."),
			"tags":     strList("Extra tags to add on top of the template's own."),
			"dry_run":  boolp("Return the result without writing."),
		},
		Handler: toolNoteFromTemplate,
	})

	register(&Tool{
		Name:    "daily_note",
		Title:   "Today's note",
		Mutates: true,
		Description: "Open the journal note for a day, creating it if needed, and optionally append to it. " +
			"The path convention is journal/YYYY/YYYY-MM-DD.md in every layout.",
		Props: map[string]any{
			"date":    str("Day as YYYY-MM-DD. Defaults to today."),
			"append":  str("Text to append under a heading. Omit to just read the note."),
			"heading": str("Heading to append under. Created if missing. Default: the current time as a heading."),
			"dry_run": boolp("Return the diff without writing."),
		},
		Handler: toolDailyNote,
	})

	register(&Tool{
		Name:    "inbox_capture",
		Title:   "Capture a thought",
		Mutates: true,
		Description: "Write something down without deciding where it belongs. Creates a timestamped note " +
			"in inbox/ and nothing else. Use it when the user says something worth keeping in passing - " +
			"filing it properly can happen later, losing it cannot be undone.",
		Props: map[string]any{
			"text":   str("What to record. Markdown is fine."),
			"title":  str("Optional title. A slug of the first line is used otherwise."),
			"tags":   strList("Optional tags."),
			"source": str("Where this came from: a URL, a person, a conversation."),
		},
		Required: []string{"text"},
		Handler:  toolInboxCapture,
	})
}

// ---------------------------------------------------------------------------

func toolNoteCreate(c *toolCtx) (any, error) {
	p, err := c.reqString("path")
	if err != nil {
		return nil, err
	}
	overwrite := c.optBool("overwrite", false)
	title := c.optString("title", "")
	body := c.rawString("content", "")
	tags := c.optList("tags")

	res, err := c.vault.Apply(writeOp{
		rel:    p,
		dryRun: c.optBool("dry_run", false),
		reason: "note_create: " + p,
		transform: func(cur string, exists bool) (string, error) {
			if exists && !overwrite {
				return "", fmt.Errorf("%w: %s. Read it first, or set overwrite", errExists, p)
			}
			n := &Note{Path: p}
			if title == "" {
				base := path.Base(p)
				title = strings.TrimSuffix(base, path.Ext(base))
			}
			_ = n.SetFront("title", title)
			if len(tags) > 0 {
				_ = n.SetFront("tags", normaliseTags(tags))
			} else {
				_ = n.SetFront("tags", []string{})
			}
			if strings.TrimSpace(body) == "" {
				n.Body = "# " + title + "\n"
			} else {
				n.Body = strings.TrimRight(body, "\n") + "\n"
			}
			return n.Render(), nil
		},
	})
	if err != nil {
		return nil, err
	}
	return withHint(res, "Link it from somewhere. An unlinked note is one you will not find again."), nil
}

func toolNoteWrite(c *toolCtx) (any, error) {
	p, err := c.reqString("path")
	if err != nil {
		return nil, err
	}
	hash, err := c.reqString("content_hash")
	if err != nil {
		return nil, fmt.Errorf("content_hash is required: read the note first so that a change made in the meantime is not overwritten")
	}
	body := c.rawString("content", "")
	keepFront := c.optBool("keep_frontmatter", true)

	res, err := c.vault.Apply(writeOp{
		rel:      p,
		expected: hash,
		dryRun:   c.optBool("dry_run", false),
		reason:   "note_write: " + p,
		transform: func(cur string, exists bool) (string, error) {
			old := ParseNote(p, cur)
			n := &Note{Path: p, Body: strings.TrimRight(body, "\n") + "\n"}
			if keepFront {
				n.front, n.hadFront = old.front, old.hadFront
			}
			return n.Render(), nil
		},
	})
	if err != nil {
		return nil, err
	}
	return toMap(res), nil
}

func toolNoteEdit(c *toolCtx) (any, error) {
	p, err := c.reqString("path")
	if err != nil {
		return nil, err
	}
	oldStr, err := c.reqString("old_string")
	if err != nil {
		return nil, err
	}
	newStr := c.rawString("new_string", "")
	replaceAll := c.optBool("replace_all", false)
	replaced := 0

	res, err := c.vault.Apply(writeOp{
		rel:      p,
		expected: c.optString("content_hash", ""),
		dryRun:   c.optBool("dry_run", false),
		reason:   "note_edit: " + p,
		transform: func(cur string, exists bool) (string, error) {
			if !exists {
				return "", fmt.Errorf("%w: %s", errNotFound, p)
			}
			out, n, err := StringEdit(cur, oldStr, newStr, replaceAll)
			replaced = n
			return out, err
		},
	})
	if err != nil {
		return nil, err
	}
	m := toMap(res)
	m["replacements"] = replaced
	return m, nil
}

func toolNoteSectionEdit(c *toolCtx) (any, error) {
	p, err := c.reqString("path")
	if err != nil {
		return nil, err
	}
	modeStr, err := c.reqString("mode")
	if err != nil {
		return nil, err
	}
	mode, err := ParseSectionMode(modeStr)
	if err != nil {
		return nil, err
	}
	heading := c.optString("heading", "")
	if mode.needsHeading() && heading == "" {
		return nil, fmt.Errorf("heading is required for mode %s", mode)
	}
	content := c.rawString("content", "")
	if content == "" && mode != SectionDelete {
		return nil, fmt.Errorf("content is required for mode %s", mode)
	}
	create := c.optBool("create", false)

	res, err := c.vault.Apply(writeOp{
		rel:      p,
		expected: c.optString("content_hash", ""),
		dryRun:   c.optBool("dry_run", false),
		reason:   fmt.Sprintf("note_section_edit(%s): %s", mode, p),
		transform: func(cur string, exists bool) (string, error) {
			if !exists {
				if !create {
					return "", fmt.Errorf("%w: %s. Set create to make it", errNotFound, p)
				}
				base := path.Base(p)
				n := &Note{Path: p}
				_ = n.SetFront("title", strings.TrimSuffix(base, path.Ext(base)))
				_ = n.SetFront("tags", []string{})
				n.Body = "# " + strings.TrimSuffix(base, path.Ext(base)) + "\n"
				if mode.needsHeading() {
					n.Body += "\n## " + strings.TrimLeft(heading, "# ") + "\n"
				}
				cur = n.Render()
			}
			n := ParseNote(p, cur)
			return SectionEdit(n, mode, heading, content)
		},
	})
	if err != nil {
		return nil, err
	}
	return toMap(res), nil
}

func toolNoteFrontmatter(c *toolCtx) (any, error) {
	p, err := c.reqString("path")
	if err != nil {
		return nil, err
	}
	setMap, _ := c.args["set"].(map[string]any)
	unset := c.optList("unset")
	addTags := normaliseTags(c.optList("add_tags"))
	removeTags := normaliseTags(c.optList("remove_tags"))
	addAliases := c.optList("add_aliases")

	if len(setMap) == 0 && len(unset) == 0 && len(addTags) == 0 && len(removeTags) == 0 && len(addAliases) == 0 {
		return nil, fmt.Errorf("nothing to do: pass set, unset, add_tags, remove_tags or add_aliases")
	}

	var after map[string]any
	res, err := c.vault.Apply(writeOp{
		rel:      p,
		expected: c.optString("content_hash", ""),
		dryRun:   c.optBool("dry_run", false),
		reason:   "note_frontmatter: " + p,
		transform: func(cur string, exists bool) (string, error) {
			if !exists {
				return "", fmt.Errorf("%w: %s", errNotFound, p)
			}
			n := ParseNote(p, cur)
			for _, k := range sortedKeys(setMap) {
				if err := n.SetFront(k, setMap[k]); err != nil {
					return "", err
				}
			}
			for _, k := range unset {
				n.DeleteFront(k)
			}
			if len(addTags) > 0 || len(removeTags) > 0 {
				cur := map[string]bool{}
				for _, t := range n.FrontList("tags") {
					cur[normaliseTag(t)] = true
				}
				for _, t := range addTags {
					cur[t] = true
				}
				for _, t := range removeTags {
					delete(cur, t)
				}
				list := make([]string, 0, len(cur))
				for t := range cur {
					list = append(list, t)
				}
				sort.Strings(list)
				if err := n.SetFront("tags", list); err != nil {
					return "", err
				}
			}
			if len(addAliases) > 0 {
				cur := map[string]bool{}
				for _, a := range n.FrontList("aliases") {
					cur[a] = true
				}
				for _, a := range addAliases {
					cur[a] = true
				}
				list := make([]string, 0, len(cur))
				for a := range cur {
					list = append(list, a)
				}
				sort.Strings(list)
				if err := n.SetFront("aliases", list); err != nil {
					return "", err
				}
			}
			out := n.Render()
			after = ParseNote(p, out).Frontmatter()
			return out, nil
		},
	})
	if err != nil {
		return nil, err
	}
	m := toMap(res)
	m["frontmatter"] = after
	return m, nil
}

func toolNoteMove(c *toolCtx) (any, error) {
	from, err := c.reqString("path")
	if err != nil {
		return nil, err
	}
	to, err := c.reqString("to")
	if err != nil {
		return nil, err
	}
	updateLinks := c.optBool("update_links", true)
	dryRun := c.optBool("dry_run", false)

	_, fromRel, err := c.vault.ResolveNote(from)
	if err != nil {
		return nil, err
	}
	_, toRel, err := c.vault.ResolveNote(to)
	if err != nil {
		return nil, err
	}
	if fromRel == toRel {
		return nil, fmt.Errorf("the source and destination are the same")
	}

	var rewrites []map[string]any
	if updateLinks {
		rewrites, err = c.planLinkRewrite(fromRel, toRel)
		if err != nil {
			return nil, err
		}
	}
	if dryRun {
		return map[string]any{
			"vault": c.vault.Name, "from": fromRel, "to": toRel, "dry_run": true,
			"link_updates": rewrites,
			"message":      fmt.Sprintf("would move the note and update %d other note(s)", len(rewrites)),
		}, nil
	}

	c.vault.writeMu.Lock()
	_, _, err = c.vault.Move(fromRel, toRel, c.optBool("overwrite", false))
	c.vault.writeMu.Unlock()
	if err != nil {
		return nil, err
	}
	_ = c.vault.idx.UpdatePath(c.vault, fromRel)
	_ = c.vault.idx.UpdatePath(c.vault, toRel)

	applied := 0
	for _, rw := range rewrites {
		src := rw["path"].(string)
		repl := rw["replacements"].([]replacement)
		if _, err := c.vault.Apply(writeOp{
			rel:       src,
			skipTouch: true,
			transform: func(cur string, exists bool) (string, error) {
				if !exists {
					return cur, nil
				}
				for _, r := range repl {
					cur = strings.ReplaceAll(cur, r.From, r.To)
				}
				return cur, nil
			},
		}); err != nil {
			logWarn("link_rewrite_failed", map[string]any{"path": src, "error": err.Error()})
			continue
		}
		applied++
		delete(rw, "replacements")
	}
	if c.vault.git != nil {
		_ = c.vault.git.Commit(fmt.Sprintf("note_move: %s -> %s (%d links updated)", fromRel, toRel, applied))
	}
	return map[string]any{
		"vault": c.vault.Name, "from": fromRel, "to": toRel,
		"notes_updated": applied, "link_updates": rewrites,
	}, nil
}

type replacement struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// planLinkRewrite works out, per linking note, which literal strings have to
// change. Doing it as literal replacements rather than a regex over the whole
// vault keeps the change auditable: every rewrite can be shown before it
// happens.
func (c *toolCtx) planLinkRewrite(fromRel, toRel string) ([]map[string]any, error) {
	links, err := c.vault.idx.Backlinks(fromRel)
	if err != nil {
		return nil, err
	}
	fromBase := strings.TrimSuffix(path.Base(fromRel), noteExt)
	toBase := strings.TrimSuffix(path.Base(toRel), noteExt)
	toNoExt := strings.TrimSuffix(toRel, noteExt)

	bySrc := map[string][]replacement{}
	for _, l := range links {
		t := strings.TrimSpace(l.Target)
		if t == "" {
			continue
		}
		var repl replacement
		switch {
		case t == fromRel:
			repl = replacement{From: "[[" + t, To: "[[" + toRel}
		case t == strings.TrimSuffix(fromRel, noteExt):
			repl = replacement{From: "[[" + t, To: "[[" + toNoExt}
		case t == fromBase:
			// A bare name link keeps working if the basename is unchanged.
			if fromBase == toBase {
				continue
			}
			repl = replacement{From: "[[" + t, To: "[[" + toBase}
		default:
			repl = replacement{From: "(" + t, To: "(" + toRel}
		}
		bySrc[l.Path] = append(bySrc[l.Path], repl)
	}
	out := make([]map[string]any, 0, len(bySrc))
	for _, src := range sortedKeys(bySrc) {
		out = append(out, map[string]any{"path": src, "replacements": bySrc[src]})
	}
	return out, nil
}

func toolNoteDelete(c *toolCtx) (any, error) {
	p, err := c.reqString("path")
	if err != nil {
		return nil, err
	}
	_, rel, err := c.vault.ResolveNote(p)
	if err != nil {
		return nil, err
	}
	back, _ := c.vault.idx.Backlinks(rel)
	res, err := c.vault.Delete(rel, c.optString("content_hash", ""), c.optBool("dry_run", false))
	if err != nil {
		return nil, err
	}
	m := toMap(res)
	if len(back) > 0 {
		refs := make([]string, 0, len(back))
		for _, b := range back {
			refs = append(refs, b.Path)
		}
		m["still_linked_from"] = refs
		m["hint"] = "Those notes now have a broken link. Fix them, or restore this note."
	}
	return m, nil
}

func toolNoteFromTemplate(c *toolCtx) (any, error) {
	tmplName := c.optString("template", "")
	if tmplName == "" {
		return c.listTemplates()
	}
	if !strings.HasSuffix(tmplName, noteExt) {
		tmplName += noteExt
	}
	abs, _, err := c.vault.ResolveNote(path.Join("templates", tmplName))
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(abs)
	if err != nil {
		return nil, fmt.Errorf("no template named %q - call note_from_template without arguments to list them", tmplName)
	}
	title := c.optString("title", strings.TrimSuffix(tmplName, noteExt))
	dest := c.optString("path", "")
	if dest == "" {
		dest = path.Join("inbox", time.Now().Format("2006-01-02")+"-"+Slug(title)+noteExt)
	}
	body := RenderTemplate(string(raw), title, time.Now())
	extra := normaliseTags(c.optList("tags"))

	res, err := c.vault.Apply(writeOp{
		rel:    dest,
		dryRun: c.optBool("dry_run", false),
		reason: "note_from_template(" + tmplName + "): " + dest,
		transform: func(cur string, exists bool) (string, error) {
			if exists {
				return "", fmt.Errorf("%w: %s", errExists, dest)
			}
			n := ParseNote(dest, body)
			if len(extra) > 0 {
				merged := map[string]bool{}
				for _, t := range n.FrontList("tags") {
					merged[normaliseTag(t)] = true
				}
				for _, t := range extra {
					merged[t] = true
				}
				list := make([]string, 0, len(merged))
				for t := range merged {
					list = append(list, t)
				}
				sort.Strings(list)
				_ = n.SetFront("tags", list)
			}
			return n.Render(), nil
		},
	})
	if err != nil {
		return nil, err
	}
	m := toMap(res)
	m["template"] = tmplName
	return m, nil
}

func (c *toolCtx) listTemplates() (any, error) {
	dir, err := c.vault.Resolve("templates")
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return map[string]any{"vault": c.vault.Name, "templates": []string{},
			"message": "This vault has no templates/ directory."}, nil
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && IsNote(e.Name()) {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	return map[string]any{"vault": c.vault.Name, "templates": names}, nil
}

func toolDailyNote(c *toolCtx) (any, error) {
	day := time.Now()
	if s := c.optString("date", ""); s != "" {
		d, err := time.Parse("2006-01-02", s)
		if err != nil {
			return nil, fmt.Errorf("date must be YYYY-MM-DD")
		}
		day = d
	}
	rel := DailyPath(day)
	appendText := c.rawString("append", "")

	if appendText == "" {
		n, err := ReadNote(c.vault, rel)
		if err == nil {
			return map[string]any{
				"vault": c.vault.Name, "path": n.Path, "date": day.Format("2006-01-02"),
				"content": n.Body, "content_hash": n.Hash, "existed": true,
			}, nil
		}
		appendText = ""
	}

	heading := c.optString("heading", "")
	res, err := c.vault.Apply(writeOp{
		rel:    rel,
		dryRun: c.optBool("dry_run", false),
		reason: "daily_note: " + rel,
		transform: func(cur string, exists bool) (string, error) {
			if !exists {
				n := &Note{Path: rel}
				_ = n.SetFront("title", day.Format("2006-01-02"))
				_ = n.SetFront("tags", []string{"journal"})
				_ = n.SetFront("date", day.Format("2006-01-02"))
				n.Body = "# " + day.Format("Monday, 2 January 2006") + "\n"
				cur = n.Render()
			}
			if appendText == "" {
				return cur, nil
			}
			n := ParseNote(rel, cur)
			h := heading
			if h == "" {
				h = time.Now().Format("15:04")
			}
			block := "## " + h + "\n\n" + strings.TrimRight(appendText, "\n")
			if found, _, _ := findSection(n, h); found >= 0 {
				return SectionEdit(n, SectionAppend, h, appendText)
			}
			return SectionEdit(n, SectionAppendEnd, "", block)
		},
	})
	if err != nil {
		return nil, err
	}
	m := toMap(res)
	m["date"] = day.Format("2006-01-02")
	return m, nil
}

func toolInboxCapture(c *toolCtx) (any, error) {
	text, err := c.reqString("text")
	if err != nil {
		return nil, err
	}
	now := time.Now()
	title := c.optString("title", "")
	if title == "" {
		first := strings.TrimSpace(strings.SplitN(strings.TrimSpace(text), "\n", 2)[0])
		first = strings.TrimLeft(first, "#-*> ")
		if len(first) > 60 {
			first = first[:60]
		}
		title = first
	}
	rel := path.Join("inbox", now.Format("2006-01-02-1504")+"-"+Slug(title)+noteExt)
	tags := normaliseTags(append(c.optList("tags"), "inbox"))
	source := c.optString("source", "")

	res, err := c.vault.Apply(writeOp{
		rel:    rel,
		reason: "inbox_capture: " + rel,
		transform: func(cur string, exists bool) (string, error) {
			n := &Note{Path: rel}
			_ = n.SetFront("title", title)
			_ = n.SetFront("tags", tags)
			_ = n.SetFront("captured", now.UTC().Format(time.RFC3339))
			if source != "" {
				_ = n.SetFront("source", source)
			}
			n.Body = strings.TrimRight(text, "\n") + "\n"
			return n.Render(), nil
		},
	})
	if err != nil {
		return nil, err
	}
	m := toMap(res)
	m["hint"] = "Captured. Nothing was filed - process inbox/ later and move what is worth keeping."
	return m, nil
}

// ---------------------------------------------------------------------------

func normaliseTags(in []string) []string {
	set := map[string]bool{}
	for _, t := range in {
		if n := normaliseTag(t); n != "" {
			set[n] = true
		}
	}
	out := make([]string, 0, len(set))
	for t := range set {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

func toMap(r *WriteResult) map[string]any {
	m := map[string]any{
		"path": r.Path, "vault": r.Vault, "bytes": r.Bytes,
	}
	if r.HashAfter != "" {
		m["content_hash"] = r.HashAfter
	}
	if r.Created {
		m["created"] = true
	}
	if r.Deleted {
		m["deleted"] = true
	}
	if r.DryRun {
		m["dry_run"] = true
	}
	if r.Diff != "" {
		m["diff"] = r.Diff
	}
	if r.Message != "" {
		m["message"] = r.Message
	}
	return m
}

func withHint(r *WriteResult, hint string) map[string]any {
	m := toMap(r)
	if !r.DryRun {
		m["hint"] = hint
	}
	return m
}
