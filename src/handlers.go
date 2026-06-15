package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// App wires the store and config into HTTP handlers.
type App struct {
	store *Store
	cfg   Config
}

// FileResponse is the JSON representation of a markdown file.
type FileResponse struct {
	Path        string      `json:"path"`
	Size        int64       `json:"size"`
	Modified    time.Time   `json:"modified"`
	Frontmatter Frontmatter `json:"frontmatter,omitempty"`
	Body        string      `json:"body"`
}

// ListResponse is the JSON representation of a directory listing.
type ListResponse struct {
	Path    string  `json:"path"`
	Type    string  `json:"type"` // always "dir"
	Count   int     `json:"count"`
	Entries []Entry `json:"entries"`
}

// ServeHTTP is the single entry point; it dispatches on the HTTP method.
func (a *App) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet, http.MethodHead:
		a.handleGet(w, r)
	case http.MethodPost:
		a.handleWrite(w, r, false)
	case http.MethodPut:
		a.handleWrite(w, r, true)
	case http.MethodPatch:
		a.handlePatch(w, r)
	case http.MethodDelete:
		a.handleDelete(w, r)
	default:
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (a *App) handleGet(w http.ResponseWriter, r *http.Request) {
	urlPath := r.URL.Path
	q := r.URL.Query()

	// A search query operates on the subtree at the given path.
	if search := q.Get("search"); search != "" {
		a.handleSearch(w, r, urlPath, search)
		return
	}

	info, _, err := a.store.Stat(urlPath)
	if err != nil {
		writeStoreError(w, err)
		return
	}

	if info.IsDir() {
		a.serveListing(w, r, urlPath)
		return
	}
	a.serveFile(w, r, urlPath, info.Size(), info.ModTime())
}

func (a *App) serveListing(w http.ResponseWriter, r *http.Request, urlPath string) {
	q := r.URL.Query()
	recursive := isTrue(q.Get("recursive"))
	withMeta := isTrue(q.Get("meta"))

	entries, err := a.store.List(urlPath, recursive, withMeta)
	if err != nil {
		writeStoreError(w, err)
		return
	}

	if prefersMarkdown(r) {
		w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		w.Write([]byte(renderMarkdownIndex(cleanURLPath(urlPath), entries)))
		return
	}

	writeJSON(w, http.StatusOK, ListResponse{
		Path:    cleanURLPath(urlPath),
		Type:    "dir",
		Count:   len(entries),
		Entries: entries,
	})
}

func (a *App) serveFile(w http.ResponseWriter, r *http.Request, urlPath string, size int64, mod time.Time) {
	content, err := a.store.ReadFile(urlPath)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	q := r.URL.Query()

	// The ETag tracks the whole-file state and pairs with If-Match on writes.
	etag := etagFor(content)
	w.Header().Set("ETag", etag)

	// Conditional GET only for the plain, unmodified representation, so the
	// validator unambiguously identifies the bytes the client would receive.
	plain := !prefersJSON(r) && !isTrue(q.Get("json")) && !isTrue(q.Get("nofrontmatter")) &&
		q.Get("head") == "" && q.Get("tail") == "" && q.Get("grep") == "" && q.Get("lines") == ""
	if plain {
		if inm := r.Header.Get("If-None-Match"); inm != "" && etagMatches(inm, etag) {
			w.WriteHeader(http.StatusNotModified)
			return
		}
	}

	// Structured JSON view with parsed frontmatter.
	if prefersJSON(r) || isTrue(q.Get("json")) {
		fm, body := splitFrontmatter(content)
		writeJSON(w, http.StatusOK, FileResponse{
			Path:        cleanURLPath(urlPath),
			Size:        size,
			Modified:    mod,
			Frontmatter: fm,
			Body:        string(body),
		})
		return
	}

	// Optionally strip frontmatter for the raw view.
	if isTrue(q.Get("nofrontmatter")) {
		_, content = splitFrontmatter(content)
	}

	head := atoiDefault(q.Get("head"), 0)
	tail := atoiDefault(q.Get("tail"), 0)
	grep := q.Get("grep")
	lineRange := q.Get("lines")

	if head > 0 || tail > 0 || grep != "" || lineRange != "" {
		sliced, mErr := applyLineModifiers(content, grep, lineRange, head, tail)
		if mErr != nil {
			writeError(w, http.StatusBadRequest, "invalid grep pattern: "+mErr.Error())
			return
		}
		content = sliced
	}

	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	if r.Method == http.MethodHead {
		w.Header().Set("Content-Length", strconv.Itoa(len(content)))
		return
	}
	w.Write(content)
}

func (a *App) handleSearch(w http.ResponseWriter, r *http.Request, urlPath, query string) {
	q := r.URL.Query()
	opts := SearchOptions{
		Query:         query,
		UseRegex:      isTrue(q.Get("regex")),
		CaseSensitive: isTrue(q.Get("case")),
		Limit:         atoiDefault(q.Get("limit"), 200),
	}
	res, err := a.store.Search(urlPath, opts)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			writeStoreError(w, err)
			return
		}
		writeError(w, http.StatusBadRequest, "search failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (a *App) handleWrite(w http.ResponseWriter, r *http.Request, overwrite bool) {
	body, err := a.readBody(w, r)
	if err != nil {
		return // error already written
	}
	ifMatch := strings.TrimSpace(r.Header.Get("If-Match"))
	created, etag, werr := a.store.WriteCond(r.URL.Path, body, overwrite, ifMatch)
	if werr != nil {
		writeStoreError(w, werr)
		return
	}
	setETag(w, etag)
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, map[string]any{
		"path":    cleanURLPath(r.URL.Path),
		"created": created,
		"size":    len(body),
		"etag":    etag,
	})
}

// handlePatch supports four edit modes, selected by query parameters:
//
//	?replace=OLD&with=NEW[&regex=1][&all=1][&case=1]  search & replace
//	?frontmatter=1            (body: key: value lines) merge frontmatter
//	?lines=A-B | ?head=N | ?tail=N | ?insert=N | ?prepend=1  line-targeted edit
//	(no parameters)           append the body (default)
//
// All modes accept an optional If-Match header for optimistic locking.
func (a *App) handlePatch(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	ifMatch := strings.TrimSpace(r.Header.Get("If-Match"))

	// Mode 1: find & replace (parameters carry the data; the body is ignored).
	if q.Has("replace") {
		spec := ReplaceSpec{
			Find:          q.Get("replace"),
			With:          q.Get("with"),
			UseRegex:      isTrue(q.Get("regex")),
			CaseSensitive: isTrue(q.Get("case")),
			All:           isTrue(q.Get("all")),
		}
		res, err := a.store.PatchReplace(r.URL.Path, spec, ifMatch)
		if err != nil {
			writeStoreError(w, err)
			return
		}
		setETag(w, res.ETag)
		writeJSON(w, http.StatusOK, map[string]any{
			"path":     cleanURLPath(r.URL.Path),
			"replaced": res.Count,
			"size":     res.Size,
			"etag":     res.ETag,
		})
		return
	}

	// The remaining modes consume the request body.
	body, err := a.readBody(w, r)
	if err != nil {
		return
	}

	// Mode 2: merge frontmatter keys.
	if isTrue(q.Get("frontmatter")) {
		res, ferr := a.store.PatchFrontmatter(r.URL.Path, body, ifMatch)
		if ferr != nil {
			writeStoreError(w, ferr)
			return
		}
		setETag(w, res.ETag)
		writeJSON(w, http.StatusOK, map[string]any{
			"path":    cleanURLPath(r.URL.Path),
			"updated": res.Count,
			"size":    res.Size,
			"etag":    res.ETag,
		})
		return
	}

	// Mode 3: targeted line edit (replace a range / head / tail, or insert).
	if q.Has("lines") || q.Has("head") || q.Has("tail") || q.Has("insert") || q.Has("prepend") {
		if q.Has("insert") && atoiDefault(q.Get("insert"), 0) <= 0 {
			writeError(w, http.StatusBadRequest, "insert must be a 1-based line number >= 1")
			return
		}
		spec := LineEdit{
			Lines:   q.Get("lines"),
			Head:    atoiDefault(q.Get("head"), 0),
			Tail:    atoiDefault(q.Get("tail"), 0),
			Insert:  atoiDefault(q.Get("insert"), 0),
			Prepend: isTrue(q.Get("prepend")),
		}
		res, lerr := a.store.PatchLines(r.URL.Path, spec, body, ifMatch)
		if lerr != nil {
			writeStoreError(w, lerr)
			return
		}
		setETag(w, res.ETag)
		writeJSON(w, http.StatusOK, map[string]any{
			"path":    cleanURLPath(r.URL.Path),
			"patched": true,
			"size":    res.Size,
			"etag":    res.ETag,
		})
		return
	}

	// Mode 4 (default): append to the file.
	res, aerr := a.store.AppendCond(r.URL.Path, body, ifMatch)
	if aerr != nil {
		writeStoreError(w, aerr)
		return
	}
	setETag(w, res.ETag)
	writeJSON(w, http.StatusOK, map[string]any{
		"path":     cleanURLPath(r.URL.Path),
		"appended": len(body),
		"size":     res.Size,
		"etag":     res.ETag,
	})
}

func (a *App) handleDelete(w http.ResponseWriter, r *http.Request) {
	recursive := isTrue(r.URL.Query().Get("recursive"))
	ifMatch := strings.TrimSpace(r.Header.Get("If-Match"))
	if err := a.store.DeleteCond(r.URL.Path, recursive, ifMatch); err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"path":    cleanURLPath(r.URL.Path),
		"deleted": true,
	})
}

func (a *App) readBody(w http.ResponseWriter, r *http.Request) ([]byte, error) {
	r.Body = http.MaxBytesReader(w, r.Body, a.cfg.MaxBodyBytes)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, http.StatusRequestEntityTooLarge, "request body too large or unreadable")
		return nil, err
	}
	return body, nil
}

// --- helpers ---------------------------------------------------------------

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{"error": msg, "status": status})
}

func setETag(w http.ResponseWriter, etag string) {
	if etag != "" {
		w.Header().Set("ETag", etag)
	}
}

func writeStoreError(w http.ResponseWriter, err error) {
	var ve *ValidationError
	switch {
	case errors.As(err, &ve):
		writeError(w, http.StatusBadRequest, ve.Error())
	case errors.Is(err, ErrPreconditionFailed):
		writeError(w, http.StatusPreconditionFailed, "if-match precondition failed (file was modified)")
	case errors.Is(err, ErrNotFound):
		writeError(w, http.StatusNotFound, "not found")
	case errors.Is(err, ErrForbidden):
		writeError(w, http.StatusForbidden, "path not allowed")
	case errors.Is(err, ErrNotMarkdown):
		writeError(w, http.StatusBadRequest, "only .md files are supported")
	case errors.Is(err, ErrAlreadyExists):
		writeError(w, http.StatusConflict, "resource already exists (use PUT to overwrite)")
	case errors.Is(err, ErrIsDirectory):
		writeError(w, http.StatusBadRequest, "path is a directory")
	case errors.Is(err, ErrNotDirectory):
		writeError(w, http.StatusBadRequest, "path is not a directory")
	case errors.Is(err, ErrDirNotEmpty):
		writeError(w, http.StatusConflict, "directory not empty (use ?recursive=true)")
	default:
		writeError(w, http.StatusInternalServerError, "internal error")
	}
}

func isTrue(v string) bool {
	switch strings.ToLower(v) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

func atoiDefault(s string, def int) int {
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 {
		return def
	}
	return n
}

func prefersJSON(r *http.Request) bool {
	return strings.Contains(r.Header.Get("Accept"), "application/json")
}

func prefersMarkdown(r *http.Request) bool {
	accept := r.Header.Get("Accept")
	return strings.Contains(accept, "text/markdown") || strings.Contains(accept, "text/plain")
}

func renderMarkdownIndex(dir string, entries []Entry) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Index of %s\n\n", dir)
	if len(entries) == 0 {
		b.WriteString("_empty_\n")
		return b.String()
	}
	for _, e := range entries {
		if e.Type == "dir" {
			fmt.Fprintf(&b, "- 📁 [%s](%s/)\n", e.Name, e.Path)
			continue
		}
		title := e.Title
		if title == "" {
			title = e.Name
		}
		fmt.Fprintf(&b, "- 📄 [%s](%s)", title, e.Path)
		if len(e.Tags) > 0 {
			fmt.Fprintf(&b, " — _%s_", strings.Join(e.Tags, ", "))
		}
		b.WriteString("\n")
	}
	return b.String()
}
