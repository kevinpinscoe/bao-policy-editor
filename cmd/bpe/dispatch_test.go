package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/kevinpinscoe/bao-policy-editor/internal/apperr"
	"github.com/kevinpinscoe/bao-policy-editor/internal/config"
)

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
		{"validate", "policy.hcl"},
		{"format", "policy.hcl"},
		{"test", "policy.hcl", "--path", "secret/data/x", "--capability", "read"},
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
		{"validate", "x.hcl"}, // not implemented
		{"--bogus"},           // usage error
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
