package main

import (
	"context"
	"fmt"
	"io"

	"github.com/kevinpinscoe/bao-policy-editor/internal/apperr"
	"github.com/kevinpinscoe/bao-policy-editor/internal/config"
)

// Execute ties argument parsing, configuration resolution, and command
// dispatch together and returns the process exit code. It performs no
// process-level side effect itself — in particular, it never calls
// os.Exit — so it is fully callable from tests. main (see main.go) is the
// only caller that turns this return value into a real process exit.
//
// stdin/stdout/stderr and lookupEnv are injected rather than read from the
// os package directly, so tests can capture output and supply a synthetic
// environment without mutating real process state.
func Execute(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer, lookupEnv config.EnvLookup) int {
	if err := ctx.Err(); err != nil {
		return renderError(apperr.Interrupted(), stderr)
	}

	cmd, err := ParseArgs(args)
	if err != nil {
		return renderError(err, stderr)
	}

	// Help and version are answered from argv alone — before configuration
	// is even resolved, so a malformed --skip-verify value elsewhere on the
	// command line never blocks `bpe --help`.
	switch cmd.Kind {
	case KindHelp:
		fmt.Fprint(stdout, helpText(cmd.HelpTopic))
		return int(apperr.ExitSuccess)
	case KindVersion:
		fmt.Fprintln(stdout, versionString())
		return int(apperr.ExitSuccess)
	}

	cfg, err := config.Resolve(cmd.ConfigFlags, lookupEnv)
	if err != nil {
		return renderError(err, stderr)
	}

	if err := ctx.Err(); err != nil {
		return renderError(apperr.Interrupted(), stderr)
	}

	if err := dispatchCommand(ctx, cmd, cfg, stdin, stdout, stderr); err != nil {
		return renderError(err, stderr)
	}
	return int(apperr.ExitSuccess)
}

// dispatchCommand performs (or, in this ticket, explicitly declines to
// perform) the action a parsed Command names. Every branch either does
// real, verified work or returns apperr.NotImplemented — never a false
// success. cfg, stdin, and stdout are threaded through for the commands
// that will need them starting with FSM-11 (HCL parsing) and FSM-13/16
// (file persistence, OpenBao connectivity); none of them are read yet.
func dispatchCommand(ctx context.Context, cmd *Command, cfg config.Config, stdin io.Reader, stdout, stderr io.Writer) error {
	if err := ctx.Err(); err != nil {
		return apperr.Interrupted()
	}

	switch cmd.Kind {
	case KindDefault:
		if cmd.PolicyFile == "" {
			return apperr.NotImplemented("the interactive policy editor")
		}
		return apperr.NotImplemented("opening a policy file in the interactive editor")
	case KindValidate:
		return apperr.NotImplemented("policy validation")
	case KindFormat:
		return apperr.NotImplemented("policy formatting")
	case KindTest:
		return apperr.NotImplemented("policy capability testing")
	default:
		return apperr.Newf(apperr.ExitOperational, "internal error: unhandled command kind %d", cmd.Kind)
	}
}

// renderError writes err to stderr — appending the one-line usage summary
// for a usage error, so a mistyped invocation shows the shape it should
// have taken — and returns the exit code it maps to.
func renderError(err error, stderr io.Writer) int {
	code := apperr.CodeOf(err)
	fmt.Fprintln(stderr, err.Error())
	if code == apperr.ExitUsage {
		fmt.Fprintln(stderr, usageLine())
	}
	return int(code)
}
