package main

import (
	"errors"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
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
func (s *Store) Write(urlPath string, content []byte, overwrite bool) (created bool, err error) {
	full, err := s.resolve(urlPath)
	if err != nil {
		return false, err
	}
	if !strings.EqualFold(filepath.Ext(full), mdExt) {
		return false, ErrNotMarkdown
	}

	existed := false
	if info, statErr := os.Stat(full); statErr == nil {
		if info.IsDir() {
			return false, ErrIsDirectory
		}
		existed = true
		if !overwrite {
			return false, ErrAlreadyExists
		}
	}

	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return false, err
	}
	if err := os.WriteFile(full, content, 0o644); err != nil {
		return false, err
	}
	return !existed, nil
}

// Append appends content to an existing markdown file, inserting a newline
// separator if the existing content does not already end with one.
func (s *Store) Append(urlPath string, content []byte) error {
	full, err := s.resolve(urlPath)
	if err != nil {
		return err
	}
	if !strings.EqualFold(filepath.Ext(full), mdExt) {
		return ErrNotMarkdown
	}
	info, err := os.Stat(full)
	if err != nil {
		if os.IsNotExist(err) {
			return ErrNotFound
		}
		return err
	}
	if info.IsDir() {
		return ErrIsDirectory
	}

	f, err := os.OpenFile(full, os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()

	if info.Size() > 0 {
		if _, err := f.WriteString("\n"); err != nil {
			return err
		}
	}
	_, err = f.Write(content)
	return err
}

// Delete removes a file, or a directory (only when recursive is true or the
// directory is empty).
func (s *Store) Delete(urlPath string, recursive bool) error {
	full, err := s.resolve(urlPath)
	if err != nil {
		return err
	}
	if full == s.Root {
		return ErrForbidden // never delete the root
	}
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
		// Refuse to delete a non-empty directory without recursive=true.
		entries, rerr := os.ReadDir(full)
		if rerr != nil {
			return rerr
		}
		if len(entries) > 0 {
			return ErrDirNotEmpty
		}
		return os.Remove(full)
	}
	return os.Remove(full)
}
