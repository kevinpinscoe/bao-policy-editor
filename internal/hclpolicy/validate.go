package hclpolicy

import (
	"fmt"
	"strings"
	"time"

	"github.com/kevinpinscoe/bao-policy-editor/internal/policy"
)

// kv2Components are the API path components OpenBao's KV v2 secrets
// engine recognizes beneath a mount (e.g. "secret/data/foo",
// "secret/metadata/foo"). A rule whose second path segment is one of
// these is treated as evidence that the mount it lives under is being
// addressed as KV v2 — see kvV2Diagnostics.
var kv2Components = map[string]bool{
	"data":     true,
	"metadata": true,
	"delete":   true,
	"undelete": true,
	"destroy":  true,
}

// Validate runs FSM-12's semantic checks against a decoded policy and
// returns every finding, in a stable, deterministic order: policy-wide
// checks first (empty policy, duplicate paths, KV v2 path-shape
// evidence), then per-rule checks in the rules' source order. It has no
// dependency on a source file or its positions — see (*Document).Validate
// for the filename-stamping convenience wrapper CLI/TUI callers actually
// want, and Diagnostic.String() for why a Validate finding prints without
// a line:column.
//
// Validate never reports a syntax problem — that is Parse's job
// (decode.go) — and separates its own findings into two severities:
//
//   - SeverityError: this specific rule is provably broken. Either it can
//     never do anything useful (e.g. an empty path, or a required
//     parameter that no value can ever satisfy — see
//     requiredParameterContradiction), or OpenBao's own ACL policy parser
//     rejects the file outright as written (an unknown capability — see
//     the citation on that check below). The build brief's rule is
//     "warnings must not prevent saving unless the policy is
//     syntactically invalid or serialization would lose data"; these
//     errors are the semantic equivalent of "syntactically invalid" for
//     the purposes of that rule, so a caller (the CLI's `validate`
//     command, and later the TUI's save gate) is expected to block on
//     them.
//   - SeverityWarning: worth a human's attention, but never proof the
//     rule is broken — must never block saving. This includes every
//     heuristic check (KV v2 path shape, list/scan-on-a-non-prefix): they
//     reason only from path shape within this one policy file, not from
//     real OpenBao mount configuration, and are documented as such at
//     each check below.
func Validate(pol policy.Policy) []Diagnostic {
	var diags []Diagnostic

	if len(pol.Rules) == 0 {
		diags = append(diags, Diagnostic{
			Severity:    SeverityWarning,
			Summary:     "policy contains no path rules",
			Detail:      "an OpenBao policy with no path blocks denies everything by default, which may be intentional (e.g. a deliberately empty starting point)",
			Remediation: "add path rules if this policy is meant to grant any access",
		})
	}

	diags = append(diags, duplicatePathDiagnostics(pol.Rules)...)
	diags = append(diags, kvV2Diagnostics(pol.Rules)...)

	for _, r := range pol.Rules {
		diags = append(diags, validateRule(r)...)
	}

	return diags
}

// Validate runs Validate(d.Policy) and stamps d.Filename onto every
// result, so a semantic finding carries a filename the same way a
// decode-time one already does (Diagnostic.String() only ever omits
// position, never filename).
func (d *Document) Validate() []Diagnostic {
	diags := Validate(d.Policy)
	for i := range diags {
		diags[i].Filename = d.Filename
	}
	return diags
}

// duplicatePathDiagnostics flags every occurrence of a path pattern
// beyond its first in rules — the build brief's "Duplicate path blocks".
func duplicatePathDiagnostics(rules []policy.Rule) []Diagnostic {
	var diags []Diagnostic
	seen := make(map[string]int, len(rules))
	for _, r := range rules {
		seen[r.Path]++
		if seen[r.Path] > 1 {
			diags = append(diags, Diagnostic{
				Severity:    SeverityWarning,
				Path:        r.Path,
				Summary:     fmt.Sprintf("path %q is defined more than once in this policy", r.Path),
				Detail:      fmt.Sprintf("this is occurrence %d of a path block for %q", seen[r.Path], r.Path),
				Remediation: "merge the duplicate path blocks into one, or give each a distinct path",
			})
		}
	}
	return diags
}

// kvV2Diagnostics implements the build brief's "Common KV v2 mistakes
// involving missing data/ or metadata/ components" — deliberately scoped
// to a same-policy heuristic, not real mount configuration (Kevin's
// instruction, 2026-09-17): "secret/" is not assumed to be KV v2 on its
// own, since it can be KV v1 or another engine entirely. A mount is only
// treated as "evidently KV v2" when another rule in this same policy
// already addresses it through one of KV v2's own API components (data/,
// metadata/, delete/, undelete/, destroy/); only then are that mount's
// other rules checked for a plausibly-missing component. A future ticket
// with real OpenBao connectivity can replace this with actual mount-type
// discovery — this one does not attempt it.
func kvV2Diagnostics(rules []policy.Rule) []Diagnostic {
	mountsWithEvidence := make(map[string]bool)
	for _, r := range rules {
		mount, component, ok := splitMountComponent(r.Path)
		if ok && kv2Components[component] {
			mountsWithEvidence[mount] = true
		}
	}

	var diags []Diagnostic
	for _, r := range rules {
		mount, component, ok := splitMountComponent(r.Path)
		if !ok || !mountsWithEvidence[mount] || kv2Components[component] {
			continue
		}
		// A wildcard segment ("secret/*" or "secret/+/...") is a
		// deliberately broad grant, not a v1-style naming mistake —
		// excluded rather than flagged.
		if component == "*" || component == "+" {
			continue
		}
		diags = append(diags, Diagnostic{
			Severity: SeverityWarning,
			Path:     r.Path,
			Summary:  fmt.Sprintf("path %q may be missing a KV v2 API component", r.Path),
			Detail: fmt.Sprintf(
				"other rules in this policy address mount %q as a KV v2 secrets engine (via data/, metadata/, delete/, undelete/, or destroy/); this is a heuristic based only on path shape within this policy file, not on the mount's actual configuration — verify against the real mount type before relying on it",
				mount,
			),
			Remediation: fmt.Sprintf("if %q is genuinely a KV v2 mount, address secret data via %s/data/... and metadata via %s/metadata/...", mount, mount, mount),
		})
	}
	return diags
}

// splitMountComponent splits path into its first segment (the mount, by
// convention) and second segment (the API component, for an engine like
// KV v2 that namespaces its own API under the mount). ok is false when
// path has fewer than two segments, since there is then no component to
// examine.
func splitMountComponent(path string) (mount, component string, ok bool) {
	segs := strings.SplitN(path, "/", 3)
	if len(segs) < 2 {
		return "", "", false
	}
	return segs[0], segs[1], true
}

// validateRule runs every check scoped to a single rule.
func validateRule(r policy.Rule) []Diagnostic {
	var diags []Diagnostic

	if r.Path == "" {
		diags = append(diags, Diagnostic{
			Severity:    SeverityError,
			Summary:     "a path block has an empty path pattern",
			Detail:      "an empty path cannot usefully match any real OpenBao request",
			Remediation: "give this path block a real path pattern, or remove it",
		})
	}

	if len(r.Capabilities) == 0 {
		diags = append(diags, Diagnostic{
			Severity:    SeverityWarning,
			Path:        r.Path,
			Summary:     fmt.Sprintf("path %q has no capabilities", r.Path),
			Detail:      "a path block with no capabilities grants no access and has no effect",
			Remediation: "add at least one capability, or remove this path block",
		})
	}

	for _, c := range r.Capabilities {
		if !c.Known() {
			diags = append(diags, Diagnostic{
				Severity: SeverityError,
				Path:     r.Path,
				Summary:  fmt.Sprintf("path %q uses unknown capability %q", r.Path, c),
				Detail: "OpenBao's ACL policy parser rejects an unrecognized capability outright (\"invalid capability\") rather than ignoring it, so a policy containing this cannot be applied as written — " +
					"see openbao/openbao's internal/vault/policy/policy.go, parsePath: `default: return nil, fmt.Errorf(\"path %q: invalid capability %q\", key, cap)`",
				Remediation: fmt.Sprintf("use one of OpenBao's recognized capabilities: %s", knownCapabilitiesList()),
			})
		}
	}

	if r.HasCapability(policy.CapabilityDeny) && len(r.Capabilities) > 1 {
		diags = append(diags, Diagnostic{
			Severity: SeverityWarning,
			Path:     r.Path,
			Summary:  fmt.Sprintf("path %q combines deny with other capabilities", r.Path),
			Detail: "OpenBao's parser rewrites this path's capabilities to just [\"deny\"] the moment deny appears and stops processing the rest of the list, so the other capabilities listed here are silently dropped, not merely lower priority — " +
				"see openbao/openbao's internal/vault/policy/policy.go, parsePath (the DenyCapability case sets pc.Capabilities = []string{DenyCapability} and jumps to PathFinished)",
			Remediation: "remove deny to grant the other capabilities, or remove the other capabilities since deny alone already has the same effect",
		})
	}

	if r.HasCapability(policy.CapabilitySudo) {
		diags = append(diags, Diagnostic{
			Severity:    SeverityWarning,
			Path:        r.Path,
			Summary:     fmt.Sprintf("path %q grants sudo", r.Path),
			Detail:      "sudo elevates access to sensitive or root-protected operations at this path",
			Remediation: "confirm this path genuinely needs sudo-gated operations before shipping this policy",
		})
	}

	if r.HasCapability(policy.CapabilityCreate) && !r.HasCapability(policy.CapabilityUpdate) {
		diags = append(diags, Diagnostic{
			Severity:    SeverityWarning,
			Path:        r.Path,
			Summary:     fmt.Sprintf("path %q grants create without update", r.Path),
			Detail:      "a caller can create new data at this path but can never modify it afterward, which is often unintentional",
			Remediation: "add update if callers should be able to modify what they create, or confirm write-once semantics are intended",
		})
	}

	if r.HasCapability(policy.CapabilityList) || r.HasCapability(policy.CapabilityScan) {
		if !strings.HasSuffix(r.Path, "/") && !strings.HasSuffix(r.Path, "*") {
			diags = append(diags, Diagnostic{
				Severity:    SeverityWarning,
				Path:        r.Path,
				Summary:     fmt.Sprintf("path %q grants list or scan but does not look like a path prefix", r.Path),
				Detail:      "list and scan are typically used against directory-like paths ending in \"/\" or \"*\"; this is a heuristic based on path shape only, not confirmation that the path is wrong, and must never block saving",
				Remediation: "if this path is meant to enumerate entries beneath it, consider ending it with \"/\" or \"*\"; otherwise list/scan here may never match a real request",
			})
		}
	}

	if idx := strings.IndexByte(r.Path, '*'); idx >= 0 && idx != len(r.Path)-1 {
		diags = append(diags, Diagnostic{
			Severity:    SeverityWarning,
			Path:        r.Path,
			Summary:     fmt.Sprintf("path %q uses \"*\" before the end of the pattern", r.Path),
			Detail:      "OpenBao only treats a trailing \"*\" as a glob wildcard; a \"*\" anywhere else in the pattern is matched literally",
			Remediation: "move the wildcard to the end of the path, or use \"+\" for a single-segment wildcard elsewhere in the pattern",
		})
	}

	if strings.HasPrefix(r.Path, "sys/") && strings.HasSuffix(r.Path, "*") {
		diags = append(diags, Diagnostic{
			Severity:    SeverityWarning,
			Path:        r.Path,
			Summary:     fmt.Sprintf("path %q grants broad access under sys/", r.Path),
			Detail:      "sys/ hosts OpenBao's administrative and system endpoints; a wide wildcard here grants far more than most callers need",
			Remediation: "narrow this to the specific sys/ subpaths this rule actually needs",
		})
	}

	if r.Expiration != nil && r.Expiration.Before(time.Now()) {
		diags = append(diags, Diagnostic{
			Severity:    SeverityWarning,
			Path:        r.Path,
			Summary:     fmt.Sprintf("path %q's expiration %s is in the past", r.Path, r.Expiration.Format(time.RFC3339)),
			Detail:      "this rule's expiration timestamp has already passed",
			Remediation: "update or remove the expiration if this rule is meant to still be active",
		})
	}

	for _, name := range r.RequiredParameters {
		if reason, contradictory := requiredParameterContradiction(r, name); contradictory {
			diags = append(diags, Diagnostic{
				Severity:    SeverityError,
				Path:        r.Path,
				Summary:     fmt.Sprintf("path %q requires parameter %q that can never be supplied", r.Path, name),
				Detail:      reason,
				Remediation: "adjust required_parameters, allowed_parameters, or denied_parameters so at least one valid value actually exists",
			})
		}
	}

	return diags
}

// requiredParameterContradiction reports whether r's requirement that
// name be present in a request can be *proven* impossible to satisfy,
// given allowed_parameters/denied_parameters. It follows OpenBao's own
// documented parameter-constraint semantics — an empty value list in
// denied_parameters denies every value of that parameter, while a
// populated list denies only the listed values, leaving any other value
// valid (https://openbao.org/docs/concepts/policies/#parameter-constraints)
// — and reports a contradiction only for the following provable cases
// (Kevin's instruction, 2026-09-17):
//
//  1. denied_parameters[name] is present with an empty value list — every
//     value of the required parameter is denied.
//  2. denied_parameters["*"] is present with an empty value list, and
//     name has no more specific entry of its own — every value of every
//     parameter, including this required one, is denied.
//  3. allowed_parameters is present (a whitelist), and name has neither
//     its own entry nor a "*" entry — the required parameter can never be
//     supplied at all, since it is not on the whitelist.
//  4. Both allowed_parameters and denied_parameters restrict name (or
//     fall back to a "*" entry) to specific, non-empty value lists, and
//     every value the allowed list permits is also in the denied list —
//     no value is both permitted and not denied.
//
// A merely partial overlap — some but not all allowed values also
// denied, or a denial that does not exhaust the allowed set — is never
// reported: another value remains valid, so the rule is not actually
// unsatisfiable. See validate_test.go for cases that stay satisfiable on
// exactly this basis.
func requiredParameterContradiction(r policy.Rule, name string) (reason string, contradictory bool) {
	denied, deniedFound := lookupParameter(r.DeniedParameters, name)
	deniedWildcard, deniedWildcardFound := lookupParameter(r.DeniedParameters, "*")

	if deniedFound && len(denied.Values) == 0 {
		return fmt.Sprintf("denied_parameters[%q] lists no values, which denies every value of %q", name, name), true
	}
	if !deniedFound && deniedWildcardFound && len(deniedWildcard.Values) == 0 {
		return fmt.Sprintf(`denied_parameters["*"] lists no values, which denies every value of every parameter, including required parameter %q`, name), true
	}

	allowed, allowedFound := lookupParameter(r.AllowedParameters, name)
	allowedWildcard, allowedWildcardFound := lookupParameter(r.AllowedParameters, "*")

	if r.AllowedParameters != nil && !allowedFound && !allowedWildcardFound {
		return fmt.Sprintf("allowed_parameters is set but lists neither %q nor a \"*\" wildcard, so required parameter %q can never be supplied", name, name), true
	}

	var allowedValues []string
	switch {
	case allowedFound && len(allowed.Values) > 0:
		allowedValues = allowed.Values
	case !allowedFound && allowedWildcardFound && len(allowedWildcard.Values) > 0:
		allowedValues = allowedWildcard.Values
	}

	var deniedValues []string
	switch {
	case deniedFound && len(denied.Values) > 0:
		deniedValues = denied.Values
	case !deniedFound && deniedWildcardFound && len(deniedWildcard.Values) > 0:
		deniedValues = deniedWildcard.Values
	}

	if len(allowedValues) > 0 && len(deniedValues) > 0 && isSubset(allowedValues, deniedValues) {
		return fmt.Sprintf(
			"every value allowed_parameters permits for %q (%s) is also listed in denied_parameters (%s)",
			name, strings.Join(allowedValues, ", "), strings.Join(deniedValues, ", "),
		), true
	}

	return "", false
}

func lookupParameter(params []policy.ParameterValues, name string) (policy.ParameterValues, bool) {
	for _, p := range params {
		if p.Name == name {
			return p, true
		}
	}
	return policy.ParameterValues{}, false
}

// isSubset reports whether every element of a also appears in b.
func isSubset(a, b []string) bool {
	set := make(map[string]bool, len(b))
	for _, v := range b {
		set[v] = true
	}
	for _, v := range a {
		if !set[v] {
			return false
		}
	}
	return true
}

func knownCapabilitiesList() string {
	names := make([]string, len(policy.Capabilities))
	for i, c := range policy.Capabilities {
		names[i] = string(c)
	}
	return strings.Join(names, ", ")
}
