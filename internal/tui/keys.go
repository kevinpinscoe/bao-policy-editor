package tui

import (
	"strings"

	"charm.land/lipgloss/v2"
)

// hint is one entry in the command footer: the key to press and what it
// does.
//
// The footer is not a convenience here, it is the discoverability model.
// The build brief rules out requiring Vim keybindings or memorized
// sequences, so every action the current screen offers is named on screen
// with the key that performs it, and the full list is one keystroke away
// on the help screen.
type hint struct {
	Key   string
	Label string

	// Short is an abbreviated label, used when the full ones do not fit
	// the terminal. Empty means Label is already short enough.
	Short string
}

func (h hint) short() string {
	if h.Short != "" {
		return h.Short
	}
	return h.Label
}

// labelStyle is how much of each hint renderHints is currently willing to
// show.
type labelStyle int

const (
	fullLabels labelStyle = iota
	shortLabels
	keysOnly
)

// renderHints lays the footer out on one line, shortening rather than
// wrapping.
//
// A second footer row would steal content from exactly the narrow
// terminals where rows are scarcest, and truncating the line silently
// loses its last entries — which in the editor's case are "save" and
// "quit", the two nobody can afford not to see. So it gives up detail
// instead: full labels, then abbreviated ones, then the keys alone. Every
// key stays visible at every width, and the help screen always has the
// full list.
func renderHints(st Styles, hints []hint, width int) string {
	for _, style := range []labelStyle{fullLabels, shortLabels, keysOnly} {
		if line := joinHints(st, hints, style); lipgloss.Width(line) <= width {
			return line
		}
	}
	return truncate(joinHints(st, hints, keysOnly), width)
}

func joinHints(st Styles, hints []hint, style labelStyle) string {
	parts := make([]string, 0, len(hints))
	for _, h := range hints {
		switch style {
		case fullLabels:
			parts = append(parts, st.FooterKey.Render(h.Key)+" "+st.Footer.Render(h.Label))
		case shortLabels:
			parts = append(parts, st.FooterKey.Render(h.Key)+" "+st.Footer.Render(h.short()))
		default:
			parts = append(parts, st.FooterKey.Render(h.Key))
		}
	}
	separator := "  ·  "
	if style != fullLabels {
		separator = " · "
	}
	return strings.Join(parts, st.Footer.Render(separator))
}

// Key bindings, gathered here so the help screen and the footers cannot
// drift apart from what Update actually accepts.
//
// Navigation is deliberately conventional: arrows and Tab move, Enter
// confirms, Escape backs out. The Vim-style j/k are accepted as well
// because they cost nothing, but nothing requires them and they are not
// advertised in the footers.
var (
	editorHints = []hint{
		{Key: "↑/↓", Label: "select rule", Short: "move"},
		{Key: "enter", Label: "edit"},
		{Key: "a", Label: "add"},
		{Key: "d", Label: "duplicate", Short: "dup"},
		{Key: "x", Label: "remove", Short: "del"},
		{Key: "p", Label: "preview", Short: "hcl"},
		{Key: "g", Label: "diagnostics", Short: "diag"},
		{Key: "t", Label: "test access", Short: "test"},
		{Key: "r", Label: "remote policies", Short: "remote"},
		{Key: "s", Label: "save"},
		{Key: "?", Label: "help"},
		{Key: "q", Label: "quit"},
	}

	connectHints = []hint{
		{Key: "tab/↑/↓", Label: "move between fields", Short: "move"},
		{Key: "enter", Label: "connect"},
		{Key: "esc", Label: "back to the editor", Short: "back"},
	}

	browseHints = []hint{
		{Key: "↑/↓", Label: "select policy", Short: "move"},
		{Key: "enter", Label: "open"},
		{Key: "n", Label: "new policy", Short: "new"},
		{Key: "x", Label: "delete", Short: "del"},
		{Key: "/", Label: "filter"},
		{Key: "r", Label: "refresh"},
		{Key: "esc", Label: "back"},
	}

	// serverConflictReviewHints spells out what writing does here, because
	// it is not the same thing ctrl+s does on the ordinary review screen:
	// there it saves the work, here it replaces a newer version on the
	// server with it.
	serverConflictReviewHints = []hint{
		{Key: "↑/↓", Label: "scroll"},
		{Key: "ctrl+s", Label: "replace the server's version with yours", Short: "overwrite"},
		{Key: "esc", Label: "back, keeping your edits", Short: "back"},
	}

	formHints = []hint{
		{Key: "tab/↑/↓", Label: "move between fields", Short: "move"},
		{Key: "space", Label: "toggle capability", Short: "toggle"},
		{Key: "enter", Label: "open field", Short: "open"},
		{Key: "ctrl+d", Label: "unset field", Short: "unset"},
		{Key: "ctrl+s", Label: "apply"},
		{Key: "esc", Label: "cancel"},
	}

	listHints = []hint{
		{Key: "↑/↓", Label: "scroll"},
		{Key: "pgup/pgdn", Label: "page"},
		{Key: "esc", Label: "back"},
	}

	reviewHints = []hint{
		{Key: "↑/↓", Label: "scroll"},
		{Key: "ctrl+s", Label: "write the file", Short: "write"},
		{Key: "esc", Label: "back"},
	}

	testHints = []hint{
		{Key: "tab", Label: "switch field", Short: "field"},
		{Key: "←/→", Label: "choose capability", Short: "capability"},
		{Key: "enter", Label: "run the check", Short: "run"},
		{Key: "esc", Label: "back"},
	}

	paramHints = []hint{
		{Key: "↑/↓", Label: "select"},
		{Key: "enter", Label: "edit"},
		{Key: "a", Label: "add"},
		{Key: "x", Label: "remove", Short: "del"},
		{Key: "ctrl+s", Label: "apply"},
		{Key: "esc", Label: "cancel"},
	}
)

// helpSections is the full key reference, shown on the help screen. It
// repeats the footers on purpose: the footer says what is available here
// and now, and this says what the editor can do at all.
var helpSections = []struct {
	Title string
	Keys  []hint
}{
	{"Moving around", []hint{
		{Key: "↑ / ↓", Label: "move the selection (j / k also work)"},
		{Key: "tab / shift+tab", Label: "move between fields in a form"},
		{Key: "pgup / pgdn", Label: "page through a long view"},
		{Key: "home / end", Label: "jump to the start or end of a long view"},
		{Key: "enter", Label: "open the selected thing"},
		{Key: "esc", Label: "go back, or cancel the current form"},
		{Key: "mouse", Label: "click a rule to select it; the wheel scrolls"},
	}},
	{"Working with rules", []hint{
		{Key: "a", Label: "add a new rule"},
		{Key: "enter", Label: "edit the selected rule"},
		{Key: "d", Label: "duplicate the selected rule, comments and all"},
		{Key: "x", Label: "remove the selected rule"},
		{Key: "space", Label: "toggle the highlighted capability in the rule form"},
		{Key: "ctrl+d", Label: "unset a field in the rule form — removes the attribute rather than emptying it"},
		{Key: "ctrl+s", Label: "apply the form"},
	}},
	{"Looking at the policy", []hint{
		{Key: "p", Label: "preview the generated HCL"},
		{Key: "g", Label: "list validation diagnostics"},
		{Key: "t", Label: "test effective access for a path and capability"},
	}},
	{"Saving and leaving", []hint{
		{Key: "s", Label: "review the pending changes, then save"},
		{Key: "ctrl+s", Label: "write the file, or the policy, from the review screen"},
		{Key: "q / ctrl+c", Label: "quit, with confirmation when there are unsaved changes"},
		{Key: "?", Label: "show this help"},
	}},
	{"Policies on an OpenBao server", []hint{
		{Key: "r", Label: "connect to a server and browse its policies (bpe --remote starts here)"},
		{Key: "enter", Label: "open the selected policy for editing"},
		{Key: "n", Label: "start a new policy on the server"},
		{Key: "x", Label: "delete a policy — asks for its name typed out, and cannot be undone"},
		{Key: "/", Label: "filter the policy list; esc leaves the filter"},
		{Key: "esc", Label: "cancel a request in flight, or go back"},
	}},
}
