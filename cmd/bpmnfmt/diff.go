package main

import (
	"fmt"
	"strings"
)

// unifiedDiff renders a minimal unified diff (3 lines of context) between
// two byte slices, line based, using an LCS table. Inputs are small (BPMN
// files), so the quadratic table is fine.
func unifiedDiff(path string, a, b []byte) string {
	al := splitLines(string(a))
	bl := splitLines(string(b))

	// LCS dynamic program.
	n, m := len(al), len(bl)
	lcs := make([][]int, n+1)
	for i := range lcs {
		lcs[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if al[i] == bl[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else if lcs[i+1][j] >= lcs[i][j+1] {
				lcs[i][j] = lcs[i+1][j]
			} else {
				lcs[i][j] = lcs[i][j+1]
			}
		}
	}

	type op struct {
		kind byte // ' ', '-', '+'
		line string
	}
	var ops []op
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case al[i] == bl[j]:
			ops = append(ops, op{' ', al[i]})
			i++
			j++
		case lcs[i+1][j] >= lcs[i][j+1]:
			ops = append(ops, op{'-', al[i]})
			i++
		default:
			ops = append(ops, op{'+', bl[j]})
			j++
		}
	}
	for ; i < n; i++ {
		ops = append(ops, op{'-', al[i]})
	}
	for ; j < m; j++ {
		ops = append(ops, op{'+', bl[j]})
	}

	// Group into hunks with 3 context lines.
	const ctx = 3
	var sb strings.Builder
	fmt.Fprintf(&sb, "--- %s\n+++ %s (bpmnfmt)\n", path, path)
	aLine, bLine := 1, 1
	k := 0
	for k < len(ops) {
		if ops[k].kind == ' ' {
			aLine++
			bLine++
			k++
			continue
		}
		// Hunk start: back up context.
		start := k
		for c := 0; c < ctx && start > 0 && ops[start-1].kind == ' '; c++ {
			start--
		}
		aStart := aLine - (k - start)
		bStart := bLine - (k - start)
		// Advance to hunk end: change runs separated by <= 2*ctx context.
		end := k
		gap := 0
		for e := k; e < len(ops); e++ {
			if ops[e].kind == ' ' {
				gap++
				if gap > 2*ctx {
					break
				}
			} else {
				gap = 0
				end = e
			}
		}
		stop := end + 1
		for c := 0; c < ctx && stop < len(ops) && ops[stop].kind == ' '; c++ {
			stop++
		}
		aCount, bCount := 0, 0
		for _, o := range ops[start:stop] {
			switch o.kind {
			case ' ':
				aCount++
				bCount++
			case '-':
				aCount++
			case '+':
				bCount++
			}
		}
		fmt.Fprintf(&sb, "@@ -%d,%d +%d,%d @@\n", aStart, aCount, bStart, bCount)
		for _, o := range ops[start:stop] {
			sb.WriteByte(o.kind)
			sb.WriteString(o.line)
			sb.WriteByte('\n')
		}
		for _, o := range ops[k:stop] {
			switch o.kind {
			case ' ':
				aLine++
				bLine++
			case '-':
				aLine++
			case '+':
				bLine++
			}
		}
		k = stop
	}
	return sb.String()
}

func splitLines(s string) []string {
	s = strings.TrimSuffix(s, "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}
