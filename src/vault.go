package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// Vaults
//
// A vault is a directory under the data root and nothing more. Everything the
// server keeps for itself - the search index, the trash, the per-vault
// instructions - lives in <vault>/.secondbrain/, and that directory is
// unreachable through every tool by the same rule that makes traversal
// impossible: no path component may begin with a dot.
//
// That single rule also covers .git and .obsidian, which is the point. A vault
// you can open in Obsidian is a vault whose internals an agent should not be
// rummaging around in.
// ---------------------------------------------------------------------------

const (
	internalDir = ".secondbrain"
	trashDir    = ".secondbrain/trash"
	indexFile   = ".secondbrain/index.db"
	instrFile   = ".secondbrain/instructions.md"
	noteExt     = ".md"
)

var (
	errNotFound     = errors.New("note not found")
	errExists       = errors.New("a note already exists at that path")
	errBadPath      = errors.New("invalid path")
	errUnknownVault = errors.New("unknown vault")
	errReadOnly     = errors.New("this user may not modify the vault")
	errStale        = errors.New("the note changed since it was read")
)

// attachmentExts is the allow-list for non-Markdown files. Anything else
// cannot be written through the API - a knowledge base is not a file share,
// and an agent that can drop arbitrary bytes into a mounted volume is a
// larger problem than the convenience is worth.
var attachmentExts = map[string]bool{
	".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".webp": true,
	".svg": true, ".pdf": true, ".txt": true, ".csv": true, ".json": true,
	".yaml": true, ".yml": true, ".mp3": true, ".m4a": true, ".wav": true,
}

type Vault struct {
	Name string
	Root string

	idx *Index
	git *GitStore

	// writeMu serialises mutations within a vault. Reads are unaffected;
	// the cost of a global write lock per vault is nothing next to the cost
	// of two tool calls interleaving inside one file.
	writeMu sync.Mutex
}

type VaultManager struct {
	root         string
	defaultVault string
	cfg          *Config

	mu     sync.RWMutex
	vaults map[string]*Vault
}

func NewVaultManager(cfg *Config) (*VaultManager, error) {
	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		return nil, fmt.Errorf("create data dir %s: %w", cfg.DataDir, err)
	}
	m := &VaultManager{
		root:         cfg.DataDir,
		defaultVault: cfg.DefaultVault,
		cfg:          cfg,
		vaults:       map[string]*Vault{},
	}
	names, err := m.scan()
	if err != nil {
		return nil, err
	}
	for _, n := range names {
		if _, err := m.open(n); err != nil {
			return nil, err
		}
	}
	// A brand new deployment gets its default vault so that the first tool
	// call has somewhere to go.
	if len(names) == 0 {
		if _, err := m.Create(m.defaultVault, LayoutWikiRaw); err != nil {
			return nil, err
		}
	}
	return m, nil
}

// scan lists the directories under the data root that look like vaults.
func (m *VaultManager) scan() ([]string, error) {
	entries, err := os.ReadDir(m.root)
	if err != nil {
		return nil, fmt.Errorf("read data dir: %w", err)
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() || !vaultNameRe.MatchString(e.Name()) {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names, nil
}

func (m *VaultManager) open(name string) (*Vault, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if v, ok := m.vaults[name]; ok {
		return v, nil
	}
	v := &Vault{Name: name, Root: filepath.Join(m.root, name)}
	if err := os.MkdirAll(filepath.Join(v.Root, internalDir), 0o755); err != nil {
		return nil, err
	}
	idx, err := OpenIndex(filepath.Join(v.Root, indexFile))
	if err != nil {
		return nil, fmt.Errorf("vault %s: open index: %w", name, err)
	}
	v.idx = idx
	if m.cfg.Git {
		g, err := OpenGitStore(v.Root, m.cfg)
		if err != nil {
			// Git is a convenience, not a precondition. A vault that cannot
			// be versioned is still a usable vault, and refusing to start
			// over it would be the wrong trade.
			logWarn("git_unavailable", map[string]any{"vault": name, "error": err.Error()})
		} else {
			v.git = g
		}
	}
	m.vaults[name] = v
	return v, nil
}

// Get resolves a vault name, applying the default and the user's allow-list.
func (m *VaultManager) Get(u *User, name string) (*Vault, error) {
	if name == "" {
		name = m.defaultVault
	}
	if !vaultNameRe.MatchString(name) {
		return nil, fmt.Errorf("%w: vault names must match %s", errUnknownVault, vaultNameRe)
	}
	if u != nil && !u.CanUseVault(name) {
		// Deliberately the same error as a missing vault: whether a vault
		// the user may not touch exists is not their business.
		return nil, fmt.Errorf("%w: %s", errUnknownVault, name)
	}
	m.mu.RLock()
	v, ok := m.vaults[name]
	m.mu.RUnlock()
	if ok {
		return v, nil
	}
	if _, err := os.Stat(filepath.Join(m.root, name)); err != nil {
		return nil, fmt.Errorf("%w: %s", errUnknownVault, name)
	}
	return m.open(name)
}

// List returns the vaults visible to a user, in name order.
func (m *VaultManager) List(u *User) []*Vault {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]*Vault, 0, len(m.vaults))
	for _, v := range m.vaults {
		if u == nil || u.CanUseVault(v.Name) {
			out = append(out, v)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (m *VaultManager) Create(name string, layout Layout) (*Vault, error) {
	if !vaultNameRe.MatchString(name) {
		return nil, fmt.Errorf("%w: vault names must match %s", errBadPath, vaultNameRe)
	}
	dir := filepath.Join(m.root, name)
	if _, err := os.Stat(dir); err == nil {
		return nil, fmt.Errorf("vault %s already exists", name)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	if err := layout.apply(dir, name); err != nil {
		return nil, err
	}
	v, err := m.open(name)
	if err != nil {
		return nil, err
	}
	if err := v.idx.Reconcile(v); err != nil {
		return nil, err
	}
	if v.git != nil {
		_ = v.git.Commit("vault_create: initialise " + name)
	}
	logInfo("vault_created", map[string]any{"vault": name, "layout": string(layout)})
	return v, nil
}

func (m *VaultManager) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, v := range m.vaults {
		if v.idx != nil {
			v.idx.Close()
		}
	}
}

// ---------------------------------------------------------------------------
// Path resolution
// ---------------------------------------------------------------------------

// Resolve turns a vault-relative path into an absolute one, or refuses.
//
// The check is structural rather than a blacklist: after cleaning, the path
// must be relative, must stay inside the vault, and no component may start
// with a dot. There is no pattern to outsmart here, only an invariant.
func (v *Vault) Resolve(rel string) (string, error) {
	rel = strings.TrimSpace(rel)
	if rel == "" {
		return "", fmt.Errorf("%w: path is empty", errBadPath)
	}
	if strings.ContainsRune(rel, 0) {
		return "", fmt.Errorf("%w: path contains a null byte", errBadPath)
	}
	rel = strings.ReplaceAll(rel, "\\", "/")
	rel = strings.TrimPrefix(rel, "./")
	if path.IsAbs(rel) {
		return "", fmt.Errorf("%w: path must be relative to the vault", errBadPath)
	}
	clean := path.Clean(rel)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("%w: path escapes the vault", errBadPath)
	}
	for _, seg := range strings.Split(clean, "/") {
		if seg == "" {
			return "", fmt.Errorf("%w: path contains an empty segment", errBadPath)
		}
		if strings.HasPrefix(seg, ".") {
			return "", fmt.Errorf("%w: %q is a hidden path and is not reachable through the API", errBadPath, clean)
		}
	}
	abs := filepath.Join(v.Root, filepath.FromSlash(clean))

	// Belt and braces: even with the checks above, verify the result really
	// is under the vault root. A symlinked directory inside the vault would
	// otherwise be a way out.
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		root, rerr := filepath.EvalSymlinks(v.Root)
		if rerr == nil && !strings.HasPrefix(real+string(os.PathSeparator), root+string(os.PathSeparator)) && real != root {
			return "", fmt.Errorf("%w: path resolves outside the vault", errBadPath)
		}
	}
	return abs, nil
}

// ResolveNote is Resolve with the .md suffix supplied when it is missing, so
// that "wiki/topic" and "wiki/topic.md" address the same note.
func (v *Vault) ResolveNote(rel string) (string, string, error) {
	rel = strings.TrimSpace(strings.ReplaceAll(rel, "\\", "/"))
	if rel != "" && !strings.HasSuffix(strings.ToLower(rel), noteExt) {
		rel += noteExt
	}
	abs, err := v.Resolve(rel)
	if err != nil {
		return "", "", err
	}
	return abs, path.Clean(strings.TrimPrefix(rel, "./")), nil
}

// Rel turns an absolute path back into the vault-relative slash form used
// everywhere in the API.
func (v *Vault) Rel(abs string) string {
	r, err := filepath.Rel(v.Root, abs)
	if err != nil {
		return abs
	}
	return filepath.ToSlash(r)
}

// ---------------------------------------------------------------------------
// Walking
// ---------------------------------------------------------------------------

type FileEntry struct {
	Path     string    `json:"path"`
	Bytes    int64     `json:"bytes"`
	Modified time.Time `json:"modified"`
	IsDir    bool      `json:"is_dir"`
}

// Walk visits every non-hidden file in the vault. Hidden directories are
// skipped wholesale, which is what keeps .git and .secondbrain out of the
// index without a special case per directory.
func (v *Vault) Walk(fn func(rel string, d fs.DirEntry) error) error {
	return filepath.WalkDir(v.Root, func(abs string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel := v.Rel(abs)
		if rel == "." {
			return nil
		}
		base := filepath.Base(abs)
		if strings.HasPrefix(base, ".") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		return fn(rel, d)
	})
}

// IsNote reports whether a vault-relative path names a Markdown note.
func IsNote(rel string) bool { return strings.EqualFold(filepath.Ext(rel), noteExt) }

// InstructionsPath is where a vault keeps the conventions it teaches an agent
// at connect time.
func (v *Vault) InstructionsPath() string { return filepath.Join(v.Root, instrFile) }

func (v *Vault) Instructions() string {
	b, err := os.ReadFile(v.InstructionsPath())
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}
