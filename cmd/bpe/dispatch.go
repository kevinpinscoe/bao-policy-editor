package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/hashicorp/hcl/v2/hclwrite"

	"github.com/kevinpinscoe/bao-policy-editor/internal/apperr"
	"github.com/kevinpinscoe/bao-policy-editor/internal/config"
	"github.com/kevinpinscoe/bao-policy-editor/internal/evaluator"
	"github.com/kevinpinscoe/bao-policy-editor/internal/fileio"
	"github.com/kevinpinscoe/bao-policy-editor/internal/hclpolicy"
	"github.com/kevinpinscoe/bao-policy-editor/internal/policy"
	"github.com/kevinpinscoe/bao-policy-editor/internal/tui"
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

// dispatchCommand performs the action a parsed Command names. Every
// branch does real, verified work — never a false success. stdout is read
// by KindValidate (FSM-12), KindTest (FSM-13), and KindFormat (FSM-14);
// KindDefault (FSM-15, FSM-17) takes both stdin and stdout, since the
// interactive editor runs on them.
//
// cfg reaches only KindDefault, and only the editor's remote mode reads
// it. validate, format, and test are local-file operations that contact no
// server, so passing the configuration to them would suggest a
// connectivity they do not have.
func dispatchCommand(ctx context.Context, cmd *Command, cfg config.Config, stdin io.Reader, stdout, stderr io.Writer) error {
	if err := ctx.Err(); err != nil {
		return apperr.Interrupted()
	}

	switch cmd.Kind {
	case KindDefault:
		return tui.Run(ctx, tui.Options{
			PolicyFile: cmd.PolicyFile,
			Remote:     cmd.Remote,
			Config:     cfg,
			Input:      stdin,
			Output:     stdout,
		})
	case KindValidate:
		return runValidate(cmd.PolicyFile, stdout)
	case KindFormat:
		return runFormat(cmd.PolicyFile, cmd.FormatCheck, stdout)
	case KindTest:
		return runTest(cmd.PolicyFile, cmd.TestPath, cmd.TestCapability, stdout)
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

// runFormat implements `bpe format <policy.hcl> [--check]`: read the file
// together with a fileio.Snapshot of it, refuse to proceed if the source
// fails to parse as syntactically valid HCL at all, canonicalize it with
// hclwrite.Format, and either report or write the result.
//
// Formatting deliberately never decodes into the policy domain model —
// hclwrite.Format operates purely on the token stream, so it is safe to
// run on a file with decode-time semantic errors (an unparseable
// expiration timestamp) or unsupported content (an unknown attribute or
// block, or contradictory permission constraints Validate would flag):
// none of that is lost, because none of it is ever reconstructed from a
// Policy value. doc.HasSyntaxError, not doc.HasErrors(), is therefore the
// gate — see hclpolicy.Document's doc comment on the distinction.
//
// When the formatted output is byte-identical to the source, this is a
// no-op: nothing is written, so the file's modification time is left
// untouched, matching Kevin's instruction that formatting an
// already-formatted file must not appear to change it.
//
// --check never writes regardless of the outcome; it reports whether the
// file would change and maps that to the exit-code contract's convention
// for a check-only formatter (0 already formatted, 1 would reformat) —
// see the Exit Codes table in README.md.
func runFormat(policyFile string, checkOnly bool, stdout io.Writer) error {
	src, snap, err := fileio.Read(policyFile)
	if err != nil {
		return apperr.Wrap(apperr.ExitOperational, "failed to read policy file", err).WithDetail(policyFile)
	}

	doc, err := hclpolicy.Parse(policyFile, src)
	if err != nil {
		return apperr.Wrap(apperr.ExitOperational, "failed to parse policy file", err).WithDetail(policyFile)
	}
	if doc.HasSyntaxError {
		for _, d := range doc.Diagnostics {
			fmt.Fprintln(stdout, d.String())
		}
		return apperr.New(apperr.ExitPolicyIssue, "policy file has syntax errors; cannot format").WithDetail(policyFile)
	}

	formatted := hclwrite.Format(src)

	if bytes.Equal(formatted, src) {
		fmt.Fprintf(stdout, "%s: already formatted\n", policyFile)
		return nil
	}

	if checkOnly {
		fmt.Fprintf(stdout, "%s: not formatted\n", policyFile)
		return apperr.New(apperr.ExitPolicyIssue, "policy file is not formatted").WithDetail(policyFile)
	}

	if err := fileio.Replace(policyFile, formatted, snap); err != nil {
		return mapFormatWriteError(policyFile, err, stdout)
	}

	fmt.Fprintf(stdout, "%s: formatted\n", policyFile)
	return nil
}

// mapFormatWriteError translates a fileio.Replace error into the
// exit-code contract's convention for a detected write conflict (4) as
// opposed to an ordinary operational failure (3) — pulled out of
// runFormat as its own function so each fileio error kind's mapping is
// directly testable without needing to provoke a real race, symlink, or
// hard link on disk.
func mapFormatWriteError(policyFile string, err error, stdout io.Writer) error {
	var post *fileio.PostReplacementError
	switch {
	case errors.As(err, &post):
		fmt.Fprintf(stdout, "%s: formatted, but a step after writing failed\n", policyFile)
		return apperr.Wrap(apperr.ExitOperational, "policy file was formatted, but a durability step afterward failed; the new content is in place", err).WithDetail(policyFile)
	case errors.Is(err, fileio.ErrConflict):
		return apperr.Wrap(apperr.ExitConflict, "policy file changed on disk since it was read; refusing to overwrite", err).WithDetail(policyFile)
	case errors.Is(err, fileio.ErrSymlink), errors.Is(err, fileio.ErrHardLinked):
		return apperr.Wrap(apperr.ExitOperational, "cannot format this file in place", err).WithDetail(policyFile)
	default:
		return apperr.Wrap(apperr.ExitOperational, "failed to write formatted policy file", err).WithDetail(policyFile)
	}
}

// runTest implements
// `bpe test <policy.hcl> --path <path> --capability <capability>`: read
// and parse the file, compile it into an internal/evaluator.Evaluator,
// simulate the check, print the structured explanation, and map the
// result to the documented exit-code contract. It performs no write and
// contacts no OpenBao server.
//
// --capability is already validated against policy.Capabilities by
// ParseArgs (cli.go) before dispatch ever runs, so an unknown capability
// never reaches here — that is a usage error (exit 2), not a policy or
// evaluation problem.
//
// Three distinct kinds of failure below map to three different exit
// codes, deliberately not collapsed into one (Kevin's instruction,
// 2026-09-17):
//
//   - The policy file itself cannot be trusted — decode errors, or
//     content FSM-11 could not represent at all (doc.Unsupported) — is
//     apperr.ExitPolicyIssue (1): the problem is the policy, not bpe's
//     ability to reason about it.
//   - internal/evaluator.Compile rejects the decoded policy outright
//     (an unknown capability or a malformed "+*" pattern) is also
//     ExitPolicyIssue (1), for the same reason.
//   - The evaluator finds a winning rule but cannot reduce it to a
//     trustworthy decision (evaluator.ErrIncompleteEvaluation — a
//     parameter-constrained or identity-templated winning rule) is
//     apperr.ExitOperational (3): the policy may well be fine, bpe test
//     simply cannot answer with only a path and a capability.
//
// A real, confident deny is ExitPolicyIssue (1); a real, confident allow
// is ExitSuccess (0).
func runTest(policyFile, testPath, testCapability string, stdout io.Writer) error {
	src, err := os.ReadFile(policyFile)
	if err != nil {
		return apperr.Wrap(apperr.ExitOperational, "failed to read policy file", err).WithDetail(policyFile)
	}

	doc, err := hclpolicy.Parse(policyFile, src)
	if err != nil {
		return apperr.Wrap(apperr.ExitOperational, "failed to parse policy file", err).WithDetail(policyFile)
	}

	// bpe test needs a complete, trustworthy domain model to simulate
	// against — unlike bpe validate, which can still usefully report
	// semantic findings around content it could not fully decode. Keep
	// the raw diagnostics visible here too, so unsupported content is
	// never silently dropped on the way to a decision the CLI cannot
	// actually stand behind (Kevin's instruction, 2026-09-17).
	if doc.HasErrors() || doc.Unsupported {
		for _, d := range doc.Diagnostics {
			fmt.Fprintln(stdout, d.String())
		}
		return apperr.New(apperr.ExitPolicyIssue, "policy file has errors or content bpe cannot fully represent; cannot simulate access against it").WithDetail(policyFile)
	}

	ev, err := evaluator.Compile([]evaluator.NamedPolicy{{
		Source: evaluator.Source{Name: policyFile},
		Policy: doc.Policy,
	}}, time.Now())
	if err != nil {
		return apperr.Wrap(apperr.ExitPolicyIssue, "policy is invalid", err).WithDetail(policyFile)
	}

	decision, evalErr := ev.Evaluate(testPath, policy.Capability(testCapability))
	fmt.Fprintln(stdout, decision.Explain())

	if evalErr != nil {
		if errors.Is(evalErr, evaluator.ErrIncompleteEvaluation) {
			return apperr.Wrap(apperr.ExitOperational, "cannot determine a trustworthy access decision", evalErr)
		}
		return apperr.Wrap(apperr.ExitOperational, "evaluation failed", evalErr)
	}

	if !decision.Allowed {
		return apperr.New(apperr.ExitPolicyIssue, "access denied")
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
