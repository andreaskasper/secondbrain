package main

import (
	"fmt"
	"sort"
	"strings"
)

// Frontmatter is parsed YAML-ish metadata from the top of a markdown file.
// Only flat key/value pairs and simple string lists are supported, which
// covers the common cases (title, tags, date, author, ...). Scalars become
// strings; sequences become []string.
type Frontmatter map[string]any

// splitFrontmatter separates a leading "--- ... ---" block from the body.
// It returns the parsed frontmatter (nil if none) and the remaining body.
// CRLF and LF line endings are both supported.
func splitFrontmatter(content []byte) (Frontmatter, []byte) {
	text := string(content)
	// Normalise line endings for detection only; we slice off the original bytes.
	normalized := strings.ReplaceAll(text, "\r\n", "\n")

	if !strings.HasPrefix(normalized, "---\n") {
		return nil, content
	}

	rest := normalized[len("---\n"):]
	// Find the closing delimiter: a line that is exactly "---".
	end := -1
	for _, marker := range []string{"\n---\n", "\n---"} {
		if idx := strings.Index(rest, marker); idx != -1 {
			// Ensure the closing "---" terminates the line / file.
			if marker == "\n---" && idx+len(marker) != len(rest) {
				continue
			}
			end = idx
			break
		}
	}
	// Handle the case where the very first frontmatter line is the closer
	// (empty frontmatter block).
	if strings.HasPrefix(rest, "---\n") {
		end = 0
	}
	if end == -1 {
		// No closing delimiter -> treat whole thing as body.
		return nil, content
	}

	fmBlock := rest[:end]
	body := rest[end:]
	body = strings.TrimPrefix(body, "\n")
	body = strings.TrimPrefix(body, "---\n")
	body = strings.TrimPrefix(body, "---")
	body = strings.TrimPrefix(body, "\n")

	fm := parseFlatYAML(fmBlock)
	return fm, []byte(body)
}

// parseFlatYAML parses a small subset of YAML: "key: value" pairs, inline
// lists ("key: [a, b]") and block lists ("key:\n  - a\n  - b").
func parseFlatYAML(block string) Frontmatter {
	fm := Frontmatter{}
	lines := strings.Split(block, "\n")

	for i := 0; i < len(lines); i++ {
		line := lines[i]
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		// Only handle top-level keys (no leading indentation).
		if line != strings.TrimLeft(line, " \t") {
			continue
		}
		colon := strings.Index(trimmed, ":")
		if colon == -1 {
			continue
		}
		key := strings.TrimSpace(trimmed[:colon])
		value := strings.TrimSpace(trimmed[colon+1:])
		if key == "" {
			continue
		}

		switch {
		case value == "":
			// Possibly a block list on the following indented lines.
			var items []string
			for j := i + 1; j < len(lines); j++ {
				next := lines[j]
				nt := strings.TrimSpace(next)
				if nt == "" {
					continue
				}
				if next == strings.TrimLeft(next, " \t") {
					break // dedented -> new key
				}
				if strings.HasPrefix(nt, "- ") {
					items = append(items, unquote(strings.TrimSpace(nt[2:])))
					i = j
				} else {
					break
				}
			}
			if items != nil {
				fm[key] = items
			} else {
				fm[key] = ""
			}
		case strings.HasPrefix(value, "[") && strings.HasSuffix(value, "]"):
			inner := strings.TrimSpace(value[1 : len(value)-1])
			var items []string
			if inner != "" {
				for _, part := range strings.Split(inner, ",") {
					items = append(items, unquote(strings.TrimSpace(part)))
				}
			}
			fm[key] = items
		default:
			fm[key] = unquote(value)
		}
	}
	return fm
}

// serializeFrontmatter renders a Frontmatter map back into a "--- ... ---"
// block followed by the body. Keys are emitted in sorted order for stable,
// diff-friendly output. An empty map yields just the body (no block).
func serializeFrontmatter(fm Frontmatter, body []byte) []byte {
	if len(fm) == 0 {
		return body
	}
	keys := make([]string, 0, len(fm))
	for k := range fm {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var b strings.Builder
	b.WriteString("---\n")
	for _, k := range keys {
		switch v := fm[k].(type) {
		case []string:
			fmt.Fprintf(&b, "%s: [%s]\n", k, strings.Join(v, ", "))
		case string:
			fmt.Fprintf(&b, "%s: %s\n", k, v)
		default:
			fmt.Fprintf(&b, "%s: %v\n", k, v)
		}
	}
	b.WriteString("---\n")
	b.Write(body)
	return []byte(b.String())
}

// isNull reports whether an incoming frontmatter value is an explicit null
// marker ("null" or "~"), used to signal deletion of a key on a merge.
func isNull(v any) bool {
	s, ok := v.(string)
	return ok && (s == "null" || s == "~")
}

func unquote(s string) string {
	if len(s) >= 2 {
		if (s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'') {
			return s[1 : len(s)-1]
		}
	}
	return s
}

// Title returns the "title" field as a string, if present.
func (fm Frontmatter) Title() string {
	if fm == nil {
		return ""
	}
	if s, ok := fm["title"].(string); ok {
		return s
	}
	return ""
}

// Tags returns the "tags" field as a slice, accepting either a list or a
// comma-separated string.
func (fm Frontmatter) Tags() []string {
	if fm == nil {
		return nil
	}
	switch v := fm["tags"].(type) {
	case []string:
		return v
	case string:
		if v == "" {
			return nil
		}
		var out []string
		for _, part := range strings.Split(v, ",") {
			if p := strings.TrimSpace(part); p != "" {
				out = append(out, p)
			}
		}
		return out
	}
	return nil
}
