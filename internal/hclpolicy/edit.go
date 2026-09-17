package hclpolicy

import (
	"bytes"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"time"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/hcl/v2/hclwrite"
	"github.com/zclconf/go-cty/cty"

	"github.com/kevinpinscoe/bao-policy-editor/internal/policy"
)

// This file is BPE's surgical editing layer: it changes one attribute,
// label, or block of an already-parsed policy file and leaves every byte
// it was not asked to change exactly as the user wrote it.
//
// # Why not regenerate from the domain model
//
// Encode (encode.go) builds fresh canonical HCL from a policy.Policy. That
// is the right tool for a document with no source of its own, and the
// wrong one for editing a file a human wrote: policy.Policy has no field
// for a comment, an unknown attribute, an unsupported nested block, or a
// top-level attribute, so regenerating from it silently discards all four.
// The build brief forbids exactly that ("Never silently discard comments,
// unknown attributes, unsupported blocks, or expressions"), so the parsed
// hclwrite token tree — not the domain model — is the source of truth for
// a document that came from a file. Kevin's instruction, 2026-09-17.
//
// # What that buys, concretely
//
// hclwrite operates on tokens. Replacing one attribute's expression leaves
// the block's other attributes, its interior comments, its trailing
// end-of-line comments, its alignment, and any nested block untouched; and
// removing a block takes that block's own leading comments with it while
// leaving an unrelated file header comment alone. Both behaviours were
// verified against hcl v2.25.0 before this layer was written, and are
// pinned by the losslessness tests in edit_test.go.
//
// # The one thing it cannot do
//
// Rewriting an attribute means replacing its expression with a freshly
// generated literal. If what is there now is *not* a literal — a variable
// reference, a function call, an interpolated template, a heredoc, or an
// expression carrying its own comment — that replacement would change the
// file's meaning or lose content. Rather than doing it, or refusing to
// open the file at all, this layer locks that single field and says why;
// see RuleSource and FieldStatus. Every other field of the same rule, and
// every other rule in the file, stays editable.

// RuleRef identifies one `path` block within a Document.
//
// It is the block's occurrence position among the file's `path` blocks,
// counted in source order from zero — not its path text, because OpenBao
// policy files may legitimately contain two blocks with the same path
// (Validate warns about it; it is not an error), and identifying a rule by
// its text would then edit whichever one happened to be found first.
//
// A RuleRef indexes Document.Policy.Rules directly: decodeFile appends
// exactly one rule per `path` block, in source order, so rule i and path
// block i are always the same rule.
//
// A RuleRef is stable only for the Document that produced it. Every
// committed edit reparses the file, which renumbers the blocks after an
// insertion or a removal; a caller holding a selection across an edit is
// responsible for adjusting it, and knows how because it knows which edit
// it just made.
type RuleRef int

var (
	// ErrUnsafeEdit is returned when an operation cannot be performed
	// without losing or ambiguously reassigning source content. It is
	// always accompanied by a message naming the field or construct
	// responsible. The rest of the document remains editable.
	ErrUnsafeEdit = errors.New("hclpolicy: edit would lose or ambiguously reassign source content")

	// ErrNotEditable is returned when the Document has no usable token
	// tree to edit — a file whose HCL could not be parsed at all.
	ErrNotEditable = errors.New("hclpolicy: document has no editable source")

	// ErrNoSuchRule is returned for a RuleRef that does not name a rule in
	// this Document.
	ErrNoSuchRule = errors.New("hclpolicy: no such rule")
)

// editableAttributes is the order supported attributes are presented in,
// and the order EditRule applies them in. It is deliberately a slice
// rather than a range over knownPathAttributes (a map) so that the
// sequence a caller sees, and the order attributes are appended to a block
// that did not previously have them, are both deterministic.
var editableAttributes = []string{
	"capabilities",
	"comment",
	"expiration",
	"required_parameters",
	"allowed_parameters",
	"denied_parameters",
}

// FieldStatus describes one supported attribute as it exists in the
// source, beyond what policy.Rule itself can express.
type FieldStatus struct {
	// Name is the HCL attribute name, e.g. "capabilities".
	Name string

	// Present reports whether the attribute appears in the source at all.
	// This is the distinction policy.Rule cannot carry: an absent
	// `comment` and `comment = ""` both decode to the empty string, and
	// they are not the same file.
	Present bool

	// Lock, when non-empty, explains why this one field cannot be edited.
	// An empty Lock means the field is editable.
	Lock string
}

// Editable reports whether this field can be changed.
func (f FieldStatus) Editable() bool { return f.Lock == "" }

// RuleSource describes what one rule's source actually contains — the
// things policy.Rule has no field for, and which a form must know about
// before it offers to rewrite anything.
type RuleSource struct {
	// Fields covers every supported attribute, in editableAttributes
	// order, whether or not it is present.
	Fields []FieldStatus

	// Unknown names the attributes in this block that BPE's domain model
	// does not represent. They are preserved through every edit; they are
	// listed so a form can say so rather than leaving the user to wonder
	// what happened to them.
	Unknown []string

	// NestedBlocks names the block types nested inside this `path` block
	// that BPE's domain model does not represent. Preserved, and listed,
	// for the same reason.
	NestedBlocks []string

	// PathLock, when non-empty, explains why this rule's path label
	// cannot be changed.
	PathLock string
}

// Field returns the status of one supported attribute by name, and
// whether that name is a supported attribute at all.
func (s RuleSource) Field(name string) (FieldStatus, bool) {
	for _, f := range s.Fields {
		if f.Name == name {
			return f, true
		}
	}
	return FieldStatus{}, false
}

// Editable reports whether this Document can be edited at all.
//
// A file whose HCL does not parse is not editable: without a token tree
// there is nothing to mutate, and without agreement between the hclwrite
// and hclsyntax parses a RuleRef cannot be trusted to name the same block
// in both. Decode-time errors and unsupported content are a different
// matter entirely and do not block editing — preserving content the domain
// model cannot represent is precisely what this layer is for.
func (d *Document) Editable() bool {
	return d.raw != nil && d.syn != nil && !d.HasSyntaxError
}

// RuleCount returns the number of `path` blocks in the document.
func (d *Document) RuleCount() int { return len(d.Policy.Rules) }

// Rule returns the decoded rule at ref.
func (d *Document) Rule(ref RuleRef) (policy.Rule, error) {
	if ref < 0 || int(ref) >= len(d.Policy.Rules) {
		return policy.Rule{}, fmt.Errorf("%w: %d", ErrNoSuchRule, ref)
	}
	return d.Policy.Rules[ref], nil
}

// RuleSource reports what the source contains for the rule at ref: which
// supported attributes are actually present, which of them can be
// rewritten safely, and what unsupported content the block carries.
func (d *Document) RuleSource(ref RuleRef) (RuleSource, error) {
	if !d.Editable() {
		return RuleSource{}, ErrNotEditable
	}
	synBlock, err := d.synPathBlock(ref)
	if err != nil {
		return RuleSource{}, err
	}
	writeBlock, err := d.writePathBlock(d.raw, ref)
	if err != nil {
		return RuleSource{}, err
	}

	src := RuleSource{Fields: make([]FieldStatus, 0, len(editableAttributes))}

	if len(synBlock.Labels) != 1 {
		src.PathLock = fmt.Sprintf("this block has %d labels; a path rule must have exactly one", len(synBlock.Labels))
	}

	for _, name := range editableAttributes {
		status := FieldStatus{Name: name}
		if attr, ok := synBlock.Body.Attributes[name]; ok {
			status.Present = true
			status.Lock = attributeLock(name, attr, writeBlock.Body().GetAttribute(name))
		}
		src.Fields = append(src.Fields, status)
	}

	for name := range synBlock.Body.Attributes {
		if !knownPathAttributes[name] {
			src.Unknown = append(src.Unknown, name)
		}
	}
	slices.Sort(src.Unknown)

	for _, nested := range synBlock.Body.Blocks {
		src.NestedBlocks = append(src.NestedBlocks, nested.Type)
	}

	return src, nil
}

// AttrEdit is one requested change to a supported attribute.
//
// It is deliberately explicit about removal rather than inferring it from
// an empty value, because absent, empty, and populated are three different
// files: `required_parameters = []` is not the same as having no
// `required_parameters` attribute, and a form that collapsed the two would
// quietly rewrite one into the other.
type AttrEdit struct {
	// Name is the supported attribute to change.
	Name string

	// Remove deletes the attribute entirely. Value is ignored when it is
	// true.
	Remove bool

	// Value is the new literal value. It must be a value hclwrite can
	// render — which every constructor below produces.
	Value cty.Value
}

// SetCapabilities builds the edit that replaces a rule's capabilities.
func SetCapabilities(caps []policy.Capability) AttrEdit {
	values := make([]string, len(caps))
	for i, c := range caps {
		values[i] = string(c)
	}
	return AttrEdit{Name: "capabilities", Value: stringsToCty(values)}
}

// SetComment builds the edit that replaces a rule's comment. An empty
// string writes `comment = ""`; use RemoveAttr to delete the attribute.
func SetComment(comment string) AttrEdit {
	return AttrEdit{Name: "comment", Value: cty.StringVal(comment)}
}

// SetExpiration builds the edit that replaces a rule's expiration,
// rendered in RFC 3339 UTC to match Encode.
func SetExpiration(t time.Time) AttrEdit {
	return AttrEdit{Name: "expiration", Value: cty.StringVal(t.UTC().Format(time.RFC3339))}
}

// SetRequiredParameters builds the edit that replaces a rule's
// required_parameters.
func SetRequiredParameters(names []string) AttrEdit {
	return AttrEdit{Name: "required_parameters", Value: stringsToCty(names)}
}

// SetParameterMap builds the edit that replaces allowed_parameters or
// denied_parameters. name must be one of those two.
func SetParameterMap(name string, params []policy.ParameterValues) AttrEdit {
	return AttrEdit{Name: name, Value: parameterMapToCty(params)}
}

// RemoveAttr builds the edit that deletes a supported attribute.
func RemoveAttr(name string) AttrEdit { return AttrEdit{Name: name, Remove: true} }

// EditRule applies newPath (when non-nil) and edits to the rule at ref,
// returning the resulting file bytes. The Document itself is not modified:
// the caller reparses the returned bytes, which is what keeps the decoded
// model, the diagnostics, and the token tree from drifting apart.
//
// Only what is asked for changes. An attribute not named in edits is not
// touched, not reordered, and not reformatted; neither is any comment,
// unknown attribute, nested block, or other rule. Passing a nil newPath
// and an empty edits slice therefore returns bytes identical to the input,
// which is what makes opening a form and accepting it unchanged a true
// no-op.
//
// Every edit is validated before any of them is applied, so a refusal
// never leaves the file half-changed. An edit naming a locked field
// returns ErrUnsafeEdit and changes nothing.
func (d *Document) EditRule(ref RuleRef, newPath *string, edits []AttrEdit) ([]byte, error) {
	if !d.Editable() {
		return nil, ErrNotEditable
	}
	src, err := d.RuleSource(ref)
	if err != nil {
		return nil, err
	}

	if newPath != nil && src.PathLock != "" {
		return nil, fmt.Errorf("%w: path: %s", ErrUnsafeEdit, src.PathLock)
	}
	for _, edit := range edits {
		status, ok := src.Field(edit.Name)
		if !ok {
			return nil, fmt.Errorf("%w: %q is not a supported policy attribute", ErrUnsafeEdit, edit.Name)
		}
		if !status.Editable() {
			return nil, fmt.Errorf("%w: %s: %s", ErrUnsafeEdit, edit.Name, status.Lock)
		}
		if !edit.Remove && edit.Value == cty.NilVal {
			return nil, fmt.Errorf("%w: %s: no value supplied", ErrUnsafeEdit, edit.Name)
		}
	}

	file, err := d.reparseForEdit()
	if err != nil {
		return nil, err
	}
	block, err := d.writePathBlock(file, ref)
	if err != nil {
		return nil, err
	}

	if newPath != nil {
		block.SetLabels([]string{*newPath})
	}
	// Apply in editableAttributes order rather than caller order, so that
	// attributes a block did not previously have are appended in the same
	// sequence every time regardless of how the form happened to collect
	// them.
	for _, name := range editableAttributes {
		for _, edit := range edits {
			if edit.Name != name {
				continue
			}
			if edit.Remove {
				block.Body().RemoveAttribute(name)
				continue
			}
			block.Body().SetAttributeValue(name, edit.Value)
		}
	}

	return file.Bytes(), nil
}

// AddRule appends a new `path` block for rule, generated canonically, and
// returns the resulting file bytes.
//
// This is the one operation that builds HCL from the domain model, and it
// is safe to do so precisely because the block is new: there is no
// existing source for it to lose. Everything already in the file is
// untouched — the new block is appended after it, separated by a blank
// line when the file is not empty.
func (d *Document) AddRule(rule policy.Rule) ([]byte, error) {
	if !d.Editable() {
		return nil, ErrNotEditable
	}
	if rule.Path == "" {
		return nil, fmt.Errorf("%w: a rule must have a path", ErrUnsafeEdit)
	}

	file, err := d.reparseForEdit()
	if err != nil {
		return nil, err
	}
	if len(bytes.TrimSpace(file.Bytes())) > 0 {
		file.Body().AppendNewline()
	}
	encodeRule(file.Body().AppendNewBlock("path", []string{rule.Path}).Body(), rule)
	return file.Bytes(), nil
}

// DuplicateRule appends a byte-for-byte copy of the rule at ref — its
// leading comments, its interior comments, its unknown attributes, and any
// nested block included — and returns the resulting file bytes.
//
// The copy is appended at the end of the file rather than inserted
// directly after the original. hclwrite appends to a body; it has no
// insert-at-position operation, and reproducing one by rebuilding the body
// would mean re-emitting every other block, which is exactly the
// wholesale rewrite this layer exists to avoid. The copy becomes the last
// rule, which is also the highest RuleRef, so a caller can select it
// without guessing.
func (d *Document) DuplicateRule(ref RuleRef) ([]byte, error) {
	if !d.Editable() {
		return nil, ErrNotEditable
	}

	file, err := d.reparseForEdit()
	if err != nil {
		return nil, err
	}
	block, err := d.writePathBlock(file, ref)
	if err != nil {
		return nil, err
	}

	tokens := block.BuildTokens(nil)
	file.Body().AppendNewline()
	file.Body().AppendUnstructuredTokens(tokens)
	return file.Bytes(), nil
}

// Removal describes what removing a rule takes with it, so that a review
// screen can say so before the removal is committed rather than leaving it
// to be discovered in a diff.
type Removal struct {
	// Path is the removed rule's path label.
	Path string

	// LeadComments is the comment text immediately above the block, which
	// hclwrite treats as belonging to it and removes along with it. This
	// is the case most likely to surprise: those comments are not inside
	// the block, and leaving them behind would be worse than removing them
	// — an orphaned comment sitting above the *next* rule reads as
	// documentation for that rule, which in an access-control file is a
	// genuine hazard rather than an untidy leftover.
	LeadComments string

	// Unknown and NestedBlocks name the unsupported content inside the
	// block that goes with it.
	Unknown      []string
	NestedBlocks []string
}

// RemoveRule deletes the rule at ref and returns the resulting file bytes
// together with a description of everything the removal takes with it.
//
// hclwrite removes the block's own leading comments along with the block,
// and leaves comments that belong to the file or to another block alone —
// verified against hcl v2.25.0 and pinned by TestRemoveRuleComments.
func (d *Document) RemoveRule(ref RuleRef) ([]byte, Removal, error) {
	if !d.Editable() {
		return nil, Removal{}, ErrNotEditable
	}
	rule, err := d.Rule(ref)
	if err != nil {
		return nil, Removal{}, err
	}
	src, err := d.RuleSource(ref)
	if err != nil {
		return nil, Removal{}, err
	}

	file, err := d.reparseForEdit()
	if err != nil {
		return nil, Removal{}, err
	}
	block, err := d.writePathBlock(file, ref)
	if err != nil {
		return nil, Removal{}, err
	}

	removal := Removal{
		Path:         rule.Path,
		LeadComments: string(block.LeadComments().Bytes()),
		Unknown:      src.Unknown,
		NestedBlocks: src.NestedBlocks,
	}

	if !file.Body().RemoveBlock(block) {
		return nil, Removal{}, fmt.Errorf("%w: rule %d could not be removed", ErrUnsafeEdit, ref)
	}

	return collapseBlankRun(d.Bytes(), file.Bytes()), removal, nil
}

// blankRun matches three or more consecutive newlines — two or more blank
// lines — allowing for carriage returns.
var blankRun = regexp.MustCompile(`(?:\r?\n){3,}`)

// collapseBlankRun tidies the extra blank line removing a block leaves
// where the block's own trailing newline and the blank line separating it
// from its neighbour end up adjacent.
//
// It only does so when the file did not already contain a run of blank
// lines of its own. That guard is what keeps this from being a
// whitespace reformat of someone else's file: if the user never wrote two
// blank lines in a row anywhere, a pair appearing after a removal can only
// have come from the removal, and collapsing it is restoring the file's
// own convention rather than imposing one.
func collapseBlankRun(before, after []byte) []byte {
	if blankRun.Match(before) {
		return after
	}
	return blankRun.ReplaceAll(after, []byte("\n\n"))
}

// reparseForEdit builds a fresh, independently-mutable token tree from the
// Document's current bytes.
//
// Editing a copy rather than d.raw is what makes a refused or failed
// operation a true no-op: the Document a caller is holding is never left
// half-modified, and the caller decides whether to adopt the returned
// bytes by reparsing them.
func (d *Document) reparseForEdit() (*hclwrite.File, error) {
	file, diags := hclwrite.ParseConfig(d.Bytes(), d.Filename, hcl.InitialPos)
	if diags.HasErrors() || file == nil {
		return nil, ErrNotEditable
	}
	return file, nil
}

// synPathBlock returns the ref-th `path` block of the hclsyntax parse.
func (d *Document) synPathBlock(ref RuleRef) (*hclsyntax.Block, error) {
	n := 0
	for _, block := range d.syn.Blocks {
		if block.Type != "path" {
			continue
		}
		if n == int(ref) {
			return block, nil
		}
		n++
	}
	return nil, fmt.Errorf("%w: %d", ErrNoSuchRule, ref)
}

// writePathBlock returns the ref-th `path` block of an hclwrite parse of
// the same bytes.
//
// The two parsers agree on how many `path` blocks a file has and in what
// order, because they are parsing the same source with the same grammar —
// which is what lets one RuleRef address a rule in the decoded model, in
// the hclsyntax tree, and in the token tree at once.
func (d *Document) writePathBlock(file *hclwrite.File, ref RuleRef) (*hclwrite.Block, error) {
	n := 0
	for _, block := range file.Body().Blocks() {
		if block.Type() != "path" {
			continue
		}
		if n == int(ref) {
			return block, nil
		}
		n++
	}
	return nil, fmt.Errorf("%w: %d", ErrNoSuchRule, ref)
}

// attributeLock decides whether one attribute's value can be replaced with
// a freshly generated literal without changing or losing anything the user
// wrote, and returns the reason it cannot when it cannot.
//
// The three refusals are all about the same thing — replacement destroys
// whatever was there — differing only in what would be destroyed.
func attributeLock(name string, attr *hclsyntax.Attribute, writeAttr *hclwrite.Attribute) string {
	if writeAttr != nil {
		tokens := writeAttr.Expr().BuildTokens(nil)
		for _, tok := range tokens {
			switch tok.Type {
			case hclsyntax.TokenOHeredoc:
				return "the value is a heredoc; rewriting it would silently convert it to a quoted string"
			case hclsyntax.TokenComment:
				return "the value carries a comment that rewriting it would discard"
			}
		}
	}
	if !isLiteralExpr(attr.Expr) {
		return "the value is an expression rather than a literal; rewriting it would replace the expression itself"
	}
	return ""
}

// isLiteralExpr reports whether expr is a plain literal that regenerating
// from a cty.Value reproduces faithfully.
//
// Anything with a moving part — a variable reference, a function call, an
// interpolated or conditional template, even a redundant pair of
// parentheses — answers false. The test is deliberately conservative: a
// false negative locks one field and explains why, while a false positive
// silently rewrites a user's expression into whatever it happened to
// evaluate to, which is the failure this whole layer exists to prevent.
func isLiteralExpr(expr hclsyntax.Expression) bool {
	switch e := expr.(type) {
	case *hclsyntax.LiteralValueExpr:
		return true

	case *hclsyntax.TemplateExpr:
		// True only for a template with no interpolation or directive —
		// an ordinary quoted string.
		return e.IsStringLiteral()

	case *hclsyntax.TupleConsExpr:
		for _, item := range e.Exprs {
			if !isLiteralExpr(item) {
				return false
			}
		}
		return true

	case *hclsyntax.ObjectConsExpr:
		for _, item := range e.Items {
			if !isLiteralObjectKey(item.KeyExpr) || !isLiteralExpr(item.ValueExpr) {
				return false
			}
		}
		return true

	case *hclsyntax.ObjectConsKeyExpr:
		return isLiteralObjectKey(e)

	default:
		return false
	}
}

// isLiteralObjectKey reports whether an object key is one BPE can
// reproduce: a quoted string, or a bare identifier such as the `foo` in
// `allowed_parameters = { foo = ["a"] }`, which HCL parses as a traversal
// but treats as the literal name.
func isLiteralObjectKey(expr hclsyntax.Expression) bool {
	key, ok := expr.(*hclsyntax.ObjectConsKeyExpr)
	if !ok {
		return isLiteralExpr(expr)
	}
	if hcl.ExprAsKeyword(key.Wrapped) != "" && !key.ForceNonLiteral {
		return true
	}
	return isLiteralExpr(key.Wrapped)
}
