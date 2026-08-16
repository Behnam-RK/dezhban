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
