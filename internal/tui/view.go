package tui

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/kevinpinscoe/bao-policy-editor/internal/hclpolicy"
)

// render assembles the whole frame: the current screen's content, the
// status or problem line, and the command footer.
//
// The footer is present on every screen and always lists the keys that
// screen actually accepts. That is the build brief's "keep available
// commands visible in a footer", and it is why nothing in this editor
// depends on the user remembering a keystroke.
func (m *Model) render() string {
	if m.dialog != dialogNone {
		return m.renderDialog()
	}

	var content string
	var hints []hint

	switch m.screen {
	case screenEditor:
		body, listTop := renderEditor(m.styles, m.session, m.selected, m.height, m.width)
		content, m.listTop = body, listTop
		hints = editorHints

	case screenForm:
		content = m.form.View(m.styles, m.width)
		hints = formHints
		if m.form.editing {
			hints = append([]hint{{Key: "enter", Label: "finish typing"}}, formHints...)
		}

	case screenParams:
		content = m.params.View(m.styles, m.width)
		hints = paramHints

	case screenTest:
		content = m.test.View(m.styles, m.session, m.width)
		hints = testHints

	case screenSavePath:
		content = m.renderSavePath()
		hints = []hint{{Key: "enter", Label: "write the file"}, {Key: "esc", Label: "back"}}

	case screenPreview:
		content = m.renderScrollScreen("HCL preview — the document exactly as it would be written")
		hints = listHints

	case screenDiagnostics:
		content = m.renderScrollScreen(m.diagnosticsTitle())
		hints = listHints

	case screenHelp:
		content = m.renderScrollScreen("Keys")
		hints = listHints

	case screenReview:
		content = m.renderScrollScreen(m.reviewTitle())
		hints = reviewHints
		if m.reviewingConflict {
			hints = []hint{{Key: "esc", Label: "back"}, {Key: "↑/↓", Label: "scroll"}}
		}
	}

	return m.frame(content, m.renderStatus(), renderHints(m.styles, hints, m.width))
}

// frame lays the window out with the status line and the footer pinned to
// the bottom, padding the content to fill whatever is left.
//
// Pinning matters because the editor runs full-window: without it the
// footer floats directly under the content and moves up and down the
// screen as the rule list grows and shrinks, which makes the one row the
// user is meant to glance at the one row that is never in the same place.
// Content taller than the window is truncated from the bottom rather than
// allowed to push the footer off the screen — every screen that can hold
// more than a window's worth scrolls in a viewport instead.
func (m *Model) frame(content, status, footer string) string {
	lines := strings.Split(content, "\n")

	available := max(1, m.height-2)
	if len(lines) > available {
		lines = lines[:available]
	}
	for len(lines) < available {
		lines = append(lines, "")
	}

	lines = append(lines, status, footer)
	return strings.Join(lines, "\n")
}

// renderScrollScreen wraps the shared viewport with a title and a scroll
// position, so a long preview or diff says how much of it is off-screen.
func (m *Model) renderScrollScreen(title string) string {
	header := m.styles.Title.Render(truncate(title, m.width))
	return header + m.styles.Dim.Render(m.scrollPosition()) + "\n" + m.viewport.View()
}

// scrollPosition says whether there is more to see, in words rather than
// as a bare percentage — "0%" at the top of a long document reads as an
// empty progress bar rather than as an invitation to scroll.
func (m *Model) scrollPosition() string {
	if m.viewport.TotalLineCount() <= m.viewport.VisibleLineCount() {
		return ""
	}
	switch {
	case m.viewport.AtTop():
		return "  ↓ more below"
	case m.viewport.AtBottom():
		return "  ↑ end"
	default:
		return fmt.Sprintf("  %d%%", int(m.viewport.ScrollPercent()*100))
	}
}

func (m *Model) diagnosticsTitle() string {
	errs, warns := m.session.ErrorCount(), m.session.WarningCount()
	switch {
	case errs == 0 && warns == 0:
		return "Diagnostics — nothing to report"
	case errs == 0:
		return fmt.Sprintf("Diagnostics — %d %s", warns, pluralize("warning", warns))
	default:
		return fmt.Sprintf("Diagnostics — %d %s, %d %s",
			errs, pluralize("error", errs), warns, pluralize("warning", warns))
	}
}

func (m *Model) reviewTitle() string {
	added, removed := CountChanges(m.review)
	if m.reviewingConflict {
		return fmt.Sprintf("What changed on disk — %d added, %d removed, against your version",
			added, removed)
	}
	// The base name, not the full path: the header already shows where the
	// file is, and a long path pushes the counts — the part this title
	// exists for — off the end of the line.
	return fmt.Sprintf("Review before saving %s — %d added, %d removed",
		filepath.Base(m.session.Filename()), added, removed)
}

// renderStatus shows the last thing that happened, or the last thing that
// went wrong. A problem outranks a status: if something was refused, that
// is what the user needs to read.
func (m *Model) renderStatus() string {
	switch {
	case m.problem != "":
		return m.styles.Error.Render(truncate(m.problem, m.width))
	case m.status != "":
		return m.styles.Dim.Render(truncate(m.status, m.width))
	default:
		return ""
	}
}

// renderDiagnostics lists every finding, with its severity spelled out.
func (m *Model) renderDiagnostics() string {
	diags := m.session.Diagnostics()
	if len(diags) == 0 {
		return m.styles.Success.Render("no issues found in this policy")
	}

	var b strings.Builder
	for _, d := range diags {
		label := m.styles.Warning.Render("warning")
		if d.Severity == hclpolicy.SeverityError {
			label = m.styles.Error.Render("error  ")
		}

		where := d.Path
		if d.Line > 0 {
			where = fmt.Sprintf("line %d", d.Line)
		}
		if where != "" {
			where = m.styles.Dim.Render("  " + where)
		}

		b.WriteString(label + "  " + truncate(d.Summary, max(20, m.width-20)) + where + "\n")
		if d.Detail != "" {
			b.WriteString(indentBlock(wrapTo(d.Detail, max(20, m.width-4)), "    ", m.width) + "\n")
		}
		if d.Remediation != "" {
			b.WriteString(m.styles.Dim.Render(indentBlock(wrapTo("try: "+d.Remediation, max(20, m.width-4)), "    ", m.width)) + "\n")
		}
		b.WriteString("\n")
	}
	return b.String()
}

// renderDiff draws a unified diff, marking every line with a character as
// well as a color so it reads correctly with NO_COLOR set.
func (m *Model) renderDiff(lines []DiffLine) string {
	if !HasChanges(lines) {
		return m.styles.Dim.Render("no differences")
	}

	var b strings.Builder
	for _, line := range lines {
		text := truncate(line.Marker()+" "+line.Text, m.width)
		switch line.Op {
		case DiffAdded:
			b.WriteString(m.styles.DiffAdded.Render(text))
		case DiffRemoved:
			b.WriteString(m.styles.DiffRemoved.Render(text))
		case DiffGap:
			b.WriteString(m.styles.DiffGap.Render(truncate("  … "+line.Text+" …", m.width)))
		default:
			b.WriteString(text)
		}
		b.WriteString("\n")
	}
	return b.String()
}

func (m *Model) renderHelp() string {
	var b strings.Builder
	for _, section := range helpSections {
		b.WriteString(m.styles.PanelTitle.Render(section.Title))
		b.WriteString("\n")
		for _, h := range section.Keys {
			b.WriteString("  " + m.styles.FooterKey.Render(pad(h.Key, 18)) + truncate(h.Label, max(20, m.width-22)) + "\n")
		}
		b.WriteString("\n")
	}
	b.WriteString(m.styles.Dim.Render(wrapTo(
		"BPE edits the file you opened rather than regenerating it. A comment, an attribute BPE "+
			"does not recognize, or a nested block it does not model is preserved exactly; where a "+
			"value cannot be rewritten safely, that one field is shown read-only with the reason.",
		max(20, m.width-2))))
	return b.String()
}

func (m *Model) renderSavePath() string {
	var b strings.Builder
	b.WriteString(m.styles.Title.Render("Save this policy"))
	b.WriteString("\n")
	b.WriteString(m.styles.Dim.Render(wrapTo(
		"This document has no file yet. BPE will create the file you name, and will not replace "+
			"an existing one — it has not read that file, so it cannot tell it apart from something unrelated.",
		max(20, m.width-2))))
	b.WriteString("\n\n")
	b.WriteString(m.styles.Label.Render(pad("path", 8)) + m.savePath.View())
	b.WriteString("\n")
	return b.String()
}

// renderDialog draws a modal question. Each one names what is at stake in
// a sentence before offering the keys, because all three are about losing
// something.
func (m *Model) renderDialog() string {
	var title string
	var body []string
	var choices []hint

	switch m.dialog {
	case dialogDiscard:
		title = "There are unsaved changes"
		added, removed := CountChanges(Diff(m.session.Original(), m.session.Current()))
		body = []string{
			fmt.Sprintf("%d %s added and %d removed have not been written to %s.",
				added, pluralize("line", added), removed, m.session.Filename()),
			"Quitting now loses them.",
		}
		choices = []hint{{Key: "s", Label: "review and save"}, {Key: "d", Label: "discard and quit"}, {Key: "esc", Label: "keep editing"}}

	case dialogRemove:
		title = "Remove this rule?"
		body = []string{fmt.Sprintf("path %s", displayPath(m.pendingRemoval.Path))}
		if comments := strings.TrimSpace(m.pendingRemoval.LeadComments); comments != "" {
			body = append(body,
				"Its own comments go with it, so they are not left above the next rule describing something they do not:",
				indentBlock(comments, "    ", m.width))
		}
		if len(m.pendingRemoval.Unknown) > 0 {
			body = append(body, "Also removed: the attributes "+strings.Join(m.pendingRemoval.Unknown, ", ")+
				", which BPE does not model but has been preserving.")
		}
		if len(m.pendingRemoval.NestedBlocks) > 0 {
			body = append(body, "Also removed: the nested "+pluralize("block", len(m.pendingRemoval.NestedBlocks))+" "+
				strings.Join(m.pendingRemoval.NestedBlocks, ", ")+".")
		}
		body = append(body, "Nothing is written to disk until you save.")
		choices = []hint{{Key: "y", Label: "remove it"}, {Key: "esc", Label: "keep it"}}

	case dialogConflict:
		title = "The file changed on disk"
		body = []string{
			fmt.Sprintf("%s is not what it was when BPE read it, so the save was refused rather than overwriting someone else's change.",
				m.session.Filename()),
			"Your edits are still here and have not been touched.",
		}
		choices = []hint{
			{Key: "v", Label: "see what changed on disk"},
			{Key: "r", Label: "reload from disk (discards your edits)"},
			{Key: "esc", Label: "cancel and keep editing"},
		}
	}

	var b strings.Builder
	b.WriteString(m.styles.Dialog.Render(truncate(title, m.width)))
	b.WriteString("\n\n")
	for _, line := range body {
		b.WriteString(wrapTo(line, max(20, m.width-2)))
		b.WriteString("\n")
	}
	b.WriteString("\n")
	b.WriteString(renderHints(m.styles, choices, m.width))
	return b.String()
}
