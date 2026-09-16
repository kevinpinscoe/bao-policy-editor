package hclpolicy

import (
	"fmt"
	"time"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/zclconf/go-cty/cty"

	"github.com/kevinpinscoe/bao-policy-editor/internal/policy"
)

// decoder carries the state needed while walking one parsed file: the
// filename (for diagnostics that don't already have one attached) and the
// diagnostics accumulated so far.
type decoder struct {
	filename    string
	diagnostics []Diagnostic
}

func (d *decoder) errorf(rng hcl.Range, summary string, args ...any) {
	d.diagnostics = append(d.diagnostics, diagnosticFromHCL(SeverityError, fmt.Sprintf(summary, args...), "", &rng))
}

// decodeFile decodes every `path` block in body into a policy.Policy. It
// returns unsupported = true if the file contains a top-level block of
// any type other than `path`, or a top-level attribute (OpenBao policy
// files have none), since FSM-11's domain model has no representation for
// either.
func (d *decoder) decodeFile(body *hclsyntax.Body) (policy.Policy, bool) {
	var pol policy.Policy
	unsupported := false

	for name, attr := range body.Attributes {
		d.diagnostics = append(d.diagnostics, diagnosticFromHCL(
			SeverityWarning,
			fmt.Sprintf("unsupported top-level attribute %q", name),
			"BPE's policy model only represents `path` blocks; this attribute is preserved in the file but not reflected in the decoded policy",
			ptr(attr.SrcRange),
		))
		unsupported = true
	}

	for _, block := range body.Blocks {
		if block.Type != "path" {
			d.diagnostics = append(d.diagnostics, diagnosticFromHCL(
				SeverityWarning,
				fmt.Sprintf("unsupported block type %q", block.Type),
				"BPE's policy model only represents `path` blocks; this block is preserved in the file but not reflected in the decoded policy",
				ptr(block.TypeRange),
			))
			unsupported = true
			continue
		}

		rule, ruleUnsupported := d.decodeRule(block)
		pol.Rules = append(pol.Rules, rule)
		if ruleUnsupported {
			unsupported = true
		}
	}

	return pol, unsupported
}

// decodeRule decodes a single `path "..." { ... }` block.
func (d *decoder) decodeRule(block *hclsyntax.Block) (policy.Rule, bool) {
	var rule policy.Rule
	unsupported := false

	if len(block.Labels) != 1 {
		d.errorf(block.TypeRange, "`path` block must have exactly one label (the path pattern), got %d", len(block.Labels))
	} else {
		rule.Path = block.Labels[0]
	}

	for name, attr := range block.Body.Attributes {
		if !knownPathAttributes[name] {
			d.diagnostics = append(d.diagnostics, diagnosticFromHCL(
				SeverityWarning,
				fmt.Sprintf("unsupported attribute %q in path %q", name, rule.Path),
				"this attribute is preserved in the file but not reflected in the decoded policy",
				ptr(attr.SrcRange),
			))
			unsupported = true
			continue
		}

		d.decodeAttribute(&rule, name, attr)
	}

	for _, nested := range block.Body.Blocks {
		d.diagnostics = append(d.diagnostics, diagnosticFromHCL(
			SeverityWarning,
			fmt.Sprintf("unsupported nested block %q in path %q", nested.Type, rule.Path),
			"this block is preserved in the file but not reflected in the decoded policy",
			ptr(nested.TypeRange),
		))
		unsupported = true
	}

	return rule, unsupported
}

func (d *decoder) decodeAttribute(rule *policy.Rule, name string, attr *hclsyntax.Attribute) {
	val, diags := attr.Expr.Value(nil)
	if diags.HasErrors() {
		d.diagnostics = append(d.diagnostics, diagnosticsFromHCL(diags)...)
		return
	}
	if val.IsNull() {
		d.errorf(attr.SrcRange, "%q must not be null", name)
		return
	}

	switch name {
	case "capabilities":
		values, ok := d.decodeStringList(attr.SrcRange, name, val)
		if !ok {
			return
		}
		rule.Capabilities = make([]policy.Capability, len(values))
		for i, v := range values {
			rule.Capabilities[i] = policy.Capability(v)
		}

	case "comment":
		s, ok := d.decodeString(attr.SrcRange, name, val)
		if !ok {
			return
		}
		rule.Comment = s

	case "expiration":
		s, ok := d.decodeString(attr.SrcRange, name, val)
		if !ok {
			return
		}
		t, err := time.Parse(time.RFC3339, s)
		if err != nil {
			d.errorf(attr.SrcRange, "invalid expiration timestamp %q: must be RFC 3339 (e.g. 2026-01-02T15:04:05Z)", s)
			return
		}
		rule.Expiration = &t

	case "required_parameters":
		values, ok := d.decodeStringList(attr.SrcRange, name, val)
		if !ok {
			return
		}
		rule.RequiredParameters = values

	case "allowed_parameters":
		values, ok := d.decodeParameterMap(attr.SrcRange, name, val)
		if !ok {
			return
		}
		rule.AllowedParameters = values

	case "denied_parameters":
		values, ok := d.decodeParameterMap(attr.SrcRange, name, val)
		if !ok {
			return
		}
		rule.DeniedParameters = values
	}
}

func (d *decoder) decodeString(rng hcl.Range, attrName string, val cty.Value) (string, bool) {
	if val.Type() != cty.String {
		d.errorf(rng, "%q must be a string, got %s", attrName, val.Type().FriendlyName())
		return "", false
	}
	return val.AsString(), true
}

// decodeStringList decodes a list/tuple/set of strings, as used by
// `capabilities` and `required_parameters`.
func (d *decoder) decodeStringList(rng hcl.Range, attrName string, val cty.Value) ([]string, bool) {
	t := val.Type()
	if !t.IsTupleType() && !t.IsListType() && !t.IsSetType() {
		d.errorf(rng, "%q must be a list of strings, got %s", attrName, t.FriendlyName())
		return nil, false
	}

	elems := val.AsValueSlice()
	out := make([]string, 0, len(elems))
	for _, elem := range elems {
		if elem.Type() != cty.String {
			d.errorf(rng, "%q must be a list of strings, but found a %s element", attrName, elem.Type().FriendlyName())
			return nil, false
		}
		out = append(out, elem.AsString())
	}
	return out, true
}

// decodeParameterMap decodes an object/map from parameter name to a list
// of strings, as used by `allowed_parameters` and `denied_parameters`. An
// empty list for a key means "any value" for that parameter name,
// matching OpenBao's own convention.
func (d *decoder) decodeParameterMap(rng hcl.Range, attrName string, val cty.Value) ([]policy.ParameterValues, bool) {
	t := val.Type()
	if !t.IsObjectType() && !t.IsMapType() {
		d.errorf(rng, "%q must be a map of parameter name to a list of strings, got %s", attrName, t.FriendlyName())
		return nil, false
	}

	valueMap := val.AsValueMap()
	out := make([]policy.ParameterValues, 0, len(valueMap))
	for name, v := range valueMap {
		values, ok := d.decodeStringList(rng, fmt.Sprintf("%s.%s", attrName, name), v)
		if !ok {
			return nil, false
		}
		out = append(out, policy.ParameterValues{Name: name, Values: values})
	}
	return out, true
}

func ptr[T any](v T) *T { return &v }
