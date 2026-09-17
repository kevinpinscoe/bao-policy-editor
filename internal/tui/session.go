// Package tui implements BPE's interactive terminal policy editor: the
// Bubble Tea models, screens, and styles behind `bpe` and
// `bpe <policy.hcl>`.
//
// It coordinates presentation and user interaction and implements no
// policy semantics of its own. Parsing, editing, and validating HCL are
// internal/hclpolicy's job; effective-access simulation is
// internal/evaluator's; safe local persistence is internal/fileio's; and
// the exit-code contract is internal/apperr's. Everything in this package
// that is worth testing — the session's edit and save lifecycle, the
// rule form's field handling, the diff — is reachable without starting a
// terminal, which is what keeps the test suite free of a live TTY.
package tui

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/kevinpinscoe/bao-policy-editor/internal/fileio"
	"github.com/kevinpinscoe/bao-policy-editor/internal/hclpolicy"
)

// ErrFileExists is returned when saving a document that has no file of its
// own to a path something is already using. BPE will create a new file but
// never silently replace one it has not read, since it holds no snapshot
// of that file and so has no way to tell an unrelated file from the one
// the user meant.
var ErrFileExists = errors.New("tui: a file already exists at that path")

// Session is one editing session over a single policy document.
//
// It holds both the bytes as they were loaded and the bytes as they stand
// now, which is what every other question in the editor is answered from:
// unsaved status is whether they differ, the HCL preview is the current
// bytes, the save review is a diff of one against the other, and the rule
// model and diagnostics come from reparsing the current bytes after each
// committed change. Kevin's instruction, 2026-09-17.
type Session struct {
	filename string
	hasFile  bool

	original []byte
	current  []byte
	snapshot fileio.Snapshot

	doc   *hclpolicy.Document
	diags []hclpolicy.Diagnostic
}

// NewSession starts an empty, unnamed document — `bpe` with no arguments.
func NewSession() *Session {
	s := &Session{filename: "(new policy)"}
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

	s := &Session{filename: path, hasFile: true, snapshot: snap}
	if err := s.Commit(src); err != nil {
		return nil, err
	}
	s.original = append([]byte(nil), src...)
	return s, nil
}

// Filename is the document's path, or a placeholder for a document that
// has never been saved.
func (s *Session) Filename() string { return s.filename }

// HasFile reports whether this document is backed by a file on disk.
func (s *Session) HasFile() bool { return s.hasFile }

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
	doc, err := hclpolicy.Parse(s.filename, src)
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
func (s *Session) Save() error {
	if !s.hasFile {
		return errors.New("tui: this document has no file yet; save it to a path first")
	}
	if err := fileio.Replace(s.filename, s.current, s.snapshot); err != nil {
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

	s.filename = path
	s.hasFile = true
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
	if !s.hasFile {
		return errors.New("tui: this document has no file to reload from")
	}
	src, snap, err := fileio.Read(s.filename)
	if err != nil {
		return err
	}
	s.snapshot = snap
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
	if !s.hasFile {
		return nil, errors.New("tui: this document has no file")
	}
	src, _, err := fileio.Read(s.filename)
	return src, err
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
	_, snap, err := fileio.Read(s.filename)
	if err != nil {
		return err
	}
	s.snapshot = snap
	return nil
}
