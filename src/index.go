package main

import (
	"database/sql"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

// ---------------------------------------------------------------------------
// The index
//
// SQLite with FTS5, kept in <vault>/.secondbrain/index.db, and entirely
// disposable. The files are the knowledge; this is a lookup table over them.
// Delete it and the next start rebuilds it - which is exactly the property you
// want from a cache that sits next to something irreplaceable.
//
// It also means an external editor is a first class citizen rather than a
// hazard: Obsidian, a git pull or rsync change the files, the watcher notices,
// and the index follows. Nothing in here is authoritative.
// ---------------------------------------------------------------------------

const schemaVersion = "1"

type Index struct {
	db *sql.DB
	mu sync.Mutex
}

func OpenIndex(dbPath string) (*Index, error) {
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", dbPath+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	idx := &Index{db: db}
	if err := idx.migrate(); err != nil {
		db.Close()
		// A corrupt index is not a reason to refuse service. Throw it away
		// and start again; the notes are untouched either way.
		if rmErr := os.Remove(dbPath); rmErr == nil {
			return OpenIndexFresh(dbPath)
		}
		return nil, err
	}
	return idx, nil
}

func OpenIndexFresh(dbPath string) (*Index, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	idx := &Index{db: db}
	if err := idx.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return idx, nil
}

func (i *Index) Close() { _ = i.db.Close() }

func (i *Index) migrate() error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS meta (key TEXT PRIMARY KEY, value TEXT)`,
		`CREATE TABLE IF NOT EXISTS notes (
			path     TEXT PRIMARY KEY,
			title    TEXT NOT NULL DEFAULT '',
			mtime    INTEGER NOT NULL DEFAULT 0,
			size     INTEGER NOT NULL DEFAULT 0,
			hash     TEXT NOT NULL DEFAULT '',
			created  TEXT NOT NULL DEFAULT '',
			updated  TEXT NOT NULL DEFAULT '',
			is_note  INTEGER NOT NULL DEFAULT 1,
			words    INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE VIRTUAL TABLE IF NOT EXISTS notes_fts USING fts5(
			path UNINDEXED, title, body,
			tokenize = "unicode61 remove_diacritics 2"
		)`,
		`CREATE TABLE IF NOT EXISTS tags (path TEXT NOT NULL, tag TEXT NOT NULL)`,
		`CREATE INDEX IF NOT EXISTS tags_tag ON tags(tag)`,
		`CREATE INDEX IF NOT EXISTS tags_path ON tags(path)`,
		`CREATE TABLE IF NOT EXISTS links (
			src TEXT NOT NULL, target TEXT NOT NULL, anchor TEXT NOT NULL DEFAULT '',
			alias TEXT NOT NULL DEFAULT '', wiki INTEGER NOT NULL DEFAULT 1,
			resolved TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE INDEX IF NOT EXISTS links_src ON links(src)`,
		`CREATE INDEX IF NOT EXISTS links_resolved ON links(resolved)`,
		`CREATE TABLE IF NOT EXISTS headings (
			path TEXT NOT NULL, level INTEGER NOT NULL, text TEXT NOT NULL, line INTEGER NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS headings_path ON headings(path)`,
		`CREATE TABLE IF NOT EXISTS aliases (path TEXT NOT NULL, alias TEXT NOT NULL)`,
		`CREATE INDEX IF NOT EXISTS aliases_alias ON aliases(alias)`,
		`CREATE TABLE IF NOT EXISTS tasks (
			path TEXT NOT NULL, line INTEGER NOT NULL, text TEXT NOT NULL, done INTEGER NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS tasks_path ON tasks(path)`,
		// Reserved for the semantic search on the roadmap. Declaring it now
		// means turning that feature on later is a migration of data, not of
		// schema, and existing indexes stay readable.
		`CREATE TABLE IF NOT EXISTS embeddings (
			path TEXT NOT NULL, chunk INTEGER NOT NULL, model TEXT NOT NULL,
			heading TEXT NOT NULL DEFAULT '', vector BLOB NOT NULL,
			PRIMARY KEY (path, chunk, model)
		)`,
	}
	for _, s := range stmts {
		if _, err := i.db.Exec(s); err != nil {
			return fmt.Errorf("schema: %w", err)
		}
	}
	var v string
	_ = i.db.QueryRow(`SELECT value FROM meta WHERE key='schema'`).Scan(&v)
	if v != schemaVersion {
		if _, err := i.db.Exec(`INSERT INTO meta(key,value) VALUES('schema',?)
			ON CONFLICT(key) DO UPDATE SET value=excluded.value`, schemaVersion); err != nil {
			return err
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Ingest
// ---------------------------------------------------------------------------

// Reconcile brings the index in line with the vault. It compares size and
// modification time first, so a restart over an unchanged vault is cheap.
func (i *Index) Reconcile(v *Vault) error {
	i.mu.Lock()
	defer i.mu.Unlock()

	known := map[string]struct {
		mtime int64
		size  int64
	}{}
	rows, err := i.db.Query(`SELECT path, mtime, size FROM notes`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var p string
		var m, s int64
		if err := rows.Scan(&p, &m, &s); err != nil {
			rows.Close()
			return err
		}
		known[p] = struct {
			mtime int64
			size  int64
		}{m, s}
	}
	rows.Close()

	seen := map[string]bool{}
	changed := 0
	err = v.Walk(func(rel string, d os.DirEntry) error {
		info, err := d.Info()
		if err != nil {
			return nil
		}
		seen[rel] = true
		if k, ok := known[rel]; ok && k.mtime == info.ModTime().UnixNano() && k.size == info.Size() {
			return nil
		}
		if err := i.ingestLocked(v, rel, info.ModTime(), info.Size()); err != nil {
			logWarn("index_ingest_failed", map[string]any{"path": rel, "error": err.Error()})
			return nil
		}
		changed++
		return nil
	})
	if err != nil {
		return err
	}
	removed := 0
	for p := range known {
		if !seen[p] {
			if err := i.removeLocked(p); err == nil {
				removed++
			}
		}
	}
	if changed > 0 || removed > 0 {
		if err := i.resolveLinksLocked(); err != nil {
			return err
		}
	}
	logInfo("index_reconciled", map[string]any{
		"vault": v.Name, "files": len(seen), "updated": changed, "removed": removed,
	})
	return nil
}

// UpdatePath reindexes one file, or drops it if it is gone.
func (i *Index) UpdatePath(v *Vault, rel string) error {
	i.mu.Lock()
	defer i.mu.Unlock()
	abs, err := v.Resolve(rel)
	if err != nil {
		return err
	}
	st, err := os.Stat(abs)
	if err != nil {
		if err := i.removeLocked(rel); err != nil {
			return err
		}
		return i.resolveLinksLocked()
	}
	if st.IsDir() {
		return nil
	}
	if err := i.ingestLocked(v, rel, st.ModTime(), st.Size()); err != nil {
		return err
	}
	return i.resolveLinksLocked()
}

func (i *Index) ingestLocked(v *Vault, rel string, mtime time.Time, size int64) error {
	if err := i.removeLocked(rel); err != nil {
		return err
	}
	if !IsNote(rel) {
		_, err := i.db.Exec(`INSERT INTO notes(path,title,mtime,size,hash,is_note) VALUES(?,?,?,?,'',0)`,
			rel, path.Base(rel), mtime.UnixNano(), size)
		return err
	}
	if size > defaultMaxNoteBytes {
		return fmt.Errorf("%s is too large to index", rel)
	}
	abs, err := v.Resolve(rel)
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(abs)
	if err != nil {
		return err
	}
	n := ParseNote(rel, string(raw))

	tx, err := i.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`INSERT INTO notes(path,title,mtime,size,hash,created,updated,is_note,words)
		VALUES(?,?,?,?,?,?,?,1,?)`,
		rel, n.Title, mtime.UnixNano(), size, n.Hash,
		n.FrontString("created"), n.FrontString("updated"),
		len(strings.Fields(n.Body))); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO notes_fts(path,title,body) VALUES(?,?,?)`,
		rel, n.Title, n.Body); err != nil {
		return err
	}
	for _, t := range n.Tags() {
		if _, err := tx.Exec(`INSERT INTO tags(path,tag) VALUES(?,?)`, rel, t); err != nil {
			return err
		}
	}
	for _, l := range n.Links() {
		w := 0
		if l.Wiki {
			w = 1
		}
		if _, err := tx.Exec(`INSERT INTO links(src,target,anchor,alias,wiki) VALUES(?,?,?,?,?)`,
			rel, l.Target, l.Anchor, l.Alias, w); err != nil {
			return err
		}
	}
	for _, h := range n.Headings() {
		if _, err := tx.Exec(`INSERT INTO headings(path,level,text,line) VALUES(?,?,?,?)`,
			rel, h.Level, h.Text, h.Line); err != nil {
			return err
		}
	}
	for _, a := range n.FrontList("aliases") {
		if _, err := tx.Exec(`INSERT INTO aliases(path,alias) VALUES(?,?)`, rel, a); err != nil {
			return err
		}
	}
	for _, t := range n.Tasks() {
		d := 0
		if t.Done {
			d = 1
		}
		if _, err := tx.Exec(`INSERT INTO tasks(path,line,text,done) VALUES(?,?,?,?)`,
			rel, t.Line, t.Text, d); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (i *Index) removeLocked(rel string) error {
	for _, q := range []string{
		`DELETE FROM notes WHERE path=?`,
		`DELETE FROM notes_fts WHERE path=?`,
		`DELETE FROM tags WHERE path=?`,
		`DELETE FROM links WHERE src=?`,
		`DELETE FROM headings WHERE path=?`,
		`DELETE FROM aliases WHERE path=?`,
		`DELETE FROM tasks WHERE path=?`,
		`DELETE FROM embeddings WHERE path=?`,
	} {
		if _, err := i.db.Exec(q, rel); err != nil {
			return err
		}
	}
	return nil
}

// resolveLinksLocked maps every link target onto a real path, so that
// backlinks and broken-link reports are a query rather than a scan.
//
// Resolution order mirrors what Obsidian does and what a person expects:
// exact path, path without extension, unique basename, note title, alias.
func (i *Index) resolveLinksLocked() error {
	byPath := map[string]string{}
	byBase := map[string][]string{}
	byTitle := map[string][]string{}

	rows, err := i.db.Query(`SELECT path, title FROM notes WHERE is_note=1`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var p, t string
		if err := rows.Scan(&p, &t); err != nil {
			rows.Close()
			return err
		}
		byPath[p] = p
		byPath[strings.TrimSuffix(p, noteExt)] = p
		base := strings.TrimSuffix(path.Base(p), noteExt)
		byBase[strings.ToLower(base)] = append(byBase[strings.ToLower(base)], p)
		if t != "" {
			byTitle[strings.ToLower(t)] = append(byTitle[strings.ToLower(t)], p)
		}
	}
	rows.Close()

	byAlias := map[string][]string{}
	arows, err := i.db.Query(`SELECT path, alias FROM aliases`)
	if err != nil {
		return err
	}
	for arows.Next() {
		var p, a string
		if err := arows.Scan(&p, &a); err != nil {
			arows.Close()
			return err
		}
		byAlias[strings.ToLower(a)] = append(byAlias[strings.ToLower(a)], p)
	}
	arows.Close()

	resolve := func(src, target string) string {
		t := strings.TrimSpace(target)
		if t == "" {
			return ""
		}
		// A relative link resolves against the linking note's directory.
		if strings.HasPrefix(t, "./") || strings.HasPrefix(t, "../") {
			t = path.Clean(path.Join(path.Dir(src), t))
		}
		t = strings.TrimPrefix(t, "/")
		if p, ok := byPath[t]; ok {
			return p
		}
		if p, ok := byPath[t+noteExt]; ok {
			return p
		}
		low := strings.ToLower(t)
		for _, m := range []map[string][]string{byBase, byTitle, byAlias} {
			if c := m[low]; len(c) == 1 {
				return c[0]
			}
		}
		return ""
	}

	lrows, err := i.db.Query(`SELECT rowid, src, target FROM links`)
	if err != nil {
		return err
	}
	type upd struct {
		id  int64
		val string
	}
	var updates []upd
	for lrows.Next() {
		var id int64
		var src, target string
		if err := lrows.Scan(&id, &src, &target); err != nil {
			lrows.Close()
			return err
		}
		updates = append(updates, upd{id, resolve(src, target)})
	}
	lrows.Close()

	tx, err := i.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	st, err := tx.Prepare(`UPDATE links SET resolved=? WHERE rowid=?`)
	if err != nil {
		return err
	}
	defer st.Close()
	for _, u := range updates {
		if _, err := st.Exec(u.val, u.id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ---------------------------------------------------------------------------
// Queries
// ---------------------------------------------------------------------------

type SearchQuery struct {
	Text          string
	PathPrefix    string
	Glob          string
	Tags          []string
	ModifiedAfter time.Time
	Limit         int
	Offset        int
}

type SearchHit struct {
	Path     string   `json:"path"`
	Title    string   `json:"title"`
	Score    float64  `json:"score"`
	Snippet  string   `json:"snippet"`
	Tags     []string `json:"tags,omitempty"`
	Modified string   `json:"modified"`
	Bytes    int64    `json:"bytes"`
}

func (i *Index) Search(q SearchQuery) ([]SearchHit, int, error) {
	i.mu.Lock()
	defer i.mu.Unlock()

	limit := q.Limit
	if limit <= 0 || limit > 200 {
		limit = 25
	}

	where := []string{"n.is_note=1"}
	args := []any{}
	joins := ""
	order := "n.mtime DESC"
	sel := "0.0 AS score, '' AS snip"

	if strings.TrimSpace(q.Text) != "" {
		joins = `JOIN notes_fts f ON f.path = n.path`
		where = append(where, "notes_fts MATCH ?")
		args = append(args, buildMatch(q.Text))
		sel = `bm25(notes_fts, 8.0, 1.0) AS score, snippet(notes_fts, 2, '<<', '>>', ' … ', 24) AS snip`
		order = "score ASC"
	}
	if q.PathPrefix != "" {
		where = append(where, "n.path LIKE ?")
		args = append(args, strings.TrimSuffix(q.PathPrefix, "/")+"/%")
	}
	if q.Glob != "" {
		where = append(where, "n.path GLOB ?")
		args = append(args, q.Glob)
	}
	if !q.ModifiedAfter.IsZero() {
		where = append(where, "n.mtime >= ?")
		args = append(args, q.ModifiedAfter.UnixNano())
	}
	for _, t := range q.Tags {
		where = append(where, "EXISTS (SELECT 1 FROM tags tg WHERE tg.path=n.path AND tg.tag=?)")
		args = append(args, normaliseTag(t))
	}

	base := fmt.Sprintf(`FROM notes n %s WHERE %s`, joins, strings.Join(where, " AND "))

	var total int
	if err := i.db.QueryRow("SELECT COUNT(*) "+base, args...).Scan(&total); err != nil {
		return nil, 0, translateFTS(err)
	}

	rows, err := i.db.Query(fmt.Sprintf(
		`SELECT n.path, n.title, n.mtime, n.size, %s %s ORDER BY %s LIMIT ? OFFSET ?`,
		sel, base, order), append(args, limit, q.Offset)...)
	if err != nil {
		return nil, 0, translateFTS(err)
	}
	defer rows.Close()

	var out []SearchHit
	for rows.Next() {
		var h SearchHit
		var mtime int64
		var score float64
		var snip string
		if err := rows.Scan(&h.Path, &h.Title, &mtime, &h.Bytes, &score, &snip); err != nil {
			return nil, 0, err
		}
		h.Modified = time.Unix(0, mtime).UTC().Format(time.RFC3339)
		// bm25 returns a negative number where more negative is better.
		// Flipping it means "higher is better", which is what every caller
		// assumes when it sees a field called score.
		h.Score = -score
		h.Snippet = strings.TrimSpace(strings.ReplaceAll(snip, "\n", " "))
		out = append(out, h)
	}
	for k := range out {
		out[k].Tags, _ = i.tagsForLocked(out[k].Path)
	}
	return out, total, rows.Err()
}

// buildMatch turns a human query into an FTS5 expression. Bare words are
// ANDed; anything containing FTS operators is passed through so that a caller
// who knows the syntax can use it.
func buildMatch(q string) string {
	q = strings.TrimSpace(q)
	if strings.ContainsAny(q, `"*():`) || strings.Contains(q, " OR ") || strings.Contains(q, " NOT ") {
		return q
	}
	fields := strings.Fields(q)
	for i, f := range fields {
		fields[i] = `"` + strings.ReplaceAll(f, `"`, ``) + `"`
	}
	return strings.Join(fields, " AND ")
}

func translateFTS(err error) error {
	if err == nil {
		return nil
	}
	if strings.Contains(err.Error(), "fts5: syntax error") {
		return fmt.Errorf("the search query is not valid FTS5 syntax - try plain words, or quote phrases")
	}
	return err
}

func (i *Index) tagsForLocked(p string) ([]string, error) {
	rows, err := i.db.Query(`SELECT tag FROM tags WHERE path=? ORDER BY tag`, p)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, nil
}

type TagCount struct {
	Tag   string `json:"tag"`
	Notes int    `json:"notes"`
}

func (i *Index) TagCounts(prefix string) ([]TagCount, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	q := `SELECT tag, COUNT(DISTINCT path) c FROM tags`
	args := []any{}
	if prefix != "" {
		q += ` WHERE tag LIKE ?`
		args = append(args, normaliseTag(prefix)+"%")
	}
	q += ` GROUP BY tag ORDER BY c DESC, tag ASC`
	rows, err := i.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TagCount
	for rows.Next() {
		var t TagCount
		if err := rows.Scan(&t.Tag, &t.Notes); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

type LinkRow struct {
	Path   string `json:"path"`
	Title  string `json:"title,omitempty"`
	Target string `json:"target,omitempty"`
	Anchor string `json:"anchor,omitempty"`
}

func (i *Index) Backlinks(target string) ([]LinkRow, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	rows, err := i.db.Query(`SELECT DISTINCT l.src, COALESCE(n.title,''), l.target, l.anchor
		FROM links l LEFT JOIN notes n ON n.path=l.src
		WHERE l.resolved=? ORDER BY l.src`, target)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LinkRow
	for rows.Next() {
		var r LinkRow
		if err := rows.Scan(&r.Path, &r.Title, &r.Target, &r.Anchor); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (i *Index) Outlinks(src string) ([]LinkRow, []string, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	rows, err := i.db.Query(`SELECT l.target, l.anchor, l.resolved, COALESCE(n.title,'')
		FROM links l LEFT JOIN notes n ON n.path=l.resolved WHERE l.src=? ORDER BY l.rowid`, src)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var res []LinkRow
	var broken []string
	for rows.Next() {
		var target, anchor, resolved, title string
		if err := rows.Scan(&target, &anchor, &resolved, &title); err != nil {
			return nil, nil, err
		}
		if resolved == "" {
			broken = append(broken, target)
			continue
		}
		res = append(res, LinkRow{Path: resolved, Title: title, Target: target, Anchor: anchor})
	}
	return res, broken, rows.Err()
}

// Related scores notes by shared tags, shared links and co-citation. It is a
// deliberately cheap stand-in for semantic search: no model, no API call, and
// in a well linked vault it surfaces most of what an embedding would.
func (i *Index) Related(src string, limit int) ([]SearchHit, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	if limit <= 0 {
		limit = 10
	}
	score := map[string]float64{}

	add := func(q string, weight float64, args ...any) error {
		rows, err := i.db.Query(q, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var p string
			var n float64
			if err := rows.Scan(&p, &n); err != nil {
				return err
			}
			if p != src && p != "" {
				score[p] += weight * n
			}
		}
		return rows.Err()
	}

	// Shared tags, discounted by how common the tag is: everything is tagged
	// #note, so #note tells you nothing.
	if err := add(`SELECT t2.path, SUM(1.0 / (SELECT COUNT(*) FROM tags t3 WHERE t3.tag=t1.tag))
		FROM tags t1 JOIN tags t2 ON t2.tag=t1.tag WHERE t1.path=? GROUP BY t2.path`, 6.0, src); err != nil {
		return nil, err
	}
	// Notes this note links to, and notes that link to it.
	if err := add(`SELECT resolved, COUNT(*) FROM links WHERE src=? AND resolved<>'' GROUP BY resolved`, 3.0, src); err != nil {
		return nil, err
	}
	if err := add(`SELECT src, COUNT(*) FROM links WHERE resolved=? GROUP BY src`, 3.0, src); err != nil {
		return nil, err
	}
	// Co-citation: notes that link to the same things this one does.
	if err := add(`SELECT l2.src, COUNT(*) FROM links l1 JOIN links l2 ON l2.resolved=l1.resolved
		WHERE l1.src=? AND l1.resolved<>'' GROUP BY l2.src`, 1.5, src); err != nil {
		return nil, err
	}

	type kv struct {
		p string
		s float64
	}
	list := make([]kv, 0, len(score))
	for p, s := range score {
		list = append(list, kv{p, s})
	}
	sort.Slice(list, func(a, b int) bool {
		if list[a].s != list[b].s {
			return list[a].s > list[b].s
		}
		return list[a].p < list[b].p
	})
	if len(list) > limit {
		list = list[:limit]
	}
	out := make([]SearchHit, 0, len(list))
	for _, e := range list {
		var h SearchHit
		var mtime int64
		if err := i.db.QueryRow(`SELECT path,title,mtime,size FROM notes WHERE path=?`, e.p).
			Scan(&h.Path, &h.Title, &mtime, &h.Bytes); err != nil {
			continue
		}
		h.Score = e.s
		h.Modified = time.Unix(0, mtime).UTC().Format(time.RFC3339)
		h.Tags, _ = i.tagsForLocked(h.Path)
		out = append(out, h)
	}
	return out, nil
}

type VaultStats struct {
	Notes        int      `json:"notes"`
	Attachments  int      `json:"attachments"`
	Words        int      `json:"words"`
	Bytes        int64    `json:"bytes"`
	Tags         int      `json:"tags"`
	Links        int      `json:"links"`
	BrokenLinks  int      `json:"broken_links"`
	Orphans      int      `json:"orphans"`
	OpenTasks    int      `json:"open_tasks"`
	LastModified string   `json:"last_modified,omitempty"`
	TopTags      []string `json:"top_tags,omitempty"`
}

func (i *Index) Stats() (*VaultStats, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	s := &VaultStats{}
	var last int64
	_ = i.db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(words),0), COALESCE(SUM(size),0), COALESCE(MAX(mtime),0)
		FROM notes WHERE is_note=1`).Scan(&s.Notes, &s.Words, &s.Bytes, &last)
	if last > 0 {
		s.LastModified = time.Unix(0, last).UTC().Format(time.RFC3339)
	}
	_ = i.db.QueryRow(`SELECT COUNT(*) FROM notes WHERE is_note=0`).Scan(&s.Attachments)
	_ = i.db.QueryRow(`SELECT COUNT(DISTINCT tag) FROM tags`).Scan(&s.Tags)
	_ = i.db.QueryRow(`SELECT COUNT(*) FROM links`).Scan(&s.Links)
	_ = i.db.QueryRow(`SELECT COUNT(*) FROM links WHERE resolved=''`).Scan(&s.BrokenLinks)
	_ = i.db.QueryRow(`SELECT COUNT(*) FROM tasks WHERE done=0`).Scan(&s.OpenTasks)
	_ = i.db.QueryRow(`SELECT COUNT(*) FROM notes n WHERE n.is_note=1
		AND NOT EXISTS (SELECT 1 FROM links l WHERE l.resolved=n.path)
		AND NOT EXISTS (SELECT 1 FROM links l2 WHERE l2.src=n.path AND l2.resolved<>'')`).Scan(&s.Orphans)
	tc, _ := i.tagCountsLocked()
	for k, t := range tc {
		if k >= 10 {
			break
		}
		s.TopTags = append(s.TopTags, fmt.Sprintf("%s (%d)", t.Tag, t.Notes))
	}
	return s, nil
}

func (i *Index) tagCountsLocked() ([]TagCount, error) {
	rows, err := i.db.Query(`SELECT tag, COUNT(DISTINCT path) c FROM tags GROUP BY tag ORDER BY c DESC LIMIT 10`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TagCount
	for rows.Next() {
		var t TagCount
		if err := rows.Scan(&t.Tag, &t.Notes); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, nil
}

type ReviewItem struct {
	Path   string `json:"path"`
	Title  string `json:"title"`
	Reason string `json:"reason"`
	Detail string `json:"detail,omitempty"`
}

// Review is the maintenance list: what in this vault is rotting.
func (i *Index) Review(limitPer int, staleAfter time.Duration) ([]ReviewItem, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	if limitPer <= 0 {
		limitPer = 15
	}
	var out []ReviewItem

	collect := func(q, reason string, args ...any) error {
		rows, err := i.db.Query(q, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var p, t, d string
			if err := rows.Scan(&p, &t, &d); err != nil {
				return err
			}
			out = append(out, ReviewItem{Path: p, Title: t, Reason: reason, Detail: d})
		}
		return rows.Err()
	}

	if err := collect(`SELECT path, title, size || ' bytes' FROM notes
		WHERE is_note=1 AND size < 300 ORDER BY size ASC LIMIT ?`, "stub", limitPer); err != nil {
		return nil, err
	}
	if err := collect(`SELECT n.path, n.title, '' FROM notes n WHERE n.is_note=1
		AND NOT EXISTS (SELECT 1 FROM links l WHERE l.resolved=n.path)
		AND NOT EXISTS (SELECT 1 FROM links l2 WHERE l2.src=n.path AND l2.resolved<>'')
		ORDER BY n.mtime DESC LIMIT ?`, "orphan", limitPer); err != nil {
		return nil, err
	}
	if err := collect(`SELECT l.src, COALESCE(n.title,''), l.target FROM links l
		LEFT JOIN notes n ON n.path=l.src WHERE l.resolved='' ORDER BY l.src LIMIT ?`,
		"broken_link", limitPer); err != nil {
		return nil, err
	}
	cutoff := time.Now().Add(-staleAfter).UnixNano()
	if err := collect(`SELECT path, title, datetime(mtime/1000000000,'unixepoch') FROM notes
		WHERE is_note=1 AND mtime < ? ORDER BY mtime ASC LIMIT ?`, "stale", cutoff, limitPer); err != nil {
		return nil, err
	}
	if err := collect(`SELECT t.path, COALESCE(n.title,''), t.text FROM tasks t
		LEFT JOIN notes n ON n.path=t.path WHERE t.done=0 ORDER BY t.path LIMIT ?`,
		"open_task", limitPer); err != nil {
		return nil, err
	}
	return out, nil
}

type TaskRow struct {
	Path  string `json:"path"`
	Line  int    `json:"line"`
	Text  string `json:"text"`
	Done  bool   `json:"done"`
	Title string `json:"note_title,omitempty"`
}

func (i *Index) Tasks(includeDone bool, pathPrefix, contains string, limit int) ([]TaskRow, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	where := []string{}
	args := []any{}
	if !includeDone {
		where = append(where, "t.done=0")
	}
	if pathPrefix != "" {
		where = append(where, "t.path LIKE ?")
		args = append(args, strings.TrimSuffix(pathPrefix, "/")+"/%")
	}
	if contains != "" {
		where = append(where, "LOWER(t.text) LIKE ?")
		args = append(args, "%"+strings.ToLower(contains)+"%")
	}
	q := `SELECT t.path, t.line, t.text, t.done, COALESCE(n.title,'') FROM tasks t
		LEFT JOIN notes n ON n.path=t.path`
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += " ORDER BY t.path, t.line LIMIT ?"
	rows, err := i.db.Query(q, append(args, limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TaskRow
	for rows.Next() {
		var t TaskRow
		var done int
		if err := rows.Scan(&t.Path, &t.Line, &t.Text, &done, &t.Title); err != nil {
			return nil, err
		}
		t.Done = done == 1
		out = append(out, t)
	}
	return out, rows.Err()
}

// NotePaths lists indexed note paths, used by vault-wide operations.
func (i *Index) NotePaths() ([]string, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	rows, err := i.db.Query(`SELECT path FROM notes WHERE is_note=1 ORDER BY path`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
