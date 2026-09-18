package tui

import (
	"context"
	"sort"

	tea "charm.land/bubbletea/v2"

	"github.com/kevinpinscoe/bao-policy-editor/internal/baoclient"
	"github.com/kevinpinscoe/bao-policy-editor/internal/config"
)

// RemoteStore is the remote-policy surface the editor uses.
//
// It is baoclient.PolicyStore plus the two things the interface has to
// show the user about the connection itself: which server it is talking to
// and anything unsafe about how. Keeping it an interface here — rather
// than depending on *baoclient.Client — is what lets every remote workflow
// in this package be tested against a fake, with no server, no network,
// and no credential.
type RemoteStore interface {
	baoclient.PolicyStore

	// Address is the server this store talks to. It contains no credential.
	Address() string

	// SecurityWarnings is anything about this connection the user must be
	// told before using it, chiefly disabled certificate verification.
	SecurityWarnings() []string
}

// The real client satisfies it, checked here rather than at a call site so
// a change to either side fails at compile time.
var _ RemoteStore = (*baoclient.Client)(nil)

// StoreFactory builds a RemoteStore from BPE's already-resolved
// configuration.
//
// It is injected into the editor rather than called directly so that a
// test can supply a factory which fails the test if it is ever invoked —
// which is how "`bpe` and `bpe policy.hcl` construct no client and start no
// network activity" is proved rather than asserted. Kevin's instruction,
// 2026-09-18.
type StoreFactory func(config.Config) (RemoteStore, error)

// DefaultStoreFactory builds the real OpenBao client.
func DefaultStoreFactory(cfg config.Config) (RemoteStore, error) {
	client, err := baoclient.New(cfg)
	if err != nil {
		return nil, err
	}
	return client, nil
}

// remoteOpID identifies one in-flight remote operation.
//
// Every command carries the id it was started under and every reply
// carries it back, so a reply that arrives after its operation was
// cancelled — or after a newer operation superseded it — can be recognized
// and dropped. Without this, a slow list finishing after the user has
// already opened a policy would replace the screen they are now on with
// the one they left. Kevin's instruction, 2026-09-18.
type remoteOpID uint64

// The replies a remote command can produce. Each carries its op id; see
// Model.accept.
type (
	// remoteConnectedMsg reports a store built and its first listing
	// fetched. The two are one operation because a client that cannot list
	// is not a usable connection, and reporting the authorization failure
	// at connect time is what puts it on the screen that can explain it.
	remoteConnectedMsg struct {
		op    remoteOpID
		store RemoteStore
		names []string
	}

	remoteListedMsg struct {
		op    remoteOpID
		names []string
	}

	// remoteOpenedMsg is a policy read for editing.
	remoteOpenedMsg struct {
		op     remoteOpID
		policy baoclient.Policy
	}

	// remoteServerCopyMsg is a policy re-read to show the user what changed
	// underneath them. It never replaces the document by itself.
	remoteServerCopyMsg struct {
		op     remoteOpID
		policy baoclient.Policy
		// discard says the user asked to throw their edits away and take
		// the server's copy, rather than merely to look at it.
		discard bool
	}

	remoteWroteMsg struct {
		op      remoteOpID
		result  baoclient.WriteResult
		created bool
	}

	remoteDeletedMsg struct {
		op   remoteOpID
		name string
	}

	// remoteErrMsg is any failure.
	//
	// It carries no operation label of its own. internal/baoclient already
	// names the operation in the error it returns ("updating policy X
	// failed: ..."), and adding a second label here produced "updating
	// policy X: updating policy X failed: ..." on screen. The rule is that
	// the operation appears exactly once, and the layer that knows which
	// request was made is the one that says so. Kevin's instruction,
	// 2026-09-18.
	//
	// created says whether the failed write was a create, which is what
	// keeps a create conflict from being reported, or retried, as an
	// update.
	remoteErrMsg struct {
		op      remoteOpID
		err     error
		created bool
	}
)

// beginOp cancels whatever was in flight, allocates the next op id, and
// puts the model into its busy state.
//
// Cancelling first is deliberate: two remote operations are never usefully
// in flight at once in a single-document editor, and superseding rather
// than queuing is what makes Escape — and simply moving on — take effect
// immediately.
func (m *Model) beginOp(label string) (remoteOpID, context.Context) {
	m.cancelOp()

	m.lastOp++
	m.op = m.lastOp

	ctx, cancel := context.WithCancel(m.ctx)
	m.opCancel = cancel
	m.busy = true
	m.busyLabel = label
	return m.op, ctx
}

// cancelOp stops any in-flight operation and leaves the busy state.
//
// It does not touch the session. A cancelled connect, open, write, or
// delete leaves the document and every unsaved edit exactly as they were —
// which is the whole point of cancelling being safe to press.
func (m *Model) cancelOp() {
	if m.opCancel != nil {
		m.opCancel()
		m.opCancel = nil
	}
	m.busy = false
	m.busyLabel = ""
}

// accept reports whether a reply belongs to the operation currently being
// awaited, and ends the busy state when it does.
//
// A reply that fails this check is discarded in full: it is either the
// answer to something the user cancelled or the answer to something a
// newer request has already superseded, and in both cases acting on it
// would replace the screen or the session the user is now looking at.
func (m *Model) accept(op remoteOpID) bool {
	if !m.busy || op != m.op {
		return false
	}
	m.busy = false
	m.busyLabel = ""
	m.opCancel = nil
	return true
}

// connectRemote builds the store and fetches the policy list.
//
// The factory is called here, inside the command, and nowhere else — so
// running the editor on a local file never reaches it and never builds a
// client.
func (m *Model) connectRemote(cfg config.Config) tea.Cmd {
	op, ctx := m.beginOp("connecting")
	factory := m.newStore

	return func() tea.Msg {
		store, err := factory(cfg)
		if err != nil {
			// baoclient.New's own errors already say what could not be
			// built ("the OpenBao client could not be created: ...").
			return remoteErrMsg{op: op, err: err}
		}
		list, err := store.List(ctx)
		if err != nil {
			return remoteErrMsg{op: op, err: err}
		}
		return remoteConnectedMsg{op: op, store: store, names: sortedNames(list.Names)}
	}
}

// refreshPolicies re-lists the policies on the store already connected.
func (m *Model) refreshPolicies(store RemoteStore) tea.Cmd {
	op, ctx := m.beginOp("listing policies")

	return func() tea.Msg {
		list, err := store.List(ctx)
		if err != nil {
			return remoteErrMsg{op: op, err: err}
		}
		return remoteListedMsg{op: op, names: sortedNames(list.Names)}
	}
}

// openPolicy reads a policy for editing, capturing the revision an update
// will later send back as `cas`.
func (m *Model) openPolicy(store RemoteStore, name string) tea.Cmd {
	op, ctx := m.beginOp("opening " + name)

	return func() tea.Msg {
		policy, err := store.Read(ctx, name)
		if err != nil {
			return remoteErrMsg{op: op, err: err}
		}
		return remoteOpenedMsg{op: op, policy: policy}
	}
}

// fetchServerCopy re-reads the policy the session is editing, to show what
// changed on the server after a rejected write.
//
// discard is carried through to the reply rather than decided there,
// because the same fetch serves two different intentions: looking at the
// server's version, and replacing the document with it. Deciding which on
// arrival is what keeps "show me" from ever turning into "overwrite me".
func (m *Model) fetchServerCopy(store RemoteStore, name string, discard bool) tea.Cmd {
	label := "fetching the server's copy of " + name
	if discard {
		label = "reloading " + name + " from the server"
	}
	op, ctx := m.beginOp(label)

	return func() tea.Msg {
		policy, err := store.Read(ctx, name)
		if err != nil {
			return remoteErrMsg{op: op, err: err}
		}
		return remoteServerCopyMsg{op: op, policy: policy, discard: discard}
	}
}

// writePolicy creates or updates the policy the session is backed by,
// using the revision the session itself holds.
func (m *Model) writePolicy() tea.Cmd {
	return m.writePolicyWith(nil)
}

// writePolicyWith creates or updates the policy, optionally sending a
// revision other than the session's own.
//
// Which of create and update it is comes from the backing's own record of
// whether the policy exists, never from whether a revision happens to be
// present. A create sends `cas = -1` and a policy that turned up in the
// meantime is a conflict; an update sends a revision. Neither ever becomes
// the other, so an override is meaningless for a create and is ignored
// there.
//
// # Why the override is a parameter rather than session state
//
// The reviewed retry after a check-and-set rejection has to send a
// revision the session does not hold — the one its own re-read reported.
// Writing that onto the session first would outlive the request: a retry
// that is cancelled, times out, or fails on TLS would leave the newer
// revision permanently attached, and a later ordinary save would then be
// accepted by the server without ever going back through the conflict
// review. Worse if the document was edited in between, since the accepted
// write would carry content the review never showed.
//
// So the revision belongs to one command and dies with it. The session's
// revision moves only in MarkRemoteSaved, after the server has confirmed
// the write — which means a failed retry leaves the original stale
// revision in place, and the next save conflicts again. Kevin's
// instruction, 2026-09-18.
func (m *Model) writePolicyWith(override *baoclient.Revision) tea.Cmd {
	store, ok := m.session.RemoteStore()
	if !ok {
		return nil
	}
	name, _ := m.session.RemoteName()
	rev, exists, _ := m.session.RemoteRevision()
	if override != nil && exists {
		rev = *override
	}
	body := string(m.session.Current())

	label := "updating " + name
	if !exists {
		label = "creating " + name
	}
	op, ctx := m.beginOp(label)

	return func() tea.Msg {
		if !exists {
			result, err := store.Create(ctx, name, body)
			if err != nil {
				return remoteErrMsg{op: op, err: err, created: true}
			}
			return remoteWroteMsg{op: op, result: result, created: true}
		}

		result, err := store.Update(ctx, name, body, rev)
		if err != nil {
			return remoteErrMsg{op: op, err: err}
		}
		return remoteWroteMsg{op: op, result: result}
	}
}

// deletePolicy removes a policy from the server. The confirmation that
// gates it is in the interface (see dialogRemoteDelete), because this
// endpoint offers no check-and-set and so there is nothing atomic to gate
// it with.
func (m *Model) deletePolicy(store RemoteStore, name string) tea.Cmd {
	op, ctx := m.beginOp("deleting " + name)

	return func() tea.Msg {
		if err := store.Delete(ctx, name); err != nil {
			return remoteErrMsg{op: op, err: err}
		}
		return remoteDeletedMsg{op: op, name: name}
	}
}

// sortedNames returns a stable, copied ordering of the policy names.
//
// OpenBao's own ordering is not part of its documented contract, and a
// list that reorders itself between refreshes moves the selection out from
// under whoever is reading it.
func sortedNames(names []string) []string {
	out := append([]string(nil), names...)
	sort.Strings(out)
	return out
}
