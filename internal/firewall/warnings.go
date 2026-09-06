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
// One warning is exactly one line, which is a rule the backends keep rather
// than something inferred here. An earlier version folded a warning's
// continuation lines into it, and could not tell a continuation from the
// ORDINARY comment that follows: pf emits "# main ruleset references the
// dezhban anchor" right after a disabled-pf warning, so the fold produced a
// single entry reading "…nothing is being filtered. Re-enable with `sudo pfctl
// -e`. main ruleset references the dezhban anchor" — a self-contradicting
// sentence, in the pane's orange row, in exactly the scenario the on-host check
// tells a tester to reproduce. Long lines wrap; a heuristic that guesses which
// comments belong together does not.
func Warnings(readback string) []string {
	var out []string
	for _, line := range strings.Split(readback, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, WarningPrefix) {
			out = append(out, strings.TrimSpace(strings.TrimPrefix(trimmed, WarningPrefix)))
		}
	}
	return out
}
