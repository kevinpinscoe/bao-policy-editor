package hclpolicy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kevinpinscoe/bao-policy-editor/internal/policy"
)

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	src, err := os.ReadFile(filepath.Join("..", "..", "testdata", "policies", name))
	if err != nil {
		t.Fatalf("reading fixture %s: %v", name, err)
	}
	return src
}

func TestParse_ValidPolicy(t *testing.T) {
	src := readFixture(t, "valid.hcl")
	doc, err := Parse("valid.hcl", src)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if doc.HasErrors() {
		t.Fatalf("HasErrors() = true, diagnostics: %v", doc.Diagnostics)
	}
	if doc.Unsupported {
		t.Error("Unsupported = true, want false for a fully-supported fixture")
	}
	if doc.HasSyntaxError {
		t.Error("HasSyntaxError = true, want false for a fully-supported fixture")
	}

	if got, want := len(doc.Policy.Rules), 3; got != want {
		t.Fatalf("len(Rules) = %d, want %d", got, want)
	}

	r0 := doc.Policy.Rules[0]
	if r0.Path != "secret/data/team-a/*" {
		t.Errorf("Rules[0].Path = %q", r0.Path)
	}
	wantCaps := []policy.Capability{policy.CapabilityCreate, policy.CapabilityRead, policy.CapabilityUpdate}
	if !capsEqual(r0.Capabilities, wantCaps) {
		t.Errorf("Rules[0].Capabilities = %v, want %v", r0.Capabilities, wantCaps)
	}
	if r0.Comment != "Team A's own KV v2 secrets" {
		t.Errorf("Rules[0].Comment = %q", r0.Comment)
	}
	if len(r0.RequiredParameters) != 1 || r0.RequiredParameters[0] != "owner" {
		t.Errorf("Rules[0].RequiredParameters = %v", r0.RequiredParameters)
	}
	// 3 entries as of FSM-12: "owner" = [] was added so the required
	// "owner" parameter is actually satisfiable (FSM-11's fixture required
	// it without ever allowing it — a contradiction Validate now catches;
	// see TestValidate_Fixture_ContradictoryRequiredParameter).
	if len(r0.AllowedParameters) != 3 {
		t.Fatalf("Rules[0].AllowedParameters = %v, want 3 entries", r0.AllowedParameters)
	}
	if owner, ok := lookupParameter(r0.AllowedParameters, "owner"); !ok || len(owner.Values) != 0 {
		t.Errorf(`Rules[0].AllowedParameters["owner"] = %v, ok=%v, want an empty-values entry permitting any value`, owner, ok)
	}
	if len(r0.DeniedParameters) != 1 || r0.DeniedParameters[0].Name != "root" {
		t.Errorf("Rules[0].DeniedParameters = %v", r0.DeniedParameters)
	}

	r2 := doc.Policy.Rules[2]
	if r2.Expiration == nil {
		t.Fatal("Rules[2].Expiration = nil, want a parsed timestamp")
	}
	want, _ := time.Parse(time.RFC3339, "2030-01-01T00:00:00Z")
	if !r2.Expiration.Equal(want) {
		t.Errorf("Rules[2].Expiration = %v, want %v", r2.Expiration, want)
	}
}

func TestParse_InvalidSyntax(t *testing.T) {
	src := readFixture(t, "invalid_syntax.hcl")
	doc, err := Parse("invalid_syntax.hcl", src)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if !doc.HasErrors() {
		t.Fatal("HasErrors() = false, want true for malformed HCL")
	}
	if !doc.HasSyntaxError {
		t.Error("HasSyntaxError = false, want true for HCL that fails to parse at all")
	}

	found := false
	for _, d := range doc.Diagnostics {
		if d.Filename != "invalid_syntax.hcl" {
			t.Errorf("Diagnostic.Filename = %q, want %q", d.Filename, "invalid_syntax.hcl")
		}
		if d.Line > 0 {
			found = true
		}
	}
	if !found {
		t.Error("no diagnostic carried a source line — positions must be reported")
	}
}

func TestParse_InvalidExpiration(t *testing.T) {
	src := readFixture(t, "invalid_expiration.hcl")
	doc, err := Parse("invalid_expiration.hcl", src)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if !doc.HasErrors() {
		t.Fatal("HasErrors() = false, want true for an invalid expiration timestamp")
	}
	if doc.HasSyntaxError {
		t.Error("HasSyntaxError = true, want false — the HCL itself parses fine; only the decoded value is invalid")
	}
	if doc.Policy.Rules[0].Expiration != nil {
		t.Error("Expiration was set despite being unparseable")
	}
}

func TestParse_CommentsPreservedInBytes(t *testing.T) {
	src := readFixture(t, "comments.hcl")
	doc, err := Parse("comments.hcl", src)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if doc.HasErrors() {
		t.Fatalf("HasErrors() = true: %v", doc.Diagnostics)
	}
	if string(doc.Bytes()) != string(src) {
		t.Errorf("Bytes() round trip did not reproduce the source exactly.\ngot:\n%s\nwant:\n%s", doc.Bytes(), src)
	}
}

func TestParse_UnsupportedBlock(t *testing.T) {
	src := readFixture(t, "unsupported_block.hcl")
	doc, err := Parse("unsupported_block.hcl", src)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if doc.HasErrors() {
		t.Fatalf("HasErrors() = true, want false (unsupported content is a warning, not an error): %v", doc.Diagnostics)
	}
	if doc.HasSyntaxError {
		t.Error("HasSyntaxError = true, want false — unsupported content is syntactically valid HCL")
	}
	if !doc.Unsupported {
		t.Error("Unsupported = false, want true — the file has a non-path top-level block")
	}
	if len(doc.Policy.Rules) != 1 {
		t.Fatalf("len(Rules) = %d, want 1 (the one path block that IS supported)", len(doc.Policy.Rules))
	}
	// The unsupported block must still be present in the byte-safe round trip.
	if !strings.Contains(string(doc.Bytes()), "unknown_block") {
		t.Error("Bytes() lost the unsupported block — content was silently dropped")
	}
}

func TestParse_UnsupportedAttribute(t *testing.T) {
	src := readFixture(t, "unsupported_attribute.hcl")
	doc, err := Parse("unsupported_attribute.hcl", src)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if !doc.Unsupported {
		t.Error("Unsupported = false, want true — the path block has an unrecognized attribute")
	}
	if doc.HasSyntaxError {
		t.Error("HasSyntaxError = true, want false — an unsupported attribute is syntactically valid HCL")
	}
	if !strings.Contains(string(doc.Bytes()), "future_option") {
		t.Error("Bytes() lost the unsupported attribute — content was silently dropped")
	}
	// The known attribute must still have decoded correctly alongside it.
	if !doc.Policy.Rules[0].HasCapability(policy.CapabilityRead) {
		t.Error("known attribute (capabilities) failed to decode alongside an unsupported one")
	}
}

func TestParse_MultipleFixtures_NeverPanics(t *testing.T) {
	entries, err := os.ReadDir(filepath.Join("..", "..", "testdata", "policies"))
	if err != nil {
		t.Fatalf("reading fixtures dir: %v", err)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		t.Run(e.Name(), func(t *testing.T) {
			src := readFixture(t, e.Name())
			doc, err := Parse(e.Name(), src)
			if err != nil {
				t.Fatalf("Parse() error = %v", err)
			}
			_ = doc.HasErrors()
			_ = doc.Bytes()
		})
	}
}

func capsEqual(a, b []policy.Capability) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
