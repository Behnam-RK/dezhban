package runner

import (
	"context"
	"net/netip"
	"sync"
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

// A liveness-redial attempt refused by the budget must still be retried once
// it refills — WITHOUT the tunnel ever reporting down. Unlike an ordinary
// drop, a zombie streak's tunnel stays up for the whole episode, so
// retryAutoWindow's guard must recognise a standing zombie streak as "the
// drop is still open", not just tunnelUp == false, or a refused
// liveness-redial attempt would never get a second chance.
func TestAZombieRefusedRedialRetriesWithoutTheTunnelGoingDown(t *testing.T) {
	be := &fakeBackend{}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	var (
		mu    sync.Mutex
		snaps []state.Snapshot
	)
	o := Options{
		// The lookup always fails — that is what makes it a zombie, and it
		// means no confirmed exit ever closes a window early: every window
		// costs its full grant, which is what makes the budget reachable
		// inside a test's lifetime.
		Monitor:        steadyFailMonitor{},
		Decider:        decision.New([]string{"IR"}, 2),
		Backend:        be,
		Log:            discardLog(),
		Interval:       time.Millisecond,
		Tunnels:        []string{"utun4"},
		Endpoints:      []netip.Addr{netip.MustParseAddr("203.0.113.7")},
		Watcher:        edgeWatcher(100000), // interface reports up for the WHOLE run — no down edge, ever
		LivenessRedial: true,
		RedialWindow:   20 * time.Millisecond,
		// Room for one full window and no more, refilling 120ms after the
		// first window's cost is recorded — same shape as
		// TestARefusedRedialRetriesWhenTheBudgetRefills, but with no down edge
		// to drive the second attempt.
		RedialBudget:       25 * time.Millisecond,
		RedialBudgetWindow: 120 * time.Millisecond,
		Publish: func(s state.Snapshot) {
			mu.Lock()
			defer mu.Unlock()
			snaps = append(snaps, s)
		},
	}
	if err := Run(ctx, o); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()

	// A refusal must have been published, or the test proved nothing: the
	// first crossing has to have been granted and spent the budget already.
	refusedAt := -1
	for i, s := range snaps {
		if s.Redial != nil {
			refusedAt = i
			break
		}
	}
	if refusedAt < 0 {
		t.Fatal("no redial refusal was ever published; the budget never ran out and this fixture tests nothing")
	}

	reopened := false
	for _, s := range snaps[refusedAt:] {
		if s.Switch == nil || !s.Switch.Open {
			continue
		}
		if s.Switch.Trigger != state.TriggerAuto {
			t.Errorf("window after the refusal has trigger %q, want %q — the retry must stay trigger 2",
				s.Switch.Trigger, state.TriggerAuto)
		}
		reopened = true
		if s.Redial != nil {
			t.Errorf("a window is open but state.redial still reports %q — "+
				"exactly one of the two may be present", s.Redial.Reason)
		}
		break
	}
	if !reopened {
		t.Error("the refused zombie-redial attempt never got a window once the budget refilled — " +
			"retryAutoWindow's tunnelUp guard is refusing a retry the streak has earned")
	}

	// The whole point: no tunnel-down edge ever happened. Confirms the retry
	// above was earned by the zombie streak, not by an ordinary drop/recovery
	// this fixture never produced.
	for _, s := range snaps {
		if s.Drop != nil {
			t.Fatalf("a tunnel drop was recorded; this fixture's tunnel must never go down: %+v", *s.Drop)
		}
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
