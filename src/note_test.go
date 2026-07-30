package main

import (
	"strings"
	"testing"
)

const sample = `---
title: Rate limiting
tags: [wiki, infra]
aliases: [Throttling]
---

# Rate limiting

Intro text with a #inline-tag and a [[token bucket]] link.

## Why

Because #abuse happens. See [other note](wiki/other.md).

### Detail

Nested content.

## How

- [ ] measure first
- [x] read the paper

` + "```" + `
# not a heading
#not-a-tag
` + "```" + `

## Related
`

func TestParseNote(t *testing.T) {
	n := ParseNote("wiki/rate-limiting.md", sample)

	if n.Title != "Rate limiting" {
		t.Errorf("title = %q", n.Title)
	}
	if !n.hadFront {
		t.Fatal("frontmatter was not detected")
	}

	hs := n.Headings()
	want := []string{"Rate limiting", "Why", "Detail", "How", "Related"}
	if len(hs) != len(want) {
		t.Fatalf("got %d headings, want %d: %+v", len(hs), len(want), hs)
	}
	for i, h := range hs {
		if h.Text != want[i] {
			t.Errorf("heading %d = %q, want %q", i, h.Text, want[i])
		}
	}

	tags := strings.Join(n.Tags(), ",")
	for _, must := range []string{"wiki", "infra", "inline-tag", "abuse"} {
		if !strings.Contains(tags, must) {
			t.Errorf("tag %q missing from %q", must, tags)
		}
	}
	if strings.Contains(tags, "not-a-tag") {
		t.Error("a tag inside a fenced code block was picked up")
	}

	links := n.Links()
	if len(links) != 2 {
		t.Fatalf("got %d links, want 2: %+v", len(links), links)
	}
	if links[0].Target != "token bucket" || !links[0].Wiki {
		t.Errorf("first link = %+v", links[0])
	}
	if links[1].Target != "wiki/other.md" {
		t.Errorf("second link = %+v", links[1])
	}

	tasks := n.Tasks()
	if len(tasks) != 2 {
		t.Fatalf("got %d tasks, want 2", len(tasks))
	}
	if tasks[0].Done || !tasks[1].Done {
		t.Errorf("task states wrong: %+v", tasks)
	}
}

func TestRenderRoundTrip(t *testing.T) {
	n := ParseNote("a.md", sample)
	out := n.Render()
	again := ParseNote("a.md", out)
	if again.Title != n.Title {
		t.Errorf("title changed across a round trip")
	}
	if again.Body != n.Body {
		t.Errorf("body changed across a round trip")
	}
	if got := again.FrontList("tags"); len(got) != 2 {
		t.Errorf("tags after round trip = %v", got)
	}
}

func TestNoFrontmatterStaysUntouched(t *testing.T) {
	raw := "# Just a note\n\nNo metadata here.\n"
	n := ParseNote("a.md", raw)
	if n.hadFront {
		t.Fatal("frontmatter detected where there is none")
	}
	if n.Render() != raw {
		t.Errorf("a note without frontmatter was rewritten:\n%q", n.Render())
	}
	if n.Title != "Just a note" {
		t.Errorf("title from H1 = %q", n.Title)
	}
}

func TestHorizontalRuleIsNotFrontmatter(t *testing.T) {
	raw := "Some text\n\n---\n\nmore text\n"
	n := ParseNote("a.md", raw)
	if n.hadFront {
		t.Error("a horizontal rule was treated as frontmatter")
	}
}

func TestSectionEdit(t *testing.T) {
	cases := []struct {
		mode    SectionMode
		heading string
		content string
		check   func(t *testing.T, out string)
	}{
		{SectionAppend, "Why", "Appended line.", func(t *testing.T, out string) {
			n := ParseNote("a.md", out)
			sec, err := SectionText(n, "Why")
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(sec, "Appended line.") {
				t.Errorf("append did not land in the section:\n%s", sec)
			}
			if !strings.Contains(sec, "Nested content.") {
				t.Error("the subsection was lost")
			}
			if !strings.Contains(out, "## How") {
				t.Error("a later section was lost")
			}
		}},
		{SectionReplace, "How", "Replaced.", func(t *testing.T, out string) {
			if strings.Contains(out, "measure first") {
				t.Error("replace_section left the old content behind")
			}
			if !strings.Contains(out, "Replaced.") {
				t.Error("replacement missing")
			}
			if !strings.Contains(out, "## Related") {
				t.Error("the next section was eaten")
			}
		}},
		{SectionDelete, "Why", "", func(t *testing.T, out string) {
			if strings.Contains(out, "Because #abuse") || strings.Contains(out, "Nested content") {
				t.Error("delete_section did not take the subsection with it")
			}
			if !strings.Contains(out, "## How") {
				t.Error("delete_section removed too much")
			}
		}},
		{SectionAppendEnd, "", "The end.", func(t *testing.T, out string) {
			if !strings.HasSuffix(strings.TrimSpace(out), "The end.") {
				t.Error("append_to_note did not append at the end")
			}
		}},
		{SectionInsertBefore, "How", "## Before How\n\nx", func(t *testing.T, out string) {
			if strings.Index(out, "Before How") > strings.Index(out, "## How") {
				t.Error("insert_before_section inserted in the wrong place")
			}
		}},
	}
	for _, tc := range cases {
		t.Run(string(tc.mode), func(t *testing.T) {
			n := ParseNote("a.md", sample)
			out, err := SectionEdit(n, tc.mode, tc.heading, tc.content)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(out, "---\n") {
				t.Error("frontmatter was lost")
			}
			tc.check(t, out)
		})
	}
}

func TestSectionEditUnknownHeading(t *testing.T) {
	n := ParseNote("a.md", sample)
	if _, err := SectionEdit(n, SectionAppend, "Nonexistent", "x"); err == nil {
		t.Fatal("editing a heading that does not exist should fail")
	}
}

func TestStringEdit(t *testing.T) {
	const body = "alpha beta\nalpha gamma\n"

	if _, _, err := StringEdit(body, "alpha", "x", false); err == nil {
		t.Error("a non-unique old_string must be refused")
	}
	out, n, err := StringEdit(body, "alpha", "x", true)
	if err != nil || n != 2 || strings.Contains(out, "alpha") {
		t.Errorf("replace_all failed: %q n=%d err=%v", out, n, err)
	}
	if _, _, err := StringEdit(body, "missing", "x", false); err == nil {
		t.Error("a missing old_string must be refused")
	}
	if _, _, err := StringEdit(body, "", "x", false); err == nil {
		t.Error("an empty old_string must be refused")
	}
	out, n, err = StringEdit(body, "alpha beta", "delta", false)
	if err != nil || n != 1 || !strings.HasPrefix(out, "delta") {
		t.Errorf("unique replace failed: %q %d %v", out, n, err)
	}
}

func TestSlug(t *testing.T) {
	cases := map[string]string{
		"Rate Limiting!":    "rate-limiting",
		"  Über Größe  ":    "ueber-groesse",
		"a/b c":             "a-b-c",
		"":                  "note",
		"-- leading dashes": "leading-dashes",
	}
	for in, want := range cases {
		if got := Slug(in); got != want {
			t.Errorf("Slug(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestUnifiedDiff(t *testing.T) {
	a := "one\ntwo\nthree\n"
	b := "one\ntwo changed\nthree\n"
	d := UnifiedDiff(a, b, "x.md")
	if !strings.Contains(d, "-two") || !strings.Contains(d, "+two changed") {
		t.Errorf("diff looks wrong:\n%s", d)
	}
	if UnifiedDiff(a, a, "x.md") != "" {
		t.Error("identical inputs should produce no diff")
	}
}
