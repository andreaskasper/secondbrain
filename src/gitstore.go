package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/transport/http"
)

// ---------------------------------------------------------------------------
// Versioning
//
// Every vault is a git repository and every mutation is a commit. That is a
// larger claim than it sounds: it means no edit this program makes is ever
// unrecoverable, that "what did it change last Tuesday" is answerable, and
// that the safety net is a format the operator already knows rather than a
// proprietary undo log.
//
// go-git is used rather than shelling out, so the runtime image stays
// distroless - there is no git binary in the container and there does not need
// to be one.
// ---------------------------------------------------------------------------

type GitStore struct {
	repo   *git.Repository
	root   string
	cfg    *Config
	mu     sync.Mutex
	remote bool
}

func OpenGitStore(root string, cfg *Config) (*GitStore, error) {
	repo, err := git.PlainOpen(root)
	if errors.Is(err, git.ErrRepositoryNotExists) {
		repo, err = git.PlainInit(root, false)
		if err != nil {
			return nil, err
		}
		if err := writeGitignore(root); err != nil {
			return nil, err
		}
	}
	if err != nil {
		return nil, err
	}
	g := &GitStore{repo: repo, root: root, cfg: cfg}
	if cfg.GitRemote != "" {
		if err := g.ensureRemote(cfg.GitRemote); err != nil {
			logWarn("git_remote_failed", map[string]any{"error": err.Error()})
		} else {
			g.remote = true
		}
	}
	return g, nil
}

// writeGitignore keeps the index and the trash out of history. Committing a
// SQLite database on every note change would make the repository enormous and
// every diff useless.
func writeGitignore(root string) error {
	body := "# secondbrain internals: derived data, never source of truth\n" +
		".secondbrain/\n.obsidian/workspace*.json\n.DS_Store\n"
	return os.WriteFile(filepath.Join(root, ".gitignore"), []byte(body), 0o644)
}

func (g *GitStore) ensureRemote(url string) error {
	_, err := g.repo.Remote("origin")
	if err == nil {
		return nil
	}
	_, err = g.repo.CreateRemote(&config.RemoteConfig{Name: "origin", URLs: []string{url}})
	return err
}

// Commit stages everything and records it. An empty commit is skipped rather
// than failing, because "the tool ran and changed nothing" is a normal outcome
// of a dry run turned real.
func (g *GitStore) Commit(message string) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	wt, err := g.repo.Worktree()
	if err != nil {
		return err
	}
	st, err := wt.Status()
	if err != nil {
		return err
	}
	if st.IsClean() {
		return nil
	}
	if err := wt.AddWithOptions(&git.AddOptions{All: true}); err != nil {
		return err
	}
	sig := &object.Signature{
		Name:  firstNonEmpty(g.cfg.GitAuthor, "secondbrain"),
		Email: firstNonEmpty(g.cfg.GitEmail, "secondbrain@localhost"),
		When:  time.Now(),
	}
	if _, err := wt.Commit(trimMessage(message), &git.CommitOptions{Author: sig, Committer: sig}); err != nil {
		return err
	}
	if g.remote {
		go g.push()
	}
	return nil
}

func (g *GitStore) push() {
	opts := &git.PushOptions{RemoteName: "origin"}
	if g.cfg.GitToken != "" {
		opts.Auth = &http.BasicAuth{Username: "secondbrain", Password: g.cfg.GitToken}
	}
	err := g.repo.Push(opts)
	switch {
	case err == nil, errors.Is(err, git.NoErrAlreadyUpToDate):
	default:
		logWarn("git_push_failed", map[string]any{"error": err.Error()})
	}
}

type Revision struct {
	Commit  string `json:"commit"`
	When    string `json:"when"`
	Author  string `json:"author"`
	Message string `json:"message"`
}

// History lists the commits that touched one path.
func (g *GitStore) History(rel string, limit int) ([]Revision, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	head, err := g.repo.Head()
	if err != nil {
		return nil, fmt.Errorf("no history yet")
	}
	iter, err := g.repo.Log(&git.LogOptions{From: head.Hash(), FileName: &rel})
	if err != nil {
		return nil, err
	}
	defer iter.Close()
	var out []Revision
	err = iter.ForEach(func(c *object.Commit) error {
		out = append(out, Revision{
			Commit:  c.Hash.String()[:12],
			When:    c.Author.When.UTC().Format(time.RFC3339),
			Author:  c.Author.Name,
			Message: strings.SplitN(c.Message, "\n", 2)[0],
		})
		if len(out) >= limit {
			return errStorerStop
		}
		return nil
	})
	if err != nil && !errors.Is(err, errStorerStop) {
		return nil, err
	}
	return out, nil
}

var errStorerStop = errors.New("stop")

// Contents returns a path as of a commit, which is what both note_diff and
// note_restore are built on.
func (g *GitStore) Contents(rel, rev string) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	h, err := g.resolveRev(rev)
	if err != nil {
		return "", err
	}
	c, err := g.repo.CommitObject(h)
	if err != nil {
		return "", err
	}
	f, err := c.File(rel)
	if err != nil {
		return "", fmt.Errorf("%s does not exist in %s", rel, rev)
	}
	return f.Contents()
}

func (g *GitStore) resolveRev(rev string) (plumbing.Hash, error) {
	if rev == "" || rev == "HEAD" {
		ref, err := g.repo.Head()
		if err != nil {
			return plumbing.ZeroHash, fmt.Errorf("no commits yet")
		}
		return ref.Hash(), nil
	}
	h, err := g.repo.ResolveRevision(plumbing.Revision(rev))
	if err != nil {
		return plumbing.ZeroHash, fmt.Errorf("unknown revision %q", rev)
	}
	return *h, nil
}

// trimMessage keeps commit subjects to one readable line.
func trimMessage(m string) string {
	m = strings.TrimSpace(strings.ReplaceAll(m, "\n", " "))
	if m == "" {
		m = "secondbrain: update"
	}
	if len(m) > 140 {
		m = m[:137] + "..."
	}
	return m
}
