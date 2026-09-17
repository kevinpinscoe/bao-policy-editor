package tui

import "strings"

// DiffOp is what happened to one line between two versions of a document.
type DiffOp int

const (
	// DiffContext is a line both versions share.
	DiffContext DiffOp = iota
	// DiffRemoved is a line only the earlier version has.
	DiffRemoved
	// DiffAdded is a line only the later version has.
	DiffAdded
	// DiffGap stands in for a run of unchanged lines the review screen
	// collapsed, so a long file's review shows only what changed.
	DiffGap
)

// DiffLine is one line of a review diff.
type DiffLine struct {
	Op DiffOp

	// Text is the line's content, without its trailing newline. For
	// DiffGap it is the summary of what was skipped.
	Text string

	// OldLine and NewLine are 1-based line numbers in the earlier and
	// later versions, or 0 where the line does not exist in that version.
	OldLine int
	NewLine int
}

// Marker is the single character conventionally printed in front of a
// diff line.
func (l DiffLine) Marker() string {
	switch l.Op {
	case DiffRemoved:
		return "-"
	case DiffAdded:
		return "+"
	case DiffGap:
		return "~"
	default:
		return " "
	}
}

// diffContextLines is how many unchanged lines are kept on either side of
// a change before the rest is collapsed into a DiffGap.
const diffContextLines = 3

// Diff produces a line-level diff of old against new, collapsing long runs
// of unchanged lines.
//
// The review screen exists so that what is about to be written is visible
// before it is written — including things the domain model cannot express,
// such as a comment disappearing along with the rule it documented. That
// requires diffing the bytes themselves rather than comparing decoded
// policies, which is why this operates on text and not on policy.Policy.
func Diff(old, new []byte) []DiffLine {
	oldLines := splitLines(old)
	newLines := splitLines(new)
	return collapseContext(diffLines(oldLines, newLines))
}

// HasChanges reports whether a diff contains anything but context.
func HasChanges(lines []DiffLine) bool {
	for _, l := range lines {
		if l.Op == DiffAdded || l.Op == DiffRemoved {
			return true
		}
	}
	return false
}

// CountChanges returns how many lines were added and removed.
func CountChanges(lines []DiffLine) (added, removed int) {
	for _, l := range lines {
		switch l.Op {
		case DiffAdded:
			added++
		case DiffRemoved:
			removed++
		}
	}
	return added, removed
}

// splitLines splits src into lines, dropping the trailing empty element a
// file-final newline would otherwise produce. A policy file is small
// enough that the whole thing is held in memory either way.
func splitLines(src []byte) []string {
	if len(src) == 0 {
		return nil
	}
	text := strings.ReplaceAll(string(src), "\r\n", "\n")
	lines := strings.Split(text, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// diffLines is a straightforward longest-common-subsequence diff.
//
// The dynamic-programming table is O(len(old) x len(new)) in both time and
// memory, which is the wrong algorithm for a large file and exactly the
// right one for an OpenBao policy: these are hand-written access-control
// files, a few dozen to a few hundred lines, and an exact minimal diff
// read by a human beats a heuristic that occasionally misaligns a block.
func diffLines(old, new []string) []DiffLine {
	lcs := make([][]int, len(old)+1)
	for i := range lcs {
		lcs[i] = make([]int, len(new)+1)
	}
	for i := len(old) - 1; i >= 0; i-- {
		for j := len(new) - 1; j >= 0; j-- {
			if old[i] == new[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
				continue
			}
			lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
		}
	}

	var out []DiffLine
	i, j := 0, 0
	for i < len(old) && j < len(new) {
		switch {
		case old[i] == new[j]:
			out = append(out, DiffLine{Op: DiffContext, Text: old[i], OldLine: i + 1, NewLine: j + 1})
			i++
			j++
		case lcs[i+1][j] >= lcs[i][j+1]:
			out = append(out, DiffLine{Op: DiffRemoved, Text: old[i], OldLine: i + 1})
			i++
		default:
			out = append(out, DiffLine{Op: DiffAdded, Text: new[j], NewLine: j + 1})
			j++
		}
	}
	for ; i < len(old); i++ {
		out = append(out, DiffLine{Op: DiffRemoved, Text: old[i], OldLine: i + 1})
	}
	for ; j < len(new); j++ {
		out = append(out, DiffLine{Op: DiffAdded, Text: new[j], NewLine: j + 1})
	}
	return out
}

// collapseContext replaces long runs of unchanged lines with a single
// DiffGap, keeping diffContextLines on either side of every change.
func collapseContext(lines []DiffLine) []DiffLine {
	if !HasChanges(lines) {
		return lines
	}

	keep := make([]bool, len(lines))
	for i, l := range lines {
		if l.Op == DiffContext {
			continue
		}
		lo := max(0, i-diffContextLines)
		hi := min(len(lines)-1, i+diffContextLines)
		for k := lo; k <= hi; k++ {
			keep[k] = true
		}
	}

	var out []DiffLine
	skipped := 0
	flush := func() {
		if skipped == 0 {
			return
		}
		out = append(out, DiffLine{Op: DiffGap, Text: gapText(skipped)})
		skipped = 0
	}
	for i, l := range lines {
		if keep[i] {
			flush()
			out = append(out, l)
			continue
		}
		skipped++
	}
	flush()
	return out
}

func gapText(n int) string {
	if n == 1 {
		return "1 unchanged line"
	}
	return itoa(n) + " unchanged lines"
}

// itoa avoids pulling strconv in for a single call site whose input is
// always a small non-negative count.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
