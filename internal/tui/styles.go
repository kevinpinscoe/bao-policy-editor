package tui

import "charm.land/lipgloss/v2"

// Styles are the editor's colors and text decorations.
//
// Two rules govern everything in this file, both from the build brief.
//
// Colors are the terminal's own ANSI palette (0-15) rather than hex
// values. An indexed color is whatever the user's theme says it is, so it
// stays legible on a light background and a dark one without BPE trying
// to detect which it is looking at.
//
// And no state is communicated by color alone. Every colored thing in the
// interface also carries a word or a glyph that says the same thing:
// diagnostics are prefixed "error"/"warning", a selected capability shows
// "[x]", the focused row shows a caret, and an unsaved document says
// "modified". With NO_COLOR set, Bubble Tea's renderer strips the color
// and the interface still reads correctly — the styles below are the
// decoration, never the message.
type Styles struct {
	Title       lipgloss.Style
	Subtitle    lipgloss.Style
	PanelTitle  lipgloss.Style
	Selected    lipgloss.Style
	Focused     lipgloss.Style
	Dim         lipgloss.Style
	Label       lipgloss.Style
	Error       lipgloss.Style
	Warning     lipgloss.Style
	Success     lipgloss.Style
	Deny        lipgloss.Style
	Locked      lipgloss.Style
	Footer      lipgloss.Style
	FooterKey   lipgloss.Style
	Dialog      lipgloss.Style
	DiffAdded   lipgloss.Style
	DiffRemoved lipgloss.Style
	DiffGap     lipgloss.Style
}

// ANSI palette indices, named so the style definitions read as intent
// rather than as numbers.
const (
	ansiRed     = "1"
	ansiGreen   = "2"
	ansiYellow  = "3"
	ansiBlue    = "4"
	ansiMagenta = "5"
	ansiCyan    = "6"
	ansiGrey    = "8"
)

// DefaultStyles builds the editor's styles.
func DefaultStyles() Styles {
	return Styles{
		Title:       lipgloss.NewStyle().Bold(true),
		Subtitle:    lipgloss.NewStyle().Foreground(lipgloss.Color(ansiCyan)),
		PanelTitle:  lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(ansiBlue)),
		Selected:    lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(ansiCyan)),
		Focused:     lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(ansiMagenta)),
		Dim:         lipgloss.NewStyle().Foreground(lipgloss.Color(ansiGrey)),
		Label:       lipgloss.NewStyle().Foreground(lipgloss.Color(ansiBlue)),
		Error:       lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(ansiRed)),
		Warning:     lipgloss.NewStyle().Foreground(lipgloss.Color(ansiYellow)),
		Success:     lipgloss.NewStyle().Foreground(lipgloss.Color(ansiGreen)),
		Deny:        lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(ansiRed)),
		Locked:      lipgloss.NewStyle().Foreground(lipgloss.Color(ansiYellow)),
		Footer:      lipgloss.NewStyle().Foreground(lipgloss.Color(ansiGrey)),
		FooterKey:   lipgloss.NewStyle().Bold(true),
		Dialog:      lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(ansiYellow)),
		DiffAdded:   lipgloss.NewStyle().Foreground(lipgloss.Color(ansiGreen)),
		DiffRemoved: lipgloss.NewStyle().Foreground(lipgloss.Color(ansiRed)),
		DiffGap:     lipgloss.NewStyle().Foreground(lipgloss.Color(ansiGrey)),
	}
}

// truncate shortens s to fit width, marking the cut with an ellipsis. It
// is what keeps a long path or comment from wrapping the layout in a
// narrow terminal.
func truncate(s string, width int) string {
	if width <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= width {
		return s
	}
	if width <= 1 {
		return "…"
	}
	runes := []rune(s)
	for len(runes) > 0 && lipgloss.Width(string(runes))+1 > width {
		runes = runes[:len(runes)-1]
	}
	return string(runes) + "…"
}

// pad right-pads s to width, so columns line up without a table library.
func pad(s string, width int) string {
	w := lipgloss.Width(s)
	if w >= width {
		return s
	}
	return s + spaces(width-w)
}

func spaces(n int) string {
	if n <= 0 {
		return ""
	}
	buf := make([]byte, n)
	for i := range buf {
		buf[i] = ' '
	}
	return string(buf)
}
