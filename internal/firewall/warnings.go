package firewall

import "strings"

// WarningPrefix marks a line InstalledRules prepends to a readback when the
// rules are LOADED but not actually filtering — pf switched off, an anchor the
// main ruleset no longer references, an nft output chain whose policy drifted
// off drop.
//
// It is a contract, not a formatting choice. "Loaded" and "enforcing" are
// different questions, and the bool InstalledRules returns answers only the
// first: a firewall can hold every rule dezhban installed and filter nothing.
// Leaving that discoverable only by reading pf syntax inside a collapsed pane
// is how a non-enforcing kill switch reads as healthy, so the warning has to be
// something a caller can find without understanding the ruleset it is wrapped in.
const WarningPrefix = "# WARNING:"

// Warnings returns the warning lines in a readback, in order, with the prefix
// and surrounding whitespace stripped. Empty when the readback carries none —
// which is the healthy case, and the only one in which "loaded" means
// "enforcing".
//
// Warnings are emitted as consecutive lines, so a continuation line (one that
// follows a warning and is itself a comment) is folded into the warning above
// it rather than dropped: the second half of "pf is DISABLED — these rules are
// loaded but nothing is / being filtered" is the half that says what it means.
func Warnings(readback string) []string {
	var out []string
	inWarning := false
	for _, line := range strings.Split(readback, "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, WarningPrefix):
			out = append(out, strings.TrimSpace(strings.TrimPrefix(trimmed, WarningPrefix)))
			inWarning = true
		case inWarning && strings.HasPrefix(trimmed, "#"):
			cont := strings.TrimSpace(strings.TrimPrefix(trimmed, "#"))
			if cont != "" && len(out) > 0 {
				out[len(out)-1] += " " + cont
			}
		default:
			inWarning = false
		}
	}
	return out
}
