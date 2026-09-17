package tui

import (
	"fmt"
	"strings"

	"github.com/kevinpinscoe/bao-policy-editor/internal/policy"
)

// labelWidth is the width of the form's label column. It is fixed rather
// than computed so the value column starts in the same place on every row,
// which is what lets the eye scan down it.
const labelWidth = 20

// View renders the rule form.
func (f *ruleForm) View(st Styles, width int) string {
	var b strings.Builder

	title := fmt.Sprintf("Edit rule — %s", displayPath(f.orig.Path))
	if f.isNew {
		title = "Add a rule"
	}
	b.WriteString(st.Title.Render(truncate(title, width)))
	b.WriteString("\n\n")

	for i, row := range f.rows() {
		b.WriteString(f.renderRow(st, row, i == f.cursor, width))
		b.WriteString("\n")
	}

	if unknown := f.unknownCapabilities(); len(unknown) > 0 {
		names := make([]string, len(unknown))
		for i, c := range unknown {
			names[i] = string(c)
		}
		b.WriteString("\n")
		b.WriteString(st.Warning.Render(truncate(
			"also in this rule: "+strings.Join(names, ", ")+
				" — BPE does not recognize these capabilities, and keeps them untouched", width)))
		b.WriteString("\n")
	}

	if len(f.src.Unknown) > 0 || len(f.src.NestedBlocks) > 0 {
		b.WriteString("\n")
		b.WriteString(st.Dim.Render(truncate(preservedNote(f.src.Unknown, f.src.NestedBlocks), width)))
		b.WriteString("\n")
	}

	if f.notice != "" {
		b.WriteString("\n" + st.Warning.Render(truncate("note: "+f.notice, width)) + "\n")
	}
	if f.problem != "" {
		b.WriteString("\n" + st.Error.Render(truncate("cannot apply: "+f.problem, width)) + "\n")
	}

	return b.String()
}

func (f *ruleForm) renderRow(st Styles, row formRow, selected bool, width int) string {
	caret := "  "
	if selected {
		caret = st.Focused.Render("> ")
	}

	label, value := f.rowLabel(row), f.rowValue(st, row, selected, width)
	styledLabel := st.Label.Render(pad(label, labelWidth))
	if selected {
		styledLabel = st.Focused.Render(pad(label, labelWidth))
	}

	line := caret + styledLabel + value
	if lock := f.lockFor(row); lock != "" {
		line += "  " + st.Locked.Render("[read-only: "+lock+"]")
	}
	return line
}

func (f *ruleForm) rowLabel(row formRow) string {
	switch row.kind {
	case rowPath:
		return "path"
	case rowCapability:
		return "  " + string(row.capability)
	case rowComment:
		return "comment"
	case rowAdvanced:
		if f.advanced {
			return "▾ advanced"
		}
		return "▸ advanced"
	case rowExpiration:
		return "  expiration"
	case rowRequired:
		return "  required params"
	case rowAllowed:
		return "  allowed params"
	case rowDenied:
		return "  denied params"
	default:
		return ""
	}
}

func (f *ruleForm) rowValue(st Styles, row formRow, selected bool, width int) string {
	valueWidth := max(10, width-labelWidth-4)

	switch row.kind {
	case rowPath:
		if f.editing && selected {
			return f.path.View()
		}
		return truncate(displayPath(f.path.Value()), valueWidth)

	case rowCapability:
		return f.renderCapability(st, row.capability)

	case rowComment:
		return f.renderOptionalText(st, f.comment, selected, valueWidth)

	case rowAdvanced:
		return st.Dim.Render(truncate(f.advancedSummary(), valueWidth))

	case rowExpiration:
		return f.renderOptionalText(st, f.expiration, selected, valueWidth)

	case rowRequired:
		return f.renderOptionalText(st, f.required, selected, valueWidth)

	case rowAllowed:
		return f.renderParams(st, f.allowed, "allowed", valueWidth)

	case rowDenied:
		return f.renderParams(st, f.denied, "denied", valueWidth)
	}
	return ""
}

// renderCapability draws a checkbox that says what it means in text as
// well as in color, so the interface still reads with NO_COLOR set.
func (f *ruleForm) renderCapability(st Styles, c policy.Capability) string {
	box := "[ ]"
	if f.caps[c] {
		box = "[x]"
	}
	rendered := box
	switch {
	case c == policy.CapabilityDeny && f.caps[c]:
		rendered = st.Deny.Render(box + "  denies this path outright")
	case c == policy.CapabilitySudo && f.caps[c]:
		rendered = st.Warning.Render(box + "  root-protected paths")
	case f.caps[c]:
		rendered = st.Success.Render(box)
	default:
		rendered = st.Dim.Render(box)
	}
	return rendered
}

func (f *ruleForm) renderOptionalText(st Styles, field optionalText, selected bool, width int) string {
	if f.editing && selected {
		return field.input.View()
	}
	if !field.set {
		return st.Dim.Render("(not set)")
	}
	if field.value == "" {
		return st.Dim.Render(`"" (set, but empty)`)
	}
	return truncate(field.value, width)
}

func (f *ruleForm) renderParams(st Styles, field optionalParams, kind string, width int) string {
	if !field.set {
		return st.Dim.Render("(not set)")
	}
	if len(field.values) == 0 {
		return st.Dim.Render("{} (set, but empty)")
	}
	names := make([]string, 0, len(field.values))
	for _, item := range field.values {
		names = append(names, item.Name)
	}
	summary := fmt.Sprintf("%d %s: %s", len(field.values), pluralize("constraint", len(field.values)), strings.Join(names, ", "))
	return truncate(summary, width)
}

// advancedSummary is what the collapsed advanced row says. It names what
// is behind it rather than merely offering to expand, so a rule's
// constraints are never invisible — the build brief's "visible summary
// such as 3 parameter constraints".
func (f *ruleForm) advancedSummary() string {
	var parts []string
	if f.expiration.set {
		parts = append(parts, "expiration set")
	}
	if f.required.set {
		parts = append(parts, fmt.Sprintf("%d required", len(f.requiredList())))
	}
	if f.allowed.set {
		parts = append(parts, fmt.Sprintf("%d allowed", len(f.allowed.values)))
	}
	if f.denied.set {
		parts = append(parts, fmt.Sprintf("%d denied", len(f.denied.values)))
	}
	if len(parts) == 0 {
		return "nothing set — expiration and parameter constraints"
	}
	return strings.Join(parts, " · ")
}

func preservedNote(unknown, nested []string) string {
	var parts []string
	if len(unknown) > 0 {
		parts = append(parts, fmt.Sprintf("%s %s", pluralize("attribute", len(unknown)), strings.Join(unknown, ", ")))
	}
	if len(nested) > 0 {
		parts = append(parts, fmt.Sprintf("nested %s %s", pluralize("block", len(nested)), strings.Join(nested, ", ")))
	}
	return "this rule also carries " + strings.Join(parts, " and ") +
		", which BPE does not model but preserves exactly"
}

func pluralize(word string, n int) string {
	if n == 1 {
		return word
	}
	return word + "s"
}

// displayPath renders an empty path visibly, so a rule with no path does
// not look like a rendering fault.
func displayPath(path string) string {
	if path == "" {
		return "(no path)"
	}
	return path
}
