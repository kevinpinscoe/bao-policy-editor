// Package hclpolicy translates between BPE's policy domain model
// (internal/policy) and native OpenBao ACL policy HCL. It never converts
// HCL to JSON, and it treats HCL input as user-owned source code: a file
// this package cannot fully represent in the domain model is never
// silently truncated to the parts it understands. See Parse and
// Document's doc comments for how that safety is structured.
package hclpolicy

import (
	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/hcl/v2/hclwrite"

	"github.com/kevinpinscoe/bao-policy-editor/internal/policy"
)

// knownPathAttributes are the attribute names FSM-11 decodes within a
// `path` block. Anything else present is unsupported content — see
// Document.Unsupported.
var knownPathAttributes = map[string]bool{
	"capabilities":        true,
	"comment":             true,
	"expiration":          true,
	"required_parameters": true,
	"allowed_parameters":  true,
	"denied_parameters":   true,
}

// Document is one parsed HCL policy file: its decoded domain-model form,
// the diagnostics produced while decoding it, and whether the source
// contains anything FSM-11's domain model cannot represent.
//
// Document intentionally keeps the parsed hclwrite.File as its source of
// truth for re-emission (Bytes) rather than reconstructing HCL from
// Policy. hclwrite operates on tokens, not a reconstructed struct, so
// comments, unknown attributes, and unsupported blocks survive a
// Parse-then-Bytes round trip unmodified even though Policy itself has no
// field for them — this is what makes "preserve comments, unknown
// attributes, unsupported blocks, and expressions where practical" hold
// without bespoke preservation logic for each construct. Encode, by
// contrast, generates fresh canonical HCL purely from a Policy value and
// is only safe to use for a policy with no unsupported content — see its
// doc comment.
type Document struct {
	// Filename is the name Parse was called with, kept on the Document
	// itself (not just on each Diagnostic) so Validate can stamp it onto
	// findings that have no source position of their own to carry it.
	Filename string

	// Policy is the decoded domain model. When Unsupported is true, it
	// reflects only the subset of the source this package could decode;
	// callers must not treat it as the complete policy in that case.
	Policy policy.Policy

	// Diagnostics are every syntax error and decode-time problem found,
	// in the order encountered. A Document can have Diagnostics and still
	// have a usable Policy: HCL diagnostics accumulate per-problem rather
	// than aborting on the first one.
	Diagnostics []Diagnostic

	// Unsupported is true when the source contains a block or attribute
	// that FSM-11's domain model has no field for. Comments and
	// formatting are preserved regardless via Bytes(); Unsupported
	// specifically means "structurally, something here has no home in
	// Policy," which is the condition the build brief requires a caller
	// to detect before doing anything that would need to regenerate this
	// content from the domain model alone.
	Unsupported bool

	// HasSyntaxError is true when the source failed to parse as
	// syntactically valid HCL at all — a raw hclwrite/hclsyntax parse
	// diagnostic at hcl.DiagError, as opposed to a decode-time semantic
	// finding (an unknown attribute, a malformed known-attribute value
	// such as an unparsable expiration timestamp, and so on), which is
	// syntactically fine HCL that simply says something FSM-11's domain
	// model cannot fully represent or trust.
	//
	// This is deliberately narrower than HasErrors(), which also reports
	// true for those decode-time findings. The distinction exists for
	// FSM-14's `bpe format`: formatting operates on the token stream via
	// hclwrite.Format and never touches the decoded Policy, so it is safe
	// to run on a file with decode-time errors or unsupported content —
	// it only needs to refuse a file HCL itself cannot parse.
	HasSyntaxError bool

	raw *hclwrite.File
}

// HasErrors reports whether any Diagnostic in the Document is
// SeverityError.
func (d *Document) HasErrors() bool {
	for _, diag := range d.Diagnostics {
		if diag.Severity == SeverityError {
			return true
		}
	}
	return false
}

// Bytes re-emits the parsed source. For an unmodified Document this is
// byte-identical to what was parsed, including comments and any content
// this package could not decode — the safe round trip the build brief
// requires. Future tickets that add editing will do so by mutating the
// underlying hclwrite tree, not by re-deriving bytes from Policy.
//
// Bytes returns nil when the source had a syntax error severe enough that
// hclwrite could not build even a token tree (HasErrors() is true in that
// case too) — there is nothing safe to re-emit, and the caller's own
// Diagnostics already explain why.
func (d *Document) Bytes() []byte {
	if d.raw == nil {
		return nil
	}
	return d.raw.Bytes()
}

// Parse decodes an HCL policy file into a Document. It always returns a
// non-nil Document — even one whose Policy is empty and whose
// Diagnostics report a fatal syntax error — so a caller can inspect
// Diagnostics/HasErrors rather than branching on a Go error for what is,
// in HCL, an expected and richly-described outcome. The returned error is
// non-nil only for a condition outside HCL's own diagnostic model (there
// is currently none — Parse never returns a non-nil error — but the
// signature keeps that door open without an API break).
func Parse(filename string, src []byte) (*Document, error) {
	doc := &Document{Filename: filename}

	rawFile, rawDiags := hclwrite.ParseConfig(src, filename, hcl.InitialPos)
	doc.Diagnostics = append(doc.Diagnostics, diagnosticsFromHCL(rawDiags)...)
	if rawDiags.HasErrors() {
		doc.HasSyntaxError = true
	}
	if rawFile == nil {
		// hclwrite failed to produce even a token tree — nothing to
		// preserve or decode. This is rare; hclwrite tolerates most
		// syntax errors that hclsyntax also reports.
		return doc, nil
	}
	doc.raw = rawFile

	hclFile, synDiags := hclsyntax.ParseConfig(src, filename, hcl.InitialPos)
	doc.Diagnostics = append(doc.Diagnostics, diagnosticsFromHCL(synDiags)...)
	if synDiags.HasErrors() {
		doc.HasSyntaxError = true
	}
	if hclFile == nil || hclFile.Body == nil {
		return doc, nil
	}

	body, ok := hclFile.Body.(*hclsyntax.Body)
	if !ok {
		// Not expected from hclsyntax.ParseConfig, but fail safe rather
		// than panic on a type assertion if it ever changes upstream.
		doc.Diagnostics = append(doc.Diagnostics, Diagnostic{
			Severity: SeverityError,
			Summary:  "internal error: unexpected HCL body implementation",
			Filename: filename,
		})
		return doc, nil
	}

	dec := &decoder{filename: filename}
	doc.Policy, doc.Unsupported = dec.decodeFile(body)
	doc.Diagnostics = append(doc.Diagnostics, dec.diagnostics...)

	return doc, nil
}
