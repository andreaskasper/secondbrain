package main

import (
	"fmt"
	"strings"
)

// ---------------------------------------------------------------------------
// Unified diff
//
// Every mutating tool can return a diff instead of writing, and every mutating
// tool returns one after writing. That is not a nicety: an agent that edits
// prose has no other way to confirm that what it changed is what it meant to
// change, and a wrong edit to a note is much harder to notice than a wrong
// edit to code, because nothing fails to compile.
// ---------------------------------------------------------------------------

const diffContext = 3

// UnifiedDiff renders the difference between two texts.
func UnifiedDiff(oldText, newText, path string) string {
	if oldText == newText {
		return ""
	}
	a := splitLines(oldText)
	b := splitLines(newText)
	ops := diffOps(a, b)

	var hunks []string
	var cur []string
	var aStart, bStart, aCount, bCount int
	pending := 0
	flush := func() {
		if len(cur) == 0 {
			return
		}
		hunks = append(hunks, fmt.Sprintf("@@ -%d,%d +%d,%d @@\n%s",
			aStart, aCount, bStart, bCount, strings.Join(cur, "\n")))
		cur, aCount, bCount = nil, 0, 0
	}

	ai, bi := 0, 0
	for i := 0; i < len(ops); i++ {
		op := ops[i]
		if op.kind == opEqual {
			// Keep up to diffContext equal lines around a change; collapse
			// longer runs into a hunk boundary.
			nextChange := -1
			for j := i; j < len(ops) && j < i+2*diffContext+1; j++ {
				if ops[j].kind != opEqual {
					nextChange = j
					break
				}
			}
			if len(cur) > 0 && nextChange < 0 {
				for n := 0; n < diffContext && i < len(ops) && ops[i].kind == opEqual; n++ {
					cur = append(cur, " "+a[ai])
					ai, bi, aCount, bCount = ai+1, bi+1, aCount+1, bCount+1
					i++
				}
				i--
				flush()
				pending = 0
				continue
			}
			if len(cur) == 0 {
				pending++
			}
			if len(cur) > 0 {
				cur = append(cur, " "+a[ai])
				aCount, bCount = aCount+1, bCount+1
			}
			ai, bi = ai+1, bi+1
			continue
		}
		if len(cur) == 0 {
			back := diffContext
			if pending < back {
				back = pending
			}
			aStart, bStart = ai-back+1, bi-back+1
			for k := back; k > 0; k-- {
				cur = append(cur, " "+a[ai-k])
				aCount, bCount = aCount+1, bCount+1
			}
			pending = 0
		}
		switch op.kind {
		case opDelete:
			cur = append(cur, "-"+a[ai])
			ai, aCount = ai+1, aCount+1
		case opInsert:
			cur = append(cur, "+"+b[bi])
			bi, bCount = bi+1, bCount+1
		}
	}
	flush()
	if len(hunks) == 0 {
		return ""
	}
	head := fmt.Sprintf("--- a/%s\n+++ b/%s\n", path, path)
	return head + strings.Join(hunks, "\n") + "\n"
}

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	s = strings.TrimSuffix(s, "\n")
	return strings.Split(s, "\n")
}

type opKind int

const (
	opEqual opKind = iota
	opDelete
	opInsert
)

type diffOp struct{ kind opKind }

// diffOps is a Myers-style LCS via dynamic programming. Notes are small; the
// quadratic table is fine and the implementation being obviously correct
// matters more here than being clever.
func diffOps(a, b []string) []diffOp {
	// Trim the common prefix and suffix first, which in practice reduces
	// almost every note edit to a handful of lines.
	p := 0
	for p < len(a) && p < len(b) && a[p] == b[p] {
		p++
	}
	s := 0
	for s < len(a)-p && s < len(b)-p && a[len(a)-1-s] == b[len(b)-1-s] {
		s++
	}
	mid := lcsOps(a[p:len(a)-s], b[p:len(b)-s])

	out := make([]diffOp, 0, p+len(mid)+s)
	for i := 0; i < p; i++ {
		out = append(out, diffOp{opEqual})
	}
	out = append(out, mid...)
	for i := 0; i < s; i++ {
		out = append(out, diffOp{opEqual})
	}
	return out
}

func lcsOps(a, b []string) []diffOp {
	n, m := len(a), len(b)
	if n == 0 {
		out := make([]diffOp, m)
		for i := range out {
			out[i] = diffOp{opInsert}
		}
		return out
	}
	if m == 0 {
		out := make([]diffOp, n)
		for i := range out {
			out[i] = diffOp{opDelete}
		}
		return out
	}
	// Guard against pathological inputs: beyond this, fall back to a plain
	// replace rather than allocate a huge table.
	if n*m > 4_000_000 {
		out := make([]diffOp, 0, n+m)
		for i := 0; i < n; i++ {
			out = append(out, diffOp{opDelete})
		}
		for i := 0; i < m; i++ {
			out = append(out, diffOp{opInsert})
		}
		return out
	}

	table := make([][]int, n+1)
	for i := range table {
		table[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if a[i] == b[j] {
				table[i][j] = table[i+1][j+1] + 1
			} else if table[i+1][j] >= table[i][j+1] {
				table[i][j] = table[i+1][j]
			} else {
				table[i][j] = table[i][j+1]
			}
		}
	}
	var out []diffOp
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case a[i] == b[j]:
			out = append(out, diffOp{opEqual})
			i, j = i+1, j+1
		case table[i+1][j] >= table[i][j+1]:
			out = append(out, diffOp{opDelete})
			i++
		default:
			out = append(out, diffOp{opInsert})
			j++
		}
	}
	for ; i < n; i++ {
		out = append(out, diffOp{opDelete})
	}
	for ; j < m; j++ {
		out = append(out, diffOp{opInsert})
	}
	return out
}
