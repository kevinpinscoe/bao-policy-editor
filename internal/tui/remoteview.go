package tui

import (
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"github.com/kevinpinscoe/bao-policy-editor/internal/config"
)

// connectForm is the screen that chooses which OpenBao server to talk to.
//
// It edits the address and the namespace, and nothing else. The token is
// deliberately not a field here: it comes from the already-resolved
// configuration (--token, BAO_TOKEN, VAULT_TOKEN) and the screen reports
// only whether one is configured. A token typed into a terminal input is a
// token in a terminal's scrollback, in a screen capture, and in whatever
// the multiplexer keeps — so the interface has no way to enter one at all,
// rather than a careful way. Kevin's instruction, 2026-09-18.
type connectForm struct {
	address   textinput.Model
	namespace textinput.Model
	focus     int

	// tokenConfigured is whether the resolved configuration carries a
	// token. The value itself is never read here, never rendered, and
	// never stored on this struct.
	tokenConfigured bool

	problem string
}

const (
	connectFieldAddress = iota
	connectFieldNamespace
	connectFieldCount
)

func newConnectForm(cfg config.Config) *connectForm {
	f := &connectForm{
		address:   newInput("https://openbao.example.com:8200"),
		namespace: newInput("(none)"),
		// Reveal is called only to ask whether the string is empty. The
		// value is not kept, not rendered, and not copied anywhere.
		tokenConfigured: cfg.Token.Reveal() != "",
	}
	f.address.SetValue(cfg.Address)
	f.namespace.SetValue(cfg.Namespace)
	return f
}

// connectResult is what a key press on the connect screen asked for.
type connectResult int

const (
	connectNone connectResult = iota
	connectCancel
	connectSubmit
)

func (f *connectForm) Update(key tea.KeyPressMsg) (connectResult, tea.Cmd) {
	switch key.String() {
	case "esc":
		return connectCancel, nil
	case "tab", "down":
		return connectNone, f.moveFocus(1)
	case "shift+tab", "up":
		return connectNone, f.moveFocus(-1)
	case "enter":
		if strings.TrimSpace(f.address.Value()) == "" {
			f.problem = "an address is required — BPE will not guess a server"
			return connectNone, nil
		}
		f.problem = ""
		return connectSubmit, nil
	}

	var cmd tea.Cmd
	switch f.focus {
	case connectFieldAddress:
		f.address, cmd = f.address.Update(key)
	case connectFieldNamespace:
		f.namespace, cmd = f.namespace.Update(key)
	}
	return connectNone, cmd
}

func (f *connectForm) moveFocus(delta int) tea.Cmd {
	f.focus = (f.focus + delta + connectFieldCount) % connectFieldCount
	f.address.Blur()
	f.namespace.Blur()
	switch f.focus {
	case connectFieldAddress:
		return f.address.Focus()
	default:
		return f.namespace.Focus()
	}
}

// apply returns the configuration to connect with: the resolved config
// with the address and namespace the user chose on this screen.
//
// Everything else — the token, the TLS settings — is carried through
// untouched from what internal/config already resolved, so the documented
// precedence stays the only precedence in play.
func (f *connectForm) apply(cfg config.Config) config.Config {
	out := cfg
	out.Address = strings.TrimSpace(f.address.Value())
	out.Namespace = strings.TrimSpace(f.namespace.Value())
	return out
}

func (f *connectForm) View(st Styles, width int) string {
	var b strings.Builder

	b.WriteString(st.Title.Render(truncate("Connect to OpenBao", width)))
	b.WriteString("\n")
	b.WriteString(st.Dim.Render(wrapTo(
		"Nothing here reaches the network until you press enter. Your current document and any "+
			"unsaved edits are untouched whether the connection succeeds or fails.",
		max(20, width-2))))
	b.WriteString("\n\n")

	rows := []struct {
		label string
		field textinput.Model
		index int
	}{
		{"address", f.address, connectFieldAddress},
		{"namespace", f.namespace, connectFieldNamespace},
	}
	for _, row := range rows {
		caret := "  "
		if f.focus == row.index {
			caret = st.Focused.Render("> ")
		}
		b.WriteString(caret + st.Label.Render(pad(row.label, 12)) + row.field.View() + "\n")
	}

	// The token line says whether one is configured and where BPE looks,
	// never what it is. "not configured" is a real, useful answer: it is
	// the reason a listing will come back empty or refused.
	token := st.Success.Render("configured")
	if !f.tokenConfigured {
		token = st.Warning.Render("not configured")
	}
	b.WriteString("  " + st.Label.Render(pad("token", 12)) + token +
		st.Dim.Render("  — from --token, BAO_TOKEN, or VAULT_TOKEN; never shown or entered here"))
	b.WriteString("\n")

	if f.problem != "" {
		b.WriteString("\n")
		b.WriteString(st.Error.Render(truncate(f.problem, width)))
		b.WriteString("\n")
	}

	return b.String()
}

// policyWord is "policy" or "policies". pluralize's "add an s" rule does
// not reach a word ending in a consonant plus y, and "0 policys" in a
// status line reads as a bug in the editor rather than an empty server.
func policyWord(n int) string {
	if n == 1 {
		return "policy"
	}
	return "policies"
}

// policyBrowser lists the policies on the connected server.
//
// The filter is explicitly focused rather than always taking keystrokes,
// so the single-letter actions on this screen (new, delete, refresh) are
// available without a modifier and typing into the filter cannot trigger
// one by accident.
type policyBrowser struct {
	names    []string
	selected int

	filter    textinput.Model
	filtering bool
}

func newPolicyBrowser(names []string) *policyBrowser {
	b := &policyBrowser{names: names}
	b.filter = newInput("filter by name")
	return b
}

// setNames adopts a fresh listing, keeping the selection on the same
// policy where it still exists — a refresh that silently moves the
// selection is how the wrong policy gets deleted.
func (b *policyBrowser) setNames(names []string) {
	previous := b.current()
	b.names = names
	b.selected = 0
	if previous == "" {
		return
	}
	for i, name := range b.visible() {
		if name == previous {
			b.selected = i
			return
		}
	}
}

// add records a policy the server now has, keeping the listing sorted.
//
// It is how a create reaches the browser without a round trip: re-listing
// would answer with a message that moves the screen and overwrites the
// status line, which is the wrong price for keeping a list current that
// `r` refreshes anyway.
func (b *policyBrowser) add(name string) {
	for _, existing := range b.names {
		if existing == name {
			return
		}
	}
	b.names = sortedNames(append(b.names, name))
}

// remove drops a policy the server no longer has, keeping the selection on
// something that still exists.
func (b *policyBrowser) remove(name string) {
	out := b.names[:0:0]
	for _, existing := range b.names {
		if existing != name {
			out = append(out, existing)
		}
	}
	b.names = out
	b.clampSelection()
}

// visible is the listing after the filter.
func (b *policyBrowser) visible() []string {
	needle := strings.ToLower(strings.TrimSpace(b.filter.Value()))
	if needle == "" {
		return b.names
	}
	var out []string
	for _, name := range b.names {
		if strings.Contains(strings.ToLower(name), needle) {
			out = append(out, name)
		}
	}
	return out
}

// current is the selected policy name, or "" when the listing is empty.
func (b *policyBrowser) current() string {
	names := b.visible()
	if b.selected < 0 || b.selected >= len(names) {
		return ""
	}
	return names[b.selected]
}

// browseResult is what a key press on the browser asked for.
type browseResult int

const (
	browseNone browseResult = iota
	browseBack
	browseOpen
	browseNew
	browseDelete
	browseRefresh
)

func (b *policyBrowser) Update(key tea.KeyPressMsg) (browseResult, tea.Cmd) {
	if b.filtering {
		switch key.String() {
		case "esc", "enter":
			b.filtering = false
			b.filter.Blur()
			b.clampSelection()
			return browseNone, nil
		}
		var cmd tea.Cmd
		b.filter, cmd = b.filter.Update(key)
		b.clampSelection()
		return browseNone, cmd
	}

	switch key.String() {
	case "esc":
		return browseBack, nil
	case "up", "k":
		b.move(-1)
	case "down", "j":
		b.move(1)
	case "home":
		b.selected = 0
	case "end":
		b.selected = max(0, len(b.visible())-1)
	case "/":
		b.filtering = true
		return browseNone, b.filter.Focus()
	case "enter":
		if b.current() != "" {
			return browseOpen, nil
		}
	case "n":
		return browseNew, nil
	case "x":
		if b.current() != "" {
			return browseDelete, nil
		}
	case "r":
		return browseRefresh, nil
	}
	return browseNone, nil
}

func (b *policyBrowser) move(delta int) {
	total := len(b.visible())
	if total == 0 {
		b.selected = 0
		return
	}
	b.selected = (b.selected + delta + total) % total
}

func (b *policyBrowser) clampSelection() {
	total := len(b.visible())
	if total == 0 {
		b.selected = 0
		return
	}
	b.selected = min(b.selected, total-1)
}

func (b *policyBrowser) View(st Styles, address string, height, width int) string {
	var sb strings.Builder

	sb.WriteString(st.Title.Render(truncate("Policies on "+address, width)))
	sb.WriteString("\n")

	filterLabel := st.Dim.Render("filter")
	if b.filtering {
		filterLabel = st.Focused.Render("filter")
	}
	sb.WriteString(filterLabel + "  " + b.filter.View() + "\n\n")

	names := b.visible()
	if len(names) == 0 {
		if strings.TrimSpace(b.filter.Value()) != "" {
			sb.WriteString(st.Dim.Render(truncate("no policy matches that filter", width)))
		} else {
			sb.WriteString(st.Dim.Render(truncate(
				"this server reports no policies your token can see — press n to create one", width)))
		}
		return sb.String()
	}

	listHeight := max(3, height-8)
	start := scrollStart(b.selected, len(names), listHeight)
	end := min(len(names), start+listHeight)

	for i := start; i < end; i++ {
		caret := "  "
		line := truncate(names[i], max(4, width-2))
		if i == b.selected {
			caret = st.Focused.Render("> ")
			line = st.Selected.Render(line)
		}
		sb.WriteString(caret + line + "\n")
	}

	if start > 0 || end < len(names) {
		sb.WriteString(st.Dim.Render(truncate(
			"  showing "+itoa(start+1)+"-"+itoa(end)+" of "+itoa(len(names)), width)))
	}

	return sb.String()
}
