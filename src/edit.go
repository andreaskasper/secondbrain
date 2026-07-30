package main

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// Writing
//
// Every mutation funnels through Apply, which does the same four things in the
// same order: check the caller's assumption about the current state, produce
// the new text, show it as a diff, and only then touch the disk. Having one
// path means the guarantees - optimistic locking, dry runs, trash, git - are
// properties of the system rather than things each tool remembers to do.
// ---------------------------------------------------------------------------

type WriteResult struct {
	Path       string `json:"path"`
	Vault      string `json:"vault"`
	Created    bool   `json:"created,omitempty"`
	Deleted    bool   `json:"deleted,omitempty"`
	DryRun     bool   `json:"dry_run,omitempty"`
	Diff       string `json:"diff,omitempty"`
	HashBefore string `json:"hash_before,omitempty"`
	HashAfter  string `json:"content_hash,omitempty"`
	Bytes      int    `json:"bytes"`
	Message    string `json:"message,omitempty"`
}

type writeOp struct {
	rel string
	// expected, when non-empty, must equal the current content hash. This is
	// the whole concurrency story: Obsidian, a git pull and an agent can all
	// write into the same directory, and the only safe assumption is that the
	// file changed since it was read.
	expected string
	dryRun   bool
	reason   string
	// transform receives the current contents ("" when the file does not
	// exist) and returns the new contents.
	transform func(cur string, exists bool) (string, error)
	// touch updates the frontmatter timestamp unless the caller opts out.
	skipTouch bool
}

func (v *Vault) Apply(op writeOp) (*WriteResult, error) {
	abs, clean, err := v.ResolveNote(op.rel)
	if err != nil {
		return nil, err
	}

	v.writeMu.Lock()
	defer v.writeMu.Unlock()

	cur, exists, err := readIfExists(abs)
	if err != nil {
		return nil, err
	}
	before := ""
	if exists {
		before = HashContent(cur)
		if op.expected != "" && !strings.EqualFold(op.expected, before) {
			return nil, fmt.Errorf("%w: %s has content_hash %s, not %s - read it again before writing",
				errStale, clean, before, op.expected)
		}
	} else if op.expected != "" {
		return nil, fmt.Errorf("%w: %s", errNotFound, clean)
	}

	next, err := op.transform(cur, exists)
	if err != nil {
		return nil, err
	}
	if !op.skipTouch && next != "" {
		next = touchUpdated(clean, next, exists)
	}

	res := &WriteResult{
		Path: clean, Vault: v.Name, Created: !exists, DryRun: op.dryRun,
		HashBefore: before, HashAfter: HashContent(next), Bytes: len(next),
		Diff: UnifiedDiff(cur, next, clean),
	}
	if next == cur && exists {
		res.Message = "no change"
		return res, nil
	}
	if op.dryRun {
		res.Message = "dry run: nothing was written"
		return res, nil
	}

	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return nil, err
	}
	if exists {
		if err := v.stash(clean, cur); err != nil {
			return nil, err
		}
	}
	if err := writeAtomic(abs, next); err != nil {
		return nil, err
	}
	v.afterWrite(clean, op.reason)
	return res, nil
}

// Delete moves a note to the trash. Nothing in this program calls os.Remove on
// a note: a knowledge base where an agent can destroy something irreversibly
// is a knowledge base you cannot let an agent near.
func (v *Vault) Delete(rel string, expected string, dryRun bool) (*WriteResult, error) {
	abs, clean, err := v.ResolveNote(rel)
	if err != nil {
		return nil, err
	}
	v.writeMu.Lock()
	defer v.writeMu.Unlock()

	cur, exists, err := readIfExists(abs)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, fmt.Errorf("%w: %s", errNotFound, clean)
	}
	before := HashContent(cur)
	if expected != "" && !strings.EqualFold(expected, before) {
		return nil, fmt.Errorf("%w: %s has content_hash %s", errStale, clean, before)
	}
	res := &WriteResult{Path: clean, Vault: v.Name, Deleted: true, DryRun: dryRun, HashBefore: before}
	if dryRun {
		res.Message = "dry run: the note was not deleted"
		return res, nil
	}
	if err := v.stash(clean, cur); err != nil {
		return nil, err
	}
	if err := os.Remove(abs); err != nil {
		return nil, err
	}
	res.Message = "moved to " + trashDir
	v.afterWrite(clean, "note_delete: "+clean)
	return res, nil
}

// Move renames a note. Link rewriting is a separate, explicit step because
// silently editing thirty other files is not something that should happen as a
// side effect of a rename.
func (v *Vault) Move(from, to string, overwrite bool) (string, string, error) {
	fromAbs, fromRel, err := v.ResolveNote(from)
	if err != nil {
		return "", "", err
	}
	toAbs, toRel, err := v.ResolveNote(to)
	if err != nil {
		return "", "", err
	}
	if _, err := os.Stat(fromAbs); err != nil {
		return "", "", fmt.Errorf("%w: %s", errNotFound, fromRel)
	}
	if _, err := os.Stat(toAbs); err == nil {
		if !overwrite {
			return "", "", fmt.Errorf("%w: %s", errExists, toRel)
		}
		// Overwriting still keeps a copy. "Nothing is destroyed" has to hold
		// on the unusual path too, or it is not a property, only a habit.
		if prev, ok, _ := readIfExists(toAbs); ok {
			if err := v.stash(toRel, prev); err != nil {
				return "", "", err
			}
		}
	}
	if err := os.MkdirAll(filepath.Dir(toAbs), 0o755); err != nil {
		return "", "", err
	}
	if err := os.Rename(fromAbs, toAbs); err != nil {
		return "", "", err
	}
	return fromRel, toRel, nil
}

func (v *Vault) afterWrite(rel, reason string) {
	if v.idx != nil {
		if err := v.idx.UpdatePath(v, rel); err != nil {
			v.metrics.IndexError()
			logWarn("index_update_failed", map[string]any{"vault": v.Name, "path": rel, "error": err.Error()})
		} else {
			v.metrics.IndexUpdated()
		}
	}
	if v.git != nil && reason != "" {
		if err := v.git.Commit(reason); err != nil {
			v.metrics.GitFailure()
			logWarn("git_commit_failed", map[string]any{"vault": v.Name, "error": err.Error()})
		} else {
			v.metrics.GitCommit()
		}
	}
}

// stash keeps the previous contents so that an overwrite is recoverable even
// when git is switched off.
func (v *Vault) stash(rel, content string) error {
	stamp := time.Now().UTC().Format("20060102-150405")
	dst := filepath.Join(v.Root, filepath.FromSlash(trashDir), stamp+"-"+strings.ReplaceAll(rel, "/", "__"))
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	return os.WriteFile(dst, []byte(content), 0o644)
}

// PurgeTrash removes stashed copies older than the retention window.
func (v *Vault) PurgeTrash(retention time.Duration) int {
	dir := filepath.Join(v.Root, filepath.FromSlash(trashDir))
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	cutoff := time.Now().Add(-retention)
	n := 0
	for _, e := range entries {
		info, err := e.Info()
		if err != nil || info.ModTime().After(cutoff) {
			continue
		}
		if os.Remove(filepath.Join(dir, e.Name())) == nil {
			n++
		}
	}
	return n
}

func readIfExists(abs string) (string, bool, error) {
	b, err := os.ReadFile(abs)
	if err != nil {
		if os.IsNotExist(err) {
			return "", false, nil
		}
		return "", false, err
	}
	return string(b), true, nil
}

// writeAtomic writes through a temporary file in the same directory so that a
// crash mid-write leaves either the old note or the new one, never half of
// each.
func writeAtomic(abs, content string) error {
	dir := filepath.Dir(abs)
	tmp, err := os.CreateTemp(dir, ".sbtmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err := tmp.WriteString(content); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(name, 0o644); err != nil {
		return err
	}
	return os.Rename(name, abs)
}

// touchUpdated maintains created/updated in the frontmatter. It only touches
// a note that already has frontmatter, or one being created - adding metadata
// to somebody's hand written file because a tool ran over it would be rude.
func touchUpdated(rel, content string, existed bool) string {
	n := ParseNote(rel, content)
	if n.front == nil && existed {
		return content
	}
	now := time.Now().UTC().Format(time.RFC3339)
	if n.FrontString("created") == "" {
		_ = n.SetFront("created", now)
	}
	_ = n.SetFront("updated", now)
	return n.Render()
}

// ---------------------------------------------------------------------------
// Transforms
// ---------------------------------------------------------------------------

// StringEdit replaces an exact substring. The uniqueness requirement is the
// safety property: if the snippet appears twice, the caller did not know what
// it was editing and the edit is refused rather than guessed.
func StringEdit(cur, oldStr, newStr string, replaceAll bool) (string, int, error) {
	if oldStr == "" {
		return "", 0, fmt.Errorf("old_string must not be empty")
	}
	if oldStr == newStr {
		return "", 0, fmt.Errorf("old_string and new_string are identical")
	}
	count := strings.Count(cur, oldStr)
	switch {
	case count == 0:
		return "", 0, fmt.Errorf("old_string was not found - it must match the note exactly, including whitespace")
	case count > 1 && !replaceAll:
		return "", 0, fmt.Errorf("old_string appears %d times; include more surrounding text to make it unique, or set replace_all", count)
	}
	if replaceAll {
		return strings.ReplaceAll(cur, oldStr, newStr), count, nil
	}
	return strings.Replace(cur, oldStr, newStr, 1), 1, nil
}

type SectionMode string

const (
	SectionAppend       SectionMode = "append_to_section"
	SectionPrepend      SectionMode = "prepend_to_section"
	SectionReplace      SectionMode = "replace_section"
	SectionInsertBefore SectionMode = "insert_before_section"
	SectionInsertAfter  SectionMode = "insert_after_section"
	SectionDelete       SectionMode = "delete_section"
	SectionAppendEnd    SectionMode = "append_to_note"
	SectionPrependTop   SectionMode = "prepend_to_note"
)

func ParseSectionMode(s string) (SectionMode, error) {
	m := SectionMode(strings.TrimSpace(s))
	switch m {
	case SectionAppend, SectionPrepend, SectionReplace, SectionInsertBefore,
		SectionInsertAfter, SectionDelete, SectionAppendEnd, SectionPrependTop:
		return m, nil
	default:
		return "", fmt.Errorf("unknown mode %q", s)
	}
}

func (m SectionMode) needsHeading() bool {
	return m != SectionAppendEnd && m != SectionPrependTop
}

// SectionEdit rewrites part of a note relative to a heading.
//
// A section runs from its heading to the next heading of the same or a higher
// level, which is what a reader means by "that section" and what an agent
// otherwise has to reconstruct by counting lines - badly.
func SectionEdit(n *Note, mode SectionMode, heading, content string) (string, error) {
	body := n.Body
	lines := strings.Split(body, "\n")

	switch mode {
	case SectionAppendEnd:
		n.Body = joinBlocks(body, content)
		return n.Render(), nil
	case SectionPrependTop:
		n.Body = joinBlocks(content, body)
		return n.Render(), nil
	}

	start, end, level := findSection(n, heading)
	if start < 0 {
		return "", fmt.Errorf("no heading matching %q in %s - call note_outline to see the headings that exist", heading, n.Path)
	}

	var out []string
	switch mode {
	case SectionReplace:
		out = append(out, lines[:start+1]...)
		out = append(out, "", strings.TrimRight(content, "\n"), "")
		out = append(out, lines[end:]...)
	case SectionDelete:
		out = append(out, lines[:start]...)
		out = append(out, lines[end:]...)
	case SectionAppend:
		tail := end
		for tail > start+1 && strings.TrimSpace(lines[tail-1]) == "" {
			tail--
		}
		out = append(out, lines[:tail]...)
		out = append(out, "", strings.TrimRight(content, "\n"), "")
		out = append(out, lines[end:]...)
	case SectionPrepend:
		out = append(out, lines[:start+1]...)
		out = append(out, "", strings.TrimRight(content, "\n"))
		out = append(out, lines[start+1:]...)
	case SectionInsertBefore:
		out = append(out, lines[:start]...)
		out = append(out, strings.TrimRight(content, "\n"), "")
		out = append(out, lines[start:]...)
	case SectionInsertAfter:
		out = append(out, lines[:end]...)
		out = append(out, "", strings.TrimRight(content, "\n"), "")
		out = append(out, lines[end:]...)
	default:
		return "", fmt.Errorf("unknown mode %q", mode)
	}
	_ = level
	n.Body = collapseBlanks(strings.Join(out, "\n"))
	return n.Render(), nil
}

// findSection locates a heading by exact text, then by case-insensitive match,
// then by prefix. Returns the heading line index, the index one past the last
// line of the section, and the heading level.
func findSection(n *Note, heading string) (int, int, int) {
	want := strings.TrimSpace(strings.TrimLeft(heading, "# "))
	hs := n.Headings()
	pick := -1
	for pass := 0; pass < 3 && pick < 0; pass++ {
		for i, h := range hs {
			switch pass {
			case 0:
				if h.Text == want {
					pick = i
				}
			case 1:
				if strings.EqualFold(h.Text, want) {
					pick = i
				}
			case 2:
				if strings.HasPrefix(strings.ToLower(h.Text), strings.ToLower(want)) {
					pick = i
				}
			}
			if pick >= 0 {
				break
			}
		}
	}
	if pick < 0 {
		return -1, -1, 0
	}
	h := hs[pick]
	start := h.Line - 1
	end := len(strings.Split(n.Body, "\n"))
	for _, other := range hs[pick+1:] {
		if other.Level <= h.Level {
			end = other.Line - 1
			break
		}
	}
	return start, end, h.Level
}

// SectionText returns just the text of one section, so that a caller can read
// a paragraph out of a long note without pulling the whole thing into context.
func SectionText(n *Note, heading string) (string, error) {
	start, end, _ := findSection(n, heading)
	if start < 0 {
		return "", fmt.Errorf("no heading matching %q in %s", heading, n.Path)
	}
	lines := strings.Split(n.Body, "\n")
	if end > len(lines) {
		end = len(lines)
	}
	return strings.TrimRight(strings.Join(lines[start:end], "\n"), "\n") + "\n", nil
}

func joinBlocks(a, b string) string {
	a = strings.TrimRight(a, "\n")
	b = strings.TrimLeft(strings.TrimRight(b, "\n"), "\n")
	if a == "" {
		return b + "\n"
	}
	if b == "" {
		return a + "\n"
	}
	return a + "\n\n" + b + "\n"
}

// collapseBlanks reduces runs of three or more blank lines to two. Section
// edits naturally leave extra separation behind and it accumulates.
func collapseBlanks(s string) string {
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	blank := 0
	for _, l := range lines {
		if strings.TrimSpace(l) == "" {
			blank++
			if blank > 2 {
				continue
			}
		} else {
			blank = 0
		}
		out = append(out, l)
	}
	res := strings.Join(out, "\n")
	return strings.TrimRight(res, "\n") + "\n"
}

// RenderTemplate expands the placeholders used in templates/.
func RenderTemplate(body, title string, now time.Time) string {
	r := strings.NewReplacer(
		"{{title}}", title,
		"{{date}}", now.Format("2006-01-02"),
		"{{time}}", now.Format("15:04"),
		"{{datetime}}", now.Format(time.RFC3339),
		"{{year}}", now.Format("2006"),
		"{{month}}", now.Format("01"),
		"{{day}}", now.Format("02"),
		"{{slug}}", Slug(title),
	)
	return r.Replace(body)
}

// DailyPath is the journal convention every layout shares.
func DailyPath(day time.Time) string {
	return path.Join("journal", day.Format("2006"), day.Format("2006-01-02")+noteExt)
}
