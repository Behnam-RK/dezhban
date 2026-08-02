package runner

import (
	"context"
	"net/netip"
	"testing"
	"time"

	"github.com/behnam-rk/dezhban/internal/decision"
	"github.com/behnam-rk/dezhban/internal/state"
)

// dezhban's posture never escalates on a lookup failure alone — an unknown
// exit country HOLDS the current posture rather than flipping it (see
// decision.Evaluate). So a tunnel that reports up but has stopped passing
// traffic stayed correctly cut, forever, with no signal to anyone. These tests
// pin the diagnosis (always on) separately from the relaxation it MAY trigger
// (opt-in, off by default) — the two halves of docs/adr/0010-tunnel-liveness.md.

// A run of failed exit checks through an up tunnel must be reported once it
// reaches the Decider's own hysteresis count, and — with the default config —
// must never open a redial window on its own. Detecting is not the same as
// acting.
func TestZombieStreakReportedButRedialStaysOffByDefault(t *testing.T) {
	be := &fakeBackend{}
	var snaps []state.Snapshot
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	o := Options{
		Monitor:   steadyFailMonitor{}, // every exit check fails, like a censoring exit or a hung tunnel
		Decider:   decision.New([]string{"IR"}, 2),
		Backend:   be,
		Log:       discardLog(),
		Interval:  15 * time.Millisecond,
		Tunnels:   []string{"utun4"},
		Endpoints: []netip.Addr{netip.MustParseAddr("203.0.113.7")},
		Watcher:   edgeWatcher(100000), // interface reports up for the whole run
		Publish:   func(s state.Snapshot) { snaps = append(snaps, s) },
		// LivenessRedial left at its zero value: off.
	}
	if err := Run(ctx, o); err != nil {
		t.Fatal(err)
	}

	var sawZombie bool
	for _, s := range snaps {
		if s.Zombie != nil && s.Zombie.Checks >= 2 {
			sawZombie = true
		}
	}
	if !sawZombie {
		t.Fatal("no published snapshot reported the zombie streak reaching the hysteresis count")
	}

	for _, c := range be.calls {
		if c == "apply-switch" {
			t.Fatalf("a redial window opened with livenessRedial off; calls = %v", be.calls)
		}
	}
}

// The same streak, with vpn.advanced.livenessRedial on, must open an automatic
// redial window through the EXISTING trigger-2 machinery — this is that
// trigger widening what counts as "down", not a fourth trigger, so it has to
// land on the same apply-switch path an ordinary tunnel drop uses.
func TestZombieStreakOpensRedialWindowWhenEnabled(t *testing.T) {
	be := &fakeBackend{}
	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	o := Options{
		Monitor:            steadyFailMonitor{},
		Decider:            decision.New([]string{"IR"}, 2),
		Backend:            be,
		Log:                discardLog(),
		Interval:           15 * time.Millisecond,
		Tunnels:            []string{"utun4"},
		Endpoints:          []netip.Addr{netip.MustParseAddr("203.0.113.7")},
		Watcher:            edgeWatcher(100000),
		LivenessRedial:     true,
		RedialWindow:       30 * time.Millisecond,
		RedialBudget:       testRedialBudget,
		RedialBudgetWindow: testRedialBudgetWindow,
	}
	if err := Run(ctx, o); err != nil {
		t.Fatal(err)
	}

	var switches int
	for _, c := range be.calls {
		if c == "apply-switch" {
			switches++
		}
	}
	if switches == 0 {
		t.Fatalf("no redial window opened with livenessRedial on; calls = %v", be.calls)
	}
}

// A tunnel that plainly reports down must never be reported as a zombie — that
// is a different, already-explained state (the guard holding a downed tunnel),
// and conflating the two would blur two distinct diagnoses into one.
func TestPlainlyDownTunnelIsNeverReportedAsZombie(t *testing.T) {
	be := &fakeBackend{}
	var snaps []state.Snapshot
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	o := Options{
		Monitor:   steadyFailMonitor{},
		Decider:   decision.New([]string{"IR"}, 1),
		Backend:   be,
		Log:       discardLog(),
		Interval:  15 * time.Millisecond,
		Tunnels:   []string{"utun4"},
		Endpoints: []netip.Addr{netip.MustParseAddr("203.0.113.7")},
		Watcher:   downWatcher(), // interface reports down for the whole run
		Publish:   func(s state.Snapshot) { snaps = append(snaps, s) },
	}
	if err := Run(ctx, o); err != nil {
		t.Fatal(err)
	}
	for _, s := range snaps {
		if s.Zombie != nil {
			t.Fatalf("a plainly-down tunnel was reported as a zombie: %+v", *s.Zombie)
		}
	}
}
