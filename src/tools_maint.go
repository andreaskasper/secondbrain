package main

import (
	"encoding/base64"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

func init() {
	register(&Tool{
		Name:  "daily_range",
		Title: "Read a range of daily notes",
		Description: "Read the journal notes for a span of days in one call. Built for weekly and " +
			"monthly reviews, where fetching seven notes individually is seven round trips and a lot " +
			"of context spent on nothing.",
		Props: map[string]any{
			"from":  str("First day, YYYY-MM-DD. Or a span such as 7d meaning the last seven days."),
			"to":    str("Last day, YYYY-MM-DD. Defaults to today."),
			"empty": boolp("Include days with no note. Default false."),
		},
		Handler: toolDailyRange,
	})

	register(&Tool{
		Name:  "vault_review",
		Title: "What needs maintenance",
		Description: "The maintenance list for a vault: stubs, orphans, broken links, notes untouched " +
			"for a long time and open tasks. A knowledge base decays quietly; this is how you see it " +
			"happening.",
		Props: map[string]any{
			"stale_after": str("How old counts as stale. Default 365d."),
			"limit":       intp("Maximum items per category. Default 15."),
			"only":        enum("Restrict to one category.", "stub", "orphan", "broken_link", "stale", "open_task"),
		},
		Handler: toolVaultReview,
	})

	register(&Tool{
		Name:    "note_merge",
		Title:   "Merge two notes",
		Mutates: true,
		Description: "Append one note's body into another, then delete the source and rewrite the links " +
			"that pointed at it. The source's title is kept as an alias on the target, so old " +
			"[[wiki links]] keep resolving. Use dry_run first.",
		Props: map[string]any{
			"path":    str("The note to keep."),
			"from":    str("The note to absorb and remove."),
			"heading": str("Heading to file the merged content under. Default: the source note's title."),
			"dry_run": boolp("Show what would happen without doing it."),
		},
		Required: []string{"path", "from"},
		Handler:  toolNoteMerge,
	})

	register(&Tool{
		Name:    "note_split",
		Title:   "Split a note",
		Mutates: true,
		Description: "Split a long note into one note per heading at a chosen level, replacing each " +
			"section in the original with a link to the new note. The cure for a note that has quietly " +
			"become a directory.",
		Props: map[string]any{
			"path":    str("The note to split."),
			"level":   intp("Heading level to split at. Default 2."),
			"dir":     str("Directory for the new notes. Default: alongside the original."),
			"dry_run": boolp("List the notes that would be created without creating them."),
		},
		Required: []string{"path"},
		Handler:  toolNoteSplit,
	})

	register(&Tool{
		Name:  "task_list",
		Title: "Open tasks",
		Description: "Every - [ ] checkbox in the vault with its file and line number. Turns a pile of " +
			"notes into a to-do list without a separate task system.",
		Props: map[string]any{
			"include_done": boolp("Include completed tasks. Default false."),
			"prefix":       str("Restrict to a directory."),
			"contains":     str("Only tasks whose text contains this."),
			"limit":        intp("Maximum tasks. Default 100."),
		},
		Handler: toolTaskList,
	})

	register(&Tool{
		Name:    "task_toggle",
		Title:   "Tick a task off",
		Mutates: true,
		Description: "Mark a checkbox done or undone. Identify it by line number from task_list, or by " +
			"its exact text if it is unique in the note.",
		Props: map[string]any{
			"path":    str("Note containing the task."),
			"line":    intp("Line number from task_list."),
			"text":    str("Exact task text, as an alternative to line."),
			"done":    boolp("Target state. Default true."),
			"dry_run": boolp("Return the diff without writing."),
		},
		Required: []string{"path"},
		Handler:  toolTaskToggle,
	})

	register(&Tool{
		Name:    "tag_rename",
		Title:   "Rename a tag",
		Mutates: true,
		Description: "Rename a tag everywhere in the vault, in frontmatter and inline, or merge it into " +
			"an existing one. Always run with dry_run first: this rewrites many files at once.",
		Props: map[string]any{
			"from":    str("Tag to rename, with or without the leading #."),
			"to":      str("New name. Use an existing tag to merge into it."),
			"dry_run": boolp("List affected notes without writing. Default true."),
		},
		Required: []string{"from", "to"},
		Handler:  toolTagRename,
	})

	register(&Tool{
		Name:    "vault_replace",
		Title:   "Vault-wide replace",
		Mutates: true,
		Description: "Find and replace a literal string or regular expression across every note. " +
			"dry_run defaults to true and returns a diff per affected note, because a vault-wide " +
			"replace is the single easiest way to damage a knowledge base.",
		Props: map[string]any{
			"pattern": str("Text to find, or a regular expression when regex is set."),
			"replace": str("Replacement. With regex, $1 refers to the first group."),
			"regex":   boolp("Treat pattern as a Go regular expression. Default false."),
			"prefix":  str("Restrict to a directory."),
			"limit":   intp("Refuse to touch more than this many notes. Default 50."),
			"dry_run": boolp("Default true. Set false to actually write."),
		},
		Required: []string{"pattern", "replace"},
		Handler:  toolVaultReplace,
	})

	register(&Tool{
		Name:  "attachment_list",
		Title: "List attachments",
		Description: "Non-Markdown files in the vault - images, PDFs, exports - with size and which " +
			"notes reference them.",
		Props: map[string]any{
			"prefix": str("Restrict to a directory. Default attachments/."),
			"limit":  intp("Maximum entries. Default 100."),
		},
		Handler: toolAttachmentList,
	})

	register(&Tool{
		Name:    "attachment_put",
		Title:   "Store an attachment",
		Mutates: true,
		Description: "Write a base64 encoded file into the vault. Only a fixed set of extensions is " +
			"accepted - a knowledge base is not a file share, and an agent that can write arbitrary " +
			"bytes into a mounted volume is a bigger problem than the convenience is worth.",
		Props: map[string]any{
			"path":      str("Destination path, e.g. \"attachments/diagram.png\"."),
			"data":      str("File contents, base64 encoded."),
			"overwrite": boolp("Replace an existing file. Default false."),
			"dry_run":   boolp("Report what would be written without writing it."),
		},
		Required: []string{"path", "data"},
		Handler:  toolAttachmentPut,
	})

	register(&Tool{
		Name:  "note_history",
		Title: "Change history",
		Description: "The commits that touched a note, newest first, with the tool that caused each " +
			"one. Available when versioning is enabled.",
		Props: map[string]any{
			"path":  str("Path relative to the vault root."),
			"limit": intp("Maximum revisions. Default 20."),
		},
		Required: []string{"path"},
		Handler:  toolNoteHistory,
	})

	register(&Tool{
		Name:  "note_diff",
		Title: "Compare revisions",
		Description: "Diff a note between two revisions, or between a revision and what is on disk now. " +
			"Use it to answer \"what did that edit actually change\".",
		Props: map[string]any{
			"path": str("Path relative to the vault root."),
			"from": str("Revision to compare from. A commit id from note_history. Default: the previous one."),
			"to":   str("Revision to compare to. Default: the working copy."),
		},
		Required: []string{"path"},
		Handler:  toolNoteDiff,
	})

	register(&Tool{
		Name:    "note_restore",
		Title:   "Restore an old version",
		Mutates: true,
		Description: "Bring a note back to how it looked at a given revision. The current version is not " +
			"lost: it becomes the previous commit, and a copy goes to the trash.",
		Props: map[string]any{
			"path":     str("Path relative to the vault root."),
			"revision": str("Commit id from note_history."),
			"dry_run":  boolp("Return the diff without writing."),
		},
		Required: []string{"path", "revision"},
		Handler:  toolNoteRestore,
	})
}

// ---------------------------------------------------------------------------

func toolDailyRange(c *toolCtx) (any, error) {
	to := time.Now()
	if s := c.optString("to", ""); s != "" {
		t, err := time.Parse("2006-01-02", s)
		if err != nil {
			return nil, fmt.Errorf("to must be YYYY-MM-DD")
		}
		to = t
	}
	from := to.AddDate(0, 0, -6)
	if s := c.optString("from", ""); s != "" {
		if d, err := parseRelative(s); err == nil {
			from = to.Add(-d)
		} else {
			t, err := time.Parse("2006-01-02", s)
			if err != nil {
				return nil, fmt.Errorf("from must be YYYY-MM-DD or a span such as 7d")
			}
			from = t
		}
	}
	if from.After(to) {
		from, to = to, from
	}
	if to.Sub(from) > 366*24*time.Hour {
		return nil, fmt.Errorf("that range is longer than a year; narrow it")
	}
	includeEmpty := c.optBool("empty", false)

	var days []map[string]any
	found := 0
	for d := from; !d.After(to); d = d.AddDate(0, 0, 1) {
		rel := DailyPath(d)
		n, err := ReadNote(c.vault, rel)
		if err != nil {
			if includeEmpty {
				days = append(days, map[string]any{"date": d.Format("2006-01-02"), "exists": false})
			}
			continue
		}
		found++
		days = append(days, map[string]any{
			"date": d.Format("2006-01-02"), "exists": true, "path": n.Path,
			"content": n.Body, "content_hash": n.Hash,
		})
	}
	return map[string]any{
		"vault": c.vault.Name, "from": from.Format("2006-01-02"), "to": to.Format("2006-01-02"),
		"days_with_notes": found, "days": days,
	}, nil
}

func toolVaultReview(c *toolCtx) (any, error) {
	stale := 365 * 24 * time.Hour
	if s := c.optString("stale_after", ""); s != "" {
		d, err := parseRelative(s)
		if err != nil {
			return nil, fmt.Errorf("stale_after must be a span such as 365d or 6m")
		}
		stale = d
	}
	items, err := c.vault.idx.Review(c.optInt("limit", 15), stale)
	if err != nil {
		return nil, err
	}
	if only := c.optString("only", ""); only != "" {
		filtered := items[:0]
		for _, it := range items {
			if it.Reason == only {
				filtered = append(filtered, it)
			}
		}
		items = filtered
	}
	grouped := map[string][]ReviewItem{}
	for _, it := range items {
		grouped[it.Reason] = append(grouped[it.Reason], it)
	}
	st, _ := c.vault.idx.Stats()
	return map[string]any{
		"vault": c.vault.Name, "stats": st, "items": grouped, "count": len(items),
	}, nil
}

func toolNoteMerge(c *toolCtx) (any, error) {
	keep, err := c.reqString("path")
	if err != nil {
		return nil, err
	}
	from, err := c.reqString("from")
	if err != nil {
		return nil, err
	}
	target, err := ReadNote(c.vault, keep)
	if err != nil {
		return nil, err
	}
	source, err := ReadNote(c.vault, from)
	if err != nil {
		return nil, err
	}
	if target.Path == source.Path {
		return nil, fmt.Errorf("a note cannot be merged into itself")
	}
	heading := c.optString("heading", source.Title)
	dryRun := c.optBool("dry_run", false)

	back, _ := c.vault.idx.Backlinks(source.Path)
	block := "## " + heading + "\n\n" +
		strings.TrimSpace(stripLeadingH1(source.Body)) + "\n\n" +
		"<!-- merged from " + source.Path + " on " + time.Now().Format("2006-01-02") + " -->\n"

	res, err := c.vault.Apply(writeOp{
		rel:    target.Path,
		dryRun: dryRun,
		reason: fmt.Sprintf("note_merge: %s into %s", source.Path, target.Path),
		transform: func(cur string, exists bool) (string, error) {
			n := ParseNote(target.Path, cur)
			aliases := map[string]bool{}
			for _, a := range n.FrontList("aliases") {
				aliases[a] = true
			}
			aliases[source.Title] = true
			aliases[strings.TrimSuffix(path.Base(source.Path), noteExt)] = true
			list := make([]string, 0, len(aliases))
			for a := range aliases {
				list = append(list, a)
			}
			sort.Strings(list)
			_ = n.SetFront("aliases", list)

			// The absorbed note's tags come along. Losing them would make the
			// merged material unfindable by the terms it was filed under.
			tags := map[string]bool{}
			for _, t := range n.FrontList("tags") {
				tags[normaliseTag(t)] = true
			}
			for _, t := range source.FrontList("tags") {
				tags[normaliseTag(t)] = true
			}
			tagList := make([]string, 0, len(tags))
			for t := range tags {
				if t != "" {
					tagList = append(tagList, t)
				}
			}
			sort.Strings(tagList)
			if len(tagList) > 0 {
				_ = n.SetFront("tags", tagList)
			}
			return SectionEdit(n, SectionAppendEnd, "", block)
		},
	})
	if err != nil {
		return nil, err
	}
	m := toMap(res)
	m["merged_from"] = source.Path
	m["backlinks_to_source"] = len(back)

	if dryRun {
		m["message"] = fmt.Sprintf("dry run: would merge %s into %s and delete the source", source.Path, target.Path)
		return m, nil
	}
	if _, err := c.vault.Delete(source.Path, "", false); err != nil {
		return nil, fmt.Errorf("merged the content but could not remove %s: %w", source.Path, err)
	}
	sub := &toolCtx{srv: c.srv, user: c.user, cfg: c.cfg, vault: c.vault, args: map[string]any{}}
	rewrites, _ := sub.planLinkRewrite(source.Path, target.Path)
	updated := 0
	for _, rw := range rewrites {
		src := rw["path"].(string)
		repl := rw["replacements"].([]replacement)
		if _, err := c.vault.Apply(writeOp{
			rel: src, skipTouch: true,
			transform: func(cur string, exists bool) (string, error) {
				if !exists {
					return cur, nil
				}
				for _, r := range repl {
					cur = strings.ReplaceAll(cur, r.From, r.To)
				}
				return cur, nil
			},
		}); err == nil {
			updated++
		}
	}
	m["links_updated"] = updated
	return m, nil
}

func stripLeadingH1(body string) string {
	lines := strings.Split(body, "\n")
	for i, l := range lines {
		if strings.TrimSpace(l) == "" {
			continue
		}
		if m := headingRe.FindStringSubmatch(strings.TrimRight(l, "\r")); m != nil && len(m[1]) == 1 {
			return strings.Join(lines[i+1:], "\n")
		}
		break
	}
	return body
}

func toolNoteSplit(c *toolCtx) (any, error) {
	p, err := c.reqString("path")
	if err != nil {
		return nil, err
	}
	n, err := ReadNote(c.vault, p)
	if err != nil {
		return nil, err
	}
	level := clamp(c.optInt("level", 2), 1, 6)
	dir := c.optString("dir", path.Dir(n.Path))
	dryRun := c.optBool("dry_run", false)

	var targets []Heading
	for _, h := range n.Headings() {
		if h.Level == level {
			targets = append(targets, h)
		}
	}
	if len(targets) == 0 {
		return nil, fmt.Errorf("%s has no level %d headings to split at - call note_outline to see its structure", n.Path, level)
	}

	type piece struct {
		Path  string `json:"path"`
		Title string `json:"title"`
		Bytes int    `json:"bytes"`
	}
	var pieces []piece
	for _, h := range targets {
		text, err := SectionText(n, h.Text)
		if err != nil {
			continue
		}
		dest := path.Join(dir, Slug(h.Text)+noteExt)
		if dest == n.Path {
			dest = path.Join(dir, Slug(h.Text)+"-1"+noteExt)
		}
		pieces = append(pieces, piece{Path: dest, Title: h.Text, Bytes: len(text)})
	}
	if dryRun {
		return map[string]any{
			"vault": c.vault.Name, "path": n.Path, "dry_run": true,
			"would_create": pieces,
			"message":      fmt.Sprintf("would create %d notes and replace each section with a link", len(pieces)),
		}, nil
	}

	created := 0
	for i, h := range targets {
		text, err := SectionText(n, h.Text)
		if err != nil {
			continue
		}
		dest := pieces[i].Path
		body := strings.TrimSpace(stripLeadingH1(text))
		if _, err := c.vault.Apply(writeOp{
			rel:    dest,
			reason: "note_split: " + dest,
			transform: func(cur string, exists bool) (string, error) {
				if exists {
					return "", fmt.Errorf("%w: %s", errExists, dest)
				}
				nn := &Note{Path: dest}
				_ = nn.SetFront("title", h.Text)
				_ = nn.SetFront("tags", n.Tags())
				_ = nn.SetFront("source_note", n.Path)
				nn.Body = "# " + h.Text + "\n\n" + body + "\n\n" +
					"Split out of [[" + strings.TrimSuffix(n.Path, noteExt) + "]].\n"
				return nn.Render(), nil
			},
		}); err != nil {
			logWarn("split_piece_failed", map[string]any{"path": dest, "error": err.Error()})
			continue
		}
		created++
	}

	// Replace each section in the original with a stub linking to its new home.
	res, err := c.vault.Apply(writeOp{
		rel:    n.Path,
		reason: "note_split: " + n.Path,
		transform: func(cur string, exists bool) (string, error) {
			cn := ParseNote(n.Path, cur)
			out := cur
			for i, h := range targets {
				stub := "See [[" + strings.TrimSuffix(pieces[i].Path, noteExt) + "|" + h.Text + "]]."
				next, err := SectionEdit(cn, SectionReplace, h.Text, stub)
				if err != nil {
					continue
				}
				cn = ParseNote(n.Path, next)
				out = next
			}
			return out, nil
		},
	})
	if err != nil {
		return nil, err
	}
	m := toMap(res)
	m["created"] = pieces
	m["notes_created"] = created
	return m, nil
}

func toolTaskList(c *toolCtx) (any, error) {
	tasks, err := c.vault.idx.Tasks(
		c.optBool("include_done", false),
		c.optString("prefix", ""),
		c.optString("contains", ""),
		c.optInt("limit", 100),
	)
	if err != nil {
		return nil, err
	}
	return map[string]any{"vault": c.vault.Name, "tasks": tasks, "count": len(tasks)}, nil
}

func toolTaskToggle(c *toolCtx) (any, error) {
	p, err := c.reqString("path")
	if err != nil {
		return nil, err
	}
	line := c.optInt("line", 0)
	text := c.optString("text", "")
	if line == 0 && text == "" {
		return nil, fmt.Errorf("pass either line or text to identify the task")
	}
	done := c.optBool("done", true)
	var changed string

	res, err := c.vault.Apply(writeOp{
		rel:    p,
		dryRun: c.optBool("dry_run", false),
		reason: "task_toggle: " + p,
		transform: func(cur string, exists bool) (string, error) {
			if !exists {
				return "", fmt.Errorf("%w: %s", errNotFound, p)
			}
			lines := strings.Split(cur, "\n")
			idx := -1
			if line > 0 {
				if line > len(lines) {
					return "", fmt.Errorf("%s has only %d lines", p, len(lines))
				}
				idx = line - 1
				if !taskRe.MatchString(strings.TrimRight(lines[idx], "\r")) {
					return "", fmt.Errorf("line %d of %s is not a checkbox - call task_list for current line numbers", line, p)
				}
			} else {
				hits := 0
				for i, l := range lines {
					m := taskRe.FindStringSubmatch(strings.TrimRight(l, "\r"))
					if m != nil && strings.TrimSpace(m[3]) == text {
						idx = i
						hits++
					}
				}
				if hits == 0 {
					return "", fmt.Errorf("no task with that exact text in %s", p)
				}
				if hits > 1 {
					return "", fmt.Errorf("%d tasks in %s have that text; use line instead", hits, p)
				}
			}
			m := taskRe.FindStringSubmatch(strings.TrimRight(lines[idx], "\r"))
			mark := " "
			if done {
				mark = "x"
			}
			lines[idx] = m[1] + "- [" + mark + "] " + m[3]
			changed = strings.TrimSpace(m[3])
			return strings.Join(lines, "\n"), nil
		},
	})
	if err != nil {
		return nil, err
	}
	mm := toMap(res)
	mm["task"] = changed
	mm["done"] = done
	return mm, nil
}

func toolTagRename(c *toolCtx) (any, error) {
	from := normaliseTag(c.optString("from", ""))
	to := normaliseTag(c.optString("to", ""))
	if from == "" || to == "" {
		return nil, fmt.Errorf("from and to are both required")
	}
	if from == to {
		return nil, fmt.Errorf("from and to are the same tag")
	}
	dryRun := c.optBool("dry_run", true)

	paths, err := c.vault.idx.NotePaths()
	if err != nil {
		return nil, err
	}
	inline := regexp.MustCompile(`(^|[^\w` + "`" + `#/])#` + regexp.QuoteMeta(from) + `\b`)

	var touched []map[string]any
	for _, rel := range paths {
		n, err := ReadNote(c.vault, rel)
		if err != nil {
			continue
		}
		has := false
		for _, t := range n.Tags() {
			if t == from {
				has = true
				break
			}
		}
		if !has {
			continue
		}
		res, err := c.vault.Apply(writeOp{
			rel:       rel,
			dryRun:    dryRun,
			skipTouch: true,
			reason:    fmt.Sprintf("tag_rename: #%s -> #%s in %s", from, to, rel),
			transform: func(cur string, exists bool) (string, error) {
				nn := ParseNote(rel, cur)
				list := nn.FrontList("tags")
				if len(list) > 0 {
					set := map[string]bool{}
					for _, t := range list {
						if normaliseTag(t) == from {
							set[to] = true
						} else {
							set[normaliseTag(t)] = true
						}
					}
					out := make([]string, 0, len(set))
					for t := range set {
						out = append(out, t)
					}
					sort.Strings(out)
					_ = nn.SetFront("tags", out)
				}
				nn.Body = inline.ReplaceAllString(nn.Body, "${1}#"+to)
				return nn.Render(), nil
			},
		})
		if err != nil {
			continue
		}
		entry := map[string]any{"path": rel}
		if dryRun && res.Diff != "" {
			entry["diff"] = res.Diff
		}
		touched = append(touched, entry)
	}
	if !dryRun && c.vault.git != nil {
		_ = c.vault.git.Commit(fmt.Sprintf("tag_rename: #%s -> #%s in %d notes", from, to, len(touched)))
	}
	msg := fmt.Sprintf("renamed #%s to #%s in %d note(s)", from, to, len(touched))
	if dryRun {
		msg = fmt.Sprintf("dry run: #%s appears in %d note(s). Set dry_run false to apply.", from, len(touched))
	}
	return map[string]any{
		"vault": c.vault.Name, "from": from, "to": to, "dry_run": dryRun,
		"notes": touched, "count": len(touched), "message": msg,
	}, nil
}

func toolVaultReplace(c *toolCtx) (any, error) {
	pattern, err := c.reqString("pattern")
	if err != nil {
		return nil, err
	}
	replace := c.rawString("replace", "")
	useRegex := c.optBool("regex", false)
	dryRun := c.optBool("dry_run", true)
	limit := clamp(c.optInt("limit", 50), 1, 500)
	prefix := strings.Trim(c.optString("prefix", ""), "/")

	var re *regexp.Regexp
	if useRegex {
		re, err = regexp.Compile(pattern)
		if err != nil {
			return nil, fmt.Errorf("invalid regular expression: %w", err)
		}
	}

	paths, err := c.vault.idx.NotePaths()
	if err != nil {
		return nil, err
	}

	// Plan the whole change before writing any of it. Discovering halfway
	// through that the limit is exceeded and returning an error would leave
	// the vault in a state nobody asked for - half replaced, and already
	// committed.
	type plan struct {
		rel  string
		cur  string
		next string
		hits int
	}
	var planned []plan
	total := 0
	for _, rel := range paths {
		if prefix != "" && !strings.HasPrefix(rel, prefix+"/") {
			continue
		}
		abs, err := c.vault.Resolve(rel)
		if err != nil {
			continue
		}
		raw, err := os.ReadFile(abs)
		if err != nil {
			continue
		}
		cur := string(raw)
		var next string
		var hits int
		if useRegex {
			hits = len(re.FindAllString(cur, -1))
			if hits == 0 {
				continue
			}
			next = re.ReplaceAllString(cur, replace)
		} else {
			hits = strings.Count(cur, pattern)
			if hits == 0 {
				continue
			}
			next = strings.ReplaceAll(cur, pattern, replace)
		}
		if next == cur {
			continue
		}
		total += hits
		planned = append(planned, plan{rel, cur, next, hits})
	}
	if len(planned) > limit {
		return nil, fmt.Errorf("this would touch %d notes and the limit is %d. Narrow it with prefix, or raise limit deliberately", len(planned), limit)
	}

	var touched []map[string]any
	for _, p := range planned {
		entry := map[string]any{"path": p.rel, "matches": p.hits}
		if dryRun {
			entry["diff"] = UnifiedDiff(p.cur, p.next, p.rel)
		} else {
			next := p.next
			res, err := c.vault.Apply(writeOp{
				rel: p.rel, skipTouch: true,
				reason:    "vault_replace: " + p.rel,
				transform: func(cur string, exists bool) (string, error) { return next, nil },
			})
			if err != nil {
				entry["error"] = err.Error()
			} else {
				entry["content_hash"] = res.HashAfter
			}
		}
		touched = append(touched, entry)
	}
	if !dryRun && c.vault.git != nil {
		_ = c.vault.git.Commit(fmt.Sprintf("vault_replace: %d notes, %d occurrences", len(touched), total))
	}
	msg := fmt.Sprintf("replaced %d occurrence(s) in %d note(s)", total, len(touched))
	if dryRun {
		msg = fmt.Sprintf("dry run: %d occurrence(s) in %d note(s). Set dry_run false to apply.", total, len(touched))
	}
	return map[string]any{
		"vault": c.vault.Name, "dry_run": dryRun, "notes": touched,
		"notes_affected": len(touched), "occurrences": total, "message": msg,
	}, nil
}

func toolAttachmentList(c *toolCtx) (any, error) {
	prefix := strings.Trim(c.optString("prefix", "attachments"), "/")
	limit := clamp(c.optInt("limit", 100), 1, 500)

	type att struct {
		Path       string `json:"path"`
		Bytes      int64  `json:"bytes"`
		Modified   string `json:"modified"`
		References int    `json:"references"`
	}
	var out []att
	err := c.vault.Walk(func(rel string, d os.DirEntry) error {
		if IsNote(rel) || len(out) >= limit {
			return nil
		}
		if prefix != "" && !strings.HasPrefix(rel, prefix+"/") {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		refs, _ := c.vault.idx.Backlinks(rel)
		out = append(out, att{
			Path: rel, Bytes: info.Size(),
			Modified: info.ModTime().UTC().Format(time.RFC3339), References: len(refs),
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return map[string]any{"vault": c.vault.Name, "attachments": out, "count": len(out)}, nil
}

func toolAttachmentPut(c *toolCtx) (any, error) {
	p, err := c.reqString("path")
	if err != nil {
		return nil, err
	}
	data, err := c.reqString("data")
	if err != nil {
		return nil, err
	}
	abs, err := c.vault.Resolve(p)
	if err != nil {
		return nil, err
	}
	ext := strings.ToLower(filepath.Ext(p))
	if !attachmentExts[ext] {
		allowed := sortedKeys(attachmentExts)
		return nil, fmt.Errorf("%q is not an accepted attachment type. Allowed: %s", ext, strings.Join(allowed, " "))
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(data))
	if err != nil {
		return nil, fmt.Errorf("data is not valid base64: %w", err)
	}
	if len(raw) > 32<<20 {
		return nil, fmt.Errorf("attachment is %d bytes; the limit is 32 MiB", len(raw))
	}
	if c.optBool("dry_run", false) {
		return map[string]any{
			"vault": c.vault.Name, "path": p, "bytes": len(raw), "dry_run": true,
			"message": "dry run: nothing was written",
		}, nil
	}
	replacing := false
	if _, err := os.Stat(abs); err == nil {
		if !c.optBool("overwrite", false) {
			return nil, fmt.Errorf("%w: %s", errExists, p)
		}
		replacing = true
	}

	c.vault.writeMu.Lock()
	defer c.vault.writeMu.Unlock()
	if replacing {
		if prev, err := os.ReadFile(abs); err == nil {
			if err := c.vault.stash(c.vault.Rel(abs), string(prev)); err != nil {
				return nil, err
			}
		}
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(abs, raw, 0o644); err != nil {
		return nil, err
	}
	rel := c.vault.Rel(abs)
	c.vault.afterWrite(rel, "attachment_put: "+rel)
	return map[string]any{
		"vault": c.vault.Name, "path": rel, "bytes": len(raw),
		"markdown": fmt.Sprintf("![%s](%s)", strings.TrimSuffix(path.Base(rel), ext), rel),
	}, nil
}

// ---------------------------------------------------------------------------
// History
// ---------------------------------------------------------------------------

func (c *toolCtx) requireGit() error {
	if c.vault.git == nil {
		return fmt.Errorf("versioning is off for this vault: start the server with SECONDBRAIN_GIT=true")
	}
	return nil
}

func toolNoteHistory(c *toolCtx) (any, error) {
	if err := c.requireGit(); err != nil {
		return nil, err
	}
	p, err := c.reqString("path")
	if err != nil {
		return nil, err
	}
	_, rel, err := c.vault.ResolveNote(p)
	if err != nil {
		return nil, err
	}
	revs, err := c.vault.git.History(rel, c.optInt("limit", 20))
	if err != nil {
		return nil, err
	}
	return map[string]any{"vault": c.vault.Name, "path": rel, "revisions": revs}, nil
}

func toolNoteDiff(c *toolCtx) (any, error) {
	if err := c.requireGit(); err != nil {
		return nil, err
	}
	p, err := c.reqString("path")
	if err != nil {
		return nil, err
	}
	_, rel, err := c.vault.ResolveNote(p)
	if err != nil {
		return nil, err
	}
	fromRev := c.optString("from", "")
	toRev := c.optString("to", "")

	if fromRev == "" {
		revs, err := c.vault.git.History(rel, 2)
		if err != nil || len(revs) == 0 {
			return nil, fmt.Errorf("no history for %s yet", rel)
		}
		if len(revs) > 1 {
			fromRev = revs[1].Commit
		} else {
			fromRev = revs[0].Commit
		}
	}
	oldText, err := c.vault.git.Contents(rel, fromRev)
	if err != nil {
		return nil, err
	}
	newText := ""
	if toRev == "" {
		abs, err := c.vault.Resolve(rel)
		if err != nil {
			return nil, err
		}
		b, err := os.ReadFile(abs)
		if err != nil {
			return nil, fmt.Errorf("%w: %s", errNotFound, rel)
		}
		newText = string(b)
		toRev = "working copy"
	} else {
		newText, err = c.vault.git.Contents(rel, toRev)
		if err != nil {
			return nil, err
		}
	}
	d := UnifiedDiff(oldText, newText, rel)
	if d == "" {
		d = "(no difference)"
	}
	return map[string]any{"vault": c.vault.Name, "path": rel, "from": fromRev, "to": toRev, "diff": d}, nil
}

func toolNoteRestore(c *toolCtx) (any, error) {
	if err := c.requireGit(); err != nil {
		return nil, err
	}
	p, err := c.reqString("path")
	if err != nil {
		return nil, err
	}
	rev, err := c.reqString("revision")
	if err != nil {
		return nil, err
	}
	_, rel, err := c.vault.ResolveNote(p)
	if err != nil {
		return nil, err
	}
	old, err := c.vault.git.Contents(rel, rev)
	if err != nil {
		return nil, err
	}
	res, err := c.vault.Apply(writeOp{
		rel:       rel,
		dryRun:    c.optBool("dry_run", false),
		skipTouch: true,
		reason:    fmt.Sprintf("note_restore: %s to %s", rel, rev),
		transform: func(cur string, exists bool) (string, error) { return old, nil },
	})
	if err != nil {
		return nil, err
	}
	m := toMap(res)
	m["restored_from"] = rev
	return m, nil
}
