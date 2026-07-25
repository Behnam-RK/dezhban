package config

import "testing"

func TestPresetsAreWellFormed(t *testing.T) {
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
	def := Default()
	Normalize(&def)
	if name, exact := MatchPreset(&def); !exact || name != "balanced" {
		t.Errorf("MatchPreset(Default()) = (%q, %v), want (\"balanced\", true)", name, exact)
	}
}

func TestPresetByNameIsCaseInsensitive(t *testing.T) {
	if _, ok := PresetByName("STRICT"); !ok {
		t.Error("PresetByName(\"STRICT\") not found")
	}
	if _, ok := PresetByName("nonexistent"); ok {
		t.Error("PresetByName(\"nonexistent\") unexpectedly found")
	}
}
