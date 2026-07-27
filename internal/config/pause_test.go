package config

import (
	"strings"
	"testing"
	"time"
)

// The offered lengths must be usable as typed: a surface passes Value straight
// to `dezhban pause`, so a Value that does not parse back to its own Duration
// would silently grant a different pause than the label promised.
func TestPauseOptionValuesRoundTrip(t *testing.T) {
	c := Default()
	Normalize(&c)
	for _, o := range PauseOptions(&c) {
		got, err := time.ParseDuration(o.Value)
		if err != nil {
			t.Errorf("%s: Value %q does not parse: %v", o.Label, o.Value, err)
			continue
		}
		if got != o.Duration {
			t.Errorf("%s: Value %q parses to %v, want %v", o.Label, o.Value, got, o.Duration)
		}
		if o.Why == "" {
			t.Errorf("%s: no reason given; a pause length should be chosen on the need, not the number", o.Label)
		}
	}
}

// Over-cap options are LISTED and explained, never hidden and never shortened.
// Hiding them teaches the user their cap is something other than it is.
func TestPauseOptionsMarkOverCapWithoutHiding(t *testing.T) {
	c := Default()
	Normalize(&c)
	c.VPN.PauseMax = 20 * time.Minute

	opts := PauseOptions(&c)
	if len(opts) != len(pauseOptions) {
		t.Fatalf("PauseOptions returned %d of %d — an over-cap option was hidden", len(opts), len(pauseOptions))
	}
	for _, o := range opts {
		overCap := o.Duration > c.VPN.PauseMax
		if overCap && o.Unavailable == "" {
			t.Errorf("%s is above the cap but is offered as available", o.Label)
		}
		if !overCap && o.Unavailable != "" {
			t.Errorf("%s is within the cap but is marked unavailable: %s", o.Label, o.Unavailable)
		}
		if overCap && !strings.Contains(o.Unavailable, "20m") {
			t.Errorf("%s: %q does not name the cap, so the user cannot tell what to change",
				o.Label, o.Unavailable)
		}
	}
}

// With pausing off, every option is unavailable and says why — the reason is
// the disabled setting, not the length.
func TestPauseOptionsWhenPausingIsDisabled(t *testing.T) {
	c := Default()
	Normalize(&c)
	c.VPN.PauseMax = Disabled

	for _, o := range PauseOptions(&c) {
		if o.Unavailable == "" {
			t.Errorf("%s is offered even though pausing is disabled", o.Label)
		}
		if !strings.Contains(o.Unavailable, "pauseMax") {
			t.Errorf("%s: %q does not name the setting that disabled it", o.Label, o.Unavailable)
		}
	}
}

// PauseRefusal is where "clamp nothing silently" lives: an over-cap request is
// refused and explained rather than quietly shortened.
func TestPauseRefusal(t *testing.T) {
	c := Default()
	Normalize(&c)
	c.VPN.PauseMax = 30 * time.Minute

	if got := PauseRefusal(&c, 15*time.Minute); got != "" {
		t.Errorf("a within-cap pause was refused: %s", got)
	}
	if got := PauseRefusal(&c, 30*time.Minute); got != "" {
		t.Errorf("a pause exactly at the cap was refused: %s", got)
	}
	// Unspecified: the daemon picks its own default, which is its business.
	if got := PauseRefusal(&c, 0); got != "" {
		t.Errorf("an unspecified duration was refused: %s", got)
	}

	over := PauseRefusal(&c, time.Hour)
	if over == "" {
		t.Fatal("an over-cap pause was accepted; it would have been silently shortened")
	}
	for _, want := range []string{"1h", "30m", "vpn.pauseMax"} {
		if !strings.Contains(over, want) {
			t.Errorf("refusal %q does not mention %q — it must name what was asked, the cap, and the key",
				over, want)
		}
	}

	c.VPN.PauseMax = Disabled
	if got := PauseRefusal(&c, 5*time.Minute); got == "" {
		t.Error("a pause was accepted with pausing disabled")
	}
}
