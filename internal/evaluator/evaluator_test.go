package evaluator

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/kevinpinscoe/bao-policy-editor/internal/policy"
)

// fixedNow is the evaluation time every test uses — Compile takes an
// explicit time.Time rather than reading time.Now() internally (Kevin's
// instruction, 2026-09-17: deterministic tests need a fixed time), so
// every test here is reproducible regardless of when it actually runs.
var fixedNow = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func rule(path string, caps ...policy.Capability) policy.Rule {
	return policy.Rule{Path: path, Capabilities: caps}
}

func compile(t *testing.T, named ...NamedPolicy) *Evaluator {
	t.Helper()
	ev, err := Compile(named, fixedNow)
	if err != nil {
		t.Fatalf("Compile() error = %v", err)
	}
	return ev
}

func named(name string, rules ...policy.Rule) NamedPolicy {
	return NamedPolicy{Source: Source{Name: name}, Policy: policy.Policy{Rules: rules}}
}

// --- Exact, prefix, and segment-wildcard matching.

func TestEvaluate_ExactMatch(t *testing.T) {
	ev := compile(t, named("p.hcl", rule("secret/data/foo", policy.CapabilityRead)))

	d, err := ev.Evaluate("secret/data/foo", policy.CapabilityRead)
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}
	if !d.Allowed || d.Stage != StageExact || d.WinningPattern != "secret/data/foo" {
		t.Errorf("got %+v, want an allowed exact match", d)
	}
}

func TestEvaluate_ExactMismatch_DoesNotFallBackToPrefix(t *testing.T) {
	// A capability the exact rule does not grant is denied outright, even
	// though a broader prefix rule elsewhere would have granted it — an
	// exact match always wins the lookup, so the prefix rule is never
	// even consulted.
	ev := compile(t,
		named("p.hcl",
			rule("secret/data/foo", policy.CapabilityRead),
			rule("secret/*", policy.CapabilityRead, policy.CapabilityUpdate),
		),
	)

	d, err := ev.Evaluate("secret/data/foo", policy.CapabilityUpdate)
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}
	if d.Allowed {
		t.Errorf("got Allowed = true, want false — the exact rule does not grant update, and must not fall back to the prefix rule")
	}
	if d.Stage != StageExact {
		t.Errorf("Stage = %v, want StageExact", d.Stage)
	}
}

func TestEvaluate_PrefixMatch(t *testing.T) {
	ev := compile(t, named("p.hcl", rule("secret/data/*", policy.CapabilityRead)))

	d, err := ev.Evaluate("secret/data/foo/bar", policy.CapabilityRead)
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}
	if !d.Allowed || d.Stage != StagePrefixOrWildcard {
		t.Errorf("got %+v, want an allowed prefix match", d)
	}
}

func TestEvaluate_SegmentWildcardMatch(t *testing.T) {
	ev := compile(t, named("p.hcl", rule("secret/+/config", policy.CapabilityRead)))

	d, err := ev.Evaluate("secret/team-a/config", policy.CapabilityRead)
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}
	if !d.Allowed || d.Stage != StagePrefixOrWildcard || d.WinningPattern != "secret/+/config" {
		t.Errorf("got %+v, want an allowed segment-wildcard match", d)
	}

	// A "+" matches exactly one segment — it must not match across a "/".
	d, err = ev.Evaluate("secret/team-a/nested/config", policy.CapabilityRead)
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}
	if d.Allowed {
		t.Error("\"+\" matched more than one path segment")
	}
}

// --- Default deny and explicit deny.

func TestEvaluate_DefaultDeny_NoMatch(t *testing.T) {
	ev := compile(t, named("p.hcl", rule("secret/data/foo", policy.CapabilityRead)))

	d, err := ev.Evaluate("secret/data/somewhere-else", policy.CapabilityRead)
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}
	if d.Allowed || d.Denied || d.Stage != StageDefaultDeny {
		t.Errorf("got %+v, want default deny (Allowed=false, Denied=false, StageDefaultDeny)", d)
	}
}

func TestEvaluate_ExplicitDeny_WithinOneRule(t *testing.T) {
	// deny combined with other capabilities in one rule: FSM-12 already
	// established (and this evaluator independently confirms) that
	// OpenBao collapses the rule to deny-only.
	ev := compile(t, named("p.hcl", rule("secret/data/foo", policy.CapabilityDeny, policy.CapabilityRead)))

	d, err := ev.Evaluate("secret/data/foo", policy.CapabilityRead)
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}
	if d.Allowed || !d.Denied {
		t.Errorf("got %+v, want Denied = true", d)
	}
	if len(d.DeniedBy) != 1 || d.DeniedBy[0].Name != "p.hcl" {
		t.Errorf("DeniedBy = %v, want [p.hcl]", d.DeniedBy)
	}
}

func TestEvaluate_ExplicitDeny_AcrossPolicies_DenyFirst(t *testing.T) {
	ev := compile(t,
		named("deny.hcl", rule("secret/data/foo", policy.CapabilityDeny)),
		named("allow.hcl", rule("secret/data/foo", policy.CapabilityRead)),
	)

	d, err := ev.Evaluate("secret/data/foo", policy.CapabilityRead)
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}
	if !d.Denied {
		t.Errorf("got %+v, want Denied = true regardless of merge order", d)
	}
}

func TestEvaluate_ExplicitDeny_AcrossPolicies_AllowFirst(t *testing.T) {
	// Same two policies, opposite compile order — the result must be
	// identical (deny wins either way), proving the merge is genuinely
	// order-independent, not an artifact of processing sequence.
	ev := compile(t,
		named("allow.hcl", rule("secret/data/foo", policy.CapabilityRead)),
		named("deny.hcl", rule("secret/data/foo", policy.CapabilityDeny)),
	)

	d, err := ev.Evaluate("secret/data/foo", policy.CapabilityRead)
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}
	if !d.Denied {
		t.Errorf("got %+v, want Denied = true regardless of merge order", d)
	}
}

// TestEvaluate_BroaderDenyDoesNotOverrideMoreSpecificAllow is the
// regression Kevin specifically asked for, 2026-09-17: a deny at a
// BROADER, losing pattern must never override a more specific WINNING
// allow. Deny only ever wins by being part of the actual winning rule's
// own merge state — Evaluate must never globally scan every
// structurally-matching rule looking for a deny to apply on top of the
// real decision.
func TestEvaluate_BroaderDenyDoesNotOverrideMoreSpecificAllow(t *testing.T) {
	t.Run("exact allow beats broader prefix deny", func(t *testing.T) {
		ev := compile(t,
			named("p.hcl",
				rule("secret/*", policy.CapabilityDeny),
				rule("secret/data/foo", policy.CapabilityRead),
			),
		)
		d, err := ev.Evaluate("secret/data/foo", policy.CapabilityRead)
		if err != nil {
			t.Fatalf("Evaluate() error = %v", err)
		}
		if !d.Allowed || d.Denied {
			t.Errorf("got %+v, want the exact allow to win outright over the broader prefix deny", d)
		}
		if d.Stage != StageExact {
			t.Errorf("Stage = %v, want StageExact", d.Stage)
		}
	})

	t.Run("longer prefix allow beats shorter prefix deny", func(t *testing.T) {
		ev := compile(t,
			named("p.hcl",
				rule("secret/*", policy.CapabilityDeny),
				rule("secret/data/*", policy.CapabilityRead),
			),
		)
		d, err := ev.Evaluate("secret/data/foo", policy.CapabilityRead)
		if err != nil {
			t.Fatalf("Evaluate() error = %v", err)
		}
		if !d.Allowed || d.Denied {
			t.Errorf("got %+v, want the longer, more specific prefix to win over the shorter prefix deny", d)
		}
		// The shorter deny prefix must show up as a losing candidate, not
		// as a silent override.
		found := false
		for _, l := range d.LosingCandidates {
			if l.Pattern == "secret/*" {
				found = true
			}
		}
		if !found {
			t.Errorf("LosingCandidates = %v, want secret/* listed as a loser", d.LosingCandidates)
		}
	})
}

// --- Capability union across policies, with source tracking.

func TestEvaluate_CapabilityUnionAcrossPolicies(t *testing.T) {
	ev := compile(t,
		named("read.hcl", rule("secret/data/foo", policy.CapabilityRead)),
		named("update.hcl", rule("secret/data/foo", policy.CapabilityUpdate)),
	)

	dRead, err := ev.Evaluate("secret/data/foo", policy.CapabilityRead)
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}
	if !dRead.Allowed || len(dRead.Sources) != 1 || dRead.Sources[0].Name != "read.hcl" {
		t.Errorf("read: got %+v, want Allowed with Sources = [read.hcl]", dRead)
	}

	dUpdate, err := ev.Evaluate("secret/data/foo", policy.CapabilityUpdate)
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}
	if !dUpdate.Allowed || len(dUpdate.Sources) != 1 || dUpdate.Sources[0].Name != "update.hcl" {
		t.Errorf("update: got %+v, want Allowed with Sources = [update.hcl]", dUpdate)
	}
}

func TestEvaluate_CapabilityUnion_SameCapabilityFromTwoPolicies(t *testing.T) {
	ev := compile(t,
		named("a.hcl", rule("secret/data/foo", policy.CapabilityRead)),
		named("b.hcl", rule("secret/data/foo", policy.CapabilityRead)),
	)

	d, err := ev.Evaluate("secret/data/foo", policy.CapabilityRead)
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}
	if !d.Allowed || len(d.Sources) != 2 {
		t.Fatalf("got %+v, want Allowed with both sources listed", d)
	}
	names := map[string]bool{d.Sources[0].Name: true, d.Sources[1].Name: true}
	if !names["a.hcl"] || !names["b.hcl"] {
		t.Errorf("Sources = %v, want both a.hcl and b.hcl", d.Sources)
	}
}

// TestEvaluate_GrantingSourcesExcludeErasedCapabilities is Kevin's point
// 8, 2026-09-17: once a deny arrives, sources that contributed a
// capability BEFORE the deny must not be reported as still granting
// anything — capability contribution tracking must reflect only what is
// actually still effective.
func TestEvaluate_GrantingSourcesExcludeErasedCapabilities(t *testing.T) {
	ev := compile(t,
		named("read.hcl", rule("secret/data/foo", policy.CapabilityRead)),
		named("deny.hcl", rule("secret/data/foo", policy.CapabilityDeny)),
	)

	d, err := ev.Evaluate("secret/data/foo", policy.CapabilityRead)
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}
	if !d.Denied {
		t.Fatalf("got %+v, want Denied = true", d)
	}
	if len(d.Sources) != 0 {
		t.Errorf("Sources = %v, want empty — read.hcl's read capability was erased by deny.hcl's deny, so it must not be reported as still granting access", d.Sources)
	}
	explanation := d.Explain()
	if strings.Contains(explanation, "read.hcl") {
		t.Errorf("Explain() = %q, must not mention read.hcl as granting anything once deny erased it", explanation)
	}
}

// --- Expiration: deterministic, snapshot semantics, and an expired deny
// contributes nothing.

func TestCompile_ExpiredRule_ExcludedEntirely(t *testing.T) {
	past := fixedNow.Add(-time.Hour)
	ev := compile(t, named("p.hcl", policy.Rule{
		Path:         "secret/data/foo",
		Capabilities: []policy.Capability{policy.CapabilityRead},
		Expiration:   &past,
	}))

	d, err := ev.Evaluate("secret/data/foo", policy.CapabilityRead)
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}
	if d.Allowed || d.Stage != StageDefaultDeny {
		t.Errorf("got %+v, want default deny — an expired rule must be excluded as if never written", d)
	}
}

func TestCompile_FutureExpiration_StillActive(t *testing.T) {
	future := fixedNow.Add(time.Hour)
	ev := compile(t, named("p.hcl", policy.Rule{
		Path:         "secret/data/foo",
		Capabilities: []policy.Capability{policy.CapabilityRead},
		Expiration:   &future,
	}))

	d, err := ev.Evaluate("secret/data/foo", policy.CapabilityRead)
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}
	if !d.Allowed {
		t.Errorf("got %+v, want Allowed = true — expiration is in the future relative to the compile time", d)
	}
}

// TestCompile_ExpiredDeny_ContributesNothing is Kevin's point 4,
// 2026-09-17: an expired deny must not merely lose to another rule — it
// must be excluded from compilation entirely, so a broader allow that it
// would otherwise have shadowed becomes reachable.
func TestCompile_ExpiredDeny_ContributesNothing(t *testing.T) {
	past := fixedNow.Add(-time.Hour)
	ev := compile(t,
		named("p.hcl",
			policy.Rule{
				Path:         "secret/data/foo",
				Capabilities: []policy.Capability{policy.CapabilityDeny},
				Expiration:   &past,
			},
			rule("secret/*", policy.CapabilityRead),
		),
	)

	d, err := ev.Evaluate("secret/data/foo", policy.CapabilityRead)
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}
	if !d.Allowed || d.Denied {
		t.Errorf("got %+v, want the prefix rule to grant access — the expired deny must contribute nothing, not even a residual denial", d)
	}
	if d.Stage != StagePrefixOrWildcard {
		t.Errorf("Stage = %v, want StagePrefixOrWildcard (the exact expired-deny rule must be gone entirely)", d.Stage)
	}
}

// --- list/scan 4-stage fallback.

func TestEvaluate_ListScan_ExactTrailingSlashTrimmed(t *testing.T) {
	ev := compile(t, named("p.hcl", rule("secret/data/team-a", policy.CapabilityList)))

	d, err := ev.Evaluate("secret/data/team-a/", policy.CapabilityList)
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}
	if !d.Allowed || d.Stage != StageExactTrailingSlashTrimmed {
		t.Errorf("got %+v, want an allowed match at StageExactTrailingSlashTrimmed", d)
	}
}

func TestEvaluate_ListScan_TrailingSlashFallback_NotAppliedToOtherCapabilities(t *testing.T) {
	// The trailing-slash retry is list/scan-only — a read request for the
	// same shape must not benefit from it.
	ev := compile(t, named("p.hcl", rule("secret/data/team-a", policy.CapabilityRead)))

	d, err := ev.Evaluate("secret/data/team-a/", policy.CapabilityRead)
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}
	if d.Allowed {
		t.Errorf("got %+v, want default deny — the trailing-slash exact retry only applies to list/scan", d)
	}
}

func TestEvaluate_ListScan_PrefixTrailingSlashTrimmed(t *testing.T) {
	// No exact rule anywhere, and the plain prefix/wildcard match on the
	// path as given (with the trailing "/") fails to match a
	// segment-wildcard pattern with the same segment count — but
	// trimming the "/" lets it match.
	ev := compile(t, named("p.hcl", rule("secret/+/team-a", policy.CapabilityScan)))

	d, err := ev.Evaluate("secret/data/team-a/", policy.CapabilityScan)
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}
	if !d.Allowed || d.Stage != StagePrefixOrWildcardTrailingSlashTrimmed {
		t.Errorf("got %+v, want an allowed match at StagePrefixOrWildcardTrailingSlashTrimmed", d)
	}
}

// --- Incomplete evaluation: parameter constraints and identity
// templates.

func TestEvaluate_ParameterConstraints_Incomplete(t *testing.T) {
	ev := compile(t, named("p.hcl", policy.Rule{
		Path:               "secret/data/foo",
		Capabilities:       []policy.Capability{policy.CapabilityCreate},
		RequiredParameters: []string{"owner"},
	}))

	d, err := ev.Evaluate("secret/data/foo", policy.CapabilityCreate)
	if !errors.Is(err, ErrIncompleteEvaluation) {
		t.Fatalf("err = %v, want ErrIncompleteEvaluation", err)
	}
	if d.Allowed {
		t.Errorf("got Allowed = true, want false — incomplete must never look like a real answer, err = %v", err)
	}
	if !d.Incomplete {
		t.Errorf("Incomplete = false, want true")
	}
}

func TestEvaluate_ParameterConstraints_OnlyMatterWhenCapabilityGranted(t *testing.T) {
	// The winning rule has a parameter constraint, but the requested
	// capability isn't even among what it grants — the answer (denied)
	// is trustworthy regardless of the constraint, matching OpenBao's own
	// operationAllowed short-circuit before any parameter checking.
	ev := compile(t, named("p.hcl", policy.Rule{
		Path:               "secret/data/foo",
		Capabilities:       []policy.Capability{policy.CapabilityCreate},
		RequiredParameters: []string{"owner"},
	}))

	d, err := ev.Evaluate("secret/data/foo", policy.CapabilityRead)
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}
	if d.Allowed || d.Incomplete {
		t.Errorf("got %+v, want a clean denial — read is not granted at all, so the create-only parameter constraint is irrelevant", d)
	}
}

func TestEvaluate_IdentityTemplate_Incomplete(t *testing.T) {
	ev := compile(t, named("p.hcl", rule("secret/data/{{identity.entity.id}}/*", policy.CapabilityRead)))

	d, err := ev.Evaluate("secret/data/{{identity.entity.id}}/foo", policy.CapabilityRead)
	if !errors.Is(err, ErrIncompleteEvaluation) {
		t.Fatalf("err = %v, want ErrIncompleteEvaluation", err)
	}
	if d.Allowed {
		t.Error("got Allowed = true, want false for an unresolved identity template")
	}
}

// --- Go-API input validation (Kevin's point 5, 2026-09-17: validated
// independently of the CLI).

func TestEvaluate_UnknownCapability_Errors(t *testing.T) {
	ev := compile(t, named("p.hcl", rule("secret/data/foo", policy.CapabilityRead)))

	_, err := ev.Evaluate("secret/data/foo", policy.Capability("frobnicate"))
	if !errors.Is(err, ErrUnknownCapability) {
		t.Fatalf("err = %v, want ErrUnknownCapability", err)
	}
}

func TestCompile_UnknownCapabilityInPolicy_Errors(t *testing.T) {
	_, err := Compile([]NamedPolicy{named("p.hcl", rule("secret/data/foo", policy.Capability("frobnicate")))}, fixedNow)
	if !errors.Is(err, ErrUnknownCapability) {
		t.Fatalf("err = %v, want ErrUnknownCapability", err)
	}
}

func TestCompile_MalformedPattern_Errors(t *testing.T) {
	_, err := Compile([]NamedPolicy{named("p.hcl", rule("secret/+*", policy.CapabilityRead))}, fixedNow)
	if !errors.Is(err, ErrMalformedPattern) {
		t.Fatalf("err = %v, want ErrMalformedPattern", err)
	}
}
