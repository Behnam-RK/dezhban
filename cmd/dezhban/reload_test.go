package main

import (
	"reflect"
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
	cfg.VPN.Autodetect = true
	cfg.VPN.AllowPhysicalDNS = true
	cfg.VPN.AllowLocalNetwork = true
	cfg.VPN.AutoArm = true
	cfg.VPN.SwitchWindow = 5 * time.Second
	cfg.VPN.ReconnectWindow = 30 * time.Second
	cfg.VPN.PauseMax = 30 * time.Minute
	cfg.VPN.EndpointRefresh = time.Minute
	cfg.VPN.EndpointGrace = 15 * time.Minute
	cfg.Control.AllowSwitchOps = true
	cfg.Control.AllowPauseOps = true
	cfg.VPN.Advanced.SwitchWindowMax = 3 * time.Minute
	cfg.VPN.Advanced.ReconnectWindowMax = 10 * time.Minute
	cfg.VPN.Advanced.ReconnectMinUptime = 15 * time.Second
	cfg.VPN.Advanced.WindowDiscoveryInterval = time.Second

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
	cfg.VPN.ReconnectWindow = config.Disabled
	cfg.VPN.PauseMax = config.Disabled

	ls := liveSettingsFrom(&cfg)
	if ls.SwitchWindow > 0 || ls.ReconnectWindow > 0 || ls.PauseMax > 0 {
		t.Errorf("a disabled window survived as enabled: switch=%v reconnect=%v pause=%v",
			ls.SwitchWindow, ls.ReconnectWindow, ls.PauseMax)
	}
}
