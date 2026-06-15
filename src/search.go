package main

import (
	"bufio"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// SearchMatch is a single hit produced by a content search.
type SearchMatch struct {
	Path    string `json:"path"`    // URL path of the file
	Line    int    `json:"line"`    // 1-based line number
	Content string `json:"content"` // the matching line (trimmed)
}

// SearchResult is the response payload for a search query.
type SearchResult struct {
	Query     string        `json:"query"`
	Regex     bool          `json:"regex"`
	Count     int           `json:"count"`
	Truncated bool          `json:"truncated"`
	Matches   []SearchMatch `json:"matches"`
}

// SearchOptions configures a content search.
type SearchOptions struct {
	Query         string
	UseRegex      bool
	CaseSensitive bool
	Limit         int
}

// Search walks the subtree rooted at urlPath and returns lines matching the
// query across all markdown files. Frontmatter is included in the search.
func (s *Store) Search(urlPath string, opts SearchOptions) (*SearchResult, error) {
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

	if opts.Limit <= 0 {
		opts.Limit = 200
	}

	var matcher func(string) bool
	if opts.UseRegex {
		pattern := opts.Query
		if !opts.CaseSensitive {
			pattern = "(?i)" + pattern
		}
		re, cerr := regexp.Compile(pattern)
		if cerr != nil {
			return nil, cerr
		}
		matcher = re.MatchString
	} else {
		needle := opts.Query
		if !opts.CaseSensitive {
			needle = strings.ToLower(needle)
			matcher = func(line string) bool {
				return strings.Contains(strings.ToLower(line), needle)
			}
		} else {
			matcher = func(line string) bool {
				return strings.Contains(line, needle)
			}
		}
	}

	result := &SearchResult{Query: opts.Query, Regex: opts.UseRegex}

	searchFile := func(diskPath string) error {
		rel, rerr := filepath.Rel(s.Root, diskPath)
		if rerr != nil {
			return nil
		}
		urlp := "/" + filepath.ToSlash(rel)

		f, oerr := os.Open(diskPath)
		if oerr != nil {
			return nil // skip unreadable files
		}
		defer f.Close()

		scanner := bufio.NewScanner(f)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		lineNo := 0
		for scanner.Scan() {
			lineNo++
			line := scanner.Text()
			if matcher(line) {
				if len(result.Matches) >= opts.Limit {
					result.Truncated = true
					return nil
				}
				result.Matches = append(result.Matches, SearchMatch{
					Path:    urlp,
					Line:    lineNo,
					Content: strings.TrimSpace(line),
				})
			}
		}
		return nil
	}

	if info.IsDir() {
		walkErr := filepath.Walk(full, func(p string, fi os.FileInfo, werr error) error {
			if werr != nil {
				return werr
			}
			if fi.IsDir() {
				if strings.HasPrefix(fi.Name(), ".") && p != full {
					return filepath.SkipDir
				}
				return nil
			}
			if strings.HasPrefix(fi.Name(), ".") || !strings.EqualFold(filepath.Ext(fi.Name()), mdExt) {
				return nil
			}
			if result.Truncated {
				return filepath.SkipDir
			}
			return searchFile(p)
		})
		if walkErr != nil {
			return nil, walkErr
		}
	} else {
		if !strings.EqualFold(filepath.Ext(full), mdExt) {
			return nil, ErrNotMarkdown
		}
		if err := searchFile(full); err != nil {
			return nil, err
		}
	}

	result.Count = len(result.Matches)
	return result, nil
}

// applyLineModifiers slices markdown content according to the query
// modifiers: grep (filter), lines (range a-b), head (first N), tail (last N).
// They are applied in that order so they compose sensibly.
func applyLineModifiers(content []byte, grep string, lineRange string, head, tail int) ([]byte, error) {
	lines := splitLines(content)

	if grep != "" {
		re, err := regexp.Compile("(?i)" + grep)
		if err != nil {
			return nil, err
		}
		filtered := lines[:0:0]
		for _, l := range lines {
			if re.MatchString(l) {
				filtered = append(filtered, l)
			}
		}
		lines = filtered
	}

	if lineRange != "" {
		from, to, ok := parseRange(lineRange, len(lines))
		if ok {
			lines = lines[from:to]
		}
	}

	if head > 0 && head < len(lines) {
		lines = lines[:head]
	}
	if tail > 0 && tail < len(lines) {
		lines = lines[len(lines)-tail:]
	}

	return []byte(strings.Join(lines, "\n")), nil
}

func splitLines(content []byte) []string {
	s := strings.ReplaceAll(string(content), "\r\n", "\n")
	s = strings.TrimSuffix(s, "\n")
	if s == "" {
		return []string{}
	}
	return strings.Split(s, "\n")
}

// parseRange parses "a-b", "a-" or "-b" (1-based, inclusive) into 0-based
// slice bounds [from,to). Returns ok=false on malformed input.
func parseRange(spec string, total int) (from, to int, ok bool) {
	parts := strings.SplitN(spec, "-", 2)
	from = 0
	to = total
	if len(parts) != 2 {
		return 0, 0, false
	}
	if parts[0] != "" {
		a := atoiSafe(parts[0])
		if a < 1 {
			return 0, 0, false
		}
		from = a - 1
	}
	if parts[1] != "" {
		b := atoiSafe(parts[1])
		if b < 1 {
			return 0, 0, false
		}
		to = b
	}
	if from > total {
		from = total
	}
	if to > total {
		to = total
	}
	if from > to {
		return 0, 0, false
	}
	return from, to, true
}

func atoiSafe(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return -1
		}
		n = n*10 + int(c-'0')
	}
	return n
}
