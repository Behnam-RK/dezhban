package runner

import (
	"context"
	"net/netip"
	"reflect"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/behnam-rk/dezhban/internal/command"
	"github.com/behnam-rk/dezhban/internal/decision"
	"github.com/behnam-rk/dezhban/internal/firewall"
	"github.com/behnam-rk/dezhban/internal/monitor"
	"github.com/behnam-rk/dezhban/internal/netdetect"
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
		Interval:     time.Hour,
		Decider:      decision.New([]string{"IR"}, 1),
		SwitchWindow: time.Minute, // would be a longer window — for the NEXT one
		RedialWindow: time.Minute,
	}

	o := Options{
		Monitor:      steadyMonitor{cc: "US"},
		Decider:      decision.New([]string{"IR"}, 1),
		Backend:      be,
		Log:          discardLog(),
		Interval:     time.Hour,
		Tunnels:      []string{"utun4"},
		Endpoints:    []netip.Addr{netip.MustParseAddr("203.0.113.7")},
		SwitchWindow: 5 * time.Second,
		RedialWindow: 30 * time.Second,
		ReloadC:      reloadC,
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
		AllowPhysicalDNS:        true,
		AllowLocalNetwork:       true,
		AutoArm:                 true,
		SwitchWindow:            5 * time.Second,
		SwitchWindowMax:         3 * time.Minute,
		RedialWindow:            30 * time.Second,
		RedialWindowMax:         10 * time.Minute,
		RedialMinUptime:         15 * time.Second,
		PauseMax:                30 * time.Minute,
		WindowDiscoveryInterval: time.Second,
		EndpointRefresh:         time.Minute,
		EndpointGrace:           15 * time.Minute,
		AllowSwitchOps:          true,
		AllowPauseOps:           true,
		AllowConfigOps:          true,
	}

	got := reflect.ValueOf(o.Live())
	typ := got.Type()
	for i := 0; i < got.NumField(); i++ {
		if got.Field(i).IsZero() {
			t.Errorf("Live() left %s at its zero value; it is missing from the mapping", typ.Field(i).Name)
		}
	}
}

// Being in LiveSettings is not the same as being adopted: the run loop used to
// snapshot several of these into locals at startup, so a reload reported the key
// as applied while every consumer kept using the value the daemon booted with.
// These tests pin the ones that were wrong, each through the behaviour it drives
// rather than through the field it sets.

// FULL BLOCK carries AllowLocalNetwork and AllowPhysicalDNS (see
// firewall.PolicyInput.FullBlock), and reapplyStanding deliberately declines to
// touch a blocked posture. So a reload that CLOSES one of those passes while the
// guard is cut has to re-install the block itself — otherwise the pass stays in the
// firewall while `config set` reports the key as applied, which is the one failure
// mode this project treats as unacceptable.
func TestTighteningAPassWhileBlockedIsEnforcedImmediately(t *testing.T) {
	be := &fakeBackend{}
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()

	reloadC := make(chan LiveSettings, 1)
	reloadC <- LiveSettings{
		Interval:          time.Hour,
		AllowLocalNetwork: false, // the change under test: on → off, while cut
	}

	o := Options{
		// A forbidden exit with hysteresis 1 puts the loop in FULL BLOCK on the
		// first reading, before the reload arrives.
		Monitor:           steadyMonitor{cc: "IR"},
		Decider:           decision.New([]string{"IR"}, 1),
		Backend:           be,
		Log:               discardLog(),
		Interval:          time.Millisecond,
		Tunnels:           []string{"utun4"},
		Endpoints:         []netip.Addr{netip.MustParseAddr("203.0.113.7")},
		AllowLocalNetwork: true,
		ReloadC:           reloadC,
	}
	if err := Run(ctx, o); err != nil {
		t.Fatal(err)
	}

	var tightened bool
	for _, p := range be.policies {
		if p.Mode == firewall.ModeFullBlock && !p.AllowLocalNetwork {
			tightened = true
		}
	}
	if !tightened {
		t.Errorf("no FULL BLOCK policy dropped the local-network pass after it was turned off; policies=%d", len(be.policies))
	}
}

// vpn.switchWindow and vpn.pauseMax are live-appliable in BOTH directions. The
// command poll used to be wired only when a window was enabled at startup, so a
// host booted in the strict zero-leak posture reported vpn.switchWindow as applied
// while the root-owned command path stayed deaf until a restart: a relaxation the
// operator explicitly asked for and did not get.
func TestReloadReEnablingAWindowRevivesTheCommandPath(t *testing.T) {
	be := &fakeBackend{}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	reloadC := make(chan LiveSettings, 1)
	reloadC <- LiveSettings{
		Interval:        time.Hour,
		SwitchWindow:    5 * time.Second, // the change under test: off → on
		SwitchWindowMax: time.Minute,
	}

	o := Options{
		Monitor:   steadyMonitor{cc: "US"},
		Decider:   decision.New([]string{"IR"}, 1),
		Backend:   be,
		Log:       discardLog(),
		Interval:  time.Hour, // the reload and the command poll are the only events
		Tunnels:   []string{"utun4"},
		Endpoints: []netip.Addr{netip.MustParseAddr("203.0.113.7")},
		// Disabled at startup: nothing may relax the guard until the reload lands.
		SwitchWindow: 0,
		PauseMax:     0,
		CommandPoll:  time.Millisecond,
		// Repeats, so the test does not depend on whether the reload or the first
		// tick wins the select — only on a command being available after the reload.
		PollCommand: func() (command.Command, bool) {
			return command.Command{Op: command.OpOpenSwitchWindow}, true
		},
		ReloadC: reloadC,
	}
	if err := Run(ctx, o); err != nil {
		t.Fatal(err)
	}
	if !hasCall(be.calls, "apply-switch") {
		t.Errorf("re-enabling vpn.switchWindow left the command path deaf; calls=%v", be.calls)
	}
}

// The mirror of the test above: with no reload, a disabled window must stay
// disabled however often a command arrives. Wiring the poll unconditionally must
// not turn "0" into a relaxation — that would be far worse than the bug it fixes.
func TestADisabledWindowIgnoresCommandsForever(t *testing.T) {
	be := &fakeBackend{}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()

	o := Options{
		Monitor:      steadyMonitor{cc: "US"},
		Decider:      decision.New([]string{"IR"}, 1),
		Backend:      be,
		Log:          discardLog(),
		Interval:     time.Hour,
		Tunnels:      []string{"utun4"},
		Endpoints:    []netip.Addr{netip.MustParseAddr("203.0.113.7")},
		SwitchWindow: 0,
		PauseMax:     0,
		CommandPoll:  time.Millisecond,
		PollCommand: func() (command.Command, bool) {
			return command.Command{Op: command.OpOpenSwitchWindow}, true
		},
	}
	if err := Run(ctx, o); err != nil {
		t.Fatal(err)
	}
	if hasCall(be.calls, "apply-switch") {
		t.Errorf("a disabled switch window was opened by a command file; calls=%v", be.calls)
	}
}

// vpn.endpointGrace decides how long an endpoint whose socket has vanished keeps
// its pass. It was snapshotted at startup, so shortening it — a tightening: fewer
// addresses reachable off-tunnel — was reported as applied while every
// reconciliation kept using the boot value.
//
// The loop starts with two endpoints and then sees a rotation: one of the original
// pair is gone and a new address has appeared. Only the grace decides whether the
// vanished one is carried along, so the size of the endpoint set the guard is
// re-applied with is exactly the observable.
func TestReloadedEndpointGraceBindsTheNextReconciliation(t *testing.T) {
	stale := netip.MustParseAddr("203.0.113.7")
	live := netip.MustParseAddr("203.0.113.8")
	rotated := netip.MustParseAddr("203.0.113.9")

	// widestGuardSet is the largest endpoint set the guard was ever applied with:
	// three means `stale` was retained by the grace, two means it expired.
	widestGuardSet := func(reload *LiveSettings) int {
		be := &fakeBackend{}
		ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
		defer cancel()

		var calls atomic.Int32
		o := Options{
			Monitor:  steadyMonitor{cc: "US"},
			Decider:  decision.New([]string{"IR"}, 1),
			Backend:  be,
			Log:      discardLog(),
			Interval: time.Hour, // endpoint refreshes are the only events that matter
			Tunnels:  []string{"utun4"},
			// First resolution sees the original pair; every later one has lost
			// `stale` and gained `rotated`. A loss alone is ignored on purpose (see
			// reconcileEndpoints), so the rotation is what makes the grace decide.
			ResolveEndpoints: func(context.Context) netdetect.EndpointSet {
				if calls.Add(1) == 1 {
					return netdetect.EndpointSet{Addrs: []netip.Addr{stale, live}}
				}
				return netdetect.EndpointSet{Addrs: []netip.Addr{live, rotated}}
			},
			EndpointRefresh: 5 * time.Millisecond,
			EndpointGrace:   time.Hour,
		}
		if reload != nil {
			ch := make(chan LiveSettings, 1)
			ch <- *reload
			o.ReloadC = ch
		}
		if err := Run(ctx, o); err != nil {
			t.Fatal(err)
		}
		widest := 0
		for _, p := range be.policies {
			if p.Mode == firewall.ModeGuard && len(p.VPNEndpoints) > widest {
				widest = len(p.VPNEndpoints)
			}
		}
		return widest
	}

	// Control: an hour of grace keeps the vanished endpoint alongside the rotation.
	if got := widestGuardSet(nil); got != 3 {
		t.Errorf("widest guard endpoint set = %d, want 3 (the vanished endpoint held by an hour of grace)", got)
	}
	// A reload shortening the grace has to bind the very next reconciliation.
	if got := widestGuardSet(&LiveSettings{Interval: time.Hour, EndpointGrace: time.Nanosecond}); got != 2 {
		t.Errorf("widest guard endpoint set = %d, want 2 — the reloaded endpoint grace never took effect", got)
	}
}

// vpn.advanced.windowDiscoveryInterval sets the in-window endpoint discovery
// cadence, and was likewise captured at startup — so the ticker for every later
// window still ran at the boot value. Here the boot value is an hour (no discovery
// can happen inside the test) and the reloaded one is a millisecond, so any
// re-resolution while the window is open proves the reload was adopted.
func TestReloadedWindowDiscoveryIntervalAppliesToTheNextWindow(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	reloadC := make(chan LiveSettings, 1)
	reloadC <- LiveSettings{
		Interval:                time.Hour,
		SwitchWindow:            5 * time.Second,
		SwitchWindowMax:         time.Minute,
		WindowDiscoveryInterval: time.Millisecond, // the change under test
	}

	var resolves atomic.Int32
	o := Options{
		Monitor:  steadyMonitor{cc: "US"},
		Decider:  decision.New([]string{"IR"}, 1),
		Backend:  &fakeBackend{},
		Log:      discardLog(),
		Interval: time.Hour,
		Tunnels:  []string{"utun4"},
		ResolveEndpoints: func(context.Context) netdetect.EndpointSet {
			resolves.Add(1)
			return netdetect.EndpointSet{Addrs: []netip.Addr{netip.MustParseAddr("203.0.113.7")}}
		},
		EndpointRefresh: time.Hour, // silence the other resolver, so the count is unambiguous
		// An hour at boot: if the ticker were built from this, in-window discovery
		// could not run even once inside the test's lifetime.
		WindowDiscoveryInterval: time.Hour,
		SwitchWindow:            5 * time.Second,
		CommandPoll:             time.Millisecond,
		PollCommand:             scriptedCommands(command.Command{Op: command.OpOpenSwitchWindow}),
		ReloadC:                 reloadC,
	}
	if err := Run(ctx, o); err != nil {
		t.Fatal(err)
	}
	// One resolution happens at startup; anything beyond it came from the in-window
	// discovery ticker.
	if got := resolves.Load(); got < 2 {
		t.Errorf("in-window discovery ran %d times (startup only); the reloaded interval was ignored", got)
	}
}
