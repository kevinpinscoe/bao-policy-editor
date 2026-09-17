//go:build unix

package fileio

import (
	"io/fs"
	"os"
	"syscall"
)

// hasMultipleLinks reports whether info describes a file with more than
// one hard link, using the link count Unix's stat(2) reports. ok is true
// whenever this platform can answer the question at all — which, given
// the unix build tag, is always, so callers on a non-Unix build see ok
// false instead and skip the check (see stat_other.go).
func hasMultipleLinks(info fs.FileInfo) (multi, ok bool) {
	st, isStatT := info.Sys().(*syscall.Stat_t)
	if !isStatT {
		return false, false
	}
	return st.Nlink > 1, true
}

// syncDir fsyncs dir itself, so a rename into it is durable across a
// crash and not merely atomic in memory. Some filesystems reject fsync
// on a directory file descriptor; that failure is returned to the caller
// (Replace wraps it as a *PostReplacementError, since by the time this
// runs the rename has already succeeded) rather than silently ignored.
func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
