//go:build !windows

package main

import (
	"bytes"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"
)

// signalRuns is how many times the editor is started and signalled.
// Before FSM-48 the wrong outcome came up in about one run in six under a
// pty, and in 54 of 60 runs here, so an unfixed build cannot pass.
const signalRuns = 60

// TestProcess_SignalAlwaysExitsInterrupted is the FSM-48 regression test.
//
// SIGTERM and SIGINT must end the interactive editor with exit 130, every
// time. Before the fix, one signal reached two handlers — main's signal.NotifyContext
// and Bubble Tea's default handler — and the race between them gave exit
// 130, exit 0 (Bubble Tea's QuitMsg won, on SIGTERM), or a hang (Bubble Tea's signal
// goroutine blocked on its send while shutdown waited for it). A single run
// usually passes, so the test repeats it.
//
// It runs the compiled program with a pipe for stdin and stdout, which
// Bubble Tea accepts; the race does not depend on a terminal. Terminal
// restoration under a real pty is covered by bao-breakglass's
// tests/bpe-baseline-probe.py.
func TestProcess_SignalAlwaysExitsInterrupted(t *testing.T) {
	bin := buildBPE(t)
	policy := filepath.Join(t.TempDir(), "policy.hcl")
	if err := os.WriteFile(policy, []byte("path \"secret/*\" {\n  capabilities = [\"read\"]\n}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, sig := range []syscall.Signal{syscall.SIGTERM, syscall.SIGINT} {
		t.Run(sig.String(), func(t *testing.T) {
			outcomes := map[string]int{}
			for i := 0; i < signalRuns; i++ {
				outcomes[signalOnce(t, bin, policy, sig)]++
			}
			if outcomes["exit 130"] != signalRuns {
				t.Errorf("%v outcomes over %d runs = %v, want exit 130 every time", sig, signalRuns, outcomes)
			}
		})
	}
}

// signalOnce starts the editor on policy, waits until it has drawn its
// first frame, sends sig and reports what happened: "exit N", "hang",
// or "no frame" if the editor never started drawing.
func signalOnce(t *testing.T, bin, policy string, sig syscall.Signal) string {
	t.Helper()

	stdinR, stdinW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer stdinW.Close() // held open, so the editor never sees end of input
	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}

	var stderr bytes.Buffer
	cmd := exec.Command(bin, policy)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdinR, stdoutW, &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	stdinR.Close()
	stdoutW.Close()

	// The first byte of output means the program is running, so both
	// signal handlers are installed. Drain the rest so the editor can
	// never block on a full pipe and look like a hang.
	firstFrame := make(chan struct{})
	go func() {
		buf := make([]byte, 1)
		if n, _ := stdoutR.Read(buf); n > 0 {
			close(firstFrame)
		}
		io.Copy(io.Discard, stdoutR)
		stdoutR.Close()
	}()

	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()

	select {
	case <-firstFrame:
	case <-time.After(10 * time.Second):
		cmd.Process.Kill()
		<-exited
		return "no frame"
	}

	if err := cmd.Process.Signal(sig); err != nil {
		t.Fatalf("sending %v: %v", sig, err)
	}

	select {
	case err := <-exited:
		var exitErr *exec.ExitError
		switch {
		case err == nil:
			return "exit 0"
		case errors.As(err, &exitErr):
			if exitErr.ExitCode() != 130 {
				t.Logf("exit %d, stderr: %s", exitErr.ExitCode(), stderr.String())
			}
			return "exit " + strconv.Itoa(exitErr.ExitCode())
		default:
			t.Fatalf("waiting for bpe: %v", err)
			return ""
		}
	case <-time.After(5 * time.Second):
		cmd.Process.Kill()
		<-exited
		return "hang"
	}
}
