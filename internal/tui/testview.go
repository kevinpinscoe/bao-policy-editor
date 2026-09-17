package tui

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"github.com/kevinpinscoe/bao-policy-editor/internal/evaluator"
	"github.com/kevinpinscoe/bao-policy-editor/internal/policy"
)

// accessTest is the effective-access screen: pick a path and a capability,
// and see what the policy as it stands right now would decide.
//
// "Right now" is the point. It runs against the document's current bytes,
// including unsaved edits, so the question it answers is "what would this
// policy do if I saved it" rather than "what does the file on disk do".
type accessTest struct {
	path      textinput.Model
	capIndex  int
	pathFocus bool

	// result is the rendered explanation of the last check, and problem
	// the reason the last check could not be made.
	result  string
	problem string
	allowed bool
	decided bool
}

func newAccessTest(initialPath string) *accessTest {
	t := &accessTest{pathFocus: true}
	t.path = newInput("secret/data/team-a/example")
	t.path.SetValue(initialPath)
	// read is the capability people check first, and starting on it saves
	// a keystroke in the common case.
	t.capIndex = indexOfCapability(policy.CapabilityRead)
	return t
}

func indexOfCapability(c policy.Capability) int {
	for i, known := range policy.Capabilities {
		if known == c {
			return i
		}
	}
	return 0
}

func (t *accessTest) capability() policy.Capability {
	return policy.Capabilities[t.capIndex]
}

// Update handles one key press. It returns true when the caller should
// leave the screen.
func (t *accessTest) Update(msg tea.Msg, session *Session) (bool, tea.Cmd) {
	key, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return false, nil
	}

	switch key.String() {
	case "esc":
		return true, nil
	case "tab", "shift+tab":
		t.pathFocus = !t.pathFocus
		if t.pathFocus {
			return false, t.path.Focus()
		}
		t.path.Blur()
		return false, nil
	case "left":
		if !t.pathFocus {
			t.capIndex = (t.capIndex - 1 + len(policy.Capabilities)) % len(policy.Capabilities)
			return false, nil
		}
	case "right":
		if !t.pathFocus {
			t.capIndex = (t.capIndex + 1) % len(policy.Capabilities)
			return false, nil
		}
	case "enter":
		t.run(session)
		return false, nil
	}

	if !t.pathFocus {
		return false, nil
	}
	var cmd tea.Cmd
	t.path, cmd = t.path.Update(key)
	return false, cmd
}

// run performs the check against the session's current document.
func (t *accessTest) run(session *Session) {
	t.result, t.problem, t.decided, t.allowed = "", "", false, false

	path := strings.TrimSpace(t.path.Value())
	if path == "" {
		t.problem = "enter a path to check"
		return
	}

	doc := session.Document()
	switch {
	case doc.HasErrors():
		t.problem = "this policy has errors, so BPE cannot simulate access against it — see the diagnostics screen"
		return
	case doc.Unsupported:
		// The same refusal `bpe test` makes, for the same reason: the
		// decoded policy is only part of what OpenBao would enforce, so a
		// confident answer from it would be a misleading one.
		t.problem = "this policy contains content BPE cannot fully represent, so a simulated decision would not be trustworthy"
		return
	}

	ev, err := evaluator.Compile([]evaluator.NamedPolicy{{
		Source: evaluator.Source{Name: session.Filename()},
		Policy: doc.Policy,
	}}, time.Now())
	if err != nil {
		t.problem = fmt.Sprintf("this policy cannot be evaluated: %v", err)
		return
	}

	decision, evalErr := ev.Evaluate(path, t.capability())
	t.result = decision.Explain()
	t.decided = true
	t.allowed = decision.Allowed

	if evalErr != nil && !errors.Is(evalErr, evaluator.ErrIncompleteEvaluation) {
		t.problem = evalErr.Error()
		t.decided = false
	}
}

// View renders the effective-access screen.
func (t *accessTest) View(st Styles, session *Session, width int) string {
	var b strings.Builder

	b.WriteString(st.Title.Render(truncate("Test effective access", width)))
	b.WriteString("\n")
	b.WriteString(st.Dim.Render(truncate("checked against the policy as it stands now, including unsaved changes", width)))
	b.WriteString("\n\n")

	pathCaret, capCaret := "  ", "  "
	if t.pathFocus {
		pathCaret = st.Focused.Render("> ")
	} else {
		capCaret = st.Focused.Render("> ")
	}

	b.WriteString(pathCaret + st.Label.Render(pad("path", 14)) + t.path.View() + "\n")
	b.WriteString(capCaret + st.Label.Render(pad("capability", 14)) + t.renderCapabilityPicker(st, width) + "\n\n")

	switch {
	case t.problem != "":
		b.WriteString(st.Error.Render(truncate(t.problem, width)))
		b.WriteString("\n")
	case t.decided:
		verdict := st.Error.Render("DENIED")
		if t.allowed {
			verdict = st.Success.Render("ALLOWED")
		}
		b.WriteString(st.PanelTitle.Render("result") + "  " + verdict + "\n\n")
		b.WriteString(indentBlock(t.result, "  ", width))
		b.WriteString("\n")
	default:
		b.WriteString(st.Dim.Render("press enter to run the check"))
		b.WriteString("\n")
	}

	return b.String()
}

// renderCapabilityPicker shows the chosen capability with its neighbours,
// so the left/right keys have something visible to act on.
func (t *accessTest) renderCapabilityPicker(st Styles, width int) string {
	parts := make([]string, 0, len(policy.Capabilities))
	for i, c := range policy.Capabilities {
		if i == t.capIndex {
			parts = append(parts, st.Selected.Render("["+string(c)+"]"))
			continue
		}
		parts = append(parts, st.Dim.Render(" "+string(c)+" "))
	}
	return truncate(strings.Join(parts, ""), max(10, width-16))
}

// indentBlock indents every line of a multi-line block and truncates it to
// width, so a long explanation cannot break a narrow layout.
func indentBlock(text, indent string, width int) string {
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		lines[i] = indent + truncate(line, max(10, width-len(indent)))
	}
	return strings.Join(lines, "\n")
}
