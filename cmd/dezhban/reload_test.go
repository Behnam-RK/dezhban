package main

import (
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/behnam-rk/dezhban/internal/config"
)

// A field added to LiveSettings but forgotten here would arrive as its zero
// value on every reload — a window silently re-enabled, a poll interval reset —
// so the mapping has to be provably complete rather than eyeballed. The config
// is populated explicitly instead of using defaults, since a default that
// happens to be false or zero would hide exactly the omission being hunted.
func TestLiveSettingsFromMapsEveryField(t *testing.T) {
	cfg := config.Default()
	cfg.PollInterval = 17 * time.Second
	cfg.BlockedCountries = []string{"IR"}
	cfg.Hysteresis = 2
	cfg.VPN.AutoDetect = true
	cfg.VPN.AllowPhysicalDNS = true
	cfg.VPN.AllowLocalNetwork = true
	cfg.VPN.AutoArm = true
	cfg.VPN.SwitchWindow = 5 * time.Second
	cfg.VPN.RedialWindow = 30 * time.Second
	cfg.VPN.PauseMax = 30 * time.Minute
	cfg.VPN.EndpointRefresh = time.Minute
	cfg.VPN.EndpointGrace = 15 * time.Minute
	cfg.Control.AllowSwitchOps = true
	cfg.Control.AllowPauseOps = true
	cfg.VPN.Advanced.SwitchWindowMax = 3 * time.Minute
	cfg.VPN.Advanced.RedialWindowMax = 10 * time.Minute
	cfg.VPN.Advanced.RedialMinUptime = 15 * time.Second
	cfg.VPN.Advanced.RedialBudget = 2 * time.Minute
	cfg.VPN.Advanced.RedialBudgetWindow = 15 * time.Minute
	cfg.VPN.Advanced.WindowDiscoveryInterval = time.Second
	cfg.VPN.Advanced.VerifyInterval = time.Minute
	cfg.VPN.Advanced.LivenessRedial = true

	got := reflect.ValueOf(liveSettingsFrom(&cfg))
	typ := got.Type()
	for i := range got.NumField() {
		if got.Field(i).IsZero() {
			t.Errorf("liveSettingsFrom left %s at its zero value; it is missing from the mapping", typ.Field(i).Name)
		}
	}
}

// The disabled sentinel has to survive the trip to the run loop. If it were
// coerced back to a default here, a user who deliberately turned a window off
// would have it quietly turned back on by the next config edit.
func TestLiveSettingsFromPreservesDisabledWindows(t *testing.T) {
	cfg := config.Default()
	cfg.VPN.SwitchWindow = config.Disabled
	cfg.VPN.RedialWindow = config.Disabled
	cfg.VPN.PauseMax = config.Disabled

	ls := liveSettingsFrom(&cfg)
	if ls.SwitchWindow > 0 || ls.RedialWindow > 0 || ls.PauseMax > 0 {
		t.Errorf("a disabled window survived as enabled: switch=%v redial=%v pause=%v",
			ls.SwitchWindow, ls.RedialWindow, ls.PauseMax)
	}
}

// Adopting a fresh Decider resets the in-progress agreement streak, so a reload
// that touched neither of its inputs must not hand one over: an unrelated config
// edit would otherwise cancel an escalation to FULL BLOCK (or a recovery) that
// real readings were already counting toward, and a caller writing settings once
// per poll interval could defer a flip indefinitely.
func TestDeciderRebuiltOnlyWhenItsInputsChange(t *testing.T) {
	base := config.Default()
	base.BlockedCountries = []string{"IR", "RU"}
	base.Hysteresis = 2

	cases := []struct {
		name string
		edit func(*config.Config)
		want bool
	}{
		{"nothing", func(*config.Config) {}, false},
		{"an unrelated live key", func(c *config.Config) { c.VPN.AllowLocalNetwork = !c.VPN.AllowLocalNetwork }, false},
		{"an unrelated restart key", func(c *config.Config) { c.LogLevel = "debug" }, false},
		{"the country list", func(c *config.Config) { c.BlockedCountries = []string{"IR"} }, true},
		{"country order", func(c *config.Config) { c.BlockedCountries = []string{"RU", "IR"} }, true},
		{"hysteresis", func(c *config.Config) { c.Hysteresis = 3 }, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cur := base
			cur.BlockedCountries = slices.Clone(base.BlockedCountries)
			tc.edit(&cur)
			if got := deciderChanged(&base, &cur); got != tc.want {
				t.Errorf("deciderChanged after changing %s = %v, want %v", tc.name, got, tc.want)
			}
		})
	}
}

// The macOS app decides whether to offer a restart by scanning `config set`'s
// stdout for this exact prefix (ConfigApply.pendingRestartKeys), deliberately, so
// the live/restart classification lives only in the daemon. That makes the wording
// a contract: reword it and the app silently stops offering the restart, reporting
// a key that is still waiting on one as fully applied.
func TestRestartMarkerIsTheContractTheAppScrapes(t *testing.T) {
	// Duplicated verbatim from ConfigApply.swift. Swift cannot import a Go const,
	// so this is the seam where the two are held together.
	const asScrapedBySwift = "Restart dezhban to apply:"
	if restartMarker != asScrapedBySwift {
		t.Fatalf("restartMarker = %q, but the app scans for %q — update ConfigApply.pendingRestartKeys in the same change",
			restartMarker, asScrapedBySwift)
	}

	// And the marker has to actually appear, with its keys, in both shapes the app
	// can encounter: a mixed outcome and a restart-only one.
	mixed := captureStdout(t, func() {
		reportWriteOutcome([]string{"pollInterval"}, []string{"logLevel", "providers"})
	})
	if !strings.Contains(mixed, asScrapedBySwift+" logLevel, providers") {
		t.Errorf("mixed outcome did not carry the marker and its keys:\n%s", mixed)
	}
	if !strings.Contains(mixed, "Saved and applied: pollInterval") {
		t.Errorf("mixed outcome did not report the applied keys:\n%s", mixed)
	}

	restartOnly := captureStdout(t, func() {
		reportWriteOutcome(nil, []string{"logLevel"})
	})
	if !strings.Contains(restartOnly, asScrapedBySwift+" logLevel") {
		t.Errorf("restart-only outcome did not carry the marker and its keys:\n%s", restartOnly)
	}

	// Nothing changed must not mention a restart at all, or the app would prompt
	// for one that buys nothing.
	quiet := captureStdout(t, func() { reportWriteOutcome(nil, nil) })
	if strings.Contains(quiet, asScrapedBySwift) {
		t.Errorf("a no-op write advertised a pending restart:\n%s", quiet)
	}
}
