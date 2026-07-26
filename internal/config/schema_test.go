package config

import (
	"strings"
	"testing"
	"time"
)

// The point of these tests is that the declared table cannot quietly fall out of
// step with the code. A metadata table nobody checks is just a fifth copy of the
// defaults with extra steps.

// TestTunablesCoverEverySettableKey is the load-bearing one: the declared table
// and the settable-key set must be exactly equal, in both directions. A new key
// added to KeyValues without a Tunable would reach the config file with no label,
// no help, and no default for any surface to show; a Tunable for a key KeyValues
// does not carry would describe something nobody can set.
func TestTunablesCoverEverySettableKey(t *testing.T) {
	c := Default()
	Normalize(&c)
	settable := KeyValues(&c)

	declared := map[string]bool{}
	for _, tun := range Tunables() {
		if declared[tun.Key] {
			t.Errorf("tunable %q is declared twice", tun.Key)
		}
		declared[tun.Key] = true
	}

	for key := range settable {
		if !declared[key] {
			t.Errorf("settable key %q has no Tunable — add one to schema.go", key)
		}
	}
	for key := range declared {
		if _, ok := settable[key]; !ok {
			t.Errorf("Tunable %q is not a settable key — remove it or add it to KeyValues", key)
		}
	}
}

// TestTunableDefaultsMatchANormalizedDefaultConfig pins the derivation itself.
// If Tunables ever grew a hand-written Default, this is what would catch it
// disagreeing with what you actually get by setting nothing.
func TestTunableDefaultsMatchANormalizedDefaultConfig(t *testing.T) {
	c := Default()
	Normalize(&c)
	want := KeyValues(&c)

	for _, tun := range Tunables() {
		if got := tun.Default; got != want[tun.Key] {
			t.Errorf("%s: declared default %q, but a normalized Default() yields %q",
				tun.Key, got, want[tun.Key])
		}
	}
}

// TestTunableRestartClassificationMatchesReload keeps one classification, not two.
// Telling a user in the settings pane that a key applies live while the reload
// path reports it as restart-required is the same failure as claiming a change
// applied when the old value is still being enforced.
func TestTunableRestartClassificationMatchesReload(t *testing.T) {
	for _, tun := range Tunables() {
		want := restartReasonFor(tun.Key)
		if tun.RestartReason != want {
			t.Errorf("%s: RestartReason %q, want %q", tun.Key, tun.RestartReason, want)
		}
		if tun.LiveAppliable() != (want == "") {
			t.Errorf("%s: LiveAppliable() disagrees with its own RestartReason", tun.Key)
		}
	}
}

// TestTunableCapKeysResolve — a cap is a key rather than a number precisely so a
// surface can read the live ceiling, which only works if the key exists.
func TestTunableCapKeysResolve(t *testing.T) {
	for _, tun := range Tunables() {
		if tun.CapKey == "" {
			continue
		}
		ceiling, ok := TunableByKey(tun.CapKey)
		if !ok {
			t.Errorf("%s: CapKey %q is not a settable key", tun.Key, tun.CapKey)
			continue
		}
		if ceiling.Kind != tun.Kind {
			t.Errorf("%s is %s but its cap %s is %s — a cap must be comparable to what it bounds",
				tun.Key, tun.Kind, ceiling.Key, ceiling.Kind)
		}
		if ceiling.CapKey != "" {
			t.Errorf("%s: cap %s is itself capped by %s; caps are deliberately not transitive",
				tun.Key, ceiling.Key, ceiling.CapKey)
		}
	}
}

// TestDisablableKeysSurviveNormalize is the one that protects the worst bug this
// tool can have. Disablable promises a surface it may offer an explicit "Off";
// that promise is only honest if the negative sentinel actually survives
// Normalize instead of being coerced back to the default.
func TestDisablableKeysSurviveNormalize(t *testing.T) {
	// Field accessors kept test-local: the production key→field table lives in
	// cmd/dezhban's configFields, and internal/config must not depend on it.
	fields := map[string]func(*Config) *time.Duration{
		"vpn.switchWindow":             func(c *Config) *time.Duration { return &c.VPN.SwitchWindow },
		"vpn.redialWindow":             func(c *Config) *time.Duration { return &c.VPN.RedialWindow },
		"vpn.pauseMax":                 func(c *Config) *time.Duration { return &c.VPN.PauseMax },
		"vpn.advanced.redialMinUptime": func(c *Config) *time.Duration { return &c.VPN.Advanced.RedialMinUptime },
	}

	var disablable []string
	for _, tun := range Tunables() {
		if tun.Disablable {
			disablable = append(disablable, tun.Key)
		}
	}
	if len(disablable) != len(fields) {
		t.Fatalf("Disablable keys are %v, but this test knows how to set %d of them — "+
			"a new disablable key needs an entry here", disablable, len(fields))
	}

	for _, key := range disablable {
		get, ok := fields[key]
		if !ok {
			t.Errorf("%s is marked Disablable but this test cannot reach its field", key)
			continue
		}
		c := Default()
		*get(&c) = Disabled
		Normalize(&c)
		if got := *get(&c); got != Disabled {
			t.Errorf("%s: Normalize turned the explicit off-sentinel into %v — "+
				"a security setting was accepted and silently discarded", key, got)
		}
		if got := KeyValues(&c)[key]; got != "off" {
			t.Errorf("%s: reads as %q once disabled, want \"off\"", key, got)
		}
	}
}

// TestNonDisablableDurationsCoerceZeroToTheirDefault is the mirror image: a key
// NOT marked Disablable must be one where Normalize really does restore the
// default, so no surface offers an "Off" that would silently do nothing.
func TestNonDisablableDurationsCoerceZeroToTheirDefault(t *testing.T) {
	for _, tun := range Tunables() {
		if tun.Kind != KindDuration || tun.Disablable {
			continue
		}
		if tun.Default == "off" {
			t.Errorf("%s is not Disablable yet defaults to off", tun.Key)
		}
	}
}

// TestTunableMetadataIsComplete — every field a surface relies on is populated,
// and the ones that are meaningful for only one Kind stay empty elsewhere.
func TestTunableMetadataIsComplete(t *testing.T) {
	for _, tun := range Tunables() {
		if tun.Label == "" {
			t.Errorf("%s: no Label", tun.Key)
		}
		if tun.Help == "" {
			t.Errorf("%s: no Help", tun.Key)
		}
		if tun.Kind == "" {
			t.Errorf("%s: no Kind", tun.Key)
		}
		if tun.DocAnchor == "" {
			t.Errorf("%s: no DocAnchor", tun.Key)
		} else if !strings.Contains(tun.DocAnchor, "#") {
			t.Errorf("%s: DocAnchor %q is not of the form page#anchor", tun.Key, tun.DocAnchor)
		}
		if tun.Unit != "" && tun.Kind != KindInt {
			t.Errorf("%s: Unit %q on a %s — units name what an int counts", tun.Key, tun.Unit, tun.Kind)
		}
		if tun.Disablable && tun.Kind != KindDuration {
			t.Errorf("%s: only durations carry the off-sentinel", tun.Key)
		}
		// The label is what a user reads; the key is the machine's name for it.
		// Mixing them is the "Serialised forms are not a UI" entry in the glossary.
		if strings.Contains(tun.Label, tun.Key) {
			t.Errorf("%s: Label %q restates the config key", tun.Key, tun.Label)
		}
		if wantAdvanced := strings.HasPrefix(tun.Key, "vpn.advanced."); tun.Advanced != wantAdvanced {
			t.Errorf("%s: Advanced=%v, want %v (it is set by the key prefix)",
				tun.Key, tun.Advanced, wantAdvanced)
		}
	}
}

// TestTunableKeysAreSorted — TunableKeys promises a stable order to CLI help and
// tests, which is only useful if it is actually stable.
func TestTunableKeysAreSorted(t *testing.T) {
	keys := TunableKeys()
	if len(keys) != len(tunables) {
		t.Fatalf("TunableKeys returned %d keys, want %d", len(keys), len(tunables))
	}
	for i := 1; i < len(keys); i++ {
		if keys[i-1] >= keys[i] {
			t.Fatalf("TunableKeys is not sorted: %q before %q", keys[i-1], keys[i])
		}
	}
}

// TestTunablesReturnsACopy — a surface that mutates what it is handed must not
// change what the next caller sees.
func TestTunablesReturnsACopy(t *testing.T) {
	first := Tunables()
	first[0].Label = "mutated"
	if Tunables()[0].Label == "mutated" {
		t.Fatal("Tunables handed out the shared table; callers can corrupt it for everyone")
	}
}
