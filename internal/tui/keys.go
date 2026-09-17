package tui

import "strings"

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
}

// renderHints lays the footer out on one line, truncated to width rather
// than wrapped — a wrapped footer steals a row from the content on
// exactly the narrow terminals where rows are scarcest.
func renderHints(st Styles, hints []hint, width int) string {
	parts := make([]string, 0, len(hints))
	for _, h := range hints {
		parts = append(parts, st.FooterKey.Render(h.Key)+" "+st.Footer.Render(h.Label))
	}
	return truncate(strings.Join(parts, st.Footer.Render("  ·  ")), width)
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
		{"↑/↓", "select rule"},
		{"enter", "edit"},
		{"a", "add"},
		{"d", "duplicate"},
		{"x", "remove"},
		{"p", "preview"},
		{"g", "diagnostics"},
		{"t", "test access"},
		{"s", "save"},
		{"?", "help"},
		{"q", "quit"},
	}

	formHints = []hint{
		{"tab/↑/↓", "move between fields"},
		{"space", "toggle capability"},
		{"enter", "open field"},
		{"ctrl+s", "apply"},
		{"esc", "cancel"},
	}

	listHints = []hint{
		{"↑/↓", "scroll"},
		{"pgup/pgdn", "page"},
		{"esc", "back"},
	}

	reviewHints = []hint{
		{"↑/↓", "scroll"},
		{"ctrl+s", "write the file"},
		{"esc", "back"},
	}

	testHints = []hint{
		{"tab", "switch field"},
		{"←/→", "choose capability"},
		{"enter", "run the check"},
		{"esc", "back"},
	}

	paramHints = []hint{
		{"↑/↓", "select"},
		{"enter", "edit"},
		{"a", "add"},
		{"x", "remove"},
		{"ctrl+s", "apply"},
		{"esc", "cancel"},
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
		{"↑ / ↓", "move the selection (j / k also work)"},
		{"tab / shift+tab", "move between fields in a form"},
		{"pgup / pgdn", "page through a long view"},
		{"home / end", "jump to the start or end of a long view"},
		{"enter", "open the selected thing"},
		{"esc", "go back, or cancel the current form"},
		{"mouse", "click a rule to select it; the wheel scrolls"},
	}},
	{"Working with rules", []hint{
		{"a", "add a new rule"},
		{"enter", "edit the selected rule"},
		{"d", "duplicate the selected rule, comments and all"},
		{"x", "remove the selected rule"},
		{"space", "toggle the highlighted capability in the rule form"},
	}},
	{"Looking at the policy", []hint{
		{"p", "preview the generated HCL"},
		{"g", "list validation diagnostics"},
		{"t", "test effective access for a path and capability"},
	}},
	{"Saving and leaving", []hint{
		{"s", "review the pending changes, then save"},
		{"ctrl+s", "write the file from the review screen"},
		{"q / ctrl+c", "quit, with confirmation when there are unsaved changes"},
		{"?", "show this help"},
	}},
}
