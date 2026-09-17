package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kevinpinscoe/bao-policy-editor/internal/apperr"
	"github.com/kevinpinscoe/bao-policy-editor/internal/config"
	"github.com/kevinpinscoe/bao-policy-editor/internal/fileio"
)

// writePolicyFile writes src to a temp file named name within t's
// temporary directory and returns its path, so validate tests never
// depend on the test binary's working directory or on testdata fixtures
// meant for internal/hclpolicy's own tests.
func writePolicyFile(t *testing.T, name, src string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
	return path
}

func noEnv(string) (string, bool) { return "", false }

func TestExecute_HelpWritesToStdout(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Execute(context.Background(), []string{"--help"}, nil, &stdout, &stderr, noEnv)

	if code != int(apperr.ExitSuccess) {
		t.Errorf("exit code = %d, want %d", code, apperr.ExitSuccess)
	}
	if stdout.Len() == 0 {
		t.Error("stdout is empty, want help text")
	}
	if stderr.Len() != 0 {
		t.Errorf("stderr = %q, want empty for --help", stderr.String())
	}
}

func TestExecute_VersionWritesToStdout(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Execute(context.Background(), []string{"--version"}, nil, &stdout, &stderr, noEnv)

	if code != int(apperr.ExitSuccess) {
		t.Errorf("exit code = %d, want %d", code, apperr.ExitSuccess)
	}
	if !strings.Contains(stdout.String(), "bpe") {
		t.Errorf("stdout = %q, want it to contain the program name", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Errorf("stderr = %q, want empty for --version", stderr.String())
	}
}

func TestExecute_UsageErrorWritesToStderr(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Execute(context.Background(), []string{"validate"}, nil, &stdout, &stderr, noEnv)

	if code != int(apperr.ExitUsage) {
		t.Errorf("exit code = %d, want %d", code, apperr.ExitUsage)
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout = %q, want empty for a usage error", stdout.String())
	}
	if stderr.Len() == 0 {
		t.Error("stderr is empty, want a usage message")
	}
}

func TestExecute_UnimplementedReturnsOperationalExitCode(t *testing.T) {
	cases := [][]string{
		nil,
		{"policy.hcl"},
	}

	for _, args := range cases {
		t.Run(strings.Join(args, "_"), func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := Execute(context.Background(), args, nil, &stdout, &stderr, noEnv)

			if code != int(apperr.ExitOperational) {
				t.Errorf("exit code = %d, want %d", code, apperr.ExitOperational)
			}
			if !strings.Contains(stderr.String(), "not implemented yet") {
				t.Errorf("stderr = %q, want it to say plainly that the feature is not implemented", stderr.String())
			}
		})
	}
}

func TestExecute_InvalidArgumentsReturnExitCode2(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Execute(context.Background(), []string{"--bogus"}, nil, &stdout, &stderr, noEnv)

	if code != int(apperr.ExitUsage) {
		t.Errorf("exit code = %d, want %d", code, apperr.ExitUsage)
	}
}

func TestExecute_CancellationReturnsExitCode130_BeforeParsing(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	var stdout, stderr bytes.Buffer
	code := Execute(ctx, []string{"validate", "policy.hcl"}, nil, &stdout, &stderr, noEnv)

	if code != int(apperr.ExitInterrupted) {
		t.Errorf("exit code = %d, want %d", code, apperr.ExitInterrupted)
	}
}

func TestExecute_CancellationReturnsExitCode130_AfterConfigResolve(t *testing.T) {
	// dispatchCommand re-checks ctx.Err() itself, so cancelling a context
	// that ParseArgs/config.Resolve never observe (because they do not
	// take one) is exercised directly here rather than through Execute's
	// early-return check.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	cmd := &Command{Kind: KindValidate, PolicyFile: "policy.hcl"}
	var stdout, stderr bytes.Buffer
	err := dispatchCommand(ctx, cmd, config.Config{}, nil, &stdout, &stderr)

	if err == nil {
		t.Fatal("dispatchCommand() error = nil, want an interrupted error")
	}
	if got := apperr.CodeOf(err); got != apperr.ExitInterrupted {
		t.Errorf("exit code = %v, want %v", got, apperr.ExitInterrupted)
	}
}

func TestExecute_Validate_FileNotFound(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Execute(context.Background(), []string{"validate", filepath.Join(t.TempDir(), "missing.hcl")}, nil, &stdout, &stderr, noEnv)

	if code != int(apperr.ExitOperational) {
		t.Errorf("exit code = %d, want %d", code, apperr.ExitOperational)
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout = %q, want empty for a read failure", stdout.String())
	}
	if !strings.Contains(stderr.String(), "failed to read policy file") {
		t.Errorf("stderr = %q, want it to say the file could not be read", stderr.String())
	}
}

func TestExecute_Validate_CleanPolicy_ExitSuccess(t *testing.T) {
	path := writePolicyFile(t, "clean.hcl", "path \"secret/data/foo\" {\n  capabilities = [\"read\"]\n}\n")

	var stdout, stderr bytes.Buffer
	code := Execute(context.Background(), []string{"validate", path}, nil, &stdout, &stderr, noEnv)

	if code != int(apperr.ExitSuccess) {
		t.Errorf("exit code = %d, want %d; stderr = %q", code, apperr.ExitSuccess, stderr.String())
	}
	if !strings.Contains(stdout.String(), "no issues found") {
		t.Errorf("stdout = %q, want it to report no issues", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Errorf("stderr = %q, want empty on success", stderr.String())
	}
}

func TestExecute_Validate_WarningsOnly_ExitSuccess(t *testing.T) {
	path := writePolicyFile(t, "sudo.hcl", "path \"secret/data/foo\" {\n  capabilities = [\"sudo\"]\n}\n")

	var stdout, stderr bytes.Buffer
	code := Execute(context.Background(), []string{"validate", path}, nil, &stdout, &stderr, noEnv)

	if code != int(apperr.ExitSuccess) {
		t.Errorf("exit code = %d, want %d (warnings alone must not fail validation); stderr = %q", code, apperr.ExitSuccess, stderr.String())
	}
	if !strings.Contains(stdout.String(), "grants sudo") {
		t.Errorf("stdout = %q, want the sudo warning printed", stdout.String())
	}
}

func TestExecute_Validate_SemanticError_ExitPolicyIssue(t *testing.T) {
	path := writePolicyFile(t, "unknown-cap.hcl", "path \"secret/data/foo\" {\n  capabilities = [\"frobnicate\"]\n}\n")

	var stdout, stderr bytes.Buffer
	code := Execute(context.Background(), []string{"validate", path}, nil, &stdout, &stderr, noEnv)

	if code != int(apperr.ExitPolicyIssue) {
		t.Errorf("exit code = %d, want %d", code, apperr.ExitPolicyIssue)
	}
	if !strings.Contains(stdout.String(), "unknown capability") {
		t.Errorf("stdout = %q, want the unknown-capability diagnostic printed", stdout.String())
	}
}

func TestExecute_Validate_SyntaxError_ExitPolicyIssue(t *testing.T) {
	path := writePolicyFile(t, "broken.hcl", "path \"secret/data/broken\" {\n  capabilities = [\"read\"]\n")

	var stdout, stderr bytes.Buffer
	code := Execute(context.Background(), []string{"validate", path}, nil, &stdout, &stderr, noEnv)

	if code != int(apperr.ExitPolicyIssue) {
		t.Errorf("exit code = %d, want %d for malformed HCL", code, apperr.ExitPolicyIssue)
	}
	if stdout.Len() == 0 {
		t.Error("stdout is empty, want the syntax-error diagnostic printed")
	}
}

func TestExecute_Test_UnknownCapability_ExitUsage(t *testing.T) {
	path := writePolicyFile(t, "clean.hcl", "path \"secret/data/foo\" {\n  capabilities = [\"read\"]\n}\n")

	var stdout, stderr bytes.Buffer
	code := Execute(context.Background(), []string{"test", path, "--path", "secret/data/foo", "--capability", "frobnicate"}, nil, &stdout, &stderr, noEnv)

	if code != int(apperr.ExitUsage) {
		t.Errorf("exit code = %d, want %d — an unknown --capability is a usage error, validated before the file is even read", code, apperr.ExitUsage)
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout = %q, want empty for a usage error", stdout.String())
	}
}

func TestExecute_Test_FileNotFound(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Execute(context.Background(), []string{"test", filepath.Join(t.TempDir(), "missing.hcl"), "--path", "secret/data/foo", "--capability", "read"}, nil, &stdout, &stderr, noEnv)

	if code != int(apperr.ExitOperational) {
		t.Errorf("exit code = %d, want %d", code, apperr.ExitOperational)
	}
	if !strings.Contains(stderr.String(), "failed to read policy file") {
		t.Errorf("stderr = %q, want it to say the file could not be read", stderr.String())
	}
}

func TestExecute_Test_Allowed_ExitSuccess(t *testing.T) {
	path := writePolicyFile(t, "clean.hcl", "path \"secret/data/foo\" {\n  capabilities = [\"read\"]\n}\n")

	var stdout, stderr bytes.Buffer
	code := Execute(context.Background(), []string{"test", path, "--path", "secret/data/foo", "--capability", "read"}, nil, &stdout, &stderr, noEnv)

	if code != int(apperr.ExitSuccess) {
		t.Errorf("exit code = %d, want %d; stderr = %q", code, apperr.ExitSuccess, stderr.String())
	}
	if !strings.Contains(stdout.String(), "ALLOWED") {
		t.Errorf("stdout = %q, want the explanation to say ALLOWED", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Errorf("stderr = %q, want empty on success", stderr.String())
	}
}

func TestExecute_Test_Denied_ExitPolicyIssue(t *testing.T) {
	path := writePolicyFile(t, "clean.hcl", "path \"secret/data/foo\" {\n  capabilities = [\"read\"]\n}\n")

	var stdout, stderr bytes.Buffer
	code := Execute(context.Background(), []string{"test", path, "--path", "secret/data/foo", "--capability", "update"}, nil, &stdout, &stderr, noEnv)

	if code != int(apperr.ExitPolicyIssue) {
		t.Errorf("exit code = %d, want %d", code, apperr.ExitPolicyIssue)
	}
	if !strings.Contains(stdout.String(), "DENIED") {
		t.Errorf("stdout = %q, want the explanation to say DENIED", stdout.String())
	}
}

func TestExecute_Test_IncompleteEvaluation_ExitOperational(t *testing.T) {
	path := writePolicyFile(t, "constrained.hcl", "path \"secret/data/foo\" {\n  capabilities = [\"create\"]\n  required_parameters = [\"owner\"]\n}\n")

	var stdout, stderr bytes.Buffer
	code := Execute(context.Background(), []string{"test", path, "--path", "secret/data/foo", "--capability", "create"}, nil, &stdout, &stderr, noEnv)

	if code != int(apperr.ExitOperational) {
		t.Errorf("exit code = %d, want %d — a parameter-constrained winning rule cannot be reduced to a trustworthy decision from path+capability alone", code, apperr.ExitOperational)
	}
	if !strings.Contains(stdout.String(), "INCOMPLETE") {
		t.Errorf("stdout = %q, want the explanation to say INCOMPLETE", stdout.String())
	}
}

// TestExecute_Test_UnsupportedAttribute_NeverAllowsUnconditionally is
// regression-review point 2, 2026-09-17: an unsupported attribute that
// might be authorization-affecting must remain visible to bpe test (its
// diagnostic printed, not dropped in the HCL-to-domain-model conversion)
// and must never let the request print an unconditional ALLOWED. The
// policy here grants exactly the requested capability on exactly the
// requested path — if bpe test evaluated the incomplete decoded Policy
// anyway, it would confidently say ALLOWED; it must refuse instead.
func TestExecute_Test_UnsupportedAttribute_NeverAllowsUnconditionally(t *testing.T) {
	path := writePolicyFile(t, "unsupported.hcl", "path \"secret/data/foo\" {\n  capabilities = [\"read\"]\n  future_option = \"nope\"\n}\n")

	var stdout, stderr bytes.Buffer
	code := Execute(context.Background(), []string{"test", path, "--path", "secret/data/foo", "--capability", "read"}, nil, &stdout, &stderr, noEnv)

	if code != int(apperr.ExitPolicyIssue) {
		t.Errorf("exit code = %d, want %d — content bpe cannot fully represent must block a decision, not be silently dropped", code, apperr.ExitPolicyIssue)
	}
	if !strings.Contains(stdout.String(), "future_option") {
		t.Errorf("stdout = %q, want the unsupported-attribute diagnostic printed, not dropped", stdout.String())
	}
	if strings.Contains(stdout.String(), "ALLOWED") {
		t.Errorf("stdout = %q, must never print ALLOWED when unsupported content could be authorization-affecting", stdout.String())
	}
}

// TestExecute_Test_UnsupportedBlock_NeverAllowsUnconditionally is the
// same regression as above, for a top-level unsupported BLOCK rather
// than an unsupported attribute within a path block — a different code
// path in hclpolicy's decoder (FSM-11), so it earns its own case rather
// than assuming the attribute case covers it.
func TestExecute_Test_UnsupportedBlock_NeverAllowsUnconditionally(t *testing.T) {
	src := "path \"secret/data/foo\" {\n  capabilities = [\"read\"]\n}\n\nunknown_block \"example\" {\n  some_attribute = \"value\"\n}\n"
	path := writePolicyFile(t, "unsupported-block.hcl", src)

	var stdout, stderr bytes.Buffer
	code := Execute(context.Background(), []string{"test", path, "--path", "secret/data/foo", "--capability", "read"}, nil, &stdout, &stderr, noEnv)

	if code != int(apperr.ExitPolicyIssue) {
		t.Errorf("exit code = %d, want %d", code, apperr.ExitPolicyIssue)
	}
	if !strings.Contains(stdout.String(), "unknown_block") {
		t.Errorf("stdout = %q, want the unsupported-block diagnostic printed, not dropped", stdout.String())
	}
	if strings.Contains(stdout.String(), "ALLOWED") {
		t.Errorf("stdout = %q, must never print ALLOWED when unsupported content could be authorization-affecting", stdout.String())
	}
}

func TestExecute_NoSensitiveValuesInOutput(t *testing.T) {
	const secret = "s.SyntheticExecuteTestToken"
	env := func(key string) (string, bool) {
		if key == "BAO_TOKEN" {
			return secret, true
		}
		return "", false
	}

	invocations := [][]string{
		{"--help"},
		{"--version"},
		{"validate"},          // usage error
		{"validate", "x.hcl"}, // x.hcl does not exist — a read failure
		{"test", "x.hcl", "--path", "secret/data/foo", "--capability", "read"}, // x.hcl does not exist — a read failure
		{"--bogus"}, // usage error
	}

	for _, args := range invocations {
		t.Run(strings.Join(args, "_"), func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			Execute(context.Background(), args, nil, &stdout, &stderr, env)

			if strings.Contains(stdout.String(), secret) {
				t.Errorf("stdout leaked the token: %q", stdout.String())
			}
			if strings.Contains(stderr.String(), secret) {
				t.Errorf("stderr leaked the token: %q", stderr.String())
			}
		})
	}
}

func TestExecute_Format_MissingFile_ExitOperational(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Execute(context.Background(), []string{"format", filepath.Join(t.TempDir(), "missing.hcl")}, nil, &stdout, &stderr, noEnv)

	if code != int(apperr.ExitOperational) {
		t.Errorf("exit code = %d, want %d", code, apperr.ExitOperational)
	}
}

func TestExecute_Format_ReformatsAndWrites_ExitSuccess(t *testing.T) {
	path := writePolicyFile(t, "messy.hcl", "path   \"secret/data/foo\"    {\ncapabilities=[\"read\"]\n}\n")

	var stdout, stderr bytes.Buffer
	code := Execute(context.Background(), []string{"format", path}, nil, &stdout, &stderr, noEnv)

	if code != int(apperr.ExitSuccess) {
		t.Errorf("exit code = %d, want %d; stderr = %q", code, apperr.ExitSuccess, stderr.String())
	}
	if !strings.Contains(stdout.String(), "formatted") {
		t.Errorf("stdout = %q, want it to report the file was formatted", stdout.String())
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := "path \"secret/data/foo\" {\n  capabilities = [\"read\"]\n}\n"
	if string(got) != want {
		t.Errorf("file content after format = %q, want %q", got, want)
	}
}

func TestExecute_Format_PreservesPermissions(t *testing.T) {
	path := writePolicyFile(t, "messy.hcl", "path   \"secret/data/foo\"    {\ncapabilities=[\"read\"]\n}\n")
	if err := os.Chmod(path, 0o640); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	code := Execute(context.Background(), []string{"format", path}, nil, &stdout, &stderr, noEnv)
	if code != int(apperr.ExitSuccess) {
		t.Fatalf("exit code = %d, want %d; stderr = %q", code, apperr.ExitSuccess, stderr.String())
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o640 {
		t.Errorf("permissions after format = %v, want %v", info.Mode().Perm(), os.FileMode(0o640))
	}
}

func TestExecute_Format_AlreadyFormatted_NoOpPreservesModTime(t *testing.T) {
	clean := "path \"secret/data/foo\" {\n  capabilities = [\"read\"]\n}\n"
	path := writePolicyFile(t, "clean.hcl", clean)

	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	// Filesystem mtime resolution can be coarser than this test runs in;
	// back the recorded time off by a safe margin so a real rewrite
	// (which would set mtime to "now") is unambiguously detectable.
	past := before.ModTime().Add(-time.Hour)
	if err := os.Chtimes(path, past, past); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	code := Execute(context.Background(), []string{"format", path}, nil, &stdout, &stderr, noEnv)

	if code != int(apperr.ExitSuccess) {
		t.Errorf("exit code = %d, want %d; stderr = %q", code, apperr.ExitSuccess, stderr.String())
	}
	if !strings.Contains(stdout.String(), "already formatted") {
		t.Errorf("stdout = %q, want it to report already formatted", stdout.String())
	}

	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !after.ModTime().Equal(past) {
		t.Errorf("ModTime changed on a no-op format: was %v, now %v — an already-formatted file must not be rewritten", past, after.ModTime())
	}
}

func TestExecute_Format_Check_WouldReformat_ExitPolicyIssue_NoWrite(t *testing.T) {
	messy := "path   \"secret/data/foo\"    {\ncapabilities=[\"read\"]\n}\n"
	path := writePolicyFile(t, "messy.hcl", messy)

	var stdout, stderr bytes.Buffer
	code := Execute(context.Background(), []string{"format", path, "--check"}, nil, &stdout, &stderr, noEnv)

	if code != int(apperr.ExitPolicyIssue) {
		t.Errorf("exit code = %d, want %d", code, apperr.ExitPolicyIssue)
	}
	if !strings.Contains(stdout.String(), "not formatted") {
		t.Errorf("stdout = %q, want it to report the file is not formatted", stdout.String())
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != messy {
		t.Errorf("--check modified the file: got %q, want the original %q unchanged", got, messy)
	}
}

func TestExecute_Format_Check_AlreadyFormatted_ExitSuccess(t *testing.T) {
	clean := "path \"secret/data/foo\" {\n  capabilities = [\"read\"]\n}\n"
	path := writePolicyFile(t, "clean.hcl", clean)

	var stdout, stderr bytes.Buffer
	code := Execute(context.Background(), []string{"format", path, "--check"}, nil, &stdout, &stderr, noEnv)

	if code != int(apperr.ExitSuccess) {
		t.Errorf("exit code = %d, want %d; stderr = %q", code, apperr.ExitSuccess, stderr.String())
	}
}

func TestExecute_Format_SyntaxError_ExitPolicyIssue_NoWrite(t *testing.T) {
	broken := "path \"secret/data/broken\" {\n  capabilities = [\"read\"]\n"
	path := writePolicyFile(t, "broken.hcl", broken)

	var stdout, stderr bytes.Buffer
	code := Execute(context.Background(), []string{"format", path}, nil, &stdout, &stderr, noEnv)

	if code != int(apperr.ExitPolicyIssue) {
		t.Errorf("exit code = %d, want %d for malformed HCL", code, apperr.ExitPolicyIssue)
	}
	if stdout.Len() == 0 {
		t.Error("stdout is empty, want the syntax-error diagnostic printed")
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != broken {
		t.Errorf("a syntactically invalid file was modified despite the refusal: got %q", got)
	}
}

// TestExecute_Format_SemanticProblemsStillFormat is FSM-14's central
// regression for Kevin's instruction: formatting is gated on HCL syntax
// validity alone. A decode-time error (an unparseable expiration), an
// unsupported attribute or block, and a Validate-level contradiction
// (required_parameters vs. denied_parameters) must all still format
// successfully, and none of the unrecognized content may be lost.
func TestExecute_Format_SemanticProblemsStillFormat(t *testing.T) {
	cases := []struct {
		name string
		src  string
		// mustContain is checked against the file's content after
		// formatting, to confirm content this domain model cannot
		// decode was not silently dropped.
		mustContain string
	}{
		{
			name:        "unparseable expiration timestamp",
			src:         "path   \"secret/data/foo\"    {\ncapabilities=[\"read\"]\nexpiration=\"not-a-timestamp\"\n}\n",
			mustContain: "not-a-timestamp",
		},
		{
			name:        "unsupported attribute",
			src:         "path   \"secret/data/foo\"    {\ncapabilities=[\"read\"]\nfuture_option=\"x\"\n}\n",
			mustContain: "future_option",
		},
		{
			name:        "unsupported top-level block",
			src:         "path \"secret/data/foo\" {\ncapabilities=[\"read\"]\n}\n\nunknown_block \"example\" {\nsome_attribute=\"value\"\n}\n",
			mustContain: "unknown_block",
		},
		{
			name: "contradictory required/denied parameter (Validate-level, not decode-level)",
			src: "path \"secret/data/team-a/*\" {\n" +
				"  capabilities         = [\"create\", \"read\", \"update\"]\n" +
				"  required_parameters  = [\"owner\"]\n" +
				"  allowed_parameters   = {\n    \"ttl\" = [\"1h\", \"24h\"]\n  }\n" +
				"  denied_parameters    = {\n    \"owner\" = []\n  }\n" +
				"}\n",
			mustContain: "required_parameters",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := writePolicyFile(t, "policy.hcl", tc.src)

			var stdout, stderr bytes.Buffer
			code := Execute(context.Background(), []string{"format", path}, nil, &stdout, &stderr, noEnv)

			if code != int(apperr.ExitSuccess) {
				t.Fatalf("exit code = %d, want %d (syntactically valid content must format even with semantic problems); stdout=%q stderr=%q", code, apperr.ExitSuccess, stdout.String(), stderr.String())
			}

			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(got), tc.mustContain) {
				t.Errorf("formatted content lost %q — got:\n%s", tc.mustContain, got)
			}
		})
	}
}

func TestExecute_Format_RefusesSymlink_ExitOperational(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.hcl")
	if err := os.WriteFile(target, []byte("path   \"secret/data/foo\"    {\ncapabilities=[\"read\"]\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.hcl")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	code := Execute(context.Background(), []string{"format", link}, nil, &stdout, &stderr, noEnv)

	if code != int(apperr.ExitOperational) {
		t.Errorf("exit code = %d, want %d — bpe format must refuse to write through a symlink", code, apperr.ExitOperational)
	}

	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(got), "  capabilities") {
		t.Errorf("symlink target was reformatted despite the refusal: %q", got)
	}
}

func TestExecute_Format_Check_FollowsSymlink(t *testing.T) {
	// --check is read-only, so it may follow a symlink (only in-place
	// writing refuses one) — see internal/fileio's package doc.
	dir := t.TempDir()
	target := filepath.Join(dir, "target.hcl")
	messy := "path   \"secret/data/foo\"    {\ncapabilities=[\"read\"]\n}\n"
	if err := os.WriteFile(target, []byte(messy), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.hcl")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	code := Execute(context.Background(), []string{"format", link, "--check"}, nil, &stdout, &stderr, noEnv)

	if code != int(apperr.ExitPolicyIssue) {
		t.Errorf("exit code = %d, want %d — --check should read through the symlink and report it needs formatting", code, apperr.ExitPolicyIssue)
	}
}

func TestMapFormatWriteError(t *testing.T) {
	cases := []struct {
		name     string
		err      error
		wantCode apperr.ExitCode
	}{
		{
			name:     "conflict",
			err:      fmt.Errorf("%w: %s", fileio.ErrConflict, fileio.ReasonContentChanged),
			wantCode: apperr.ExitConflict,
		},
		{
			name:     "symlink",
			err:      fileio.ErrSymlink,
			wantCode: apperr.ExitOperational,
		},
		{
			name:     "hard linked",
			err:      fileio.ErrHardLinked,
			wantCode: apperr.ExitOperational,
		},
		{
			name:     "post-replacement failure",
			err:      &fileio.PostReplacementError{Cause: errors.New("directory sync failed")},
			wantCode: apperr.ExitOperational,
		},
		{
			name:     "unrecognized error",
			err:      errors.New("disk full"),
			wantCode: apperr.ExitOperational,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var stdout bytes.Buffer
			got := mapFormatWriteError("policy.hcl", tc.err, &stdout)
			if code := apperr.CodeOf(got); code != tc.wantCode {
				t.Errorf("CodeOf(mapFormatWriteError(...)) = %d, want %d (err: %v)", code, tc.wantCode, got)
			}
		})
	}
}
