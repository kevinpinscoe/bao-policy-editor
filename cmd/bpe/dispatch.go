package main

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/kevinpinscoe/bao-policy-editor/internal/apperr"
	"github.com/kevinpinscoe/bao-policy-editor/internal/config"
	"github.com/kevinpinscoe/bao-policy-editor/internal/hclpolicy"
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

// dispatchCommand performs (or, for a command not yet built, explicitly
// declines to perform) the action a parsed Command names. Every branch
// either does real, verified work or returns apperr.NotImplemented — never
// a false success. cfg and stdin are threaded through for the commands
// that will need them starting with FSM-13/16/14 (effective-access
// evaluation, OpenBao connectivity, file persistence); neither is read
// yet. stdout is read as of FSM-12, by KindValidate.
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
		return runValidate(cmd.PolicyFile, stdout)
	case KindFormat:
		return apperr.NotImplemented("policy formatting")
	case KindTest:
		return apperr.NotImplemented("policy capability testing")
	default:
		return apperr.Newf(apperr.ExitOperational, "internal error: unhandled command kind %d", cmd.Kind)
	}
}

// runValidate implements `bpe validate <policy.hcl>`: read and parse the
// file, run FSM-12's semantic validation (internal/hclpolicy.Validate)
// when the file decoded cleanly enough to trust its domain model, print
// every diagnostic to stdout, and map the result to the documented
// exit-code contract. It performs no write and contacts no OpenBao
// server.
//
// Semantic validation is skipped when doc.HasErrors() — a file with a
// decode-time error may have an incomplete or empty Policy, and running
// semantic checks against that would report confusing findings (e.g.
// "policy contains no path rules") that only restate the real problem,
// which is already in doc.Diagnostics.
func runValidate(policyFile string, stdout io.Writer) error {
	src, err := os.ReadFile(policyFile)
	if err != nil {
		return apperr.Wrap(apperr.ExitOperational, "failed to read policy file", err).WithDetail(policyFile)
	}

	doc, err := hclpolicy.Parse(policyFile, src)
	if err != nil {
		// Parse's own contract reserves a non-nil error for a condition
		// outside HCL's own diagnostic model; there is currently none,
		// but the check stays so a future one is not silently swallowed.
		return apperr.Wrap(apperr.ExitOperational, "failed to parse policy file", err).WithDetail(policyFile)
	}

	diags := append([]hclpolicy.Diagnostic{}, doc.Diagnostics...)
	if !doc.HasErrors() {
		diags = append(diags, doc.Validate()...)
	}

	if len(diags) == 0 {
		fmt.Fprintf(stdout, "%s: no issues found\n", policyFile)
		return nil
	}

	hasError := false
	for _, d := range diags {
		fmt.Fprintln(stdout, d.String())
		if d.Severity == hclpolicy.SeverityError {
			hasError = true
		}
	}

	if hasError {
		return apperr.New(apperr.ExitPolicyIssue, "policy validation failed")
	}
	return nil
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
