package hclpolicy

import (
	"time"

	"github.com/hashicorp/hcl/v2/hclwrite"
	"github.com/zclconf/go-cty/cty"

	"github.com/kevinpinscoe/bao-policy-editor/internal/policy"
)

// Encode generates canonical, deterministic HCL for p. It builds fresh
// hclwrite tokens directly from the domain model, so it is only
// appropriate for a Policy that came from a Document with
// Unsupported == false — a Policy decoded from a Document with unsupported
// content reflects only part of that file, and Encode has no way to know
// what the rest was. Round-tripping a parsed file, including one with
// unsupported content, is Document.Bytes' job, not this function's.
//
// Determinism relies on cty's own map/object key ordering, which iterates
// in sorted key order (verified against the go-cty v1.19.0 source used by
// this module) — so ParameterValues, which this package stores as an
// ordered slice, is converted to a cty object whose emitted attribute
// order is alphabetical regardless of slice order, and capability/
// parameter lists keep the domain model's own slice order since HCL lists
// are positional rather than keyed.
func Encode(p policy.Policy) ([]byte, error) {
	f := hclwrite.NewEmptyFile()
	body := f.Body()

	for i, rule := range p.Rules {
		if i > 0 {
			body.AppendNewline()
		}
		encodeRule(body.AppendNewBlock("path", []string{rule.Path}).Body(), rule)
	}

	return f.Bytes(), nil
}

func encodeRule(body *hclwrite.Body, rule policy.Rule) {
	if len(rule.Capabilities) > 0 {
		body.SetAttributeValue("capabilities", stringsToCty(capabilitiesToStrings(rule.Capabilities)))
	}
	if rule.Comment != "" {
		body.SetAttributeValue("comment", cty.StringVal(rule.Comment))
	}
	if rule.Expiration != nil {
		body.SetAttributeValue("expiration", cty.StringVal(rule.Expiration.UTC().Format(time.RFC3339)))
	}
	if len(rule.RequiredParameters) > 0 {
		body.SetAttributeValue("required_parameters", stringsToCty(rule.RequiredParameters))
	}
	if len(rule.AllowedParameters) > 0 {
		body.SetAttributeValue("allowed_parameters", parameterMapToCty(rule.AllowedParameters))
	}
	if len(rule.DeniedParameters) > 0 {
		body.SetAttributeValue("denied_parameters", parameterMapToCty(rule.DeniedParameters))
	}
}

func capabilitiesToStrings(caps []policy.Capability) []string {
	out := make([]string, len(caps))
	for i, c := range caps {
		out[i] = string(c)
	}
	return out
}

func stringsToCty(values []string) cty.Value {
	vals := make([]cty.Value, len(values))
	for i, v := range values {
		vals[i] = cty.StringVal(v)
	}
	return cty.ListVal(vals)
}

func parameterMapToCty(params []policy.ParameterValues) cty.Value {
	attrs := make(map[string]cty.Value, len(params))
	for _, p := range params {
		if len(p.Values) == 0 {
			// An empty list means "any value" — encode as an empty list
			// of strings, matching OpenBao's own convention, rather than
			// an empty tuple that cty would otherwise infer.
			attrs[p.Name] = cty.ListValEmpty(cty.String)
			continue
		}
		attrs[p.Name] = stringsToCty(p.Values)
	}
	return cty.ObjectVal(attrs)
}
