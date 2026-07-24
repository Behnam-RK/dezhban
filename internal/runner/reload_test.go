package runner

import (
	"context"
	"net/netip"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/behnam-rk/dezhban/internal/decision"
	"github.com/behnam-rk/dezhban/internal/firewall"
	"github.com/behnam-rk/dezhban/internal/monitor"
)

func hasCall(calls []string, want string) bool {
	return slices.Contains(calls, want)
}

// The point of live reload: an edit reaches enforcement, not just bookkeeping.
// The loop starts with a country list that allows the observed exit and is handed
// one that forbids it, so the only way a FULL BLOCK can appear is if the reloaded
// decider is the one actually deciding.
func TestReloadedCountryListReachesEnforcement(t *testing.T) {
	be := &fakeBackend{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	reloadC := make(chan LiveSettings, 1)
	reloadC <- LiveSettings{
		Interval:         time.Millisecond,
		Decider:          decision.New([]string{"US"}, 1),
		BlockedCountries: []string{"US"},
	}

	o := Options{
		Monitor: &fakeMonitor{cancel: cancel, results: []monitor.Result{
			reading("US"), reading("US"), reading("US"),
		}},
		Decider:   decision.New([]string{"IR"}, 1), // US is allowed to begin with
		Backend:   be,
		Log:       discardLog(),
		Interval:  time.Millisecond,
		Tunnels:   []string{"utun4"},
		Endpoints: []netip.Addr{netip.MustParseAddr("203.0.113.7")},
		ReloadC:   reloadC,
	}
	if err := Run(ctx, o); err != nil {
		t.Fatal(err)
	}
	if !hasCall(be.calls, "apply-fullblock") {
		t.Errorf("no FULL BLOCK after reloading a country list that forbids the exit; calls=%v", be.calls)
	}
}

// A reload that changes the shape of the standing rules has to reinstall them.
// Left until the next unrelated re-apply, the daemon would keep enforcing the
// old rule set while every surface reported the new setting as applied.
func TestReloadReinstallsStandingRulesWhenPolicyChanges(t *testing.T) {
	be := &fakeBackend{}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	reloadC := make(chan LiveSettings, 1)
	reloadC <- LiveSettings{
		Interval:          time.Hour,
		Decider:           decision.New([]string{"IR"}, 1),
		AllowLocalNetwork: true, // the change under test
	}

	o := Options{
		Monitor:           steadyMonitor{cc: "US"},
		Decider:           decision.New([]string{"IR"}, 1),
		Backend:           be,
		Log:               discardLog(),
		Interval:          time.Hour, // suppress the geo tick; the reload is the only event
		Tunnels:           []string{"utun4"},
		Endpoints:         []netip.Addr{netip.MustParseAddr("203.0.113.7")},
		AllowLocalNetwork: false,
		ReloadC:           reloadC,
	}
	if err := Run(ctx, o); err != nil {
		t.Fatal(err)
	}

	var reapplied bool
	for _, p := range be.policies {
		if p.Mode == firewall.ModeGuard && p.AllowLocalNetwork {
			reapplied = true
		}
	}
	if !reapplied {
		t.Errorf("no guard policy carried the reloaded AllowLocalNetwork; policies=%d", len(be.policies))
	}
}

// A reload must never be able to lengthen a relaxation that is already open.
// Window durations are read when an episode opens, so a mid-episode reload can
// only affect the next one — this pins that the running loop keeps enforcing
// while a reload is in flight rather than tearing anything down.
func TestReloadLeavesTheStandingPostureIntact(t *testing.T) {
	be := &fakeBackend{}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()

	reloadC := make(chan LiveSettings, 1)
	reloadC <- LiveSettings{
		Interval:        time.Hour,
		Decider:         decision.New([]string{"IR"}, 1),
		SwitchWindow:    time.Minute, // would be a longer window — for the NEXT one
		ReconnectWindow: time.Minute,
	}

	o := Options{
		Monitor:         steadyMonitor{cc: "US"},
		Decider:         decision.New([]string{"IR"}, 1),
		Backend:         be,
		Log:             discardLog(),
		Interval:        time.Hour,
		Tunnels:         []string{"utun4"},
		Endpoints:       []netip.Addr{netip.MustParseAddr("203.0.113.7")},
		SwitchWindow:    5 * time.Second,
		ReconnectWindow: 30 * time.Second,
		ReloadC:         reloadC,
	}
	if err := Run(ctx, o); err != nil {
		t.Fatal(err)
	}
	// Nothing opened a window, so nothing may have relaxed the guard.
	if hasCall(be.calls, "apply-switch") {
		t.Errorf("a reload opened or altered a window; calls=%v", be.calls)
	}
	if !hasCall(be.calls, "apply-guard") {
		t.Errorf("the guard was not standing after a reload; calls=%v", be.calls)
	}
}

// Live() is the inverse of applying a LiveSettings, so a field added to the
// struct but forgotten in Live() would silently reset to its zero value on every
// reload — a disabled window quietly re-enabled, say. Reflection catches the
// omission that a hand-written comparison would not.
func TestLiveCapturesEveryLiveSetting(t *testing.T) {
	o := Options{
		Interval:                17 * time.Second,
		Decider:                 decision.New([]string{"IR"}, 2),
		BlockedCountries:        []string{"IR"},
		Autodetect:              true,
		AllowPhysicalDNS:        true,
		AllowLocalNetwork:       true,
		AutoArm:                 true,
		SwitchWindow:            5 * time.Second,
		SwitchWindowMax:         3 * time.Minute,
		ReconnectWindow:         30 * time.Second,
		ReconnectWindowMax:      10 * time.Minute,
		ReconnectMinUptime:      15 * time.Second,
		PauseMax:                30 * time.Minute,
		WindowDiscoveryInterval: time.Second,
		EndpointRefresh:         time.Minute,
		EndpointGrace:           15 * time.Minute,
		AllowSwitchOps:          true,
		AllowPauseOps:           true,
	}

	got := reflect.ValueOf(o.Live())
	typ := got.Type()
	for i := 0; i < got.NumField(); i++ {
		if got.Field(i).IsZero() {
			t.Errorf("Live() left %s at its zero value; it is missing from the mapping", typ.Field(i).Name)
		}
	}
}
