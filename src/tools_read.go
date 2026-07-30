package main

import (
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
		Name:    "vault_list",
		Title:   "List vaults",
		NoVault: true,
		Description: "List the vaults available to you with their note count, size and last change. " +
			"Call this first if you do not know which vault the user means.",
		Handler: toolVaultList,
	})

	register(&Tool{
		Name:    "vault_create",
		Title:   "Create a vault",
		NoVault: true,
		Mutates: true,
		Description: "Create a new vault and populate it with a starting structure. " +
			"Layouts: wiki-raw (default; separates raw source material from distilled wiki notes), " +
			"zettelkasten (atomic, densely linked notes), para (organised by actionability), " +
			"empty (directories only). The chosen layout also writes an instructions file that is " +
			"sent to the client on connect.",
		Props: map[string]any{
			"name":   str("Vault name. Lowercase letters, digits, hyphen and underscore only."),
			"layout": enum("Starting structure.", AllLayouts()...),
		},
		Required: []string{"name"},
		Handler:  toolVaultCreate,
	})

	register(&Tool{
		Name:  "vault_stats",
		Title: "Vault statistics",
		Description: "Counts and health signals for a vault: notes, words, tags, links, broken links, " +
			"orphaned notes and open tasks. Useful before a cleanup, or to decide whether a search " +
			"came back empty because nothing matched or because the vault is empty.",
		Handler: toolVaultStats,
	})

	register(&Tool{
		Name:  "note_list",
		Title: "List notes",
		Description: "List notes by directory or glob, newest first by default. This is directory " +
			"browsing, not search: use note_search when you know what you are looking for.",
		Props: map[string]any{
			"prefix": str("Restrict to this directory, e.g. \"wiki/\"."),
			"glob":   str("Glob over the full path, e.g. \"projects/*/notes.md\"."),
			"sort":   enum("Ordering.", "modified", "path", "size"),
			"limit":  intp("Maximum entries. Default 100."),
			"offset": intp("Entries to skip."),
		},
		Handler: toolNoteList,
	})

	register(&Tool{
		Name:  "note_search",
		Title: "Search notes",
		Description: "Full-text search across a vault, ranked, with a snippet per hit. Supports FTS5 " +
			"syntax: bare words are ANDed, \"quoted phrases\" match exactly, prefix* works, OR and NOT " +
			"are available. Filter by tag, path prefix or modification date. This is the tool to reach " +
			"for before creating any note.",
		Props: map[string]any{
			"query":          str("What to look for. Leave empty to browse by filter alone."),
			"tags":           strList("Only notes carrying all of these tags."),
			"prefix":         str("Restrict to a directory."),
			"glob":           str("Restrict by glob over the path."),
			"modified_after": str("Only notes changed since then: YYYY-MM-DD, RFC3339, or a span such as 7d."),
			"limit":          intp("Maximum hits. Default 25, maximum 200."),
			"offset":         intp("Hits to skip, for paging."),
		},
		Handler: toolNoteSearch,
	})

	register(&Tool{
		Name:  "vault_grep",
		Title: "Regex search",
		Description: "Regular expression search over the raw text of every note, with surrounding lines. " +
			"Full-text search tokenises words, so it cannot find a URL, a code fragment, a partial " +
			"identifier or a piece of punctuation - this can. Slower than note_search; use it when " +
			"note_search is the wrong shape of tool rather than as a first resort.",
		Props: map[string]any{
			"pattern":        str("Go regular expression (RE2 syntax)."),
			"case_sensitive": boolp("Match case exactly. Default false."),
			"prefix":         str("Restrict to a directory."),
			"context":        intp("Lines of context on each side. Default 1, maximum 5."),
			"limit":          intp("Maximum matches. Default 50."),
		},
		Required: []string{"pattern"},
		Handler:  toolVaultGrep,
	})

	register(&Tool{
		Name:  "note_read",
		Title: "Read a note",
		Description: "Read a note. Returns the content, the parsed frontmatter and a content_hash that " +
			"you must pass back to note_write. Pass a heading to read only that section, which is much " +
			"cheaper than reading a long note in full.",
		Props: map[string]any{
			"path":          str("Path relative to the vault root. The .md suffix is optional."),
			"heading":       str("Read only the section under this heading, including its subsections."),
			"with_metadata": boolp("Include headings, tags, links and tasks. Default true."),
		},
		Required: []string{"path"},
		Handler:  toolNoteRead,
	})

	register(&Tool{
		Name:  "note_outline",
		Title: "Outline a note",
		Description: "The shape of a note without its text: frontmatter, heading tree, tags, link counts " +
			"and size. Read this first when a note might be long - it costs a fraction of note_read and " +
			"tells you which section you actually want.",
		Props: map[string]any{
			"path": str("Path relative to the vault root."),
		},
		Required: []string{"path"},
		Handler:  toolNoteOutline,
	})

	register(&Tool{
		Name:  "note_backlinks",
		Title: "Links in and out",
		Description: "Which notes link to this one, which notes it links to, and which of its links point " +
			"at nothing. The fastest way to understand where a note sits in the vault.",
		Props: map[string]any{
			"path": str("Path relative to the vault root."),
		},
		Required: []string{"path"},
		Handler:  toolNoteBacklinks,
	})

	register(&Tool{
		Name:  "note_related",
		Title: "Related notes",
		Description: "Notes that are probably about the same thing, scored by shared tags (weighted so " +
			"that rare tags count for more), by direct links in either direction, and by co-citation. " +
			"Use it before writing a new note to find the one that already exists.",
		Props: map[string]any{
			"path":  str("Path relative to the vault root."),
			"limit": intp("Maximum results. Default 10."),
		},
		Required: []string{"path"},
		Handler:  toolNoteRelated,
	})

	register(&Tool{
		Name:  "tag_list",
		Title: "List tags",
		Description: "Every tag in the vault with the number of notes carrying it, most used first. " +
			"Frontmatter tags and inline #tags are merged.",
		Props: map[string]any{
			"prefix": str("Only tags starting with this, e.g. \"project/\"."),
			"limit":  intp("Maximum tags. Default 200."),
		},
		Handler: toolTagList,
	})
}

// ---------------------------------------------------------------------------

func toolVaultList(c *toolCtx) (any, error) {
	vaults := c.srv.vaults.List(c.user)
	out := make([]map[string]any, 0, len(vaults))
	for _, v := range vaults {
		st, err := v.idx.Stats()
		if err != nil {
			st = &VaultStats{}
		}
		entry := map[string]any{
			"name":          v.Name,
			"notes":         st.Notes,
			"attachments":   st.Attachments,
			"words":         st.Words,
			"bytes":         st.Bytes,
			"last_modified": st.LastModified,
			"is_default":    v.Name == c.cfg.DefaultVault,
			"versioned":     v.git != nil,
		}
		if instr := v.Instructions(); instr != "" {
			entry["has_instructions"] = true
		}
		out = append(out, entry)
	}
	return map[string]any{"vaults": out, "default_vault": c.cfg.DefaultVault}, nil
}

func toolVaultCreate(c *toolCtx) (any, error) {
	name, err := c.reqString("name")
	if err != nil {
		return nil, err
	}
	layout, err := ParseLayout(c.optString("layout", string(LayoutWikiRaw)))
	if err != nil {
		return nil, err
	}
	if !c.user.CanUseVault(name) {
		return nil, fmt.Errorf("you may not create a vault named %q", name)
	}
	v, err := c.srv.vaults.Create(name, layout)
	if err != nil {
		return nil, err
	}
	if _, err := StartWatcher(v, c.srv.stop); err != nil {
		logWarn("watch_unavailable", map[string]any{"vault": v.Name, "error": err.Error()})
	}
	dirs := []string{}
	entries, _ := os.ReadDir(v.Root)
	for _, e := range entries {
		if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
			dirs = append(dirs, e.Name()+"/")
		}
	}
	return map[string]any{
		"vault":        v.Name,
		"layout":       string(layout),
		"directories":  dirs,
		"instructions": v.Instructions(),
		"message":      "Vault created. Its conventions are in " + instrFile + " and are sent to clients on connect.",
	}, nil
}

func toolVaultStats(c *toolCtx) (any, error) {
	st, err := c.vault.idx.Stats()
	if err != nil {
		return nil, err
	}
	return map[string]any{"vault": c.vault.Name, "stats": st}, nil
}

func toolNoteList(c *toolCtx) (any, error) {
	limit := clamp(c.optInt("limit", 100), 1, 500)
	offset := c.optInt("offset", 0)
	prefix := strings.Trim(c.optString("prefix", ""), "/")
	glob := c.optString("glob", "")

	var entries []FileEntry
	err := c.vault.Walk(func(rel string, d os.DirEntry) error {
		if prefix != "" && !strings.HasPrefix(rel, prefix+"/") && rel != prefix {
			return nil
		}
		if glob != "" {
			ok, err := path.Match(glob, rel)
			if err != nil {
				return fmt.Errorf("invalid glob %q: %w", glob, err)
			}
			if !ok {
				return nil
			}
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		entries = append(entries, FileEntry{
			Path: rel, Bytes: info.Size(), Modified: info.ModTime().UTC(),
		})
		return nil
	})
	if err != nil {
		return nil, err
	}

	switch c.optString("sort", "modified") {
	case "path":
		sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	case "size":
		sort.Slice(entries, func(i, j int) bool { return entries[i].Bytes > entries[j].Bytes })
	default:
		sort.Slice(entries, func(i, j int) bool { return entries[i].Modified.After(entries[j].Modified) })
	}

	total := len(entries)
	if offset > 0 && offset < len(entries) {
		entries = entries[offset:]
	} else if offset >= len(entries) {
		entries = nil
	}
	if len(entries) > limit {
		entries = entries[:limit]
	}
	return map[string]any{
		"vault": c.vault.Name, "total": total, "returned": len(entries), "entries": entries,
	}, nil
}

func toolNoteSearch(c *toolCtx) (any, error) {
	after, err := c.optTime("modified_after")
	if err != nil {
		return nil, err
	}
	q := SearchQuery{
		Text:          c.optString("query", ""),
		PathPrefix:    c.optString("prefix", ""),
		Glob:          c.optString("glob", ""),
		Tags:          c.optList("tags"),
		ModifiedAfter: after,
		Limit:         c.optInt("limit", 25),
		Offset:        c.optInt("offset", 0),
	}
	hits, total, err := c.vault.idx.Search(q)
	if err != nil {
		return nil, err
	}
	res := map[string]any{
		"vault": c.vault.Name, "total": total, "returned": len(hits), "hits": hits,
	}
	if total == 0 {
		res["hint"] = "Nothing matched. Try fewer words, drop the filters, or use vault_grep for a literal string."
	}
	return res, nil
}

func toolVaultGrep(c *toolCtx) (any, error) {
	pattern, err := c.reqString("pattern")
	if err != nil {
		return nil, err
	}
	if !c.optBool("case_sensitive", false) {
		pattern = "(?i)" + pattern
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, fmt.Errorf("invalid regular expression: %w", err)
	}
	ctxLines := clamp(c.optInt("context", 1), 0, 5)
	limit := clamp(c.optInt("limit", 50), 1, 300)
	prefix := strings.Trim(c.optString("prefix", ""), "/")

	type match struct {
		Path    string   `json:"path"`
		Line    int      `json:"line"`
		Text    string   `json:"text"`
		Context []string `json:"context,omitempty"`
	}
	var out []match
	scanned, truncated := 0, false

	err = c.vault.Walk(func(rel string, d os.DirEntry) error {
		if len(out) >= limit {
			truncated = true
			return filepath.SkipAll
		}
		if !IsNote(rel) {
			return nil
		}
		if prefix != "" && !strings.HasPrefix(rel, prefix+"/") {
			return nil
		}
		info, err := d.Info()
		if err != nil || info.Size() > defaultMaxNoteBytes {
			return nil
		}
		abs, err := c.vault.Resolve(rel)
		if err != nil {
			return nil
		}
		raw, err := os.ReadFile(abs)
		if err != nil {
			return nil
		}
		scanned++
		lines := strings.Split(string(raw), "\n")
		for i, line := range lines {
			if !re.MatchString(line) {
				continue
			}
			m := match{Path: rel, Line: i + 1, Text: strings.TrimRight(line, "\r")}
			if ctxLines > 0 {
				lo := max(0, i-ctxLines)
				hi := min(len(lines), i+ctxLines+1)
				for j := lo; j < hi; j++ {
					if j == i {
						continue
					}
					m.Context = append(m.Context, fmt.Sprintf("%d: %s", j+1, strings.TrimRight(lines[j], "\r")))
				}
			}
			out = append(out, m)
			if len(out) >= limit {
				truncated = true
				return filepath.SkipAll
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"vault": c.vault.Name, "matches": out, "notes_scanned": scanned, "truncated": truncated,
	}, nil
}

func toolNoteRead(c *toolCtx) (any, error) {
	p, err := c.reqString("path")
	if err != nil {
		return nil, err
	}
	n, err := ReadNote(c.vault, p)
	if err != nil {
		return nil, err
	}
	content := n.Body
	if h := c.optString("heading", ""); h != "" {
		content, err = SectionText(n, h)
		if err != nil {
			return nil, err
		}
	}
	res := map[string]any{
		"vault":        c.vault.Name,
		"path":         n.Path,
		"title":        n.Title,
		"content":      content,
		"content_hash": n.Hash,
		"bytes":        n.Bytes,
		"modified":     n.Modified.UTC().Format(time.RFC3339),
		"frontmatter":  n.Frontmatter(),
	}
	if c.optBool("with_metadata", true) {
		res["headings"] = n.Headings()
		res["tags"] = n.Tags()
		res["links"] = n.Links()
		if tasks := n.Tasks(); len(tasks) > 0 {
			res["tasks"] = tasks
		}
	}
	return res, nil
}

func toolNoteOutline(c *toolCtx) (any, error) {
	p, err := c.reqString("path")
	if err != nil {
		return nil, err
	}
	n, err := ReadNote(c.vault, p)
	if err != nil {
		return nil, err
	}
	links := n.Links()
	back, _ := c.vault.idx.Backlinks(n.Path)
	return map[string]any{
		"vault":        c.vault.Name,
		"path":         n.Path,
		"title":        n.Title,
		"content_hash": n.Hash,
		"bytes":        n.Bytes,
		"words":        len(strings.Fields(n.Body)),
		"modified":     n.Modified.UTC().Format(time.RFC3339),
		"frontmatter":  n.Frontmatter(),
		"headings":     n.Headings(),
		"tags":         n.Tags(),
		"outgoing":     len(links),
		"incoming":     len(back),
		"open_tasks":   countOpen(n.Tasks()),
	}, nil
}

func toolNoteBacklinks(c *toolCtx) (any, error) {
	p, err := c.reqString("path")
	if err != nil {
		return nil, err
	}
	_, rel, err := c.vault.ResolveNote(p)
	if err != nil {
		return nil, err
	}
	in, err := c.vault.idx.Backlinks(rel)
	if err != nil {
		return nil, err
	}
	out, broken, err := c.vault.idx.Outlinks(rel)
	if err != nil {
		return nil, err
	}
	res := map[string]any{
		"vault": c.vault.Name, "path": rel,
		"incoming": in, "outgoing": out,
	}
	if len(broken) > 0 {
		res["broken"] = broken
		res["hint"] = "Broken links point at notes that do not exist. Either create them or fix the link with note_edit."
	}
	return res, nil
}

func toolNoteRelated(c *toolCtx) (any, error) {
	p, err := c.reqString("path")
	if err != nil {
		return nil, err
	}
	_, rel, err := c.vault.ResolveNote(p)
	if err != nil {
		return nil, err
	}
	hits, err := c.vault.idx.Related(rel, c.optInt("limit", 10))
	if err != nil {
		return nil, err
	}
	return map[string]any{"vault": c.vault.Name, "path": rel, "related": hits}, nil
}

func toolTagList(c *toolCtx) (any, error) {
	tags, err := c.vault.idx.TagCounts(c.optString("prefix", ""))
	if err != nil {
		return nil, err
	}
	limit := clamp(c.optInt("limit", 200), 1, 1000)
	if len(tags) > limit {
		tags = tags[:limit]
	}
	return map[string]any{"vault": c.vault.Name, "tags": tags, "count": len(tags)}, nil
}

// ---------------------------------------------------------------------------

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func countOpen(tasks []Task) int {
	n := 0
	for _, t := range tasks {
		if !t.Done {
			n++
		}
	}
	return n
}
