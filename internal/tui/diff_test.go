package tui

import (
	"strings"
	"testing"
)

func TestDiffOfIdenticalContentHasNoChanges(t *testing.T) {
	lines := Diff([]byte(samplePolicy), []byte(samplePolicy))

	if HasChanges(lines) {
		t.Errorf("identical content produced changes: %v", lines)
	}
	added, removed := CountChanges(lines)
	if added != 0 || removed != 0 {
		t.Errorf("added = %d, removed = %d, want 0 and 0", added, removed)
	}
}

func TestDiffReportsAddedAndRemovedLines(t *testing.T) {
	old := "a\nb\nc\n"
	new := "a\nB\nc\nd\n"

	lines := Diff([]byte(old), []byte(new))
	added, removed := CountChanges(lines)
	if added != 2 || removed != 1 {
		t.Errorf("added = %d, removed = %d, want 2 and 1 — %v", added, removed, lines)
	}

	var rendered []string
	for _, l := range lines {
		rendered = append(rendered, l.Marker()+l.Text)
	}
	joined := strings.Join(rendered, "|")
	for _, want := range []string{" a", "-b", "+B", " c", "+d"} {
		if !strings.Contains(joined, want) {
			t.Errorf("diff is missing %q: %s", want, joined)
		}
	}
}

func TestDiffShowsARemovedCommentAlongsideItsRule(t *testing.T) {
	// The case the review screen exists for: a comment disappearing is
	// something only a byte-level diff can show, because the domain model
	// has no field for it.
	const before = `# describes rule A
path "secret/data/a" {
  capabilities = ["read"]
}
`
	lines := Diff([]byte(before), nil)

	var removed []string
	for _, l := range lines {
		if l.Op == DiffRemoved {
			removed = append(removed, l.Text)
		}
	}
	if len(removed) == 0 || removed[0] != "# describes rule A" {
		t.Errorf("the removed comment is not in the diff: %v", removed)
	}
}

func TestDiffCollapsesLongRunsOfUnchangedLines(t *testing.T) {
	var old, new strings.Builder
	for i := range 40 {
		line := "line " + itoa(i) + "\n"
		old.WriteString(line)
		if i == 20 {
			new.WriteString("changed\n")
			continue
		}
		new.WriteString(line)
	}

	lines := Diff([]byte(old.String()), []byte(new.String()))

	gaps := 0
	context := 0
	for _, l := range lines {
		switch l.Op {
		case DiffGap:
			gaps++
		case DiffContext:
			context++
		}
	}
	if gaps == 0 {
		t.Error("a 40-line file with one change produced no collapsed gap")
	}
	if context > 2*diffContextLines {
		t.Errorf("context lines = %d, want at most %d", context, 2*diffContextLines)
	}
}

func TestDiffHandlesAnEmptySide(t *testing.T) {
	added := Diff(nil, []byte("a\nb\n"))
	if n, _ := CountChanges(added); n != 2 {
		t.Errorf("added = %d, want 2", n)
	}

	removed := Diff([]byte("a\nb\n"), nil)
	if _, n := CountChanges(removed); n != 2 {
		t.Errorf("removed = %d, want 2", n)
	}

	if HasChanges(Diff(nil, nil)) {
		t.Error("two empty documents produced changes")
	}
}

func TestDiffLineNumbersPointAtTheRightVersions(t *testing.T) {
	lines := Diff([]byte("a\nb\n"), []byte("a\nc\n"))

	for _, l := range lines {
		switch l.Op {
		case DiffRemoved:
			if l.OldLine == 0 || l.NewLine != 0 {
				t.Errorf("removed line has OldLine=%d NewLine=%d", l.OldLine, l.NewLine)
			}
		case DiffAdded:
			if l.NewLine == 0 || l.OldLine != 0 {
				t.Errorf("added line has OldLine=%d NewLine=%d", l.OldLine, l.NewLine)
			}
		case DiffContext:
			if l.OldLine == 0 || l.NewLine == 0 {
				t.Errorf("context line has OldLine=%d NewLine=%d", l.OldLine, l.NewLine)
			}
		}
	}
}
