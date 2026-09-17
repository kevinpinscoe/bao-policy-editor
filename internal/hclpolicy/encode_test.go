package hclpolicy

import (
	"testing"
	"time"

	"github.com/kevinpinscoe/bao-policy-editor/internal/policy"
)

func examplePolicy() policy.Policy {
	exp := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	return policy.Policy{
		Rules: []policy.Rule{
			{
				Path:               "secret/data/team-a/*",
				Capabilities:       []policy.Capability{policy.CapabilityCreate, policy.CapabilityRead},
				Comment:            "Team A's secrets",
				Expiration:         &exp,
				RequiredParameters: []string{"owner"},
				AllowedParameters: []policy.ParameterValues{
					{Name: "env", Values: nil},
					{Name: "ttl", Values: []string{"1h", "24h"}},
				},
				DeniedParameters: []policy.ParameterValues{
					{Name: "root", Values: nil},
				},
			},
			{
				Path:         "secret/metadata/team-a/*",
				Capabilities: []policy.Capability{policy.CapabilityList},
			},
		},
	}
}

func TestEncode_Deterministic(t *testing.T) {
	p := examplePolicy()

	out1, err := Encode(p)
	if err != nil {
		t.Fatalf("Encode() error = %v", err)
	}
	out2, err := Encode(p)
	if err != nil {
		t.Fatalf("Encode() error = %v", err)
	}

	if string(out1) != string(out2) {
		t.Errorf("Encode() is not deterministic:\nrun 1:\n%s\nrun 2:\n%s", out1, out2)
	}
}

func TestEncode_DeterministicAcrossMapOrdering(t *testing.T) {
	// Build the same AllowedParameters content with the slice populated in
	// the opposite order, to prove determinism does not depend on the
	// order ParameterValues happened to be constructed in.
	p1 := policy.Policy{Rules: []policy.Rule{{
		Path: "secret/data/x",
		AllowedParameters: []policy.ParameterValues{
			{Name: "a", Values: []string{"1"}},
			{Name: "b", Values: []string{"2"}},
		},
	}}}
	p2 := policy.Policy{Rules: []policy.Rule{{
		Path: "secret/data/x",
		AllowedParameters: []policy.ParameterValues{
			{Name: "b", Values: []string{"2"}},
			{Name: "a", Values: []string{"1"}},
		},
	}}}

	out1, err := Encode(p1)
	if err != nil {
		t.Fatalf("Encode(p1) error = %v", err)
	}
	out2, err := Encode(p2)
	if err != nil {
		t.Fatalf("Encode(p2) error = %v", err)
	}
	if string(out1) != string(out2) {
		t.Errorf("Encode() output depends on ParameterValues slice order:\np1:\n%s\np2:\n%s", out1, out2)
	}
}

func TestEncode_DecodeRoundTrip(t *testing.T) {
	p := examplePolicy()

	out, err := Encode(p)
	if err != nil {
		t.Fatalf("Encode() error = %v", err)
	}

	doc, err := Parse("encoded.hcl", out)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if doc.HasErrors() {
		t.Fatalf("Parse(Encode(p)) produced errors: %v", doc.Diagnostics)
	}
	if doc.Unsupported {
		t.Error("Parse(Encode(p)) reported unsupported content for a policy that came entirely from the domain model")
	}

	if len(doc.Policy.Rules) != len(p.Rules) {
		t.Fatalf("round-tripped rule count = %d, want %d", len(doc.Policy.Rules), len(p.Rules))
	}
	r0 := doc.Policy.Rules[0]
	if r0.Path != p.Rules[0].Path {
		t.Errorf("round-tripped Path = %q, want %q", r0.Path, p.Rules[0].Path)
	}
	if !capsEqual(r0.Capabilities, p.Rules[0].Capabilities) {
		t.Errorf("round-tripped Capabilities = %v, want %v", r0.Capabilities, p.Rules[0].Capabilities)
	}
	if r0.Comment != p.Rules[0].Comment {
		t.Errorf("round-tripped Comment = %q, want %q", r0.Comment, p.Rules[0].Comment)
	}
	if r0.Expiration == nil || !r0.Expiration.Equal(*p.Rules[0].Expiration) {
		t.Errorf("round-tripped Expiration = %v, want %v", r0.Expiration, p.Rules[0].Expiration)
	}
}

func TestEncode_EmptyPolicy(t *testing.T) {
	out, err := Encode(policy.Policy{})
	if err != nil {
		t.Fatalf("Encode() error = %v", err)
	}
	if len(out) != 0 {
		t.Errorf("Encode(empty policy) = %q, want empty output", out)
	}
}
