package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"

	"github.com/kevinpinscoe/bao-policy-editor/internal/baoclient"
	"github.com/kevinpinscoe/bao-policy-editor/internal/config"
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
	screenConnect
	screenBrowse
	screenRemoteName
)

// dialog is a modal question laid over whatever screen is showing.
//
// Modal questions are reserved for decisions that are irreversible or
// destructive: discarding unsaved work, deleting a rule along with its
// comments, resolving a file or a policy that changed underneath the
// editor, and removing a policy from a server. Everything else is a screen
// the user can back out of with Escape.
type dialog int

const (
	dialogNone dialog = iota
	dialogDiscard
	dialogRemove
	dialogConflict
	dialogRemoteConflict
	dialogRemoteDiscard
	dialogRemoteDelete
	dialogRemoteNameTaken
	dialogRemoteDraftLoss
)

// reviewKind says what the review screen is currently showing, which
// decides what writing from it would mean.
type reviewKind int

const (
	// reviewSave is the pending save: the document as loaded against the
	// document as it stands.
	reviewSave reviewKind = iota
	// reviewDiskConflict is the local file as it is on disk right now
	// against the document in the editor.
	reviewDiskConflict
	// reviewServerConflict is the policy as it is on the server right now
	// against the document in the editor, shown after a check-and-set
	// rejection. Writing from here retries with the revision that fetch
	// reported — which is why it is reachable only after the diff has been
	// put on screen.
	reviewServerConflict
)

// ModelOptions carries everything the editor needs beyond the document
// itself. A zero value is a perfectly good local-only editor.
type ModelOptions struct {
	// Context bounds every remote operation. Commands derive their own
	// cancellable child from it, so cancelling the program cancels
	// whatever is in flight.
	Context context.Context

	// Config is BPE's already-resolved configuration, used as-is for
	// remote connections.
	Config config.Config

	// NewStore builds the OpenBao client. It is called only when remote
	// mode is actually entered — never for a local session.
	NewStore StoreFactory

	// StartRemote asks the editor to open on the remote browser.
	StartRemote bool
}

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
	// reviewKind says which comparison it is.
	review     []DiffLine
	reviewKind reviewKind

	// pendingRemoval describes the rule the remove dialog is asking about.
	pendingRemoval hclpolicy.Removal
	pendingRef     hclpolicy.RuleRef

	// --- remote state ---

	ctx         context.Context
	cfg         config.Config
	newStore    StoreFactory
	startRemote bool

	// store is the connected server, nil until remote mode is entered and
	// a connection succeeds.
	store RemoteStore

	// warnings are the connection's security warnings, kept for as long as
	// the connection lasts so a disabled certificate check cannot be
	// scrolled past or forgotten.
	warnings []string

	connect *connectForm
	browse  *policyBrowser

	// remoteName is the name field for a policy being created.
	remoteName textinput.Model

	// deleteTarget and deleteConfirm drive the delete dialog, which
	// requires the policy's exact name to be typed out.
	deleteTarget  string
	deleteConfirm textinput.Model

	// takenName is the policy name a create was refused for.
	takenName string

	// pendingRevision is the revision a re-read of the server reported
	// while showing the conflict diff.
	//
	// It is held here rather than adopted onto the session, and it is
	// consumed only by ctrl+s on the reviewServerConflict screen. Writing
	// it onto the session at the moment the diff was *displayed* would
	// mean that viewing the server's version, pressing Escape, and later
	// doing an ordinary save would send that newer revision — an overwrite
	// the user never confirmed from the conflict review, arrived at by
	// looking. Leaving that review clears this, so the next ordinary save
	// carries the stale revision again and the server refuses it again,
	// which is the correct answer.
	pendingRevision *baoclient.Revision

	// busy and busyLabel report an in-flight remote operation; op,
	// lastOp and opCancel identify and cancel it. See remote.go.
	busy      bool
	busyLabel string
	op        remoteOpID
	lastOp    remoteOpID
	opCancel  context.CancelFunc

	status  string
	problem string
}

// New builds the editor model around an open session, with no remote
// capability configured.
func New(session *Session) *Model {
	return NewWithOptions(session, ModelOptions{})
}

// NewWithOptions builds the editor model with remote support available.
func NewWithOptions(session *Session, opts ModelOptions) *Model {
	ctx := opts.Context
	if ctx == nil {
		ctx = context.Background()
	}
	factory := opts.NewStore
	if factory == nil {
		factory = DefaultStoreFactory
	}

	m := &Model{
		session: session,
		styles:  DefaultStyles(),
		// A sane starting size so the first render is not degenerate if
		// the terminal is slow to report its own.
		width:       80,
		height:      24,
		viewport:    viewport.New(viewport.WithWidth(80), viewport.WithHeight(18)),
		savePath:    newInput("path to write the policy to"),
		ctx:         ctx,
		cfg:         opts.Config,
		newStore:    factory,
		startRemote: opts.StartRemote,
		remoteName:  newInput("policy name, e.g. team-a-readonly"),
	}
	m.viewport.MouseWheelEnabled = true
	// Fill the viewport's full height with blank lines rather than
	// emitting only the lines it has. Without this a short preview leaves
	// whatever the previous screen drew showing underneath it.
	m.viewport.FillHeight = true
	m.savePath.SetValue(SuggestSavePath())
	m.deleteConfirm = newInput("type the policy name to confirm")

	// If the session arrived already backed by a remote policy, the store
	// that read it is the connection this model is working over.
	if store, ok := session.RemoteStore(); ok {
		m.adoptStore(store)
	}
	return m
}

// Init implements tea.Model.
//
// `bpe --remote` starts here: with an address already configured there is
// nothing to ask, so it connects straight away and lands on the browser;
// without one it opens the connect screen instead. A local session returns
// no command at all, which is what keeps `bpe` and `bpe policy.hcl` from
// touching the network.
func (m *Model) Init() tea.Cmd {
	if !m.startRemote {
		return nil
	}
	return m.enterRemote()
}

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

	case remoteConnectedMsg:
		return m.handleConnected(msg)

	case remoteListedMsg:
		return m.handleListed(msg)

	case remoteOpenedMsg:
		return m.handleOpened(msg)

	case remoteServerCopyMsg:
		return m.handleServerCopy(msg)

	case remoteWroteMsg:
		return m.handleWrote(msg)

	case remoteDeletedMsg:
		return m.handleDeleted(msg)

	case remoteErrMsg:
		return m.handleRemoteErr(msg)
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
	if m.dialog != dialogNone || m.screen != screenEditor || m.busy {
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
	// A remote operation in flight takes only two keys: Escape cancels it
	// and Ctrl+C cancels it and leaves. Everything else is swallowed
	// rather than queued, so a keystroke pressed during a slow request
	// cannot act on a screen that is about to be replaced.
	if m.busy {
		switch key.String() {
		case "esc":
			m.cancelOp()
			m.status = "cancelled — nothing was changed"
			return m, nil
		case "ctrl+c":
			m.cancelOp()
			return m.requestQuit()
		}
		return m, nil
	}

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
	case screenConnect:
		return m.handleConnectKey(key)
	case screenBrowse:
		return m.handleBrowseKey(key)
	case screenRemoteName:
		return m.handleRemoteNameKey(key)
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
	case "r":
		return m, m.enterRemote()
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
		m.leaveReview()
		m.screen = screenEditor
		return m, nil
	case "ctrl+c":
		return m.requestQuit()
	case "ctrl+s":
		switch {
		case m.screen != screenReview:
			// Not a review screen; fall through to scrolling.
		case m.reviewKind == reviewSave:
			return m, m.save()
		case m.reviewKind == reviewServerConflict:
			return m, m.retryAfterConflictReview()
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

	case dialogRemoteConflict:
		return m.handleRemoteConflictKey(answer)

	case dialogRemoteDiscard:
		switch answer {
		case "y", "enter":
			m.dialog = dialogNone
			return m, m.reloadFromServer(true)
		case "esc", "n":
			// Back to the conflict question rather than out of the
			// resolution entirely — the conflict is still unresolved.
			m.dialog = dialogRemoteConflict
		}

	case dialogRemoteNameTaken:
		switch answer {
		case "o":
			// Opening the server's copy replaces the document, which for a
			// refused create means throwing away the draft that was just
			// written. That is a second, separate loss from the one the
			// name-taken question is about, so it gets its own question —
			// unless there is nothing to lose.
			if m.session.Dirty() {
				m.dialog = dialogRemoteDraftLoss
				return m, nil
			}
			m.dialog = dialogNone
			return m, m.openTakenPolicy()
		case "esc", "c", "n":
			m.dialog = dialogNone
		}

	case dialogRemoteDraftLoss:
		switch answer {
		case "y":
			m.dialog = dialogNone
			return m, m.openTakenPolicy()
		case "esc", "n", "c":
			// Back to the question that led here, with the draft intact.
			m.dialog = dialogRemoteNameTaken
		}

	case dialogRemoteDelete:
		return m.handleRemoteDeleteKey(key)
	}
	return m, nil
}

func (m *Model) handleRemoteConflictKey(answer string) (tea.Model, tea.Cmd) {
	switch answer {
	case "v":
		m.dialog = dialogNone
		return m, m.reloadFromServer(false)
	case "r":
		m.dialog = dialogRemoteDiscard
	case "esc", "c":
		m.dialog = dialogNone
		m.status = "your edits are unchanged — the server was not written to"
	}
	return m, nil
}

// handleRemoteDeleteKey drives the delete confirmation, which is a typed
// name rather than a single key.
//
// A `y`/`n` question is the right weight for removing a rule from a
// document that has not been saved yet. It is the wrong weight for
// removing a policy from a live server, where the endpoint offers no
// check-and-set, nothing is staged, and there is no undo — so the
// confirmation is the policy's own name, typed out.
func (m *Model) handleRemoteDeleteKey(key tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch key.String() {
	case "esc":
		m.dialog = dialogNone
		m.status = "deletion cancelled — nothing was removed"
		return m, nil
	case "enter":
		if strings.TrimSpace(m.deleteConfirm.Value()) != m.deleteTarget {
			m.problem = "that does not match " + m.deleteTarget + " — nothing was deleted"
			return m, nil
		}
		if m.store == nil {
			m.problem = "not connected to a server"
			return m, nil
		}
		m.dialog = dialogNone
		m.problem = ""
		return m, m.deletePolicy(m.store, m.deleteTarget)
	}

	var cmd tea.Cmd
	m.deleteConfirm, cmd = m.deleteConfirm.Update(key)
	return m, cmd
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

// save writes the document to wherever its backing says it belongs.
//
// The three backings take three different paths, chosen from the backing
// itself rather than from which fields happen to be populated: a remote
// policy goes to the server as a cancellable command, a local file is
// written in place, and a document with neither is routed to the
// save-path screen.
func (m *Model) save() tea.Cmd {
	switch m.session.Kind() {
	case backingRemote:
		return m.writePolicy()

	case backingLocal:
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

	default:
		m.screen = screenSavePath
		return m.savePath.Focus()
	}
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

// leaveReview resets the review screen's state, including the revision a
// conflict review was holding.
//
// Clearing that revision is the point. It is only ever a licence to write,
// granted by having the server's version on screen, and it expires when
// that screen is left — so backing out of a conflict review and saving
// normally afterwards sends the stale revision and is refused again,
// rather than quietly completing the overwrite the user declined to
// confirm.
func (m *Model) leaveReview() {
	m.reviewKind = reviewSave
	m.pendingRevision = nil
}

// retryAfterConflictReview writes the user's version over the server's,
// using the revision the conflict review's own re-read reported.
//
// It is reachable only from that review screen, and only while the
// revision it fetched is still held — which together are what make this
// the reviewed, confirmed overwrite rather than an incidental one.
func (m *Model) retryAfterConflictReview() tea.Cmd {
	if m.pendingRevision == nil {
		// Nothing fetched, or the review was already left and re-entered
		// some other way. Refusing is right: without a re-read there is
		// nothing newer to write against.
		m.problem = "re-read the policy before retrying — press esc, save again, and choose to see what is on the server"
		return nil
	}
	if err := m.session.AdoptRemoteRevision(*m.pendingRevision); err != nil {
		m.problem = err.Error()
		return nil
	}
	m.leaveReview()
	return m.writePolicy()
}

// showReview builds the save review: what is about to be written, against
// what was read.
func (m *Model) showReview() {
	if !m.session.Dirty() {
		m.status = "nothing to save — the document matches " + m.unchangedAgainst()
		return
	}
	m.leaveReview()
	m.review = Diff(m.session.Original(), m.session.Current())
	m.screen = screenReview
	m.viewport.SetContent(m.renderDiff(m.review))
	m.viewport.GotoTop()
}

func (m *Model) unchangedAgainst() string {
	if m.session.IsRemote() {
		return "the policy on the server"
	}
	return "the file on disk"
}

// showConflictDiff answers "what changed underneath me?" after a refused
// local save, by diffing the file as it is on disk right now against the
// document in the editor.
func (m *Model) showConflictDiff() {
	disk, err := m.session.OnDisk()
	if err != nil {
		m.problem = err.Error()
		return
	}
	m.leaveReview()
	m.reviewKind = reviewDiskConflict
	m.review = Diff(disk, m.session.Current())
	m.screen = screenReview
	m.viewport.SetContent(m.renderDiff(m.review))
	m.viewport.GotoTop()
}

// --- remote flow ---

// enterRemote opens remote mode: straight to a connection when an address
// is already configured, and to the connect screen when one is not.
//
// This is the only path to a client. Nothing above it is reached by
// opening, editing, or saving a local file.
func (m *Model) enterRemote() tea.Cmd {
	if m.store != nil {
		m.screen = screenBrowse
		if m.browse == nil {
			m.browse = newPolicyBrowser(nil)
		}
		return m.refreshPolicies(m.store)
	}

	m.connect = newConnectForm(m.cfg)
	m.screen = screenConnect

	if strings.TrimSpace(m.cfg.Address) != "" {
		return m.connectRemote(m.cfg)
	}
	return m.connect.moveFocus(0)
}

func (m *Model) handleConnectKey(key tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	result, cmd := m.connect.Update(key)
	switch result {
	case connectCancel:
		m.screen = screenEditor
		m.status = "not connected — your document is unchanged"
		return m, nil
	case connectSubmit:
		m.problem = ""
		return m, m.connectRemote(m.connect.apply(m.cfg))
	}
	return m, cmd
}

func (m *Model) handleBrowseKey(key tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	result, cmd := m.browse.Update(key)
	switch result {
	case browseBack:
		m.screen = screenEditor
		return m, nil
	case browseOpen:
		if m.store == nil {
			return m, nil
		}
		if m.session.Dirty() {
			m.problem = "this document has unsaved changes — save or discard them before opening another policy"
			return m, nil
		}
		return m, m.openPolicy(m.store, m.browse.current())
	case browseNew:
		m.remoteName.SetValue("")
		m.screen = screenRemoteName
		return m, m.remoteName.Focus()
	case browseDelete:
		m.deleteTarget = m.browse.current()
		m.deleteConfirm.SetValue("")
		m.dialog = dialogRemoteDelete
		return m, m.deleteConfirm.Focus()
	case browseRefresh:
		if m.store == nil {
			return m, nil
		}
		return m, m.refreshPolicies(m.store)
	}
	return m, cmd
}

func (m *Model) handleRemoteNameKey(key tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch key.String() {
	case "esc":
		m.screen = screenBrowse
		return m, nil
	case "enter":
		name := strings.TrimSpace(m.remoteName.Value())
		if name == "" {
			m.problem = "a policy name is required"
			return m, nil
		}
		if m.store == nil {
			m.problem = "not connected to a server"
			return m, nil
		}
		if m.session.Dirty() {
			m.problem = "this document has unsaved changes — save or discard them first"
			return m, nil
		}
		m.adoptSession(NewRemoteSession(m.store, name))
		m.screen = screenEditor
		m.status = "new policy " + name + " — it is not on the server until you save"
		return m, nil
	}

	var cmd tea.Cmd
	m.remoteName, cmd = m.remoteName.Update(key)
	return m, cmd
}

// reloadFromServer re-reads the policy behind the session. discard says
// whether the user asked to replace their edits with the server's copy or
// only to look at it.
func (m *Model) reloadFromServer(discard bool) tea.Cmd {
	store, ok := m.session.RemoteStore()
	if !ok {
		m.problem = "this document is not backed by a remote policy"
		return nil
	}
	name, _ := m.session.RemoteName()
	return m.fetchServerCopy(store, name, discard)
}

// openTakenPolicy reads the policy a create was refused for, so the user
// can edit the one that is actually there.
//
// This is a read, and the session it produces is an existing policy with a
// real revision — so the next write is an update that sends that revision
// as `cas`. The refused create is not converted into an update; the user
// is handed the real policy and decides.
func (m *Model) openTakenPolicy() tea.Cmd {
	if m.store == nil || m.takenName == "" {
		return nil
	}
	return m.openPolicy(m.store, m.takenName)
}

// adoptStore records the connection and its security warnings.
func (m *Model) adoptStore(store RemoteStore) {
	m.store = store
	m.warnings = store.SecurityWarnings()
}

// adoptSession replaces the document being edited, resetting the
// per-document view state that would otherwise point into the old one.
func (m *Model) adoptSession(session *Session) {
	m.session = session
	m.selected = 0
	m.review = nil
	m.problem = ""
	m.leaveReview()
}

func (m *Model) handleConnected(msg remoteConnectedMsg) (tea.Model, tea.Cmd) {
	if !m.accept(msg.op) {
		return m, nil
	}
	m.adoptStore(msg.store)
	m.browse = newPolicyBrowser(msg.names)
	m.screen = screenBrowse
	m.problem = ""
	m.status = fmt.Sprintf("connected to %s — %d %s",
		msg.store.Address(), len(msg.names), policyWord(len(msg.names)))
	return m, nil
}

func (m *Model) handleListed(msg remoteListedMsg) (tea.Model, tea.Cmd) {
	if !m.accept(msg.op) {
		return m, nil
	}
	if m.browse == nil {
		m.browse = newPolicyBrowser(msg.names)
	} else {
		m.browse.setNames(msg.names)
	}
	m.screen = screenBrowse
	m.status = fmt.Sprintf("%d %s", len(msg.names), policyWord(len(msg.names)))
	return m, nil
}

func (m *Model) handleOpened(msg remoteOpenedMsg) (tea.Model, tea.Cmd) {
	if !m.accept(msg.op) {
		return m, nil
	}
	if m.store == nil {
		return m, nil
	}
	session, err := OpenRemoteSession(m.store, msg.policy)
	if err != nil {
		m.problem = "that policy could not be parsed: " + err.Error()
		return m, nil
	}
	m.adoptSession(session)
	m.screen = screenEditor
	m.status = "opened " + msg.policy.Name + " from " + m.store.Address()
	if !msg.policy.Revision.HasVersion {
		m.problem = "this server did not report a version for " + msg.policy.Name +
			"; BPE will refuse to update it rather than write without conflict protection"
	}
	return m, nil
}

func (m *Model) handleServerCopy(msg remoteServerCopyMsg) (tea.Model, tea.Cmd) {
	if !m.accept(msg.op) {
		return m, nil
	}

	if msg.discard {
		if err := m.session.ReloadRemote(msg.policy); err != nil {
			m.problem = err.Error()
			return m, nil
		}
		m.selected = min(m.selected, max(0, m.session.Document().RuleCount()-1))
		m.leaveReview()
		m.screen = screenEditor
		m.status = "reloaded " + msg.policy.Name + " from the server — your unsaved edits were discarded"
		return m, nil
	}

	// Looking, not taking. The session is not touched at all — not its
	// bytes and not its revision. The revision this read reported is held
	// aside, and only ctrl+s from the screen below will use it.
	rev := msg.policy.Revision
	m.pendingRevision = &rev
	m.reviewKind = reviewServerConflict
	m.review = Diff([]byte(msg.policy.Body), m.session.Current())
	m.screen = screenReview
	m.viewport.SetContent(m.renderDiff(m.review))
	m.viewport.GotoTop()
	return m, nil
}

func (m *Model) handleWrote(msg remoteWroteMsg) (tea.Model, tea.Cmd) {
	if !m.accept(msg.op) {
		return m, nil
	}
	if err := m.session.MarkRemoteSaved(msg.result); err != nil {
		m.problem = err.Error()
		return m, nil
	}

	name, _ := m.session.RemoteName()
	verb := "updated"
	if msg.created {
		verb = "created"
	}
	m.screen = screenEditor
	m.status = verb + " " + name + " on the server"

	if !msg.result.Revision.HasVersion {
		m.problem = "the server reported no new version for " + name +
			"; re-open it before the next update, which will otherwise be refused for lack of conflict protection"
	}
	if len(msg.result.Warnings) > 0 {
		m.problem = strings.Join(msg.result.Warnings, "; ")
	}

	// A create adds a name the browser has not seen; it is added to the
	// listing in place rather than by re-listing the server.
	//
	// Re-listing would work, but its reply moves to the browser and
	// replaces the status line with a policy count — so the screen the
	// user is on and the confirmation they just earned would both be
	// thrown away by a refresh they never asked for. The listing is
	// brought up to date by `r`, and by opening the browser, which is
	// where a stale one would actually matter.
	if msg.created && m.browse != nil {
		m.browse.add(name)
	}
	return m, nil
}

func (m *Model) handleDeleted(msg remoteDeletedMsg) (tea.Model, tea.Cmd) {
	if !m.accept(msg.op) {
		return m, nil
	}
	m.status = "deleted " + msg.name + " from the server"

	// If the deleted policy is the one being edited, the document is now
	// an unsaved policy that no longer exists on the server. Saying so —
	// and recording it on the backing — is what keeps the next save from
	// being an update with a revision for something that is gone.
	if name, ok := m.session.RemoteName(); ok && name == msg.name {
		if err := m.session.MarkRemoteDeleted(); err == nil {
			m.problem = msg.name + " no longer exists on the server; saving this document would create it again"
		}
	}

	// Removed in place, for the same reason a create is added in place:
	// the status line here is the confirmation that the delete happened,
	// and a refresh would overwrite it with a count.
	if m.browse != nil {
		m.browse.remove(msg.name)
	}
	return m, nil
}

// handleRemoteErr turns a failed remote operation into something on
// screen, without ever touching the document.
//
// Every branch here leaves the session, its bytes, and its unsaved edits
// exactly as they were. A connection that failed, a token that was
// rejected, a certificate that did not verify, and a write the server
// refused are all reported and nothing else — Kevin's instruction,
// 2026-09-18.
func (m *Model) handleRemoteErr(msg remoteErrMsg) (tea.Model, tea.Cmd) {
	if !m.accept(msg.op) {
		return m, nil
	}

	switch {
	case errors.Is(msg.err, context.Canceled):
		m.status = "cancelled — nothing was changed"
		return m, nil

	case errors.Is(msg.err, baoclient.ErrConflict) && msg.created:
		// A create that collided stays a create that collided. Turning it
		// into an update here would overwrite a policy this session has
		// never read, with no revision to check it against — which is the
		// single most dangerous thing this screen could do quietly.
		m.takenName, _ = m.session.RemoteName()
		m.dialog = dialogRemoteNameTaken
		return m, nil

	case errors.Is(msg.err, baoclient.ErrConflict):
		m.dialog = dialogRemoteConflict
		return m, nil

	case errors.Is(msg.err, baoclient.ErrConflictProtectionUnsupported):
		m.problem = "this server did not report a version for this policy, so BPE refused to update it — " +
			"re-open the policy, or use a server that reports policy versions. Your edits are unchanged."
		return m, nil
	}

	m.problem = msg.during + ": " + msg.err.Error()

	// A failure while connecting leaves nothing connected, so the connect
	// screen is where the user can do something about it. A failure once
	// connected leaves them wherever they were.
	if m.store == nil && m.screen != screenConnect {
		if m.connect == nil {
			m.connect = newConnectForm(m.cfg)
		}
		m.screen = screenConnect
	}
	return m, nil
}
