// Package apperr defines BPE's structured application error and its
// exit-code contract. Both the CLI (cmd/bpe) and, later, the TUI
// (internal/tui) construct and present errors through this type so the two
// front ends share one vocabulary for "what went wrong and how severely."
package apperr

import (
	"errors"
	"fmt"
)

// ExitCode is a process exit status. Named constants are used everywhere
// instead of bare integers so the meaning of a given exit is documented at
// the call site rather than left to be memorized.
type ExitCode int

// The exit-code contract for BPE. 0, 2, 3, and 130 are active as of FSM-10.
// 1 and 4 are reserved for later tickets (policy validation/test results,
// and concurrent-modification conflicts) and are not returned by any code
// introduced in this ticket.
const (
	// ExitSuccess is returned for a successful command, or for --help/help/
	// --version output.
	ExitSuccess ExitCode = 0

	// ExitPolicyIssue is reserved for a policy validation failure or a
	// denied policy test result. No command in this ticket returns it.
	ExitPolicyIssue ExitCode = 1

	// ExitUsage is returned for a command-line usage or configuration
	// error: bad arguments, an unknown command or flag, a missing required
	// value, or a malformed configuration value (e.g. an unparsable
	// boolean).
	ExitUsage ExitCode = 2

	// ExitOperational is returned for an operational failure, including a
	// command whose real implementation is deliberately not built yet.
	ExitOperational ExitCode = 3

	// ExitConflict is reserved for a concurrent modification or version
	// conflict (e.g. an OpenBao policy changed remotely since it was
	// read). No command in this ticket returns it.
	ExitConflict ExitCode = 4

	// ExitInterrupted is returned when the user interrupts the process
	// (Ctrl+C / SIGINT) or the process receives SIGTERM.
	ExitInterrupted ExitCode = 130
)

// AppError is BPE's structured error type. It carries a user-facing
// message, the exit code (or category) it maps to, the underlying cause it
// wraps (if any), and an optional safe contextual detail — never a
// credential, token, or other sensitive value.
type AppError struct {
	// Message is shown to the user. It must never contain a token,
	// credential, or other sensitive value.
	Message string

	// Code is the exit code this error maps to.
	Code ExitCode

	// Cause is the underlying error being wrapped, if any. It participates
	// in errors.Is/errors.As via Unwrap.
	Cause error

	// Detail is optional, safe, additional context (e.g. a flag name or a
	// file path). It must never contain a token, credential, or other
	// sensitive value.
	Detail string
}

// Error implements the error interface. It deliberately does not include a
// stack trace or any internal diagnostic beyond Message/Detail/Cause — see
// the package doc.
func (e *AppError) Error() string {
	msg := e.Message
	if e.Detail != "" {
		msg = fmt.Sprintf("%s: %s", msg, e.Detail)
	}
	if e.Cause != nil {
		msg = fmt.Sprintf("%s: %v", msg, e.Cause)
	}
	return msg
}

// Unwrap exposes Cause to errors.Is and errors.As.
func (e *AppError) Unwrap() error {
	return e.Cause
}

// ExitCode returns the exit code this error maps to. main is the only
// place that turns this into an os.Exit call.
func (e *AppError) ExitCode() ExitCode {
	return e.Code
}

// New creates an AppError with no wrapped cause.
func New(code ExitCode, message string) *AppError {
	return &AppError{Message: message, Code: code}
}

// Newf creates an AppError with no wrapped cause, formatting the message.
func Newf(code ExitCode, format string, args ...any) *AppError {
	return &AppError{Message: fmt.Sprintf(format, args...), Code: code}
}

// Wrap creates an AppError that wraps an underlying cause.
func Wrap(code ExitCode, message string, cause error) *AppError {
	return &AppError{Message: message, Code: code, Cause: cause}
}

// WithDetail returns a copy of e with Detail set. It never mutates e.
func (e *AppError) WithDetail(detail string) *AppError {
	cp := *e
	cp.Detail = detail
	return &cp
}

// Usage constructs an ExitUsage AppError for a command-line usage or
// configuration error.
func Usage(message string) *AppError {
	return New(ExitUsage, message)
}

// Usagef constructs an ExitUsage AppError, formatting the message.
func Usagef(format string, args ...any) *AppError {
	return Newf(ExitUsage, format, args...)
}

// NotImplemented constructs an ExitOperational AppError for a command whose
// real implementation belongs to a later ticket. feature should name the
// behavior plainly, e.g. "policy validation".
func NotImplemented(feature string) *AppError {
	return Newf(ExitOperational, "%s is not implemented yet", feature)
}

// Interrupted constructs the AppError returned when the process is
// cancelled by a signal (Ctrl+C / SIGTERM).
func Interrupted() *AppError {
	return New(ExitInterrupted, "interrupted")
}

// CodeOf returns the exit code for err: ExitSuccess for a nil error, the
// AppError's own code when err is (or wraps) an *AppError, and
// ExitOperational for any other error. This is the single place that maps
// an arbitrary error to a process exit code.
func CodeOf(err error) ExitCode {
	if err == nil {
		return ExitSuccess
	}
	var appErr *AppError
	if errors.As(err, &appErr) {
		return appErr.Code
	}
	return ExitOperational
}
