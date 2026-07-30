package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testVault(t *testing.T) *Vault {
	t.Helper()
	dir := t.TempDir()
	root := filepath.Join(dir, "default")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := LayoutWikiRaw.apply(root, "default"); err != nil {
		t.Fatal(err)
	}
	v := &Vault{Name: "default", Root: root}
	idx, err := OpenIndex(filepath.Join(root, indexFile))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(idx.Close)
	v.idx = idx
	if err := idx.Reconcile(v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestResolveRefusesEscape(t *testing.T) {
	v := testVault(t)
	bad := []string{
		"../outside.md",
		"wiki/../../outside.md",
		"/etc/passwd",
		".secondbrain/index.db",
		"wiki/.hidden/note.md",
		".git/config",
		"",
		"..",
		"./../x",
		"wiki/\x00evil.md",
	}
	for _, p := range bad {
		if _, err := v.Resolve(p); err == nil {
			t.Errorf("Resolve(%q) was accepted, it must not be", p)
		}
	}
	good := []string{"wiki/topic.md", "a/b/c.md", "note.md", "./wiki/topic.md"}
	for _, p := range good {
		abs, err := v.Resolve(p)
		if err != nil {
			t.Errorf("Resolve(%q) failed: %v", p, err)
			continue
		}
		if !strings.HasPrefix(abs, v.Root) {
			t.Errorf("Resolve(%q) = %q, which is outside the vault", p, abs)
		}
	}
}

func TestResolveNoteAddsExtension(t *testing.T) {
	v := testVault(t)
	_, rel, err := v.ResolveNote("wiki/topic")
	if err != nil {
		t.Fatal(err)
	}
	if rel != "wiki/topic.md" {
		t.Fatalf("got %q, want wiki/topic.md", rel)
	}
}

func TestVaultNamePattern(t *testing.T) {
	ok := []string{"default", "work", "a", "my-vault_2"}
	bad := []string{"", "..", "../x", "Work", "with space", "a/b", ".hidden", strings.Repeat("x", 65)}
	for _, n := range ok {
		if !vaultNameRe.MatchString(n) {
			t.Errorf("%q should be a valid vault name", n)
		}
	}
	for _, n := range bad {
		if vaultNameRe.MatchString(n) {
			t.Errorf("%q should not be a valid vault name", n)
		}
	}
}
