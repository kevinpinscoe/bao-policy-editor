// Package evaluator implements OpenBao-compatible effective-access
// simulation for BPE. It works entirely on internal/policy's
// UI-independent domain model — no dependency on Bubble Tea, the
// filesystem, HCL, or the OpenBao API — so it can be constructed and
// queried purely in memory, exactly like internal/policy itself.
//
// The matching, merge, and priority algorithm here is ported from
// OpenBao's own ACL implementation, not invented (Kevin's instruction,
// per the build brief: "Do not invent matching semantics. Base behavior
// on current official OpenBao documentation and cite the relevant
// documentation in code comments where an algorithm would otherwise be
// surprising."). Every non-obvious piece of behavior below cites exactly
// where it comes from.
//
// Pinned reference, read and verified byte-identical against this exact
// commit on 2026-09-17: github.com/openbao/openbao, commit
// cb8bb0c281daabf81720e60aa989ce776572cd59 (main branch tip at read
// time).
//
//	https://github.com/openbao/openbao/blob/cb8bb0c281daabf81720e60aa989ce776572cd59/internal/vault/policy/acl.go
//	https://github.com/openbao/openbao/blob/cb8bb0c281daabf81720e60aa989ce776572cd59/internal/vault/policy/policy.go
package evaluator

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/kevinpinscoe/bao-policy-editor/internal/policy"
)

// Sentinel errors. Wrap one of these with fmt.Errorf's %w so callers can
// use errors.Is regardless of the specific message attached.
var (
	// ErrUnknownCapability means a capability string — either one found
	// inside a policy being compiled, or the capability argument passed
	// to Evaluate — is not one of policy.Capabilities. This is checked
	// independently at both Compile and Evaluate, so a direct Go-API
	// caller gets the same protection the CLI's own upfront validation
	// gives it (Kevin's instruction, 2026-09-17: validate Go-API inputs,
	// not only CLI arguments).
	ErrUnknownCapability = errors.New("unknown capability")

	// ErrMalformedPattern means a policy's path pattern uses the "+*"
	// combination, which OpenBao's own parser rejects outright (policy.go,
	// pinned commit cb8bb0c281daabf81720e60aa989ce776572cd59, line 563):
	// https://github.com/openbao/openbao/blob/cb8bb0c281daabf81720e60aa989ce776572cd59/internal/vault/policy/policy.go#L563-L565
	ErrMalformedPattern = errors.New("malformed path pattern")

	// ErrIncompleteEvaluation means Evaluate found a winning rule but
	// cannot reduce it to a trustworthy allow/deny from a path and
	// capability alone — see Evaluate's doc comment for the two cases
	// this covers. A caller must not treat the accompanying Decision's
	// Allowed field as a real answer when this error is returned; it is
	// always false, which is the safe default, but it is not a denial.
	ErrIncompleteEvaluation = errors.New("cannot determine a trustworthy access decision from path and capability alone")
)

// Source identifies where a policy came from, for a Decision's source
// attribution ("source policy or file", "capabilities contributed by
// identical winning patterns" — FSM-13's acceptance criteria). BPE
// evaluates local HCL files, not named OpenBao policies, so this is
// ordinarily a filename — kept as its own type rather than a bare string
// so a later ticket (remote OpenBao policies, FSM-16) can populate it
// with a real policy name instead without an API break.
type Source struct {
	Name string
}

// NamedPolicy pairs a decoded policy with the Source it came from.
// Compile accepts a slice of these — not a single policy.Policy — because
// FSM-13 requires capability union and source attribution "across
// multiple policies" (a token in real OpenBao can carry more than one),
// even though today's only caller (bpe test) compiles just one file.
type NamedPolicy struct {
	Source Source
	Policy policy.Policy
}

// compiledRule is the merged, ready-to-query state for one distinct path
// pattern, after folding in every NamedPolicy's rule for that pattern.
// See mergeRule for the merge algorithm itself.
type compiledRule struct {
	// pattern is the rule's Path exactly as originally written — used
	// for display and, for a segment-wildcard rule, for the structural
	// match itself (matchWildcardRule uses pattern directly, including
	// any trailing "*", exactly as OpenBao's own segmentWildcardPaths map
	// does; see classifyPath's doc comment for why the trailing "*" is
	// NOT stripped from HasSegmentWildcards paths at classification
	// time).
	pattern string

	// matchPath is the lookup key for an exact or plain-prefix rule:
	// pattern unchanged for an exact rule, or pattern with its trailing
	// "*" stripped for a prefix rule. Unused for a segment-wildcard rule.
	matchPath string

	isPrefix            bool
	hasSegmentWildcards bool

	// looksTemplated is a heuristic: pattern contains what looks like an
	// unresolved OpenBao identity template ("{{" ... "}}"). BPE's domain
	// model (internal/policy, from FSM-11) has no concept of identity
	// templating — it decodes the path label as a literal string — so a
	// templated pattern would be matched against its literal template
	// syntax rather than a resolved identity, which is not a trustworthy
	// simulation. See Evaluate's doc comment for the resulting
	// ErrIncompleteEvaluation behavior.
	looksTemplated bool

	// effective is the capability set this rule actually grants, AFTER
	// deny-erasure (see mergeRule) — never the raw union of everything
	// ever merged in. Kevin's instruction, 2026-09-17: capability
	// contribution tracking must stay separate from effective grants
	// after deny erasure, so an explanation never describes an erased
	// capability as still granting access.
	effective map[policy.Capability]bool

	// grantingSources maps each capability in effective to the Source(s)
	// whose rule contributed it. Kept in lockstep with effective by
	// mergeRule: when a capability is erased from effective (deny
	// arrives), its entry here is deleted too, not merely left stale.
	grantingSources map[policy.Capability][]Source

	// hasParameterConstraints is true once any merged rule set
	// RequiredParameters, AllowedParameters, or DeniedParameters, and
	// false again if a later deny erases them (mirroring OpenBao's own
	// merge, which nils out AllowedParameters/DeniedParameters the
	// moment deny wins — acl.go line 170-171, pinned commit
	// cb8bb0c281daabf81720e60aa989ce776572cd59). FSM-13 does not attempt
	// to evaluate these constraints against concrete request data (bpe
	// test supplies no request parameters at all) — their mere presence
	// on the winning rule is what triggers ErrIncompleteEvaluation. See
	// Evaluate's doc comment.
	hasParameterConstraints bool
}

// sources returns the deduplicated, name-sorted union of every Source
// that has ever granted any capability still present in effective — used
// for LosingCandidate display, where "which policy defined this
// candidate" matters more than which specific capability it granted.
func (r *compiledRule) sources() []Source {
	seen := make(map[string]bool)
	var out []Source
	for _, srcs := range r.grantingSources {
		for _, s := range srcs {
			if !seen[s.Name] {
				seen[s.Name] = true
				out = append(out, s)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Evaluator is a compiled, ready-to-query index built from one or more
// policies by Compile — mirroring OpenBao's own ACL type (acl.go,
// pinned commit cb8bb0c281daabf81720e60aa989ce776572cd59), which merges
// every policy's path rules once at construction time rather than
// re-parsing or re-scanning raw policies on every request.
type Evaluator struct {
	at        time.Time
	exact     map[string]*compiledRule
	prefixes  []*compiledRule
	wildcards []*compiledRule

	// templated is every compiledRule whose pattern looks like it
	// contains an unresolved OpenBao identity template ("{{"/"}}"),
	// regardless of which of the three indices above it also lives in.
	// Evaluate consults this before conceding a true default deny — see
	// templatedNearMiss.
	templated []*compiledRule
}

// Compile builds an Evaluator from policies. at is the evaluation time
// used to decide which rules are expired (policy.Rule.Expiration) — it
// is an explicit parameter, not time.Now() read internally, so
// compilation is deterministic and testable (Kevin's instruction,
// 2026-09-17).
//
// The returned Evaluator is a SNAPSHOT at that instant: an expired rule
// is excluded from every index entirely, as if it had never been
// written — it contributes nothing, not even a deny (mirroring OpenBao's
// own parser, which skips an expired path outright rather than keeping
// it around as an inert or deny rule — policy.go, pinned commit
// cb8bb0c281daabf81720e60aa989ce776572cd59, lines 546-552):
//
//	https://github.com/openbao/openbao/blob/cb8bb0c281daabf81720e60aa989ce776572cd59/internal/vault/policy/policy.go#L546-L552
//
// If a rule's applicability could change — time passing makes it expire,
// or a policy is edited — the caller must call Compile again with a
// fresh at. An Evaluator never re-checks expiration lazily; it only
// knows what was true when it was built.
//
// Compile validates its input and returns a non-nil error rather than
// silently accepting something that would produce a misleading decision
// later (Kevin's instruction, 2026-09-17): an unknown capability
// anywhere in any policy (wrapping ErrUnknownCapability) or a path using
// the forbidden "+*" combination (wrapping ErrMalformedPattern) fails
// the whole compilation — matching OpenBao's own parser, which rejects
// both outright at policy-load time rather than ignoring them.
func Compile(policies []NamedPolicy, at time.Time) (*Evaluator, error) {
	e := &Evaluator{
		at:    at,
		exact: make(map[string]*compiledRule),
	}

	for _, np := range policies {
		for _, rule := range np.Policy.Rules {
			if rule.Expiration != nil && at.After(*rule.Expiration) {
				continue
			}

			for _, c := range rule.Capabilities {
				if !c.Known() {
					return nil, fmt.Errorf("%w: path %q (from %s): %q", ErrUnknownCapability, rule.Path, np.Source.Name, c)
				}
			}

			matchPath, isPrefix, hasSegmentWildcards, err := classifyPath(rule.Path)
			if err != nil {
				return nil, fmt.Errorf("%w: path %q (from %s): %w", ErrMalformedPattern, rule.Path, np.Source.Name, err)
			}

			target := e.findOrCreate(rule.Path, matchPath, isPrefix, hasSegmentWildcards)
			mergeRule(target, rule, np.Source)
		}
	}

	return e, nil
}

// findOrCreate returns the compiledRule for pattern, creating and
// indexing it on first sight. Dedup key: matchPath for an exact or
// plain-prefix rule (equivalent to comparing the raw pattern itself,
// since matchPath is a deterministic, reversible transform of it), or
// the full original pattern for a segment-wildcard rule (matching
// OpenBao's own segmentWildcardPaths map, keyed by the un-stripped
// path — acl.go line 151, pinned commit
// cb8bb0c281daabf81720e60aa989ce776572cd59).
func (e *Evaluator) findOrCreate(pattern, matchPath string, isPrefix, hasSegmentWildcards bool) *compiledRule {
	switch {
	case hasSegmentWildcards:
		for _, existing := range e.wildcards {
			if existing.pattern == pattern {
				return existing
			}
		}
	case isPrefix:
		for _, existing := range e.prefixes {
			if existing.matchPath == matchPath {
				return existing
			}
		}
	default:
		if existing, ok := e.exact[matchPath]; ok {
			return existing
		}
	}

	target := &compiledRule{
		pattern:             pattern,
		matchPath:           matchPath,
		isPrefix:            isPrefix,
		hasSegmentWildcards: hasSegmentWildcards,
		looksTemplated:      strings.Contains(pattern, "{{") && strings.Contains(pattern, "}}"),
		effective:           make(map[policy.Capability]bool),
		grantingSources:     make(map[policy.Capability][]Source),
	}

	switch {
	case hasSegmentWildcards:
		e.wildcards = append(e.wildcards, target)
	case isPrefix:
		e.prefixes = append(e.prefixes, target)
	default:
		e.exact[matchPath] = target
	}

	if target.looksTemplated {
		e.templated = append(e.templated, target)
	}

	return target
}

// mergeRule folds rule (from source) into target, replicating OpenBao's
// own per-path merge rule exactly (acl.go's NewACL, pinned commit
// cb8bb0c281daabf81720e60aa989ce776572cd59):
//
//	https://github.com/openbao/openbao/blob/cb8bb0c281daabf81720e60aa989ce776572cd59/internal/vault/policy/acl.go#L161-L179
//
// The two branches below are individually order-dependent — which rule
// is "already merged into target" and which is "arriving now" matters
// within each branch — but the overall RESULT is order-independent:
// whichever of "target is already deny-only" or "rule itself denies" is
// true, the merged result becomes deny-only, regardless of which rule
// was processed first. This is what makes an explicit deny actually take
// precedence over an allow AT THE SAME PATTERN, across multiple
// policies.
//
// This function is only ever called once a pattern has already won the
// lookup in Evaluate (or, here, once it is the target for a specific
// distinct pattern during Compile) — it never runs across DIFFERENT
// patterns. A broader deny at a pattern that is not the winning pattern
// for a given request never reaches here at all for that request: see
// Evaluate's doc comment and evaluator_test.go's
// TestEvaluate_BroaderDenyDoesNotOverrideMoreSpecificAllow, which is the
// regression proving this (Kevin's instruction, 2026-09-17).
func mergeRule(target *compiledRule, rule policy.Rule, source Source) {
	ruleIsDeny := rule.HasCapability(policy.CapabilityDeny)

	switch {
	case target.effective[policy.CapabilityDeny]:
		// "If we are explicitly denied in the existing capability set,
		// don't save anything else."
		return

	case ruleIsDeny:
		// "If this new policy explicitly denies, only save the deny
		// value" — erase whatever was there before, including its
		// granting-source history and any parameter constraints, and
		// record only the deny.
		for cap := range target.effective {
			delete(target.effective, cap)
		}
		for cap := range target.grantingSources {
			delete(target.grantingSources, cap)
		}
		target.effective[policy.CapabilityDeny] = true
		target.grantingSources[policy.CapabilityDeny] = append(target.grantingSources[policy.CapabilityDeny], source)
		target.hasParameterConstraints = false

	default:
		for _, cap := range rule.Capabilities {
			target.effective[cap] = true
			target.grantingSources[cap] = append(target.grantingSources[cap], source)
		}
		if len(rule.RequiredParameters) > 0 || len(rule.AllowedParameters) > 0 || len(rule.DeniedParameters) > 0 {
			target.hasParameterConstraints = true
		}
	}
}

// MatchStage identifies which stage of OpenBao's lookup order (see
// Evaluate) produced a Decision's winning rule, or that none did.
type MatchStage int

const (
	// StageNone is the zero value — never returned by Evaluate.
	StageNone MatchStage = iota
	// StageExact: an exact match on the path as given.
	StageExact
	// StageExactTrailingSlashTrimmed: list/scan only — an exact match
	// once a trailing "/" is trimmed from the requested path.
	StageExactTrailingSlashTrimmed
	// StagePrefixOrWildcard: the highest-priority prefix or
	// segment-wildcard match on the path as given.
	StagePrefixOrWildcard
	// StagePrefixOrWildcardTrailingSlashTrimmed: list/scan only, and only
	// tried when the path ends in "/" — the same as
	// StagePrefixOrWildcard, once that trailing "/" is trimmed.
	StagePrefixOrWildcardTrailingSlashTrimmed
	// StageDefaultDeny: nothing matched at any stage.
	StageDefaultDeny
)

func (s MatchStage) String() string {
	switch s {
	case StageExact:
		return "exact match"
	case StageExactTrailingSlashTrimmed:
		return "exact match (trailing \"/\" trimmed for list/scan)"
	case StagePrefixOrWildcard:
		return "prefix or wildcard match"
	case StagePrefixOrWildcardTrailingSlashTrimmed:
		return "prefix or wildcard match (trailing \"/\" trimmed for list/scan)"
	case StageDefaultDeny:
		return "no match (default deny)"
	default:
		return "unknown"
	}
}

// LossReason is why a candidate pattern that structurally matched the
// requested path did not win. Kevin's instruction, 2026-09-17: distinct,
// honest reasons — never attribute every loss to the same cause.
type LossReason int

const (
	// LossReasonShorterPrefix: a plain "*"-suffix prefix pattern lost
	// because a longer, more specific prefix pattern also matched.
	// Decided by longest-prefix selection, not the 5-rule comparator —
	// OpenBao's radix tree only ever returns the single longest match,
	// so a shorter prefix is never even compared against the winner.
	LossReasonShorterPrefix LossReason = iota
	// LossReasonWildcardPriority: this candidate (a segment-wildcard
	// pattern, or the single longest plain-prefix pattern) lost OpenBao's
	// 5-rule priority comparator against the winner. Detail names which
	// of the 5 rules decided.
	LossReasonWildcardPriority
)

func (r LossReason) String() string {
	switch r {
	case LossReasonShorterPrefix:
		return "a longer, more specific prefix pattern matched the same path"
	case LossReasonWildcardPriority:
		return "lost OpenBao's wildcard/glob priority comparison"
	default:
		return "unknown"
	}
}

// LosingCandidate is one pattern that structurally matched the requested
// path but did not win — FSM-13's "relevant matching patterns that lost
// due to priority". Never populated for a stage that short-circuited
// before this candidate could even be discovered — see Evaluate's doc
// comment on why an exact match never produces losing candidates.
type LosingCandidate struct {
	Pattern string
	Sources []Source
	Reason  LossReason
	Detail  string
}

// Decision is FSM-13's structured explanation of one effective-access
// check, carrying every field the ticket's acceptance criteria name:
// requested path, requested capability, allow-or-deny result, winning
// path pattern, source policy or file, capabilities contributed by
// identical winning patterns, explicit-deny information, and relevant
// matching patterns that lost due to priority.
type Decision struct {
	RequestedPath       string
	RequestedCapability policy.Capability

	// Allowed is the final answer. It is only meaningful when Incomplete
	// is false and the returned error is nil — see Evaluate's doc
	// comment. It is always false when Incomplete is true, which is the
	// safe default, but must never be read as "denied" in that case.
	Allowed bool

	// Denied is true only when the winning rule was an explicit deny —
	// distinct from Allowed being false because nothing matched at all,
	// or because the winning rule simply did not grant the requested
	// capability.
	Denied bool

	// WinningPattern is the rule's Path exactly as written, or empty if
	// nothing matched (Stage == StageDefaultDeny).
	WinningPattern string
	Stage          MatchStage

	// Sources are the Source(s) whose rule granted RequestedCapability at
	// the winning pattern — kept in lockstep with what is actually still
	// effective after any deny-erasure (Kevin's instruction, 2026-09-17):
	// empty whenever Denied is true or nothing was granted, never a stale
	// record of a capability that was later erased by a deny.
	Sources []Source

	// DeniedBy are the Source(s) whose rule set the deny, populated only
	// when Denied is true.
	DeniedBy []Source

	LosingCandidates []LosingCandidate

	// Incomplete is true when the winning rule cannot be reduced to a
	// trustworthy allow/deny from path and capability alone — see
	// Evaluate's doc comment for the two cases this covers.
	Incomplete       bool
	IncompleteReason string
}

// Evaluate simulates a single OpenBao access check, replicating the real
// lookup order exactly (acl.go's AllowOperation, pinned commit
// cb8bb0c281daabf81720e60aa989ce776572cd59):
//
//		https://github.com/openbao/openbao/blob/cb8bb0c281daabf81720e60aa989ce776572cd59/internal/vault/policy/acl.go#L343-L432
//
//	 1. Exact match on path as given.
//	 2. (list/scan only) Exact match with a trailing "/" trimmed.
//	 3. The single highest-priority prefix-or-segment-wildcard match.
//	 4. (list/scan only, only if path ends in "/") Step 3 again with the
//	    trailing "/" trimmed.
//	 5. Default deny — nothing matched.
//
// Each stage is tried in order and the first one to find ANY rule wins
// outright; later stages are never consulted once an earlier one
// matches, and Evaluate never scans every structurally-matching rule
// looking for a deny. This is deliberate fidelity to the real algorithm
// (Kevin's instruction, 2026-09-17): a broader deny at a pattern that is
// not the actual winner never enters into the decision, because Evaluate
// commits to the single winning rule from the single winning stage and
// only ever inspects that rule.
//
// capability must be one of policy.Capabilities (ErrUnknownCapability
// otherwise) — validated here independently of whatever validation a
// caller may or may not already have done, so a direct Go-API caller
// gets the same protection the CLI's own upfront validation gives it.
//
// If the winning rule cannot be reduced to a trustworthy allow/deny from
// path and capability alone, Evaluate returns a non-nil error wrapping
// ErrIncompleteEvaluation instead of guessing. Three cases trigger this:
//
//   - The winning rule's pattern looks like an unresolved OpenBao
//     identity template ("{{"/"}}" present) — BPE's domain model has no
//     concept of identity templating, so the pattern would be matched
//     against its literal template syntax rather than a resolved
//     identity, which is not a trustworthy simulation of what OpenBao
//     itself would decide.
//   - The winning rule grants the requested capability but also carries
//     required_parameters, allowed_parameters, or denied_parameters — a
//     real decision needs the actual request's parameter values, which
//     Evaluate (and bpe test, which supplies only a path and a
//     capability) is never given.
//   - Regardless of what the literal lookup decided — a confident allow,
//     a confident deny, or a clean default deny — a DIFFERENT rule
//     elsewhere carries an unresolved identity template that is
//     structurally consistent with path, so it could plausibly match
//     once resolved. That resolution could introduce a higher-priority
//     match, merge into the same resolved pattern as the literal winner
//     and change its effective capabilities or add a deny, or (when
//     nothing else matched) simply be the only rule that would have
//     matched at all. Kevin's instruction, 2026-09-17: a definite result
//     is only kept when independence from the template can be
//     established; this check runs on every request, not only when
//     nothing else matched, and conservatively returns INCOMPLETE rather
//     than trying to prove the template's resolution could not have
//     changed the outcome. See templatedNearMiss.
//
// Decision is still populated with what IS known (winning pattern where
// there is one, stage) when this error is returned, but Decision.Allowed
// is always false in that case and must never be read as a real denial —
// check Decision.Incomplete, or the returned error, instead.
func (e *Evaluator) Evaluate(path string, capability policy.Capability) (Decision, error) {
	if !capability.Known() {
		return Decision{}, fmt.Errorf("%w: %q", ErrUnknownCapability, capability)
	}

	// OpenBao strips every leading "/" from the request path before
	// matching (acl.go, pinned commit
	// cb8bb0c281daabf81720e60aa989ce776572cd59, lines 383-389):
	// https://github.com/openbao/openbao/blob/cb8bb0c281daabf81720e60aa989ce776572cd59/internal/vault/policy/acl.go#L383-L389
	path = strings.TrimLeft(path, "/")

	d := Decision{RequestedPath: path, RequestedCapability: capability}
	isListOrScan := capability == policy.CapabilityList || capability == policy.CapabilityScan

	dec, err := e.lookupLiteral(d, path, isListOrScan)

	// Regardless of what the literal lookup above found — a confident
	// allow, a confident deny, or a clean default deny — an unresolved
	// identity-template rule elsewhere that could plausibly match this
	// exact path might change that result once resolved: it could
	// introduce a higher-priority match, merge into an identical
	// resolved pattern and change the effective capabilities or add a
	// deny, or (the original gap) simply be the only rule that would
	// have matched at all. Kevin's instruction, 2026-09-17: keep a
	// definite result only when independence from the template can be
	// established; conservative INCOMPLETE is preferred over trying to
	// prove that in every case, so this check runs whenever the literal
	// lookup did not already decide the result is incomplete for some
	// other reason (decide's own looksTemplated/parameter-constraint
	// checks).
	if !dec.Incomplete {
		if tmpl := e.templatedNearMiss(path); tmpl != nil {
			return withTemplateUncertainty(dec, tmpl, path)
		}
		if isListOrScan && strings.HasSuffix(path, "/") {
			if tmpl := e.templatedNearMiss(strings.TrimSuffix(path, "/")); tmpl != nil {
				return withTemplateUncertainty(dec, tmpl, path)
			}
		}
	}

	return dec, err
}

// lookupLiteral runs the real 4-stage-plus-default-deny lookup exactly
// as before this ticket's regression review — a purely literal match,
// with no awareness of identity templates at all. Evaluate wraps this
// with the template-uncertainty check above.
func (e *Evaluator) lookupLiteral(d Decision, path string, isListOrScan bool) (Decision, error) {
	if rule, ok := e.exact[path]; ok {
		return decide(d, rule, StageExact)
	}
	if isListOrScan {
		if rule, ok := e.exact[strings.TrimSuffix(path, "/")]; ok {
			return decide(d, rule, StageExactTrailingSlashTrimmed)
		}
	}
	if rule, losers := e.matchNonExact(path); rule != nil {
		d.LosingCandidates = losers
		return decide(d, rule, StagePrefixOrWildcard)
	}
	if isListOrScan && strings.HasSuffix(path, "/") {
		trimmed := strings.TrimSuffix(path, "/")
		if rule, losers := e.matchNonExact(trimmed); rule != nil {
			d.LosingCandidates = losers
			return decide(d, rule, StagePrefixOrWildcardTrailingSlashTrimmed)
		}
	}

	d.Stage = StageDefaultDeny
	d.Allowed = false
	return d, nil
}

// withTemplateUncertainty overrides an otherwise-decided Decision
// because an unresolved identity-template rule could plausibly change
// it once resolved. WinningPattern/Stage/Sources/DeniedBy are left as
// the literal lookup found them — informational context about what
// matched literally — but Allowed and Denied are reset to false, since
// either could be exactly what the template resolution changes.
func withTemplateUncertainty(d Decision, tmpl *compiledRule, path string) (Decision, error) {
	d.Incomplete = true
	d.Allowed = false
	d.Denied = false
	d.IncompleteReason = fmt.Sprintf(
		"path %q contains an unresolved OpenBao identity template that could plausibly match %q once resolved, which could change this result — a higher-priority match, a merge into an identical resolved pattern that changes effective capabilities, or a new deny; bpe does not resolve identity templates, so this cannot be treated as a final answer",
		tmpl.pattern, path,
	)
	return d, fmt.Errorf("%w: %s", ErrIncompleteEvaluation, d.IncompleteReason)
}

// decide fills in d from the winning rule and stage. See Evaluate's doc
// comment for the ordering rationale (templated pattern gates everything
// about this match; deny is absolute; only then does capability grant
// and parameter-constraint completeness matter).
func decide(d Decision, rule *compiledRule, stage MatchStage) (Decision, error) {
	d.WinningPattern = rule.pattern
	d.Stage = stage

	if rule.looksTemplated {
		d.Incomplete = true
		d.IncompleteReason = fmt.Sprintf(
			"path %q looks like it contains an unresolved OpenBao identity template (\"{{\"/\"}}\"); bpe does not resolve identity templates, so this match cannot be trusted",
			rule.pattern,
		)
		return d, fmt.Errorf("%w: %s", ErrIncompleteEvaluation, d.IncompleteReason)
	}

	if rule.effective[policy.CapabilityDeny] {
		d.Denied = true
		d.Allowed = false
		d.DeniedBy = rule.grantingSources[policy.CapabilityDeny]
		return d, nil
	}

	if !rule.effective[d.RequestedCapability] {
		d.Allowed = false
		return d, nil
	}

	if rule.hasParameterConstraints {
		d.Incomplete = true
		d.IncompleteReason = fmt.Sprintf(
			"path %q has required_parameters, allowed_parameters, or denied_parameters set; a path and capability check alone cannot determine whether a concrete request's parameters would satisfy them",
			rule.pattern,
		)
		return d, fmt.Errorf("%w: %s", ErrIncompleteEvaluation, d.IncompleteReason)
	}

	d.Allowed = true
	d.Sources = rule.grantingSources[d.RequestedCapability]
	return d, nil
}
