package config

import (
	"testing"
	"time"
)

func changeFor(t *testing.T, changes []Change, key string) Change {
	t.Helper()
	for _, ch := range changes {
		if ch.Key == key {
			return ch
		}
	}
	t.Fatalf("no change reported for %q; got %v", key, changes)
	return Change{}
}

// An unedited config must produce no changes at all. If this drifts, every
// reload would report a pile of phantom edits and the user could never tell a
// real one from noise.
func TestChangesEmptyForIdenticalConfigs(t *testing.T) {
	a, b := Default(), Default()
	if got := Changes(&a, &b); len(got) != 0 {
		t.Fatalf("Changes on identical configs = %v, want none", got)
	}
}

// The two halves of the answer: what changed, and whether the running daemon can
// actually adopt it.
func TestChangesClassifiesLiveAndRestartRequired(t *testing.T) {
	old := Default()
	cur := Default()
	cur.PollInterval = 42 * time.Second // live: the run loop owns the geo ticker
	cur.LogLevel = "debug"              // restart: the logger is wired before the loop

	changes := Changes(&old, &cur)
	if len(changes) != 2 {
		t.Fatalf("Changes = %v, want exactly the two edited keys", changes)
	}
	// Sorted by key, so the ordering itself is part of the contract.
	if changes[0].Key != "logLevel" || changes[1].Key != "pollInterval" {
		t.Errorf("changes not sorted by key: %v", changes)
	}

	poll := changeFor(t, changes, "pollInterval")
	if poll.NeedsRestart() {
		t.Errorf("pollInterval reported as needing a restart (%q); the run loop owns its ticker", poll.RestartReason)
	}
	if poll.From != old.PollInterval.String() || poll.To != "42s" {
		t.Errorf("pollInterval change = %q → %q, want %q → \"42s\"", poll.From, poll.To, old.PollInterval)
	}

	level := changeFor(t, changes, "logLevel")
	if !level.NeedsRestart() {
		t.Error("logLevel reported as live-appliable; the logger is built before the run loop starts")
	}

	live, needRestart := SplitByRestart(changes)
	if len(live) != 1 || live[0].Key != "pollInterval" {
		t.Errorf("live changes = %v, want just pollInterval", live)
	}
	if len(needRestart) != 1 || needRestart[0].Key != "logLevel" {
		t.Errorf("restart-required changes = %v, want just logLevel", needRestart)
	}
}

// The disabled sentinel is an explicit opt-out, so it has to read as one. Left
// as a raw duration it renders "-1ns", which tells a user nothing and looks like
// corruption in a reload report.
func TestKeyValuesRendersDisabledWindowsAsOff(t *testing.T) {
	c := Default()
	c.VPN.SwitchWindow = Disabled
	c.VPN.ReconnectWindow = Disabled
	c.VPN.PauseMax = Disabled

	kv := KeyValues(&c)
	for _, key := range []string{"vpn.switchWindow", "vpn.reconnectWindow", "vpn.pauseMax"} {
		if kv[key] != "off" {
			t.Errorf("%s rendered as %q, want \"off\"", key, kv[key])
		}
	}
}

// Turning a window off is a security-relevant edit, so it must show up as a
// change like any other rather than being swallowed.
func TestChangesReportsDisablingAWindow(t *testing.T) {
	old := Default()
	cur := Default()
	cur.VPN.ReconnectWindow = Disabled

	ch := changeFor(t, Changes(&old, &cur), "vpn.reconnectWindow")
	if ch.To != "off" {
		t.Errorf("disabling the reconnect window reported %q, want \"off\"", ch.To)
	}
	if ch.NeedsRestart() {
		t.Errorf("disabling the reconnect window reported as needing a restart (%q)", ch.RestartReason)
	}
}

// Every key must be deliberately classified. An unclassified key silently
// defaults to restart-required, which is safe but wrong to leave in place — this
// is the test that makes someone decide.
func TestEveryKeyIsClassifiedExactlyOnce(t *testing.T) {
	c := Default()
	for key := range KeyValues(&c) {
		_, restart := restartReasons[key]
		live := liveKeys[key]
		switch {
		case restart && live:
			t.Errorf("key %q is classified as both live and restart-required", key)
		case !restart && !live:
			t.Errorf("key %q is unclassified; add it to liveKeys or restartReasons", key)
		}
	}
	for key := range restartReasons {
		if _, ok := KeyValues(&c)[key]; !ok {
			t.Errorf("restartReasons names %q, which is not a real config key", key)
		}
	}
	for key := range liveKeys {
		if _, ok := KeyValues(&c)[key]; !ok {
			t.Errorf("liveKeys names %q, which is not a real config key", key)
		}
	}
}

// Every restart reason is shown to a user, so an empty one would render as a
// blank explanation next to a setting that silently did not take effect.
func TestEveryRestartReasonExplainsItself(t *testing.T) {
	for key, reason := range restartReasons {
		if reason == "" {
			t.Errorf("key %q is restart-required with no reason given", key)
		}
	}
}

// MergeLive has to move exactly the live keys and nothing else. If it moved a
// restart-required key too, the daemon's idea of its running config would drift
// from what it is actually enforcing, and the key would stop being reported as
// pending — the user would never learn a restart was still owed.
func TestMergeLiveMovesOnlyLiveKeys(t *testing.T) {
	base := Default()

	cur := Default()
	cur.PollInterval = 99 * time.Second                     // live
	cur.VPN.ReconnectWindow = Disabled                      // live, and security-relevant
	cur.Control.AllowPauseOps = !base.Control.AllowPauseOps // live
	cur.LogLevel = "debug"                                  // restart-required
	cur.VPN.ArmAtBoot = !base.VPN.ArmAtBoot                 // restart-required
	cur.Providers = []string{"https://ifconfig.co/json"}    // restart-required

	got := Changes(&base, MergeLive(&base, &cur))

	want := map[string]bool{
		"pollInterval":          true,
		"vpn.reconnectWindow":   true,
		"control.allowPauseOps": true,
	}
	for _, ch := range got {
		if !want[ch.Key] {
			t.Errorf("MergeLive adopted %q, which is not live-appliable", ch.Key)
		}
		delete(want, ch.Key)
	}
	for key := range want {
		t.Errorf("MergeLive did not adopt the live key %q", key)
	}
}

// Every key MergeLive is willing to move must be one classified as live, and
// every live key must actually be moved. This is what keeps the field-by-field
// copy above honest against the liveKeys table beside it.
func TestMergeLiveCoversExactlyTheLiveKeys(t *testing.T) {
	base := Default()

	// A config that differs from base in every single key.
	cur := Default()
	cur.PollInterval = base.PollInterval + time.Second
	cur.BlockedCountries = []string{"ZZ"}
	cur.Hysteresis = base.Hysteresis + 1
	cur.VPN.Autodetect = !base.VPN.Autodetect
	cur.VPN.AllowPhysicalDNS = !base.VPN.AllowPhysicalDNS
	cur.VPN.AllowLocalNetwork = !base.VPN.AllowLocalNetwork
	cur.VPN.AutoArm = !base.VPN.AutoArm
	cur.VPN.SwitchWindow = base.VPN.SwitchWindow + time.Second
	cur.VPN.ReconnectWindow = base.VPN.ReconnectWindow + time.Second
	cur.VPN.PauseMax = base.VPN.PauseMax + time.Second
	cur.VPN.EndpointRefresh = base.VPN.EndpointRefresh + time.Second
	cur.VPN.EndpointGrace = base.VPN.EndpointGrace + time.Second
	cur.Control.AllowSwitchOps = !base.Control.AllowSwitchOps
	cur.Control.AllowPauseOps = !base.Control.AllowPauseOps
	cur.VPN.Advanced.SwitchWindowMax = base.VPN.Advanced.SwitchWindowMax + time.Second
	cur.VPN.Advanced.ReconnectWindowMax = base.VPN.Advanced.ReconnectWindowMax + time.Second
	cur.VPN.Advanced.ReconnectMinUptime = base.VPN.Advanced.ReconnectMinUptime + time.Second
	cur.VPN.Advanced.WindowDiscoveryInterval = base.VPN.Advanced.WindowDiscoveryInterval + time.Second

	moved := map[string]bool{}
	for _, ch := range Changes(&base, MergeLive(&base, &cur)) {
		moved[ch.Key] = true
	}
	for key := range liveKeys {
		if !moved[key] {
			t.Errorf("live key %q is never adopted by MergeLive", key)
		}
	}
	for key := range moved {
		if !liveKeys[key] {
			t.Errorf("MergeLive adopts %q, which is not classified as live", key)
		}
	}
}
