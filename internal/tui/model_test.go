package tui

import (
	"os"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// newTestModel builds a model sized like an ordinary terminal, with the
// window-size message the runtime would have delivered already applied.
func newTestModel(t *testing.T, session *Session) *Model {
	t.Helper()
	m := New(session)
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	return m
}

// send drives the model through a series of keystrokes, the way the
// runtime would.
func send(t *testing.T, m *Model, keystrokes ...string) {
	t.Helper()
	for _, k := range keystrokes {
		m.Update(pressKey(k))
	}
}

func TestEditorNavigatesTheRuleList(t *testing.T) {
	s, _ := openTestSession(t, samplePolicy)
	m := newTestModel(t, s)

	if m.selected != 0 {
		t.Fatalf("initial selection = %d, want 0", m.selected)
	}
	send(t, m, "down")
	if m.selected != 1 {
		t.Errorf("after down, selection = %d, want 1", m.selected)
	}
	// The list wraps, so there is no dead end at either edge.
	send(t, m, "down")
	if m.selected != 0 {
		t.Errorf("after wrapping, selection = %d, want 0", m.selected)
	}
	send(t, m, "up")
	if m.selected != 1 {
		t.Errorf("after up from 0, selection = %d, want 1", m.selected)
	}
	send(t, m, "home")
	if m.selected != 0 {
		t.Errorf("after home, selection = %d, want 0", m.selected)
	}
	send(t, m, "end")
	if m.selected != 1 {
		t.Errorf("after end, selection = %d, want 1", m.selected)
	}
}

func TestEditingARuleAndApplyingItChangesTheDocument(t *testing.T) {
	s, _ := openTestSession(t, samplePolicy)
	m := newTestModel(t, s)

	send(t, m, "enter")
	if m.screen != screenForm {
		t.Fatalf("enter did not open the rule form (screen = %d)", m.screen)
	}

	// Move to the `update` capability and tick it.
	send(t, m, "down", "down", "down")
	if row := m.form.currentRow(); row.kind != rowCapability || string(row.capability) != "update" {
		t.Fatalf("cursor is on %v, expected the update capability", row)
	}
	send(t, m, " ", "ctrl+s")

	if m.screen != screenEditor {
		t.Fatalf("applying the form did not return to the editor (screen = %d)", m.screen)
	}
	if !s.Dirty() {
		t.Error("the document is not reported as modified after an applied edit")
	}
	if !strings.Contains(string(s.Current()), `["read", "list", "update"]`) {
		t.Errorf("the capability was not added:\n%s", s.Current())
	}
	if !strings.Contains(string(s.Current()), "# team A") {
		t.Errorf("the edit discarded a comment:\n%s", s.Current())
	}
}

func TestOpeningAndAcceptingAFormUnchangedWritesNothing(t *testing.T) {
	s, _ := openTestSession(t, samplePolicy)
	m := newTestModel(t, s)

	send(t, m, "enter", "ctrl+s")

	if s.Dirty() {
		t.Errorf("accepting an unchanged form modified the document:\n%s", s.Current())
	}
	if string(s.Current()) != samplePolicy {
		t.Errorf("accepting an unchanged form rewrote the file:\n%s", s.Current())
	}
	if !strings.Contains(m.status, "no changes") {
		t.Errorf("status = %q, want it to say nothing changed", m.status)
	}
}

func TestCancellingAFormChangesNothing(t *testing.T) {
	s, _ := openTestSession(t, samplePolicy)
	m := newTestModel(t, s)

	send(t, m, "enter", "down", " ", "esc")

	if s.Dirty() {
		t.Error("cancelling a form modified the document")
	}
	if m.screen != screenEditor {
		t.Errorf("escape did not return to the editor (screen = %d)", m.screen)
	}
}

func TestAddingARuleAppendsItAndSelectsIt(t *testing.T) {
	s, _ := openTestSession(t, samplePolicy)
	m := newTestModel(t, s)
	before := s.Document().RuleCount()

	send(t, m, "a")
	if m.screen != screenForm || !m.form.isNew {
		t.Fatalf("a did not open the add form (screen = %d)", m.screen)
	}
	// The cursor starts on the path field; open it and type a path.
	send(t, m, "enter")
	for _, key := range typeText("secret/data/new") {
		m.Update(key)
	}
	send(t, m, "enter", "ctrl+s")

	if got := s.Document().RuleCount(); got != before+1 {
		t.Fatalf("rule count = %d, want %d", got, before+1)
	}
	if m.selected != s.Document().RuleCount()-1 {
		t.Errorf("selection = %d, want the new rule at %d", m.selected, s.Document().RuleCount()-1)
	}
	if !strings.Contains(string(s.Current()), `path "secret/data/new"`) {
		t.Errorf("the new rule is not in the document:\n%s", s.Current())
	}
}

func TestAddingARuleWithNoPathIsRefused(t *testing.T) {
	s, _ := openTestSession(t, samplePolicy)
	m := newTestModel(t, s)
	before := string(s.Current())

	send(t, m, "a", "ctrl+s")

	if m.screen != screenForm {
		t.Error("a form with no path was applied instead of refused")
	}
	if m.form.problem == "" {
		t.Error("the refusal gave no reason")
	}
	if string(s.Current()) != before {
		t.Error("a refused add changed the document")
	}
}

func TestDuplicatingARuleKeepsItsComments(t *testing.T) {
	s, _ := openTestSession(t, samplePolicy)
	m := newTestModel(t, s)
	before := s.Document().RuleCount()

	send(t, m, "d")

	if got := s.Document().RuleCount(); got != before+1 {
		t.Fatalf("rule count = %d, want %d", got, before+1)
	}
	if m.selected != s.Document().RuleCount()-1 {
		t.Errorf("selection = %d, want the copy at %d", m.selected, s.Document().RuleCount()-1)
	}
	if strings.Count(string(s.Current()), "# team A") != 2 {
		t.Errorf("the copy did not keep the original's comment:\n%s", s.Current())
	}
}

func TestRemovingARuleAsksFirstAndNamesWhatGoesWithIt(t *testing.T) {
	s, _ := openTestSession(t, samplePolicy)
	m := newTestModel(t, s)
	before := s.Document().RuleCount()

	send(t, m, "x")
	if m.dialog != dialogRemove {
		t.Fatalf("x did not raise the remove confirmation (dialog = %d)", m.dialog)
	}
	if s.Document().RuleCount() != before {
		t.Error("the rule was removed before the question was answered")
	}
	if !strings.Contains(m.pendingRemoval.LeadComments, "# team A") {
		t.Errorf("the pending removal did not record the rule's comment: %q", m.pendingRemoval.LeadComments)
	}
	if !strings.Contains(m.render(), "# team A") {
		t.Error("the confirmation does not show the comment that would go with the rule")
	}

	// Backing out leaves everything alone.
	send(t, m, "esc")
	if m.dialog != dialogNone || s.Document().RuleCount() != before {
		t.Error("cancelling the confirmation removed the rule anyway")
	}

	send(t, m, "x", "y")
	if got := s.Document().RuleCount(); got != before-1 {
		t.Errorf("rule count = %d, want %d", got, before-1)
	}
	if strings.Contains(string(s.Current()), "# team A") {
		t.Errorf("the rule's comment outlived it:\n%s", s.Current())
	}
}

func TestQuittingWithUnsavedChangesAsksFirst(t *testing.T) {
	s, _ := openTestSession(t, samplePolicy)
	m := newTestModel(t, s)

	// Clean document: quitting goes straight through.
	_, cmd := m.Update(pressKey("q"))
	if cmd == nil {
		t.Error("quitting a clean document did not issue a quit command")
	}
	if m.dialog != dialogNone {
		t.Error("quitting a clean document raised a confirmation")
	}

	// Dirty document: it asks.
	send(t, m, "d")
	if !s.Dirty() {
		t.Fatal("the test did not manage to make the document dirty")
	}
	_, cmd = m.Update(pressKey("q"))
	if cmd != nil {
		t.Error("quitting a modified document issued a quit command without asking")
	}
	if m.dialog != dialogDiscard {
		t.Fatalf("quitting a modified document did not ask (dialog = %d)", m.dialog)
	}

	// Escape keeps editing; d discards and quits.
	send(t, m, "esc")
	if m.dialog != dialogNone {
		t.Error("escape did not dismiss the confirmation")
	}
	m.Update(pressKey("q"))
	_, cmd = m.Update(pressKey("d"))
	if cmd == nil {
		t.Error("discarding did not issue a quit command")
	}
}

func TestCtrlCAsksBeforeDiscardingUnsavedChanges(t *testing.T) {
	s, _ := openTestSession(t, samplePolicy)
	m := newTestModel(t, s)
	send(t, m, "d")

	_, cmd := m.Update(pressKey("ctrl+c"))
	if cmd != nil || m.dialog != dialogDiscard {
		t.Errorf("ctrl+c discarded unsaved work without asking (dialog = %d, cmd = %v)", m.dialog, cmd)
	}
}

func TestSaveShowsTheReviewFirstAndThenWrites(t *testing.T) {
	s, path := openTestSession(t, samplePolicy)
	m := newTestModel(t, s)

	send(t, m, "d", "s")
	if m.screen != screenReview {
		t.Fatalf("s did not open the review screen (screen = %d)", m.screen)
	}
	if !HasChanges(m.review) {
		t.Error("the review shows no changes for a modified document")
	}
	onDisk, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(onDisk) != samplePolicy {
		t.Error("opening the review wrote to the file")
	}

	send(t, m, "ctrl+s")
	if s.Dirty() {
		t.Error("the document is still modified after writing from the review screen")
	}
	if m.screen != screenEditor {
		t.Errorf("saving did not return to the editor (screen = %d)", m.screen)
	}
	written, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(written) == samplePolicy {
		t.Error("the file was not written")
	}
}

func TestSaveWithNoChangesSaysSoInsteadOfOpeningTheReview(t *testing.T) {
	s, _ := openTestSession(t, samplePolicy)
	m := newTestModel(t, s)

	send(t, m, "s")

	if m.screen != screenEditor {
		t.Errorf("s on a clean document left the editor (screen = %d)", m.screen)
	}
	if !strings.Contains(m.status, "nothing to save") {
		t.Errorf("status = %q, want it to say there is nothing to save", m.status)
	}
}

func TestSaveConflictOffersReloadAndKeepsTheEdits(t *testing.T) {
	s, path := openTestSession(t, samplePolicy)
	m := newTestModel(t, s)

	send(t, m, "d")
	const theirs = "path \"secret/data/theirs\" {\n  capabilities = [\"read\"]\n}\n"
	if err := os.WriteFile(path, []byte(theirs), 0o644); err != nil {
		t.Fatal(err)
	}

	send(t, m, "s", "ctrl+s")
	if m.dialog != dialogConflict {
		t.Fatalf("a changed file did not raise the conflict dialog (dialog = %d)", m.dialog)
	}
	if !s.Dirty() {
		t.Error("the refused save cleared the document's modified state")
	}

	// v shows what changed underneath, without resolving anything.
	send(t, m, "v")
	if m.screen != screenReview || !m.reviewingConflict {
		t.Errorf("v did not show the on-disk diff (screen = %d, conflict = %v)", m.screen, m.reviewingConflict)
	}
	if !HasChanges(m.review) {
		t.Error("the conflict diff shows no differences")
	}

	// r takes what is on disk, which is the destructive answer and says so.
	send(t, m, "esc", "s", "ctrl+s", "r")
	if string(s.Current()) != theirs {
		t.Errorf("reloading did not take the file's contents:\n%s", s.Current())
	}
	if s.Dirty() {
		t.Error("the document is modified immediately after a reload")
	}
}

func TestAnUnparseableFileBlocksEveryEditingAction(t *testing.T) {
	s, _ := openTestSession(t, "path \"secret/data/a\" {\n  capabilities = [\n")
	m := newTestModel(t, s)
	before := string(s.Current())

	for _, key := range []string{"enter", "a", "d", "x"} {
		m.problem = ""
		send(t, m, key)
		if m.screen != screenEditor {
			t.Errorf("%q opened an editing screen on a read-only document (screen = %d)", key, m.screen)
			m.screen = screenEditor
		}
		if m.dialog != dialogNone {
			t.Errorf("%q raised a dialog on a read-only document", key)
			m.dialog = dialogNone
		}
		if m.problem == "" {
			t.Errorf("%q gave no reason for refusing", key)
		}
	}
	if string(s.Current()) != before {
		t.Error("a read-only document was changed")
	}
}

func TestReadOnlyDocumentStillPreviewsAndReportsDiagnostics(t *testing.T) {
	s, _ := openTestSession(t, "path \"secret/data/a\" {\n  capabilities = [\n")
	m := newTestModel(t, s)

	send(t, m, "p")
	if m.screen != screenPreview {
		t.Errorf("preview is unavailable on a read-only document (screen = %d)", m.screen)
	}
	send(t, m, "esc", "g")
	if m.screen != screenDiagnostics {
		t.Errorf("diagnostics are unavailable on a read-only document (screen = %d)", m.screen)
	}
	if !strings.Contains(m.render(), "error") {
		t.Error("the diagnostics screen does not report the syntax error")
	}
}

func TestEffectiveAccessScreenExplainsItsDecision(t *testing.T) {
	s, _ := openTestSession(t, samplePolicy)
	m := newTestModel(t, s)

	send(t, m, "t")
	if m.screen != screenTest {
		t.Fatalf("t did not open the test screen (screen = %d)", m.screen)
	}
	// The path field is prefilled from the selected rule; replace it with
	// something that rule actually matches.
	m.test.path.SetValue("secret/data/team-a/example")
	send(t, m, "enter")

	if m.test.problem != "" {
		t.Fatalf("the check reported a problem: %s", m.test.problem)
	}
	if !m.test.allowed {
		t.Errorf("read on secret/data/team-a/example was denied:\n%s", m.test.result)
	}
	if !strings.Contains(m.test.result, "secret/data/team-a/*") {
		t.Errorf("the explanation does not name the winning pattern:\n%s", m.test.result)
	}

	// And a path nothing covers is denied, with the reason shown.
	m.test.path.SetValue("secret/data/other/thing")
	send(t, m, "enter")
	if m.test.allowed {
		t.Error("an uncovered path was allowed")
	}
	if !strings.Contains(m.render(), "DENIED") {
		t.Error("the screen does not say the request was denied")
	}
}

func TestHelpListsTheKeys(t *testing.T) {
	s, _ := openTestSession(t, samplePolicy)
	m := newTestModel(t, s)

	send(t, m, "?")
	if m.screen != screenHelp {
		t.Fatalf("? did not open help (screen = %d)", m.screen)
	}
	rendered := m.render()
	for _, want := range []string{"duplicate", "remove", "test effective access"} {
		if !strings.Contains(rendered, want) {
			t.Errorf("help does not mention %q", want)
		}
	}
}

// TestEveryScreenRendersInANarrowTerminal is the build brief's "the
// interface must remain usable in narrow terminals", checked the only way
// that can be checked without a human looking: no screen may emit a line
// wider than the terminal, at any width, because a line that overflows
// wraps and the layout comes apart.
func TestEveryScreenRendersInANarrowTerminal(t *testing.T) {
	for _, width := range []int{40, 60, 100} {
		screens := []struct {
			name string
			keys []string
		}{
			{"editor", nil},
			{"form", []string{"enter"}},
			{"params", []string{"enter", "end", "enter"}},
			{"preview", []string{"p"}},
			{"diagnostics", []string{"g"}},
			{"test", []string{"t"}},
			{"help", []string{"?"}},
			{"review", []string{"d", "s"}},
			{"remove dialog", []string{"x"}},
		}

		for _, sc := range screens {
			m := New(mustReopen(t, samplePolicy))
			m.Update(tea.WindowSizeMsg{Width: width, Height: 20})
			send(t, m, sc.keys...)

			rendered := m.render()
			if rendered == "" {
				t.Errorf("width %d, screen %s: rendered nothing", width, sc.name)
			}
			for i, line := range strings.Split(rendered, "\n") {
				// lipgloss.Width ignores the ANSI escapes the styles emit, so
				// this measures what the terminal would actually show.
				if got := lipgloss.Width(line); got > width {
					t.Errorf("width %d, screen %s, line %d: %d cells wide\n%q",
						width, sc.name, i, got, line)
				}
			}
		}
	}
}

func TestEditorRendersWithoutAnyRules(t *testing.T) {
	m := newTestModel(t, NewSession())

	rendered := m.render()
	if !strings.Contains(rendered, "no rules yet") {
		t.Errorf("an empty policy does not say so:\n%s", rendered)
	}
	// Navigation on an empty list must not panic or select a phantom rule.
	send(t, m, "down", "up", "end", "home")
	if m.selected != 0 {
		t.Errorf("selection = %d on an empty policy, want 0", m.selected)
	}
}

func TestMouseClickSelectsARule(t *testing.T) {
	s, _ := openTestSession(t, samplePolicy)
	m := newTestModel(t, s)
	m.render() // records listTop

	m.Update(tea.MouseClickMsg{X: 4, Y: m.listTop + 1})
	if m.selected != 1 {
		t.Errorf("clicking the second row selected %d, want 1", m.selected)
	}

	// A click above the list, or past the last rule, changes nothing.
	m.Update(tea.MouseClickMsg{X: 4, Y: 0})
	m.Update(tea.MouseClickMsg{X: 4, Y: m.listTop + 50})
	if m.selected != 1 {
		t.Errorf("an out-of-range click moved the selection to %d", m.selected)
	}
}

func mustReopen(t *testing.T, contents string) *Session {
	t.Helper()
	s, _ := openTestSession(t, contents)
	return s
}
