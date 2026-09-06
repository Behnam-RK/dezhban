package firewall

import (
	"reflect"
	"testing"
)

// "Loaded" and "enforcing" are different questions. A readback that carries a
// warning describes a firewall holding dezhban's rules and filtering nothing,
// and a caller has to be able to find that without parsing the ruleset.
func TestWarningsAreFoundAndFolded(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		want []string
	}{
		{"healthy readback carries none", "# main ruleset references the dezhban anchor\nblock drop out all\n", nil},
		{"empty", "", nil},
		{
			name: "a two-line warning folds into one",
			in: "# WARNING: pf is DISABLED — these rules are loaded but nothing is\n" +
				"# being filtered. Re-enable with `sudo pfctl -e`.\n" +
				"block drop out all\n",
			want: []string{"pf is DISABLED — these rules are loaded but nothing is being filtered. Re-enable with `sudo pfctl -e`."},
		},
		{
			name: "rules after a warning do not extend it",
			in:   "# WARNING: something\nblock drop out all\n# an ordinary comment\n",
			want: []string{"something"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := Warnings(tc.in); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Warnings() = %#v, want %#v", got, tc.want)
			}
		})
	}
}
