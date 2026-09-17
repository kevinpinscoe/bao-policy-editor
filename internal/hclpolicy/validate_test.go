package hclpolicy

import (
	"strings"
	"testing"
	"time"

	"github.com/kevinpinscoe/bao-policy-editor/internal/policy"
)

// findDiagnostic returns the first diagnostic whose Summary contains
// substr, and whether one was found.
func findDiagnostic(diags []Diagnostic, substr string) (Diagnostic, bool) {
	for _, d := range diags {
		if strings.Contains(d.Summary, substr) {
			return d, true
		}
	}
	return Diagnostic{}, false
}

func TestValidate_MissingOrEmptyPath(t *testing.T) {
	pol := policy.Policy{Rules: []policy.Rule{
		{Path: "", Capabilities: []policy.Capability{policy.CapabilityRead}},
	}}
	diags := Validate(pol)

	d, found := findDiagnostic(diags, "empty path pattern")
	if !found {
		t.Fatalf("no empty-path diagnostic found in %v", diags)
	}
	if d.Severity != SeverityError {
		t.Errorf("Severity = %v, want SeverityError — an empty path can never usefully match a request", d.Severity)
	}
}

func TestValidate_RuleWithNoCapabilities(t *testing.T) {
	pol := policy.Policy{Rules: []policy.Rule{
		{Path: "secret/data/foo"},
	}}
	diags := Validate(pol)

	d, found := findDiagnostic(diags, "has no capabilities")
	if !found {
		t.Fatalf("no no-capabilities diagnostic found in %v", diags)
	}
	if d.Severity != SeverityWarning {
		t.Errorf("Severity = %v, want SeverityWarning", d.Severity)
	}
	if d.Path != "secret/data/foo" {
		t.Errorf("Path = %q, want the rule's path", d.Path)
	}
}

func TestValidate_UnknownCapability_IsError(t *testing.T) {
	// OpenBao's own ACL policy parser rejects an unrecognized capability
	// with a hard error at parse time (internal/vault/policy/policy.go,
	// parsePath's default case) rather than ignoring it — verified against
	// openbao/openbao's source, 2026-09-17 — so a policy containing one
	// cannot be applied as written. This must be an error, not a warning.
	pol := policy.Policy{Rules: []policy.Rule{
		{Path: "secret/data/foo", Capabilities: []policy.Capability{"frobnicate"}},
	}}
	diags := Validate(pol)

	d, found := findDiagnostic(diags, "unknown capability")
	if !found {
		t.Fatalf("no unknown-capability diagnostic found in %v", diags)
	}
	if d.Severity != SeverityError {
		t.Errorf("Severity = %v, want SeverityError (OpenBao rejects the whole policy, it does not merely ignore the capability)", d.Severity)
	}
}

func TestValidate_KnownCapability_NoDiagnostic(t *testing.T) {
	for _, c := range policy.Capabilities {
		pol := policy.Policy{Rules: []policy.Rule{
			{Path: "secret/data/foo", Capabilities: []policy.Capability{c}},
		}}
		diags := Validate(pol)
		if _, found := findDiagnostic(diags, "unknown capability"); found {
			t.Errorf("capability %q was flagged as unknown, but it is one of policy.Capabilities", c)
		}
	}
}

func TestValidate_DenyCombinedWithOtherCapabilities(t *testing.T) {
	// OpenBao's parser overwrites the capabilities list to exactly
	// ["deny"] the moment deny appears, and stops processing the rest —
	// the other capabilities never take effect, they are not merely
	// lower priority. Still a warning (the policy is applicable, just
	// probably not what the author intended), not an error.
	pol := policy.Policy{Rules: []policy.Rule{
		{Path: "secret/data/foo", Capabilities: []policy.Capability{policy.CapabilityDeny, policy.CapabilityRead}},
	}}
	diags := Validate(pol)

	d, found := findDiagnostic(diags, "combines deny with other capabilities")
	if !found {
		t.Fatalf("no deny-combination diagnostic found in %v", diags)
	}
	if d.Severity != SeverityWarning {
		t.Errorf("Severity = %v, want SeverityWarning", d.Severity)
	}
}

func TestValidate_DenyAlone_NoDiagnostic(t *testing.T) {
	pol := policy.Policy{Rules: []policy.Rule{
		{Path: "secret/data/foo", Capabilities: []policy.Capability{policy.CapabilityDeny}},
	}}
	diags := Validate(pol)
	if _, found := findDiagnostic(diags, "combines deny"); found {
		t.Error("a rule with only deny was flagged as combining deny with other capabilities")
	}
}

func TestValidate_DuplicatePathBlocks(t *testing.T) {
	pol := policy.Policy{Rules: []policy.Rule{
		{Path: "secret/data/foo", Capabilities: []policy.Capability{policy.CapabilityRead}},
		{Path: "secret/data/bar", Capabilities: []policy.Capability{policy.CapabilityRead}},
		{Path: "secret/data/foo", Capabilities: []policy.Capability{policy.CapabilityUpdate}},
	}}
	diags := Validate(pol)

	count := 0
	for _, d := range diags {
		if strings.Contains(d.Summary, "defined more than once") {
			count++
		}
	}
	if count != 1 {
		t.Errorf("got %d duplicate-path diagnostics, want exactly 1 (the second occurrence only)", count)
	}
}

func TestValidate_WildcardPlacement(t *testing.T) {
	tests := []struct {
		name string
		path string
		want bool
	}{
		{"trailing wildcard is fine", "secret/data/team-a/*", false},
		{"leading wildcard is suspicious", "*/data/foo", true},
		{"mid-pattern wildcard is suspicious", "secret/*/foo", true},
		{"no wildcard at all", "secret/data/foo", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pol := policy.Policy{Rules: []policy.Rule{
				{Path: tt.path, Capabilities: []policy.Capability{policy.CapabilityRead}},
			}}
			diags := Validate(pol)
			_, found := findDiagnostic(diags, "before the end of the pattern")
			if found != tt.want {
				t.Errorf("path %q: wildcard-placement diagnostic found = %v, want %v", tt.path, found, tt.want)
			}
		})
	}
}

func TestValidate_BroadSysAccess(t *testing.T) {
	pol := policy.Policy{Rules: []policy.Rule{
		{Path: "sys/*", Capabilities: []policy.Capability{policy.CapabilityRead}},
	}}
	diags := Validate(pol)
	d, found := findDiagnostic(diags, "broad access under sys/")
	if !found {
		t.Fatalf("no broad-sys diagnostic found in %v", diags)
	}
	if d.Severity != SeverityWarning {
		t.Errorf("Severity = %v, want SeverityWarning", d.Severity)
	}
}

func TestValidate_NarrowSysAccess_NoDiagnostic(t *testing.T) {
	pol := policy.Policy{Rules: []policy.Rule{
		{Path: "sys/health", Capabilities: []policy.Capability{policy.CapabilityRead}},
	}}
	diags := Validate(pol)
	if _, found := findDiagnostic(diags, "broad access under sys/"); found {
		t.Error("a narrow sys/ path was flagged as broad")
	}
}

func TestValidate_SudoUse(t *testing.T) {
	pol := policy.Policy{Rules: []policy.Rule{
		{Path: "secret/data/foo", Capabilities: []policy.Capability{policy.CapabilitySudo, policy.CapabilityRead}},
	}}
	diags := Validate(pol)
	if _, found := findDiagnostic(diags, "grants sudo"); !found {
		t.Fatalf("no sudo diagnostic found in %v", diags)
	}
}

func TestValidate_CreateWithoutUpdate(t *testing.T) {
	pol := policy.Policy{Rules: []policy.Rule{
		{Path: "secret/data/foo", Capabilities: []policy.Capability{policy.CapabilityCreate}},
	}}
	diags := Validate(pol)
	if _, found := findDiagnostic(diags, "create without update"); !found {
		t.Fatalf("no create-without-update diagnostic found in %v", diags)
	}
}

func TestValidate_CreateWithUpdate_NoDiagnostic(t *testing.T) {
	pol := policy.Policy{Rules: []policy.Rule{
		{Path: "secret/data/foo", Capabilities: []policy.Capability{policy.CapabilityCreate, policy.CapabilityUpdate}},
	}}
	diags := Validate(pol)
	if _, found := findDiagnostic(diags, "create without update"); found {
		t.Error("create+update was flagged as create-without-update")
	}
}

func TestValidate_ListScanOnNonPrefixPath(t *testing.T) {
	// Explicitly heuristic — build brief: "list or scan on a path that is
	// unlikely to represent a prefix" — must always be a warning, never an
	// error, since it is a guess based on path shape alone.
	pol := policy.Policy{Rules: []policy.Rule{
		{Path: "secret/data/foo", Capabilities: []policy.Capability{policy.CapabilityList}},
	}}
	diags := Validate(pol)
	d, found := findDiagnostic(diags, "does not look like a path prefix")
	if !found {
		t.Fatalf("no list/scan-prefix diagnostic found in %v", diags)
	}
	if d.Severity != SeverityWarning {
		t.Errorf("Severity = %v, want SeverityWarning — this check is a heuristic and must never block saving", d.Severity)
	}
}

func TestValidate_ListOnPrefixPath_NoDiagnostic(t *testing.T) {
	for _, path := range []string{"secret/data/", "secret/data/*"} {
		pol := policy.Policy{Rules: []policy.Rule{
			{Path: path, Capabilities: []policy.Capability{policy.CapabilityList}},
		}}
		diags := Validate(pol)
		if _, found := findDiagnostic(diags, "does not look like a path prefix"); found {
			t.Errorf("path %q looks like a prefix but was flagged", path)
		}
	}
}

func TestValidate_ExpirationInThePast(t *testing.T) {
	past := time.Now().Add(-24 * time.Hour)
	pol := policy.Policy{Rules: []policy.Rule{
		{Path: "secret/data/foo", Capabilities: []policy.Capability{policy.CapabilityRead}, Expiration: &past},
	}}
	diags := Validate(pol)
	d, found := findDiagnostic(diags, "is in the past")
	if !found {
		t.Fatalf("no expired diagnostic found in %v", diags)
	}
	if d.Severity != SeverityWarning {
		t.Errorf("Severity = %v, want SeverityWarning — an expired rule may be intentional", d.Severity)
	}
}

func TestValidate_ExpirationInTheFuture_NoDiagnostic(t *testing.T) {
	future := time.Now().Add(24 * time.Hour)
	pol := policy.Policy{Rules: []policy.Rule{
		{Path: "secret/data/foo", Capabilities: []policy.Capability{policy.CapabilityRead}, Expiration: &future},
	}}
	diags := Validate(pol)
	if _, found := findDiagnostic(diags, "is in the past"); found {
		t.Error("a future expiration was flagged as being in the past")
	}
}

func TestValidate_EmptyPolicy(t *testing.T) {
	diags := Validate(policy.Policy{})
	d, found := findDiagnostic(diags, "no path rules")
	if !found {
		t.Fatalf("no empty-policy diagnostic found in %v", diags)
	}
	if d.Severity != SeverityWarning {
		t.Errorf("Severity = %v, want SeverityWarning — an empty (deny-by-default) policy may be intentional", d.Severity)
	}
}

// --- Contradictory parameter constraints: value-aware, per Kevin's
// 2026-09-17 correction. Only a *provable* impossibility is an error;
// a merely partial overlap must remain satisfiable and silent.

func TestValidate_RequiredParam_DeniedWithEmptyList_IsError(t *testing.T) {
	pol := policy.Policy{Rules: []policy.Rule{{
		Path:               "secret/data/foo",
		Capabilities:       []policy.Capability{policy.CapabilityCreate},
		RequiredParameters: []string{"ttl"},
		DeniedParameters:   []policy.ParameterValues{{Name: "ttl", Values: nil}},
	}}}
	diags := Validate(pol)
	d, found := findDiagnostic(diags, `requires parameter "ttl" that can never be supplied`)
	if !found {
		t.Fatalf("no contradiction diagnostic found in %v", diags)
	}
	if d.Severity != SeverityError {
		t.Errorf("Severity = %v, want SeverityError — denied_parameters with no values denies every value", d.Severity)
	}
}

func TestValidate_RequiredParam_WildcardDeniedWithEmptyList_IsError(t *testing.T) {
	pol := policy.Policy{Rules: []policy.Rule{{
		Path:               "secret/data/foo",
		Capabilities:       []policy.Capability{policy.CapabilityCreate},
		RequiredParameters: []string{"ttl"},
		DeniedParameters:   []policy.ParameterValues{{Name: "*", Values: nil}},
	}}}
	diags := Validate(pol)
	if _, found := findDiagnostic(diags, `requires parameter "ttl" that can never be supplied`); !found {
		t.Fatalf("no contradiction diagnostic found in %v", diags)
	}
}

func TestValidate_RequiredParam_AbsentFromAllowedWhitelist_IsError(t *testing.T) {
	pol := policy.Policy{Rules: []policy.Rule{{
		Path:               "secret/data/foo",
		Capabilities:       []policy.Capability{policy.CapabilityCreate},
		RequiredParameters: []string{"ttl"},
		AllowedParameters:  []policy.ParameterValues{{Name: "env", Values: nil}},
	}}}
	diags := Validate(pol)
	if _, found := findDiagnostic(diags, `requires parameter "ttl" that can never be supplied`); !found {
		t.Fatalf("no contradiction diagnostic found in %v", diags)
	}
}

func TestValidate_RequiredParam_AbsentFromAllowedButWildcardPresent_Satisfiable(t *testing.T) {
	pol := policy.Policy{Rules: []policy.Rule{{
		Path:               "secret/data/foo",
		Capabilities:       []policy.Capability{policy.CapabilityCreate},
		RequiredParameters: []string{"ttl"},
		AllowedParameters:  []policy.ParameterValues{{Name: "*", Values: nil}},
	}}}
	diags := Validate(pol)
	if _, found := findDiagnostic(diags, "can never be supplied"); found {
		t.Error("a \"*\" entry in allowed_parameters should let the required parameter through, but a contradiction was reported")
	}
}

func TestValidate_AllowedValuesEntirelyDenied_IsError(t *testing.T) {
	pol := policy.Policy{Rules: []policy.Rule{{
		Path:               "secret/data/foo",
		Capabilities:       []policy.Capability{policy.CapabilityCreate},
		RequiredParameters: []string{"env"},
		AllowedParameters:  []policy.ParameterValues{{Name: "env", Values: []string{"dev", "staging"}}},
		DeniedParameters:   []policy.ParameterValues{{Name: "env", Values: []string{"dev", "staging"}}},
	}}}
	diags := Validate(pol)
	d, found := findDiagnostic(diags, `requires parameter "env" that can never be supplied`)
	if !found {
		t.Fatalf("no contradiction diagnostic found in %v", diags)
	}
	if d.Severity != SeverityError {
		t.Errorf("Severity = %v, want SeverityError — every allowed value is also denied", d.Severity)
	}
}

// TestValidate_PartialDenial_RemainsSatisfiable is the case Kevin
// specifically asked to see proven safe: a required parameter with only
// some of its allowed values denied is NOT a contradiction, because a
// populated denied_parameters list rejects only the listed values —
// https://openbao.org/docs/concepts/policies/#parameter-constraints —
// and at least one allowed value remains valid.
func TestValidate_PartialDenial_RemainsSatisfiable(t *testing.T) {
	pol := policy.Policy{Rules: []policy.Rule{{
		Path:               "secret/data/foo",
		Capabilities:       []policy.Capability{policy.CapabilityCreate},
		RequiredParameters: []string{"env"},
		AllowedParameters:  []policy.ParameterValues{{Name: "env", Values: []string{"dev", "staging", "prod"}}},
		DeniedParameters:   []policy.ParameterValues{{Name: "env", Values: []string{"prod"}}},
	}}}
	diags := Validate(pol)
	if _, found := findDiagnostic(diags, "can never be supplied"); found {
		t.Error("dev/staging remain valid after prod is denied, but a contradiction was reported")
	}
}

// TestValidate_DeniedSpecificValue_NoAllowedRestriction_RemainsSatisfiable
// covers a required parameter with only a specific denied value and no
// allowed_parameters restriction at all — infinitely many other values
// remain valid, so this must not be reported as a contradiction.
func TestValidate_DeniedSpecificValue_NoAllowedRestriction_RemainsSatisfiable(t *testing.T) {
	pol := policy.Policy{Rules: []policy.Rule{{
		Path:               "secret/data/foo",
		Capabilities:       []policy.Capability{policy.CapabilityCreate},
		RequiredParameters: []string{"env"},
		DeniedParameters:   []policy.ParameterValues{{Name: "env", Values: []string{"prod"}}},
	}}}
	diags := Validate(pol)
	if _, found := findDiagnostic(diags, "can never be supplied"); found {
		t.Error("denying only one specific value with no allowed-list restriction leaves other values valid, but a contradiction was reported")
	}
}

// --- KV v2 same-mount-evidence heuristic (Kevin's 2026-09-17 correction):
// secret/ is never assumed to be KV v2 on its own; the warning only fires
// when another rule under the same mount already uses a KV v2 API
// component.

func TestValidate_KV2Evidence_MissingComponent_Warns(t *testing.T) {
	pol := policy.Policy{Rules: []policy.Rule{
		{Path: "secret/data/team-a/*", Capabilities: []policy.Capability{policy.CapabilityRead}},
		{Path: "secret/team-a-notes", Capabilities: []policy.Capability{policy.CapabilityRead}},
	}}
	diags := Validate(pol)
	d, found := findDiagnostic(diags, "may be missing a KV v2 API component")
	if !found {
		t.Fatalf("no KV v2 diagnostic found in %v", diags)
	}
	if d.Path != "secret/team-a-notes" {
		t.Errorf("Path = %q, want the rule missing the component", d.Path)
	}
	if d.Severity != SeverityWarning {
		t.Errorf("Severity = %v, want SeverityWarning — this is a same-policy heuristic, not confirmed mount configuration, and must never block saving", d.Severity)
	}
}

func TestValidate_NoKV2Evidence_NoWarning(t *testing.T) {
	// secret/ can be KV v1 or any other engine — with nothing else in the
	// policy addressing that mount via data/metadata/etc., there is no
	// basis to guess it is KV v2.
	pol := policy.Policy{Rules: []policy.Rule{
		{Path: "secret/foo", Capabilities: []policy.Capability{policy.CapabilityRead}},
	}}
	diags := Validate(pol)
	if _, found := findDiagnostic(diags, "KV v2"); found {
		t.Error("an ordinary secret/foo path with no KV v2 evidence anywhere in the policy was flagged")
	}
}

func TestValidate_CorrectKV2Path_NoWarning(t *testing.T) {
	pol := policy.Policy{Rules: []policy.Rule{
		{Path: "secret/data/foo", Capabilities: []policy.Capability{policy.CapabilityRead}},
		{Path: "secret/metadata/foo", Capabilities: []policy.Capability{policy.CapabilityList}},
	}}
	diags := Validate(pol)
	if _, found := findDiagnostic(diags, "KV v2"); found {
		t.Error("correctly-formed KV v2 paths were flagged")
	}
}

// --- Document.Validate stamps the filename.

func TestDocumentValidate_StampsFilename(t *testing.T) {
	src := []byte("path \"secret/data/foo\" {\n  capabilities = [\"sudo\"]\n}\n")
	doc, err := Parse("policy.hcl", src)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	diags := doc.Validate()
	if len(diags) == 0 {
		t.Fatal("want at least the sudo warning")
	}
	for _, d := range diags {
		if d.Filename != "policy.hcl" {
			t.Errorf("Filename = %q, want %q", d.Filename, "policy.hcl")
		}
	}
}

// --- Diagnostic.String() formatting.

func TestDiagnostic_String_OmitsPositionWhenZero(t *testing.T) {
	d := Diagnostic{Severity: SeverityWarning, Summary: "example", Filename: "policy.hcl"}
	got := d.String()
	if strings.Contains(got, ":0:0:") {
		t.Errorf("String() = %q, did not want a misleading :0:0: position", got)
	}
	want := "policy.hcl: warning: example"
	if got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
}

func TestDiagnostic_String_KeepsPositionWhenPresent(t *testing.T) {
	d := Diagnostic{Severity: SeverityError, Summary: "example", Filename: "policy.hcl", Line: 3, Column: 5}
	got := d.String()
	want := "policy.hcl:3:5: error: example"
	if got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
}
