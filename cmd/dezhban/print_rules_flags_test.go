package main

import "testing"

// print-rules carries two kinds of flag: ones describing a ruleset to RENDER
// (--mode) and ones selecting a live ruleset to REPORT (--applied, --installed).
// Mixing them, or asking for --json where the output is firewall syntax, is a
// request that cannot be honoured — and accepting it while quietly ignoring half
// of it is the failure this project treats as the worst kind.
func TestPrintRulesRefusesFlagsItCannotHonour(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want int
	}{
		{"two sources at once", []string{"--applied", "--installed"}, 2},
		{"mode with applied", []string{"--applied", "--mode", "fullblock"}, 2},
		{"mode with installed", []string{"--installed", "--mode", "guard"}, 2},
		{"json with no source", []string{"--json"}, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := cmdPrintRules(tc.args); got != tc.want {
				t.Errorf("cmdPrintRules(%v) = %d, want %d", tc.args, got, tc.want)
			}
		})
	}
}

// The refusal must be about what the user TYPED, not about a flag's default.
// --mode defaults to "guard", so testing its value instead of whether it was
// given would reject every plain --applied run.
func TestPrintRulesAppliedIsFineWithoutMode(t *testing.T) {
	// No record exists under the test's state dir, which is the documented
	// "nothing recorded" answer: exit 0, not a refusal and not a failure.
	if got := cmdPrintRules([]string{"--applied"}); got != 0 {
		t.Errorf("cmdPrintRules(--applied) = %d, want 0", got)
	}
}
