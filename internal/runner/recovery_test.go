package runner

import (
	"context"
	"net/netip"
	"sync"
	"testing"
	"time"

	"github.com/behnam-rk/dezhban/internal/decision"
	"github.com/behnam-rk/dezhban/internal/monitor"
	"github.com/behnam-rk/dezhban/internal/netdetect"
	"github.com/behnam-rk/dezhban/internal/state"
)

// A user watching a blocked network needs to tell "recovering, one reading to
// go" from "stuck". The count is the whole answer, so it has to reach the
// snapshot both surfaces read.
func TestSnapshotCarriesTheHysteresisStreak(t *testing.T) {
	d := decision.New([]string{"IR"}, 3)
	// Two blocked readings out of the three needed: a flip is under way.
	for range 2 {
		d.Evaluate(monitor.Result{Reading: monitor.Reading{CountryCode: "IR"}})
	}

	var got state.Snapshot
	o := Options{
		Decider:  d,
		Interval: time.Minute,
		Publish:  func(s state.Snapshot) { got = s },
	}
	o.publish(false, false, monitor.Reading{CountryCode: "IR"}, nil, nil, nil, nil, nil, "")

	if got.Pending == nil {
		t.Fatal("no pending flip published while a hysteresis streak was running")
	}
	if got.Pending.To != "full-block" || got.Pending.Have != 2 || got.Pending.Need != 3 {
		t.Errorf("pending = %+v, want full-block 2 of 3", *got.Pending)
	}
}

// Publishing must be a pure read. If observing progress advanced or reset the
// streak, the snapshot would be changing the very thing it reports.
func TestPublishingProgressDoesNotDisturbTheStreak(t *testing.T) {
	d := decision.New([]string{"IR"}, 3)
	d.Evaluate(monitor.Result{Reading: monitor.Reading{CountryCode: "IR"}})

	o := Options{Decider: d, Interval: time.Minute, Publish: func(state.Snapshot) {}}
	for range 5 {
		o.publish(false, false, monitor.Reading{}, nil, nil, nil, nil, nil, "")
	}
	_, have, _ := d.Pending()
	if have != 1 {
		t.Fatalf("streak = %d after five publishes, want it untouched at 1", have)
	}
}

// In standby, and while a window is open, the geo state machine is not driving
// the posture. Advertising a streak there would promise a change that cannot
// arrive.
func TestNoPendingFlipReportedWhenNothingCanAct(t *testing.T) {
	d := decision.New([]string{"IR"}, 3)
	d.Evaluate(monitor.Result{Reading: monitor.Reading{CountryCode: "IR"}})
	o := Options{Decider: d}

	if p := o.pendingFlip(true, false); p != nil {
		t.Errorf("pending %+v reported in standby", *p)
	}
	if p := o.pendingFlip(false, true); p != nil {
		t.Errorf("pending %+v reported while a window was open", *p)
	}
	if p := o.pendingFlip(false, false); p == nil {
		t.Error("no pending flip reported while the guard was actually counting")
	}
}

// A settled decider has nothing to report — an always-present field would make
// "no change under way" indistinguishable from "0 of 3 so far".
func TestNoPendingFlipWhenSettled(t *testing.T) {
	o := Options{Decider: decision.New([]string{"IR"}, 3)}
	if p := o.pendingFlip(false, false); p != nil {
		t.Errorf("pending %+v reported with no streak running", *p)
	}
}

// countingMonitor reports a fixed country and counts how many lookups it served,
// which is how the tests below observe the probe cadence.
type countingMonitor struct {
	cc string
	n  int
}

func (m *countingMonitor) Poll(ctx context.Context) <-chan monitor.Result {
	ch := make(chan monitor.Result)
	close(ch)
	return ch
}

func (m *countingMonitor) Once(ctx context.Context) (monitor.Reading, error) {
	m.n++
	return monitor.Reading{CountryCode: m.cc, IP: netip.MustParseAddr("203.0.113.9")}, nil
}

// recoveryOpts is a FULL-BLOCK-capable loop with a slow configured cadence, so
// any probing faster than that is attributable to the acceleration alone.
func recoveryOpts(be Backend, mon Monitor, w *netdetect.Watcher) Options {
	return Options{
		Monitor:   mon,
		Decider:   decision.New([]string{"IR"}, 1),
		Backend:   be,
		Log:       discardLog(),
		Interval:  time.Hour, // far too slow to explain any observed probing
		Tunnels:   []string{"utun4"},
		Endpoints: []netip.Addr{netip.MustParseAddr("203.0.113.7")},
		// Provider addresses are what make FULL BLOCK probe-able without lifting
		// the guard — the precondition for accelerating at all.
		ResolveProviders: func(context.Context) []netip.Addr {
			return []netip.Addr{netip.MustParseAddr("198.51.100.5")}
		},
		Watcher: w,
	}
}

// The payoff: after a redial the guard comes back in seconds instead of after
// poll-interval × hysteresis. With Interval at an hour, only the accelerated
// cadence can explain a restore inside the test's lifetime.
func TestTunnelUpWhileBlockedProbesUntilTheGuardIsRestored(t *testing.T) {
	be := &fakeBackend{}
	mon := &countingMonitor{cc: "IR"} // start on a blocked exit
	tun := &scriptedWatcher{}
	o := recoveryOpts(be, mon, tun.watcher())

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

	tun.send(netdetect.TunnelState{Up: true, Names: []string{"utun4"}, Detail: "connected"})
	if !waitFor(t, snaps, func(s state.Snapshot) bool { return s.Posture == "full-block" }) {
		t.Fatal("never reached FULL BLOCK on a blocked exit")
	}

	// The VPN redials onto an allowed exit: drop, then up.
	mon.cc = "US"
	tun.send(netdetect.TunnelState{Up: false, Detail: "dropped"})
	tun.send(netdetect.TunnelState{Up: true, Names: []string{"utun4"}, Detail: "redialed"})

	if !waitFor(t, snaps, func(s state.Snapshot) bool { return s.Posture == "guard" }) {
		t.Fatal("the guard was never restored; a tunnel-up edge in FULL BLOCK must probe for recovery " +
			"rather than wait for the configured poll interval")
	}
	cancel()
	<-done
}

// The rail that matters. Without provider addresses each probe LIFTS the guard,
// so accelerating would multiply real-IP exposure by the speed-up factor. The
// daemon must decline to accelerate rather than trade a leak for latency.
func TestNoAccelerationWhenProbingWouldHaveToLiftTheGuard(t *testing.T) {
	be := &fakeBackend{}
	mon := &countingMonitor{cc: "IR"}
	tun := &scriptedWatcher{}
	o := recoveryOpts(be, mon, tun.watcher())
	o.ResolveProviders = nil // no tunnel-scoped pass → probing costs a guard lift

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

	tun.send(netdetect.TunnelState{Up: true, Names: []string{"utun4"}, Detail: "connected"})
	if !waitFor(t, snaps, func(s state.Snapshot) bool { return s.Posture == "full-block" }) {
		t.Fatal("never reached FULL BLOCK")
	}

	before := mon.n
	mon.cc = "US"
	tun.send(netdetect.TunnelState{Up: false, Detail: "dropped"})
	tun.send(netdetect.TunnelState{Up: true, Names: []string{"utun4"}, Detail: "redialed"})
	time.Sleep(300 * time.Millisecond) // many fastProbeIntervals, were it accelerating

	if extra := mon.n - before; extra > 0 {
		t.Fatalf("%d lookup(s) ran after the tunnel-up edge with no tunnel-scoped provider pass; "+
			"each would have lifted the guard, so this must fall back to the configured cadence", extra)
	}
	cancel()
	<-done
}

// scriptedWatcher drives tunnel edges from a test by holding a state the
// sampler keeps returning.
//
// `send` deliberately BLOCKS for several sample intervals. netdetect.Watcher
// debounces a down edge over DownDebounce consecutive samples, so a state
// visible for only one tick is swallowed — which is correct for a real
// interface (it stops a flapping redial churning rule reloads) and would make a
// fake that flipped states instantaneously silently emit no edges at all.
type scriptedWatcher struct {
	mu   sync.Mutex
	last netdetect.TunnelState
}

const scriptedInterval = 5 * time.Millisecond

func (s *scriptedWatcher) watcher() *netdetect.Watcher {
	return &netdetect.Watcher{
		Tunnels:  []string{"utun4"},
		Interval: scriptedInterval,
		Sample: func([]string) netdetect.TunnelState {
			s.mu.Lock()
			defer s.mu.Unlock()
			return s.last
		},
	}
}

func (s *scriptedWatcher) send(st netdetect.TunnelState) {
	s.mu.Lock()
	s.last = st
	s.mu.Unlock()
	// Long enough to clear the down debounce and be observed as a real edge.
	time.Sleep(10 * scriptedInterval)
}

// waitFor drains snapshots until one satisfies pred, or the deadline passes.
func waitFor(t *testing.T, snaps <-chan state.Snapshot, pred func(state.Snapshot) bool) bool {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case s := <-snaps:
			if pred(s) {
				return true
			}
		case <-deadline:
			return false
		}
	}
}
