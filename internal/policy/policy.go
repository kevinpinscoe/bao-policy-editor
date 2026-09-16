// Package policy defines BPE's UI-independent OpenBao ACL policy domain
// model. It has no dependency on Bubble Tea, the filesystem, or the
// OpenBao API — see the architecture note in README.md — so it can be
// constructed, inspected, and compared purely in memory. Translating
// between this model and HCL source is internal/hclpolicy's job, not
// this package's.
package policy

import (
	"slices"
	"time"
)

// Capability is one OpenBao ACL capability that a path rule can grant or
// deny.
type Capability string

// The capabilities OpenBao ACL policies recognize.
const (
	CapabilityCreate Capability = "create"
	CapabilityRead   Capability = "read"
	CapabilityUpdate Capability = "update"
	CapabilityPatch  Capability = "patch"
	CapabilityDelete Capability = "delete"
	CapabilityList   Capability = "list"
	CapabilityScan   Capability = "scan"
	CapabilitySudo   Capability = "sudo"
	CapabilityDeny   Capability = "deny"
)

// Capabilities lists every capability BPE recognizes, in the order the
// TUI presents them (see the build brief's TUI requirements).
var Capabilities = []Capability{
	CapabilityCreate,
	CapabilityRead,
	CapabilityUpdate,
	CapabilityPatch,
	CapabilityDelete,
	CapabilityList,
	CapabilityScan,
	CapabilitySudo,
	CapabilityDeny,
}

// Known reports whether c is one of the capabilities OpenBao recognizes.
// Deciding what to do with an unknown capability (reject it, warn about
// it) is policy validation's job — FSM-12 — not this package's; Known is
// exposed so that later ticket can ask the question without duplicating
// the capability list.
func (c Capability) Known() bool {
	return slices.Contains(Capabilities, c)
}

// ParameterValues associates a request-parameter name with the specific
// values a rule's required/allowed/denied-parameters constraint applies
// to. An empty Values means "any value" for that parameter name, matching
// OpenBao's own convention of mapping a parameter name to an empty list
// in allowed_parameters/denied_parameters.
type ParameterValues struct {
	Name   string
	Values []string
}

// Rule is a single `path` block: an OpenBao path pattern and the access
// it grants or restricts.
type Rule struct {
	// Path is the OpenBao path pattern this rule matches, exactly as
	// written — including any trailing "*" or "+" wildcard segments.
	Path string

	// Capabilities are the capabilities this rule grants or denies, in
	// the order they appeared in the source.
	Capabilities []Capability

	// Comment is the rule's optional documentation string, from the
	// `comment` attribute.
	Comment string

	// Expiration is the rule's optional expiration timestamp, from the
	// `expiration` attribute. Nil means no expiration was set.
	Expiration *time.Time

	// RequiredParameters are request parameters that must be present for
	// this rule to apply, from `required_parameters`.
	RequiredParameters []string

	// AllowedParameters and DeniedParameters constrain which values a
	// request parameter may or may not take, from `allowed_parameters`
	// and `denied_parameters`.
	AllowedParameters []ParameterValues
	DeniedParameters  []ParameterValues
}

// HasCapability reports whether the rule includes c among its
// capabilities.
func (r Rule) HasCapability(c Capability) bool {
	return slices.Contains(r.Capabilities, c)
}

// Policy is a full OpenBao ACL policy: an ordered list of path rules, in
// the order they appeared in the source file.
type Policy struct {
	Rules []Rule
}
