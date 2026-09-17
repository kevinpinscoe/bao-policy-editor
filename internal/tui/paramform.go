package tui

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"github.com/kevinpinscoe/bao-policy-editor/internal/policy"
)

// paramForm edits one parameter-constraint map — allowed_parameters or
// denied_parameters — in a screen of its own.
//
// It is a separate screen rather than more rows on the rule form because a
// parameter map is a list of lists: each entry has a name and any number
// of permitted or forbidden values, and flattening that onto the rule form
// would produce exactly the crowded single screen the build brief rules
// out.
//
// The empty-list case is the one that costs people time, so the screen
// says what it means outright rather than leaving it to be inferred:
// OpenBao reads an empty value list under allowed_parameters as "this
// parameter may take any value", and under denied_parameters as "this
// parameter may not be supplied at all".
type paramForm struct {
	// kind is rowAllowed or rowDenied — which map is being edited, so the
	// finished result goes back to the right field.
	kind rowKind

	rulePath string
	values   []policy.ParameterValues

	cursor int

	// editing is the index of the entry being edited, or -1.
	editing   int
	nameInput textinput.Model
	valsInput textinput.Model
	// nameFocused says which of the two inputs has the cursor while an
	// entry is open.
	nameFocused bool

	problem string
}

func newParamForm(kind rowKind, rulePath string, values []policy.ParameterValues) *paramForm {
	p := &paramForm{
		kind:      kind,
		rulePath:  rulePath,
		values:    append([]policy.ParameterValues(nil), values...),
		editing:   -1,
		nameInput: newInput("parameter name"),
		valsInput: newInput("comma-separated values, or leave empty"),
	}
	return p
}

func (p *paramForm) attribute() string {
	if p.kind == rowDenied {
		return "denied_parameters"
	}
	return "allowed_parameters"
}

// emptyMeaning is the plain-English reading of an empty value list for
// this map, shown on the screen and beside every entry that has one.
func (p *paramForm) emptyMeaning() string {
	if p.kind == rowDenied {
		return "denies the parameter entirely — no value may be supplied"
	}
	return "permits any value"
}

type paramResult int

const (
	paramOngoing paramResult = iota
	paramApply
	paramCancel
)

// Update handles one key press.
func (p *paramForm) Update(msg tea.Msg) (paramResult, tea.Cmd) {
	key, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return paramOngoing, nil
	}

	if p.editing >= 0 {
		return p.updateEditing(key)
	}

	switch key.String() {
	case "esc":
		return paramCancel, nil
	case "ctrl+s":
		if problem := validateParams(p.attribute(), optionalParams{set: true, values: p.values}); problem != "" {
			p.problem = problem
			return paramOngoing, nil
		}
		return paramApply, nil
	case "up", "k":
		p.move(-1)
	case "down", "j":
		p.move(1)
	case "a":
		p.values = append(p.values, policy.ParameterValues{})
		p.cursor = len(p.values) - 1
		return paramOngoing, p.openEntry()
	case "enter":
		if len(p.values) == 0 {
			return paramOngoing, nil
		}
		return paramOngoing, p.openEntry()
	case "x":
		p.removeEntry()
	}
	return paramOngoing, nil
}

func (p *paramForm) updateEditing(key tea.KeyPressMsg) (paramResult, tea.Cmd) {
	switch key.String() {
	case "tab", "shift+tab", "up", "down":
		p.nameFocused = !p.nameFocused
		return paramOngoing, p.focusActive()
	case "enter", "esc":
		p.commitEntry()
		return paramOngoing, nil
	}

	var cmd tea.Cmd
	if p.nameFocused {
		p.nameInput, cmd = p.nameInput.Update(key)
	} else {
		p.valsInput, cmd = p.valsInput.Update(key)
	}
	return paramOngoing, cmd
}

func (p *paramForm) openEntry() tea.Cmd {
	p.editing = p.cursor
	entry := p.values[p.cursor]
	p.nameInput.SetValue(entry.Name)
	p.valsInput.SetValue(strings.Join(entry.Values, ", "))
	p.nameFocused = true
	p.problem = ""
	return p.focusActive()
}

func (p *paramForm) focusActive() tea.Cmd {
	if p.nameFocused {
		p.valsInput.Blur()
		return p.nameInput.Focus()
	}
	p.nameInput.Blur()
	return p.valsInput.Focus()
}

func (p *paramForm) commitEntry() {
	if p.editing < 0 || p.editing >= len(p.values) {
		p.editing = -1
		return
	}
	p.values[p.editing] = policy.ParameterValues{
		Name:   strings.TrimSpace(p.nameInput.Value()),
		Values: splitCommaList(p.valsInput.Value()),
	}
	p.editing = -1
	p.nameInput.Blur()
	p.valsInput.Blur()
}

func (p *paramForm) removeEntry() {
	if len(p.values) == 0 {
		return
	}
	p.values = append(p.values[:p.cursor], p.values[p.cursor+1:]...)
	if p.cursor >= len(p.values) {
		p.cursor = max(0, len(p.values)-1)
	}
}

func (p *paramForm) move(delta int) {
	if len(p.values) == 0 {
		return
	}
	p.cursor = (p.cursor + delta + len(p.values)) % len(p.values)
}

// View renders the parameter subform.
func (p *paramForm) View(st Styles, width int) string {
	var b strings.Builder

	b.WriteString(st.Title.Render(truncate(fmt.Sprintf("%s — %s", p.attribute(), displayPath(p.rulePath)), width)))
	b.WriteString("\n")
	b.WriteString(st.Dim.Render(truncate("an entry with no values "+p.emptyMeaning(), width)))
	b.WriteString("\n\n")

	if len(p.values) == 0 {
		b.WriteString(st.Dim.Render("  no entries yet — press a to add one"))
		b.WriteString("\n")
	}

	for i, entry := range p.values {
		caret := "  "
		if i == p.cursor {
			caret = st.Focused.Render("> ")
		}

		if p.editing == i {
			b.WriteString(caret + st.Label.Render(pad("name", 12)) + p.nameInput.View() + "\n")
			b.WriteString("  " + st.Label.Render(pad("values", 12)) + p.valsInput.View() + "\n")
			continue
		}

		name := entry.Name
		if name == "" {
			name = st.Dim.Render("(unnamed)")
		}
		values := strings.Join(entry.Values, ", ")
		if len(entry.Values) == 0 {
			values = st.Dim.Render("(empty — " + p.emptyMeaning() + ")")
		}
		b.WriteString(caret + pad(truncate(name, 20), 22) + truncate(values, max(10, width-26)) + "\n")
	}

	if p.problem != "" {
		b.WriteString("\n" + st.Error.Render(truncate("cannot apply: "+p.problem, width)) + "\n")
	}

	return b.String()
}
