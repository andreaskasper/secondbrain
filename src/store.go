package main

import (
	"errors"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Sentinel errors returned by the store and mapped to HTTP statuses by the handlers.
var (
	ErrForbidden     = errors.New("path escapes data directory")
	ErrNotMarkdown   = errors.New("only .md files are supported")
	ErrNotFound      = errors.New("not found")
	ErrAlreadyExists = errors.New("already exists")
	ErrIsDirectory   = errors.New("path is a directory")
	ErrNotDirectory  = errors.New("path is not a directory")
	ErrDirNotEmpty   = errors.New("directory not empty")
)

const mdExt = ".md"

// Store is a thin, path-safe wrapper around a directory tree of markdown files.
type Store struct {
	Root string
	// mu serialises read-modify-write operations (partial edits, conditional
	// writes/deletes) so that concurrent requests cannot corrupt a file or
	// race past an If-Match precondition.
	mu sync.Mutex
}

// NewStore creates the data directory if needed and returns a Store rooted there.
func NewStore(root string) (*Store, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return nil, err
	}
	return &Store{Root: abs}, nil
}

// Entry describes a single item in a directory listing.
type Entry struct {
	Name     string    `json:"name"`
	Path     string    `json:"path"`
	Type     string    `json:"type"` // "file" or "dir"
	Size     int64     `json:"size,omitempty"`
	Modified time.Time `json:"modified"`
	Title    string    `json:"title,omitempty"`
	Tags     []string  `json:"tags,omitempty"`
}

// cleanURLPath normalises an incoming URL path into an absolute, dot-free path
// rooted at "/". Any attempt to escape upward is neutralised by path.Clean.
func cleanURLPath(urlPath string) string {
	if urlPath == "" {
		urlPath = "/"
	}
	return path.Clean("/" + strings.TrimPrefix(urlPath, "/"))
}

// resolve maps a URL path to an absolute filesystem path, guaranteeing the
// result stays within the store root (defence in depth on top of path.Clean).
func (s *Store) resolve(urlPath string) (string, error) {
	clean := cleanURLPath(urlPath)
	full := filepath.Join(s.Root, filepath.FromSlash(clean))

	rel, err := filepath.Rel(s.Root, full)
	if err != nil {
		return "", ErrForbidden
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", ErrForbidden
	}
	return full, nil
}

// Stat returns file info for a URL path along with its resolved disk path.
func (s *Store) Stat(urlPath string) (os.FileInfo, string, error) {
	full, err := s.resolve(urlPath)
	if err != nil {
		return nil, "", err
	}
	info, err := os.Stat(full)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, full, ErrNotFound
		}
		return nil, full, err
	}
	return info, full, nil
}

// ReadFile reads a markdown file's raw bytes.
func (s *Store) ReadFile(urlPath string) ([]byte, error) {
	full, err := s.resolve(urlPath)
	if err != nil {
		return nil, err
	}
	if !strings.EqualFold(filepath.Ext(full), mdExt) {
		return nil, ErrNotMarkdown
	}
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
	return os.ReadFile(full)
}

// List returns the entries of a directory. When recursive is true the whole
// subtree is returned (depth-first, sorted). When withMeta is true each .md
// entry is annotated with its frontmatter title/tags.
func (s *Store) List(urlPath string, recursive, withMeta bool) ([]Entry, error) {
	full, err := s.resolve(urlPath)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(full)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if !info.IsDir() {
		return nil, ErrNotDirectory
	}

	var entries []Entry

	add := func(diskPath string, fi os.FileInfo) {
		name := fi.Name()
		if strings.HasPrefix(name, ".") {
			return // hide dotfiles
		}
		rel, relErr := filepath.Rel(s.Root, diskPath)
		if relErr != nil {
			return
		}
		urlp := "/" + filepath.ToSlash(rel)
		if fi.IsDir() {
			entries = append(entries, Entry{
				Name:     name,
				Path:     urlp,
				Type:     "dir",
				Modified: fi.ModTime(),
			})
			return
		}
		if !strings.EqualFold(filepath.Ext(name), mdExt) {
			return // only surface markdown files
		}
		e := Entry{
			Name:     name,
			Path:     urlp,
			Type:     "file",
			Size:     fi.Size(),
			Modified: fi.ModTime(),
		}
		if withMeta {
			if data, rerr := os.ReadFile(diskPath); rerr == nil {
				fm, _ := splitFrontmatter(data)
				e.Title = fm.Title()
				e.Tags = fm.Tags()
			}
		}
		entries = append(entries, e)
	}

	if recursive {
		err = filepath.Walk(full, func(p string, fi os.FileInfo, werr error) error {
			if werr != nil {
				return werr
			}
			if p == full {
				return nil // skip the directory itself
			}
			if fi.IsDir() && strings.HasPrefix(fi.Name(), ".") {
				return filepath.SkipDir
			}
			add(p, fi)
			return nil
		})
		if err != nil {
			return nil, err
		}
	} else {
		dirEntries, derr := os.ReadDir(full)
		if derr != nil {
			return nil, derr
		}
		for _, de := range dirEntries {
			fi, ierr := de.Info()
			if ierr != nil {
				continue
			}
			add(filepath.Join(full, de.Name()), fi)
		}
	}

	sort.Slice(entries, func(i, j int) bool {
		// Directories first, then alphabetical by path.
		if (entries[i].Type == "dir") != (entries[j].Type == "dir") {
			return entries[i].Type == "dir"
		}
		return entries[i].Path < entries[j].Path
	})
	return entries, nil
}

// Write creates or overwrites a markdown file. When overwrite is false and the
// file already exists, ErrAlreadyExists is returned. Parent directories are
// created automatically.
//
// Deprecated: use WriteCond, which also supports If-Match preconditions and
// shares the store write lock. Kept for backwards compatibility.
func (s *Store) Write(urlPath string, content []byte, overwrite bool) (created bool, err error) {
	created, _, err = s.WriteCond(urlPath, content, overwrite, "")
	return created, err
}

// Delete removes a file, or a directory (only when recursive is true or the
// directory is empty).
func (s *Store) Delete(urlPath string, recursive bool) error {
	return s.DeleteCond(urlPath, recursive, "")
}
