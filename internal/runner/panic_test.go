package runner

import (
	"context"
	"net/netip"
	"sync/atomic"
	"testing"
	"time"

	"github.com/behnam-rk/dezhban/internal/control"
	"github.com/behnam-rk/dezhban/internal/decision"
	"github.com/behnam-rk/dezhban/internal/state"
)

// `dezhban panic` tears down every rule directly, as root, independent of the
// daemon. These tests pin the two rules that keep the daemon from silently
// undoing that: every AUTOMATIC re-apply path must stand down while
// PanicDisarmed reports true, and every EXPLICIT operator command must clear
// the marker (never be blocked by it) — exactly like OpUnblock already did
// before this change.

// fakePanicMarker is a minimal PanicDisarmed/ClearPanicDisarm pair for tests:
// an atomic flag plus a call counter for the clear, so assertions can check
// both "did it stand down" and "did it clear" without a real state-dir file.
type fakePanicMarker struct {
	disarmed atomic.Bool
	clears   atomic.Int64
}

func (m *fakePanicMarker) Disarmed() bool { return m.disarmed.Load() }
func (m *fakePanicMarker) Clear() error {
	m.clears.Add(1)
	m.disarmed.Store(false)
	return nil
}

// A tunnel drop from healthy GUARD must NOT open the automatic redial window
// while panic-disarmed — the marker must fully suppress trigger 2, both the
// drop edge (maybeAutoWindow) and the bound-lifted retry (retryAutoWindow),
// via autoWindowPossible.
func TestVPNAutoRedialWindowSuppressedWhilePanicDisarmed(t *testing.T) {
	be := &fakeBackend{}
	m := &fakePanicMarker{}
	m.disarmed.Store(true)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	o := Options{
		Monitor:            steadyMonitor{cc: "US"},
		Decider:            decision.New([]string{"IR"}, 1),
		Backend:            be,
		Log:                discardLog(),
		Interval:           time.Millisecond,
		Tunnels:            []string{"utun4"},
		Endpoints:          []netip.Addr{netip.MustParseAddr("203.0.113.7")},
		Watcher:            edgeWatcher(5), // clean up->down edge ~5ms in
		RedialWindow:       50 * time.Millisecond,
		RedialBudget:       testRedialBudget,
		RedialBudgetWindow: testRedialBudgetWindow,
		PanicDisarmed:      m.Disarmed,
		ClearPanicDisarm:   m.Clear,
	}
	if err := Run(ctx, o); err != nil {
		t.Fatal(err)
	}
	if containsCall(be.calls, "apply-switch") {
		t.Fatalf("automatic redial window opened while panic-disarmed; calls=%v", be.calls)
	}
	if m.clears.Load() != 0 {
		t.Errorf("ClearPanicDisarm called %d times; an automatic trigger must never clear the marker itself", m.clears.Load())
	}
}

// A blocked-country reading must not drive the geo state machine's Apply
// calls while panic-disarmed — vpnGeoStep (and its lift-and-probe fallback)
// must never run at all.
func TestVPNGeoStateMachineSuppressedWhilePanicDisarmed(t *testing.T) {
	be := &fakeBackend{}
	m := &fakePanicMarker{}
	m.disarmed.Store(true)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	o := Options{
		Monitor:          steadyMonitor{cc: "IR"}, // blocked country every reading
		Decider:          decision.New([]string{"IR"}, 1),
		Backend:          be,
		Log:              discardLog(),
		Interval:         time.Millisecond,
		Tunnels:          []string{"utun4"},
		Endpoints:        []netip.Addr{netip.MustParseAddr("203.0.113.7")},
		PanicDisarmed:    m.Disarmed,
		ClearPanicDisarm: m.Clear,
	}
	if err := Run(ctx, o); err != nil {
		t.Fatal(err)
	}
	if containsCall(be.calls, "apply-fullblock") {
		t.Fatalf("geo state machine applied FULL BLOCK while panic-disarmed; calls=%v", be.calls)
	}
}

// A tunnel-set change (autodetect growth) must not re-apply the standing
// guard while panic-disarmed — reapplyStanding must stand down, same as
// enforcement verification already does.
func TestVPNTunnelChangeReapplySuppressedWhilePanicDisarmed(t *testing.T) {
	be := &fakeBackend{}
	m := &fakePanicMarker{}
	m.disarmed.Store(true)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	o := Options{
		Monitor:          steadyMonitor{cc: "US"},
		Decider:          decision.New([]string{"IR"}, 1),
		Backend:          be,
		Log:              discardLog(),
		Interval:         time.Hour, // suppress geoTick; isolate the tunnel-change path
		AutoDetect:       true,
		Tunnels:          []string{"utun4"},
		Endpoints:        []netip.Addr{netip.MustParseAddr("203.0.113.7")},
		Watcher:          growWatcher(), // {utun4} -> {utun4,utun6}
		PanicDisarmed:    m.Disarmed,
		ClearPanicDisarm: m.Clear,
	}
	if err := Run(ctx, o); err != nil {
		t.Fatal(err)
	}
	for _, p := range be.policies {
		for _, ifc := range p.TunnelIfaces {
			if ifc == "utun6" {
				t.Fatalf("standing guard re-applied with the grown tunnel set while panic-disarmed; policies=%v", be.policies)
			}
		}
	}
}

// An explicit `block` (control socket) is an operator engaging with
// enforcement, so it must clear a standing panic-disarm marker unconditionally
// and never be refused because of it.
func TestControlOpBlockClearsPanicMarker(t *testing.T) {
	be := &fakeBackend{}
	m := &fakePanicMarker{}
	m.disarmed.Store(true)
	o := vpnOpts(be)
	o.PanicDisarmed = m.Disarmed
	o.ClearPanicDisarm = m.Clear
	path := startControlled(t, o)

	resp := do(t, path, control.Request{Op: control.OpBlock})
	if !resp.OK {
		t.Fatalf("block over the control socket failed while panic-disarmed: %+v", resp)
	}
	if m.clears.Load() == 0 {
		t.Error("ClearPanicDisarm was never called by an explicit block")
	}
	if !containsCall(be.calls, "apply-fullblock") {
		t.Errorf("expected apply-fullblock; calls=%v", be.calls)
	}
}

// An explicit `switch` open and an explicit `pause` (control socket) are both
// operator commands — each must clear a standing panic-disarm marker via
// openWindow's shared choke point, and must never be refused because of it.
func TestControlSwitchAndPauseClearPanicMarker(t *testing.T) {
	for _, tc := range []struct {
		name string
		op   control.Op
	}{
		{"switch", control.OpOpenSwitch},
		{"pause", control.OpPause},
	} {
		t.Run(tc.name, func(t *testing.T) {
			be := &fakeBackend{}
			m := &fakePanicMarker{}
			m.disarmed.Store(true)
			o := vpnOpts(be)
			o.PanicDisarmed = m.Disarmed
			o.ClearPanicDisarm = m.Clear
			path := startControlled(t, o)

			resp := do(t, path, control.Request{Op: tc.op})
			if !resp.OK {
				t.Fatalf("%s over the control socket failed while panic-disarmed: %+v", tc.name, resp)
			}
			if m.clears.Load() == 0 {
				t.Errorf("ClearPanicDisarm was never called by an explicit %s", tc.name)
			}
			if !containsCall(be.calls, "apply-switch") {
				t.Errorf("expected apply-switch; calls=%v", be.calls)
			}
		})
	}
}

// An automatic redial window's own expiry timer firing while panic-disarmed
// must NOT reinstate rules — but the window's bookkeeping still closes (its
// clock is genuinely up), so a later snapshot must not keep reporting it open.
func TestVPNWindowExpiresWithoutReapplyWhilePanicDisarmed(t *testing.T) {
	be := &fakeBackend{}
	start := time.Now()
	const disarmAfter = 15 * time.Millisecond // after the window opens (~5ms), before it expires (~25ms)
	var lastSnap state.Snapshot
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	o := Options{
		Monitor:            steadyMonitor{cc: "US"},
		Decider:            decision.New([]string{"IR"}, 1),
		Backend:            be,
		Log:                discardLog(),
		Interval:           time.Millisecond,
		Tunnels:            []string{"utun4"},
		Endpoints:          []netip.Addr{netip.MustParseAddr("203.0.113.7")},
		Watcher:            edgeWatcher(5),
		RedialWindow:       20 * time.Millisecond,
		RedialBudget:       testRedialBudget,
		RedialBudgetWindow: testRedialBudgetWindow,
		PanicDisarmed:      func() bool { return time.Since(start) > disarmAfter },
		Publish:            func(s state.Snapshot) { lastSnap = s },
	}
	if err := Run(ctx, o); err != nil {
		t.Fatal(err)
	}
	if !containsCall(be.calls, "apply-switch") {
		t.Fatalf("expected the window to open before the marker was set; calls=%v", be.calls)
	}
	seenSwitch := false
	for _, c := range be.calls {
		if c == "apply-switch" {
			seenSwitch = true
			continue
		}
		if seenSwitch && c == "apply-guard" {
			t.Fatalf("window revert re-applied rules after expiry while panic-disarmed; calls=%v", be.calls)
		}
	}
	if lastSnap.Switch != nil && lastSnap.Switch.Open {
		t.Error("final snapshot still reports the switch window open past its expired deadline")
	}
}

// An explicit cancel (control socket) of an open window must clear a standing
// panic-disarm marker and still perform the revert — an operator interacting
// with the window is never blocked by the marker, even mid-window.
func TestControlOpCancelSwitchClearsMarkerAndReverts(t *testing.T) {
	be := &fakeBackend{}
	m := &fakePanicMarker{}
	o := vpnOpts(be)
	o.PanicDisarmed = m.Disarmed
	o.ClearPanicDisarm = m.Clear
	// Long enough that the test's own cancel — not the window's own deadline —
	// is what closes it.
	o.SwitchWindow = time.Minute
	path := startControlled(t, o)

	resp := do(t, path, control.Request{Op: control.OpOpenSwitch})
	if !resp.OK {
		t.Fatalf("open switch failed: %+v", resp)
	}
	// Simulate an operator running `dezhban panic` mid-window: the daemon's own
	// windowActive bookkeeping does not know the rules were just torn down out
	// from under it.
	m.disarmed.Store(true)
	m.clears.Store(0)

	resp = do(t, path, control.Request{Op: control.OpCancelSwitch})
	if !resp.OK {
		t.Fatalf("cancel switch failed while panic-disarmed: %+v", resp)
	}
	if m.clears.Load() == 0 {
		t.Error("ClearPanicDisarm was never called by an explicit cancel")
	}
	if !applyGuardAfterSwitch(be.calls) {
		t.Errorf("expected a guard-apply revert after the explicit cancel; calls=%v", be.calls)
	}
}
