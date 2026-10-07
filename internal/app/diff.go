package app

import (
	"fmt"
	"strings"
)

// diffOp is one line of an edit script: a kept (' '), deleted ('-'), or
// inserted ('+') line, with its 1-based line number in the before/after files.
type diffOp struct {
	typ  byte
	line string
	bi   int
	ai   int
}

// unifiedDiff returns a unified diff of before and after with the given number
// of context lines around each change. It returns "" when the inputs are equal.
// The algorithm is an O(n*m) longest-common-subsequence diff, which is fine for
// the small settings files this is used on.
func unifiedDiff(before, after []string, context int) string {
	ops := diffOps(before, after)
	if len(ops) == 0 {
		return ""
	}
	var changeIdx []int
	for i, o := range ops {
		if o.typ != ' ' {
			changeIdx = append(changeIdx, i)
		}
	}
	if len(changeIdx) == 0 {
		return ""
	}
	type span struct{ lo, hi int }
	var spans []span
	for _, ci := range changeIdx {
		lo := ci - context
		if lo < 0 {
			lo = 0
		}
		hi := ci + context
		if hi >= len(ops) {
			hi = len(ops) - 1
		}
		if len(spans) > 0 && lo <= spans[len(spans)-1].hi+1 {
			if hi > spans[len(spans)-1].hi {
				spans[len(spans)-1].hi = hi
			}
		} else {
			spans = append(spans, span{lo, hi})
		}
	}
	var buf strings.Builder
	for _, s := range spans {
		hunk := ops[s.lo : s.hi+1]
		bStart, bCount, aStart, aCount := hunkCounts(hunk)
		fmt.Fprintf(&buf, "@@ -%d,%d +%d,%d @@\n", bStart, bCount, aStart, aCount)
		for _, o := range hunk {
			buf.WriteByte(o.typ)
			buf.WriteString(o.line)
			buf.WriteByte('\n')
		}
	}
	return buf.String()
}

// diffOps computes the edit script between before and after via LCS.
func diffOps(before, after []string) []diffOp {
	n, m := len(before), len(after)
	lcs := make([][]int, n+1)
	for i := range lcs {
		lcs[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if before[i] == after[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else if lcs[i+1][j] >= lcs[i][j+1] {
				lcs[i][j] = lcs[i+1][j]
			} else {
				lcs[i][j] = lcs[i][j+1]
			}
		}
	}
	ops := make([]diffOp, 0, n+m)
	i, j := 0, 0
	for i < n && j < m {
		if before[i] == after[j] {
			ops = append(ops, diffOp{' ', before[i], i + 1, j + 1})
			i++
			j++
		} else if lcs[i+1][j] >= lcs[i][j+1] {
			ops = append(ops, diffOp{'-', before[i], i + 1, 0})
			i++
		} else {
			ops = append(ops, diffOp{'+', after[j], 0, j + 1})
			j++
		}
	}
	for ; i < n; i++ {
		ops = append(ops, diffOp{'-', before[i], i + 1, 0})
	}
	for ; j < m; j++ {
		ops = append(ops, diffOp{'+', after[j], 0, j + 1})
	}
	return ops
}

// hunkCounts computes the before/after start line and count for a hunk.
func hunkCounts(hunk []diffOp) (bStart, bCount, aStart, aCount int) {
	for _, o := range hunk {
		if o.typ == ' ' || o.typ == '-' {
			if bCount == 0 {
				bStart = o.bi
			}
			bCount++
		}
		if o.typ == ' ' || o.typ == '+' {
			if aCount == 0 {
				aStart = o.ai
			}
			aCount++
		}
	}
	return
}
