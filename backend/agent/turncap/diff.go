// Package turncap records what the AI changed during each conversation turn and
// keeps the versions, so a turn anchor can be reviewed hunk by hunk: which files
// changed, what the code looked like before, and whether an old change can still
// be applied to the working copy.
//
// Capture has two interchangeable back ends:
//
//   - git: at turn start a base tree is taken with `git stash create` (a commit
//     object that does NOT touch the worktree), at turn end another one; the diff
//     between the two trees is exactly this turn's changes, before-content
//     included. Untracked files are covered by a blob snapshot of the untracked
//     list taken at turn start.
//   - snapshot: for projects without git, turn start stores every text file in
//     the content-addressed blob store; turn end compares hashes and reads the
//     old version back from there.
//
// Everything lands in memdb (anchor_files + blobs), so versions survive an app
// restart and the review panel works on any past turn. See docs/anchor-design.md.
package turncap

import "strings"

// Hunk is one contiguous changed region between two versions of a file.
// All line numbers are 0-based and end-exclusive: [Start, End).
type Hunk struct {
	Index       int
	BeforeStart int
	BeforeEnd   int
	AfterStart  int
	AfterEnd    int
	Before      []string // old lines — what applying this hunk restores
	After       []string // new lines — what this hunk introduced
}

// Delta is how many lines this hunk adds (negative when it removes lines).
func (h Hunk) Delta() int {
	return len(h.After) - len(h.Before)
}

// Hunks splits two versions of a text file into changed regions using a
// line-level LCS. Only the edited regions are returned; unchanged runs between
// them are skipped. Common prefixes/suffixes are trimmed first so the DP table
// stays small; if the remaining middle is huge the middle is reported as one
// coarse hunk (correct, just not fine-grained).
func Hunks(before, after string) []Hunk {
	a := strings.Split(before, "\n")
	b := strings.Split(after, "\n")

	// 公共前后缀先剥掉：改动通常集中在一小段，剩下的才值得做 LCS。
	p := 0
	for p < len(a) && p < len(b) && a[p] == b[p] {
		p++
	}
	s := 0
	for s < len(a)-p && s < len(b)-p && a[len(a)-1-s] == b[len(b)-1-s] {
		s++
	}
	ma, mb := a[p:len(a)-s], b[p:len(b)-s]

	var mid []Hunk
	if len(ma) == 0 && len(mb) == 0 {
		mid = nil
	} else if len(ma)*len(mb) > 4_000_000 {
		// 超大的中间段不拆：一个粗 hunk 语义上仍然正确。
		mid = []Hunk{{BeforeStart: p, BeforeEnd: p + len(ma), AfterStart: p, AfterEnd: p + len(mb), Before: ma, After: mb}}
	} else {
		mid = lcsHunks(ma, mb, p, p)
	}

	var out []Hunk
	for _, h := range mid {
		h.Index = len(out)
		out = append(out, h)
	}
	return out
}

// lcsHunks computes the changed regions of two already-trimmed slices.
// offA/offB are the offsets of those slices inside the original files.
func lcsHunks(a, b []string, offA, offB int) []Hunk {
	pairs := lcsPairs(a, b)
	var out []Hunk
	i, j := 0, 0
	flush := func(ai, bi int) {
		if ai == i && bi == j {
			return
		}
		out = append(out, Hunk{
			BeforeStart: offA + i, BeforeEnd: offA + ai,
			AfterStart: offB + j, AfterEnd: offB + bi,
			Before: a[i:ai], After: b[j:bi],
		})
	}
	for _, pr := range pairs {
		flush(pr.OldIdx, pr.NewIdx)
		i, j = pr.OldIdx+1, pr.NewIdx+1
	}
	flush(len(a), len(b))
	return out
}

type pair struct{ OldIdx, NewIdx int }

// lcsPairs returns the matched line index pairs of a longest common
// subsequence, in order.
func lcsPairs(a, b []string) []pair {
	n, m := len(a), len(b)
	if n == 0 || m == 0 {
		return nil
	}
	dp := make([]int32, (n+1)*(m+1))
	at := func(i, j int) int { return i*(m+1) + j }
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if a[i] == b[j] {
				dp[at(i, j)] = dp[at(i+1, j+1)] + 1
			} else if dp[at(i+1, j)] >= dp[at(i, j+1)] {
				dp[at(i, j)] = dp[at(i+1, j)]
			} else {
				dp[at(i, j)] = dp[at(i, j+1)]
			}
		}
	}
	var out []pair
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case a[i] == b[j]:
			out = append(out, pair{i, j})
			i++
			j++
		case dp[at(i+1, j)] >= dp[at(i, j+1)]:
			i++
		default:
			j++
		}
	}
	return out
}
