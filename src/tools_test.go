package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testCtx(t *testing.T) *toolCtx {
	t.Helper()
	v := testVault(t)
	cfg := &Config{DefaultVault: "default", MaxResponseBytes: 1 << 20, DataDir: filepath.Dir(v.Root)}
	v.metrics = NewMetrics()
	srv := &Server{metrics: v.metrics, vaults: &VaultManager{
		root: cfg.DataDir, defaultVault: "default", cfg: cfg,
		vaults: map[string]*Vault{"default": v},
	}, stop: make(chan struct{})}
	return &toolCtx{srv: srv, user: &User{Name: "test"}, cfg: cfg, vault: v, args: map[string]any{}}
}

func call(t *testing.T, c *toolCtx, name string, args map[string]any) map[string]any {
	t.Helper()
	tool, ok := registry[name]
	if !ok {
		t.Fatalf("no tool named %s", name)
	}
	c.args = args
	out, err := tool.Handler(c)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	m, ok := out.(map[string]any)
	if !ok {
		t.Fatalf("%s did not return an object", name)
	}
	return m
}

func callErr(t *testing.T, c *toolCtx, name string, args map[string]any) error {
	t.Helper()
	c.args = args
	_, err := registry[name].Handler(c)
	if err == nil {
		t.Fatalf("%s should have failed", name)
	}
	return err
}

func TestCreateSearchEditCycle(t *testing.T) {
	c := testCtx(t)

	call(t, c, "note_create", map[string]any{
		"path":    "wiki/token-bucket.md",
		"title":   "Token bucket",
		"tags":    []any{"infra", "Algorithm"},
		"content": "# Token bucket\n\n## Idea\n\nA bucket refills at a fixed rate.\n\n## Related\n",
	})

	if err := callErr(t, c, "note_create", map[string]any{"path": "wiki/token-bucket.md"}); err == nil ||
		!strings.Contains(err.Error(), "already exists") {
		t.Errorf("creating over an existing note should be refused, got %v", err)
	}

	res := call(t, c, "note_search", map[string]any{"query": "bucket refills"})
	if res["total"].(int) != 1 {
		t.Fatalf("search found %v notes, want 1", res["total"])
	}
	hits := res["hits"].([]SearchHit)
	if hits[0].Path != "wiki/token-bucket.md" || hits[0].Snippet == "" {
		t.Errorf("unexpected hit: %+v", hits[0])
	}

	read := call(t, c, "note_read", map[string]any{"path": "wiki/token-bucket"})
	hash := read["content_hash"].(string)
	if hash == "" {
		t.Fatal("note_read returned no content_hash")
	}

	// A stale hash must be refused.
	if err := callErr(t, c, "note_write", map[string]any{
		"path": "wiki/token-bucket.md", "content": "x", "content_hash": strings.Repeat("0", 32),
	}); !strings.Contains(err.Error(), "changed since") {
		t.Errorf("stale write should mention the conflict, got %v", err)
	}

	sec := call(t, c, "note_section_edit", map[string]any{
		"path": "wiki/token-bucket.md", "mode": "append_to_section",
		"heading": "Idea", "content": "Bursts are allowed up to the bucket size.",
	})
	if sec["diff"] == nil {
		t.Error("a section edit should return a diff")
	}

	after := call(t, c, "note_read", map[string]any{"path": "wiki/token-bucket.md"})
	body := after["content"].(string)
	if !strings.Contains(body, "Bursts are allowed") || !strings.Contains(body, "## Related") {
		t.Errorf("section edit damaged the note:\n%s", body)
	}

	dry := call(t, c, "note_edit", map[string]any{
		"path": "wiki/token-bucket.md", "old_string": "fixed rate", "new_string": "constant rate",
		"dry_run": true,
	})
	if dry["dry_run"] != true || dry["diff"] == nil {
		t.Error("dry_run must report a diff and not write")
	}
	again := call(t, c, "note_read", map[string]any{"path": "wiki/token-bucket.md"})
	if !strings.Contains(again["content"].(string), "fixed rate") {
		t.Error("dry_run wrote to the note")
	}
}

func TestTagsAreNormalised(t *testing.T) {
	c := testCtx(t)
	call(t, c, "note_create", map[string]any{
		"path": "wiki/a.md", "tags": []any{"#Infra", "infra", "Zettel"},
	})
	res := call(t, c, "note_read", map[string]any{"path": "wiki/a.md"})
	tags := res["tags"].([]string)
	if len(tags) != 2 {
		t.Fatalf("tags = %v, want two normalised tags", tags)
	}
}

func TestBacklinksAndMove(t *testing.T) {
	c := testCtx(t)
	call(t, c, "note_create", map[string]any{
		"path": "wiki/target.md", "title": "Target", "content": "# Target\n",
	})
	call(t, c, "note_create", map[string]any{
		"path": "wiki/source.md", "title": "Source",
		"content": "# Source\n\nSee [[wiki/target]] for more.\n",
	})

	links := call(t, c, "note_backlinks", map[string]any{"path": "wiki/target.md"})
	in := links["incoming"].([]LinkRow)
	if len(in) != 1 || in[0].Path != "wiki/source.md" {
		t.Fatalf("backlinks = %+v", in)
	}

	plan := call(t, c, "note_move", map[string]any{
		"path": "wiki/target.md", "to": "wiki/renamed.md", "dry_run": true,
	})
	if len(plan["link_updates"].([]map[string]any)) != 1 {
		t.Errorf("dry run should list one note to update: %+v", plan["link_updates"])
	}
	if _, err := os.Stat(filepath.Join(c.vault.Root, "wiki", "renamed.md")); err == nil {
		t.Fatal("dry_run moved the note")
	}

	call(t, c, "note_move", map[string]any{"path": "wiki/target.md", "to": "wiki/renamed.md"})
	src := call(t, c, "note_read", map[string]any{"path": "wiki/source.md"})
	if !strings.Contains(src["content"].(string), "[[wiki/renamed]]") {
		t.Errorf("the link was not rewritten:\n%s", src["content"])
	}
}

func TestDeleteKeepsACopy(t *testing.T) {
	c := testCtx(t)
	call(t, c, "note_create", map[string]any{"path": "wiki/gone.md", "content": "# Gone\n\nvaluable\n"})
	call(t, c, "note_delete", map[string]any{"path": "wiki/gone.md"})

	if _, err := os.Stat(filepath.Join(c.vault.Root, "wiki", "gone.md")); err == nil {
		t.Fatal("the note is still there")
	}
	entries, err := os.ReadDir(filepath.Join(c.vault.Root, filepath.FromSlash(trashDir)))
	if err != nil || len(entries) == 0 {
		t.Fatalf("nothing was kept in the trash: %v", err)
	}
	raw, _ := os.ReadFile(filepath.Join(c.vault.Root, filepath.FromSlash(trashDir), entries[0].Name()))
	if !strings.Contains(string(raw), "valuable") {
		t.Error("the trashed copy does not hold the content")
	}
}

func TestTasksAndToggle(t *testing.T) {
	c := testCtx(t)
	call(t, c, "note_create", map[string]any{
		"path":    "projects/p.md",
		"content": "# P\n\n- [ ] first thing\n- [ ] second thing\n",
	})
	list := call(t, c, "task_list", map[string]any{})
	tasks := list["tasks"].([]TaskRow)
	if len(tasks) != 2 {
		t.Fatalf("found %d tasks, want 2", len(tasks))
	}
	call(t, c, "task_toggle", map[string]any{"path": "projects/p.md", "text": "first thing"})
	after := call(t, c, "task_list", map[string]any{})
	if len(after["tasks"].([]TaskRow)) != 1 {
		t.Error("toggling did not remove the task from the open list")
	}
}

func TestVaultReplaceIsDryByDefault(t *testing.T) {
	c := testCtx(t)
	call(t, c, "note_create", map[string]any{"path": "wiki/a.md", "content": "# A\n\nfoo everywhere\n"})
	res := call(t, c, "vault_replace", map[string]any{"pattern": "foo", "replace": "bar"})
	if res["dry_run"] != true {
		t.Fatal("vault_replace must default to a dry run")
	}
	read := call(t, c, "note_read", map[string]any{"path": "wiki/a.md"})
	if !strings.Contains(read["content"].(string), "foo") {
		t.Error("the default dry run wrote to the note")
	}
	call(t, c, "vault_replace", map[string]any{"pattern": "foo", "replace": "bar", "dry_run": false})
	read = call(t, c, "note_read", map[string]any{"path": "wiki/a.md"})
	if !strings.Contains(read["content"].(string), "bar") {
		t.Error("the real replace did nothing")
	}
}

func TestGrepFindsWhatSearchCannot(t *testing.T) {
	c := testCtx(t)
	call(t, c, "note_create", map[string]any{
		"path": "wiki/a.md", "content": "# A\n\nSee https://example.com/x?y=1 for the API key format AKIA1234.\n",
	})
	res := call(t, c, "vault_grep", map[string]any{"pattern": `AKIA\d+`})
	matches := res["matches"]
	if matches == nil {
		t.Fatal("grep found nothing")
	}
}

func TestReadOnlyUserSeesNoWriteTools(t *testing.T) {
	all := toolDefinitions(&User{Name: "a"})
	ro := toolDefinitions(&User{Name: "b", ReadOnly: true})
	if len(ro) >= len(all) {
		t.Fatalf("read-only user was offered %d tools, full user %d", len(ro), len(all))
	}
	for _, d := range ro {
		if registry[d["name"].(string)].Mutates {
			t.Errorf("read-only user was offered the mutating tool %s", d["name"])
		}
	}
}

func TestEveryToolHasADescriptionAndSchema(t *testing.T) {
	for name, tool := range registry {
		if len(tool.Description) < 60 {
			t.Errorf("%s: description is too thin for a model to route on", name)
		}
		schema := tool.schema("default")
		props := schema["properties"].(map[string]any)
		for _, req := range tool.Required {
			if _, ok := props[req]; !ok {
				t.Errorf("%s: required argument %q is not in the schema", name, req)
			}
		}
		if !tool.NoVault {
			if _, ok := props["vault"]; !ok {
				t.Errorf("%s: missing the vault argument", name)
			}
		}
	}
}

func TestVaultReplaceRefusesBeforeWriting(t *testing.T) {
	c := testCtx(t)
	for _, p := range []string{"wiki/a.md", "wiki/b.md", "wiki/c.md"} {
		call(t, c, "note_create", map[string]any{"path": p, "content": "# X\n\nfoo\n"})
	}
	err := callErr(t, c, "vault_replace", map[string]any{
		"pattern": "foo", "replace": "bar", "dry_run": false, "limit": 2,
	})
	if !strings.Contains(err.Error(), "would touch 3 notes") {
		t.Errorf("expected the limit to be reported up front, got %v", err)
	}
	// Nothing may have been written before the refusal.
	for _, p := range []string{"wiki/a.md", "wiki/b.md", "wiki/c.md"} {
		read := call(t, c, "note_read", map[string]any{"path": p})
		if !strings.Contains(read["content"].(string), "foo") {
			t.Fatalf("%s was written despite the refusal", p)
		}
	}
}

func TestOverwritingAMoveKeepsACopy(t *testing.T) {
	c := testCtx(t)
	call(t, c, "note_create", map[string]any{"path": "wiki/a.md", "content": "# A\n\nkeep me\n"})
	call(t, c, "note_create", map[string]any{"path": "wiki/b.md", "content": "# B\n\nincoming\n"})

	if err := callErr(t, c, "note_move", map[string]any{"path": "wiki/b.md", "to": "wiki/a.md"}); err == nil {
		t.Fatal("moving onto an existing note without overwrite must fail")
	}
	call(t, c, "note_move", map[string]any{"path": "wiki/b.md", "to": "wiki/a.md", "overwrite": true})

	entries, err := os.ReadDir(filepath.Join(c.vault.Root, filepath.FromSlash(trashDir)))
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range entries {
		raw, _ := os.ReadFile(filepath.Join(c.vault.Root, filepath.FromSlash(trashDir), e.Name()))
		if strings.Contains(string(raw), "keep me") {
			found = true
		}
	}
	if !found {
		t.Error("the overwritten note was not kept in the trash")
	}
}

func TestNoteMergeCarriesTagsAndAliases(t *testing.T) {
	c := testCtx(t)
	call(t, c, "note_create", map[string]any{"path": "wiki/keep.md", "title": "Keep",
		"tags": []any{"alpha"}, "content": "# Keep\n"})
	call(t, c, "note_create", map[string]any{"path": "wiki/gone.md", "title": "Gone",
		"tags": []any{"beta"}, "content": "# Gone\n\nabsorbed text\n"})
	call(t, c, "note_merge", map[string]any{"path": "wiki/keep.md", "from": "wiki/gone.md"})

	read := call(t, c, "note_read", map[string]any{"path": "wiki/keep.md"})
	if !strings.Contains(read["content"].(string), "absorbed text") {
		t.Error("the merged content is missing")
	}
	tags := read["tags"].([]string)
	if len(tags) != 2 {
		t.Errorf("tags after merge = %v, want both", tags)
	}
	fm := read["frontmatter"].(map[string]any)
	if fm["aliases"] == nil {
		t.Error("the absorbed note's title was not kept as an alias")
	}
}

func TestMetricsRenderIsValidExposition(t *testing.T) {
	m := NewMetrics()
	m.ObserveTool("note_search", "ok", 0.012, 400, false, false, false)
	m.ObserveTool("note_write", "error", 0.003, 0, false, false, true)
	m.ObserveHTTP("/mcp", 200)
	m.ObserveLogin("success")
	m.GitCommit()

	out := m.Render(snapshot{
		Version: "0.1.0", Commit: "abc", Users: 1, Tokens: 2, Sessions: 1,
		Vaults: []vaultSnapshot{{Name: "default", Stats: &VaultStats{Notes: 12, Words: 900}}},
	})

	for _, must := range []string{
		"# HELP secondbrain_build_info",
		"# TYPE secondbrain_tool_calls_total counter",
		`secondbrain_tool_calls_total{tool="note_search",outcome="ok"} 1`,
		`secondbrain_vault_notes{vault="default"} 12`,
		"secondbrain_git_commits_total 1",
	} {
		if !strings.Contains(out, must) {
			t.Errorf("missing from the exposition output: %s", must)
		}
	}
	// Every non-comment line must be `name value` or `name{labels} value`.
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if len(strings.Fields(line)) < 2 {
			t.Errorf("malformed metric line: %q", line)
		}
	}
}

func TestMetricsAreOffByDefault(t *testing.T) {
	t.Setenv("SECONDBRAIN_USERNAME", "a")
	t.Setenv("SECONDBRAIN_PASSWORD", "password123")
	t.Setenv("SECONDBRAIN_PUBLIC_URL", "https://example.com")
	cfg, err := LoadConfig("")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Metrics {
		t.Error("metrics must be off unless asked for")
	}
	if cfg.MetricsPath != "/metrics" {
		t.Errorf("default metrics path = %q", cfg.MetricsPath)
	}

	t.Setenv("SECONDBRAIN_METRICS", "true")
	t.Setenv("SECONDBRAIN_METRICS_KEY", "short")
	if _, err := LoadConfig(""); err == nil {
		t.Error("a short metrics key must be refused")
	}
	t.Setenv("SECONDBRAIN_METRICS_PATH", "/mcp")
	t.Setenv("SECONDBRAIN_METRICS_KEY", "0123456789abcdef0123")
	if _, err := LoadConfig(""); err == nil {
		t.Error("a metrics path colliding with /mcp must be refused")
	}
}
