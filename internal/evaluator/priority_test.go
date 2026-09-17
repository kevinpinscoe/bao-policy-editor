package evaluator

// The seven test cases in poltests below are ported directly from
// OpenBao's own TestACL_SegmentWildcardPriority, which is licensed under
// the same MPL-2.0 as this repository:
//
//	Copyright (c) HashiCorp, Inc.
//	SPDX-License-Identifier: MPL-2.0
//
// Source, pinned to the exact commit read and verified byte-identical on
// 2026-09-17: github.com/openbao/openbao, commit
// cb8bb0c281daabf81720e60aa989ce776572cd59,
// internal/vault/policy/acl_test.go:
//
//	https://github.com/openbao/openbao/blob/cb8bb0c281daabf81720e60aa989ce776572cd59/internal/vault/policy/acl_test.go#L703-L803
//
// Each case's HCL is unchanged from the original; only the harness
// differs, adapted to BPE's own policy.Policy/Evaluator types (the
// original built *Policy/*ACL via OpenBao's own HCL parser and NewACL —
// this test builds policy.Rule values directly and runs them through
// this package's Compile/Evaluate, since BPE does not depend on
// OpenBao's parser). Reusing OpenBao's own verified test cases, rather
// than inventing new ones, is what makes this priority_test.go a direct
// check against the real, documented behavior — not merely BPE's own
// interpretation of it.

import (
	"testing"

	"github.com/kevinpinscoe/bao-policy-editor/internal/policy"
)

func TestEvaluate_SegmentWildcardPriority(t *testing.T) {
	type poltest struct {
		name       string
		readPath   string
		updatePath string
		queryPath  string
	}

	// Each case has a read rule and an update rule; the update rule must
	// win because it is more specific, per whichever priority dimension
	// the case name identifies.
	poltests := []poltest{
		{
			// Verify edge conditions. Here "*" is more specific both
			// because of first wildcard position (0 vs -1/infinity) and
			// #wildcards.
			"first-wildcard-position and wildcard-count edge case",
			"+/*", "*", "foo/bar/bar/baz",
		},
		{
			// Verify edge conditions. Here "+/*" is less specific because
			// of first wildcard position.
			"first wildcard position",
			"+/*", "foo/+/*", "foo/bar/bar/baz",
		},
		{
			// Verify that more wildcard segments is lower priority.
			"wildcard segment count (prefix pattern)",
			"foo/+/+/*", "foo/+/bar/baz", "foo/bar/bar/baz",
		},
		{
			// Verify that more wildcard segments is lower priority.
			"wildcard segment count (non-prefix pattern)",
			"foo/+/+/baz", "foo/+/bar/baz", "foo/bar/bar/baz",
		},
		{
			// Verify that first wildcard position is lower priority.
			// "(" is used here because it is lexicographically smaller
			// than "+".
			"first wildcard position, lexicographic decoy",
			"foo/+/(ar/baz", "foo/(ar/+/baz", "foo/(ar/(ar/baz",
		},
		{
			// Verify that a glob has lower priority, even if the prefix
			// is the same otherwise.
			"trailing glob is lower priority",
			"foo/bar/+/baz*", "foo/bar/+/baz", "foo/bar/bar/baz",
		},
		{
			// Verify that a shorter prefix has lower priority.
			"shorter prefix is lower priority",
			"foo/bar/+/b*", "foo/bar/+/ba*", "foo/bar/bar/baz",
		},
	}

	for _, pt := range poltests {
		t.Run(pt.name, func(t *testing.T) {
			ev := compile(t, named("p.hcl",
				rule(pt.readPath, policy.CapabilityRead),
				rule(pt.updatePath, policy.CapabilityUpdate),
			))

			dUpdate, err := ev.Evaluate(pt.queryPath, policy.CapabilityUpdate)
			if err != nil {
				t.Fatalf("Evaluate(update) error = %v", err)
			}
			if !dUpdate.Allowed {
				t.Errorf("update: got %+v, want Allowed = true (update path %q should win)", dUpdate, pt.updatePath)
			}

			dRead, err := ev.Evaluate(pt.queryPath, policy.CapabilityRead)
			if err != nil {
				t.Fatalf("Evaluate(read) error = %v", err)
			}
			if dRead.Allowed {
				t.Errorf("read: got %+v, want Allowed = false (read path %q should lose)", dRead, pt.readPath)
			}
		})
	}
}
