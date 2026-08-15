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

// The table feeds internal/setup.CommonBlocked, whose test depends on "AQ"
// existing as a real code but NOT being a common-blocked option.
func TestTableCoversTheCodesOtherPackagesRelyOn(t *testing.T) {
	for _, c := range []string{"IR", "RU", "CN", "KP", "SY", "CU", "BY", "AQ"} {
		if Name(c) == "" {
			t.Errorf("Name(%q) is empty; a package downstream expects a name", c)
		}
	}
}
