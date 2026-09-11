package main

import (
	"fmt"
	"strings"
)

// ---------------------------------------------------------------------------
// When two writers meet
//
// content_hash is optimistic locking: the caller says which version it read,
// and a write against a stale version is refused. That refusal is correct and
// it is the only reason an agent, Obsidian and a git pull can share a
// directory without quietly eating each other's paragraphs.
//
// It is also, quite often, unnecessary. Two edits to a long note usually touch
// different parts of it. Refusing them both because one arrived second turns
// a non-problem into a round trip, and the agent's second attempt has to
// re-read the whole note - which is exactly the whole-note rewrite that this
// program spends its other five hundred lines trying to avoid.
//
// So a stale write is merged when it can be merged and refused when it cannot.
// "Can be" is strict: the two sets of changes must not touch the same lines.
// Anything that overlaps is a conflict and comes back as the error it always
// was, now carrying the diff of what the other writer did, which is the thing
// the caller actually needs in order to try again.
//
// The base version comes out of git. Without git there is no base, no merge is
// attempted, and behaviour is unchanged - which is also true when the base is
// too old to still be in the history that gets searched.
// ---------------------------------------------------------------------------

// mergeSearchDepth is how far back to look for the version the caller read.
// A caller working from something forty commits old is not in a race, it is
// working from a stale copy, and merging that silently is not a kindness.
const mergeSearchDepth = 40

// merge3 performs a line-based three-way merge. It returns the merged text
// and the number of regions where both sides changed the same lines.
// A non-zero conflict count means the merged text should not be used.
func merge3(base, ours, theirs string) (string, int) {
	b := splitLines(base)
	o := splitLines(ours)
	t := splitLines(theirs)

	ourChunks := changeChunks(b, o)
	theirChunks := changeChunks(b, t)

	var out []string
	conflicts := 0
	i, oi, ti := 0, 0, 0

	for i <= len(b) {
		// A chunk applies at this position when it starts here. Insertions
		// have a zero-length base range and still start somewhere.
		var oc, tc *changeChunk
		if oi < len(ourChunks) && ourChunks[oi].start == i {
			oc = &ourChunks[oi]
		}
		if ti < len(theirChunks) && theirChunks[ti].start == i {
			tc = &theirChunks[ti]
		}

		switch {
		case oc != nil && tc != nil:
			if sameLines(oc.repl, tc.repl) && oc.end == tc.end {
				// Both made the same change. Not a conflict, and applying
				// it once is the only sensible reading.
				out = append(out, oc.repl...)
				i = oc.end
			} else if oc.end == i && tc.end == i {
				// Two insertions at the same point, different text. There
				// is no way to know which comes first.
				conflicts++
				i = oc.end
			} else {
				conflicts++
				i = maxInt(oc.end, tc.end)
			}
			oi, ti = oi+1, ti+1
		case oc != nil:
			if overlaps(oc, theirChunks, ti) {
				conflicts++
			} else {
				out = append(out, oc.repl...)
			}
			i = oc.end
			oi++
		case tc != nil:
			if overlaps(tc, ourChunks, oi) {
				conflicts++
			} else {
				out = append(out, tc.repl...)
			}
			i = tc.end
			ti++
		default:
			if i == len(b) {
				i++
				continue
			}
			out = append(out, b[i])
			i++
		}
		// Chunks that ended before the cursor have been consumed or
		// subsumed by a conflict; skip past them.
		for oi < len(ourChunks) && ourChunks[oi].start < i {
			oi++
		}
		for ti < len(theirChunks) && theirChunks[ti].start < i {
			ti++
		}
	}

	merged := strings.Join(out, "\n")
	if merged != "" && !strings.HasSuffix(merged, "\n") {
		merged += "\n"
	}
	return merged, conflicts
}

// changeChunk is a replacement of base[start:end] by repl.
type changeChunk struct {
	start, end int
	repl       []string
}

// changeChunks reduces a two-way diff to the regions that differ.
func changeChunks(base, other []string) []changeChunk {
	ops := diffOps(base, other)
	var out []changeChunk
	bi, oi := 0, 0
	var cur *changeChunk
	for _, op := range ops {
		switch op.kind {
		case opEqual:
			cur = nil
			bi, oi = bi+1, oi+1
		case opDelete:
			if cur == nil {
				out = append(out, changeChunk{start: bi, end: bi})
				cur = &out[len(out)-1]
			}
			bi++
			cur.end = bi
		case opInsert:
			if cur == nil {
				out = append(out, changeChunk{start: bi, end: bi})
				cur = &out[len(out)-1]
			}
			cur.repl = append(cur.repl, other[oi])
			oi++
		}
	}
	return out
}

// overlaps reports whether c collides with any not-yet-consumed chunk from
// the other side. Touching the same base lines is a collision; meeting at a
// boundary is not.
func overlaps(c *changeChunk, others []changeChunk, from int) bool {
	for i := from; i < len(others); i++ {
		o := others[i]
		if o.start >= c.end {
			return false
		}
		if o.end > c.start {
			return true
		}
	}
	return false
}

func sameLines(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// ---------------------------------------------------------------------------
// Finding the base
// ---------------------------------------------------------------------------

// FindByHash looks back through the history of one path for the version whose
// content hashes to want. It is how a caller's content_hash is turned back
// into the text the caller was actually looking at.
func (g *GitStore) FindByHash(rel, want string, limit int) (string, string, bool) {
	if g == nil || want == "" {
		return "", "", false
	}
	revs, err := g.History(rel, limit)
	if err != nil {
		return "", "", false
	}
	for _, r := range revs {
		body, err := g.Contents(rel, r.Commit)
		if err != nil {
			continue
		}
		if strings.EqualFold(HashContent(body), want) {
			return body, r.Commit, true
		}
	}
	return "", "", false
}

// mergeOutcome is what Apply learned from trying to reconcile a stale write.
type mergeOutcome struct {
	ok         bool
	merged     string
	baseCommit string
	// why and diff explain a refusal. The diff is the important half: it is
	// what the other writer did, which is what the caller needs in order to
	// decide what to do next without re-reading the whole note blind.
	why  string
	diff string
}

// tryMerge attempts to rescue a write whose content_hash no longer matches.
// It returns nil when merging was not even attempted.
func (v *Vault) tryMerge(op writeOp, clean, cur, expected string) *mergeOutcome {
	if !v.mergeEnabled || v.git == nil {
		return nil
	}
	base, commit, found := v.git.FindByHash(clean, expected, mergeSearchDepth)
	if !found {
		return &mergeOutcome{why: fmt.Sprintf(
			"the version you read is not among the last %d commits of this note, so there is no base to merge against",
			mergeSearchDepth)}
	}
	// The transform is replayed against the version the caller actually
	// read, not against what is on disk now. That is the whole point: it
	// produces what the caller meant, which is then reconciled with what
	// somebody else did in the meantime.
	ours, err := op.transform(base, true)
	if err != nil {
		return &mergeOutcome{
			why:  "your edit does not apply to the version you read: " + err.Error(),
			diff: UnifiedDiff(base, cur, clean),
		}
	}
	// Deliberately no touchUpdated here. Stamping "updated" on our side
	// before the merge would put a changed timestamp line on both sides of
	// every concurrent write, and two changes to the same line is the
	// definition of a conflict - so every merge would fail, always. The
	// stamp is applied to the merged result by the caller instead.
	merged, conflicts := merge3(base, ours, cur)
	if conflicts > 0 {
		return &mergeOutcome{
			why:  fmt.Sprintf("%d region(s) were changed by both of you", conflicts),
			diff: UnifiedDiff(base, cur, clean),
		}
	}
	return &mergeOutcome{ok: true, merged: merged, baseCommit: commit}
}
