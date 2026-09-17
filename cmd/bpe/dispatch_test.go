package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kevinpinscoe/bao-policy-editor/internal/apperr"
	"github.com/kevinpinscoe/bao-policy-editor/internal/config"
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
		{"format", "policy.hcl"},
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

func TestExecute_Test_UnsupportedContent_ExitPolicyIssue(t *testing.T) {
	path := writePolicyFile(t, "unsupported.hcl", "path \"secret/data/foo\" {\n  capabilities = [\"read\"]\n  future_option = \"nope\"\n}\n")

	var stdout, stderr bytes.Buffer
	code := Execute(context.Background(), []string{"test", path, "--path", "secret/data/foo", "--capability", "read"}, nil, &stdout, &stderr, noEnv)

	if code != int(apperr.ExitPolicyIssue) {
		t.Errorf("exit code = %d, want %d — content bpe cannot fully represent must block a decision, not be silently dropped", code, apperr.ExitPolicyIssue)
	}
	if !strings.Contains(stdout.String(), "future_option") {
		t.Errorf("stdout = %q, want the unsupported-attribute diagnostic printed, not dropped", stdout.String())
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
