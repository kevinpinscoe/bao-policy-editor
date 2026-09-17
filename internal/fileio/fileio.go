// Package fileio provides safe local persistence for BPE: reading a file
// together with a snapshot of its on-disk state, and later replacing it
// only if that state has not changed — plus an atomic write with no
// partial-file failure mode. It exists so a read-modify-write flow (today,
// `bpe format`; later, the TUI's save and FSM-16's local side of a remote
// edit) never silently discards a concurrent external change, and never
// leaves a half-written file behind on failure.
//
// This package is local-filesystem only. Remote OpenBao compare-and-swap
// (policy version/CAS handling) belongs to internal/baoclient, which does
// not need — and must not reuse — the local snapshot type here; the two
// represent different kinds of "has this changed since I read it."
//
// # What Snapshot detects, and what it does not
//
// Snapshot records more than size and modification time, specifically so
// a same-size edit that lands within the same modification-time
// resolution window is still caught: it also records a content hash and
// the file's identity (compared portably via os.SameFile, which checks
// device and inode on Unix). Replace, below, treats a deletion, a
// replacement (a different file placed at the same path — the common
// result of an editor's own atomic "write new, rename over old"), a
// content change, or a permission change as a conflict.
//
// # This is not a filesystem compare-and-swap
//
// Checking a Snapshot against the current on-disk state and then renaming
// a new file into place are two separate operations, with a real window
// between them. An uncooperative concurrent writer can still land a
// change in that window; Replace narrows it by re-checking as late as
// practical, immediately before building the temporary file, but it
// cannot close it. Treat this package as a good-faith detector for the
// ordinary case — a human editor, another BPE invocation, or a sync tool
// touching the same file — not as a guarantee against a deliberate race.
//
// # Crash durability and what is not preserved
//
// Replace's rename is atomic on both of BPE's supported platforms
// (Linux, macOS): a reader never observes a half-written file. Replace
// additionally fsyncs the temporary file before renaming it, and fsyncs
// the containing directory afterward where the platform and filesystem
// support it, so the rename itself is durable across a crash rather than
// only atomic in memory. A failure in that post-rename directory sync is
// reported as a PostReplacementError: the new content is already in
// place, and a caller must not treat that error as "nothing happened."
// Extended attributes and ACLs on the original file are not preserved —
// only the ordinary Unix permission bits (Snapshot's Mode) are.
package fileio

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// Snapshot is an opaque record of a regular file's on-disk state at the
// moment it was read, produced by Read and consumed by Replace. Its
// fields are deliberately unexported: callers carry a Snapshot forward
// between a Read and a later Replace without inspecting or reconstructing
// it themselves.
type Snapshot struct {
	info fs.FileInfo
	hash [sha256.Size]byte
}

// ErrSymlink is returned by Replace when path is a symlink. BPE never
// writes through a symlink in place — replacing it would either rewrite
// the link's target file (surprising the user about which file changed)
// or replace the link itself (silently changing what the path points at
// after this run). Read-only use (a --check style verification) is
// unaffected: it follows the link as os.Open ordinarily would.
var ErrSymlink = errors.New("fileio: refusing to write through a symlink")

// ErrHardLinked is returned by Replace when path is reliably detected to
// have more than one hard link. Only Unix build targets can detect this
// (via the link count in the platform stat structure); on a platform
// where that detection is unavailable, this check is skipped entirely
// rather than guessed at — see the platform-specific hasMultipleLinks.
var ErrHardLinked = errors.New("fileio: refusing to overwrite a hard-linked file")

// ErrConflict is the sentinel a caller checks for with errors.Is to
// recognize any external-change conflict Replace detects — deletion,
// replacement, a content change, or a permission change. The returned
// error always wraps it together with which of those occurred; see
// ConflictReason.
var ErrConflict = errors.New("fileio: file changed on disk since it was read")

// ConflictReason names which specific external change Replace detected.
type ConflictReason string

const (
	ReasonDeleted        ConflictReason = "file no longer exists"
	ReasonReplaced       ConflictReason = "a different file now exists at this path"
	ReasonContentChanged ConflictReason = "file content has changed"
	ReasonModeChanged    ConflictReason = "file permissions have changed"
)

// PostReplacementError indicates a failure that happened after Replace's
// atomic rename had already put the new content in place — only the
// best-effort parent-directory durability sync failed afterward. Callers
// must treat this as "the write succeeded, but its durability across an
// immediate crash could not be confirmed," never as "nothing happened."
type PostReplacementError struct {
	Cause error
}

func (e *PostReplacementError) Error() string {
	return fmt.Sprintf("fileio: file was replaced, but a step after replacement failed: %v", e.Cause)
}

func (e *PostReplacementError) Unwrap() error { return e.Cause }

// Read reads path and returns its contents together with a Snapshot of
// its state at that exact moment. The stat used to build the Snapshot is
// taken from the same open file descriptor the content is read from, so
// the two cannot disagree about which file they describe even if path is
// renamed out from under them immediately afterward.
//
// Read does not refuse or special-case a symlink; it follows one exactly
// as os.Open would. Symlink refusal is Replace's concern, for the write
// side only — see ErrSymlink.
func Read(path string) ([]byte, Snapshot, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, Snapshot{}, err
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return nil, Snapshot{}, err
	}
	if !info.Mode().IsRegular() {
		return nil, Snapshot{}, fmt.Errorf("fileio: %s is not a regular file", path)
	}

	data, err := io.ReadAll(f)
	if err != nil {
		return nil, Snapshot{}, err
	}

	return data, Snapshot{info: info, hash: sha256.Sum256(data)}, nil
}

// Replace atomically writes data to path, replacing whatever is there —
// but only if path's on-disk state still matches want, a Snapshot an
// earlier Read of the same path produced. It:
//
//   - refuses (ErrSymlink) if path is currently a symlink;
//   - refuses (ErrHardLinked) if path is currently detected to have more
//     than one hard link, on platforms where that is detectable;
//   - refuses (wrapping ErrConflict) if path's content, identity, or
//     permissions no longer match want;
//   - otherwise writes to a temporary file in path's own directory,
//     created with restrictive permissions, fsyncs it, sets it to want's
//     original permission bits, closes it, and renames it over path —
//     removing the temporary file on any failure at or before the rename,
//     and never removing path itself as a failure fallback;
//   - fsyncs the containing directory afterward where supported, and
//     reports any failure there as a *PostReplacementError rather than an
//     ordinary one, since the replacement has already happened by then.
//
// See the package doc for what this does and does not guarantee.
func Replace(path string, data []byte, want Snapshot) error {
	dir := filepath.Dir(path)

	linfo, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("%w: %s: %v", ErrConflict, ReasonDeleted, err)
	}
	if linfo.Mode()&os.ModeSymlink != 0 {
		return ErrSymlink
	}
	if multi, ok := hasMultipleLinks(linfo); ok && multi {
		return ErrHardLinked
	}

	// Re-verify as late as practical, immediately before building the
	// temporary file — this narrows, but (per the package doc) can never
	// close, the race between this check and the rename below.
	_, curSnap, err := Read(path)
	if err != nil {
		return fmt.Errorf("%w: %s: %v", ErrConflict, ReasonDeleted, err)
	}
	if reason, changed := want.diff(curSnap); changed {
		return fmt.Errorf("%w: %s", ErrConflict, reason)
	}

	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".bpe-tmp-*")
	if err != nil {
		return fmt.Errorf("fileio: failed to create temporary file: %w", err)
	}
	tmpPath := tmp.Name()
	committed := false
	defer func() {
		if !committed {
			tmp.Close()
			os.Remove(tmpPath)
		}
	}()

	if _, err := tmp.Write(data); err != nil {
		return fmt.Errorf("fileio: failed to write temporary file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("fileio: failed to sync temporary file: %w", err)
	}
	if err := tmp.Chmod(want.info.Mode().Perm()); err != nil {
		return fmt.Errorf("fileio: failed to set permissions on temporary file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("fileio: failed to close temporary file: %w", err)
	}

	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("fileio: failed to replace %s: %w", path, err)
	}
	// The rename succeeded: path now holds the new content. Nothing past
	// this point removes the temporary file's name (it no longer exists
	// under that name) or path itself on failure.
	committed = true

	if err := syncDir(dir); err != nil {
		return &PostReplacementError{Cause: fmt.Errorf("failed to sync directory %s: %w", dir, err)}
	}
	return nil
}

// diff reports whether cur (a fresh Snapshot plus the content it was
// taken from) differs from s in any way Replace treats as a conflict,
// and if so, which ConflictReason best describes it. Identity is
// compared with os.SameFile rather than by reaching into platform stat
// fields directly, so this check itself has no platform-specific code.
func (s Snapshot) diff(cur Snapshot) (ConflictReason, bool) {
	if !os.SameFile(s.info, cur.info) {
		return ReasonReplaced, true
	}
	if s.info.Mode().Perm() != cur.info.Mode().Perm() {
		return ReasonModeChanged, true
	}
	if s.hash != cur.hash {
		return ReasonContentChanged, true
	}
	return "", false
}
