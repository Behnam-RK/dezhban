package firewall

import (
	"reflect"
	"strings"
	"testing"
)

// "Loaded" and "enforcing" are different questions. A readback that carries a
// warning describes a firewall holding dezhban's rules and filtering nothing,
// and a caller has to be able to find that without parsing the ruleset.
func TestWarningsAreFoundOnePerLine(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		want []string
	}{
		{"healthy readback carries none", "# main ruleset references the dezhban anchor\nblock drop out all\n", nil},
		{"empty", "", nil},
		{
			name: "a warning is one entry",
			in: "# WARNING: pf is DISABLED — these rules are loaded but nothing is being filtered.\n" +
				"block drop out all\n",
			want: []string{"pf is DISABLED — these rules are loaded but nothing is being filtered."},
		},
		{
			// The regression this shape exists to prevent: pf prints its
			// anchor-reference line directly after a disabled-pf warning, and a
			// fold that guessed at continuations merged the two into one
			// self-contradicting sentence.
			name: "an ordinary comment after a warning stays separate",
			in: "# WARNING: pf is DISABLED — nothing is being filtered.\n" +
				"# main ruleset references the dezhban anchor\n" +
				"block drop out all\n",
			want: []string{"pf is DISABLED — nothing is being filtered."},
		},
		{
			name: "two warnings are two entries",
			in:   "# WARNING: first thing\n# WARNING: second thing\nblock drop out all\n",
			want: []string{"first thing", "second thing"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := Warnings(tc.in); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Warnings() = %#v, want %#v", got, tc.want)
			}
		})
	}
}

// Every warning the backends actually emit must survive Warnings() intact and
// alone. A warning that arrives merged with its neighbour is the pane's orange
// row and a JSON consumer's warnings[0], so its text is a contract.
func TestEveryEmittedWarningIsSelfContained(t *testing.T) {
	// The exact pair pf produces with `pfctl -d` and the anchor still
	// referenced — the scenario docs/contribute/testing.md tells a tester to
	// reproduce.
	readback := "# WARNING: pf is DISABLED — these rules are loaded but nothing is being filtered. Re-enable with `sudo pfctl -e`.\n" +
		"# main ruleset references the dezhban anchor\n" +
		"block drop out all\n"
	got := Warnings(readback)
	if len(got) != 1 {
		t.Fatalf("Warnings() = %#v, want exactly one", got)
	}
	if strings.Contains(got[0], "references the dezhban anchor") {
		t.Errorf("the warning absorbed the comment after it: %q", got[0])
	}
}
