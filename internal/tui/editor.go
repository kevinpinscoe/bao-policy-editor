package tui

import (
	"fmt"
	"strings"
	"time"

	"charm.land/lipgloss/v2"

	"github.com/kevinpinscoe/bao-policy-editor/internal/hclpolicy"
	"github.com/kevinpinscoe/bao-policy-editor/internal/policy"
)

// narrowWidth is the point below which the editor stops trying to show the
// rule list and the rule details side by side and stacks them instead.
//
// The build brief requires the interface to stay usable in a narrow
// terminal. Two columns in 70 cells means two unreadable columns, so the
// layout changes shape rather than shrinking past legibility.
const narrowWidth = 80

// listColumnWidth is how much of a wide terminal the rule list takes.
const listColumnWidth = 44

// renderEditor draws the main screen: the rule list and the selected
// rule's details. It returns the content and the screen row the first rule
// occupies, which is what lets a mouse click pick a rule.
func renderEditor(st Styles, s *Session, selected, height, width int) (string, int) {
	header := renderHeader(st, s, width)
	headerLines := strings.Count(header, "\n") + 1

	bodyHeight := max(3, height-headerLines-2)

	if width < narrowWidth {
		// Stacked: the list gets the top half, the details the rest.
		listHeight := max(3, bodyHeight/2)
		list := renderRuleList(st, s, selected, listHeight, width)
		details := renderRuleDetails(st, s, selected, width)
		// A blank line between them: stacked, there is no vertical rule to
		// separate the list from the details, and without it the first
		// detail row reads as another entry in the list.
		return header + "\n" + list + "\n\n" + details, headerLines + 1
	}

	detailWidth := width - listColumnWidth - 3
	list := renderRuleList(st, s, selected, bodyHeight, listColumnWidth)
	details := renderRuleDetails(st, s, selected, detailWidth)

	body := lipgloss.JoinHorizontal(
		lipgloss.Top,
		lipgloss.NewStyle().Width(listColumnWidth).Render(list),
		lipgloss.NewStyle().Width(3).Render(" │ "),
		lipgloss.NewStyle().Width(detailWidth).Render(details),
	)
	return header + "\n" + body, headerLines + 2
}

// renderHeader names the document and its state. Every piece of state here
// is a word, not a color: "modified", "read-only", "2 errors" — so the
// header carries the same information with NO_COLOR set.
func renderHeader(st Styles, s *Session, width int) string {
	name := s.Filename()

	// Where the document lives leads the flag list, spelled out, because
	// "modified" means two very different things depending on the answer —
	// an unsaved file, or a policy a server is still serving the old
	// version of. It is a word rather than a colour so the distinction
	// survives NO_COLOR. Kevin's instruction, 2026-09-18.
	origin := st.Dim.Render(s.Origin())
	if s.IsRemote() {
		origin = st.Subtitle.Render(s.Origin())
	}
	flags := []string{origin}

	if s.Dirty() {
		flags = append(flags, st.Warning.Render("modified"))
	} else {
		flags = append(flags, st.Dim.Render("saved"))
	}
	if !s.Editable() {
		flags = append(flags, st.Error.Render("read-only"))
	}
	if n := s.ErrorCount(); n > 0 {
		flags = append(flags, st.Error.Render(fmt.Sprintf("%d %s", n, pluralize("error", n))))
	}
	if n := s.WarningCount(); n > 0 {
		flags = append(flags, st.Warning.Render(fmt.Sprintf("%d %s", n, pluralize("warning", n))))
	}

	rules := s.Document().RuleCount()
	flags = append(flags, st.Dim.Render(fmt.Sprintf("%d %s", rules, pluralize("rule", rules))))

	// The filename is truncated from the left: the tail names the file,
	// and a path's leading directories are the part nobody is reading.
	line := st.Title.Render(truncateLeft(name, max(10, width-40))) + "  " + strings.Join(flags, st.Dim.Render(" · "))
	out := truncate(line, width)

	if reason := s.ReadOnlyReason(); reason != "" {
		out += "\n" + st.Error.Render(truncate(reason, width))
	}
	return out
}

// renderRuleList draws the rule list, scrolled so the selection stays
// visible.
func renderRuleList(st Styles, s *Session, selected, height, width int) string {
	rules := s.Document().Policy.Rules
	if len(rules) == 0 {
		return st.Dim.Render(truncate("no rules yet — press a to add one", width))
	}

	start := scrollStart(selected, len(rules), height)
	end := min(len(rules), start+height)

	var b strings.Builder
	for i := start; i < end; i++ {
		caret := "  "
		if i == selected {
			caret = st.Focused.Render("> ")
		}
		label := fmt.Sprintf("%s  %s", pad(capabilityFlags(rules[i]), 10), displayPath(rules[i].Path))
		line := truncate(label, max(4, width-2))
		if i == selected {
			line = st.Selected.Render(line)
		}
		b.WriteString(caret + line + "\n")
	}

	if start > 0 || end < len(rules) {
		b.WriteString(st.Dim.Render(truncate(fmt.Sprintf("  showing %d-%d of %d", start+1, end, len(rules)), width)))
	}
	return strings.TrimRight(b.String(), "\n")
}

// scrollStart keeps the selected row inside a window of the given height.
func scrollStart(selected, total, height int) int {
	if total <= height || height <= 0 {
		return 0
	}
	start := selected - height/2
	start = max(0, start)
	return min(start, total-height)
}

// capabilityFlags is a fixed-width, per-capability summary: the initial of
// each capability the rule grants, in canonical order, with a dot where it
// does not.
//
// A column of letters reads faster than a comma list when scanning twenty
// rules, and it is the same width on every row, which is what makes the
// path column line up. `deny` is spelled out instead, because a rule that
// denies is not a rule with one more capability.
func capabilityFlags(rule policy.Rule) string {
	if rule.HasCapability(policy.CapabilityDeny) {
		return "DENY"
	}

	var b strings.Builder
	for _, c := range policy.Capabilities {
		if c == policy.CapabilityDeny {
			continue
		}
		if rule.HasCapability(c) {
			b.WriteString(strings.ToUpper(string(c)[:1]))
			continue
		}
		b.WriteString("·")
	}
	return b.String()
}

// renderRuleDetails draws everything about the selected rule, including
// the content BPE preserves but does not model.
func renderRuleDetails(st Styles, s *Session, selected, width int) string {
	doc := s.Document()
	if selected < 0 || selected >= doc.RuleCount() {
		return st.Dim.Render("no rule selected")
	}
	rule := doc.Policy.Rules[selected]

	var b strings.Builder
	b.WriteString(st.PanelTitle.Render(truncate("rule "+itoa(selected+1), width)))
	b.WriteString("\n")
	b.WriteString(detailLine(st, "path", displayPath(rule.Path), width))

	caps := make([]string, 0, len(rule.Capabilities))
	for _, c := range rule.Capabilities {
		switch {
		case c == policy.CapabilityDeny:
			caps = append(caps, st.Deny.Render(string(c)))
		case c == policy.CapabilitySudo:
			caps = append(caps, st.Warning.Render(string(c)))
		case !c.Known():
			caps = append(caps, st.Error.Render(string(c)+" (unknown)"))
		default:
			caps = append(caps, string(c))
		}
	}
	if len(caps) == 0 {
		b.WriteString(detailLine(st, "capabilities", st.Warning.Render("none — this rule grants nothing"), width))
	} else {
		b.WriteString(detailLine(st, "capabilities", strings.Join(caps, ", "), width))
	}

	src, err := doc.RuleSource(hclpolicy.RuleRef(selected))
	if err != nil {
		// A document that cannot describe its own source is read-only, and
		// the header already says so; the details panel just shows less.
		src = hclpolicy.RuleSource{}
	}

	b.WriteString(optionalDetail(st, "comment", src, "comment", quoteOrEmpty(rule.Comment), width))
	b.WriteString(optionalDetail(st, "expiration", src, "expiration", formatExpiration(st, rule.Expiration), width))
	b.WriteString(optionalDetail(st, "required", src, "required_parameters", strings.Join(rule.RequiredParameters, ", "), width))
	b.WriteString(optionalDetail(st, "allowed", src, "allowed_parameters", formatParams(st, rule.AllowedParameters, "any value"), width))
	b.WriteString(optionalDetail(st, "denied", src, "denied_parameters", formatParams(st, rule.DeniedParameters, "no value permitted"), width))

	if len(src.Unknown) > 0 || len(src.NestedBlocks) > 0 {
		b.WriteString("\n")
		b.WriteString(st.Dim.Render(wrapTo(preservedNote(src.Unknown, src.NestedBlocks), width)))
		b.WriteString("\n")
	}

	var locked []string
	for _, field := range src.Fields {
		if !field.Editable() {
			locked = append(locked, field.Name)
		}
	}
	if src.PathLock != "" {
		locked = append([]string{"path"}, locked...)
	}
	if len(locked) > 0 {
		b.WriteString("\n")
		b.WriteString(st.Locked.Render(wrapTo("read-only here: "+strings.Join(locked, ", ")+
			" — open the rule to see why", width)))
		b.WriteString("\n")
	}

	return b.String()
}

// optionalDetail renders a field that may legitimately be absent, showing
// the difference between absent and present-but-empty.
func optionalDetail(st Styles, label string, src hclpolicy.RuleSource, attr, value string, width int) string {
	status, ok := src.Field(attr)
	if ok && !status.Present {
		return ""
	}
	if value == "" {
		value = st.Dim.Render("(set, but empty)")
	}
	return detailLine(st, label, value, width)
}

func detailLine(st Styles, label, value string, width int) string {
	const detailLabelWidth = 14
	return st.Label.Render(pad(label, detailLabelWidth)) +
		truncate(value, max(6, width-detailLabelWidth)) + "\n"
}

func quoteOrEmpty(s string) string {
	if s == "" {
		return ""
	}
	return `"` + s + `"`
}

func formatExpiration(st Styles, t *time.Time) string {
	if t == nil {
		return ""
	}
	formatted := t.UTC().Format(time.RFC3339)
	if t.Before(time.Now()) {
		return st.Error.Render(formatted + " (expired — this rule no longer applies)")
	}
	return formatted
}

// formatParams renders a parameter map, spelling out what an empty value
// list means rather than showing an empty pair of brackets.
func formatParams(st Styles, params []policy.ParameterValues, emptyMeaning string) string {
	if len(params) == 0 {
		return ""
	}
	parts := make([]string, 0, len(params))
	for _, item := range params {
		if len(item.Values) == 0 {
			parts = append(parts, item.Name+st.Dim.Render(" ("+emptyMeaning+")"))
			continue
		}
		parts = append(parts, item.Name+"="+strings.Join(item.Values, "/"))
	}
	return strings.Join(parts, "  ")
}

// wrapTo wraps text at width on word boundaries, for the few places that
// show a sentence rather than a field.
func wrapTo(text string, width int) string {
	if width <= 0 {
		return text
	}
	var lines []string
	var line string
	for _, word := range strings.Fields(text) {
		switch {
		case line == "":
			line = word
		case lipgloss.Width(line)+1+lipgloss.Width(word) <= width:
			line += " " + word
		default:
			lines = append(lines, line)
			line = word
		}
	}
	if line != "" {
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}
