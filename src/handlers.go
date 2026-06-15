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
	created, werr := a.store.Write(r.URL.Path, body, overwrite)
	if werr != nil {
		writeStoreError(w, werr)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, map[string]any{
		"path":    cleanURLPath(r.URL.Path),
		"created": created,
		"size":    len(body),
	})
}

func (a *App) handlePatch(w http.ResponseWriter, r *http.Request) {
	body, err := a.readBody(w, r)
	if err != nil {
		return
	}
	if aerr := a.store.Append(r.URL.Path, body); aerr != nil {
		writeStoreError(w, aerr)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"path":     cleanURLPath(r.URL.Path),
		"appended": len(body),
	})
}

func (a *App) handleDelete(w http.ResponseWriter, r *http.Request) {
	recursive := isTrue(r.URL.Query().Get("recursive"))
	if err := a.store.Delete(r.URL.Path, recursive); err != nil {
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

func writeStoreError(w http.ResponseWriter, err error) {
	switch {
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
