package policy

import "testing"

func TestCapability_Known(t *testing.T) {
	cases := []struct {
		c    Capability
		want bool
	}{
		{CapabilityCreate, true},
		{CapabilityRead, true},
		{CapabilityUpdate, true},
		{CapabilityPatch, true},
		{CapabilityDelete, true},
		{CapabilityList, true},
		{CapabilityScan, true},
		{CapabilitySudo, true},
		{CapabilityDeny, true},
		{Capability("frobnicate"), false},
		{Capability(""), false},
	}
	for _, tc := range cases {
		if got := tc.c.Known(); got != tc.want {
			t.Errorf("Capability(%q).Known() = %v, want %v", tc.c, got, tc.want)
		}
	}
}

func TestRule_HasCapability(t *testing.T) {
	r := Rule{Capabilities: []Capability{CapabilityRead, CapabilityList}}

	if !r.HasCapability(CapabilityRead) {
		t.Error("HasCapability(read) = false, want true")
	}
	if !r.HasCapability(CapabilityList) {
		t.Error("HasCapability(list) = false, want true")
	}
	if r.HasCapability(CapabilityDelete) {
		t.Error("HasCapability(delete) = true, want false")
	}
}

func TestPolicy_ZeroValueIsUsable(t *testing.T) {
	var p Policy
	if len(p.Rules) != 0 {
		t.Errorf("zero-value Policy has %d rules, want 0", len(p.Rules))
	}
}
