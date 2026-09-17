package tui

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kevinpinscoe/bao-policy-editor/internal/fileio"
	"github.com/kevinpinscoe/bao-policy-editor/internal/hclpolicy"
)

const samplePolicy = `# team A
path "secret/data/team-a/*" {
  capabilities = ["read", "list"]
  comment      = "team A read access"
}

path "sys/policies/acl/*" {
  capabilities = ["deny"]
}
`

// writePolicy puts a policy in the test's own temporary directory. Nothing
// in this package's tests touches a file outside it.
func writePolicy(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "policy.hcl")
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("writing the fixture failed: %v", err)
	}
	return path
}

func openTestSession(t *testing.T, contents string) (*Session, string) {
	t.Helper()
	path := writePolicy(t, contents)
	s, err := OpenSession(path)
	if err != nil {
		t.Fatalf("OpenSession returned an error: %v", err)
	}
	return s, path
}

func TestOpenSessionDoesNotModifyTheFile(t *testing.T) {
	path := writePolicy(t, samplePolicy)
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	s, err := OpenSession(path)
	if err != nil {
		t.Fatalf("OpenSession returned an error: %v", err)
	}

	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !before.ModTime().Equal(after.ModTime()) {
		t.Error("opening the policy changed its modification time")
	}
	if s.Dirty() {
		t.Error("a freshly opened document reported itself as modified")
	}
	if got := string(s.Current()); got != samplePolicy {
		t.Errorf("the document does not match the file:\n%s", got)
	}
	if s.Document().RuleCount() != 2 {
		t.Errorf("rule count = %d, want 2", s.Document().RuleCount())
	}
}

func TestNewSessionStartsEmptyAndClean(t *testing.T) {
	s := NewSession()

	if s.HasFile() {
		t.Error("a new document reported that it has a file")
	}
	if s.Dirty() {
		t.Error("a new document reported itself as modified")
	}
	if s.Document().RuleCount() != 0 {
		t.Errorf("rule count = %d, want 0", s.Document().RuleCount())
	}
	if !s.Editable() {
		t.Error("a new document is not editable")
	}
}

func TestSessionDirtyTracksTheBytes(t *testing.T) {
	s, _ := openTestSession(t, samplePolicy)

	next, err := s.Document().EditRule(0, nil, []hclpolicy.AttrEdit{hclpolicy.SetComment("changed")})
	if err != nil {
		t.Fatalf("EditRule returned an error: %v", err)
	}
	if err := s.Commit(next); err != nil {
		t.Fatalf("Commit returned an error: %v", err)
	}
	if !s.Dirty() {
		t.Error("the document is not reported as modified after an edit")
	}

	// Committing the original bytes back makes it clean again — dirtiness
	// is a property of the content, not a flag that latches.
	if err := s.Commit([]byte(samplePolicy)); err != nil {
		t.Fatal(err)
	}
	if s.Dirty() {
		t.Error("the document is still reported as modified after being put back")
	}
}

func TestSessionSaveWritesAndClearsDirty(t *testing.T) {
	s, path := openTestSession(t, samplePolicy)

	next, err := s.Document().EditRule(0, nil, []hclpolicy.AttrEdit{hclpolicy.SetComment("changed")})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Commit(next); err != nil {
		t.Fatal(err)
	}
	if err := s.Save(); err != nil {
		t.Fatalf("Save returned an error: %v", err)
	}

	if s.Dirty() {
		t.Error("the document is still reported as modified after a save")
	}
	written, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(written), `comment      = "changed"`) {
		t.Errorf("the file does not carry the edit:\n%s", written)
	}
	if !strings.Contains(string(written), "# team A") {
		t.Errorf("saving discarded a comment:\n%s", written)
	}
}

func TestSessionSaveRefusesAFileChangedOnDiskAndKeepsTheEdits(t *testing.T) {
	s, path := openTestSession(t, samplePolicy)

	next, err := s.Document().EditRule(0, nil, []hclpolicy.AttrEdit{hclpolicy.SetComment("my edit")})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Commit(next); err != nil {
		t.Fatal(err)
	}

	// Somebody else edits the file underneath the editor.
	const theirs = samplePolicy + "\npath \"secret/data/theirs\" {\n  capabilities = [\"read\"]\n}\n"
	if err := os.WriteFile(path, []byte(theirs), 0o644); err != nil {
		t.Fatal(err)
	}

	err = s.Save()
	if !errors.Is(err, fileio.ErrConflict) {
		t.Fatalf("Save error = %v, want fileio.ErrConflict", err)
	}

	// The refusal must not cost the user their work, and must not have
	// written anything.
	if !s.Dirty() {
		t.Error("the document stopped reporting itself as modified after a refused save")
	}
	if !strings.Contains(string(s.Current()), `comment      = "my edit"`) {
		t.Error("the in-memory edit was lost when the save was refused")
	}
	onDisk, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(onDisk) != theirs {
		t.Errorf("the refused save wrote to the file anyway:\n%s", onDisk)
	}
}

func TestSessionReloadTakesWhatIsOnDisk(t *testing.T) {
	s, path := openTestSession(t, samplePolicy)

	if err := s.Commit([]byte("path \"a\" {\n  capabilities = [\"read\"]\n}\n")); err != nil {
		t.Fatal(err)
	}
	const theirs = "path \"theirs\" {\n  capabilities = [\"list\"]\n}\n"
	if err := os.WriteFile(path, []byte(theirs), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := s.Reload(); err != nil {
		t.Fatalf("Reload returned an error: %v", err)
	}
	if string(s.Current()) != theirs {
		t.Errorf("Reload did not take the file's contents:\n%s", s.Current())
	}
	if s.Dirty() {
		t.Error("the document is reported as modified immediately after a reload")
	}
	// And the snapshot moved with it, so the next save is not refused.
	if err := s.Save(); err != nil {
		t.Errorf("saving after a reload failed: %v", err)
	}
}

func TestSessionSaveAsRefusesAnExistingFile(t *testing.T) {
	dir := t.TempDir()
	existing := filepath.Join(dir, "taken.hcl")
	if err := os.WriteFile(existing, []byte("# something else entirely\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	s := NewSession()
	if err := s.Commit([]byte("path \"a\" {\n  capabilities = [\"read\"]\n}\n")); err != nil {
		t.Fatal(err)
	}

	if err := s.SaveAs(existing); !errors.Is(err, ErrFileExists) {
		t.Fatalf("SaveAs error = %v, want ErrFileExists", err)
	}
	kept, err := os.ReadFile(existing)
	if err != nil {
		t.Fatal(err)
	}
	if string(kept) != "# something else entirely\n" {
		t.Errorf("SaveAs overwrote the existing file:\n%s", kept)
	}
}

func TestSessionSaveAsCreatesAndAdoptsTheFile(t *testing.T) {
	target := filepath.Join(t.TempDir(), "new.hcl")

	s := NewSession()
	const content = "path \"secret/data/a\" {\n  capabilities = [\"read\"]\n}\n"
	if err := s.Commit([]byte(content)); err != nil {
		t.Fatal(err)
	}

	if err := s.SaveAs(target); err != nil {
		t.Fatalf("SaveAs returned an error: %v", err)
	}
	if !s.HasFile() || s.Filename() != target {
		t.Errorf("the session did not adopt the new path: hasFile=%v filename=%q", s.HasFile(), s.Filename())
	}
	if s.Dirty() {
		t.Error("the document is reported as modified immediately after SaveAs")
	}
	written, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(written) != content {
		t.Errorf("SaveAs wrote the wrong content:\n%s", written)
	}

	// A subsequent ordinary save must work against the file it just made.
	if err := s.Commit([]byte(content + "\npath \"b\" {\n  capabilities = [\"list\"]\n}\n")); err != nil {
		t.Fatal(err)
	}
	if err := s.Save(); err != nil {
		t.Errorf("saving after SaveAs failed: %v", err)
	}
}

func TestSessionOpensAnUnparseableFileReadOnly(t *testing.T) {
	s, _ := openTestSession(t, "path \"secret/data/a\" {\n  capabilities = [\n")

	if s.Editable() {
		t.Error("a file with a syntax error opened as editable")
	}
	if s.ReadOnlyReason() == "" {
		t.Error("a read-only document gave no reason")
	}
	if s.ErrorCount() == 0 {
		t.Error("a file with a syntax error reported no errors")
	}
}

func TestSessionCountsDiagnostics(t *testing.T) {
	// `deny` alongside another capability is a Validate finding, not a
	// parse error, so this exercises the path that runs semantic
	// validation as well as parsing.
	s, _ := openTestSession(t, "path \"secret/data/a\" {\n  capabilities = [\"deny\", \"read\"]\n}\n")

	if s.ErrorCount()+s.WarningCount() == 0 {
		t.Errorf("expected a diagnostic for deny combined with read, got none: %v", s.Diagnostics())
	}
}
