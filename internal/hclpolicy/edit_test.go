package hclpolicy

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/kevinpinscoe/bao-policy-editor/internal/policy"
)

// richSource exercises every kind of content the editing layer promises to
// preserve, in one file: a file-level header comment, a comment attached to
// a block, comments inside a block, an end-of-line comment, an unknown
// attribute, an unsupported nested block, an unknown top-level attribute, an
// unsupported top-level block, and a trailing comment after the last block.
const richSource = `# file header — belongs to the file, not to any rule

audit_device = "stdout" # unknown top-level attribute

# describes rule A
path "secret/data/a/*" {
  # why rule A exists
  capabilities = ["read", "list"] # trailing note
  weird_attr   = "kept"

  nested "thing" {
    value = 1
  }
}

path "secret/data/b" {
  capabilities = ["update"]
  comment      = "rule B"
}

unsupported_block "x" {
  y = 2
}

# trailing comment at the end of the file
`

func parseForEdit(t *testing.T, src string) *Document {
	t.Helper()
	doc, err := Parse("policy.hcl", []byte(src))
	if err != nil {
		t.Fatalf("Parse returned an error: %v", err)
	}
	if doc.HasSyntaxError {
		t.Fatalf("fixture does not parse: %v", doc.Diagnostics)
	}
	if !doc.Editable() {
		t.Fatal("fixture parsed but is not editable")
	}
	return doc
}

// commit applies bytes the way the TUI does — reparse, so every assertion
// afterwards is made against a document built the same way a real session
// would build it.
func commit(t *testing.T, src []byte) *Document {
	t.Helper()
	doc, err := Parse("policy.hcl", src)
	if err != nil {
		t.Fatalf("reparse returned an error: %v", err)
	}
	if doc.HasSyntaxError {
		t.Fatalf("edit produced unparseable HCL:\n%s\ndiagnostics: %v", src, doc.Diagnostics)
	}
	return doc
}

func TestParseThenBytesIsIdentical(t *testing.T) {
	doc := parseForEdit(t, richSource)
	if got := string(doc.Bytes()); got != richSource {
		t.Errorf("round trip changed the file:\n--- got ---\n%s\n--- want ---\n%s", got, richSource)
	}
}

func TestEditRuleWithNoChangesIsAByteLevelNoOp(t *testing.T) {
	doc := parseForEdit(t, richSource)
	for ref := range doc.RuleCount() {
		got, err := doc.EditRule(RuleRef(ref), nil, nil)
		if err != nil {
			t.Fatalf("EditRule(%d) returned an error: %v", ref, err)
		}
		if string(got) != richSource {
			t.Errorf("EditRule(%d) with no changes rewrote the file:\n%s", ref, got)
		}
	}
}

func TestEditRulePreservesEverythingItWasNotAskedToChange(t *testing.T) {
	doc := parseForEdit(t, richSource)

	got, err := doc.EditRule(0, nil, []AttrEdit{
		SetCapabilities([]policy.Capability{policy.CapabilityRead, policy.CapabilityList, policy.CapabilityScan}),
	})
	if err != nil {
		t.Fatalf("EditRule returned an error: %v", err)
	}
	out := string(got)

	if !strings.Contains(out, `capabilities = ["read", "list", "scan"]`) {
		t.Errorf("capabilities were not updated:\n%s", out)
	}

	preserved := []string{
		"# file header — belongs to the file, not to any rule",
		`audit_device = "stdout" # unknown top-level attribute`,
		"# describes rule A",
		"# why rule A exists",
		"# trailing note",
		`weird_attr   = "kept"`,
		`nested "thing" {`,
		`unsupported_block "x" {`,
		"# trailing comment at the end of the file",
		`comment      = "rule B"`,
	}
	for _, want := range preserved {
		if !strings.Contains(out, want) {
			t.Errorf("edit discarded %q:\n%s", want, out)
		}
	}
}

func TestEditRuleRenamesOnlyTheSelectedBlockAmongDuplicatePaths(t *testing.T) {
	const src = `path "secret/data/shared" {
  capabilities = ["read"]
}

path "secret/data/shared" {
  capabilities = ["list"]
}
`
	doc := parseForEdit(t, src)
	if doc.RuleCount() != 2 {
		t.Fatalf("expected 2 rules, got %d", doc.RuleCount())
	}

	renamed := "secret/data/second"
	got, err := doc.EditRule(1, &renamed, nil)
	if err != nil {
		t.Fatalf("EditRule returned an error: %v", err)
	}

	after := commit(t, got)
	if after.Policy.Rules[0].Path != "secret/data/shared" {
		t.Errorf("the first duplicate was renamed too: %q", after.Policy.Rules[0].Path)
	}
	if after.Policy.Rules[1].Path != renamed {
		t.Errorf("the second duplicate was not renamed: %q", after.Policy.Rules[1].Path)
	}
}

func TestEditRuleDistinguishesAbsentEmptyAndPopulated(t *testing.T) {
	const src = `path "secret/data/a" {
  capabilities = ["read"]
}

path "secret/data/b" {
  capabilities = ["read"]
  comment      = ""
}
`
	doc := parseForEdit(t, src)

	first, ok := mustRuleSource(t, doc, 0).Field("comment")
	if !ok || first.Present {
		t.Errorf("rule 0 has no comment attribute but reported Present = %v", first.Present)
	}
	second, ok := mustRuleSource(t, doc, 1).Field("comment")
	if !ok || !second.Present {
		t.Errorf(`rule 1 has comment = "" but reported Present = %v`, second.Present)
	}

	// Removing an absent attribute must not invent one, and must not
	// disturb the file.
	got, err := doc.EditRule(0, nil, []AttrEdit{RemoveAttr("comment")})
	if err != nil {
		t.Fatalf("EditRule returned an error: %v", err)
	}
	if string(got) != src {
		t.Errorf("removing an absent attribute changed the file:\n%s", got)
	}
}

func TestEditRuleRefusesALockedField(t *testing.T) {
	tests := []struct {
		name      string
		src       string
		attribute string
		wantLock  string
	}{
		{
			name: "variable reference",
			src: `path "secret/data/a" {
  capabilities = var.caps
}
`,
			attribute: "capabilities",
			wantLock:  "expression rather than a literal",
		},
		{
			name: "interpolated string",
			src: `path "secret/data/a" {
  capabilities = ["read"]
  comment      = "owned by ${team}"
}
`,
			attribute: "comment",
			wantLock:  "expression rather than a literal",
		},
		{
			name: "heredoc",
			src: `path "secret/data/a" {
  capabilities = ["read"]
  comment      = <<-EOT
    a multi-line note
  EOT
}
`,
			attribute: "comment",
			wantLock:  "heredoc",
		},
		{
			name: "comment inside the value",
			src: `path "secret/data/a" {
  capabilities = [
    "read", # only reading
    "list",
  ]
}
`,
			attribute: "capabilities",
			wantLock:  "carries a comment",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			doc := parseForEdit(t, tc.src)
			status, ok := mustRuleSource(t, doc, 0).Field(tc.attribute)
			if !ok {
				t.Fatalf("%s is not a supported attribute", tc.attribute)
			}
			if status.Editable() {
				t.Fatalf("expected %s to be locked, but it is editable", tc.attribute)
			}
			if !strings.Contains(status.Lock, tc.wantLock) {
				t.Errorf("lock reason = %q, want it to mention %q", status.Lock, tc.wantLock)
			}

			_, err := doc.EditRule(0, nil, []AttrEdit{SetComment("replacement"), SetCapabilities([]policy.Capability{policy.CapabilityRead})})
			if !errors.Is(err, ErrUnsafeEdit) {
				t.Fatalf("EditRule error = %v, want ErrUnsafeEdit", err)
			}
		})
	}
}

func TestALockedFieldDoesNotLockTheRestOfTheRule(t *testing.T) {
	const src = `path "secret/data/a" {
  capabilities = var.caps
  comment      = "editable"
}
`
	doc := parseForEdit(t, src)

	got, err := doc.EditRule(0, nil, []AttrEdit{SetComment("still editable")})
	if err != nil {
		t.Fatalf("editing an unlocked field of a partly-locked rule failed: %v", err)
	}
	out := string(got)
	if !strings.Contains(out, `comment      = "still editable"`) {
		t.Errorf("comment was not updated:\n%s", out)
	}
	if !strings.Contains(out, "capabilities = var.caps") {
		t.Errorf("the locked expression was rewritten:\n%s", out)
	}
}

func TestAddRuleAppendsWithoutDisturbingTheFile(t *testing.T) {
	doc := parseForEdit(t, richSource)

	got, err := doc.AddRule(policy.Rule{
		Path:         "secret/data/new",
		Capabilities: []policy.Capability{policy.CapabilityRead},
		Comment:      "added by a test",
	})
	if err != nil {
		t.Fatalf("AddRule returned an error: %v", err)
	}
	if !strings.HasPrefix(string(got), richSource) {
		t.Errorf("AddRule modified the existing content:\n%s", got)
	}

	after := commit(t, got)
	if after.RuleCount() != doc.RuleCount()+1 {
		t.Fatalf("rule count = %d, want %d", after.RuleCount(), doc.RuleCount()+1)
	}
	added := after.Policy.Rules[after.RuleCount()-1]
	if added.Path != "secret/data/new" || added.Comment != "added by a test" {
		t.Errorf("added rule = %+v", added)
	}
}

func TestAddRuleIntoAnEmptyDocumentDoesNotLeadWithABlankLine(t *testing.T) {
	doc := parseForEdit(t, "")

	got, err := doc.AddRule(policy.Rule{
		Path:         "secret/data/new",
		Capabilities: []policy.Capability{policy.CapabilityRead},
	})
	if err != nil {
		t.Fatalf("AddRule returned an error: %v", err)
	}
	if strings.HasPrefix(string(got), "\n") {
		t.Errorf("the first rule of an empty document was preceded by a blank line:\n%q", got)
	}
}

func TestDuplicateRuleCopiesCommentsAndUnsupportedContent(t *testing.T) {
	doc := parseForEdit(t, richSource)

	got, err := doc.DuplicateRule(0)
	if err != nil {
		t.Fatalf("DuplicateRule returned an error: %v", err)
	}
	if !strings.HasPrefix(string(got), richSource) {
		t.Errorf("DuplicateRule modified the existing content:\n%s", got)
	}

	after := commit(t, got)
	if after.RuleCount() != doc.RuleCount()+1 {
		t.Fatalf("rule count = %d, want %d", after.RuleCount(), doc.RuleCount()+1)
	}

	// The copy is the last rule, and it carries everything the original
	// carried — including the parts the domain model cannot represent.
	copyRef := RuleRef(after.RuleCount() - 1)
	copySrc := mustRuleSource(t, after, copyRef)
	if len(copySrc.Unknown) != 1 || copySrc.Unknown[0] != "weird_attr" {
		t.Errorf("duplicate lost its unknown attribute: %v", copySrc.Unknown)
	}
	if len(copySrc.NestedBlocks) != 1 || copySrc.NestedBlocks[0] != "nested" {
		t.Errorf("duplicate lost its nested block: %v", copySrc.NestedBlocks)
	}

	tail := string(got)[len(richSource):]
	for _, want := range []string{"# describes rule A", "# why rule A exists", "# trailing note"} {
		if !strings.Contains(tail, want) {
			t.Errorf("duplicate lost %q:\n%s", want, tail)
		}
	}
}

func TestDuplicateThenEditLeavesTheOriginalAlone(t *testing.T) {
	doc := parseForEdit(t, richSource)

	duplicated, err := doc.DuplicateRule(0)
	if err != nil {
		t.Fatalf("DuplicateRule returned an error: %v", err)
	}
	after := commit(t, duplicated)

	copyRef := RuleRef(after.RuleCount() - 1)
	renamed := "secret/data/a-copy/*"
	edited, err := after.EditRule(copyRef, &renamed, []AttrEdit{
		SetCapabilities([]policy.Capability{policy.CapabilityRead}),
	})
	if err != nil {
		t.Fatalf("EditRule on the duplicate returned an error: %v", err)
	}

	final := commit(t, edited)
	original := final.Policy.Rules[0]
	if original.Path != "secret/data/a/*" {
		t.Errorf("editing the duplicate renamed the original: %q", original.Path)
	}
	if len(original.Capabilities) != 2 {
		t.Errorf("editing the duplicate changed the original's capabilities: %v", original.Capabilities)
	}
	if final.Policy.Rules[copyRef].Path != renamed {
		t.Errorf("the duplicate was not renamed: %q", final.Policy.Rules[copyRef].Path)
	}
}

func TestRemoveRuleComments(t *testing.T) {
	doc := parseForEdit(t, richSource)

	got, removal, err := doc.RemoveRule(0)
	if err != nil {
		t.Fatalf("RemoveRule returned an error: %v", err)
	}
	out := string(got)

	if removal.Path != "secret/data/a/*" {
		t.Errorf("removal.Path = %q", removal.Path)
	}
	if !strings.Contains(removal.LeadComments, "# describes rule A") {
		t.Errorf("removal did not report the block's leading comment: %q", removal.LeadComments)
	}
	if len(removal.Unknown) != 1 || removal.Unknown[0] != "weird_attr" {
		t.Errorf("removal did not report the unknown attribute: %v", removal.Unknown)
	}
	if len(removal.NestedBlocks) != 1 || removal.NestedBlocks[0] != "nested" {
		t.Errorf("removal did not report the nested block: %v", removal.NestedBlocks)
	}

	// The rule's own comments go with it — an orphaned "# describes rule
	// A" left sitting above rule B would read as documentation for rule B.
	for _, gone := range []string{"# describes rule A", "# why rule A exists", `weird_attr   = "kept"`, `nested "thing" {`} {
		if strings.Contains(out, gone) {
			t.Errorf("%q survived the removal:\n%s", gone, out)
		}
	}
	// Everything that belongs to the file or to another rule stays.
	for _, kept := range []string{
		"# file header — belongs to the file, not to any rule",
		`audit_device = "stdout" # unknown top-level attribute`,
		`path "secret/data/b" {`,
		`unsupported_block "x" {`,
		"# trailing comment at the end of the file",
	} {
		if !strings.Contains(out, kept) {
			t.Errorf("%q was removed along with the rule:\n%s", kept, out)
		}
	}

	after := commit(t, got)
	if after.RuleCount() != doc.RuleCount()-1 {
		t.Errorf("rule count = %d, want %d", after.RuleCount(), doc.RuleCount()-1)
	}
}

func TestRemoveRuleDoesNotLeaveAStackOfBlankLines(t *testing.T) {
	const src = `path "secret/data/a" {
  capabilities = ["read"]
}

path "secret/data/b" {
  capabilities = ["list"]
}

path "secret/data/c" {
  capabilities = ["update"]
}
`
	doc := parseForEdit(t, src)

	got, _, err := doc.RemoveRule(1)
	if err != nil {
		t.Fatalf("RemoveRule returned an error: %v", err)
	}
	if strings.Contains(string(got), "\n\n\n") {
		t.Errorf("removal left a run of blank lines:\n%q", got)
	}
}

func TestRemoveRuleLeavesAnExistingBlankRunAlone(t *testing.T) {
	// A file that already uses double blank lines keeps them: the
	// tidy-up only fires when the run can only have come from the removal.
	const src = `path "secret/data/a" {
  capabilities = ["read"]
}


path "secret/data/b" {
  capabilities = ["list"]
}


path "secret/data/c" {
  capabilities = ["update"]
}
`
	doc := parseForEdit(t, src)

	got, _, err := doc.RemoveRule(0)
	if err != nil {
		t.Fatalf("RemoveRule returned an error: %v", err)
	}
	if !strings.Contains(string(got), "}\n\n\npath \"secret/data/c\"") {
		t.Errorf("the file's own double blank lines were collapsed:\n%q", got)
	}
}

func TestOperationsRefuseAnUnparseableDocument(t *testing.T) {
	doc, err := Parse("broken.hcl", []byte(`path "secret/data/a" {`))
	if err != nil {
		t.Fatalf("Parse returned an error: %v", err)
	}
	if !doc.HasSyntaxError {
		t.Fatal("fixture was expected to have a syntax error")
	}
	if doc.Editable() {
		t.Fatal("a document with a syntax error reported itself as editable")
	}

	if _, err := doc.EditRule(0, nil, nil); !errors.Is(err, ErrNotEditable) {
		t.Errorf("EditRule error = %v, want ErrNotEditable", err)
	}
	if _, err := doc.AddRule(policy.Rule{Path: "x"}); !errors.Is(err, ErrNotEditable) {
		t.Errorf("AddRule error = %v, want ErrNotEditable", err)
	}
	if _, err := doc.DuplicateRule(0); !errors.Is(err, ErrNotEditable) {
		t.Errorf("DuplicateRule error = %v, want ErrNotEditable", err)
	}
	if _, _, err := doc.RemoveRule(0); !errors.Is(err, ErrNotEditable) {
		t.Errorf("RemoveRule error = %v, want ErrNotEditable", err)
	}
}

func TestOperationsRejectAnOutOfRangeRef(t *testing.T) {
	doc := parseForEdit(t, richSource)

	if _, err := doc.EditRule(99, nil, nil); !errors.Is(err, ErrNoSuchRule) {
		t.Errorf("EditRule error = %v, want ErrNoSuchRule", err)
	}
	if _, err := doc.DuplicateRule(-1); !errors.Is(err, ErrNoSuchRule) {
		t.Errorf("DuplicateRule error = %v, want ErrNoSuchRule", err)
	}
	if _, _, err := doc.RemoveRule(99); !errors.Is(err, ErrNoSuchRule) {
		t.Errorf("RemoveRule error = %v, want ErrNoSuchRule", err)
	}
}

func TestEditRuleRejectsAnUnsupportedAttributeName(t *testing.T) {
	doc := parseForEdit(t, richSource)

	_, err := doc.EditRule(0, nil, []AttrEdit{{Name: "weird_attr", Value: stringsToCty([]string{"x"})}})
	if !errors.Is(err, ErrUnsafeEdit) {
		t.Errorf("EditRule error = %v, want ErrUnsafeEdit", err)
	}
}

func TestEditRuleWritesEverySupportedAttribute(t *testing.T) {
	const src = `path "secret/data/a" {
  capabilities = ["read"]
}
`
	doc := parseForEdit(t, src)
	expires := time.Date(2027, 1, 2, 3, 4, 5, 0, time.UTC)

	got, err := doc.EditRule(0, nil, []AttrEdit{
		SetComment("a note"),
		SetExpiration(expires),
		SetRequiredParameters([]string{"reason"}),
		SetParameterMap("allowed_parameters", []policy.ParameterValues{{Name: "env", Values: []string{"dev"}}}),
		SetParameterMap("denied_parameters", []policy.ParameterValues{{Name: "admin"}}),
	})
	if err != nil {
		t.Fatalf("EditRule returned an error: %v", err)
	}

	after := commit(t, got)
	rule := after.Policy.Rules[0]
	if rule.Comment != "a note" {
		t.Errorf("comment = %q", rule.Comment)
	}
	if rule.Expiration == nil || !rule.Expiration.Equal(expires) {
		t.Errorf("expiration = %v", rule.Expiration)
	}
	if len(rule.RequiredParameters) != 1 || rule.RequiredParameters[0] != "reason" {
		t.Errorf("required_parameters = %v", rule.RequiredParameters)
	}
	if len(rule.AllowedParameters) != 1 || rule.AllowedParameters[0].Name != "env" {
		t.Errorf("allowed_parameters = %v", rule.AllowedParameters)
	}
	if len(rule.DeniedParameters) != 1 || len(rule.DeniedParameters[0].Values) != 0 {
		t.Errorf("denied_parameters = %v", rule.DeniedParameters)
	}
}

func TestEditRuleAppendsNewAttributesInACanonicalOrder(t *testing.T) {
	const src = `path "secret/data/a" {
  capabilities = ["read"]
}
`
	doc := parseForEdit(t, src)

	// Supplied deliberately out of order; the emitted order must not
	// depend on it.
	got, err := doc.EditRule(0, nil, []AttrEdit{
		SetRequiredParameters([]string{"reason"}),
		SetComment("a note"),
	})
	if err != nil {
		t.Fatalf("EditRule returned an error: %v", err)
	}

	out := string(got)
	commentAt := strings.Index(out, "comment")
	requiredAt := strings.Index(out, "required_parameters")
	if commentAt < 0 || requiredAt < 0 || commentAt > requiredAt {
		t.Errorf("attributes were not appended in canonical order:\n%s", out)
	}
}

func mustRuleSource(t *testing.T, doc *Document, ref RuleRef) RuleSource {
	t.Helper()
	src, err := doc.RuleSource(ref)
	if err != nil {
		t.Fatalf("RuleSource(%d) returned an error: %v", ref, err)
	}
	return src
}
