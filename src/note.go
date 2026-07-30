package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// ---------------------------------------------------------------------------
// Notes
//
// A note is a Markdown file with optional YAML frontmatter, which is to say:
// it is exactly what Obsidian, Logseq, Foam and a plain text editor already
// understand. Everything below is parsing, never a proprietary format - if
// this program is deleted tomorrow the notes are unaffected, which is the
// whole argument for storing knowledge as files.
// ---------------------------------------------------------------------------

type Note struct {
	Path     string    `json:"path"`
	Title    string    `json:"title"`
	Hash     string    `json:"content_hash"`
	Bytes    int       `json:"bytes"`
	Modified time.Time `json:"modified"`

	// front is the parsed frontmatter mapping node, kept as a yaml.Node so
	// that key order and comments survive a round trip. A note the operator
	// wrote by hand should come back out looking the way they left it.
	front *yaml.Node
	// hadFront records whether the file started with a frontmatter block, so
	// that we do not add one to a note that never had it unless asked.
	hadFront bool

	Body string `json:"-"`
	Raw  string `json:"-"`
}

var (
	fenceRe   = regexp.MustCompile("^(\\s{0,3})(```|~~~)")
	headingRe = regexp.MustCompile(`^(#{1,6})\s+(.+?)\s*#*\s*$`)
	wikiRe    = regexp.MustCompile(`\[\[([^\[\]|#]+)(?:#([^\[\]|]+))?(?:\|([^\[\]]*))?\]\]`)
	mdLinkRe  = regexp.MustCompile(`\[([^\]\n]*)\]\(([^)\s]+)(?:\s+"[^"]*")?\)`)
	inlineTag = regexp.MustCompile(`(^|[^\w\x60#/])#([A-Za-z][\w/-]{0,63})`)
	taskRe    = regexp.MustCompile(`^(\s*)[-*+]\s+\[( |x|X)\]\s*(.*)$`)
	slugRe    = regexp.MustCompile(`[^a-z0-9]+`)
)

// ---------------------------------------------------------------------------
// Reading
// ---------------------------------------------------------------------------

func ReadNote(v *Vault, rel string) (*Note, error) {
	abs, clean, err := v.ResolveNote(rel)
	if err != nil {
		return nil, err
	}
	st, err := os.Stat(abs)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%w: %s", errNotFound, clean)
		}
		return nil, err
	}
	if st.IsDir() {
		return nil, fmt.Errorf("%w: %s is a directory", errBadPath, clean)
	}
	if st.Size() > defaultMaxNoteBytes {
		return nil, fmt.Errorf("note %s is %d bytes, larger than the %d byte limit", clean, st.Size(), defaultMaxNoteBytes)
	}
	raw, err := os.ReadFile(abs)
	if err != nil {
		return nil, err
	}
	n := ParseNote(clean, string(raw))
	n.Modified = st.ModTime()
	return n, nil
}

// ParseNote splits raw file contents into frontmatter and body.
func ParseNote(rel, raw string) *Note {
	n := &Note{Path: rel, Raw: raw, Bytes: len(raw), Hash: HashContent(raw)}
	front, body, ok := splitFrontmatter(raw)
	n.Body = body
	n.hadFront = ok
	if ok {
		var doc yaml.Node
		if err := yaml.Unmarshal([]byte(front), &doc); err == nil &&
			len(doc.Content) == 1 && doc.Content[0].Kind == yaml.MappingNode {
			n.front = doc.Content[0]
		}
	}
	n.Title = n.deriveTitle()
	return n
}

// splitFrontmatter returns the YAML block, the body, and whether a block was
// present. Only a block that starts on the very first line counts - a "---"
// further down is a horizontal rule, not metadata.
func splitFrontmatter(raw string) (string, string, bool) {
	s := strings.TrimPrefix(raw, "\ufeff")
	if !strings.HasPrefix(s, "---\n") && !strings.HasPrefix(s, "---\r\n") {
		return "", raw, false
	}
	rest := s[strings.Index(s, "\n")+1:]
	lines := strings.Split(rest, "\n")
	for i, line := range lines {
		t := strings.TrimRight(line, "\r")
		if t == "---" || t == "..." {
			front := strings.Join(lines[:i], "\n")
			body := ""
			if i+1 < len(lines) {
				body = strings.Join(lines[i+1:], "\n")
			}
			return front, strings.TrimPrefix(body, "\n"), true
		}
	}
	// Unterminated block: treat the whole file as body rather than guess.
	return "", raw, false
}

func HashContent(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])[:32]
}

func (n *Note) deriveTitle() string {
	if t := n.FrontString("title"); t != "" {
		return t
	}
	for _, line := range strings.Split(n.Body, "\n") {
		if m := headingRe.FindStringSubmatch(strings.TrimRight(line, "\r")); m != nil && len(m[1]) == 1 {
			return strings.TrimSpace(m[2])
		}
	}
	base := path.Base(n.Path)
	return strings.TrimSuffix(base, path.Ext(base))
}

// ---------------------------------------------------------------------------
// Frontmatter access
// ---------------------------------------------------------------------------

func (n *Note) FrontString(key string) string {
	v := n.frontValue(key)
	if v == nil || v.Kind != yaml.ScalarNode {
		return ""
	}
	return v.Value
}

func (n *Note) frontValue(key string) *yaml.Node {
	if n.front == nil {
		return nil
	}
	for i := 0; i+1 < len(n.front.Content); i += 2 {
		if n.front.Content[i].Value == key {
			return n.front.Content[i+1]
		}
	}
	return nil
}

// FrontList reads a field that may be a YAML sequence or a comma separated
// scalar, because both spellings occur in real vaults.
func (n *Note) FrontList(key string) []string {
	v := n.frontValue(key)
	if v == nil {
		return nil
	}
	switch v.Kind {
	case yaml.SequenceNode:
		out := make([]string, 0, len(v.Content))
		for _, c := range v.Content {
			if s := strings.TrimSpace(c.Value); s != "" {
				out = append(out, s)
			}
		}
		return out
	case yaml.ScalarNode:
		if strings.TrimSpace(v.Value) == "" {
			return nil
		}
		return splitList(v.Value)
	}
	return nil
}

// Frontmatter returns the metadata as a plain map for JSON output.
func (n *Note) Frontmatter() map[string]any {
	out := map[string]any{}
	if n.front == nil {
		return out
	}
	var m map[string]any
	if err := n.front.Decode(&m); err == nil {
		for k, v := range m {
			out[k] = v
		}
	}
	return out
}

func (n *Note) SetFront(key string, value any) error {
	if n.front == nil {
		n.front = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		n.hadFront = true
	}
	var val yaml.Node
	if err := val.Encode(value); err != nil {
		return err
	}
	if val.Kind == yaml.SequenceNode {
		val.Style = yaml.FlowStyle
	}
	for i := 0; i+1 < len(n.front.Content); i += 2 {
		if n.front.Content[i].Value == key {
			n.front.Content[i+1] = &val
			return nil
		}
	}
	n.front.Content = append(n.front.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, &val)
	return nil
}

func (n *Note) DeleteFront(key string) bool {
	if n.front == nil {
		return false
	}
	for i := 0; i+1 < len(n.front.Content); i += 2 {
		if n.front.Content[i].Value == key {
			n.front.Content = append(n.front.Content[:i], n.front.Content[i+2:]...)
			return true
		}
	}
	return false
}

// Render reassembles the note. A note that had no frontmatter and still has
// none comes back byte for byte.
func (n *Note) Render() string {
	if n.front == nil || len(n.front.Content) == 0 {
		if !n.hadFront {
			return n.Body
		}
	}
	if n.front == nil {
		return n.Body
	}
	out, err := yaml.Marshal(n.front)
	if err != nil {
		return n.Body
	}
	return "---\n" + strings.TrimRight(string(out), "\n") + "\n---\n" + n.Body
}

// ---------------------------------------------------------------------------
// Structure
// ---------------------------------------------------------------------------

type Heading struct {
	Level int    `json:"level"`
	Text  string `json:"text"`
	Line  int    `json:"line"` // 1-based, relative to the body
}

// Headings lists the ATX headings in the body, ignoring anything inside a
// fenced code block - a shell prompt starting with # is not a section.
func (n *Note) Headings() []Heading {
	var out []Heading
	inFence := false
	var fence string
	for i, line := range strings.Split(n.Body, "\n") {
		l := strings.TrimRight(line, "\r")
		if m := fenceRe.FindStringSubmatch(l); m != nil {
			if !inFence {
				inFence, fence = true, m[2]
			} else if strings.HasPrefix(strings.TrimSpace(l), fence) {
				inFence = false
			}
			continue
		}
		if inFence {
			continue
		}
		if m := headingRe.FindStringSubmatch(l); m != nil {
			out = append(out, Heading{Level: len(m[1]), Text: strings.TrimSpace(m[2]), Line: i + 1})
		}
	}
	return out
}

type Link struct {
	Target string `json:"target"`
	Anchor string `json:"anchor,omitempty"`
	Alias  string `json:"alias,omitempty"`
	Wiki   bool   `json:"wiki"`
}

// Links extracts outgoing links. External URLs are skipped: this is a map of
// the vault, not of the internet.
func (n *Note) Links() []Link {
	var out []Link
	seen := map[string]bool{}
	body := stripFences(n.Body)
	for _, m := range wikiRe.FindAllStringSubmatch(body, -1) {
		t := strings.TrimSpace(m[1])
		if t == "" {
			continue
		}
		key := "w:" + t + "#" + m[2]
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, Link{Target: t, Anchor: strings.TrimSpace(m[2]), Alias: strings.TrimSpace(m[3]), Wiki: true})
	}
	for _, m := range mdLinkRe.FindAllStringSubmatch(body, -1) {
		t := strings.TrimSpace(m[2])
		if t == "" || strings.Contains(t, "://") || strings.HasPrefix(t, "#") ||
			strings.HasPrefix(t, "mailto:") || strings.HasPrefix(t, "tel:") {
			continue
		}
		anchor := ""
		if i := strings.Index(t, "#"); i >= 0 {
			anchor, t = t[i+1:], t[:i]
		}
		t = decodePercent(t)
		if t == "" || seen["m:"+t] {
			continue
		}
		seen["m:"+t] = true
		out = append(out, Link{Target: t, Anchor: anchor, Alias: strings.TrimSpace(m[1])})
	}
	return out
}

// Tags merges frontmatter tags with inline #tags.
func (n *Note) Tags() []string {
	set := map[string]bool{}
	for _, t := range n.FrontList("tags") {
		set[normaliseTag(t)] = true
	}
	for _, t := range n.FrontList("tag") {
		set[normaliseTag(t)] = true
	}
	for _, m := range inlineTag.FindAllStringSubmatch(stripFences(n.Body), -1) {
		set[normaliseTag(m[2])] = true
	}
	out := make([]string, 0, len(set))
	for t := range set {
		if t != "" {
			out = append(out, t)
		}
	}
	sort.Strings(out)
	return out
}

type Task struct {
	Path   string `json:"path"`
	Line   int    `json:"line"`
	Text   string `json:"text"`
	Done   bool   `json:"done"`
	Indent int    `json:"-"`
}

// Tasks finds GitHub style checkboxes, which is what every Markdown editor
// and every human already uses for a to-do.
func (n *Note) Tasks() []Task {
	var out []Task
	offset := n.bodyLineOffset()
	inFence := false
	var fence string
	for i, line := range strings.Split(n.Body, "\n") {
		l := strings.TrimRight(line, "\r")
		if m := fenceRe.FindStringSubmatch(l); m != nil {
			if !inFence {
				inFence, fence = true, m[2]
			} else if strings.HasPrefix(strings.TrimSpace(l), fence) {
				inFence = false
			}
			continue
		}
		if inFence {
			continue
		}
		if m := taskRe.FindStringSubmatch(l); m != nil {
			text := strings.TrimSpace(m[3])
			if text == "" {
				continue
			}
			out = append(out, Task{
				Path: n.Path, Line: offset + i + 1, Text: text,
				Done: m[2] != " ", Indent: len(m[1]),
			})
		}
	}
	return out
}

// bodyLineOffset is how many lines the frontmatter occupies, so that reported
// line numbers match what an editor shows.
func (n *Note) bodyLineOffset() int {
	if !n.hadFront {
		return 0
	}
	idx := strings.Index(n.Raw, "\n---")
	if idx < 0 {
		return 0
	}
	return strings.Count(n.Raw[:idx+4], "\n") + 1
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func stripFences(body string) string {
	var b strings.Builder
	inFence := false
	var fence string
	for _, line := range strings.Split(body, "\n") {
		l := strings.TrimRight(line, "\r")
		if m := fenceRe.FindStringSubmatch(l); m != nil {
			if !inFence {
				inFence, fence = true, m[2]
			} else if strings.HasPrefix(strings.TrimSpace(l), fence) {
				inFence = false
			}
			b.WriteByte('\n')
			continue
		}
		if inFence {
			b.WriteByte('\n')
			continue
		}
		b.WriteString(l)
		b.WriteByte('\n')
	}
	return b.String()
}

func normaliseTag(t string) string {
	t = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(t), "#"))
	t = strings.Trim(t, "/")
	return strings.ToLower(t)
}

func decodePercent(s string) string {
	if !strings.Contains(s, "%") {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '%' && i+2 < len(s) {
			var v int
			if _, err := fmt.Sscanf(s[i+1:i+3], "%02x", &v); err == nil {
				b.WriteByte(byte(v))
				i += 2
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// Slug turns a title into a filename component.
func Slug(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.NewReplacer("ä", "ae", "ö", "oe", "ü", "ue", "ß", "ss").Replace(s)
	s = slugRe.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if len(s) > 80 {
		s = s[:80]
		s = strings.Trim(s, "-")
	}
	if s == "" {
		s = "note"
	}
	return s
}
