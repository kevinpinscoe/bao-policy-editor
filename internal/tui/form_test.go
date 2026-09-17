package tui

import (
	"strings"
	"testing"

	"github.com/kevinpinscoe/bao-policy-editor/internal/policy"
)

// formFor builds the rule form for the first rule of a policy, the way the
// editor does.
func formFor(t *testing.T, src string) (*ruleForm, *Session) {
	t.Helper()
	s, _ := openTestSession(t, src)
	rule, err := s.Document().Rule(0)
	if err != nil {
		t.Fatalf("Rule(0) returned an error: %v", err)
	}
	ruleSrc, err := s.Document().RuleSource(0)
	if err != nil {
		t.Fatalf("RuleSource(0) returned an error: %v", err)
	}
	return newRuleForm(0, rule, ruleSrc), s
}

func TestFormWithNoChangesProducesNoEdits(t *testing.T) {
	f, _ := formFor(t, samplePolicy)

	newPath, edits, err := f.edits()
	if err != nil {
		t.Fatalf("edits returned an error: %v", err)
	}
	if newPath != nil {
		t.Errorf("an untouched form wants to rename the path to %q", *newPath)
	}
	if len(edits) != 0 {
		t.Errorf("an untouched form produced %d edits: %+v", len(edits), edits)
	}
}

func TestFormPreservesTheSourceCapabilityOrder(t *testing.T) {
	// The checkboxes are listed in canonical order; the file is not
	// obliged to use it. Ticking nothing must still produce no edit.
	f, _ := formFor(t, "path \"secret/data/a\" {\n  capabilities = [\"list\", \"read\"]\n}\n")

	if got := f.capabilitySet(); len(got) != 2 || got[0] != policy.CapabilityList || got[1] != policy.CapabilityRead {
		t.Errorf("capabilitySet() = %v, want the source's own order", got)
	}
	_, edits, err := f.edits()
	if err != nil {
		t.Fatal(err)
	}
	if len(edits) != 0 {
		t.Errorf("a form over out-of-order capabilities produced edits: %+v", edits)
	}
}

func TestFormKeepsCapabilitiesBPEDoesNotRecognize(t *testing.T) {
	f, _ := formFor(t, "path \"secret/data/a\" {\n  capabilities = [\"read\", \"teleport\"]\n}\n")

	if unknown := f.unknownCapabilities(); len(unknown) != 1 || string(unknown[0]) != "teleport" {
		t.Fatalf("unknownCapabilities() = %v", unknown)
	}

	// Ticking another capability must carry the unknown one through
	// rather than dropping it on the way to a rewritten list.
	f.caps[policy.CapabilityList] = true
	caps := f.capabilitySet()
	var found bool
	for _, c := range caps {
		if string(c) == "teleport" {
			found = true
		}
	}
	if !found {
		t.Errorf("capabilitySet() = %v, dropped the unrecognized capability", caps)
	}

	// And it says so on screen, so the user is not left guessing.
	if !strings.Contains(f.View(DefaultStyles(), 100), "teleport") {
		t.Error("the form does not mention the unrecognized capability")
	}
}

func TestFormDistinguishesAbsentEmptyAndPopulated(t *testing.T) {
	tests := []struct {
		name      string
		src       string
		wantSet   bool
		wantValue string
	}{
		{
			name:    "absent",
			src:     "path \"a\" {\n  capabilities = [\"read\"]\n}\n",
			wantSet: false,
		},
		{
			name:      "present but empty",
			src:       "path \"a\" {\n  capabilities = [\"read\"]\n  comment      = \"\"\n}\n",
			wantSet:   true,
			wantValue: "",
		},
		{
			name:      "populated",
			src:       "path \"a\" {\n  capabilities = [\"read\"]\n  comment      = \"hello\"\n}\n",
			wantSet:   true,
			wantValue: "hello",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f, _ := formFor(t, tc.src)

			if f.comment.set != tc.wantSet {
				t.Errorf("comment.set = %v, want %v", f.comment.set, tc.wantSet)
			}
			if f.comment.value != tc.wantValue {
				t.Errorf("comment.value = %q, want %q", f.comment.value, tc.wantValue)
			}

			// However it started, an untouched form emits no edit for it.
			_, edits, err := f.edits()
			if err != nil {
				t.Fatal(err)
			}
			for _, e := range edits {
				if e.Name == "comment" {
					t.Errorf("an untouched comment produced an edit: %+v", e)
				}
			}

			rendered := f.View(DefaultStyles(), 100)
			if !tc.wantSet && !strings.Contains(rendered, "(not set)") {
				t.Error("an absent comment is not shown as unset")
			}
			if tc.wantSet && tc.wantValue == "" && !strings.Contains(rendered, "set, but empty") {
				t.Error("an empty-but-present comment is not distinguished from an absent one")
			}
		})
	}
}

func TestUnsettingAnAttributeRemovesItRatherThanEmptyingIt(t *testing.T) {
	f, _ := formFor(t, "path \"a\" {\n  capabilities = [\"read\"]\n  comment      = \"hello\"\n}\n")

	// Move the cursor onto the comment row and unset it.
	f.cursor = indexOfRow(t, f, rowComment)
	f.unsetCurrent()

	_, edits, err := f.edits()
	if err != nil {
		t.Fatal(err)
	}
	if len(edits) != 1 || edits[0].Name != "comment" || !edits[0].Remove {
		t.Fatalf("edits = %+v, want a single removal of comment", edits)
	}
}

func TestSettingAnAbsentAttributeAddsIt(t *testing.T) {
	f, _ := formFor(t, "path \"a\" {\n  capabilities = [\"read\"]\n}\n")

	f.comment.set = true
	f.comment.value = "now documented"

	_, edits, err := f.edits()
	if err != nil {
		t.Fatal(err)
	}
	if len(edits) != 1 || edits[0].Name != "comment" || edits[0].Remove {
		t.Fatalf("edits = %+v, want a single comment assignment", edits)
	}
}

func TestFormShowsAdvancedContentUpFrontWhenTheRuleUsesIt(t *testing.T) {
	const src = `path "a" {
  capabilities        = ["read"]
  required_parameters = ["reason"]
  allowed_parameters = {
    env = ["dev", "staging"]
  }
}
`
	f, _ := formFor(t, src)

	if !f.advanced {
		t.Error("a rule with parameter constraints opened with them collapsed")
	}
	summary := f.advancedSummary()
	if !strings.Contains(summary, "1 required") || !strings.Contains(summary, "1 allowed") {
		t.Errorf("advancedSummary() = %q, want it to count what is there", summary)
	}

	// And the summary is visible even once collapsed, so nothing a rule
	// carries can be invisible.
	f.advanced = false
	rendered := f.View(DefaultStyles(), 100)
	if !strings.Contains(rendered, "1 required") {
		t.Errorf("the collapsed advanced row does not summarize its contents:\n%s", rendered)
	}
}

func TestFormLocksOnlyTheFieldItCannotRewrite(t *testing.T) {
	const src = `path "a" {
  capabilities = var.caps
  comment      = "editable"
}
`
	f, _ := formFor(t, src)

	capsRow := formRow{kind: rowCapability, capability: policy.CapabilityRead}
	if f.lockFor(capsRow) == "" {
		t.Error("an expression-valued capabilities attribute was not locked")
	}
	if lock := f.lockFor(formRow{kind: rowComment}); lock != "" {
		t.Errorf("comment was locked too: %s", lock)
	}
	if lock := f.lockFor(formRow{kind: rowPath}); lock != "" {
		t.Errorf("path was locked: %s", lock)
	}

	// Trying to toggle the locked field explains why rather than doing
	// nothing silently.
	f.cursor = indexOfRow(t, f, rowCapability)
	f.activate()
	if f.problem == "" {
		t.Error("toggling a locked capability gave no explanation")
	}
	if !strings.Contains(f.View(DefaultStyles(), 120), "read-only") {
		t.Error("the form does not mark the locked field read-only")
	}
}

func TestFormRefusesAnUnparseableExpiration(t *testing.T) {
	f, _ := formFor(t, "path \"a\" {\n  capabilities = [\"read\"]\n}\n")

	f.expiration.set = true
	f.expiration.value = "next tuesday"

	if problem := f.validate(); problem == "" {
		t.Error("a nonsense expiration passed validation")
	}
	if _, _, err := f.edits(); err == nil {
		t.Error("edits() accepted a nonsense expiration")
	}
}

func TestFormNoticesDenyCombinedWithOtherCapabilities(t *testing.T) {
	f, _ := formFor(t, "path \"a\" {\n  capabilities = [\"read\"]\n}\n")

	f.cursor = indexOfRowFor(t, f, policy.CapabilityDeny)
	f.activate()

	if f.notice == "" {
		t.Error("ticking deny alongside read produced no notice")
	}
	// It is advice, not a refusal: the edit still applies.
	if problem := f.validate(); problem != "" {
		t.Errorf("deny alongside read was refused: %s", problem)
	}
}

func TestParameterSubformRoundTripsThroughTheRuleForm(t *testing.T) {
	f, _ := formFor(t, "path \"a\" {\n  capabilities = [\"read\"]\n}\n")

	p := newParamForm(rowAllowed, "a", nil)
	p.values = []policy.ParameterValues{
		{Name: "env", Values: []string{"dev"}},
		{Name: "region"},
	}
	f.setParams(rowAllowed, p.values)

	_, edits, err := f.edits()
	if err != nil {
		t.Fatal(err)
	}
	if len(edits) != 1 || edits[0].Name != "allowed_parameters" {
		t.Fatalf("edits = %+v, want a single allowed_parameters assignment", edits)
	}

	// And the empty-values entry is explained rather than shown as an
	// empty pair of brackets.
	rendered := p.View(DefaultStyles(), 100)
	if !strings.Contains(rendered, "permits any value") {
		t.Errorf("the allowed-parameters subform does not explain an empty value list:\n%s", rendered)
	}

	denied := newParamForm(rowDenied, "a", nil)
	if !strings.Contains(denied.View(DefaultStyles(), 100), "denies the parameter entirely") {
		t.Error("the denied-parameters subform does not explain an empty value list")
	}
}

func TestParameterSubformRejectsDuplicateAndUnnamedEntries(t *testing.T) {
	p := newParamForm(rowAllowed, "a", []policy.ParameterValues{{Name: "env"}, {Name: "env"}})
	if result, _ := p.Update(pressKey("ctrl+s")); result != paramOngoing {
		t.Error("a duplicate parameter name was accepted")
	}
	if !strings.Contains(p.problem, "twice") {
		t.Errorf("problem = %q, want it to name the duplicate", p.problem)
	}

	p = newParamForm(rowAllowed, "a", []policy.ParameterValues{{Name: ""}})
	if result, _ := p.Update(pressKey("ctrl+s")); result != paramOngoing {
		t.Error("an unnamed parameter was accepted")
	}
}

func TestFormEditsSurviveARoundTripThroughTheDocument(t *testing.T) {
	const src = `# rule comment
path "secret/data/a" {
  # inner note
  capabilities = ["read"]
  weird_attr   = "kept"
}
`
	f, s := formFor(t, src)

	f.comment.set = true
	f.comment.value = "added by the form"
	f.caps[policy.CapabilityList] = true

	newPath, edits, err := f.edits()
	if err != nil {
		t.Fatal(err)
	}
	next, err := s.Document().EditRule(0, newPath, edits)
	if err != nil {
		t.Fatalf("EditRule returned an error: %v", err)
	}
	if err := s.Commit(next); err != nil {
		t.Fatal(err)
	}

	out := string(s.Current())
	for _, want := range []string{"# rule comment", "# inner note", `weird_attr   = "kept"`,
		`comment      = "added by the form"`, `["read", "list"]`} {
		if !strings.Contains(out, want) {
			t.Errorf("the round trip lost or failed to write %q:\n%s", want, out)
		}
	}
}

// indexOfRow finds the first row of a given kind, so a test can put the
// cursor on it without hard-coding a row number that moves whenever the
// form's layout changes.
func indexOfRow(t *testing.T, f *ruleForm, kind rowKind) int {
	t.Helper()
	for i, row := range f.rows() {
		if row.kind == kind {
			return i
		}
	}
	t.Fatalf("no row of kind %d in the form", kind)
	return 0
}

func indexOfRowFor(t *testing.T, f *ruleForm, c policy.Capability) int {
	t.Helper()
	for i, row := range f.rows() {
		if row.kind == rowCapability && row.capability == c {
			return i
		}
	}
	t.Fatalf("no row for capability %s", c)
	return 0
}
