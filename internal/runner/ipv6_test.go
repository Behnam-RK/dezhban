package runner

import (
	"context"
	"errors"
	"net/netip"
	"sync/atomic"
	"testing"
	"time"

	"github.com/behnam-rk/dezhban/internal/netdetect"
	"github.com/behnam-rk/dezhban/internal/state"
)

// The public-IPv6 observation must ride a confirmed ALLOWED reading in healthy
// GUARD, land in the snapshot, and hold its slow cadence — one lookup, not one
// per reading. It feeds no decision, so a posture must never move on it.
func TestIPv6ObservedInHealthyGuardOnCadence(t *testing.T) {
	be := &fakeBackend{}
	mon := &countingMonitor{cc: "US"} // allowed exit → healthy GUARD
	tun := &scriptedWatcher{}
	o := recoveryOpts(be, mon, tun.watcher())

	var v6calls atomic.Int64
	o.LookupIPv6 = func(context.Context) (netip.Addr, error) {
		v6calls.Add(1)
		return netip.MustParseAddr("2001:db8::42"), nil
	}

	snaps := make(chan state.Snapshot, 64)
	o.Publish = func(s state.Snapshot) {
		select {
		case snaps <- s:
		default:
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- Run(ctx, o) }()

	tun.send(t, netdetect.TunnelState{Up: true, Names: []string{"utun4"}, Detail: "connected"})
	if !waitFor(t, snaps, func(s state.Snapshot) bool { return s.Posture == "guard" && s.IPv6 == "2001:db8::42" }) {
		t.Fatal("snapshot never carried the observed IPv6 address in healthy GUARD")
	}
	if n := v6calls.Load(); n != 1 {
		t.Errorf("LookupIPv6 called %d times, want exactly 1 (fixed cadence, and readings inside it must not re-trigger)", n)
	}
	cancel()
	<-done
}

// In FULL BLOCK the lookup must not run at all — the provider pass does not
// cover the v6 endpoints (ADR-0006) — and a pre-block address must not be
// shown as current.
func TestIPv6NeverLookedUpInFullBlock(t *testing.T) {
	be := &fakeBackend{}
	mon := &countingMonitor{cc: "IR"} // blocked exit → FULL BLOCK
	tun := &scriptedWatcher{}
	o := recoveryOpts(be, mon, tun.watcher())

	var v6calls atomic.Int64
	o.LookupIPv6 = func(context.Context) (netip.Addr, error) {
		v6calls.Add(1)
		return netip.MustParseAddr("2001:db8::42"), nil
	}

	snaps := make(chan state.Snapshot, 64)
	o.Publish = func(s state.Snapshot) {
		select {
		case snaps <- s:
		default:
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- Run(ctx, o) }()

	tun.send(t, netdetect.TunnelState{Up: true, Names: []string{"utun4"}, Detail: "connected"})
	if !waitFor(t, snaps, func(s state.Snapshot) bool { return s.Posture == "full-block" }) {
		t.Fatal("never reached FULL BLOCK on a blocked exit")
	}
	if n := v6calls.Load(); n != 0 {
		t.Errorf("LookupIPv6 called %d times under FULL BLOCK, want 0", n)
	}
	cancel()
	<-done
}

// A tunnel-down edge invalidates the observation, and invalidating it must also
// RESCHEDULE it. Clearing alone left the 5-minute timer from the last successful
// lookup running, so the row stayed blank until that timer happened to expire —
// and on a host that drops more often than the interval, it was never populated
// again at all. The value is re-observed on the first confirmed ALLOWED reading
// after the re-arm floor, not five minutes later.
func TestIPv6ReObservedAfterATunnelDropInvalidatesIt(t *testing.T) {
	// The floor exists to stop a flapping tunnel forcing a lookup per flap; the
	// property under test is that a re-arm happens at all, so shrink it to keep
	// the test in real time.
	defer func(prev time.Duration) { ipv6RearmFloor = prev }(ipv6RearmFloor)
	ipv6RearmFloor = 10 * time.Millisecond

	be := &fakeBackend{}
	mon := &countingMonitor{cc: "US"} // allowed exit → healthy GUARD
	tun := &scriptedWatcher{}
	o := recoveryOpts(be, mon, tun.watcher())
	// The observation rides a reading, and recoveryOpts polls hourly on purpose.
	// Poll fast here so the reading after the redial arrives inside the test —
	// the property under test is the re-arm, not the poll cadence.
	o.Interval = 50 * time.Millisecond

	// A second address, so the re-observation is distinguishable from a stale
	// value that was simply never cleared.
	var v6calls atomic.Int64
	o.LookupIPv6 = func(context.Context) (netip.Addr, error) {
		if v6calls.Add(1) == 1 {
			return netip.MustParseAddr("2001:db8::42"), nil
		}
		return netip.MustParseAddr("2001:db8::99"), nil
	}

	snaps := make(chan state.Snapshot, 64)
	o.Publish = func(s state.Snapshot) {
		select {
		case snaps <- s:
		default:
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- Run(ctx, o) }()

	tun.send(t, netdetect.TunnelState{Up: true, Names: []string{"utun4"}, Detail: "connected"})
	if !waitFor(t, snaps, func(s state.Snapshot) bool { return s.IPv6 == "2001:db8::42" }) {
		t.Fatal("never observed the first IPv6 address in healthy GUARD")
	}

	tun.send(t, netdetect.TunnelState{Up: false, Names: []string{"utun4"}, Detail: "down"})
	if !waitFor(t, snaps, func(s state.Snapshot) bool { return s.IPv6 == "" }) {
		t.Fatal("the tunnel-down edge did not clear the observed IPv6 address")
	}

	tun.send(t, netdetect.TunnelState{Up: true, Names: []string{"utun4"}, Detail: "connected"})
	if !waitFor(t, snaps, func(s state.Snapshot) bool { return s.IPv6 == "2001:db8::99" }) {
		t.Fatalf("IPv6 was never re-observed after the redial (%d lookups) — invalidation cleared the value without rescheduling it", v6calls.Load())
	}
	cancel()
	<-done
}

// A failed lookup leaves the field empty and everything else untouched — it is
// an observation, and its failure is not even a warning.
func TestIPv6LookupFailureIsInvisible(t *testing.T) {
	be := &fakeBackend{}
	mon := &countingMonitor{cc: "US"}
	tun := &scriptedWatcher{}
	o := recoveryOpts(be, mon, tun.watcher())
	o.LookupIPv6 = func(context.Context) (netip.Addr, error) {
		return netip.Addr{}, errors.New("no v6 route")
	}

	snaps := make(chan state.Snapshot, 64)
	o.Publish = func(s state.Snapshot) {
		select {
		case snaps <- s:
		default:
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- Run(ctx, o) }()

	tun.send(t, netdetect.TunnelState{Up: true, Names: []string{"utun4"}, Detail: "connected"})
	if !waitFor(t, snaps, func(s state.Snapshot) bool { return s.Posture == "guard" }) {
		t.Fatal("never reached healthy GUARD")
	}
	// Allow one more publish to flow through, then assert emptiness held.
	deadline := time.After(time.Second)
	for {
		select {
		case s := <-snaps:
			if s.IPv6 != "" {
				t.Fatalf("snapshot carries IPv6 %q from a failed lookup", s.IPv6)
			}
			if s.LookupErr != "" {
				t.Fatalf("a failed v6 observation surfaced as LookupErr %q", s.LookupErr)
			}
		case <-deadline:
			cancel()
			<-done
			return
		}
	}
}
