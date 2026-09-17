package hclpolicy

import (
	"fmt"

	"github.com/hashicorp/hcl/v2"
)

// Severity distinguishes a syntax/structural error, which prevents a rule
// (or the whole file) from being represented in the domain model, from a
// semantic warning, which does not. Deeper semantic validation — unknown
// capabilities, suspicious wildcards, and so on — is Validate's job (see
// validate.go), not decodeFile's; Parse's own diagnostics are limited to
// what decoding itself needs to report.
type Severity int

const (
	SeverityError Severity = iota
	SeverityWarning
)

func (s Severity) String() string {
	switch s {
	case SeverityWarning:
		return "warning"
	default:
		return "error"
	}
}

// Diagnostic is one parse, decode, or validation finding, carrying the
// filename and source position the build brief requires ("Report parse
// diagnostics with filenames and source positions") when one is known. It
// intentionally does not expose hcl.Diagnostic itself, so hclpolicy's
// consumers — the CLI and, later, the TUI — are not coupled to the HCL
// library's own diagnostic type.
type Diagnostic struct {
	Severity Severity
	Summary  string
	Detail   string
	Filename string
	Line     int
	Column   int

	// Path is the OpenBao path pattern this diagnostic concerns, for a
	// Validate finding tied to one rule. Empty for a decode-time
	// diagnostic, or a Validate finding about the policy as a whole (e.g.
	// "no path rules at all") rather than one rule.
	Path string

	// Remediation is actionable guidance on how to address the finding,
	// set by Validate's semantic checks. Empty when Summary/Detail already
	// say everything worth saying, as is typical for decode-time
	// diagnostics.
	Remediation string
}

// String formats the diagnostic as "filename:line:column: severity:
// summary" — a conventional, tool-friendly, single-line form. Detail, if
// present, follows on the same line after a colon. Validate's findings
// carry no source position (they identify a rule by Path, not by a source
// range — see validate.go), so Line/Column are omitted from the line
// entirely rather than printed as a misleading "0:0"; a decode-time
// diagnostic always has a real position and keeps the full
// "filename:line:column:" form unchanged.
func (d Diagnostic) String() string {
	var msg string
	if d.Line == 0 && d.Column == 0 {
		msg = fmt.Sprintf("%s: %s: %s", d.Filename, d.Severity, d.Summary)
	} else {
		msg = fmt.Sprintf("%s:%d:%d: %s: %s", d.Filename, d.Line, d.Column, d.Severity, d.Summary)
	}
	if d.Detail != "" {
		msg = fmt.Sprintf("%s: %s", msg, d.Detail)
	}
	if d.Remediation != "" {
		msg = fmt.Sprintf("%s (%s)", msg, d.Remediation)
	}
	return msg
}

// diagnosticsFromHCL converts hcl.Diagnostics into this package's own
// Diagnostic type, at SeverityError (hcl.DiagError) or SeverityWarning
// (anything else) as appropriate. A diagnostic with no subject range
// (rare — HCL itself sometimes produces one for a whole-file problem)
// reports line/column 0 rather than panicking on a nil pointer.
func diagnosticsFromHCL(diags hcl.Diagnostics) []Diagnostic {
	out := make([]Diagnostic, 0, len(diags))
	for _, d := range diags {
		sev := SeverityWarning
		if d.Severity == hcl.DiagError {
			sev = SeverityError
		}
		out = append(out, diagnosticFromHCL(sev, d.Summary, d.Detail, d.Subject))
	}
	return out
}

func diagnosticFromHCL(sev Severity, summary, detail string, rng *hcl.Range) Diagnostic {
	d := Diagnostic{Severity: sev, Summary: summary, Detail: detail}
	if rng != nil {
		d.Filename = rng.Filename
		d.Line = rng.Start.Line
		d.Column = rng.Start.Column
	}
	return d
}
