package main

import (
	"context"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
	"time"
)

// buildBPE compiles the real binary once per test run into a temporary
// directory and returns its path. This backs the small number of
// process-level tests below, which verify end-to-end exit-code behavior
// through the actual compiled program rather than through Execute
// directly (already covered by dispatch_test.go).
func buildBPE(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "bpe")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}

	cmd := exec.Command("go", "build", "-o", bin, ".")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go build ./cmd/bpe failed: %v\n%s", err, out)
	}
	return bin
}

func TestProcess_VersionExitsZero(t *testing.T) {
	bin := buildBPE(t)
	out, err := exec.Command(bin, "--version").CombinedOutput()
	if err != nil {
		t.Fatalf("%s --version failed: %v\n%s", bin, err, out)
	}
	if len(out) == 0 {
		t.Error("--version produced no output")
	}
}

func TestProcess_HelpExitsZero(t *testing.T) {
	bin := buildBPE(t)
	out, err := exec.Command(bin, "--help").CombinedOutput()
	if err != nil {
		t.Fatalf("%s --help failed: %v\n%s", bin, err, out)
	}
	if len(out) == 0 {
		t.Error("--help produced no output")
	}
}

func TestProcess_UnknownFlagExitsUsage(t *testing.T) {
	bin := buildBPE(t)
	cmd := exec.Command(bin, "--bogus")
	err := cmd.Run()

	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("expected *exec.ExitError, got %T (%v)", err, err)
	}
	if got := exitErr.ExitCode(); got != 2 {
		t.Errorf("exit code = %d, want 2", got)
	}
}

func TestProcess_NotImplementedExitsOperational(t *testing.T) {
	bin := buildBPE(t)
	cmd := exec.Command(bin, "validate", "policy.hcl")
	err := cmd.Run()

	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("expected *exec.ExitError, got %T (%v)", err, err)
	}
	if got := exitErr.ExitCode(); got != 3 {
		t.Errorf("exit code = %d, want 3", got)
	}
}

// TestSignalDerivedContext_CancelsOnSIGINT exercises the same
// signal.NotifyContext mechanism main.go wires up, directly, on the test
// process itself. This is a focused unit-level test of the cancellation
// wiring (portable per FSM-10's requirement) rather than a full end-to-end
// subprocess signal test: the bare command is required to return
// immediately with "not implemented yet" rather than block, so there is
// nothing in this release for an external SIGINT to interrupt mid-flight.
func TestSignalDerivedContext_CancelsOnSIGINT(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("SIGINT delivery via syscall.Kill is not portable to Windows")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := syscall.Kill(os.Getpid(), syscall.SIGINT); err != nil {
		t.Fatalf("syscall.Kill(SIGINT) failed: %v", err)
	}

	select {
	case <-ctx.Done():
		if ctx.Err() != context.Canceled {
			t.Errorf("ctx.Err() = %v, want %v", ctx.Err(), context.Canceled)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("context was not cancelled within 2s of SIGINT")
	}
}
