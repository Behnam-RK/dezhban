package runner

import (
	"context"
	"errors"
	"net/netip"
	"sync"
	"testing"
	"time"

	"github.com/behnam-rk/dezhban/internal/command"
	"github.com/behnam-rk/dezhban/internal/decision"
	"github.com/behnam-rk/dezhban/internal/monitor"
	"github.com/behnam-rk/dezhban/internal/state"
)

// scriptedZombieMonitor fails every lookup except the call at successAt
// (0-indexed), which succeeds — just enough to genuinely resolve a zombie
// streak (the same way a real recovered exit would) so a second, distinct
// streak can start immediately after, all without the tunnel interface
// itself ever reporting down.
type scriptedZombieMonitor struct {
	mu        sync.Mutex
	calls     int
	successAt int
}

func (m *scriptedZombieMonitor) Poll(ctx context.Context) <-chan monitor.Result {
	ch := make(chan monitor.Result)
	go func() { <-ctx.Done(); close(ch) }()
	return ch
}

func (m *scriptedZombieMonitor) Once(context.Context) (monitor.Reading, error) {
	m.mu.Lock()
	n := m.calls
	m.calls++
	m.mu.Unlock()
	if n == m.successAt {
		return monitor.Reading{CountryCode: "US"}, nil
	}
	return monitor.Reading{}, errors.New("lookup failed")
}

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
//
// Exactly ONE window, never more: the run's 400ms comfortably outlasts a
// 30ms window plus the ~30ms (Hysteresis=2 × 15ms interval) it takes the
// streak to re-cross the threshold once the window closes, so a version that
// reopens on every expiry (the bug resetZombie's full/partial split fixed —
// zombieRedialTried was being cleared just because a window was open, not
// because the hang had actually resolved) would open several here, not one.
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
	if switches != 1 {
		t.Fatalf("apply-switch called %d time(s) for one continuous, never-resolving zombie streak; "+
			"want exactly 1 — a streak's window expiring must never reopen a new one on its own. calls = %v",
			switches, be.calls)
	}
}

// A liveness-redial attempt refused by the budget must still be retried once
// it refills — WITHOUT the tunnel ever reporting down. Unlike an ordinary
// drop, a zombie streak's tunnel stays up for the whole episode, so
// retryAutoWindow's guard must recognise a standing zombie streak as "the
// drop is still open", not just tunnelUp == false, or a refused
// liveness-redial attempt would never get a second chance.
//
// A single continuous streak, though, gets at most ONE automatic attempt —
// its window expiring must never reopen a new one on its own (see
// resetZombie's full/partial split in runner.go, which fixed exactly that:
// an earlier version reset zombieRedialTried whenever a window was open,
// letting a still-hung tunnel reopen a window every expiry). So the refusal
// this test needs has to come from a SECOND, genuinely distinct streak
// spending a budget the FIRST streak's own grant already mostly used up —
// not from the same streak reattempting after its window closes.
func TestAZombieRefusedRedialRetriesWithoutTheTunnelGoingDown(t *testing.T) {
	be := &fakeBackend{}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	var (
		mu    sync.Mutex
		snaps []state.Snapshot
	)
	o := Options{
		// Fails every lookup except call index 3, which succeeds — just
		// enough to genuinely resolve the FIRST zombie streak right after its
		// window closes, so a SECOND streak starts immediately and is the one
		// that gets refused. Index 3, not 2: index 0 is runGuard's own
		// pre-loop startup observation (len(tunnels)>0 && len(endpoints)>0),
		// which never touches zombieChecks; indices 1-2 are the two real
		// geoTick failures that cross the Hysteresis(2) threshold and open
		// the first window. No confirmed exit ever closes a window EARLY
		// (both streaks' windows suppress lookups entirely while open), so
		// each granted window costs its full duration.
		Monitor:        &scriptedZombieMonitor{successAt: 3},
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
		// first streak's window cost is recorded — same shape as
		// TestARefusedRedialRetriesWhenTheBudgetRefills, but two zombie
		// streaks stand in for the two ordinary drops that fixture uses.
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
	// second streak's attempt has to have been refused because the first
	// streak's window already spent the budget.
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

	// Exactly two automatic windows total — one grant per streak, never a
	// third from either streak reopening on its own once its window expires.
	var switches int
	for _, c := range be.calls {
		if c == "apply-switch" {
			switches++
		}
	}
	if switches != 2 {
		t.Errorf("apply-switch called %d time(s), want exactly 2 (one grant per streak); calls = %v", switches, be.calls)
	}
}

// Disabling vpn.advanced.livenessRedial live, after a zombie streak's
// automatic attempt has already been refused by the budget, must cancel that
// standing refusal's retry — not just future streaks. retryAutoWindow's
// tunnelUp/dg.zombie guard alone would still let the retry fire once the
// budget refills, silently bypassing the operator's just-disabled opt-in.
// Same fixture as TestAZombieRefusedRedialRetriesWithoutTheTunnelGoingDown,
// but the refusal triggers a reload turning livenessRedial off instead of
// letting it stand.
func TestDisablingLivenessRedialDropsAStandingZombieRefusal(t *testing.T) {
	be := &fakeBackend{}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	reloadC := make(chan LiveSettings, 1)
	var (
		mu       sync.Mutex
		snaps    []state.Snapshot
		disabled bool
	)
	o := Options{
		Monitor:            &scriptedZombieMonitor{successAt: 3},
		Decider:            decision.New([]string{"IR"}, 2),
		Backend:            be,
		Log:                discardLog(),
		Interval:           time.Millisecond,
		Tunnels:            []string{"utun4"},
		Endpoints:          []netip.Addr{netip.MustParseAddr("203.0.113.7")},
		Watcher:            edgeWatcher(100000), // interface reports up for the WHOLE run — no down edge, ever
		LivenessRedial:     true,
		RedialWindow:       20 * time.Millisecond,
		RedialBudget:       25 * time.Millisecond,
		RedialBudgetWindow: 120 * time.Millisecond,
		ReloadC:            reloadC,
	}
	o.Publish = func(s state.Snapshot) {
		mu.Lock()
		defer mu.Unlock()
		snaps = append(snaps, s)
		// The moment the second streak's attempt is refused, turn
		// livenessRedial off — event-driven so the test cannot race the
		// refusal or the budget refilling.
		if s.Redial != nil && !disabled {
			disabled = true
			ls := o.Live()
			ls.LivenessRedial = false
			select {
			case reloadC <- ls:
			default:
			}
		}
	}
	if err := Run(ctx, o); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()

	if !disabled {
		t.Fatal("no redial refusal was ever published; the budget never ran out and this fixture tests nothing")
	}

	refusedAt := -1
	for i, s := range snaps {
		if s.Redial != nil {
			refusedAt = i
			break
		}
	}
	for _, s := range snaps[refusedAt+1:] {
		if s.Switch != nil && s.Switch.Open && s.Switch.Trigger == state.TriggerAuto {
			t.Fatalf("an automatic window opened after livenessRedial was disabled following the refusal: %+v", *s.Switch)
		}
	}

	// Exactly ONE automatic window total — the first streak's grant — never
	// a second from the disabled retry firing once the budget refilled.
	var switches int
	for _, c := range be.calls {
		if c == "apply-switch" {
			switches++
		}
	}
	if switches != 1 {
		t.Errorf("apply-switch called %d time(s), want exactly 1 (the first streak's grant only); calls = %v", switches, be.calls)
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

// If `dezhban hold` is armed before a zombie streak's one-shot
// liveness-redial attempt fires, maybeAutoWindow's holdArmed branch
// (consumeHold:false for this caller) suppresses it without ever reaching
// grantAutoWindow — so unlike an ordinary drop's refusal, there is no
// standing redialRefused for retryAutoWindow to act on once hold is
// cancelled. Without zombieHoldSuppressed restoring the attempt directly,
// the streak's one shot (already spent via zombieRedialTried) was forfeited
// for good: nothing else would ever re-arm it while the same streak stands.
// This pins that cancelling hold gives the streak's attempt back — a window
// must open afterward, all without the tunnel ever reporting down.
func TestHoldCancelRestoresASuppressedZombieRedialAttempt(t *testing.T) {
	be := &fakeBackend{}
	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()

	var (
		mu       sync.Mutex
		snaps    []state.Snapshot
		armed    bool
		canceled bool
		start    = time.Now()
	)
	o := Options{
		Monitor:            steadyFailMonitor{}, // never resolves — one continuous streak
		Decider:            decision.New([]string{"IR"}, 2),
		Backend:            be,
		Log:                discardLog(),
		Interval:           15 * time.Millisecond,
		Tunnels:            []string{"utun4"},
		Endpoints:          []netip.Addr{netip.MustParseAddr("203.0.113.7")},
		Watcher:            edgeWatcher(100000), // interface reports up for the whole run
		LivenessRedial:     true,
		RedialWindow:       30 * time.Millisecond,
		RedialBudget:       testRedialBudget,
		RedialBudgetWindow: testRedialBudgetWindow,
		CommandPoll:        time.Millisecond,
		Publish: func(s state.Snapshot) {
			mu.Lock()
			defer mu.Unlock()
			snaps = append(snaps, s)
		},
	}
	o.PollCommand = func() (command.Command, bool) {
		mu.Lock()
		defer mu.Unlock()
		// Armed on the very first poll (~1ms in), well before the streak can
		// cross the Hysteresis(2) threshold (~30ms in, at Interval=15ms), so
		// the streak's one attempt is suppressed by hold rather than actually
		// run.
		if !armed {
			armed = true
			return command.Command{Op: command.OpHoldArm, IssuedAt: time.Now(), Nonce: "arm"}, true
		}
		if armed && !canceled && time.Since(start) > 150*time.Millisecond {
			canceled = true
			return command.Command{Op: command.OpHoldCancel, IssuedAt: time.Now(), Nonce: "cancel"}, true
		}
		return command.Command{}, false
	}
	if err := Run(ctx, o); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if !armed || !canceled {
		t.Fatalf("fixture never reached the state under test (armed=%v canceled=%v)", armed, canceled)
	}

	cancelIdx := -1
	for i := 1; i < len(snaps); i++ {
		prev, cur := snaps[i-1], snaps[i]
		if prev.Hold != nil && prev.Hold.Armed && (cur.Hold == nil || !cur.Hold.Armed) {
			cancelIdx = i
		}
	}
	if cancelIdx < 0 {
		t.Fatal("never observed hold going from armed to cancelled; fixture proved nothing")
	}

	for _, s := range snaps[cancelIdx:] {
		if s.Switch == nil || !s.Switch.Open || s.Switch.Trigger != state.TriggerAuto {
			continue
		}
		// Confirm no tunnel-down edge ever happened — the window must be
		// attributable to the restored zombie attempt, not an ordinary drop.
		for _, d := range snaps {
			if d.Drop != nil {
				t.Fatalf("a tunnel drop was recorded; this fixture's tunnel must never go down: %+v", *d.Drop)
			}
		}
		return
	}
	t.Error("after hold the line was cancelled, the suppressed zombie liveness-redial attempt never got " +
		"a window: it was silently forfeited instead of being restored")
}
