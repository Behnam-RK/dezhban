package config

import (
	"strings"
	"testing"
	"time"
)

func TestPresetsAreWellFormed(t *testing.T) {
	t.Parallel()
	for _, p := range Presets() {
		t.Run(p.Name, func(t *testing.T) {
			if p.Summary == "" {
				t.Error("empty Summary")
			}
			if p.Cost == "" {
				t.Error("empty Cost — a security tool states costs beside benefits")
			}
			if len(p.Values) != len(presetKeys) {
				t.Fatalf("Values has %d entries, want %d (presetKeys)", len(p.Values), len(presetKeys))
			}
			for _, key := range presetKeys {
				if _, ok := p.Values[key]; !ok {
					t.Errorf("missing value for %q", key)
				}
			}
		})
	}
}

// TestPresetApplyValidates proves every shipped preset produces a valid
// config — the round-trip a preset must survive before it's ever offered to
// a user.
func TestPresetApplyValidates(t *testing.T) {
	t.Parallel()
	base := Default()
	base.VPN.Endpoints = []string{"203.0.113.9"} // a config with a known endpoint validates cleanly
	Normalize(&base)

	for _, p := range Presets() {
		t.Run(p.Name, func(t *testing.T) {
			cfg, err := p.apply(base)
			if err != nil {
				t.Fatalf("apply: %v", err)
			}
			Normalize(&cfg)
			if err := cfg.Validate(); err != nil {
				t.Errorf("preset %s produced an invalid config: %v", p.Name, err)
			}
		})
	}
}

func TestStrictPresetDisablesAllThreeWindowsAndSurvivesNormalize(t *testing.T) {
	t.Parallel()
	base := Default()
	strict, _ := PresetByName("strict")
	cfg, err := strict.apply(base)
	if err != nil {
		t.Fatal(err)
	}
	Normalize(&cfg)

	if cfg.VPN.SwitchWindow != Disabled {
		t.Errorf("SwitchWindow = %v, want Disabled", cfg.VPN.SwitchWindow)
	}
	if cfg.VPN.RedialWindow != Disabled {
		t.Errorf("RedialWindow = %v, want Disabled", cfg.VPN.RedialWindow)
	}
	if cfg.VPN.PauseMax != Disabled {
		t.Errorf("PauseMax = %v, want Disabled", cfg.VPN.PauseMax)
	}
}

// TestBalancedPresetMatchesDefault pins that the shipped defaults and the
// middle preset can never silently disagree.
func TestBalancedPresetMatchesDefault(t *testing.T) {
	t.Parallel()
	def := Default()
	Normalize(&def)

	balanced, ok := PresetByName("balanced")
	if !ok {
		t.Fatal("no \"balanced\" preset")
	}
	if drift := PresetDrift(&def, balanced); len(drift) != 0 {
		t.Errorf("Default() drifts from Balanced: %+v", drift)
	}
}

func TestPresetDriftEmptyForExactMatch(t *testing.T) {
	t.Parallel()
	base := Default()
	relaxed, _ := PresetByName("relaxed")
	cfg, err := relaxed.apply(base)
	if err != nil {
		t.Fatal(err)
	}
	Normalize(&cfg)

	if drift := PresetDrift(&cfg, relaxed); len(drift) != 0 {
		t.Errorf("drift = %+v, want none (cfg was built from this exact preset)", drift)
	}
}

func TestPresetDriftNamesExactlyTheDivergentKeys(t *testing.T) {
	t.Parallel()
	base := Default()
	balanced, _ := PresetByName("balanced")
	cfg, err := balanced.apply(base)
	if err != nil {
		t.Fatal(err)
	}
	Normalize(&cfg)

	// Diverge exactly one preset key from Balanced.
	cfg.Hysteresis = 9

	drift := PresetDrift(&cfg, balanced)
	if len(drift) != 1 {
		t.Fatalf("drift = %+v, want exactly 1 changed key", drift)
	}
	if drift[0].Key != "hysteresis" {
		t.Errorf("drift[0].Key = %q, want \"hysteresis\"", drift[0].Key)
	}
}

func TestMatchPresetReportsCustomWhenDrifted(t *testing.T) {
	t.Parallel()
	base := Default()
	balanced, _ := PresetByName("balanced")
	cfg, err := balanced.apply(base)
	if err != nil {
		t.Fatal(err)
	}
	Normalize(&cfg)
	cfg.Hysteresis = 9 // one key off from every preset

	if name, exact := MatchPreset(&cfg); exact {
		t.Errorf("MatchPreset = (%q, true), want a Custom (no exact match)", name)
	}
}

func TestMatchPresetFindsExactMatch(t *testing.T) {
	t.Parallel()
	def := Default()
	Normalize(&def)
	if name, exact := MatchPreset(&def); !exact || name != "balanced" {
		t.Errorf("MatchPreset(Default()) = (%q, %v), want (\"balanced\", true)", name, exact)
	}
}

func TestPresetByNameIsCaseInsensitive(t *testing.T) {
	t.Parallel()
	if _, ok := PresetByName("STRICT"); !ok {
		t.Error("PresetByName(\"STRICT\") not found")
	}
	if _, ok := PresetByName("nonexistent"); ok {
		t.Error("PresetByName(\"nonexistent\") unexpectedly found")
	}
}

// A user who lowers vpn.advanced.switchWindowMax by hand puts some presets out
// of reach — Relaxed's 30s switch window cannot be written under a 10s cap.
// PresetConflicts must say so up front, naming both sides, rather than leaving
// the user to decode Validate's "exceeds vpn.advanced.switchWindowMax" at apply
// time. And the preset must NOT resolve it by raising the cap: the cap is the
// operator's own ceiling on a sanctioned relaxation of the guard.
func TestPresetConflictsAgainstLoweredAdvancedCaps(t *testing.T) {
	t.Parallel()
	base := Default()
	Normalize(&base)
	if got := PresetConflicts(&base, mustPreset(t, "relaxed")); len(got) != 0 {
		t.Fatalf("relaxed conflicts at shipped defaults: %v", got)
	}

	lowered := base
	lowered.VPN.Advanced.SwitchWindowMax = 10 * time.Second
	got := PresetConflicts(&lowered, mustPreset(t, "relaxed"))
	if len(got) != 1 {
		t.Fatalf("PresetConflicts = %v, want exactly one conflict", got)
	}
	for _, want := range []string{"vpn.switchWindow", "vpn.advanced.switchWindowMax", "30s", "10s"} {
		if !strings.Contains(got[0], want) {
			t.Errorf("conflict %q does not name %q", got[0], want)
		}
	}
	// Strict disables the windows outright, which no cap can forbid — a
	// tightening is always appliable.
	if c := PresetConflicts(&lowered, mustPreset(t, "strict")); len(c) != 0 {
		t.Errorf("strict conflicts under a lowered cap: %v", c)
	}

	// The redial cap is separate and must be reported separately — the two
	// windows deliberately never share a ceiling.
	lowered2 := base
	lowered2.VPN.Advanced.RedialWindowMax = time.Minute
	got2 := PresetConflicts(&lowered2, mustPreset(t, "relaxed"))
	if len(got2) != 1 || !strings.Contains(got2[0], "vpn.advanced.redialWindowMax") {
		t.Errorf("PresetConflicts = %v, want one redialWindowMax conflict", got2)
	}
}

// A preset never widens a cap: neither advanced cap may appear in presetKeys,
// or applying a "strictness" macro would silently raise a ceiling the operator
// set by hand.
func TestPresetsNeverSetTheAdvancedCaps(t *testing.T) {
	t.Parallel()
	for _, k := range presetKeys {
		if k == "vpn.advanced.switchWindowMax" || k == "vpn.advanced.redialWindowMax" {
			t.Errorf("presetKeys contains %q — a preset must never widen a cap the operator set", k)
		}
	}
}

func mustPreset(t *testing.T, name string) Preset {
	t.Helper()
	p, ok := PresetByName(name)
	if !ok {
		t.Fatalf("preset %q not found", name)
	}
	return p
}
