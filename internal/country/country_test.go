package country

import "testing"

func TestNameKnownCodes(t *testing.T) {
	cases := map[string]string{
		"IR": "Iran",
		"RU": "Russia",
		"KP": "North Korea",
		"KZ": "Kazakhstan",
		"CN": "China", // CLDR says "China mainland"; we shorten it deliberately.
		"US": "United States",
		"GB": "United Kingdom",
	}
	for code, want := range cases {
		if got := Name(code); got != want {
			t.Errorf("Name(%q) = %q, want %q", code, got, want)
		}
	}
}

func TestNameNormalizesInput(t *testing.T) {
	// Config values reach here already upper-cased by config.Normalize, but the
	// simulate flag and provider readings do not all take that path.
	for _, in := range []string{"ir", " IR ", "iR", "\tir\n"} {
		if got := Name(in); got != "Iran" {
			t.Errorf("Name(%q) = %q, want Iran", in, got)
		}
	}
}

func TestUnknownCodeDegradesToTheCodeItself(t *testing.T) {
	// blockedCountries has never validated ISO membership — "ZZ" loads today and
	// internal/config/reload_test.go depends on that. An unknown code must
	// therefore still render as something, and that something is the code.
	if got := Name("ZZ"); got != "" {
		t.Errorf("Name(ZZ) = %q, want empty", got)
	}
	if got := Label("ZZ"); got != "ZZ" {
		t.Errorf("Label(ZZ) = %q, want ZZ", got)
	}
	if got := Label("zz"); got != "ZZ" {
		t.Errorf("Label(zz) = %q, want ZZ", got)
	}
}

func TestLabelFormat(t *testing.T) {
	if got := Label("IR"); got != "Iran (IR)" {
		t.Errorf("Label(IR) = %q, want %q", got, "Iran (IR)")
	}
	// Empty in, empty out — callers print a "(none)" placeholder themselves.
	if got := Label(""); got != "" {
		t.Errorf("Label(\"\") = %q, want empty", got)
	}
	if got := Label("   "); got != "" {
		t.Errorf("Label(spaces) = %q, want empty", got)
	}
}

func TestLabels(t *testing.T) {
	got := Labels([]string{"IR", "RU", "ZZ"})
	want := []string{"Iran (IR)", "Russia (RU)", "ZZ"}
	if len(got) != len(want) {
		t.Fatalf("Labels() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("Labels()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
	if Labels(nil) != nil {
		t.Error("Labels(nil) should be nil so callers can test emptiness")
	}
}

// Names is what structured output carries, so the contract that matters is
// length: the result pairs with the input index-for-index, and an unknown code
// holds its place with "" rather than being dropped. Dropping it would shorten
// the slice and silently pair every later name with the wrong country.
func TestNamesPairsIndexForIndex(t *testing.T) {
	got := Names([]string{"IR", "ZZ", "RU"})
	want := []string{"Iran", "", "Russia"}
	if len(got) != len(want) {
		t.Fatalf("Names() = %v (len %d), want %v (len %d)", got, len(got), want, len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("Names()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
	if Names(nil) != nil {
		t.Error("Names(nil) should be nil so callers can test emptiness")
	}
}

// Names must NOT return what Labels returns. state.BlockedCountryNames holds
// bare names so a consumer composes "Iran (IR)" itself, by the one rule it
// already applies to state.CountryName; a label here renders "Iran (IR) (IR)".
func TestNamesAreBareNotLabels(t *testing.T) {
	n, l := Names([]string{"IR"}), Labels([]string{"IR"})
	if n[0] != "Iran" {
		t.Errorf("Names()[0] = %q, want the bare %q", n[0], "Iran")
	}
	if n[0] == l[0] {
		t.Errorf("Names() and Labels() agree (%q); the two shapes must stay distinct", n[0])
	}
}

// The table feeds internal/setup.CommonBlocked, whose test depends on "AQ"
// existing as a real code but NOT being a common-blocked option.
func TestTableCoversTheCodesOtherPackagesRelyOn(t *testing.T) {
	for _, c := range []string{"IR", "RU", "CN", "KP", "SY", "CU", "BY", "AQ"} {
		if Name(c) == "" {
			t.Errorf("Name(%q) is empty; a package downstream expects a name", c)
		}
	}
}
