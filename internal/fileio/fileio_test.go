package fileio

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeTemp(t *testing.T, dir, name string, data []byte, perm os.FileMode) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, data, perm); err != nil {
		t.Fatalf("writeTemp: %v", err)
	}
	return path
}

func TestRead(t *testing.T) {
	dir := t.TempDir()

	t.Run("regular file", func(t *testing.T) {
		path := writeTemp(t, dir, "a.hcl", []byte("path \"secret/*\" {}\n"), 0o644)
		data, snap, err := Read(path)
		if err != nil {
			t.Fatalf("Read: %v", err)
		}
		if string(data) != "path \"secret/*\" {}\n" {
			t.Errorf("data = %q", data)
		}
		if snap.info == nil {
			t.Error("snapshot info is nil")
		}
	})

	t.Run("missing file", func(t *testing.T) {
		if _, _, err := Read(filepath.Join(dir, "missing.hcl")); err == nil {
			t.Error("expected an error for a missing file")
		}
	})

	t.Run("directory", func(t *testing.T) {
		sub := filepath.Join(dir, "subdir")
		if err := os.Mkdir(sub, 0o755); err != nil {
			t.Fatal(err)
		}
		if _, _, err := Read(sub); err == nil {
			t.Error("expected an error reading a directory as a file")
		}
	})

	t.Run("follows a symlink", func(t *testing.T) {
		target := writeTemp(t, dir, "target.hcl", []byte("path \"kv/*\" {}\n"), 0o644)
		link := filepath.Join(dir, "link.hcl")
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
		data, _, err := Read(link)
		if err != nil {
			t.Fatalf("Read through symlink: %v", err)
		}
		if string(data) != "path \"kv/*\" {}\n" {
			t.Errorf("data = %q", data)
		}
	})
}

func TestReplace_Success(t *testing.T) {
	dir := t.TempDir()
	path := writeTemp(t, dir, "policy.hcl", []byte("old"), 0o640)

	_, snap, err := Read(path)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}

	if err := Replace(path, []byte("new"), snap); err != nil {
		t.Fatalf("Replace: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile after Replace: %v", err)
	}
	if string(got) != "new" {
		t.Errorf("content = %q, want %q", got, "new")
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("directory has %d entries after Replace, want 1 (no leftover temp file): %v", len(entries), entries)
	}
}

func TestReplace_PreservesPermissions(t *testing.T) {
	dir := t.TempDir()
	path := writeTemp(t, dir, "policy.hcl", []byte("old"), 0o640)

	_, snap, err := Read(path)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if err := Replace(path, []byte("new"), snap); err != nil {
		t.Fatalf("Replace: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o640 {
		t.Errorf("permissions = %v, want %v", info.Mode().Perm(), os.FileMode(0o640))
	}
}

func TestReplace_ConflictDetection(t *testing.T) {
	t.Run("content changed, same size, mtime unchanged", func(t *testing.T) {
		dir := t.TempDir()
		path := writeTemp(t, dir, "policy.hcl", []byte("aaa"), 0o644)
		_, snap, err := Read(path)
		if err != nil {
			t.Fatal(err)
		}

		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("bbb"), 0o644); err != nil {
			t.Fatal(err)
		}
		// Force the modification time back to what it was, so a
		// same-size edit within one mtime tick is exercised even on a
		// filesystem with coarse timestamp resolution.
		if err := os.Chtimes(path, info.ModTime(), info.ModTime()); err != nil {
			t.Fatal(err)
		}

		err = Replace(path, []byte("new"), snap)
		if !errors.Is(err, ErrConflict) {
			t.Fatalf("Replace error = %v, want wrapping ErrConflict", err)
		}

		got, _ := os.ReadFile(path)
		if string(got) != "bbb" {
			t.Errorf("original content was overwritten despite conflict: %q", got)
		}
	})

	t.Run("file deleted", func(t *testing.T) {
		dir := t.TempDir()
		path := writeTemp(t, dir, "policy.hcl", []byte("aaa"), 0o644)
		_, snap, err := Read(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}

		if err := Replace(path, []byte("new"), snap); !errors.Is(err, ErrConflict) {
			t.Fatalf("Replace error = %v, want wrapping ErrConflict", err)
		}
	})

	t.Run("file replaced with a different file of identical content", func(t *testing.T) {
		dir := t.TempDir()
		path := writeTemp(t, dir, "policy.hcl", []byte("aaa"), 0o644)
		_, snap, err := Read(path)
		if err != nil {
			t.Fatal(err)
		}

		// Simulate an editor's own atomic replace: write elsewhere, then
		// rename over path. Content and permissions are identical to the
		// original, but the underlying file is a different inode.
		other := writeTemp(t, dir, "policy.hcl.new", []byte("aaa"), 0o644)
		if err := os.Rename(other, path); err != nil {
			t.Fatal(err)
		}

		err = Replace(path, []byte("new"), snap)
		if !errors.Is(err, ErrConflict) {
			t.Fatalf("Replace error = %v, want wrapping ErrConflict (identity changed)", err)
		}
	})

	t.Run("permission changed", func(t *testing.T) {
		dir := t.TempDir()
		path := writeTemp(t, dir, "policy.hcl", []byte("aaa"), 0o644)
		_, snap, err := Read(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, 0o600); err != nil {
			t.Fatal(err)
		}

		if err := Replace(path, []byte("new"), snap); !errors.Is(err, ErrConflict) {
			t.Fatalf("Replace error = %v, want wrapping ErrConflict (mode changed)", err)
		}
	})
}

func TestReplace_RefusesSymlink(t *testing.T) {
	dir := t.TempDir()
	target := writeTemp(t, dir, "target.hcl", []byte("aaa"), 0o644)
	link := filepath.Join(dir, "link.hcl")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}

	_, snap, err := Read(link)
	if err != nil {
		t.Fatal(err)
	}

	if err := Replace(link, []byte("new"), snap); !errors.Is(err, ErrSymlink) {
		t.Fatalf("Replace error = %v, want ErrSymlink", err)
	}

	targetContent, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(targetContent) != "aaa" {
		t.Errorf("symlink target was modified despite refusal: %q", targetContent)
	}
}

func TestReplace_RefusesHardLinkedFile(t *testing.T) {
	dir := t.TempDir()
	path := writeTemp(t, dir, "policy.hcl", []byte("aaa"), 0o644)
	link := filepath.Join(dir, "policy-link.hcl")
	if err := os.Link(path, link); err != nil {
		t.Skipf("hard links unsupported on this filesystem: %v", err)
	}

	_, snap, err := Read(path)
	if err != nil {
		t.Fatal(err)
	}

	err = Replace(path, []byte("new"), snap)
	if multi, ok := hasMultipleLinks(mustLstat(t, path)); ok {
		if !multi {
			t.Fatalf("test setup did not actually create a second hard link")
		}
		if !errors.Is(err, ErrHardLinked) {
			t.Fatalf("Replace error = %v, want ErrHardLinked", err)
		}
	} else {
		t.Skip("hard-link detection unavailable on this platform (see stat_other.go)")
	}
}

func mustLstat(t *testing.T, path string) os.FileInfo {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info
}

func TestReplace_NoLeftoverTempFileOnWriteFailure(t *testing.T) {
	// A temp file cannot be written to after it is closed; closing it
	// early (by shadowing os.CreateTemp's normal lifecycle is not
	// exposed to callers) is not something this package's public API
	// lets a test force directly. Instead this test drives the one
	// failure a caller can trigger without internal hooks: an
	// unwritable destination directory, which fails at os.CreateTemp
	// itself, before any temp file exists to leak.
	dir := t.TempDir()
	path := writeTemp(t, dir, "policy.hcl", []byte("aaa"), 0o644)
	_, snap, err := Read(path)
	if err != nil {
		t.Fatal(err)
	}

	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o700) })

	if os.Geteuid() == 0 {
		t.Skip("running as root: directory permissions do not block writes")
	}

	err = Replace(path, []byte("new"), snap)
	if err == nil {
		t.Fatal("expected Replace to fail with an unwritable directory")
	}

	entries, rdErr := os.ReadDir(dir)
	if rdErr != nil {
		os.Chmod(dir, 0o700)
		t.Fatalf("ReadDir: %v", rdErr)
	}
	if len(entries) != 1 {
		t.Errorf("directory has %d entries after failed Replace, want 1 (original only, no temp leftover): %v", len(entries), entries)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "aaa" {
		t.Errorf("original content changed despite a failed Replace: %q", got)
	}
}

func TestSnapshot_CapturedFromTheSameRead(t *testing.T) {
	// Read's Snapshot must reflect exactly the bytes it returned, not a
	// second, separate look at the file — this guards against a
	// regression that re-stats or re-reads the path instead of the
	// already-open descriptor.
	dir := t.TempDir()
	path := writeTemp(t, dir, "policy.hcl", []byte("aaa"), 0o644)

	data, snap, err := Read(path)
	if err != nil {
		t.Fatal(err)
	}

	// Change the file the instant after Read returns. If Replace's
	// later conflict check somehow re-derived its comparison from a
	// fresh read of the path rather than trusting the Snapshot taken at
	// Read time, this would not be exercised as a conflict — but it
	// must still be, since content genuinely changed after the read.
	if err := os.WriteFile(path, []byte("zzz"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, time.Now(), time.Now()); err != nil {
		t.Fatal(err)
	}

	if err := Replace(path, data, snap); !errors.Is(err, ErrConflict) {
		t.Fatalf("Replace error = %v, want ErrConflict", err)
	}
}

func TestReplace_UnchangedContentStillReplaces(t *testing.T) {
	// Replace itself always writes when called; "skip the write when
	// formatted output is unchanged" is the caller's decision (see
	// cmd/bpe's runFormat), not this package's. This test documents that
	// boundary: Replace has no built-in no-op path.
	dir := t.TempDir()
	path := writeTemp(t, dir, "policy.hcl", []byte("same"), 0o644)
	_, snap, err := Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := Replace(path, []byte("same"), snap); err != nil {
		t.Fatalf("Replace: %v", err)
	}
}
