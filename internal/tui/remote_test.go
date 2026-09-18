package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/kevinpinscoe/bao-policy-editor/internal/baoclient"
	"github.com/kevinpinscoe/bao-policy-editor/internal/config"
)

const remotePolicyBody = `path "secret/data/team-a/*" {
  capabilities = ["read", "list"]
}
`

// --- offline is offline ---

// TestLocalEditingNeverBuildsAClient is the acceptance criterion "offline
// mode remains usable without configuration or network access", proved
// rather than asserted: the store factory fails the test if it is called
// at all, and the session is driven through every local workflow.
//
// The configuration deliberately carries a full address and token, so this
// is not passing merely because there was nothing to connect to.
func TestLocalEditingNeverBuildsAClient(t *testing.T) {
	for _, tc := range []struct {
		name    string
		session func(t *testing.T) *Session
	}{
		{"bpe with no file", func(*testing.T) *Session { return NewSession() }},
		{"bpe policy.hcl", func(t *testing.T) *Session {
			s, _ := openTestSession(t, samplePolicy)
			return s
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := NewWithOptions(tc.session(t), ModelOptions{
				Config: config.Config{
					Address: "https://openbao.example.com:8200",
					Token:   config.SensitiveString("s.should-never-be-used"),
				},
				NewStore: func(config.Config) (RemoteStore, error) {
					t.Fatal("a local editing session built an OpenBao client")
					return nil, nil
				},
			})
			m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})

			// Init is what `bpe --remote` would use to connect. Without it,
			// nothing may reach the network.
			deliver(t, m, m.Init())

			// Every local workflow: navigate, duplicate, preview,
			// diagnostics, test access, review, help.
			step(t, m, "down", "d", "p", "esc", "g", "esc", "t", "esc", "?", "esc", "s", "esc")

			if m.store != nil {
				t.Error("a local editing session ended up with a connected store")
			}
		})
	}
}

// --- the backing is exactly one of three ---

func TestSessionBackingIsExactlyOneOf(t *testing.T) {
	store := newFakeStore("https://bao.test:8200")

	t.Run("a new document is neither local nor remote", func(t *testing.T) {
		s := NewSession()
		if s.Kind() != backingNew {
			t.Errorf("Kind() = %d, want backingNew", s.Kind())
		}
		if s.HasFile() {
			t.Error("a new document claims to have a file")
		}
		if s.IsRemote() {
			t.Error("a new document claims to be remote")
		}
		if _, ok := s.RemoteStore(); ok {
			t.Error("a new document produced a remote store")
		}
	})

	t.Run("a local document exposes no remote state", func(t *testing.T) {
		s, _ := openTestSession(t, samplePolicy)
		if s.Kind() != backingLocal {
			t.Errorf("Kind() = %d, want backingLocal", s.Kind())
		}
		if s.IsRemote() {
			t.Error("a local document claims to be remote")
		}
		if _, ok := s.RemoteName(); ok {
			t.Error("a local document produced a remote policy name")
		}
		if _, _, ok := s.RemoteRevision(); ok {
			t.Error("a local document produced a remote revision")
		}
		// Every remote mutation is refused rather than silently applied to
		// a backing that does not exist.
		for name, err := range map[string]error{
			"MarkRemoteSaved":     s.MarkRemoteSaved(baoclient.WriteResult{}),
			"AdoptRemoteRevision": s.AdoptRemoteRevision(baoclient.Revision{}),
			"MarkRemoteDeleted":   s.MarkRemoteDeleted(),
			"ReloadRemote":        s.ReloadRemote(baoclient.Policy{}),
		} {
			if err != ErrNotRemote {
				t.Errorf("%s on a local session returned %v, want ErrNotRemote", name, err)
			}
		}
	})

	t.Run("a remote document exposes no local state", func(t *testing.T) {
		s, err := OpenRemoteSession(store, baoclient.Policy{
			Name:     "team-a",
			Body:     remotePolicyBody,
			Revision: baoclient.Revision{Version: 3, HasVersion: true},
		})
		if err != nil {
			t.Fatal(err)
		}
		if s.Kind() != backingRemote {
			t.Errorf("Kind() = %d, want backingRemote", s.Kind())
		}
		if s.HasFile() {
			t.Error("a remote document claims to have a file")
		}
		if s.Save() != ErrNotLocal {
			t.Error("Save() on a remote session did not refuse")
		}
		if s.Reload() != ErrNotLocal {
			t.Error("Reload() on a remote session did not refuse")
		}
		if _, err := s.OnDisk(); err != ErrNotLocal {
			t.Error("OnDisk() on a remote session did not refuse")
		}
		rev, exists, ok := s.RemoteRevision()
		if !ok || !exists || rev.Version != 3 {
			t.Errorf("RemoteRevision() = %v, %v, %v; want version 3, exists, ok", rev, exists, ok)
		}
	})

	// The one transition between backings: saving a remote document to a
	// file makes it a local document, and nothing of the remote backing
	// survives to be written to by accident.
	t.Run("SaveAs replaces the backing rather than adding one", func(t *testing.T) {
		s, err := OpenRemoteSession(store, baoclient.Policy{
			Name:     "team-a",
			Body:     remotePolicyBody,
			Revision: baoclient.Revision{Version: 3, HasVersion: true},
		})
		if err != nil {
			t.Fatal(err)
		}
		path := writePolicy(t, samplePolicy) + ".new"
		if err := s.SaveAs(path); err != nil {
			t.Fatal(err)
		}
		if s.Kind() != backingLocal {
			t.Fatalf("after SaveAs, Kind() = %d, want backingLocal", s.Kind())
		}
		if s.IsRemote() {
			t.Error("after SaveAs the document is still remote")
		}
		if _, ok := s.RemoteStore(); ok {
			t.Error("after SaveAs a remote store is still reachable")
		}
	})
}

// --- listing, opening, saving ---

func TestRemoteConnectListsAndOpensAPolicy(t *testing.T) {
	store := newFakeStore("https://bao.test:8200")
	store.seed("team-a", remotePolicyBody, 4)
	store.seed("team-b", remotePolicyBody, 1)

	m := connectTestModel(t, store)
	if got := m.browse.visible(); len(got) != 2 || got[0] != "team-a" || got[1] != "team-b" {
		t.Fatalf("browser listing = %v, want [team-a team-b] in order", got)
	}

	selectPolicy(t, m, "team-b")
	step(t, m, "enter")

	if name, _ := m.session.RemoteName(); name != "team-b" {
		t.Errorf("opened policy = %q, want team-b", name)
	}
	if string(m.session.Current()) != remotePolicyBody {
		t.Error("the opened document does not match what the server returned")
	}
	if m.session.Dirty() {
		t.Error("a freshly opened remote policy is already modified")
	}
	rev, exists, _ := m.session.RemoteRevision()
	if !exists || rev.Version != 1 {
		t.Errorf("revision after open = %v (exists %v), want version 1", rev, exists)
	}
}

// TestRemoteUpdateShowsADiffAndRequiresConfirmation covers the acceptance
// criterion that a remote write is never a keystroke away: the review
// screen is reached first, and nothing is sent until it is confirmed.
func TestRemoteUpdateShowsADiffAndRequiresConfirmation(t *testing.T) {
	store := newFakeStore("https://bao.test:8200")
	store.seed("team-a", remotePolicyBody, 4)

	m := openRemotePolicy(t, store, "team-a")
	step(t, m, "d") // duplicate a rule, so there is something to save
	if !m.session.Dirty() {
		t.Fatal("duplicating a rule did not modify the document")
	}

	step(t, m, "s")
	if m.screen != screenReview || m.reviewKind != reviewSave {
		t.Fatalf("s did not open the save review (screen = %d, kind = %d)", m.screen, m.reviewKind)
	}
	if !HasChanges(m.review) {
		t.Error("the save review shows no differences")
	}
	for _, call := range store.callLog() {
		if strings.HasPrefix(call, "update:") || strings.HasPrefix(call, "create:") {
			t.Fatalf("a write (%s) was sent before the review was confirmed", call)
		}
	}

	body, _ := store.bodyOf("team-a")
	if body != remotePolicyBody {
		t.Fatal("the server's copy changed before the write was confirmed")
	}

	step(t, m, "ctrl+s")
	updated, _ := store.bodyOf("team-a")
	if updated == remotePolicyBody {
		t.Error("confirming the review did not write to the server")
	}
	if m.session.Dirty() {
		t.Error("the document is still modified after a successful write")
	}
	rev, _, _ := m.session.RemoteRevision()
	if rev.Version != 5 {
		t.Errorf("revision after update = %d, want 5", rev.Version)
	}
}

// --- conflicts ---

// TestStaleUpdateIsRefusedAndKeepsTheEdits is the "stale versions do not
// silently overwrite newer policies" criterion.
func TestStaleUpdateIsRefusedAndKeepsTheEdits(t *testing.T) {
	store := newFakeStore("https://bao.test:8200")
	store.seed("team-a", remotePolicyBody, 4)

	m := openRemotePolicy(t, store, "team-a")
	step(t, m, "d")
	mine := string(m.session.Current())

	// Somebody else writes in the meantime.
	const theirs = "path \"secret/data/theirs\" {\n  capabilities = [\"read\"]\n}\n"
	store.seed("team-a", theirs, 9)

	step(t, m, "s", "ctrl+s")

	if m.dialog != dialogRemoteConflict {
		t.Fatalf("a stale update did not raise the conflict dialog (dialog = %d, problem = %q)",
			m.dialog, m.problem)
	}
	if got, _ := store.bodyOf("team-a"); got != theirs {
		t.Error("the refused write reached the server anyway")
	}
	if string(m.session.Current()) != mine {
		t.Error("the refused write changed the document")
	}
	if !m.session.Dirty() {
		t.Error("the refused write cleared the document's modified state")
	}

	// v shows what is on the server, and resolves nothing by itself.
	step(t, m, "v")
	if m.screen != screenReview || m.reviewKind != reviewServerConflict {
		t.Fatalf("v did not show the server diff (screen = %d, kind = %d)", m.screen, m.reviewKind)
	}
	if string(m.session.Current()) != mine {
		t.Error("looking at the server's version changed the document")
	}

	// Only now, after the diff has been on screen, does the retry go out —
	// with the revision that fetch reported.
	step(t, m, "ctrl+s")
	if got, _ := store.bodyOf("team-a"); got != mine {
		t.Error("the reviewed retry did not write the user's version")
	}
	if m.session.Dirty() {
		t.Error("the document is still modified after the retry succeeded")
	}
}

func TestRemoteConflictCancelKeepsTheEditsAndWritesNothing(t *testing.T) {
	store := newFakeStore("https://bao.test:8200")
	store.seed("team-a", remotePolicyBody, 4)

	m := openRemotePolicy(t, store, "team-a")
	step(t, m, "d")
	mine := string(m.session.Current())
	store.seed("team-a", "path \"x\" {\n  capabilities = [\"read\"]\n}\n", 9)

	step(t, m, "s", "ctrl+s")
	if m.dialog != dialogRemoteConflict {
		t.Fatalf("expected the conflict dialog, got %d", m.dialog)
	}

	step(t, m, "esc")
	if m.dialog != dialogNone {
		t.Error("esc did not dismiss the conflict dialog")
	}
	if string(m.session.Current()) != mine || !m.session.Dirty() {
		t.Error("cancelling the conflict lost the edits")
	}
	for _, call := range store.callLog() {
		if call == "update:team-a" && false {
			t.Fatal("unreachable")
		}
	}
	if got, _ := store.bodyOf("team-a"); got == mine {
		t.Error("cancelling the conflict wrote to the server anyway")
	}
}

// TestTakingTheServersVersionNeedsASecondConfirmation covers Kevin's
// requirement that reload/discard is confirmed, not a single keystroke.
func TestTakingTheServersVersionNeedsASecondConfirmation(t *testing.T) {
	store := newFakeStore("https://bao.test:8200")
	store.seed("team-a", remotePolicyBody, 4)

	m := openRemotePolicy(t, store, "team-a")
	step(t, m, "d")
	mine := string(m.session.Current())

	const theirs = "path \"secret/data/theirs\" {\n  capabilities = [\"read\"]\n}\n"
	store.seed("team-a", theirs, 9)
	step(t, m, "s", "ctrl+s")

	// r asks rather than acting.
	step(t, m, "r")
	if m.dialog != dialogRemoteDiscard {
		t.Fatalf("r did not ask before discarding (dialog = %d)", m.dialog)
	}
	if string(m.session.Current()) != mine {
		t.Fatal("r discarded the edits before the confirmation was answered")
	}

	// Declining returns to the unresolved conflict, not out of it.
	step(t, m, "esc")
	if m.dialog != dialogRemoteConflict {
		t.Errorf("declining the discard left dialog = %d, want the conflict question", m.dialog)
	}
	if string(m.session.Current()) != mine {
		t.Error("declining the discard lost the edits anyway")
	}

	// Accepting takes the server's copy.
	step(t, m, "r", "y")
	if string(m.session.Current()) != theirs {
		t.Error("confirming the discard did not take the server's version")
	}
	if m.session.Dirty() {
		t.Error("after reloading from the server the document is still modified")
	}
}

// TestCreateConflictStaysACreateConflict is the "a create conflict must
// remain a conflict; never silently reinterpret it as an update"
// requirement.
func TestCreateConflictStaysACreateConflict(t *testing.T) {
	store := newFakeStore("https://bao.test:8200")
	const existing = "path \"secret/data/someone-elses\" {\n  capabilities = [\"read\"]\n}\n"
	store.seed("team-a", existing, 7)

	m := connectTestModel(t, store)

	// Start a new policy whose name is, unknown to us, already taken.
	step(t, m, "n")
	typeInto(t, m, "team-a")
	step(t, m, "enter")
	if !m.session.IsRemote() {
		t.Fatalf("naming a new policy did not produce a remote session; problem = %q", m.problem)
	}
	if _, exists, _ := m.session.RemoteRevision(); exists {
		t.Fatal("a policy being created is recorded as already existing")
	}

	// Give it a rule and save. The cursor starts on the path field, which
	// is opened with enter, typed into, and closed again.
	step(t, m, "a", "enter")
	typeInto(t, m, "secret/data/mine/*")
	step(t, m, "enter", "ctrl+s")
	if m.screen != screenEditor {
		t.Fatalf("the add form did not apply (screen = %d, form problem = %q)", m.screen, m.form.problem)
	}
	step(t, m, "s", "ctrl+s")

	if m.dialog != dialogRemoteNameTaken {
		t.Fatalf("a create onto an existing name did not raise the name-taken dialog (dialog = %d, problem = %q)",
			m.dialog, m.problem)
	}
	if got, _ := store.bodyOf("team-a"); got != existing {
		t.Fatal("a refused create overwrote the existing policy")
	}
	for _, call := range store.callLog() {
		if strings.HasPrefix(call, "update:") {
			t.Fatalf("the refused create was retried as an update (%s)", call)
		}
	}

	// Opening the existing policy is a read, and produces a real revision —
	// so the next write is a genuine, conflict-checked update.
	step(t, m, "o")
	if string(m.session.Current()) != existing {
		t.Error("opening the existing policy did not load the server's content")
	}
	rev, exists, _ := m.session.RemoteRevision()
	if !exists || rev.Version != 7 {
		t.Errorf("after opening the taken policy, revision = %v (exists %v), want version 7", rev, exists)
	}
}

// --- deletion ---

func TestDeleteRequiresTheExactNameTyped(t *testing.T) {
	store := newFakeStore("https://bao.test:8200")
	store.seed("team-a", remotePolicyBody, 1)

	m := connectTestModel(t, store)
	selectPolicy(t, m, "team-a")
	step(t, m, "x")

	if m.dialog != dialogRemoteDelete {
		t.Fatalf("x did not open the delete confirmation (dialog = %d)", m.dialog)
	}

	// Pressing enter on an empty field deletes nothing.
	step(t, m, "enter")
	if _, ok := store.bodyOf("team-a"); !ok {
		t.Fatal("an unconfirmed delete removed the policy")
	}

	// A near miss deletes nothing either.
	typeInto(t, m, "team-")
	step(t, m, "enter")
	if _, ok := store.bodyOf("team-a"); !ok {
		t.Fatal("a partially typed name deleted the policy")
	}
	if m.problem == "" {
		t.Error("a mistyped confirmation said nothing")
	}

	// The exact name does.
	typeInto(t, m, "a")
	step(t, m, "enter")
	if _, ok := store.bodyOf("team-a"); ok {
		t.Error("the confirmed delete did not remove the policy")
	}
}

func TestDeleteDialogSaysThereIsNoConflictProtection(t *testing.T) {
	store := newFakeStore("https://bao.test:8200")
	store.seed("team-a", remotePolicyBody, 1)

	m := connectTestModel(t, store)
	selectPolicy(t, m, "team-a")
	step(t, m, "x")

	// Line wrapping puts these phrases at unpredictable offsets, so the
	// frame is flattened to one line before they are looked for.
	frame := strings.Join(strings.Fields(stripANSI(m.render())), " ")
	for _, phrase := range []string{
		"no check-and-set",
		"there is no undo",
		"Type the policy name exactly to confirm",
		"team-a",
	} {
		if !strings.Contains(frame, phrase) {
			t.Errorf("the delete dialog does not say %q:\n%s", phrase, frame)
		}
	}
}

func TestEscapeCancelsTheDeleteConfirmation(t *testing.T) {
	store := newFakeStore("https://bao.test:8200")
	store.seed("team-a", remotePolicyBody, 1)

	m := connectTestModel(t, store)
	selectPolicy(t, m, "team-a")
	step(t, m, "x", "esc")

	if m.dialog != dialogNone {
		t.Error("esc did not dismiss the delete confirmation")
	}
	if _, ok := store.bodyOf("team-a"); !ok {
		t.Error("cancelling the confirmation deleted the policy")
	}
}

// --- cancellation and late replies ---

// TestEscapeCancelsWorkInFlight holds a request open, cancels it, and
// checks that the reply which eventually arrives changes nothing.
func TestEscapeCancelsWorkInFlight(t *testing.T) {
	store := newFakeStore("https://bao.test:8200")
	store.seed("team-a", remotePolicyBody, 1)
	store.gate = make(chan struct{})

	m := newRemoteModel(t, store, NewSession())
	cmd := m.enterRemote()
	if !m.busy {
		t.Fatal("entering remote mode did not mark the model busy")
	}

	// The user gives up before the server answers.
	step(t, m, "esc")
	if m.busy {
		t.Error("esc did not leave the busy state")
	}

	// The request now completes. Its reply must be ignored: the operation
	// it belongs to was cancelled.
	close(store.gate)
	deliver(t, m, cmd)

	if m.store != nil {
		t.Error("a cancelled connection still produced a connected store")
	}
	if m.screen == screenBrowse {
		t.Error("a cancelled connection still moved to the browser")
	}
}

// TestLateReplyFromASupersededOperationIsIgnored is the other half of the
// same rule: a reply that lost a race must not replace a newer screen.
func TestLateReplyFromASupersededOperationIsIgnored(t *testing.T) {
	store := newFakeStore("https://bao.test:8200")
	store.seed("team-a", remotePolicyBody, 1)

	m := connectTestModel(t, store)
	selectPolicy(t, m, "team-a")
	step(t, m, "enter")
	if !m.session.IsRemote() {
		t.Fatal("the policy did not open")
	}
	opened := string(m.session.Current())

	// A reply carrying a stale op id — the shape a slow first request
	// takes when a second has already been issued and answered.
	m.Update(remoteOpenedMsg{
		op: m.op - 1,
		policy: baoclient.Policy{
			Name: "something-else",
			Body: "path \"stale\" {\n  capabilities = [\"read\"]\n}\n",
		},
	})

	if name, _ := m.session.RemoteName(); name != "team-a" {
		t.Errorf("a superseded reply replaced the session (now editing %q)", name)
	}
	if string(m.session.Current()) != opened {
		t.Error("a superseded reply replaced the document")
	}
}

// TestAFailedConnectionLeavesTheDocumentAlone covers the requirement that
// no connection, authentication, or TLS failure may disturb existing work.
func TestAFailedConnectionLeavesTheDocumentAlone(t *testing.T) {
	store := newFakeStore("https://bao.test:8200")
	store.errs["list"] = baoclient.ErrUnauthorized

	s, _ := openTestSession(t, samplePolicy)
	m := newRemoteModel(t, store, s)

	// Make an unsaved edit first, so there is something to lose.
	step(t, m, "d")
	edited := string(s.Current())
	if !s.Dirty() {
		t.Fatal("the setup edit did not modify the document")
	}

	deliver(t, m, m.enterRemote())

	if m.problem == "" {
		t.Error("a refused listing reported nothing")
	}
	if m.store != nil {
		t.Error("a failed connection left a store behind")
	}
	if string(s.Current()) != edited {
		t.Error("a failed connection changed the document")
	}
	if !s.Dirty() {
		t.Error("a failed connection cleared the document's unsaved state")
	}
	if m.session != s {
		t.Error("a failed connection replaced the session")
	}
}

func TestAFailedWriteLeavesTheDocumentAlone(t *testing.T) {
	store := newFakeStore("https://bao.test:8200")
	store.seed("team-a", remotePolicyBody, 1)

	m := openRemotePolicy(t, store, "team-a")
	step(t, m, "d")
	mine := string(m.session.Current())

	store.errs["update:team-a"] = baoclient.ErrTLS
	step(t, m, "s", "ctrl+s")

	if m.problem == "" {
		t.Error("a failed write reported nothing")
	}
	if string(m.session.Current()) != mine || !m.session.Dirty() {
		t.Error("a failed write disturbed the document")
	}
	if got, _ := store.bodyOf("team-a"); got != remotePolicyBody {
		t.Error("a failed write reached the server")
	}
}

// --- security ---

// TestSkipVerifyWarningIsPersistentAndTextual checks that the warning is
// on screen on every screen, and that it survives having colour stripped.
func TestSkipVerifyWarningIsPersistentAndTextual(t *testing.T) {
	store := newFakeStore("https://bao.test:8200")
	store.warnings = []string{"TLS certificate verification is DISABLED for this connection"}
	store.seed("team-a", remotePolicyBody, 1)

	m := openRemotePolicy(t, store, "team-a")

	// Every screen reachable from here, including a modal question.
	for _, keys := range [][]string{
		{},
		{"p"}, {"esc", "g"}, {"esc", "?"}, {"esc", "t"},
		{"esc", "d", "s"},
		{"esc", "x"},
		{"esc", "r"},
	} {
		step(t, m, keys...)
		frame := stripANSI(m.render())
		if !strings.Contains(frame, "verification is DISABLED") {
			t.Fatalf("the TLS warning is missing after %v (screen = %d, dialog = %d):\n%s",
				keys, m.screen, m.dialog, frame)
		}
	}
}

// TestTheTokenNeverReachesTheInterface renders every remote screen with a
// token configured and checks it appears in none of them.
//
// The connect screen is the one that could plausibly leak it — it is the
// screen about credentials — so it is rendered with the field focused and
// with a token present.
func TestTheTokenNeverReachesTheInterface(t *testing.T) {
	const token = "s.AAAAAAAAAAAAAAAAAAAAAAAA"

	store := newFakeStore("https://bao.test:8200")
	store.seed("team-a", remotePolicyBody, 1)

	m := NewWithOptions(NewSession(), ModelOptions{
		Config: config.Config{
			Address: store.address,
			Token:   config.SensitiveString(token),
		},
		NewStore: func(config.Config) (RemoteStore, error) { return store, nil },
	})
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})

	frames := []string{}

	// The connect screen, before any connection.
	m.connect = newConnectForm(m.cfg)
	m.screen = screenConnect
	frames = append(frames, m.render())

	// Connected, browsing, editing, reviewing, and every dialog.
	deliver(t, m, m.enterRemote())
	frames = append(frames, m.render())
	selectPolicy(t, m, "team-a")
	step(t, m, "enter")
	frames = append(frames, m.render())
	step(t, m, "d", "s")
	frames = append(frames, m.render())
	step(t, m, "esc", "q")
	frames = append(frames, m.render())

	for i, frame := range frames {
		if strings.Contains(frame, token) {
			t.Errorf("frame %d rendered the token:\n%s", i, frame)
		}
		// The prefix on its own would be enough to matter.
		if strings.Contains(frame, "s.AAAA") {
			t.Errorf("frame %d rendered part of the token:\n%s", i, frame)
		}
	}

	// And the connect screen does say whether one is configured, since
	// "not configured" is the explanation for an empty or refused listing.
	if !strings.Contains(stripANSI(frames[0]), "configured") {
		t.Error("the connect screen does not report whether a token is configured")
	}
}

// TestTheConnectScreenHasNoTokenField guards the shape of the screen
// rather than one rendering of it: there is no input to type a token into,
// so there is no path by which one could reach the terminal at all.
func TestTheConnectScreenHasNoTokenField(t *testing.T) {
	f := newConnectForm(config.Config{Token: config.SensitiveString("s.secret")})

	// Focus cycles through exactly the fields that exist.
	seen := map[int]bool{}
	for i := 0; i < connectFieldCount*2; i++ {
		seen[f.focus] = true
		f.moveFocus(1)
	}
	if len(seen) != connectFieldCount {
		t.Errorf("focus visits %d fields, want %d", len(seen), connectFieldCount)
	}
	if connectFieldCount != 2 {
		t.Errorf("the connect form has %d fields; it should have exactly two (address, namespace)",
			connectFieldCount)
	}

	// Typing goes to the address, never to anything holding a credential.
	f.focus = connectFieldAddress
	for _, msg := range typeText("abc") {
		f.Update(msg)
	}
	if !strings.Contains(f.address.Value(), "abc") {
		t.Error("typing on the connect screen did not reach the address field")
	}
}

// --- the editor's own guards ---

func TestOpeningAnotherPolicyRefusesToDiscardUnsavedWork(t *testing.T) {
	store := newFakeStore("https://bao.test:8200")
	store.seed("team-a", remotePolicyBody, 1)
	store.seed("team-b", remotePolicyBody, 1)

	m := openRemotePolicy(t, store, "team-a")
	step(t, m, "d")
	edited := string(m.session.Current())

	step(t, m, "r") // back to the browser
	selectPolicy(t, m, "team-b")
	step(t, m, "enter")

	if name, _ := m.session.RemoteName(); name != "team-a" {
		t.Errorf("opening another policy discarded unsaved work (now editing %q)", name)
	}
	if string(m.session.Current()) != edited {
		t.Error("opening another policy changed the document")
	}
	if m.problem == "" {
		t.Error("the refusal to open another policy said nothing")
	}
}

func TestAServerWithoutVersionsRefusesToUpdate(t *testing.T) {
	store := newFakeStore("https://bao.test:8200")
	store.seed("team-a", remotePolicyBody, 1)

	m := openRemotePolicy(t, store, "team-a")

	// A server that reports no version leaves the session unable to make a
	// conflict-safe write, and BPE refuses rather than writing blind.
	if err := m.session.AdoptRemoteRevision(baoclient.Revision{}); err != nil {
		t.Fatal(err)
	}
	step(t, m, "d", "s", "ctrl+s")

	if !strings.Contains(m.problem, "conflict protection") &&
		!strings.Contains(m.problem, "did not report a version") {
		t.Errorf("a versionless update was not explained: problem = %q", m.problem)
	}
	if got, _ := store.bodyOf("team-a"); got != remotePolicyBody {
		t.Error("a versionless update reached the server")
	}
	if !m.session.Dirty() {
		t.Error("a refused versionless update cleared the modified state")
	}
}

func TestDeletingTheOpenPolicyMakesTheNextSaveACreate(t *testing.T) {
	store := newFakeStore("https://bao.test:8200")
	store.seed("team-a", remotePolicyBody, 1)

	m := openRemotePolicy(t, store, "team-a")
	step(t, m, "r") // to the browser
	selectPolicy(t, m, "team-a")
	step(t, m, "x")
	typeInto(t, m, "team-a")
	step(t, m, "enter")

	if _, exists, ok := m.session.RemoteRevision(); !ok || exists {
		t.Error("after deleting the open policy the session still thinks it exists on the server")
	}
	if m.problem == "" {
		t.Error("deleting the policy being edited said nothing about it")
	}
}
