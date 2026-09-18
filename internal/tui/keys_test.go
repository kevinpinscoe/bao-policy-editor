package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

// pressKey builds the key-press message a given keystroke produces, so
// tests drive the editor the way a terminal would rather than calling its
// internals directly.
//
// It covers the keys BPE actually binds. Anything else is treated as a
// single printable character, which is what an unrecognized letter is.
func pressKey(keystroke string) tea.KeyPressMsg {
	switch keystroke {
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEsc}
	case "tab":
		return tea.KeyPressMsg{Code: tea.KeyTab}
	case "shift+tab":
		return tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift}
	case "up":
		return tea.KeyPressMsg{Code: tea.KeyUp}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	case "left":
		return tea.KeyPressMsg{Code: tea.KeyLeft}
	case "right":
		return tea.KeyPressMsg{Code: tea.KeyRight}
	case "home":
		return tea.KeyPressMsg{Code: tea.KeyHome}
	case "end":
		return tea.KeyPressMsg{Code: tea.KeyEnd}
	case "backspace":
		return tea.KeyPressMsg{Code: tea.KeyBackspace}
	case " ", "space":
		return tea.KeyPressMsg{Code: tea.KeySpace, Text: " "}
	}

	if len(keystroke) > 5 && keystroke[:5] == "ctrl+" {
		return tea.KeyPressMsg{Code: rune(keystroke[5]), Mod: tea.ModCtrl}
	}

	runes := []rune(keystroke)
	return tea.KeyPressMsg{Code: runes[0], Text: keystroke}
}

// typeText returns the key presses for a run of ordinary characters.
func typeText(text string) []tea.KeyPressMsg {
	out := make([]tea.KeyPressMsg, 0, len(text))
	for _, r := range text {
		if r == ' ' {
			out = append(out, tea.KeyPressMsg{Code: tea.KeySpace, Text: " "})
			continue
		}
		out = append(out, tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	return out
}

// TestPressKeyMatchesWhatUpdateSees guards the helper itself: every
// keystroke the editor binds must come back out of pressKey with the same
// string Update switches on, or a test could pass while the real binding
// is broken.
func TestPressKeyMatchesWhatUpdateSees(t *testing.T) {
	bound := []string{
		"enter", "esc", "tab", "shift+tab", "up", "down", "left", "right",
		"home", "end", "a", "d", "x", "p", "g", "t", "s", "q", "y", "n", "r",
		"v", "c", "j", "k", "?", "ctrl+s", "ctrl+c", "ctrl+d",
		// Remote mode: o opens a policy whose name a create collided with,
		// and / focuses the browser's filter.
		"o", "/",
	}
	for _, keystroke := range bound {
		if got := pressKey(keystroke).String(); got != keystroke {
			t.Errorf("pressKey(%q).String() = %q, want %q", keystroke, got, keystroke)
		}
	}
	if got := pressKey(" ").String(); got != "space" && got != " " {
		t.Errorf("pressKey(space).String() = %q", got)
	}
}
