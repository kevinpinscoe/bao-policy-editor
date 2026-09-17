package tui

import (
	"errors"
	"fmt"
	"strings"

	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"

	"github.com/kevinpinscoe/bao-policy-editor/internal/fileio"
	"github.com/kevinpinscoe/bao-policy-editor/internal/hclpolicy"
)

// screen is which view the editor is currently showing.
type screen int

const (
	screenEditor screen = iota
	screenForm
	screenParams
	screenPreview
	screenDiagnostics
	screenTest
	screenReview
	screenHelp
	screenSavePath
)

// dialog is a modal question laid over whatever screen is showing.
//
// Modal questions are reserved for the three decisions that are actually
// irreversible or destructive: discarding unsaved work, deleting a rule
// along with its comments, and resolving a file that changed underneath
// the editor. Everything else is a screen the user can back out of with
// Escape.
type dialog int

const (
	dialogNone dialog = iota
	dialogDiscard
	dialogRemove
	dialogConflict
)

// Model is the editor's root Bubble Tea model.
type Model struct {
	session *Session
	styles  Styles

	width  int
	height int

	screen screen
	dialog dialog

	selected int
	// listTop is the screen row the first rule occupies, recorded at
	// render time so a mouse click can be turned back into a rule.
	listTop int

	form   *ruleForm
	params *paramForm
	test   *accessTest

	viewport viewport.Model
	savePath textinput.Model

	// review holds the diff currently on the review screen, and
	// reviewingConflict says whether it is the pending save (original
	// against current) or the surprise on disk (disk against current).
	review            []DiffLine
	reviewingConflict bool

	// pendingRemoval describes the rule the remove dialog is asking about.
	pendingRemoval hclpolicy.Removal
	pendingRef     hclpolicy.RuleRef

	status  string
	problem string
}

// New builds the editor model around an open session.
func New(session *Session) *Model {
	m := &Model{
		session: session,
		styles:  DefaultStyles(),
		// A sane starting size so the first render is not degenerate if
		// the terminal is slow to report its own.
		width:    80,
		height:   24,
		viewport: viewport.New(viewport.WithWidth(80), viewport.WithHeight(18)),
		savePath: newInput("path to write the policy to"),
	}
	m.viewport.MouseWheelEnabled = true
	m.savePath.SetValue(SuggestSavePath())
	return m
}

// Init implements tea.Model.
func (m *Model) Init() tea.Cmd { return nil }

// View implements tea.Model.
func (m *Model) View() tea.View {
	v := tea.NewView(m.render())
	// Full-window mode. Bubble Tea leaves the alternate screen on quit,
	// on an interrupt, and on a recovered panic, which is what restores
	// the user's terminal in all three cases without BPE managing it.
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	return v
}

// Update implements tea.Model.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.viewport.SetWidth(max(10, msg.Width))
		m.viewport.SetHeight(max(3, msg.Height-4))
		return m, nil

	case tea.MouseWheelMsg:
		if m.scrollingScreen() {
			var cmd tea.Cmd
			m.viewport, cmd = m.viewport.Update(msg)
			return m, cmd
		}
		return m, nil

	case tea.MouseClickMsg:
		m.handleClick(msg.Mouse())
		return m, nil

	case tea.KeyPressMsg:
		return m.handleKey(msg)
	}

	return m, nil
}

// scrollingScreen reports whether the current screen is one the shared
// viewport is showing.
func (m *Model) scrollingScreen() bool {
	switch m.screen {
	case screenPreview, screenDiagnostics, screenReview, screenHelp:
		return true
	default:
		return false
	}
}

// handleClick turns a click on the rule list into a selection. Mouse
// support is an addition, never a requirement: everything it does has a
// key that does the same thing.
func (m *Model) handleClick(mouse tea.Mouse) {
	if m.dialog != dialogNone || m.screen != screenEditor {
		return
	}
	row := mouse.Y - m.listTop
	if row < 0 {
		return
	}
	rules := m.session.Document().RuleCount()
	height := m.listHeight()
	index := scrollStart(m.selected, rules, height) + row
	if index >= 0 && index < rules {
		m.selected = index
	}
}

func (m *Model) listHeight() int {
	if m.width < narrowWidth {
		return max(3, (m.height-6)/2)
	}
	return max(3, m.height-6)
}

func (m *Model) handleKey(key tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.dialog != dialogNone {
		return m.handleDialogKey(key)
	}

	switch m.screen {
	case screenEditor:
		return m.handleEditorKey(key)
	case screenForm:
		return m.handleFormKey(key)
	case screenParams:
		return m.handleParamsKey(key)
	case screenTest:
		done, cmd := m.test.Update(key, m.session)
		if done {
			m.screen = screenEditor
		}
		return m, cmd
	case screenSavePath:
		return m.handleSavePathKey(key)
	default:
		return m.handleScrollKey(key)
	}
}

func (m *Model) handleEditorKey(key tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	rules := m.session.Document().RuleCount()
	m.problem = ""

	switch key.String() {
	case "q", "ctrl+c":
		return m.requestQuit()
	case "?":
		m.showHelp()
	case "up", "k":
		m.moveSelection(-1, rules)
	case "down", "j":
		m.moveSelection(1, rules)
	case "home":
		m.selected = 0
	case "end":
		m.selected = max(0, rules-1)
	case "enter":
		return m, m.openEditForm()
	case "a":
		return m, m.openAddForm()
	case "d":
		m.duplicateSelected()
	case "x":
		m.requestRemove()
	case "p":
		m.showPreview()
	case "g":
		m.showDiagnostics()
	case "t":
		m.test = newAccessTest(m.selectedPath())
		m.screen = screenTest
		return m, m.test.path.Focus()
	case "s":
		m.showReview()
	}
	return m, nil
}

func (m *Model) moveSelection(delta, total int) {
	if total == 0 {
		m.selected = 0
		return
	}
	m.selected = (m.selected + delta + total) % total
}

func (m *Model) selectedPath() string {
	doc := m.session.Document()
	if m.selected < 0 || m.selected >= doc.RuleCount() {
		return ""
	}
	return doc.Policy.Rules[m.selected].Path
}

// requireEditable is the single gate on every action that would change the
// document. A file BPE could not parse is browsable, previewable, and
// testable, but not editable, and saying so once here keeps that from
// being re-checked in six places.
func (m *Model) requireEditable() bool {
	if m.session.Editable() {
		return true
	}
	m.problem = m.session.ReadOnlyReason()
	return false
}

func (m *Model) openEditForm() tea.Cmd {
	if !m.requireEditable() {
		return nil
	}
	doc := m.session.Document()
	if m.selected >= doc.RuleCount() {
		return nil
	}
	ref := hclpolicy.RuleRef(m.selected)
	rule, err := doc.Rule(ref)
	if err != nil {
		m.problem = err.Error()
		return nil
	}
	src, err := doc.RuleSource(ref)
	if err != nil {
		m.problem = err.Error()
		return nil
	}
	m.form = newRuleForm(ref, rule, src)
	m.screen = screenForm
	return nil
}

func (m *Model) openAddForm() tea.Cmd {
	if !m.requireEditable() {
		return nil
	}
	m.form = newAddForm()
	m.screen = screenForm
	return nil
}

func (m *Model) duplicateSelected() {
	if !m.requireEditable() || m.session.Document().RuleCount() == 0 {
		return
	}
	next, err := m.session.Document().DuplicateRule(hclpolicy.RuleRef(m.selected))
	if err != nil {
		m.problem = err.Error()
		return
	}
	if err := m.session.Commit(next); err != nil {
		m.problem = err.Error()
		return
	}
	// The copy is appended, so it is the last rule. Selecting it is what
	// makes "duplicate then edit" the obvious next move.
	m.selected = m.session.Document().RuleCount() - 1
	m.status = "duplicated — the copy kept the original's comments and any content BPE does not model"
}

func (m *Model) requestRemove() {
	if !m.requireEditable() || m.session.Document().RuleCount() == 0 {
		return
	}
	ref := hclpolicy.RuleRef(m.selected)
	_, removal, err := m.session.Document().RemoveRule(ref)
	if err != nil {
		m.problem = err.Error()
		return
	}
	m.pendingRef = ref
	m.pendingRemoval = removal
	m.dialog = dialogRemove
}

func (m *Model) confirmRemove() {
	next, _, err := m.session.Document().RemoveRule(m.pendingRef)
	if err != nil {
		m.problem = err.Error()
		return
	}
	if err := m.session.Commit(next); err != nil {
		m.problem = err.Error()
		return
	}
	m.selected = min(m.selected, max(0, m.session.Document().RuleCount()-1))
	m.status = fmt.Sprintf("removed %s", displayPath(m.pendingRemoval.Path))
}

func (m *Model) handleFormKey(key tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	result, cmd := m.form.Update(key)
	switch result {
	case formCancel:
		m.screen = screenEditor
		m.status = "edit cancelled — nothing was changed"
	case formApply:
		m.applyForm()
	case formOpenAllowed:
		m.params = newParamForm(rowAllowed, m.form.path.Value(), m.form.allowed.values)
		m.screen = screenParams
	case formOpenDenied:
		m.params = newParamForm(rowDenied, m.form.path.Value(), m.form.denied.values)
		m.screen = screenParams
	}
	return m, cmd
}

// applyForm turns the finished form into document bytes.
//
// An edit that produces no change is reported as such rather than
// committed: it would be a no-op on the bytes anyway, but saying so is
// what tells the user their rule really was left alone.
func (m *Model) applyForm() {
	if m.form.isNew {
		rule, err := m.form.rule()
		if err != nil {
			m.form.problem = err.Error()
			return
		}
		next, err := m.session.Document().AddRule(rule)
		if err != nil {
			m.form.problem = err.Error()
			return
		}
		if err := m.session.Commit(next); err != nil {
			m.form.problem = err.Error()
			return
		}
		m.selected = m.session.Document().RuleCount() - 1
		m.screen = screenEditor
		m.status = "rule added"
		return
	}

	newPath, edits, err := m.form.edits()
	if err != nil {
		m.form.problem = err.Error()
		return
	}
	if newPath == nil && len(edits) == 0 {
		m.screen = screenEditor
		m.status = "no changes — the rule was left exactly as it was"
		return
	}

	next, err := m.session.Document().EditRule(m.form.ref, newPath, edits)
	if err != nil {
		m.form.problem = err.Error()
		return
	}
	if err := m.session.Commit(next); err != nil {
		m.form.problem = err.Error()
		return
	}
	m.screen = screenEditor
	m.status = fmt.Sprintf("updated rule %d", int(m.form.ref)+1)
}

func (m *Model) handleParamsKey(key tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	result, cmd := m.params.Update(key)
	switch result {
	case paramApply:
		m.form.setParams(m.params.kind, m.params.values)
		m.screen = screenForm
	case paramCancel:
		m.screen = screenForm
	}
	return m, cmd
}

func (m *Model) handleScrollKey(key tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch key.String() {
	case "esc", "q":
		m.screen = screenEditor
		m.reviewingConflict = false
		return m, nil
	case "ctrl+c":
		return m.requestQuit()
	case "ctrl+s":
		if m.screen == screenReview && !m.reviewingConflict {
			return m, m.save()
		}
	}

	var cmd tea.Cmd
	m.viewport, cmd = m.viewport.Update(key)
	return m, cmd
}

func (m *Model) handleSavePathKey(key tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch key.String() {
	case "esc":
		m.screen = screenReview
		return m, nil
	case "enter":
		path := strings.TrimSpace(m.savePath.Value())
		if err := m.session.SaveAs(path); err != nil {
			m.problem = saveErrorMessage(err)
			return m, nil
		}
		m.screen = screenEditor
		m.status = "written to " + m.session.Filename()
		return m, nil
	}

	var cmd tea.Cmd
	m.savePath, cmd = m.savePath.Update(key)
	return m, cmd
}

func (m *Model) handleDialogKey(key tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	answer := key.String()

	switch m.dialog {
	case dialogDiscard:
		switch answer {
		case "d":
			return m, tea.Quit
		case "s":
			m.dialog = dialogNone
			m.showReview()
		case "esc", "c", "n":
			m.dialog = dialogNone
		}

	case dialogRemove:
		switch answer {
		case "y", "enter":
			m.dialog = dialogNone
			m.confirmRemove()
		case "esc", "n":
			m.dialog = dialogNone
			m.status = "removal cancelled"
		}

	case dialogConflict:
		switch answer {
		case "r":
			m.dialog = dialogNone
			if err := m.session.Reload(); err != nil {
				m.problem = err.Error()
				return m, nil
			}
			m.selected = min(m.selected, max(0, m.session.Document().RuleCount()-1))
			m.screen = screenEditor
			m.status = "reloaded from disk — your unsaved edits were discarded"
		case "v":
			m.dialog = dialogNone
			m.showConflictDiff()
		case "esc", "c":
			m.dialog = dialogNone
		}
	}
	return m, nil
}

// requestQuit asks before throwing away unsaved work, and leaves
// immediately when there is none.
func (m *Model) requestQuit() (tea.Model, tea.Cmd) {
	if !m.session.Dirty() {
		return m, tea.Quit
	}
	m.dialog = dialogDiscard
	return m, nil
}

// save writes the document, routing a document with no file of its own to
// the save-path screen and a conflict to the conflict dialog.
func (m *Model) save() tea.Cmd {
	if !m.session.HasFile() {
		m.screen = screenSavePath
		return m.savePath.Focus()
	}

	err := m.session.Save()
	switch {
	case err == nil:
		m.screen = screenEditor
		m.status = "saved " + m.session.Filename()
	case errors.Is(err, fileio.ErrConflict):
		// The in-memory document is untouched by a refused write, so the
		// user's edits are still there to be reconciled.
		m.dialog = dialogConflict
	default:
		var post *fileio.PostReplacementError
		if errors.As(err, &post) {
			// The file was replaced; only the durability step afterward
			// failed. Reporting this as a failed save would be wrong.
			m.screen = screenEditor
			m.status = "saved " + m.session.Filename()
			m.problem = "the file was written, but confirming it survives an immediate crash failed: " + err.Error()
			return nil
		}
		m.problem = saveErrorMessage(err)
	}
	return nil
}

func saveErrorMessage(err error) string {
	switch {
	case errors.Is(err, ErrFileExists):
		return err.Error() + " — BPE will not replace a file it has not read; choose another name"
	case errors.Is(err, fileio.ErrSymlink):
		return "that path is a symlink; BPE will not write through one"
	case errors.Is(err, fileio.ErrHardLinked):
		return "that file has more than one hard link; BPE will not replace it"
	default:
		return err.Error()
	}
}

func (m *Model) showPreview() {
	m.screen = screenPreview
	m.viewport.SetContent(string(m.session.Current()))
	m.viewport.GotoTop()
}

func (m *Model) showDiagnostics() {
	m.screen = screenDiagnostics
	m.viewport.SetContent(m.renderDiagnostics())
	m.viewport.GotoTop()
}

func (m *Model) showHelp() {
	m.screen = screenHelp
	m.viewport.SetContent(m.renderHelp())
	m.viewport.GotoTop()
}

// showReview builds the save review: what is about to be written, against
// what was read.
func (m *Model) showReview() {
	if !m.session.Dirty() {
		m.status = "nothing to save — the document matches the file on disk"
		return
	}
	m.reviewingConflict = false
	m.review = Diff(m.session.Original(), m.session.Current())
	m.screen = screenReview
	m.viewport.SetContent(m.renderDiff(m.review))
	m.viewport.GotoTop()
}

// showConflictDiff answers "what changed underneath me?" after a refused
// save, by diffing the file as it is on disk right now against the
// document in the editor.
func (m *Model) showConflictDiff() {
	disk, err := m.session.OnDisk()
	if err != nil {
		m.problem = err.Error()
		return
	}
	m.reviewingConflict = true
	m.review = Diff(disk, m.session.Current())
	m.screen = screenReview
	m.viewport.SetContent(m.renderDiff(m.review))
	m.viewport.GotoTop()
}
