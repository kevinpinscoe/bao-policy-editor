// Command bpe is the bao-policy-editor terminal application.
//
// main is deliberately thin: it wires a signal-derived context and the
// real process I/O, calls Execute (dispatch.go) for everything else, and
// is the only place in this program that calls os.Exit. Reusable logic —
// argument parsing (cli.go), configuration resolution (internal/config),
// and command dispatch (dispatch.go) — lives in packages and functions
// that never call os.Exit themselves, so they stay callable from tests.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	code := Execute(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr, os.LookupEnv)
	os.Exit(code)
}
