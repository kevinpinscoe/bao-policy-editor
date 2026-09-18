// Package tui implements BPE's interactive terminal policy editor: the
// Bubble Tea models, screens, and styles behind `bpe`, `bpe <policy.hcl>`,
// and `bpe --remote`.
//
// It coordinates presentation and user interaction and implements no
// policy semantics of its own. Parsing, editing, and validating HCL are
// internal/hclpolicy's job; effective-access simulation is
// internal/evaluator's; safe local persistence is internal/fileio's;
// talking to an OpenBao server is internal/baoclient's; and the exit-code
// contract is internal/apperr's. Everything in this package that is worth
// testing — the session's edit and save lifecycle, the rule form's field
// handling, the diff, and every remote workflow — is reachable without
// starting a terminal or contacting a server, which is what keeps the test
// suite free of both a live TTY and a live OpenBao.
package tui

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/kevinpinscoe/bao-policy-editor/internal/baoclient"
	"github.com/kevinpinscoe/bao-policy-editor/internal/fileio"
	"github.com/kevinpinscoe/bao-policy-editor/internal/hclpolicy"
)

// ErrFileExists is returned when saving a document that has no file of its
// own to a path something is already using. BPE will create a new file but
// never silently replace one it has not read, since it holds no snapshot
// of that file and so has no way to tell an unrelated file from the one
// the user meant.
var ErrFileExists = errors.New("tui: a file already exists at that path")

// ErrNotRemote and ErrNotLocal are returned when an operation is asked of
// a session whose backing cannot perform it. They exist so a caller that
// reaches the wrong branch gets a typed error rather than a nil-pointer
// dereference on a backing that was never populated.
var (
	ErrNotRemote = errors.New("tui: this document is not backed by a remote policy")
	ErrNotLocal  = errors.New("tui: this document is not backed by a local file")
)

// backingKind names where a session's document came from, and therefore
// where saving it sends it.
type backingKind int

const (
	// backingNew is a document that has never been saved anywhere.
	backingNew backingKind = iota
	// backingLocal is a document backed by a file on disk.
	backingLocal
	// backingRemote is a document backed by a policy on an OpenBao server.
	backingRemote
)

// backing is a session's one and only origin.
//
// It is an interface held in a single field rather than a pair of optional
// structs precisely so that "local" and "remote" cannot both be populated:
// there is one slot, adopting a new backing replaces whatever was in it,
// and the kind is read off the value rather than inferred from which of
// several pointers happens to be non-nil. Kevin's instruction,
// 2026-09-18 — the failure that shape invites is a session that is
// half-migrated between two origins and saves to the wrong one.
type backing interface {
	kind() backingKind

	// title is what the document is called in messages and headers.
	title() string

	// origin is a short phrase naming where the document lives, shown in
	// the header so local and remote are never mistaken for each other.
	origin() string
}

// newBacking is a document that exists only in memory.
type newBacking struct{}

func (newBacking) kind() backingKind { return backingNew }
func (newBacking) title() string     { return "(new policy)" }
func (newBacking) origin() string    { return "unsaved" }

// localBacking is a document backed by a file on disk, together with the
// snapshot that makes a conflicting overwrite detectable.
type localBacking struct {
	path     string
	snapshot fileio.Snapshot
}

func (*localBacking) kind() backingKind { return backingLocal }
func (b *localBacking) title() string   { return b.path }
func (*localBacking) origin() string    { return "local file" }

// remoteBacking is a document backed by a policy on an OpenBao server.
//
// rev carries the version the policy was read at, which is what a
// conflict-safe update sends back as `cas`. exists distinguishes a policy
// read from the server from one being created on it — the two take
// different write paths and a create conflict must never be reinterpreted
// as an update, so which of the two this is has to be recorded rather than
// guessed from whether rev happens to hold a version.
type remoteBacking struct {
	store  RemoteStore
	name   string
	rev    baoclient.Revision
	exists bool
}

func (*remoteBacking) kind() backingKind { return backingRemote }
func (b *remoteBacking) title() string   { return b.name }
func (b *remoteBacking) origin() string {
	if !b.exists {
		return "new remote policy on " + b.store.Address()
	}
	return "remote · " + b.store.Address()
}

// Session is one editing session over a single policy document.
//
// It holds both the bytes as they were loaded and the bytes as they stand
// now, which is what every other question in the editor is answered from:
// unsaved status is whether they differ, the HCL preview is the current
// bytes, the save review is a diff of one against the other, and the rule
// model and diagnostics come from reparsing the current bytes after each
// committed change. Kevin's instruction, 2026-09-17.
//
// Where those bytes came from, and where saving them sends them, is the
// backing — exactly one of new, local, or remote. Every screen above this
// reads the same fields whichever it is.
type Session struct {
	backing backing

	original []byte
	current  []byte

	doc   *hclpolicy.Document
	diags []hclpolicy.Diagnostic
}

// NewSession starts an empty, unnamed document — `bpe` with no arguments.
func NewSession() *Session {
	s := &Session{backing: newBacking{}}
	// An empty document parses cleanly to zero rules; the error return is
	// impossible here and ignoring it would be the only way this could
	// panic later, so it is checked rather than dropped.
	if err := s.Commit(nil); err != nil {
		panic(fmt.Sprintf("tui: empty document failed to parse: %v", err))
	}
	s.original = nil
	return s
}

// OpenSession reads and parses path without modifying it. Opening a file
// is a read: nothing is written, the modification time is untouched, and a
// file whose HCL does not parse still opens — read-only, with its
// diagnostics on display, which is more use than refusing to show it.
func OpenSession(path string) (*Session, error) {
	src, snap, err := fileio.Read(path)
	if err != nil {
		return nil, err
	}

	s := &Session{backing: &localBacking{path: path, snapshot: snap}}
	if err := s.Commit(src); err != nil {
		return nil, err
	}
	s.original = append([]byte(nil), src...)
	return s, nil
}

// NewRemoteSession starts an empty document destined for a policy that
// does not exist on the server yet. Saving it is a create, and a create
// that collides with a policy someone else made in the meantime stays a
// conflict — see Model's remote write handling.
func NewRemoteSession(store RemoteStore, name string) *Session {
	s := &Session{backing: &remoteBacking{store: store, name: name, exists: false}}
	if err := s.Commit(nil); err != nil {
		panic(fmt.Sprintf("tui: empty remote document failed to parse: %v", err))
	}
	s.original = nil
	return s
}

// OpenRemoteSession builds a session around a policy already read from a
// server, holding the revision that read reported so an update can send it
// back as `cas`.
func OpenRemoteSession(store RemoteStore, policy baoclient.Policy) (*Session, error) {
	src := []byte(policy.Body)
	s := &Session{backing: &remoteBacking{
		store:  store,
		name:   policy.Name,
		rev:    policy.Revision,
		exists: true,
	}}
	if err := s.Commit(src); err != nil {
		return nil, err
	}
	s.original = append([]byte(nil), src...)
	return s, nil
}

// Kind reports which backing is in force.
func (s *Session) Kind() backingKind { return s.backing.kind() }

// Filename is what the document is called — a path, a policy name, or a
// placeholder for a document that has never been saved anywhere.
func (s *Session) Filename() string { return s.backing.title() }

// Origin is a short phrase naming where the document lives, for the
// header. It is words rather than a colour, so local and remote stay
// distinguishable with NO_COLOR set.
func (s *Session) Origin() string { return s.backing.origin() }

// HasFile reports whether this document is backed by a file on disk.
func (s *Session) HasFile() bool { return s.backing.kind() == backingLocal }

// IsRemote reports whether this document is backed by a policy on a
// server.
func (s *Session) IsRemote() bool { return s.backing.kind() == backingRemote }

// remote returns the remote backing, or false for any other kind. It is
// the only way the rest of the package reaches those fields, so there is
// no path that reads a remote detail off a session that has none.
func (s *Session) remote() (*remoteBacking, bool) {
	b, ok := s.backing.(*remoteBacking)
	return b, ok
}

// local returns the local backing, or false for any other kind.
func (s *Session) local() (*localBacking, bool) {
	b, ok := s.backing.(*localBacking)
	return b, ok
}

// RemoteStore returns the store this document is backed by, for issuing
// further operations against the same server.
func (s *Session) RemoteStore() (RemoteStore, bool) {
	if b, ok := s.remote(); ok {
		return b.store, true
	}
	return nil, false
}

// RemoteName returns the policy name on the server.
func (s *Session) RemoteName() (string, bool) {
	if b, ok := s.remote(); ok {
		return b.name, true
	}
	return "", false
}

// RemoteRevision returns the revision the policy was last read at, and
// whether the policy exists on the server at all.
func (s *Session) RemoteRevision() (rev baoclient.Revision, exists bool, ok bool) {
	b, isRemote := s.remote()
	if !isRemote {
		return baoclient.Revision{}, false, false
	}
	return b.rev, b.exists, true
}

// Current returns the document's bytes as they stand now.
func (s *Session) Current() []byte { return s.current }

// Original returns the document's bytes as they were loaded.
func (s *Session) Original() []byte { return s.original }

// Document returns the parse of the current bytes.
func (s *Session) Document() *hclpolicy.Document { return s.doc }

// Diagnostics returns every parse and validation finding for the current
// bytes.
func (s *Session) Diagnostics() []hclpolicy.Diagnostic { return s.diags }

// Dirty reports whether the document has unsaved changes.
func (s *Session) Dirty() bool { return !bytes.Equal(s.original, s.current) }

// Editable reports whether the document can be edited at all. It is false
// only for a file whose HCL does not parse — see hclpolicy.Document's
// Editable.
func (s *Session) Editable() bool { return s.doc.Editable() }

// ReadOnlyReason explains why the document cannot be edited, or returns
// the empty string when it can.
func (s *Session) ReadOnlyReason() string {
	if s.Editable() {
		return ""
	}
	return "this file has HCL syntax errors, so BPE cannot edit it without risking the content it could not parse"
}

// Commit adopts src as the document's current bytes and reparses it.
//
// Reparsing after every committed change, rather than mutating a
// long-lived model, is what keeps the decoded rules, the diagnostics, the
// HCL preview, and the token tree from drifting apart: there is only ever
// one source of truth, and it is the bytes.
func (s *Session) Commit(src []byte) error {
	doc, err := hclpolicy.Parse(s.backing.title(), src)
	if err != nil {
		return err
	}

	s.current = src
	s.doc = doc
	s.diags = collectDiagnostics(doc)
	return nil
}

// collectDiagnostics gathers the parse diagnostics and, when the document
// decoded cleanly enough for its domain model to be trustworthy, the
// semantic validation findings too.
//
// Semantic validation is skipped for a document with decode errors for the
// same reason `bpe validate` skips it: an incompletely decoded policy
// produces findings that only restate the real problem, which is already
// in the parse diagnostics.
func collectDiagnostics(doc *hclpolicy.Document) []hclpolicy.Diagnostic {
	diags := append([]hclpolicy.Diagnostic{}, doc.Diagnostics...)
	if !doc.HasErrors() {
		diags = append(diags, doc.Validate()...)
	}
	return diags
}

// ErrorCount and WarningCount report how many diagnostics of each severity
// the current document has.
func (s *Session) ErrorCount() int   { return s.countDiagnostics(hclpolicy.SeverityError) }
func (s *Session) WarningCount() int { return s.countDiagnostics(hclpolicy.SeverityWarning) }

func (s *Session) countDiagnostics(sev hclpolicy.Severity) int {
	n := 0
	for _, d := range s.diags {
		if d.Severity == sev {
			n++
		}
	}
	return n
}

// Save writes the current bytes back to the document's own file.
//
// It goes through fileio.Replace, so the write is atomic, preserves the
// file's permissions, and is refused outright if the file changed on disk
// since it was read. A refusal leaves the in-memory document exactly as it
// was — the caller decides what to do about the conflict, and the user's
// unsaved work is never discarded to resolve one.
//
// A remote document is not saved here. Writing to a server is a network
// operation that has to be cancellable and must not block the interface,
// so it runs as a Bubble Tea command instead (see remote.go); this returns
// ErrNotLocal rather than silently doing nothing.
func (s *Session) Save() error {
	b, ok := s.local()
	if !ok {
		return ErrNotLocal
	}
	if err := fileio.Replace(b.path, s.current, b.snapshot); err != nil {
		var post *fileio.PostReplacementError
		if !errors.As(err, &post) {
			return err
		}
		// The replacement already happened; only the durability step
		// afterward failed. Accepting the write here, and reporting the
		// failure separately, is the difference between "saved, but its
		// survival of an immediate crash is unconfirmed" and "not saved."
		s.markSaved()
		return err
	}

	s.markSaved()
	return s.refreshSnapshot()
}

// SaveAs writes the current bytes to a new path and adopts it as the
// document's file.
//
// It refuses a path that already exists. BPE holds no snapshot of a file
// it has not read, so it cannot tell whether that file is an earlier
// version of this policy or something unrelated — and overwriting the
// second on the strength of a typed filename is not a mistake worth
// offering.
func (s *Session) SaveAs(path string) error {
	if path == "" {
		return errors.New("tui: no path given")
	}
	if _, err := os.Lstat(path); err == nil {
		return fmt.Errorf("%w: %s", ErrFileExists, path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}

	if err := os.WriteFile(path, s.current, 0o600); err != nil {
		return err
	}

	// Adopting the file replaces whatever backing was in force. That is
	// the one-of invariant doing its job: a document written to disk is a
	// local document from that point on, and there is no residual remote
	// backing left behind it to save to by accident.
	s.backing = &localBacking{path: path}
	s.markSaved()
	if err := s.refreshSnapshot(); err != nil {
		return err
	}
	// Reparse so diagnostics are stamped with the real filename rather
	// than the placeholder the document carried before it had one.
	return s.Commit(s.current)
}

// Reload discards the in-memory document and reads the file again. It is
// the "take what is on disk" answer to a save conflict, and it is
// deliberately explicit: nothing calls it to recover from a conflict on
// the user's behalf.
func (s *Session) Reload() error {
	b, ok := s.local()
	if !ok {
		return ErrNotLocal
	}
	src, snap, err := fileio.Read(b.path)
	if err != nil {
		return err
	}
	b.snapshot = snap
	if err := s.Commit(src); err != nil {
		return err
	}
	s.original = append([]byte(nil), src...)
	return nil
}

// OnDisk returns the file's current contents, for showing the user what
// changed underneath them after a save conflict. It does not touch the
// session's own state.
func (s *Session) OnDisk() ([]byte, error) {
	b, ok := s.local()
	if !ok {
		return nil, ErrNotLocal
	}
	src, _, err := fileio.Read(b.path)
	return src, err
}

// MarkRemoteSaved records that a remote write succeeded: the current bytes
// become the saved baseline, the policy now exists, and the revision moves
// on to whatever the server reported.
//
// A server that returns no version leaves the backing without one, which
// is what makes the *next* update refuse rather than write blind —
// baoclient.Update returns ErrConflictProtectionUnsupported instead of
// falling back to a racy read-compare-write.
func (s *Session) MarkRemoteSaved(result baoclient.WriteResult) error {
	b, ok := s.remote()
	if !ok {
		return ErrNotRemote
	}
	b.exists = true
	b.rev = result.Revision
	s.markSaved()
	return nil
}

// There is deliberately no method here that attaches a revision to the
// session without a write behind it.
//
// One existed, and it was the source of the same defect twice: the
// reviewed retry after a check-and-set rejection needs to send a revision
// the session does not hold, and writing that onto the session — whether
// when the conflict diff was merely displayed, or a moment before the
// retry was dispatched — left it attached to a document whose write had
// not happened. A cancelled or failed retry then left the session holding
// a revision the server would accept, so a later ordinary save completed
// an overwrite that never went back through the review.
//
// A revision the session did not read for itself now belongs to a single
// write command and dies with it; see Model.writePolicyWith. The session's
// own revision moves in exactly three places: a read (OpenRemoteSession),
// a confirmed write (MarkRemoteSaved), and taking the server's copy
// wholesale (ReloadRemote). Each of those has the server's current state
// actually in hand. Kevin's instruction, 2026-09-18.

// MarkRemoteDeleted records that the policy behind this document no
// longer exists on the server.
//
// The document itself is untouched — the user's bytes are still theirs to
// save. What changes is that the next write is a create rather than an
// update, because an update would send a `cas` for a policy that is gone.
func (s *Session) MarkRemoteDeleted() error {
	b, ok := s.remote()
	if !ok {
		return ErrNotRemote
	}
	b.exists = false
	b.rev = baoclient.Revision{}
	return nil
}

// ReloadRemote replaces the document with the server's copy, discarding
// unsaved edits. Like Reload, nothing calls it to resolve a conflict on
// the user's behalf.
func (s *Session) ReloadRemote(policy baoclient.Policy) error {
	b, ok := s.remote()
	if !ok {
		return ErrNotRemote
	}
	src := []byte(policy.Body)
	b.rev = policy.Revision
	b.exists = true
	if err := s.Commit(src); err != nil {
		return err
	}
	s.original = append([]byte(nil), src...)
	return nil
}

// SuggestSavePath proposes a filename for a document that has none,
// alongside whatever directory the process is running in.
func SuggestSavePath() string {
	wd, err := os.Getwd()
	if err != nil {
		return "policy.hcl"
	}
	return filepath.Join(wd, "policy.hcl")
}

func (s *Session) markSaved() {
	s.original = append([]byte(nil), s.current...)
}

func (s *Session) refreshSnapshot() error {
	b, ok := s.local()
	if !ok {
		return ErrNotLocal
	}
	_, snap, err := fileio.Read(b.path)
	if err != nil {
		return err
	}
	b.snapshot = snap
	return nil
}
