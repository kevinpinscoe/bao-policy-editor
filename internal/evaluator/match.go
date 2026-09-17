package evaluator

import (
	"errors"
	"strings"
)

// classifyPath replicates OpenBao's own path-pattern classification
// exactly (policy.go's ParseACLPolicyWithTemplating, pinned commit
// cb8bb0c281daabf81720e60aa989ce776572cd59, lines 555-579):
//
//	https://github.com/openbao/openbao/blob/cb8bb0c281daabf81720e60aa989ce776572cd59/internal/vault/policy/policy.go#L555-L579
//
// raw is a rule's Path exactly as decoded. matchPath is the lookup key:
// raw unchanged for an exact rule, or raw with its trailing "*" stripped
// for a plain prefix rule. isPrefix and hasSegmentWildcards classify
// which of the three matching modes applies — see compiledRule's doc
// comment for what each one means for lookup.
//
// A "+" only has special meaning when it occupies an entire path
// segment — as the whole path, immediately after a "/", or immediately
// before one. A "+" embedded inside a segment (e.g. "foo+bar") is a
// literal character with no wildcard meaning, exactly as OpenBao treats
// it. A trailing "*" is only stripped (and isPrefix set) when the path
// has no segment wildcards — a segment-wildcard path keeps its trailing
// "*" as part of the pattern OpenBao itself stores
// (segmentWildcardPaths is keyed by the un-stripped path; see
// matchWildcardRule, which strips it locally for its own structural
// match).
//
// err is non-nil only for the "+*" combination, which OpenBao's parser
// rejects outright as invalid rather than accepting it with any
// matching behavior at all.
func classifyPath(raw string) (matchPath string, isPrefix, hasSegmentWildcards bool, err error) {
	if strings.Contains(raw, "+*") {
		return "", false, false, errors.New(`"+*" is forbidden`)
	}

	if raw == "+" || strings.Count(raw, "/+") > 0 || strings.HasPrefix(raw, "+/") {
		hasSegmentWildcards = true
	}

	matchPath = raw
	if before, ok := strings.CutSuffix(raw, "*"); ok && !hasSegmentWildcards {
		matchPath = before
		isPrefix = true
	}

	return matchPath, isPrefix, hasSegmentWildcards, nil
}

// templatedNearMiss reports whether some rule containing an unresolved
// OpenBao identity template could plausibly match path once that
// template is resolved to a real value. Evaluate calls this on every
// request, regardless of what the literal lookup already decided
// (Kevin's instruction, 2026-09-17, generalizing an earlier, narrower
// version of this check that ran only before a true default deny): a
// broad literal allow could be shadowed by a more specific templated
// deny once resolved, a broad literal deny could be shadowed by a more
// specific templated allow, and a template that could resolve to the
// exact same pattern as a literal rule could change that rule's merged
// capabilities or introduce a deny — none of which a purely literal
// lookup can rule out.
//
// This is deliberately conservative rather than exhaustive: a templated
// segment is treated as matching anything (the same way a "+" segment
// is, and regardless of whether the template occupies a whole segment or
// is embedded alongside literal text within one — OpenBao's own
// templating is not restricted to whole-segment placement, and neither
// is this check), so a rule is flagged whenever it is structurally
// CONSISTENT with path, not only when BPE can prove it would actually
// win or actually merge. That keeps the check scoped to rules that could
// genuinely have mattered for this specific path — an unrelated
// templated rule elsewhere in the policy (different literal prefix,
// different segment count) does not poison an otherwise-independent
// result.
func (e *Evaluator) templatedNearMiss(path string) *compiledRule {
	for _, r := range e.templated {
		if couldTemplatedRuleMatch(r.pattern, path) {
			return r
		}
	}
	return nil
}

// couldTemplatedRuleMatch is templatedNearMiss's per-rule structural
// check: every literal segment of pattern must equal the corresponding
// segment of path exactly (or, for a trailing "*" prefix pattern's last
// segment, be a literal string prefix of it — the same partial-match
// rule matchWildcardRule uses); every "+" or template ("{{"/"}}")
// segment matches anything.
func couldTemplatedRuleMatch(pattern, path string) bool {
	isPrefix := false
	p := pattern
	if before, ok := strings.CutSuffix(pattern, "*"); ok {
		isPrefix = true
		p = before
	}

	patternSegs := strings.Split(p, "/")
	pathSegs := strings.Split(path, "/")

	if len(pathSegs) < len(patternSegs) {
		return false
	}
	if !isPrefix && len(pathSegs) != len(patternSegs) {
		return false
	}

	for i, seg := range patternSegs {
		switch {
		case seg == "+":
		case strings.Contains(seg, "{{") && strings.Contains(seg, "}}"):
			// A template may be embedded within a segment alongside
			// literal text ("user-{{identity.entity.id}}-config"), not
			// only occupy a whole segment on its own — OpenBao's
			// templating syntax is not restricted that way, and this
			// check does not assume it is. Only the literal text before
			// the first "{{" and after the last "}}" in this segment is
			// known regardless of what the template resolves to;
			// require the corresponding path segment to actually carry
			// that literal prefix and suffix, rather than treating the
			// whole segment as an unconditional wildcard (which would
			// make an unrelated path falsely look like a near miss).
			open := strings.Index(seg, "{{")
			closeIdx := strings.LastIndex(seg, "}}") + 2
			if !strings.HasPrefix(pathSegs[i], seg[:open]) || !strings.HasSuffix(pathSegs[i], seg[closeIdx:]) {
				return false
			}
		case isPrefix && i == len(patternSegs)-1:
			if !strings.HasPrefix(pathSegs[i], seg) {
				return false
			}
		case seg != pathSegs[i]:
			return false
		}
	}
	return true
}

// wildcardCandidate mirrors OpenBao's wcPathDescr (acl.go) — a candidate
// among prefix-or-segment-wildcard matches, ranked by the 5-rule
// priority comparator in priorityLess.
type wildcardCandidate struct {
	rule          *compiledRule
	firstWCOrGlob int
	isPrefix      bool
	wildcards     int
	wcPath        string
}

// priorityLess reports whether a is lower priority than b, replicating
// OpenBao's own comparator exactly (acl.go's CheckAllowedFromNonExactPaths,
// its `less` closure, pinned commit
// cb8bb0c281daabf81720e60aa989ce776572cd59, lines 690-744):
//
//	https://github.com/openbao/openbao/blob/cb8bb0c281daabf81720e60aa989ce776572cd59/internal/vault/policy/acl.go#L690-L744
//
// Priority order, highest first, verified against OpenBao's own
// TestACL_SegmentWildcardPriority (ported into priority_test.go):
//
//  1. A later first wildcard/glob position wins (a more specific literal
//     prefix before the first wildcard beats an earlier one).
//  2. Not ending in "*" beats ending in "*".
//  3. Fewer "+" segments beats more.
//  4. A longer matched path beats a shorter one.
//  5. Lexicographically larger wins the remaining tie — OpenBao's own
//     comment calls this case "should never really come up."
//
// reason names which of the five decided, so a LosingCandidate can cite
// the actual cause rather than a generic "lost on priority" (Kevin's
// instruction, 2026-09-17).
func priorityLess(a, b wildcardCandidate) (less bool, reason string) {
	if a.firstWCOrGlob != b.firstWCOrGlob {
		return a.firstWCOrGlob < b.firstWCOrGlob, "an earlier first wildcard/glob position is lower priority"
	}
	if a.isPrefix != b.isPrefix {
		return a.isPrefix, `a pattern ending in "*" is lower priority than one that does not`
	}
	if a.wildcards != b.wildcards {
		return a.wildcards > b.wildcards, `more "+" wildcard segments is lower priority`
	}
	if len(a.wcPath) != len(b.wcPath) {
		return len(a.wcPath) < len(b.wcPath), "a shorter pattern is lower priority"
	}
	if a.wcPath != b.wcPath {
		return a.wcPath < b.wcPath, "lexicographically smaller is lower priority (arbitrary tie-break)"
	}
	return false, ""
}

// matchNonExact finds the single highest-priority prefix-or-wildcard
// match for path, replicating CheckAllowedFromNonExactPaths's non-
// bareMount behavior (acl.go, pinned commit
// cb8bb0c281daabf81720e60aa989ce776572cd59, lines 687-838); the
// bareMount case does not apply here — BPE evaluates one path at a time,
// it has no concept of a "mount" to bulk-check:
//
//	https://github.com/openbao/openbao/blob/cb8bb0c281daabf81720e60aa989ce776572cd59/internal/vault/policy/acl.go#L687-L838
//
// It returns the winner and, best-effort, an explanation of every OTHER
// candidate that structurally matched but lost — this discovery work
// never influences which rule wins; winner selection alone is the
// faithful port of the real algorithm, and losers is purely descriptive.
func (e *Evaluator) matchNonExact(path string) (winner *compiledRule, losers []LosingCandidate) {
	var candidates []wildcardCandidate

	var prefixWinner *compiledRule
	bestLen := -1
	for _, r := range e.prefixes {
		if strings.HasPrefix(path, r.matchPath) && len(r.matchPath) > bestLen {
			prefixWinner = r
			bestLen = len(r.matchPath)
		}
	}
	if prefixWinner != nil {
		candidates = append(candidates, wildcardCandidate{
			rule:          prefixWinner,
			firstWCOrGlob: len(prefixWinner.matchPath),
			isPrefix:      true,
			wcPath:        prefixWinner.matchPath,
		})
	}

	pathParts := strings.Split(path, "/")
	for _, r := range e.wildcards {
		if cand, ok := matchWildcardRule(r, path, pathParts); ok {
			candidates = append(candidates, cand)
		}
	}

	if len(candidates) == 0 {
		return nil, nil
	}

	winnerIdx := 0
	for i := 1; i < len(candidates); i++ {
		if less, _ := priorityLess(candidates[winnerIdx], candidates[i]); less {
			winnerIdx = i
		}
	}
	winner = candidates[winnerIdx].rule

	for i, c := range candidates {
		if i == winnerIdx {
			continue
		}
		_, reason := priorityLess(c, candidates[winnerIdx])
		losers = append(losers, LosingCandidate{
			Pattern: c.rule.pattern,
			Sources: c.rule.sources(),
			Reason:  LossReasonWildcardPriority,
			Detail:  reason,
		})
	}

	for _, r := range e.prefixes {
		if r != prefixWinner && strings.HasPrefix(path, r.matchPath) {
			losers = append(losers, LosingCandidate{
				Pattern: r.pattern,
				Sources: r.sources(),
				Reason:  LossReasonShorterPrefix,
				Detail:  "a longer, more specific prefix pattern matched the same path",
			})
		}
	}

	return winner, losers
}

// matchWildcardRule reports whether r structurally matches path,
// replicating the SWCPATH loop's non-bareMount branch exactly (acl.go,
// pinned commit cb8bb0c281daabf81720e60aa989ce776572cd59, lines
// 768-827):
//
//	https://github.com/openbao/openbao/blob/cb8bb0c281daabf81720e60aa989ce776572cd59/internal/vault/policy/acl.go#L768-L827
//
// The trailing segment of a prefix (isPrefix) pattern may match by
// literal string prefix rather than whole-segment equality — e.g.
// pattern segment "ba" structurally matches path segment "bar". Every
// other segment (and every segment of a non-prefix pattern) must match
// exactly, or be "+" (matching any single segment).
func matchWildcardRule(r *compiledRule, path string, pathParts []string) (wildcardCandidate, bool) {
	full := r.pattern
	isPrefix := false
	currWCPath := full
	if strings.HasSuffix(full, "*") {
		isPrefix = true
		currWCPath = full[:len(full)-1]
	}
	splitCurr := strings.Split(currWCPath, "/")

	if len(pathParts) < len(splitCurr) {
		return wildcardCandidate{}, false
	}
	if !isPrefix && len(splitCurr) != len(pathParts) {
		return wildcardCandidate{}, false
	}

	wildcards := 0
	for i, part := range splitCurr {
		switch {
		case part == "+":
			wildcards++
		case part == pathParts[i]:
			// exact literal segment match
		case isPrefix && i == len(splitCurr)-1 && strings.HasPrefix(pathParts[i], part):
			// trailing partial-prefix segment match
		default:
			return wildcardCandidate{}, false
		}
	}

	return wildcardCandidate{
		rule:          r,
		firstWCOrGlob: strings.Index(full, "+"),
		isPrefix:      isPrefix,
		wildcards:     wildcards,
		wcPath:        currWCPath,
	}, true
}
