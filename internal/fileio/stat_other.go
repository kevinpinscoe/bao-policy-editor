//go:build !unix

package fileio

import "io/fs"

// hasMultipleLinks always reports ok = false on a non-Unix build: hard-
// link detection here relies on syscall.Stat_t's link count, which has
// no equivalent in this file. Replace therefore skips the hard-link
// refusal entirely on such a platform — see ErrHardLinked's doc comment.
// BPE's released builds are Linux and macOS only (both covered by
// stat_unix.go's "unix" build tag), so this file exists only to keep the
// package buildable elsewhere, not because it is a supported target.
func hasMultipleLinks(_ fs.FileInfo) (multi, ok bool) {
	return false, false
}

// syncDir is a no-op on a platform with no directory-fsync equivalent
// wired up here. See stat_unix.go for the supported-platform behavior.
func syncDir(_ string) error {
	return nil
}
