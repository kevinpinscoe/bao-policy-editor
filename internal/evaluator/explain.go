package evaluator

import (
	"fmt"
	"strings"
)

// Explain renders d as multi-line, human-readable text — FSM-13's
// "Human-readable output explains why access was allowed or denied."
// Both the CLI and a future TUI can call this directly, or read d's
// fields to build their own presentation.
func (d Decision) Explain() string {
	var b strings.Builder

	fmt.Fprintf(&b, "path:       %s\n", d.RequestedPath)
	fmt.Fprintf(&b, "capability: %s\n", d.RequestedCapability)

	switch {
	case d.Incomplete:
		fmt.Fprintf(&b, "result:     INCOMPLETE — cannot determine a trustworthy decision\n")
		fmt.Fprintf(&b, "reason:     %s\n", d.IncompleteReason)
	case d.Allowed:
		fmt.Fprintf(&b, "result:     ALLOWED\n")
	default:
		fmt.Fprintf(&b, "result:     DENIED\n")
	}

	fmt.Fprintf(&b, "stage:      %s\n", d.Stage)

	if d.WinningPattern != "" {
		fmt.Fprintf(&b, "pattern:    %s\n", d.WinningPattern)
	}

	if d.Denied {
		fmt.Fprintf(&b, "denied by:  %s\n", formatSources(d.DeniedBy))
	} else if d.Allowed {
		fmt.Fprintf(&b, "granted by: %s\n", formatSources(d.Sources))
	}

	for _, l := range d.LosingCandidates {
		fmt.Fprintf(&b, "lost out:   %s (%s) — %s: %s\n", l.Pattern, formatSources(l.Sources), l.Reason, l.Detail)
	}

	return strings.TrimRight(b.String(), "\n")
}

func formatSources(sources []Source) string {
	if len(sources) == 0 {
		return "(none)"
	}
	names := make([]string, len(sources))
	for i, s := range sources {
		names[i] = s.Name
	}
	return strings.Join(names, ", ")
}
