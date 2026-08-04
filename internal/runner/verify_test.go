package runner

import (
	"context"
	"errors"
	"net/netip"
	"testing"
	"time"

	"github.com/behnam-rk/dezhban/internal/command"
	"github.com/behnam-rk/dezhban/internal/decision"
	"github.com/behnam-rk/dezhban/internal/state"
)

// Every other Apply in the run loop is triggered by something dezhban itself
// did. Enforcement verification is the one path that notices a ruleset removed
// from OUTSIDE the daemon and puts it back — these tests pin that behaviour
// directly, plus the two ways it must NOT act: an unreadable backend, and the
// key turned off.

// A missing ruleset must be re-applied, and the repair must show up in the
// published snapshot so an observer can see it happened.
func TestVerifyTickRepairsMissingRules(t *testing.T) {
	var calls int
	be := &fakeBackend{isBlockedFn: func() (bool, error) {
		calls++
		return calls > 1, nil // first check: missing; every check after: present
	}}

	var snaps []state.Snapshot
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	o := Options{
		Monitor:        steadyMonitor{cc: "US"}, // allowed exit: guard holds steady throughout
		Decider:        decision.New([]string{"IR"}, 1),
		Backend:        be,
		Log:            discardLog(),
		Interval:       50 * time.Millisecond,
		Tunnels:        []string{"utun4"},
		Endpoints:      []netip.Addr{netip.MustParseAddr("203.0.113.7")},
		VerifyInterval: 10 * time.Millisecond,
		Publish:        func(s state.Snapshot) { snaps = append(snaps, s) },
	}
	if err := Run(ctx, o); err != nil {
		t.Fatal(err)
	}

	if calls < 2 {
		t.Fatalf("IsBlocked called %d times, want at least 2 (one missing, one clean)", calls)
	}

	guards := 0
	for _, c := range be.calls {
		if c == "apply-guard" {
			guards++
		}
	}
	if guards < 2 {
		t.Errorf("apply-guard count = %d, want at least 2 (startup + repair); calls = %v", guards, be.calls)
	}

	var sawMissing, sawClearedAfter bool
	for _, s := range snaps {
		if s.Verify != nil && s.Verify.Missing {
			sawMissing = true
			continue
		}
		if sawMissing && s.Verify == nil {
			sawClearedAfter = true
		}
	}
	if !sawMissing {
		t.Error("no published snapshot reported the missing ruleset")
	}
	if !sawClearedAfter {
		t.Error("Verify was never cleared by a later clean check")
	}
}

// Enforcement verification finding the rules gone must re-apply even an
// UNRESTRICTED switch window's policy — the one case reapplyWindow's own
// ordinary reason (a tunnel/endpoint change) would skip, since an unrestricted
// window already passes everything and no such change ever needs to touch it.
// Verification's reason is different: the pass itself vanished along with the
// rest of the ruleset, so reapplyCurrent's force path has to reach it anyway,
// or the host would sit open behind a window while the daemon logged a repair.
func TestVerifyTickRepairsAnOpenUnrestrictedWindow(t *testing.T) {
	var calls int
	be := &fakeBackend{isBlockedFn: func() (bool, error) {
		calls++
		return calls > 1, nil // first check: missing; every check after: present
	}}

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	o := Options{
		Monitor:         steadyMonitor{cc: "US"},
		Decider:         decision.New([]string{"IR"}, 1),
		Backend:         be,
		Log:             discardLog(),
		Interval:        time.Hour, // no geo ticks needed; the window stays open throughout
		Tunnels:         []string{"utun4"},
		Endpoints:       []netip.Addr{netip.MustParseAddr("203.0.113.7")},
		SwitchWindow:    5 * time.Second, // outlives the test; no WindowProtocols/Ports set → unrestricted
		SwitchWindowMax: time.Minute,
		CommandPoll:     5 * time.Millisecond,
		PollCommand:     scriptedCommands(command.Command{Op: command.OpOpenSwitchWindow}),
		VerifyInterval:  10 * time.Millisecond,
	}
	if err := Run(ctx, o); err != nil {
		t.Fatal(err)
	}

	if calls < 2 {
		t.Fatalf("IsBlocked called %d times, want at least 2 (one missing, one clean)", calls)
	}

	var switches int
	for _, c := range be.calls {
		if c == "apply-switch" {
			switches++
		}
	}
	if switches < 2 {
		t.Fatalf("apply-switch count = %d, want at least 2 (the initial open, plus verification's repair "+
			"of the unrestricted window's vanished pass); calls = %v", switches, be.calls)
	}
}

// An unreadable backend is not evidence the rules are gone — the daemon must
// report it and change nothing, the same discipline as an undeterminable exit
// country holding the current posture.
func TestVerifyTickHoldsOnReadError(t *testing.T) {
	readErr := errors.New("pfctl: no such process")
	be := &fakeBackend{isBlockedFn: func() (bool, error) { return false, readErr }}

	var snaps []state.Snapshot
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	o := Options{
		Monitor:        steadyMonitor{cc: "US"},
		Decider:        decision.New([]string{"IR"}, 1),
		Backend:        be,
		Log:            discardLog(),
		Interval:       50 * time.Millisecond,
		Tunnels:        []string{"utun4"},
		Endpoints:      []netip.Addr{netip.MustParseAddr("203.0.113.7")},
		VerifyInterval: 10 * time.Millisecond,
		Publish:        func(s state.Snapshot) { snaps = append(snaps, s) },
	}
	if err := Run(ctx, o); err != nil {
		t.Fatal(err)
	}

	guards := 0
	for _, c := range be.calls {
		if c == "apply-guard" {
			guards++
		}
	}
	if guards != 1 {
		t.Errorf("apply-guard count = %d, want exactly 1 (startup only) — a read error must never trigger a repair; calls = %v", guards, be.calls)
	}

	var sawErr bool
	for _, s := range snaps {
		if s.Verify != nil && s.Verify.Err != "" {
			sawErr = true
			if s.Verify.Missing {
				t.Error("a read error must not also be reported as Missing")
			}
		}
	}
	if !sawErr {
		t.Error("no published snapshot reported the read error")
	}
}

// vpn.advanced.verifyInterval: "0" must actually turn verification off, not
// merely slow it down — the same "0 is an explicit opt-out" discipline as the
// three relaxation windows.
func TestVerifyIntervalDisabledNeverChecks(t *testing.T) {
	be := &fakeBackend{isBlockedFn: func() (bool, error) {
		t.Fatal("IsBlocked called with verification disabled")
		return true, nil
	}}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	o := Options{
		Monitor:        steadyMonitor{cc: "US"},
		Decider:        decision.New([]string{"IR"}, 1),
		Backend:        be,
		Log:            discardLog(),
		Interval:       50 * time.Millisecond,
		Tunnels:        []string{"utun4"},
		Endpoints:      []netip.Addr{netip.MustParseAddr("203.0.113.7")},
		VerifyInterval: -1, // the Disabled sentinel, however the caller spells it
	}
	if err := Run(ctx, o); err != nil {
		t.Fatal(err)
	}
}
