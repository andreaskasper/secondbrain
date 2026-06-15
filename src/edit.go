package main

import (
	"errors"
	"fmt"
	"hash/fnv"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// ErrPreconditionFailed is returned when an If-Match precondition does not
// match the current state of the file (optimistic concurrency control). It
// maps to HTTP 412.
var ErrPreconditionFailed = errors.New("precondition failed")

// ValidationError marks a client-side mistake in an edit request (bad line
// range, bad pattern, empty search, ...). It maps to HTTP 400.
type ValidationError struct{ msg string }

func (e *ValidationError) Error() string { return e.msg }

func badEdit(format string, a ...any) error {
	return &ValidationError{msg: fmt.Sprintf(format, a...)}
}

// etagFor returns a strong, content-derived ETag (quoted) for the given bytes.
// A change of a single byte changes the tag, which is exactly what we need for
// an If-Match / If-None-Match validator.
func etagFor(content []byte) string {
	h := fnv.New64a()
	_, _ = h.Write(content)
	return `"` + strconv.FormatUint(h.Sum64(), 16) + `"`
}

func normETag(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "W/")
	return strings.Trim(s, `"`)
}

// etagMatches reports whether the If-Match header value satisfies the current
// ETag. "*" matches any existing file; a comma-separated list matches if any
// member matches. Weak ("W/") prefixes and quoting are ignored.
func etagMatches(ifMatch, current string) bool {
	if strings.TrimSpace(ifMatch) == "*" {
		return true
	}
	cur := normETag(current)
	for _, tok := range strings.Split(ifMatch, ",") {
		if normETag(tok) == cur {
			return true
		}
	}
	return false
}

// EditResult is the outcome of a partial edit / conditional write.
type EditResult struct {
	Content []byte
	ETag    string
	Size    int
	Count   int // replacements made / frontmatter keys touched, where relevant
}

// editLocked reads a file under the store write lock, optionally verifies an
// If-Match precondition, applies transform to produce the new content, and
// writes it back. transform returns (newContent, count, error).
func (s *Store) editLocked(urlPath, ifMatch string, transform func(old []byte) ([]byte, int, error)) (*EditResult, error) {
	full, err := s.resolve(urlPath)
	if err != nil {
		return nil, err
	}
	if !strings.EqualFold(filepath.Ext(full), mdExt) {
		return nil, ErrNotMarkdown
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	info, err := os.Stat(full)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if info.IsDir() {
		return nil, ErrIsDirectory
	}

	old, err := os.ReadFile(full)
	if err != nil {
		return nil, err
	}
	if ifMatch != "" && !etagMatches(ifMatch, etagFor(old)) {
		return nil, ErrPreconditionFailed
	}

	newContent, count, terr := transform(old)
	if terr != nil {
		return nil, terr
	}
	if err := os.WriteFile(full, newContent, 0o644); err != nil {
		return nil, err
	}
	return &EditResult{
		Content: newContent,
		ETag:    etagFor(newContent),
		Size:    len(newContent),
		Count:   count,
	}, nil
}

// --- line-targeted edits ---------------------------------------------------

// LineEdit selects which part of a file a PATCH body replaces / where it is
// inserted. Exactly one selector is honoured, checked in the order below.
type LineEdit struct {
	Lines   string // "A-B", "A-", "-B" (1-based, inclusive) -> replace range
	Head    int    // replace the first N lines
	Tail    int    // replace the last N lines
	Insert  int    // insert the body *before* this 1-based line
	Prepend bool   // insert the body at the very top
}

// PatchLines replaces or inserts lines in an existing file.
func (s *Store) PatchLines(urlPath string, e LineEdit, body []byte, ifMatch string) (*EditResult, error) {
	return s.editLocked(urlPath, ifMatch, func(old []byte) ([]byte, int, error) {
		nc, err := applyLineEdit(old, e, body)
		return nc, 0, err
	})
}

func applyLineEdit(content []byte, e LineEdit, body []byte) ([]byte, error) {
	lines := splitLines(content)
	repl := splitLines(body)
	trailing := endsWithNewline(content)

	var out []string
	switch {
	case e.Prepend:
		out = append(out, repl...)
		out = append(out, lines...)
	case e.Insert > 0:
		idx := e.Insert - 1
		if idx > len(lines) {
			idx = len(lines)
		}
		out = append(out, lines[:idx]...)
		out = append(out, repl...)
		out = append(out, lines[idx:]...)
	case e.Head > 0:
		n := e.Head
		if n > len(lines) {
			n = len(lines)
		}
		out = append(out, repl...)
		out = append(out, lines[n:]...)
	case e.Tail > 0:
		n := e.Tail
		if n > len(lines) {
			n = len(lines)
		}
		out = append(out, lines[:len(lines)-n]...)
		out = append(out, repl...)
	case e.Lines != "":
		from, to, ok := parseRange(e.Lines, len(lines))
		if !ok {
			return nil, badEdit("invalid line range %q", e.Lines)
		}
		out = append(out, lines[:from]...)
		out = append(out, repl...)
		out = append(out, lines[to:]...)
	default:
		// No selector -> append (kept for completeness; the handler routes
		// the no-selector case to AppendCond instead).
		out = append(out, lines...)
		out = append(out, repl...)
	}

	res := strings.Join(out, "\n")
	if trailing && res != "" {
		res += "\n"
	}
	return []byte(res), nil
}

func endsWithNewline(b []byte) bool {
	return len(b) > 0 && b[len(b)-1] == '\n'
}

// --- find & replace --------------------------------------------------------

// ReplaceSpec configures a search-and-replace edit.
type ReplaceSpec struct {
	Find          string
	With          string
	UseRegex      bool
	CaseSensitive bool
	All           bool // replace every occurrence (default: first only)
}

// PatchReplace performs a search-and-replace over the whole file.
func (s *Store) PatchReplace(urlPath string, spec ReplaceSpec, ifMatch string) (*EditResult, error) {
	return s.editLocked(urlPath, ifMatch, func(old []byte) ([]byte, int, error) {
		return applyReplace(old, spec)
	})
}

func applyReplace(content []byte, spec ReplaceSpec) ([]byte, int, error) {
	if spec.Find == "" {
		return nil, 0, badEdit("replace: empty search string")
	}
	pat := spec.Find
	if !spec.UseRegex {
		pat = regexp.QuoteMeta(pat)
	}
	if !spec.CaseSensitive {
		pat = "(?i)" + pat
	}
	re, err := regexp.Compile(pat)
	if err != nil {
		return nil, 0, badEdit("replace: invalid pattern: %v", err)
	}

	text := string(content)
	count := 0
	if spec.All {
		count = len(re.FindAllStringIndex(text, -1))
		if spec.UseRegex {
			text = re.ReplaceAllString(text, spec.With)
		} else {
			text = re.ReplaceAllLiteralString(text, spec.With)
		}
	} else if loc := re.FindStringIndex(text); loc != nil {
		count = 1
		repl := spec.With
		if spec.UseRegex {
			repl = re.ReplaceAllString(text[loc[0]:loc[1]], spec.With)
		}
		text = text[:loc[0]] + repl + text[loc[1]:]
	}
	return []byte(text), count, nil
}

// --- frontmatter merge -----------------------------------------------------

// PatchFrontmatter merges the key/value pairs in body into the file's
// frontmatter, creating the block if absent. A value of "null" or "~" deletes
// the key.
func (s *Store) PatchFrontmatter(urlPath string, body []byte, ifMatch string) (*EditResult, error) {
	incoming := parseFlatYAML(string(body))
	if len(incoming) == 0 {
		return nil, &ValidationError{msg: "frontmatter: no valid 'key: value' lines in body"}
	}
	return s.editLocked(urlPath, ifMatch, func(old []byte) ([]byte, int, error) {
		fm, rest := splitFrontmatter(old)
		if fm == nil {
			fm = Frontmatter{}
		}
		n := 0
		for k, v := range incoming {
			if isNull(v) {
				delete(fm, k)
			} else {
				fm[k] = v
			}
			n++
		}
		return serializeFrontmatter(fm, rest), n, nil
	})
}

// --- conditional append / write / delete -----------------------------------

// AppendCond appends body to an existing file, ensuring a single newline
// separator, honouring an optional If-Match precondition.
func (s *Store) AppendCond(urlPath string, body []byte, ifMatch string) (*EditResult, error) {
	return s.editLocked(urlPath, ifMatch, func(old []byte) ([]byte, int, error) {
		out := make([]byte, 0, len(old)+len(body)+1)
		out = append(out, old...)
		if len(old) > 0 && !endsWithNewline(old) {
			out = append(out, '\n')
		}
		out = append(out, body...)
		return out, 0, nil
	})
}

// WriteCond creates or overwrites a file. With ifMatch set it requires the
// current content to match (or, for a non-existent file, fails the
// precondition). Parent directories are created automatically.
func (s *Store) WriteCond(urlPath string, content []byte, overwrite bool, ifMatch string) (created bool, etag string, err error) {
	full, err := s.resolve(urlPath)
	if err != nil {
		return false, "", err
	}
	if !strings.EqualFold(filepath.Ext(full), mdExt) {
		return false, "", ErrNotMarkdown
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	existed := false
	if info, statErr := os.Stat(full); statErr == nil {
		if info.IsDir() {
			return false, "", ErrIsDirectory
		}
		existed = true
		if !overwrite {
			return false, "", ErrAlreadyExists
		}
		if ifMatch != "" {
			old, rerr := os.ReadFile(full)
			if rerr != nil {
				return false, "", rerr
			}
			if !etagMatches(ifMatch, etagFor(old)) {
				return false, "", ErrPreconditionFailed
			}
		}
	} else if ifMatch != "" {
		// Caller required a specific (or any "*") existing version, but the
		// file is absent -> the precondition cannot hold.
		return false, "", ErrPreconditionFailed
	}

	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return false, "", err
	}
	if err := os.WriteFile(full, content, 0o644); err != nil {
		return false, "", err
	}
	return !existed, etagFor(content), nil
}

// DeleteCond removes a file or directory. For files an optional If-Match
// precondition is honoured; for directories it is ignored.
func (s *Store) DeleteCond(urlPath string, recursive bool, ifMatch string) error {
	full, err := s.resolve(urlPath)
	if err != nil {
		return err
	}
	if full == s.Root {
		return ErrForbidden // never delete the root
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	info, err := os.Stat(full)
	if err != nil {
		if os.IsNotExist(err) {
			return ErrNotFound
		}
		return err
	}
	if info.IsDir() {
		if recursive {
			return os.RemoveAll(full)
		}
		entries, rerr := os.ReadDir(full)
		if rerr != nil {
			return rerr
		}
		if len(entries) > 0 {
			return ErrDirNotEmpty
		}
		return os.Remove(full)
	}
	if ifMatch != "" {
		old, rerr := os.ReadFile(full)
		if rerr != nil {
			return rerr
		}
		if !etagMatches(ifMatch, etagFor(old)) {
			return ErrPreconditionFailed
		}
	}
	return os.Remove(full)
}
