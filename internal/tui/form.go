package tui

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"github.com/kevinpinscoe/bao-policy-editor/internal/hclpolicy"
	"github.com/kevinpinscoe/bao-policy-editor/internal/policy"
)

// The rule form edits one `path` block.
//
// # Why it shows every supported field
//
// The form covers the whole domain model — path, all nine capabilities,
// comment, expiration, and the three parameter constraints — rather than
// the three fields most rules use. Kevin's instruction, 2026-09-17: a form
// that cannot display a constraint must not be allowed to rewrite the rule
// carrying it, because the user would have no way to know it was there.
// Showing everything, and keeping the rarely-used half collapsed behind an
// "advanced" row with a summary, is how both hold at once.
//
// # Why it tracks "set" separately from "empty"
//
// `comment = ""` and no `comment` attribute are different files, and so
// are `required_parameters = []` and no `required_parameters`. Each
// optional field therefore carries its own set/unset state alongside its
// text, the form shows "(not set)" rather than an empty box, and unsetting
// is its own key rather than something inferred from clearing the text.
//
// # Why accepting an unchanged form writes nothing
//
// Every field compares itself against the rule as parsed, and emits an
// hclpolicy.AttrEdit only when it actually differs. Capabilities preserve
// the source's own ordering — and any capability BPE does not recognize —
// so ticking nothing produces no edit even though the checkboxes are
// listed in a canonical order the file may not use.

// rowKind identifies what a form row is, independently of where it
// currently sits, since the advanced rows come and go.
type rowKind int

const (
	rowPath rowKind = iota
	rowCapability
	rowComment
	rowAdvanced
	rowExpiration
	rowRequired
	rowAllowed
	rowDenied
)

type formRow struct {
	kind rowKind
	// capability is meaningful only for rowCapability.
	capability policy.Capability
}

// optionalText is one optional attribute whose value is edited as text.
type optionalText struct {
	set   bool
	value string
	input textinput.Model
}

// optionalParams is one optional parameter-map attribute, edited in its
// own subform.
type optionalParams struct {
	set    bool
	values []policy.ParameterValues
}

// ruleForm is the add/edit form for a single rule.
type ruleForm struct {
	// ref is the rule being edited. It is ignored when isNew is true.
	ref   hclpolicy.RuleRef
	isNew bool

	src  hclpolicy.RuleSource
	orig policy.Rule

	path textinput.Model

	// caps holds the checkbox state for the nine capabilities BPE knows.
	// A capability in the file that BPE does not recognize is not shown as
	// a checkbox and is never dropped — see unknownCapabilities.
	caps map[policy.Capability]bool

	comment    optionalText
	expiration optionalText
	required   optionalText
	allowed    optionalParams
	denied     optionalParams

	advanced bool
	cursor   int
	editing  bool

	problem string
	notice  string
}

// newRuleForm builds the form for an existing rule.
func newRuleForm(ref hclpolicy.RuleRef, rule policy.Rule, src hclpolicy.RuleSource) *ruleForm {
	f := &ruleForm{ref: ref, src: src, orig: rule, caps: map[policy.Capability]bool{}}

	f.path = newInput("path pattern, e.g. secret/data/team-a/*")
	f.path.SetValue(rule.Path)

	for _, c := range rule.Capabilities {
		if c.Known() {
			f.caps[c] = true
		}
	}

	f.comment = newOptionalText(src, "comment", rule.Comment, "a note about why this rule exists")
	f.required = newOptionalText(src, "required_parameters", strings.Join(rule.RequiredParameters, ", "),
		"comma-separated parameter names")

	expiration := ""
	if rule.Expiration != nil {
		expiration = rule.Expiration.UTC().Format(time.RFC3339)
	}
	f.expiration = newOptionalText(src, "expiration", expiration, "RFC 3339, e.g. 2027-01-02T15:04:05Z")

	f.allowed = optionalParams{set: fieldPresent(src, "allowed_parameters"), values: rule.AllowedParameters}
	f.denied = optionalParams{set: fieldPresent(src, "denied_parameters"), values: rule.DeniedParameters}

	// Open the advanced section straight away when the rule already uses
	// any of it, so nothing a rule carries is hidden behind a keystroke
	// the user does not know to press.
	f.advanced = f.hasAdvancedContent()
	return f
}

// newAddForm builds the form for a brand-new rule. Every optional field
// starts unset, so an added rule's HCL contains only what the user
// actually filled in.
func newAddForm() *ruleForm {
	f := &ruleForm{isNew: true, caps: map[policy.Capability]bool{}, src: emptyRuleSource()}
	f.path = newInput("path pattern, e.g. secret/data/team-a/*")
	f.comment = newOptionalText(f.src, "comment", "", "a note about why this rule exists")
	f.required = newOptionalText(f.src, "required_parameters", "", "comma-separated parameter names")
	f.expiration = newOptionalText(f.src, "expiration", "", "RFC 3339, e.g. 2027-01-02T15:04:05Z")
	f.caps[policy.CapabilityRead] = true
	return f
}

func newInput(placeholder string) textinput.Model {
	in := textinput.New()
	in.Prompt = ""
	in.Placeholder = placeholder
	return in
}

func newOptionalText(src hclpolicy.RuleSource, attr, value, placeholder string) optionalText {
	t := optionalText{set: fieldPresent(src, attr), value: value, input: newInput(placeholder)}
	t.input.SetValue(value)
	return t
}

func fieldPresent(src hclpolicy.RuleSource, attr string) bool {
	status, ok := src.Field(attr)
	return ok && status.Present
}

// emptyRuleSource is the source description of a rule that does not exist
// yet: every field absent, nothing locked.
func emptyRuleSource() hclpolicy.RuleSource {
	src := hclpolicy.RuleSource{}
	for _, name := range []string{"capabilities", "comment", "expiration", "required_parameters", "allowed_parameters", "denied_parameters"} {
		src.Fields = append(src.Fields, hclpolicy.FieldStatus{Name: name})
	}
	return src
}

func (f *ruleForm) hasAdvancedContent() bool {
	return f.expiration.set || f.required.set || f.allowed.set || f.denied.set
}

// rows returns the form's currently visible rows, in order.
func (f *ruleForm) rows() []formRow {
	rows := []formRow{{kind: rowPath}}
	for _, c := range policy.Capabilities {
		rows = append(rows, formRow{kind: rowCapability, capability: c})
	}
	rows = append(rows, formRow{kind: rowComment}, formRow{kind: rowAdvanced})
	if f.advanced {
		rows = append(rows,
			formRow{kind: rowExpiration},
			formRow{kind: rowRequired},
			formRow{kind: rowAllowed},
			formRow{kind: rowDenied},
		)
	}
	return rows
}

func (f *ruleForm) currentRow() formRow {
	rows := f.rows()
	if f.cursor < 0 || f.cursor >= len(rows) {
		return rows[0]
	}
	return rows[f.cursor]
}

// lockFor returns the reason the attribute behind a row cannot be edited,
// or "" when it can.
func (f *ruleForm) lockFor(row formRow) string {
	switch row.kind {
	case rowPath:
		return f.src.PathLock
	case rowCapability:
		return f.fieldLock("capabilities")
	case rowComment:
		return f.fieldLock("comment")
	case rowExpiration:
		return f.fieldLock("expiration")
	case rowRequired:
		return f.fieldLock("required_parameters")
	case rowAllowed:
		return f.fieldLock("allowed_parameters")
	case rowDenied:
		return f.fieldLock("denied_parameters")
	default:
		return ""
	}
}

func (f *ruleForm) fieldLock(attr string) string {
	status, ok := f.src.Field(attr)
	if !ok {
		return ""
	}
	return status.Lock
}

// unknownCapabilities are capabilities present in the source that BPE does
// not recognize. They have no checkbox, they are never removed by an edit,
// and the form says so rather than letting them vanish quietly.
func (f *ruleForm) unknownCapabilities() []policy.Capability {
	var out []policy.Capability
	for _, c := range f.orig.Capabilities {
		if !c.Known() {
			out = append(out, c)
		}
	}
	return out
}

// activeInput returns the text input the cursor is currently on, or nil
// for a row that is not text-edited.
func (f *ruleForm) activeInput() *textinput.Model {
	switch f.currentRow().kind {
	case rowPath:
		return &f.path
	case rowComment:
		return &f.comment.input
	case rowExpiration:
		return &f.expiration.input
	case rowRequired:
		return &f.required.input
	default:
		return nil
	}
}

// optionalFor returns the optional field a row edits, or nil.
func (f *ruleForm) optionalFor(row formRow) *optionalText {
	switch row.kind {
	case rowComment:
		return &f.comment
	case rowExpiration:
		return &f.expiration
	case rowRequired:
		return &f.required
	default:
		return nil
	}
}

// formResult is what a finished form hands back to the root model.
type formResult int

const (
	formOngoing formResult = iota
	formApply
	formCancel
	formOpenAllowed
	formOpenDenied
)

// Update handles one key press. It returns the outcome so the root model
// can decide what to do next, keeping screen transitions in one place
// rather than scattered through the form.
func (f *ruleForm) Update(msg tea.Msg) (formResult, tea.Cmd) {
	key, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return formOngoing, nil
	}

	if f.editing {
		return f.updateEditing(key)
	}

	switch key.String() {
	case "esc":
		return formCancel, nil
	case "ctrl+s":
		if problem := f.validate(); problem != "" {
			f.problem = problem
			return formOngoing, nil
		}
		return formApply, nil
	case "up", "k", "shift+tab":
		f.move(-1)
	case "down", "j", "tab":
		f.move(1)
	case "home":
		f.cursor = 0
	case "end":
		f.cursor = len(f.rows()) - 1
	case " ", "space":
		return f.activate()
	case "enter":
		return f.activate()
	case "ctrl+d":
		f.unsetCurrent()
	}
	return formOngoing, nil
}

func (f *ruleForm) updateEditing(key tea.KeyPressMsg) (formResult, tea.Cmd) {
	input := f.activeInput()
	if input == nil {
		f.editing = false
		return formOngoing, nil
	}

	switch key.String() {
	case "enter", "esc":
		// Both commit the typed text to the form; nothing is written to
		// the document until the whole form is applied, so there is no
		// difference worth making the user remember.
		f.editing = false
		input.Blur()
		f.syncInput()
		f.problem = ""
		return formOngoing, nil
	}

	var cmd tea.Cmd
	*input, cmd = input.Update(key)
	return formOngoing, cmd
}

// activate is what Enter or Space does on the current row.
func (f *ruleForm) activate() (formResult, tea.Cmd) {
	row := f.currentRow()
	if lock := f.lockFor(row); lock != "" {
		f.problem = fmt.Sprintf("%s cannot be edited: %s", rowAttribute(row), lock)
		return formOngoing, nil
	}
	f.problem = ""

	switch row.kind {
	case rowAdvanced:
		f.advanced = !f.advanced
		return formOngoing, nil

	case rowCapability:
		f.caps[row.capability] = !f.caps[row.capability]
		f.notice = denyNotice(f.capabilitySet())
		return formOngoing, nil

	case rowAllowed:
		f.allowed.set = true
		return formOpenAllowed, nil

	case rowDenied:
		f.denied.set = true
		return formOpenDenied, nil
	}

	if opt := f.optionalFor(row); opt != nil {
		opt.set = true
	}
	input := f.activeInput()
	if input == nil {
		return formOngoing, nil
	}
	f.editing = true
	return formOngoing, input.Focus()
}

// unsetCurrent removes an optional attribute entirely, as distinct from
// clearing its value.
func (f *ruleForm) unsetCurrent() {
	row := f.currentRow()
	if lock := f.lockFor(row); lock != "" {
		f.problem = fmt.Sprintf("%s cannot be edited: %s", rowAttribute(row), lock)
		return
	}
	f.problem = ""

	switch row.kind {
	case rowAllowed:
		f.allowed.set = false
		f.allowed.values = nil
	case rowDenied:
		f.denied.set = false
		f.denied.values = nil
	default:
		if opt := f.optionalFor(row); opt != nil {
			opt.set = false
			opt.value = ""
			opt.input.SetValue("")
		}
	}
}

func (f *ruleForm) move(delta int) {
	rows := len(f.rows())
	f.cursor = (f.cursor + delta + rows) % rows
}

// syncInput copies the focused text input's value into the form's own
// state, so the rest of the form reads one value rather than sometimes the
// widget and sometimes the field.
func (f *ruleForm) syncInput() {
	switch f.currentRow().kind {
	case rowComment:
		f.comment.value = f.comment.input.Value()
	case rowExpiration:
		f.expiration.value = f.expiration.input.Value()
	case rowRequired:
		f.required.value = f.required.input.Value()
	}
}

// setParams receives a finished parameter subform.
func (f *ruleForm) setParams(kind rowKind, values []policy.ParameterValues) {
	switch kind {
	case rowAllowed:
		f.allowed.set = true
		f.allowed.values = values
	case rowDenied:
		f.denied.set = true
		f.denied.values = values
	}
}

// capabilitySet returns the capabilities the form would write, preserving
// the source's own order and carrying through any capability BPE does not
// recognize.
//
// Preserving the order matters for more than tidiness: it is what makes
// opening a rule and accepting it unchanged produce no edit at all, even
// though the checkboxes are presented in a canonical order the file is
// under no obligation to use.
func (f *ruleForm) capabilitySet() []policy.Capability {
	var out []policy.Capability
	for _, c := range f.orig.Capabilities {
		if !c.Known() || f.caps[c] {
			out = append(out, c)
		}
	}
	for _, c := range policy.Capabilities {
		if f.caps[c] && !slices.Contains(out, c) {
			out = append(out, c)
		}
	}
	return out
}

// requiredList splits the required-parameters text input into names.
func (f *ruleForm) requiredList() []string {
	return splitCommaList(f.required.value)
}

func splitCommaList(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

// validate checks the form for problems that would produce nonsense HCL.
// It returns the first blocking problem, or "" when the form can be
// applied. Policy-level advice — a suspicious wildcard, a broad sys/*
// grant — is hclpolicy.Validate's job and appears in the diagnostics panel
// after the edit lands; blocking on it here would stop the user writing a
// policy they are in the middle of thinking about.
func (f *ruleForm) validate() string {
	if strings.TrimSpace(f.path.Value()) == "" {
		return "a rule needs a path"
	}
	if f.expiration.set {
		if _, err := parseExpiration(f.expiration.value); err != nil {
			return err.Error()
		}
	}
	if problem := validateParams("allowed_parameters", f.allowed); problem != "" {
		return problem
	}
	if problem := validateParams("denied_parameters", f.denied); problem != "" {
		return problem
	}
	return ""
}

func validateParams(attr string, p optionalParams) string {
	if !p.set {
		return ""
	}
	seen := map[string]bool{}
	for _, item := range p.values {
		if strings.TrimSpace(item.Name) == "" {
			return attr + ": a parameter needs a name"
		}
		if seen[item.Name] {
			return fmt.Sprintf("%s: %q is listed twice", attr, item.Name)
		}
		seen[item.Name] = true
	}
	return ""
}

func parseExpiration(value string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339, strings.TrimSpace(value))
	if err != nil {
		return time.Time{}, fmt.Errorf("expiration must be RFC 3339, e.g. 2027-01-02T15:04:05Z")
	}
	return t, nil
}

// denyNotice warns about a capability combination OpenBao treats specially
// — it is advice shown beside the checkboxes, not a reason to refuse the
// edit.
func denyNotice(caps []policy.Capability) string {
	if !slices.Contains(caps, policy.CapabilityDeny) || len(caps) <= 1 {
		return ""
	}
	return "deny overrides every other capability on this rule; the others will have no effect"
}

// rule assembles the form's state into a domain-model rule, for adding a
// new one.
func (f *ruleForm) rule() (policy.Rule, error) {
	rule := policy.Rule{
		Path:         strings.TrimSpace(f.path.Value()),
		Capabilities: f.capabilitySet(),
	}
	if f.comment.set {
		rule.Comment = f.comment.value
	}
	if f.expiration.set {
		t, err := parseExpiration(f.expiration.value)
		if err != nil {
			return policy.Rule{}, err
		}
		rule.Expiration = &t
	}
	if f.required.set {
		rule.RequiredParameters = f.requiredList()
	}
	if f.allowed.set {
		rule.AllowedParameters = f.allowed.values
	}
	if f.denied.set {
		rule.DeniedParameters = f.denied.values
	}
	return rule, nil
}

// edits produces the minimal set of changes needed to turn the rule as
// parsed into the rule as the form now describes it.
//
// "Minimal" is the whole point: a field the user did not touch produces no
// AttrEdit, so its tokens — and any comment, alignment, or formatting
// around them — are left exactly as written.
func (f *ruleForm) edits() (*string, []hclpolicy.AttrEdit, error) {
	var newPath *string
	if path := strings.TrimSpace(f.path.Value()); path != f.orig.Path {
		newPath = &path
	}

	var edits []hclpolicy.AttrEdit

	if caps := f.capabilitySet(); !slices.Equal(caps, f.orig.Capabilities) {
		edits = append(edits, hclpolicy.SetCapabilities(caps))
	}

	if edit, changed := textEdit("comment", f.comment, fieldPresent(f.src, "comment"), f.orig.Comment,
		func(v string) hclpolicy.AttrEdit { return hclpolicy.SetComment(v) }); changed {
		edits = append(edits, edit)
	}

	if edit, changed, err := f.expirationEdit(); err != nil {
		return nil, nil, err
	} else if changed {
		edits = append(edits, edit)
	}

	if edit, changed := listEdit("required_parameters", f.required, fieldPresent(f.src, "required_parameters"),
		f.orig.RequiredParameters, f.requiredList()); changed {
		edits = append(edits, edit)
	}

	if edit, changed := paramsEdit("allowed_parameters", f.allowed, fieldPresent(f.src, "allowed_parameters"), f.orig.AllowedParameters); changed {
		edits = append(edits, edit)
	}
	if edit, changed := paramsEdit("denied_parameters", f.denied, fieldPresent(f.src, "denied_parameters"), f.orig.DeniedParameters); changed {
		edits = append(edits, edit)
	}

	return newPath, edits, nil
}

// textEdit decides what to do with a simple optional string attribute. The
// three-way comparison — was it there, is it there now, and did its value
// change — is what keeps absent, empty, and populated distinct.
func textEdit(attr string, field optionalText, wasPresent bool, wasValue string, set func(string) hclpolicy.AttrEdit) (hclpolicy.AttrEdit, bool) {
	switch {
	case !field.set && !wasPresent:
		return hclpolicy.AttrEdit{}, false
	case !field.set && wasPresent:
		return hclpolicy.RemoveAttr(attr), true
	case field.set && !wasPresent:
		return set(field.value), true
	case field.value != wasValue:
		return set(field.value), true
	default:
		return hclpolicy.AttrEdit{}, false
	}
}

func listEdit(attr string, field optionalText, wasPresent bool, wasValue, nowValue []string) (hclpolicy.AttrEdit, bool) {
	switch {
	case !field.set && !wasPresent:
		return hclpolicy.AttrEdit{}, false
	case !field.set && wasPresent:
		return hclpolicy.RemoveAttr(attr), true
	case field.set && !wasPresent:
		return hclpolicy.SetRequiredParameters(nowValue), true
	case !slices.Equal(wasValue, nowValue):
		return hclpolicy.SetRequiredParameters(nowValue), true
	default:
		return hclpolicy.AttrEdit{}, false
	}
}

func paramsEdit(attr string, field optionalParams, wasPresent bool, wasValue []policy.ParameterValues) (hclpolicy.AttrEdit, bool) {
	switch {
	case !field.set && !wasPresent:
		return hclpolicy.AttrEdit{}, false
	case !field.set && wasPresent:
		return hclpolicy.RemoveAttr(attr), true
	case field.set && !wasPresent:
		return hclpolicy.SetParameterMap(attr, field.values), true
	case !sameParams(wasValue, field.values):
		return hclpolicy.SetParameterMap(attr, field.values), true
	default:
		return hclpolicy.AttrEdit{}, false
	}
}

func (f *ruleForm) expirationEdit() (hclpolicy.AttrEdit, bool, error) {
	wasPresent := fieldPresent(f.src, "expiration")
	switch {
	case !f.expiration.set && !wasPresent:
		return hclpolicy.AttrEdit{}, false, nil
	case !f.expiration.set && wasPresent:
		return hclpolicy.RemoveAttr("expiration"), true, nil
	}

	t, err := parseExpiration(f.expiration.value)
	if err != nil {
		return hclpolicy.AttrEdit{}, false, err
	}
	if wasPresent && f.orig.Expiration != nil && f.orig.Expiration.Equal(t) {
		return hclpolicy.AttrEdit{}, false, nil
	}
	return hclpolicy.SetExpiration(t), true, nil
}

func sameParams(a, b []policy.ParameterValues) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Name != b[i].Name || !slices.Equal(a[i].Values, b[i].Values) {
			return false
		}
	}
	return true
}

func rowAttribute(row formRow) string {
	switch row.kind {
	case rowPath:
		return "path"
	case rowCapability:
		return "capabilities"
	case rowComment:
		return "comment"
	case rowExpiration:
		return "expiration"
	case rowRequired:
		return "required_parameters"
	case rowAllowed:
		return "allowed_parameters"
	case rowDenied:
		return "denied_parameters"
	default:
		return "this field"
	}
}
