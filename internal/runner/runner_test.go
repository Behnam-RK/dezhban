package runner

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/behnam-rk/dezhban/internal/command"
	"github.com/behnam-rk/dezhban/internal/decision"
	"github.com/behnam-rk/dezhban/internal/firewall"
	"github.com/behnam-rk/dezhban/internal/monitor"
	"github.com/behnam-rk/dezhban/internal/netdetect"
	"github.com/behnam-rk/dezhban/internal/state"
)

// fakeMonitor is a deterministic Monitor for tests. Poll (legacy loop) drains
// results then closes the channel, ending the loop. Once (VPN loop / recovery
// probe) returns results in order and cancels the run context after the last
// one, so the manual-ticker VPN loop exits without a real clock.
type fakeMonitor struct {
	results []monitor.Result
	idx     int
	cancel  context.CancelFunc
}

func (f *fakeMonitor) Poll(ctx context.Context) <-chan monitor.Result {
	ch := make(chan monitor.Result)
	go func() {
		defer close(ch)
		for _, r := range f.results {
			select {
			case ch <- r:
			case <-ctx.Done():
				return
			}
		}
	}()
	return ch
}

func (f *fakeMonitor) Once(context.Context) (monitor.Reading, error) {
	if f.idx >= len(f.results) {
		if f.cancel != nil {
			f.cancel()
		}
		return monitor.Reading{}, context.Canceled
	}
	r := f.results[f.idx]
	f.idx++
	if f.idx >= len(f.results) && f.cancel != nil {
		// Last result: let the loop process it, then exit on ctx.Done next select.
		f.cancel()
	}
	return r.Reading, r.Err
}

// fakeBackend records the sequence of calls made against it. applyErr, when set,
// makes Apply fail (the call is still recorded) so tests can exercise
// enforcement-failure paths.
type fakeBackend struct {
	calls    []string
	policies []firewall.Policy
	applyErr error
	// isBlockedFn drives enforcement verification. nil answers "the rules are
	// present" — the healthy reply — so every test that does not care about
	// verification is unaffected by its existence.
	isBlockedFn func() (bool, error)
}

func (b *fakeBackend) Apply(p firewall.Policy) error {
	b.policies = append(b.policies, p)
	switch p.Mode {
	case firewall.ModeGuard:
		b.calls = append(b.calls, "apply-guard")
	case firewall.ModeSwitchWindow:
		b.calls = append(b.calls, "apply-switch")
	default:
		b.calls = append(b.calls, "apply-fullblock")
	}
	return b.applyErr
}
func (b *fakeBackend) Unblock() error {
	b.calls = append(b.calls, "unblock")
	return nil
}
func (b *fakeBackend) Cleanup() error {
	b.calls = append(b.calls, "cleanup")
	return nil
}
func (b *fakeBackend) IsBlocked() (bool, error) {
	b.calls = append(b.calls, "is-blocked")
	if b.isBlockedFn == nil {
		return true, nil
	}
	return b.isBlockedFn()
}

func reading(cc string) monitor.Result {
	return monitor.Result{Reading: monitor.Reading{CountryCode: cc}}
}

func failResult() monitor.Result {
	return monitor.Result{Err: errors.New("all providers failed")}
}

func discardLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// A redial budget generous enough, at these millisecond test durations, that the
// ledger never refuses. Every test asserting some OTHER precondition of the
// automatic window sets these, so it cannot pass for the wrong reason: an
// Options with a redial window but no budget opens NOTHING (a zero budget can
// afford no window), and a test expecting no window would then agree with itself
// while proving nothing. Tests about the budget itself set their own numbers.
const (
	testRedialBudget       = 10 * time.Second
	testRedialBudgetWindow = time.Minute
)

// oneHostAL is a non-empty allowlist so the legacy mid-block refresh re-Blocks
// (an empty refresh is deliberately skipped — see TestLegacyRefreshSkipWhenEmpty).
func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// --- state publishing ---

func TestPostureName(t *testing.T) {
	cases := []struct {
		blocked, window, standby bool
		want                     string
	}{
		{false, false, false, "guard"},
		{true, false, false, "full-block"},
		{false, true, false, "switch-window"},
		{true, true, false, "switch-window"}, // a window outranks a full block
		{false, false, true, "standby"},      // no tunnel observed yet
		{true, false, true, "full-block"},    // a full block outranks standby
		{false, true, true, "switch-window"}, // so does a window
	}
	for _, c := range cases {
		if got := postureName(c.blocked, c.window, c.standby); got != c.want {
			t.Errorf("postureName(blocked=%v, window=%v, standby=%v) = %q, want %q",
				c.blocked, c.window, c.standby, got, c.want)
		}
	}
}

// TestShouldArmAtBoot pins the four-way permutation the arm-at-boot decision
// depends on. Only (armAtBoot=true, tunnelEverUp=true, endpoint known) may
// override an AutoArm-computed standby — every other combination must leave
// standby alone, preserving ADR-0002's "a fresh install can never lock itself
// out" guarantee.
func TestShouldArmAtBoot(t *testing.T) {
	cases := []struct {
		armAtBoot, tunnelEverUp bool
		endpointCount           int
		want                    bool
	}{
		{false, false, 0, false},
		{false, true, 1, false}, // armAtBoot off: today's behavior, unchanged
		{true, false, 1, false}, // never observed up: the ADR-0002 rail holds
		{true, true, 0, false},  // no endpoint known: arming would be a lockout
		{true, true, 1, true},   // both conditions hold: arm
		{true, true, 3, true},   // endpoint count otherwise irrelevant once >0
	}
	for _, c := range cases {
		got := shouldArmAtBoot(c.armAtBoot, c.tunnelEverUp, c.endpointCount)
		if got != c.want {
			t.Errorf("shouldArmAtBoot(armAtBoot=%v, tunnelEverUp=%v, endpoints=%d) = %v, want %v",
				c.armAtBoot, c.tunnelEverUp, c.endpointCount, got, c.want)
		}
	}
}

// TestLegacyPublishesPostureTransitions asserts a snapshot fires on every poll
// with the correct posture as the daemon crosses allow→block→allow, then a
// terminal "stopped" snapshot on shutdown so observers flip immediately.
func TestVPNGuardFullBlockAndProbeRecovery(t *testing.T) {
	be := &fakeBackend{}
	ctx, cancel := context.WithCancel(context.Background())
	o := Options{
		Monitor: &fakeMonitor{cancel: cancel, results: []monitor.Result{
			reading("US"), // allow, already guard → no-op
			reading("IR"), // full block (enter)
			reading("US"), // probe sees allowed country → recover to guard
		}},
		Decider:   decision.New([]string{"IR"}, 1),
		Backend:   be,
		Log:       discardLog(),
		Interval:  time.Millisecond,
		Tunnels:   []string{"utun4"},
		Endpoints: []netip.Addr{netip.MustParseAddr("203.0.113.7")},
	}
	if err := Run(ctx, o); err != nil {
		t.Fatal(err)
	}
	// startup guard; enter full block; recovery tick = lift(guard)+recut(fullblock)
	// from the probe, then restore guard on the Allow verdict; cleanup.
	want := []string{
		"apply-guard",     // startup guard
		"apply-fullblock", // IR → FULL BLOCK
		"apply-guard",     // probe lift
		"apply-fullblock", // probe re-cut (before deciding)
		"apply-guard",     // US verdict → restore guard
		"cleanup",
	}
	if !equal(be.calls, want) {
		t.Fatalf("calls = %v, want %v", be.calls, want)
	}

	// Full-block policy under VPN must carry the tunnel ifaces and no dst-IP list.
	var fb firewall.Policy
	found := false
	for _, p := range be.policies {
		if p.Mode == firewall.ModeFullBlock {
			fb, found = p, true
		}
	}
	if !found {
		t.Fatal("no full-block policy applied")
	}
	if len(fb.TunnelIfaces) == 0 {
		t.Error("VPN full block must carry tunnel ifaces")
	}
}

// A single allowed probe must not lift a hysteresis>1 block: recovery requires
// `Hysteresis` consecutive allowed probes.
func TestVPNProbeRespectsHysteresis(t *testing.T) {
	be := &fakeBackend{}
	ctx, cancel := context.WithCancel(context.Background())
	o := Options{
		Monitor: &fakeMonitor{cancel: cancel, results: []monitor.Result{
			reading("IR"), // streak 1 toward block (still guard)
			reading("IR"), // streak 2 → FULL BLOCK
			reading("US"), // probe: streak 1 toward allow → still blocked
			reading("US"), // probe: streak 2 → recover to guard
		}},
		Decider:   decision.New([]string{"IR"}, 2),
		Backend:   be,
		Log:       discardLog(),
		Interval:  time.Millisecond,
		Tunnels:   []string{"utun4"},
		Endpoints: []netip.Addr{netip.MustParseAddr("203.0.113.7")},
	}
	if err := Run(ctx, o); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"apply-guard",     // startup guard
		"apply-fullblock", // 2nd IR → FULL BLOCK
		"apply-guard",     // probe 1 lift
		"apply-fullblock", // probe 1 re-cut (US #1 → still blocked)
		"apply-guard",     // probe 2 lift
		"apply-fullblock", // probe 2 re-cut
		"apply-guard",     // US #2 → recover to guard
		"cleanup",
	}
	if !equal(be.calls, want) {
		t.Fatalf("calls = %v, want %v", be.calls, want)
	}
}

// In VPN guard mode an undeterminable country (lookup error) must HOLD the
// current posture, never escalate GUARD→FULL BLOCK. The standing guard is
// already the fail-closed block for physical leaks; escalating on an unknown
// would cut the tunnel's own egress and livelock the redial. hysteresis=1
// so that, without the hold, a single error would immediately FULL BLOCK.
func TestVPNHoldsGuardOnLookupError(t *testing.T) {
	be := &fakeBackend{}
	ctx, cancel := context.WithCancel(context.Background())
	o := Options{
		Monitor: &fakeMonitor{cancel: cancel, results: []monitor.Result{
			failResult(), // undeterminable → hold guard (must NOT full block)
			failResult(), // still undeterminable → still guard
		}},
		Decider:   decision.New([]string{"IR"}, 1), // failClosed, hysteresis 1
		Backend:   be,
		Log:       discardLog(),
		Interval:  time.Millisecond,
		Tunnels:   []string{"utun4"},
		Endpoints: []netip.Addr{netip.MustParseAddr("203.0.113.7")},
	}
	if err := Run(ctx, o); err != nil {
		t.Fatal(err)
	}
	want := []string{"apply-guard", "cleanup"} // startup guard held throughout
	if !equal(be.calls, want) {
		t.Fatalf("calls = %v, want %v (a lookup error must not FULL BLOCK in guard mode)", be.calls, want)
	}
}

// While already in FULL BLOCK, a lookup error during the recovery probe must
// NOT lift the block: recovery requires a *successful* Allow reading. The probe
// still lifts+re-cuts each tick (recovery keeps trying), but an error holds the
// block rather than recovering.
func TestVPNHoldsFullBlockOnProbeError(t *testing.T) {
	be := &fakeBackend{}
	ctx, cancel := context.WithCancel(context.Background())
	o := Options{
		Monitor: &fakeMonitor{cancel: cancel, results: []monitor.Result{
			reading("IR"), // enter FULL BLOCK
			failResult(),  // probe error → hold block
			failResult(),  // probe error → hold block
		}},
		Decider:   decision.New([]string{"IR"}, 1),
		Backend:   be,
		Log:       discardLog(),
		Interval:  time.Millisecond,
		Tunnels:   []string{"utun4"},
		Endpoints: []netip.Addr{netip.MustParseAddr("203.0.113.7")},
	}
	if err := Run(ctx, o); err != nil {
		t.Fatal(err)
	}
	// startup guard; IR → full block; each blocked tick probes (lift+re-cut)
	// but an error never restores guard.
	//
	// The SHAPE, not an exact tick count. Asserting the literal seven-call
	// sequence made this test depend on a race it does not test: fakeMonitor.Once
	// cancels while handing out the last result, and the loop then has both
	// ctx.Done() and a 1ms geo ticker ready at its next select — which picks
	// uniformly among ready cases, so one more probe cycle is a coin flip whenever
	// processing the previous one took longer than the interval. That is
	// vanishingly rare on a fast machine and ordinary under -race on a loaded CI
	// runner, where it failed with one extra "apply-guard apply-fullblock" pair.
	//
	// What the test is actually for survives intact and is now stated directly: a
	// probe lift is ALWAYS followed by a re-cut, so a probe error never leaves the
	// guard standing in place of a full block. An extra probe cycle satisfies that
	// as fully as the expected number does; a lifted block does not, at any count.
	assertProbeNeverLiftsTheBlock(t, be.calls)
}

// assertProbeNeverLiftsTheBlock pins the call sequence of a daemon that entered
// FULL BLOCK and stayed there: the startup guard, the escalation, then some whole
// number of lift-and-probe pairs, then cleanup. Nothing may end on a lift.
//
// Timing-independent by construction — see the caller for why an exact length
// cannot be. The lower bound is what keeps it from passing vacuously if probing
// stops happening at all.
func assertProbeNeverLiftsTheBlock(t *testing.T, calls []string) {
	t.Helper()
	if n := len(calls); n < 7 {
		t.Fatalf("calls = %v: want the startup guard, the escalation, at least two "+
			"probe pairs and cleanup", calls)
	}
	if last := calls[len(calls)-1]; last != "cleanup" {
		t.Fatalf("calls = %v: last call is %q, want cleanup", calls, last)
	}
	body := calls[:len(calls)-1]
	if len(body)%2 != 0 {
		t.Fatalf("calls = %v: %d calls before cleanup is odd, so a lift went "+
			"un-recut — a probe error must not lift FULL BLOCK", calls, len(body))
	}
	for i, call := range body {
		want := "apply-fullblock"
		if i%2 == 0 {
			want = "apply-guard"
		}
		if call != want {
			t.Fatalf("calls = %v: call %d is %q, want %q (a probe error must not "+
				"lift FULL BLOCK)", calls, i, call, want)
		}
	}
}

func TestVPNStartupGuardFailureAborts(t *testing.T) {
	be := &failingGuardBackend{}
	o := Options{
		Monitor:   &fakeMonitor{},
		Decider:   decision.New([]string{"IR"}, 1),
		Backend:   be,
		Log:       discardLog(),
		Interval:  time.Millisecond,
		Tunnels:   []string{"utun4"},
		Endpoints: []netip.Addr{netip.MustParseAddr("203.0.113.7")},
	}
	err := Run(context.Background(), o)
	if err == nil {
		t.Fatal("expected startup guard failure to return an error")
	}
	// Cleanup must still run on the way out (deferred), never leaving stale rules.
	if be.cleanups != 1 {
		t.Fatalf("cleanup ran %d times, want 1", be.cleanups)
	}
}

type failingGuardBackend struct {
	cleanups int
}

func (b *failingGuardBackend) Apply(p firewall.Policy) error { return errors.New("guard apply failed") }
func (b *failingGuardBackend) Unblock() error                { return nil }
func (b *failingGuardBackend) IsBlocked() (bool, error)      { return true, nil }
func (b *failingGuardBackend) Cleanup() error                { b.cleanups++; return nil }

// --- tunnel watcher ---

// signalBackend is concurrency-safe (the watcher runs in its own goroutine) and
// records every call, so a test can assert on the sequence after the fact.
type signalBackend struct {
	mu    sync.Mutex
	calls []string
}

func (b *signalBackend) record(s string) {
	b.mu.Lock()
	b.calls = append(b.calls, s)
	b.mu.Unlock()
}
func (b *signalBackend) Apply(p firewall.Policy) error {
	if p.Mode == firewall.ModeGuard {
		b.record("apply-guard")
	} else {
		b.record("apply-fullblock")
	}
	return nil
}
func (b *signalBackend) Unblock() error           { b.record("unblock"); return nil }
func (b *signalBackend) Cleanup() error           { b.record("cleanup"); return nil }
func (b *signalBackend) IsBlocked() (bool, error) { return true, nil }
func (b *signalBackend) has(call string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, c := range b.calls {
		if c == call {
			return true
		}
	}
	return false
}

// idleMonitor never yields a reading; Poll stays open until ctx is cancelled, so
// the legacy loop survives long enough for the watcher to drive it.
type idleMonitor struct{}

func (idleMonitor) Poll(ctx context.Context) <-chan monitor.Result {
	ch := make(chan monitor.Result)
	go func() {
		<-ctx.Done()
		close(ch)
	}()
	return ch
}
func (idleMonitor) Once(ctx context.Context) (monitor.Reading, error) {
	<-ctx.Done()
	return monitor.Reading{}, ctx.Err()
}

// steadyMonitor always returns the same country with no error, so a test can run
// the VPN loop for a fixed wall-clock window (cancelling via a timeout context)
// without depending on the monitor exhausting a fixed slice of readings.
type steadyMonitor struct{ cc string }

func (steadyMonitor) Poll(ctx context.Context) <-chan monitor.Result {
	ch := make(chan monitor.Result)
	go func() { <-ctx.Done(); close(ch) }()
	return ch
}
func (m steadyMonitor) Once(context.Context) (monitor.Reading, error) {
	return monitor.Reading{CountryCode: m.cc}, nil
}

func downWatcher() *netdetect.Watcher {
	return &netdetect.Watcher{
		Interval: time.Millisecond,
		Sample:   func([]string) netdetect.TunnelState { return netdetect.TunnelState{Up: false} },
	}
}

// presentTunnel, noTunnel and brokenProbe are the three answers
// Options.ProbeTunnelIfaces can give, as AutoArm reads them: a tunnel interface
// exists on this host, so start ARMED — none does, so start in STANDBY — or the
// probe itself failed, which arms immediately (fail-closed). A test that sets
// AutoArm and none of these is not testing a posture, it is testing whether
// whoever runs it has a VPN up.
// They are the probes themselves, not factories for one: each is stateless, so
// a call layer that only ever returns the same closure would buy nothing and
// hide the signature that has to match Options.ProbeTunnelIfaces.
func presentTunnel() ([]string, error) { return []string{"utun4"}, nil }

func noTunnel() ([]string, error) { return nil, nil }

func brokenProbe() ([]string, error) { return nil, errors.New("interface enumeration failed") }

// The AutoArm startup decision itself, every way it can go. It had no test at
// all: the probe read the live host, so the only way to reach any branch was to
// run the suite on a machine that happened to be in that state — which is how
// the standby branch stayed unexercised while the armed one silently carried
// TestVerifyFindingClearedOnStandbyEntry.
//
// The four branches, and why each matters:
//   - no tunnel present must install NOTHING (ADR-0002's rail: a fresh install
//     cannot lock itself out);
//   - one present must raise the standing guard;
//   - a FAILED probe must arm anyway — an unreadable host is not evidence that
//     no tunnel exists, and guessing "standby" there would silently open the
//     network on exactly the host whose state we cannot see;
//   - ArmAtBoot overrides an empty probe, but only on a host that has connected
//     before and knows an endpoint (shouldArmAtBoot); this is the branch every
//     normal boot takes, since the VPN client starts after this daemon.
func TestAutoArmStartPostureFollowsTheProbe(t *testing.T) {
	for _, tc := range []struct {
		name      string
		probe     func() ([]string, error)
		armAtBoot bool
		// country is what the exit-country lookup answers at startup. It is a
		// case field rather than vpnOpts' steady "US" because "nothing
		// installed" has to hold for a BLOCKED answer too — and that is the
		// only answer that can reach an Apply from standby, since an allowed
		// one has nothing to escalate to. A standby row pinned to "US" agrees
		// with itself while the rail it names is broken.
		country string
		posture string
		guarded bool
	}{
		{name: "no tunnel interface → standby, nothing installed", probe: noTunnel, country: "US", posture: "standby"},
		{name: "no tunnel interface, blocked country → standby, still nothing installed", probe: noTunnel, country: "IR", posture: "standby"},
		{name: "tunnel interface present → armed", probe: presentTunnel, country: "US", posture: "guard", guarded: true},
		{name: "probe failed → armed, fail-closed", probe: brokenProbe, country: "US", posture: "guard", guarded: true},
		{name: "no tunnel but armAtBoot on a host that has connected → armed", probe: noTunnel, country: "US", armAtBoot: true, posture: "guard", guarded: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			be := &fakeBackend{}
			o := vpnOpts(be)
			o.Monitor = steadyMonitor{cc: tc.country}
			o.AutoArm = true
			o.ProbeTunnelIfaces = tc.probe
			// ArmAtBoot needs BOTH rails to override standby: a tunnel observed
			// up before on this host, and a known endpoint (vpnOpts pins one).
			o.ArmAtBoot = tc.armAtBoot
			o.TunnelEverUp = tc.armAtBoot
			o.Watcher = downWatcher()
			o.Log = discardLog()
			// The FIRST snapshot is the startup posture under test; the last one
			// is always "stopped", published as the loop tears down. Run hands
			// snapshots to a BACKGROUND writer goroutine (see Options.Publish),
			// not to the run loop, so the capture is guarded — the read below is
			// only ordered after the writer by Run's bounded flush, and that
			// bound can expire.
			var mu sync.Mutex
			var first state.Snapshot
			var published bool
			// Bounded, but not paid in full: the startup posture is published
			// before the loop's first select, so stop the loop the moment it
			// lands instead of sitting out a fixed timeout in every case. The
			// timeout stays as the backstop for "no snapshot ever arrived",
			// which the !ok check below reports by name.
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			o.Publish = func(s state.Snapshot) {
				mu.Lock()
				defer mu.Unlock()
				if !published {
					first, published = s, true
					cancel()
				}
			}

			if err := Run(ctx, o); err != nil {
				t.Fatalf("Run: %v", err)
			}

			mu.Lock()
			got, ok := first, published
			mu.Unlock()
			if !ok {
				t.Fatal("the run loop published no snapshot at all")
			}
			if got.Posture != tc.posture {
				t.Errorf("start posture = %q, want %q", got.Posture, tc.posture)
			}
			if applied := containsCall(be.calls, "apply-guard"); applied != tc.guarded {
				t.Errorf("standing guard applied = %v, want %v; calls=%v", applied, tc.guarded, be.calls)
			}
			// "Nothing installed" is the whole ADR-0002 claim, so assert it as
			// such: not merely that the guard shape was skipped, but that NO
			// policy reached the backend. Checking only apply-guard would let a
			// standby that installed a full block pass.
			if !tc.guarded && len(be.policies) > 0 {
				t.Errorf("standby installed %d policy/policies (calls=%v), want none — a host with no tunnel gets no rules", len(be.policies), be.calls)
			}
		})
	}
}

// BlockedCountryNames ships BARE names, and this pins the wiring rather than
// the helper. country.Names and country.Labels differ by one word at the call
// site and the compiler is happy with either, so nothing but an assertion on a
// really-published snapshot stops the field going back to labels — at which
// point a consumer that composes a label the documented way, as the macOS app
// does, renders "Iran (IR) (IR)".
//
// Bare because CountryName beside it is bare: one shape for a single country
// and for a list, so a reader applies one rule and cannot be misled by the
// analogy between two fields that look parallel.
func TestPublishedBlockedCountryNamesAreBare(t *testing.T) {
	be := &fakeBackend{}
	o := vpnOpts(be)
	// A recognised code and an unrecognised one: the second must hold its index
	// with "" rather than being dropped, or every later name pairs with the
	// wrong country.
	o.BlockedCountries = []string{"IR", "ZZ"}
	o.Log = discardLog()

	var mu sync.Mutex
	var first state.Snapshot
	var published bool
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	o.Publish = func(s state.Snapshot) {
		mu.Lock()
		defer mu.Unlock()
		if !published {
			first, published = s, true
			cancel()
		}
	}
	if err := Run(ctx, o); err != nil {
		t.Fatalf("Run: %v", err)
	}

	mu.Lock()
	got, ok := first, published
	mu.Unlock()
	if !ok {
		t.Fatal("the run loop published no snapshot at all")
	}
	want := []string{"Iran", ""}
	if len(got.BlockedCountryNames) != len(want) {
		t.Fatalf("blockedCountryNames = %q, want %q (it must pair with blockedCountries index-for-index)",
			got.BlockedCountryNames, want)
	}
	for i := range want {
		if got.BlockedCountryNames[i] != want[i] {
			t.Errorf("blockedCountryNames[%d] = %q, want the bare %q", i, got.BlockedCountryNames[i], want[i])
		}
	}
}

// In legacy mode a tunnel drop must block immediately, with no geo reading at
// all, and a still-down tunnel must not auto-unblock.
func TestVPNWatcherObservabilityOnly(t *testing.T) {
	be := &fakeBackend{}
	// steadyMonitor always reports US (allowed), so the guard holds throughout and
	// the loop is bounded by the timeout, not by a fixed reading slice — the skip
	// added for a down tunnel would otherwise stop the geo ticks that drained it.
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	o := Options{
		Monitor:   steadyMonitor{cc: "US"},
		Decider:   decision.New([]string{"IR"}, 1),
		Backend:   be,
		Log:       discardLog(),
		Interval:  time.Millisecond,
		Tunnels:   []string{"utun4"},
		Endpoints: []netip.Addr{netip.MustParseAddr("203.0.113.7")},
		Watcher:   downWatcher(),
	}
	if err := Run(ctx, o); err != nil {
		t.Fatal(err)
	}
	for _, c := range be.calls {
		if c == "apply-fullblock" {
			t.Fatalf("watcher must not trigger a full block in VPN mode; calls = %v", be.calls)
		}
	}
	guards := 0
	for _, c := range be.calls {
		if c == "apply-guard" {
			guards++
		}
	}
	if guards != 1 {
		t.Errorf("apply-guard count = %d, want 1 (startup only); calls = %v", guards, be.calls)
	}
}

// While the tunnel is down and still guarding, the geo step must be skipped: a
// lookup can only leave through the down tunnel and fail, and a failed lookup
// fail-closes to FULL BLOCK — which renders no passes and closes the very
// endpoints the guard holds open for redial. So a failing monitor must NOT
// drive a full block while the tunnel is down; the standing guard just holds.
func TestVPNTunnelDownSkipsGeoStep(t *testing.T) {
	be := &fakeBackend{}
	// US at startup keeps the initial guard (blocked=false); any further Once call
	// — reachable only if the skip is broken — exhausts the slice and returns an
	// error, which under fail-closed hysteresis=1 would immediately full-block.
	mon := &fakeMonitor{results: []monitor.Result{reading("US")}}
	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	o := Options{
		Monitor:   mon,
		Decider:   decision.New([]string{"IR"}, 1), // fail-closed, no hysteresis
		Backend:   be,
		Log:       discardLog(),
		Interval:  100 * time.Millisecond, // geo ticks land long after the down edge (~1ms)
		Tunnels:   []string{"utun4"},
		Endpoints: []netip.Addr{netip.MustParseAddr("203.0.113.7")},
		Watcher:   downWatcher(), // samples down every 1ms → down edge within a few ms
	}
	if err := Run(ctx, o); err != nil {
		t.Fatal(err)
	}
	for _, c := range be.calls {
		if c == "apply-fullblock" {
			t.Fatalf("a down tunnel in GUARD must not full-block on failed lookups; calls = %v", be.calls)
		}
	}
	if mon.idx != 1 {
		t.Errorf("monitor Once calls = %d, want 1 (startup only; geo step skipped while tunnel down)", mon.idx)
	}
}

func addrsOf(ss ...string) []netip.Addr {
	out := make([]netip.Addr, len(ss))
	for i, s := range ss {
		out[i] = netip.MustParseAddr(s)
	}
	return out
}

func TestReconcileEndpoints(t *testing.T) {
	set := func(ss ...string) netdetect.EndpointSet { return netdetect.EndpointSet{Addrs: addrsOf(ss...)} }

	// Empty refresh never narrows the set.
	if got, ch := reconcileEndpoints(addrsOf("1.1.1.1"), set(), false); ch || !sameAddrs(got, addrsOf("1.1.1.1")) {
		t.Errorf("empty fresh: got %v changed=%v, want unchanged", got, ch)
	}
	// Guarding: a different set fully replaces.
	if got, ch := reconcileEndpoints(addrsOf("1.1.1.1"), set("2.2.2.2"), false); !ch || !sameAddrs(got, addrsOf("2.2.2.2")) {
		t.Errorf("guard replace: got %v changed=%v, want [2.2.2.2] changed", got, ch)
	}
	// Guarding: identical set is no change.
	if _, ch := reconcileEndpoints(addrsOf("1.1.1.1"), set("1.1.1.1"), false); ch {
		t.Error("guard identical: want no change")
	}
	// Guarding: a loss-only refresh (transient flake) must not drop an endpoint.
	if got, ch := reconcileEndpoints(addrsOf("1.1.1.1", "2.2.2.2"), set("1.1.1.1"), false); ch || !sameAddrs(got, addrsOf("1.1.1.1", "2.2.2.2")) {
		t.Errorf("guard loss-only: got %v changed=%v, want unchanged (flake must not drop a needed endpoint)", got, ch)
	}
	// Guarding: a rotation that surfaces a new address still replaces.
	if got, ch := reconcileEndpoints(addrsOf("1.1.1.1"), set("3.3.3.3"), false); !ch || !sameAddrs(got, addrsOf("3.3.3.3")) {
		t.Errorf("guard rotation: got %v changed=%v, want [3.3.3.3] changed", got, ch)
	}
	// Blocked: union-only growth.
	if got, ch := reconcileEndpoints(addrsOf("1.1.1.1"), set("2.2.2.2"), true); !ch || !sameAddrs(got, addrsOf("1.1.1.1", "2.2.2.2")) {
		t.Errorf("blocked grow: got %v changed=%v, want union", got, ch)
	}
	// Blocked: a shrinking refresh must not drop endpoints.
	if _, ch := reconcileEndpoints(addrsOf("1.1.1.1", "2.2.2.2"), set("1.1.1.1"), true); ch {
		t.Error("blocked shrink: want no change (must not drop an endpoint while blocked)")
	}
}

func TestReconcileWithGrace(t *testing.T) {
	set := func(ss ...string) netdetect.EndpointSet { return netdetect.EndpointSet{Addrs: addrsOf(ss...)} }
	now := time.Now()
	const grace = 15 * time.Minute
	a1 := netip.MustParseAddr("1.1.1.1")
	a3 := netip.MustParseAddr("3.3.3.3")

	// Rotation with the old endpoint still within grace: the new address enters
	// AND the recently-seen one rides along — a dropped VPN redialing its old
	// server must not find it walled off.
	seen := map[netip.Addr]time.Time{a1: now.Add(-5 * time.Minute)}
	if got, ch := reconcileWithGrace(addrsOf("1.1.1.1"), set("3.3.3.3"), false, seen, now, grace); !ch ||
		!sameAddrs(got, addrsOf("3.3.3.3", "1.1.1.1")) {
		t.Errorf("rotation in grace: got %v changed=%v, want [3.3.3.3 1.1.1.1] changed", got, ch)
	}

	// Same rotation past the grace: the stale endpoint ages out.
	seen = map[netip.Addr]time.Time{a1: now.Add(-20 * time.Minute)}
	if got, ch := reconcileWithGrace(addrsOf("1.1.1.1"), set("3.3.3.3"), false, seen, now, grace); !ch ||
		!sameAddrs(got, addrsOf("3.3.3.3")) {
		t.Errorf("rotation past grace: got %v changed=%v, want [3.3.3.3] changed", got, ch)
	}

	// Fresh sightings are stamped, so a just-seen endpoint's clock restarts.
	seen = map[netip.Addr]time.Time{}
	_, _ = reconcileWithGrace(addrsOf(), set("3.3.3.3"), false, seen, now, grace)
	if got, ok := seen[a3]; !ok || !got.Equal(now) {
		t.Errorf("stamp: lastSeen[3.3.3.3] = %v ok=%v, want stamped now", got, ok)
	}

	// growOnly (block / switch window) retains unconditionally — even past grace.
	seen = map[netip.Addr]time.Time{a1: now.Add(-20 * time.Minute)}
	if got, ch := reconcileWithGrace(addrsOf("1.1.1.1"), set("3.3.3.3"), true, seen, now, grace); !ch ||
		!sameAddrs(got, addrsOf("1.1.1.1", "3.3.3.3")) {
		t.Errorf("growOnly: got %v changed=%v, want union", got, ch)
	}

	// lastSeen is pruned of addresses that are neither current nor fresh.
	gone := netip.MustParseAddr("9.9.9.9")
	seen = map[netip.Addr]time.Time{gone: now.Add(-time.Hour)}
	_, _ = reconcileWithGrace(addrsOf("1.1.1.1"), set("1.1.1.1"), false, seen, now, grace)
	if _, ok := seen[gone]; ok {
		t.Error("prune: lastSeen kept an address that is neither current nor fresh")
	}
}

func TestReconcileTunnels(t *testing.T) {
	pinned := map[string]bool{"utun4": true}
	// Growth: a new observed tunnel is added.
	if got, ch := reconcileTunnels([]string{"utun4"}, []string{"utun4", "utun6"}, pinned); !ch ||
		!sameStrings(got, []string{"utun4", "utun6"}) {
		t.Errorf("growth: got %v changed=%v", got, ch)
	}
	// Pinned name is kept even when not observed; a non-pinned one is pruned.
	if got, ch := reconcileTunnels([]string{"utun4", "utun6"}, []string{}, pinned); !ch ||
		!sameStrings(got, []string{"utun4"}) {
		t.Errorf("prune non-pinned: got %v changed=%v", got, ch)
	}
	// Never narrow to empty (no pinned, nothing observed → keep current).
	if got, ch := reconcileTunnels([]string{"utun6"}, []string{}, nil); ch ||
		!sameStrings(got, []string{"utun6"}) {
		t.Errorf("never empty: got %v changed=%v", got, ch)
	}
	// No change when the set is identical.
	if _, ch := reconcileTunnels([]string{"utun4"}, []string{"utun4"}, pinned); ch {
		t.Error("identical set reported changed")
	}
}

// growWatcher emits {utun4} then {utun4,utun6} (a set-growth event).
func growWatcher() *netdetect.Watcher {
	states := []netdetect.TunnelState{
		{Up: true, Name: "utun4", Names: []string{"utun4"}},
		{Up: true, Name: "utun4", Names: []string{"utun4", "utun6"}},
	}
	i := 0
	return &netdetect.Watcher{
		Interval: time.Millisecond,
		Sample: func([]string) netdetect.TunnelState {
			st := states[i]
			if i < len(states)-1 {
				i++
			}
			return st
		},
	}
}

// A newly-appeared tunnel (autodetect) grows the set and re-applies the guard
// with the new interface in the pass list.
func TestVPNNewTunnelReappliesGuard(t *testing.T) {
	be := &fakeBackend{}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	o := Options{
		Monitor:    steadyMonitor{cc: "US"},
		Decider:    decision.New([]string{"IR"}, 1),
		Backend:    be,
		Log:        discardLog(),
		Interval:   time.Hour, // suppress geoTick during the test
		AutoDetect: true,
		Tunnels:    []string{"utun4"},
		Endpoints:  []netip.Addr{netip.MustParseAddr("203.0.113.7")},
		Watcher:    growWatcher(),
	}
	if err := Run(ctx, o); err != nil {
		t.Fatal(err)
	}
	// Some applied guard policy must carry utun6 (the grown interface).
	found := false
	for _, p := range be.policies {
		if p.Mode == firewall.ModeGuard {
			for _, ifc := range p.TunnelIfaces {
				if ifc == "utun6" {
					found = true
				}
			}
		}
	}
	if !found {
		t.Errorf("expected a guard policy re-applied with utun6; policies=%d", len(be.policies))
	}
}

// With autodetect and zero tunnels up, the standing posture is the endpoints-open
// FULL BLOCK shape (not a ModeGuard the backend would reject), and the geo step
// is suppressed (no tunnel to observe through).
func TestVPNZeroTunnelStandingPosture(t *testing.T) {
	be := &fakeBackend{}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	o := Options{
		Monitor:    steadyMonitor{cc: "US"},
		Decider:    decision.New([]string{"IR"}, 1),
		Backend:    be,
		Log:        discardLog(),
		Interval:   time.Millisecond, // geoTick would fire fast — must be suppressed
		AutoDetect: true,
		Tunnels:    nil, // no tunnels
		Endpoints:  []netip.Addr{netip.MustParseAddr("203.0.113.7")},
	}
	if err := Run(ctx, o); err != nil {
		t.Fatal(err)
	}
	if len(be.policies) == 0 || be.policies[0].Mode != firewall.ModeFullBlock {
		t.Fatalf("startup standing posture = %v, want ModeFullBlock endpoints-open shape", be.policies)
	}
	if len(be.policies[0].VPNEndpoints) == 0 {
		t.Error("standing posture must keep endpoints open")
	}
	// Geo suppressed with zero tunnels: no guard should ever be applied.
	for _, c := range be.calls {
		if c == "apply-guard" {
			t.Errorf("zero-tunnel posture must not apply ModeGuard; calls=%v", be.calls)
		}
	}
}

// scriptedCommands returns a PollCommand that yields each command once, in order.
func scriptedCommands(cmds ...command.Command) func() (command.Command, bool) {
	i := 0
	return func() (command.Command, bool) {
		if i >= len(cmds) {
			return command.Command{}, false
		}
		c := cmds[i]
		i++
		return c, true
	}
}

// A switch window opens on command and, on cancel, reverts to the prior posture
// (GUARD) immediately. (Expiry uses the same closeWindowRevert path, but a real
// expiry wait is too slow for a unit test, so cancel exercises the revert.)
func TestSwitchWindowCancelRevertsToGuard(t *testing.T) {
	be := &fakeBackend{}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	o := Options{
		Monitor:         steadyMonitor{cc: "US"},
		Decider:         decision.New([]string{"IR"}, 1),
		Backend:         be,
		Log:             discardLog(),
		Interval:        time.Hour,
		Tunnels:         []string{"utun4"},
		Endpoints:       []netip.Addr{netip.MustParseAddr("203.0.113.7")},
		SwitchWindow:    20 * time.Second,
		SwitchWindowMax: time.Minute,
		CommandPoll:     5 * time.Millisecond,
		PollCommand: scriptedCommands(
			command.Command{Op: command.OpOpenSwitchWindow},
			command.Command{Op: command.OpCancelSwitchWindow},
		),
	}
	if err := Run(ctx, o); err != nil {
		t.Fatal(err)
	}
	if !containsCall(be.calls, "apply-switch") {
		t.Fatalf("expected apply-switch (window open); calls=%v", be.calls)
	}
	// A guard apply must appear AFTER the switch apply (the cancel revert).
	if !applyGuardAfterSwitch(be.calls) {
		t.Fatalf("expected guard restored after window cancel; calls=%v", be.calls)
	}
}

// A switch window with a verified allowed exit closes early to GUARD and learns
// the discovered endpoint.
func TestSwitchWindowEarlyCloseLearnsEndpoint(t *testing.T) {
	be := &fakeBackend{}
	learned := map[string][]netip.Addr{}
	var snaps []state.Snapshot
	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	discovered := netip.MustParseAddr("198.51.100.9")
	o := Options{
		Publish:                 func(s state.Snapshot) { snaps = append(snaps, s) },
		Monitor:                 steadyMonitor{cc: "US"}, // exit verified allowed
		Decider:                 decision.New([]string{"IR"}, 1),
		Backend:                 be,
		Log:                     discardLog(),
		Interval:                time.Hour,
		Tunnels:                 []string{"utun4"},
		Endpoints:               []netip.Addr{netip.MustParseAddr("203.0.113.7")},
		SwitchWindow:            5 * time.Second, // long; must close EARLY, not on expiry
		SwitchWindowMax:         time.Minute,
		CommandPoll:             5 * time.Millisecond,
		WindowDiscoveryInterval: 5 * time.Millisecond,
		PollCommand:             scriptedCommands(command.Command{Op: command.OpOpenSwitchWindow, Profile: "newvpn"}),
		ResolveEndpointsWith: func(context.Context, []string) netdetect.EndpointSet {
			return netdetect.EndpointSet{
				Addrs:   []netip.Addr{netip.MustParseAddr("203.0.113.7"), discovered},
				Sources: map[netip.Addr]string{discovered: "discovered"},
			}
		},
		Learn: func(profile, iface string, addrs []netip.Addr) {
			learned[profile] = append(learned[profile], addrs...)
		},
	}
	if err := Run(ctx, o); err != nil {
		t.Fatal(err)
	}
	if got := learned["newvpn"]; len(got) != 1 || got[0] != discovered {
		t.Fatalf("learned[newvpn] = %v, want [%v]", got, discovered)
	}
	if !applyGuardAfterSwitch(be.calls) {
		t.Fatalf("expected guard applied after early close; calls=%v", be.calls)
	}
	// The verified close must attribute the active profile so status/GUI can show it.
	sawActive := false
	for _, s := range snaps {
		if s.ActiveProfile == "newvpn" {
			sawActive = true
		}
	}
	if !sawActive {
		t.Fatalf("expected a snapshot with ActiveProfile=%q after verified close; got %d snapshots", "newvpn", len(snaps))
	}
}

// switchThenGuardFailBackend succeeds the startup guard and the switch-window
// apply, but fails the guard apply a verified early-close attempts — so the
// close-to-guard path can be exercised under a firewall that won't cooperate.
type switchThenGuardFailBackend struct {
	fakeBackend
	sawSwitch bool
}

func (b *switchThenGuardFailBackend) Apply(p firewall.Policy) error {
	b.policies = append(b.policies, p)
	switch p.Mode {
	case firewall.ModeSwitchWindow:
		b.calls = append(b.calls, "apply-switch")
		b.sawSwitch = true
		return nil
	case firewall.ModeGuard:
		b.calls = append(b.calls, "apply-guard")
		if b.sawSwitch {
			return errors.New("guard apply failed")
		}
		return nil
	default:
		b.calls = append(b.calls, "apply-fullblock")
		return nil
	}
}

// A verified early-close whose guard apply FAILS must hold the window open: the
// firewall may still be in switch-window posture, so the runner must not learn,
// attribute an active profile, or report the window closed — it keeps retrying.
func TestSwitchWindowVerifiedCloseHoldsOpenOnApplyFailure(t *testing.T) {
	be := &switchThenGuardFailBackend{}
	learned := map[string][]netip.Addr{}
	var snaps []state.Snapshot
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	discovered := netip.MustParseAddr("198.51.100.9")
	o := Options{
		Publish:                 func(s state.Snapshot) { snaps = append(snaps, s) },
		Monitor:                 steadyMonitor{cc: "US"}, // exit would verify allowed
		Decider:                 decision.New([]string{"IR"}, 1),
		Backend:                 be,
		Log:                     discardLog(),
		Interval:                time.Hour,
		Tunnels:                 []string{"utun4"},
		Endpoints:               []netip.Addr{netip.MustParseAddr("203.0.113.7")},
		SwitchWindow:            5 * time.Second,
		SwitchWindowMax:         time.Minute,
		CommandPoll:             5 * time.Millisecond,
		WindowDiscoveryInterval: 5 * time.Millisecond,
		PollCommand:             scriptedCommands(command.Command{Op: command.OpOpenSwitchWindow, Profile: "newvpn"}),
		ResolveEndpointsWith: func(context.Context, []string) netdetect.EndpointSet {
			return netdetect.EndpointSet{
				Addrs:   []netip.Addr{netip.MustParseAddr("203.0.113.7"), discovered},
				Sources: map[netip.Addr]string{discovered: "discovered"},
			}
		},
		Learn: func(profile, iface string, addrs []netip.Addr) {
			learned[profile] = append(learned[profile], addrs...)
		},
	}
	if err := Run(ctx, o); err != nil {
		t.Fatal(err)
	}
	if len(learned) != 0 {
		t.Fatalf("learn must not run when the close apply fails; got %v", learned)
	}
	for _, s := range snaps {
		if s.ActiveProfile != "" {
			t.Fatalf("active profile must not be attributed when the close apply fails; got %q", s.ActiveProfile)
		}
	}
	if !applyGuardAfterSwitch(be.calls) {
		t.Fatalf("expected a guard-apply attempt after the switch open; calls=%v", be.calls)
	}
}

// containsCall reports whether the backend recorded a given call. The one copy
// for this package: it used to exist three times over (contains, hasCall,
// containsCall), so a test could assert against a helper that had quietly
// drifted from the others.
func containsCall(calls []string, want string) bool {
	return slices.Contains(calls, want)
}

func applyGuardAfterSwitch(calls []string) bool {
	seenSwitch := false
	for _, c := range calls {
		if c == "apply-switch" {
			seenSwitch = true
		}
		if seenSwitch && c == "apply-guard" {
			return true
		}
	}
	return false
}

// A live tunnel with NO known server address is the one shape the guard must never be
// armed in: its block-all covers the physical link, which is what carries the tunnel's
// own encrypted transport. Arming it cuts every packet, kills the VPN, and destroys the
// very socket endpoint discovery would have learned from — an unrecoverable blackout,
// not a kill switch. Autodetect/switch-window ("relaxed") must NOT excuse it: relaxed
// exists for the ZERO-tunnel case, where a total cut is correct and a switch window
// recovers it.
func TestVPNRefusesToArmGuardThatWouldCutTheTunnelsOwnTransport(t *testing.T) {
	be := &fakeBackend{}
	o := Options{
		Monitor:  &fakeMonitor{},
		Decider:  decision.New([]string{"IR"}, 1),
		Backend:  be,
		Log:      discardLog(),
		Interval: time.Millisecond,
		Tunnels:  []string{"utun4"}, // tunnel is up
		// Endpoints: none — discovery found nothing (WireGuard's unconnected UDP
		// socket never shows up as a connected flow).
		AutoDetect: true, // "relaxed" — must not rescue this
	}
	var snaps []state.Snapshot
	o.Publish = func(s state.Snapshot) { snaps = append(snaps, s) }
	err := Run(context.Background(), o)
	if err == nil {
		t.Fatal("daemon armed a guard with a live tunnel and no known endpoint; that cuts the tunnel's own transport and blacks the host out")
	}
	if !strings.Contains(err.Error(), "refusing to start") {
		t.Fatalf("err = %v, want a refusal to start", err)
	}
	// No rules may be APPLIED: refusing means the user keeps their network. (The
	// deferred Cleanup still runs, as it must — it is the safety net that guarantees
	// no dezhban rule can outlive the process, and with nothing applied it is a no-op.)
	for _, c := range be.calls {
		if strings.HasPrefix(c, "apply") {
			t.Fatalf("a ruleset was applied despite the refusal: %v", be.calls)
		}
	}
	// The refusal must be OBSERVABLE, not just returned: under a service manager
	// the returned error dies in a log nobody reads, and the state file is the one
	// surface `status --json` and the menubar app see. A bare "stopped" would be
	// indistinguishable from a deliberate shutdown.
	if len(snaps) == 0 {
		t.Fatal("no snapshot published on refusal")
	}
	last := snaps[len(snaps)-1]
	if last.Posture != "stopped" {
		t.Fatalf("final posture = %q, want \"stopped\"", last.Posture)
	}
	if !strings.Contains(last.EnforcementErr, "refusing to start") {
		t.Fatalf("final snapshot enforcementErr = %q, want the refusal reason", last.EnforcementErr)
	}
}

// The zero-tunnel case is the one `relaxed` is for: no VPN is connected, so a total cut
// is the correct standing posture and a switch window can recover from it.
func TestVPNArmsStandingPostureWithNoTunnelAndNoEndpoint(t *testing.T) {
	be := &fakeBackend{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // one pass through startup, then stop
	o := Options{
		Monitor:    &fakeMonitor{},
		Decider:    decision.New([]string{"IR"}, 1),
		Backend:    be,
		Log:        discardLog(),
		Interval:   time.Millisecond,
		AutoDetect: true,
	}
	if err := Run(ctx, o); err != nil {
		t.Fatalf("refused to start with no tunnel and no endpoint; that is the legal standing-cut case: %v", err)
	}
	if len(be.calls) == 0 {
		t.Fatal("no standing posture was applied")
	}
}

// --- automatic redial window ---

// edgeWatcher scripts a tunnel that is up for the first upSamples samples and
// permanently down afterwards: one clean up→down edge. Sample runs on the
// watcher's single goroutine, so the plain counter is race-free.
func edgeWatcher(upSamples int) *netdetect.Watcher {
	n := 0
	return &netdetect.Watcher{
		Interval: time.Millisecond,
		Sample: func([]string) netdetect.TunnelState {
			n++
			if n <= upSamples {
				return netdetect.TunnelState{Up: true, Name: "utun4", Names: []string{"utun4"}}
			}
			return netdetect.TunnelState{}
		},
	}
}

// steadyFailMonitor always fails the lookup, so no exit is ever confirmed.
type steadyFailMonitor struct{}

func (steadyFailMonitor) Poll(ctx context.Context) <-chan monitor.Result {
	ch := make(chan monitor.Result)
	go func() { <-ctx.Done(); close(ch) }()
	return ch
}
func (steadyFailMonitor) Once(context.Context) (monitor.Reading, error) {
	return monitor.Reading{}, errors.New("lookup failed")
}

// A tunnel drop from healthy GUARD must open the automatic redial window
// (ModeSwitchWindow), and its expiry must revert to GUARD — fail closed, no
// second window without a new up edge.
func TestVPNAutoRedialWindowOpensAndExpires(t *testing.T) {
	be := &fakeBackend{}
	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
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
		RedialWindow:       50 * time.Millisecond,
		RedialBudget:       testRedialBudget,
		RedialBudgetWindow: testRedialBudgetWindow,
	}
	if err := Run(ctx, o); err != nil {
		t.Fatal(err)
	}
	switches, guards := 0, 0
	for _, c := range be.calls {
		switch c {
		case "apply-switch":
			switches++
		case "apply-guard":
			guards++
		}
	}
	if switches != 1 {
		t.Fatalf("apply-switch count = %d, want exactly 1 (open once on the drop, never reopen while still down); calls = %v", switches, be.calls)
	}
	if guards < 2 {
		t.Fatalf("apply-guard count = %d, want >=2 (startup + fail-closed revert on expiry); calls = %v", guards, be.calls)
	}
	// The revert must come AFTER the window (fail closed on expiry).
	last := be.calls[len(be.calls)-2] // final call is cleanup
	if last != "apply-guard" {
		t.Fatalf("posture after expiry = %q, want apply-guard; calls = %v", last, be.calls)
	}
}

// The cut must be observable. On a tunnel drop the run loop opens the redial
// window in the same pass, so unless the guard-holding-a-downed-tunnel snapshot
// is published FIRST and the drop is then carried through the window, no
// observer could ever report the drop — they poll the state file about once a
// second and would only ever see "a window is open".
func TestTunnelDropPublishesTheCutBeforeRelaxing(t *testing.T) {
	be := &fakeBackend{}
	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()

	var mu sync.Mutex
	var snaps []state.Snapshot
	o := Options{
		Monitor:            steadyMonitor{cc: "US"},
		Decider:            decision.New([]string{"IR"}, 1),
		Backend:            be,
		Log:                discardLog(),
		Interval:           time.Millisecond,
		Tunnels:            []string{"utun4"},
		Endpoints:          []netip.Addr{netip.MustParseAddr("203.0.113.7")},
		Watcher:            edgeWatcher(5),
		RedialWindow:       50 * time.Millisecond,
		RedialBudget:       testRedialBudget,
		RedialBudgetWindow: testRedialBudgetWindow,
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

	firstDrop, firstWindow := -1, -1
	for i, s := range snaps {
		if firstDrop == -1 && s.Drop != nil {
			firstDrop = i
		}
		if firstWindow == -1 && s.Posture == "switch-window" {
			firstWindow = i
		}
	}
	if firstDrop == -1 {
		t.Fatalf("no snapshot carried a Drop record; postures = %v", postures(snaps))
	}
	if firstWindow == -1 {
		t.Fatalf("the redial window never opened; postures = %v", postures(snaps))
	}
	if firstDrop >= firstWindow {
		t.Errorf("the drop was first reported at snapshot %d but the window opened at %d — "+
			"the cut must be published before anything relaxes", firstDrop, firstWindow)
	}
	if got := snaps[firstDrop].Posture; got != "guard" {
		t.Errorf("the cut snapshot's posture = %q, want \"guard\" (the guard holding a downed tunnel)", got)
	}
	if snaps[firstDrop].Drop.At.IsZero() {
		t.Error("the drop record carries no time, which is the one thing it exists to report")
	}

	// Carried through the window, or the surface that users actually look at
	// still cannot name the drop.
	if snaps[firstWindow].Drop == nil {
		t.Error("the window snapshot dropped the record; both surfaces would be back to only saying \"window open\"")
	} else if !snaps[firstWindow].Drop.At.Equal(snaps[firstDrop].Drop.At) {
		t.Error("the window snapshot carries a different drop time than the cut it followed")
	}
}

// The drop record must not outlive the drop: once a tunnel is back, continuing
// to carry it would leave both surfaces narrating an event that has ended.
func TestDropRecordClearsWhenTheTunnelReturns(t *testing.T) {
	// Down for the first three samples, then up for the rest.
	n := 0
	watcher := &netdetect.Watcher{
		Interval: time.Millisecond,
		Sample: func([]string) netdetect.TunnelState {
			n++
			if n <= 2 || (n > 4 && n <= 6) {
				return netdetect.TunnelState{Up: true, Name: "utun4", Names: []string{"utun4"}}
			}
			if n > 6 {
				return netdetect.TunnelState{Up: true, Name: "utun4", Names: []string{"utun4"}}
			}
			return netdetect.TunnelState{}
		},
	}

	be := &fakeBackend{}
	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()

	var mu sync.Mutex
	var snaps []state.Snapshot
	o := Options{
		Monitor:            steadyMonitor{cc: "US"},
		Decider:            decision.New([]string{"IR"}, 1),
		Backend:            be,
		Log:                discardLog(),
		Interval:           time.Millisecond,
		Tunnels:            []string{"utun4"},
		Endpoints:          []netip.Addr{netip.MustParseAddr("203.0.113.7")},
		Watcher:            watcher,
		RedialWindow:       10 * time.Millisecond,
		RedialBudget:       testRedialBudget,
		RedialBudgetWindow: testRedialBudgetWindow,
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

	sawDrop := false
	for _, s := range snaps {
		if s.Drop != nil {
			sawDrop = true
		}
	}
	if !sawDrop {
		t.Fatal("the tunnel never dropped in this test; the fixture is wrong")
	}
	// The last snapshot with a tunnel up must carry no drop.
	for i := len(snaps) - 1; i >= 0; i-- {
		if !anyTunnelUp(snaps[i].Tunnels) {
			continue
		}
		if snaps[i].Drop != nil {
			t.Errorf("snapshot %d has a tunnel up but still carries a drop record", i)
		}
		break
	}
}

// Hold the line must suppress the automatic redial window for exactly one drop:
// a deliberate disconnect stays cut instead of being handed a relaxation nobody
// asked for. It never opens anything — the three sanctioned triggers are
// unchanged — so the assertion is the absence of a switch window.
func TestHoldTheLineSuppressesTheRedialWindow(t *testing.T) {
	be := &fakeBackend{}
	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()

	// Arm it over the command file before the tunnel drops.
	cmds := []command.Command{{Op: command.OpHoldArm, IssuedAt: time.Now(), Nonce: "hold-1"}}
	sent := 0
	o := Options{
		Monitor:            steadyMonitor{cc: "US"},
		Decider:            decision.New([]string{"IR"}, 1),
		Backend:            be,
		Log:                discardLog(),
		Interval:           time.Millisecond,
		Tunnels:            []string{"utun4"},
		Endpoints:          []netip.Addr{netip.MustParseAddr("203.0.113.7")},
		Watcher:            edgeWatcher(8),
		RedialWindow:       50 * time.Millisecond,
		RedialBudget:       testRedialBudget,
		RedialBudgetWindow: testRedialBudgetWindow,
		CommandPoll:        time.Millisecond,
		PollCommand: func() (command.Command, bool) {
			if sent < len(cmds) {
				c := cmds[sent]
				sent++
				return c, true
			}
			return command.Command{}, false
		},
	}
	if err := Run(ctx, o); err != nil {
		t.Fatal(err)
	}
	for _, c := range be.calls {
		if c == "apply-switch" {
			t.Fatalf("a redial window opened despite hold the line being armed; calls = %v", be.calls)
		}
	}
}

// Without the arm, the very same fixture must open a window — otherwise the
// test above would pass for the wrong reason (a fixture that never drops).
func TestWithoutHoldTheSameDropOpensAWindow(t *testing.T) {
	be := &fakeBackend{}
	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()

	o := Options{
		Monitor:            steadyMonitor{cc: "US"},
		Decider:            decision.New([]string{"IR"}, 1),
		Backend:            be,
		Log:                discardLog(),
		Interval:           time.Millisecond,
		Tunnels:            []string{"utun4"},
		Endpoints:          []netip.Addr{netip.MustParseAddr("203.0.113.7")},
		Watcher:            edgeWatcher(8),
		RedialWindow:       50 * time.Millisecond,
		RedialBudget:       testRedialBudget,
		RedialBudgetWindow: testRedialBudgetWindow,
		CommandPoll:        time.Millisecond,
		PollCommand:        func() (command.Command, bool) { return command.Command{}, false },
	}
	if err := Run(ctx, o); err != nil {
		t.Fatal(err)
	}
	opened := false
	for _, c := range be.calls {
		if c == "apply-switch" {
			opened = true
		}
	}
	if !opened {
		t.Fatalf("the control fixture never opened a window, so the hold test proves nothing; calls = %v", be.calls)
	}
}

// It is one-shot. Arming must not silently persist and cut a LATER, accidental
// drop off from the redial help it should have had.
func TestHoldTheLineIsSpentByTheDropItCovers(t *testing.T) {
	be := &fakeBackend{}
	ctx, cancel := context.WithTimeout(context.Background(), 600*time.Millisecond)
	defer cancel()

	// up, up, DOWN (hold spends here), up, up, DOWN (must open a window).
	n := 0
	watcher := &netdetect.Watcher{
		Interval: 2 * time.Millisecond,
		Sample: func([]string) netdetect.TunnelState {
			n++
			up := netdetect.TunnelState{Up: true, Name: "utun4", Names: []string{"utun4"}}
			switch {
			case n <= 3:
				return up
			case n <= 6:
				return netdetect.TunnelState{}
			case n <= 10:
				return up
			default:
				return netdetect.TunnelState{}
			}
		},
	}

	cmds := []command.Command{{Op: command.OpHoldArm, IssuedAt: time.Now(), Nonce: "hold-1"}}
	sent := 0
	o := Options{
		Monitor:            steadyMonitor{cc: "US"},
		Decider:            decision.New([]string{"IR"}, 1),
		Backend:            be,
		Log:                discardLog(),
		Interval:           time.Millisecond,
		Tunnels:            []string{"utun4"},
		Endpoints:          []netip.Addr{netip.MustParseAddr("203.0.113.7")},
		Watcher:            watcher,
		RedialWindow:       30 * time.Millisecond,
		RedialBudget:       testRedialBudget,
		RedialBudgetWindow: testRedialBudgetWindow,
		CommandPoll:        time.Millisecond,
		PollCommand: func() (command.Command, bool) {
			if sent < len(cmds) {
				c := cmds[sent]
				sent++
				return c, true
			}
			return command.Command{}, false
		},
	}
	if err := Run(ctx, o); err != nil {
		t.Fatal(err)
	}
	switches := 0
	for _, c := range be.calls {
		if c == "apply-switch" {
			switches++
		}
	}
	if switches == 0 {
		t.Fatalf("the second drop opened no window — hold the line outlived the drop it covered; calls = %v", be.calls)
	}
}

// postures summarizes a snapshot run for failure messages.
func postures(snaps []state.Snapshot) []string {
	out := make([]string, 0, len(snaps))
	for _, s := range snaps {
		out = append(out, s.Posture)
	}
	return out
}

// A manual command taking over an already-open AUTO window must keep the
// episode's original (auto) exposure cap, never the manual cap — see
// Options.RedialWindowMax's doc comment and the windowMax fork in Run's
// openWindow closure. SwitchWindowMax is deliberately set much larger than
// RedialWindowMax here: if the two caps were ever collapsed back into one
// shared value keyed off SwitchWindowMax (the pre-2026-07-22 shape), the
// manual takeover's 5s request would sail through un-clamped and the window
// would still be open when this test's context ends — no revert observed.
// With the caps correctly kept separate, the auto episode's cap holds and the
// window reverts (fail-closed) well within the test's deadline regardless of
// the takeover's requested duration.
func TestManualTakeoverKeepsAutoWindowExposureCap(t *testing.T) {
	be := &fakeBackend{}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	o := Options{
		Monitor:            steadyMonitor{cc: "US"},
		Decider:            decision.New([]string{"IR"}, 1),
		Backend:            be,
		Log:                discardLog(),
		Interval:           time.Millisecond,
		Tunnels:            []string{"utun4"},
		Endpoints:          []netip.Addr{netip.MustParseAddr("203.0.113.7")},
		Watcher:            edgeWatcher(2),        // drops at ~2ms, opening the AUTO window
		RedialWindow:       15 * time.Millisecond, // auto window's own initial duration
		RedialBudget:       testRedialBudget,
		RedialBudgetWindow: testRedialBudgetWindow,
		RedialWindowMax:    30 * time.Millisecond, // the correct cap for this episode
		SwitchWindow:       time.Second,           // manual switch windows enabled at all
		SwitchWindowMax:    10 * time.Second,      // deliberately far larger than the auto cap
		CommandPoll:        10 * time.Millisecond,
		PollCommand: scriptedCommands(
			// Arrives ~10ms in, while the auto window (opened ~2ms, due 15ms
			// later) is still active — a takeover, not a fresh open. clampWindow
			// only caps it against SwitchWindowMax (10s), so this passes through
			// requesting a 5s extension.
			command.Command{Op: command.OpOpenSwitchWindow, Duration: "5s"},
		),
	}
	if err := Run(ctx, o); err != nil {
		t.Fatal(err)
	}
	if !containsCall(be.calls, "apply-switch") {
		t.Fatalf("expected apply-switch (auto window open); calls=%v", be.calls)
	}
	if !applyGuardAfterSwitch(be.calls) {
		t.Fatalf("expected the auto episode's cap (RedialWindowMax) to force a revert well "+
			"before this test's deadline, regardless of the takeover's 5s request; calls=%v", be.calls)
	}
}

// A tunnel that was never OBSERVED up (armed start presumes up, but no watcher
// up sample and no confirmed exit) must not open an auto window on its first
// down sample — there is nothing to "redial".
func TestVPNAutoWindowRequiresObservedUp(t *testing.T) {
	be := &fakeBackend{}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	o := Options{
		Monitor:            steadyFailMonitor{},
		Decider:            decision.New([]string{"IR"}, 1),
		Backend:            be,
		Log:                discardLog(),
		Interval:           time.Millisecond,
		Tunnels:            []string{"utun4"},
		Endpoints:          []netip.Addr{netip.MustParseAddr("203.0.113.7")},
		Watcher:            downWatcher(),
		RedialWindow:       50 * time.Millisecond,
		RedialBudget:       testRedialBudget,
		RedialBudgetWindow: testRedialBudgetWindow,
	}
	if err := Run(ctx, o); err != nil {
		t.Fatal(err)
	}
	for _, c := range be.calls {
		if c == "apply-switch" {
			t.Fatalf("auto window opened for a tunnel never observed up; calls = %v", be.calls)
		}
	}
}

// flapWatcher scripts a flap: down at start (so no window opens on the initial
// presumed-up→down sample), then briefly up, then down for good. The final drop
// follows an OBSERVED up-streak of only a few milliseconds — exactly what the
// flap guard must suppress.
func flapWatcher() *netdetect.Watcher {
	n := 0
	return &netdetect.Watcher{
		Interval: time.Millisecond,
		Sample: func([]string) netdetect.TunnelState {
			n++
			if n > 5 && n <= 8 {
				return netdetect.TunnelState{Up: true, Name: "utun4", Names: []string{"utun4"}}
			}
			return netdetect.TunnelState{}
		},
	}
}

// The inversion ADR-0009 exists for. A drop after an up-streak shorter than
// RedialMinUptime, with no confirmed exit, used to get NO window at all — so a
// struggling VPN got no automatic help at exactly the moment it needed it, and
// the user had to run `dezhban switch` by hand. That is a product failure in a
// tool whose whole promise is minimum interaction, so a fast drop now still gets
// a window; it is the rolling budget, not the uptime, that eventually refuses.
//
// How much SHORTER the backed-off window is belongs to internal/redial, which
// tests the arithmetic directly. This test owns the wiring: that a fast drop
// reaches the ledger at all and that the ledger's grant opens a real window.
func TestVPNAutoWindowFastDropStillGetsAWindow(t *testing.T) {
	be := &fakeBackend{}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	o := Options{
		Monitor:            steadyFailMonitor{},
		Decider:            decision.New([]string{"IR"}, 1),
		Backend:            be,
		Log:                discardLog(),
		Interval:           time.Millisecond,
		Tunnels:            []string{"utun4"},
		Endpoints:          []netip.Addr{netip.MustParseAddr("203.0.113.7")},
		Watcher:            flapWatcher(),
		RedialWindow:       50 * time.Millisecond,
		RedialBudget:       testRedialBudget,
		RedialBudgetWindow: testRedialBudgetWindow,
		RedialMinUptime:    10 * time.Second, // every drop here counts as fast
	}
	if err := Run(ctx, o); err != nil {
		t.Fatal(err)
	}
	if !containsCall(be.calls, "apply-switch") {
		t.Fatalf("a fast drop got no redial window; the backoff is suppressing again "+
			"instead of shortening. calls = %v", be.calls)
	}
}

// The other half: the budget, not the uptime, is what refuses. Same fixture,
// same fast drop, but a budget too small to afford a window — the guard must
// hold and traffic stay cut. This is the bound ADR-0009 adds; without it the
// automatic window is unbounded across drops, which is what ships today.
func TestVPNAutoWindowRefusedWhenBudgetCannotAffordIt(t *testing.T) {
	be := &fakeBackend{}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	o := Options{
		Monitor:            steadyFailMonitor{},
		Decider:            decision.New([]string{"IR"}, 1),
		Backend:            be,
		Log:                discardLog(),
		Interval:           time.Millisecond,
		Tunnels:            []string{"utun4"},
		Endpoints:          []netip.Addr{netip.MustParseAddr("203.0.113.7")},
		Watcher:            flapWatcher(),
		RedialWindow:       50 * time.Millisecond,
		RedialBudget:       time.Millisecond, // cannot afford even one window
		RedialBudgetWindow: testRedialBudgetWindow,
	}
	if err := Run(ctx, o); err != nil {
		t.Fatal(err)
	}
	for _, c := range be.calls {
		if c == "apply-switch" {
			t.Fatalf("a window opened on a budget that could not afford it; calls = %v", be.calls)
		}
	}
}

// A drop while in FULL BLOCK (forbidden exit) must never auto-open a window:
// relaxing from a known-bad state needs an explicit operator command.
func TestVPNAutoWindowNotFromFullBlock(t *testing.T) {
	be := &fakeBackend{}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	o := Options{
		Monitor:            steadyMonitor{cc: "IR"}, // forbidden exit → FULL BLOCK at startup
		Decider:            decision.New([]string{"IR"}, 1),
		Backend:            be,
		Log:                discardLog(),
		Interval:           time.Millisecond,
		Tunnels:            []string{"utun4"},
		Endpoints:          []netip.Addr{netip.MustParseAddr("203.0.113.7")},
		Watcher:            edgeWatcher(5),
		RedialWindow:       50 * time.Millisecond,
		RedialBudget:       testRedialBudget,
		RedialBudgetWindow: testRedialBudgetWindow,
	}
	if err := Run(ctx, o); err != nil {
		t.Fatal(err)
	}
	for _, c := range be.calls {
		if c == "apply-switch" {
			t.Fatalf("auto window opened from FULL BLOCK; calls = %v", be.calls)
		}
	}
}

// A failed exit-country lookup must be classified, not blanket-reported.
//
// The symptom this fixes: dezhban showed geo-provider errors during switch and
// redial windows. Those are the moments the tunnel is DOWN — that is why the
// window exists — so there is no VPN exit to measure and the lookup failing is
// correct behaviour. Reporting it as an error trains people to ignore the field,
// and it was most of what made the providers look broken.
//
// A tunnel-up failure is a different thing entirely: the exit may be censoring
// the providers (an Iranian exit blocking them looks exactly like this), and
// that IS worth showing.
func TestLookupFailureClassification(t *testing.T) {
	cases := []struct {
		name           string
		tunnels        []state.Tunnel
		wantLookupErr  bool
		wantExitUnknwn bool
	}{
		{"tunnel up — genuine failure", []state.Tunnel{{Name: "utun4", Up: true}}, true, false},
		{"tunnel down — expected", []state.Tunnel{{Name: "utun4", Up: false}}, false, true},
		{"no tunnels at all — expected", nil, false, true},
		{"one of several up — genuine", []state.Tunnel{{Name: "utun4", Up: false}, {Name: "utun5", Up: true}}, true, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var got state.Snapshot
			o := Options{Publish: func(s state.Snapshot) { got = s }}
			o.publish(false, false, monitor.Reading{}, errors.New("all providers failed"), nil, c.tunnels, nil, nil, "", nil, nil, nil, diag{})

			if hasErr := got.LookupErr != ""; hasErr != c.wantLookupErr {
				t.Errorf("LookupErr set = %v, want %v (got %q)", hasErr, c.wantLookupErr, got.LookupErr)
			}
			if hasUnk := got.ExitUnknown != ""; hasUnk != c.wantExitUnknwn {
				t.Errorf("ExitUnknown set = %v, want %v (got %q)", hasUnk, c.wantExitUnknwn, got.ExitUnknown)
			}
			// Never both — an observer showing each field independently would
			// otherwise render the same condition twice, once alarmingly.
			if got.LookupErr != "" && got.ExitUnknown != "" {
				t.Error("LookupErr and ExitUnknown are both set; they are mutually exclusive")
			}
		})
	}
}

// A successful lookup sets neither field, whatever the tunnel state.
func TestSuccessfulLookupSetsNoErrorFields(t *testing.T) {
	var got state.Snapshot
	o := Options{Publish: func(s state.Snapshot) { got = s }}
	o.publish(false, false, monitor.Reading{CountryCode: "NL"}, nil, nil,
		[]state.Tunnel{{Name: "utun4", Up: true}}, nil, nil, "", nil, nil, nil, diag{})
	if got.LookupErr != "" || got.ExitUnknown != "" {
		t.Errorf("a successful lookup set LookupErr=%q ExitUnknown=%q, want both empty", got.LookupErr, got.ExitUnknown)
	}
}

// With tunnel-scoped provider passes in the FULL BLOCK ruleset, the recovery
// probe must NOT lift the guard.
//
// The old path applied ModeGuard — full tunnel egress — for up to
// probeEgressBudget on EVERY probe tick, just to make one HTTP request, and kept
// doing it for as long as a forbidden exit persisted. That is a recurring leak
// measured in seconds per tick, and it is what the tunnel-scoped pass removes.
func TestProbeSkipsGuardLiftWhenProvidersArePassed(t *testing.T) {
	be := &fakeBackend{}
	o := Options{Backend: be, Log: discardLog(), Monitor: &fakeMonitor{results: []monitor.Result{reading("IR")}}}

	fullBlock := firewall.Policy{
		Mode:          firewall.ModeFullBlock,
		TunnelIfaces:  []string{"utun4"},
		ProviderAddrs: []netip.Addr{netip.MustParseAddr("104.16.1.1")},
	}
	guard := firewall.Policy{Mode: firewall.ModeGuard, TunnelIfaces: []string{"utun4"}}

	if _, err := o.probe(context.Background(), guard, fullBlock); err != nil {
		t.Fatalf("probe: %v", err)
	}
	if len(be.calls) != 0 {
		t.Errorf("probe touched the firewall (%v) — with providers passed it must observe without lifting", be.calls)
	}
}

// Without provider passes the fallback must still work: lift, observe, re-cut.
// Losing that would leave a FULL BLOCK unable to observe its way out — a block
// that can never lift is worse than a bounded leak.
func TestProbeFallsBackToLiftWhenNoProviders(t *testing.T) {
	be := &fakeBackend{}
	o := Options{Backend: be, Log: discardLog(), Monitor: &fakeMonitor{results: []monitor.Result{reading("IR")}}}

	fullBlock := firewall.Policy{Mode: firewall.ModeFullBlock, TunnelIfaces: []string{"utun4"}}
	guard := firewall.Policy{Mode: firewall.ModeGuard, TunnelIfaces: []string{"utun4"}}

	if _, err := o.probe(context.Background(), guard, fullBlock); err != nil {
		t.Fatalf("probe: %v", err)
	}
	want := []string{"apply-guard", "apply-fullblock"}
	if strings.Join(be.calls, ",") != strings.Join(want, ",") {
		t.Errorf("fallback probe calls = %v, want %v (lift then re-cut)", be.calls, want)
	}
}

// --- the redial ledger's debit/credit pairing at the runner seam ---

// firstWindowFailsBackend fails the FIRST window-open Apply and succeeds after,
// so a test can observe what the ledger did about a window that never opened.
type firstWindowFailsBackend struct {
	mu     sync.Mutex
	calls  []string
	failed bool
}

func (b *firstWindowFailsBackend) Apply(p firewall.Policy) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	switch p.Mode {
	case firewall.ModeGuard:
		b.calls = append(b.calls, "apply-guard")
	case firewall.ModeSwitchWindow:
		if !b.failed {
			b.failed = true
			b.calls = append(b.calls, "apply-switch-failed")
			return errors.New("pfctl said no")
		}
		b.calls = append(b.calls, "apply-switch")
	default:
		b.calls = append(b.calls, "apply-fullblock")
	}
	return nil
}
func (b *firstWindowFailsBackend) Unblock() error           { return nil }
func (b *firstWindowFailsBackend) IsBlocked() (bool, error) { return true, nil }
func (b *firstWindowFailsBackend) Cleanup() error           { return nil }
func (b *firstWindowFailsBackend) seen() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.calls...)
}

// twoDropWatcher goes up, down, up, down — two separate drops, so a test can ask
// what the SECOND one was allowed to do.
func twoDropWatcher() *netdetect.Watcher {
	n := 0
	return &netdetect.Watcher{
		Interval: time.Millisecond,
		Sample: func([]string) netdetect.TunnelState {
			n++
			if (n > 5 && n <= 8) || (n > 20 && n <= 24) {
				return netdetect.TunnelState{Up: true, Name: "utun4", Names: []string{"utun4"}}
			}
			return netdetect.TunnelState{}
		},
	}
}

// A window whose rules never landed cost the user nothing, so it must cost the
// budget nothing. The grant is debited before Backend.Apply runs — it has to be,
// the decision comes first — so a failed Apply would otherwise leave the debit
// standing with no window to close it, and expire deliberately never ages an
// open episode out. The budget here affords exactly one window: if the failed
// open is charged, the second drop gets nothing.
func TestAFailedOpenCostsTheBudgetNothing(t *testing.T) {
	be := &firstWindowFailsBackend{}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	o := Options{
		Monitor:            steadyFailMonitor{},
		Decider:            decision.New([]string{"IR"}, 1),
		Backend:            be,
		Log:                discardLog(),
		Interval:           time.Millisecond,
		Tunnels:            []string{"utun4"},
		Endpoints:          []netip.Addr{netip.MustParseAddr("203.0.113.7")},
		Watcher:            twoDropWatcher(),
		RedialWindow:       20 * time.Millisecond,
		RedialBudget:       20 * time.Millisecond, // room for exactly one window
		RedialBudgetWindow: time.Minute,
	}
	if err := Run(ctx, o); err != nil {
		t.Fatal(err)
	}
	calls := be.seen()
	var failed, opened int
	for _, c := range calls {
		switch c {
		case "apply-switch-failed":
			failed++
		case "apply-switch":
			opened++
		}
	}
	if failed == 0 {
		t.Fatalf("the fixture never attempted a window open, so nothing is proven; calls = %v", calls)
	}
	if opened == 0 {
		t.Errorf("the second drop got no window: the first open FAILED (no rules applied, "+
			"no exposure taken) yet the budget was charged for it. The ledger is measuring "+
			"exposure OFFERED, which is what credit-on-close exists to prevent. calls = %v", calls)
	}
}

// docs/usage/cli.md promises a reader that an open window is reported by
// state.switch "instead, never here", and tells scripts to match on
// .redial.reason. A manual switch opened over a standing refusal used to publish
// both at once — the guard relaxed and, in the same snapshot, an explanation of
// why it was holding until 3:15PM.
func TestAnOpenWindowIsNeverPublishedBesideARefusal(t *testing.T) {
	be := &fakeBackend{}
	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()

	var mu sync.Mutex
	var snaps []state.Snapshot
	sent := false

	o := Options{
		Monitor:   steadyFailMonitor{},
		Decider:   decision.New([]string{"IR"}, 1),
		Backend:   be,
		Log:       discardLog(),
		Interval:  time.Millisecond,
		Tunnels:   []string{"utun4"},
		Endpoints: []netip.Addr{netip.MustParseAddr("203.0.113.7")},
		Watcher:   flapWatcher(),
		// Too small to afford any window, so the drop is refused and the
		// refusal stands while the tunnel stays down.
		RedialWindow:       50 * time.Millisecond,
		RedialBudget:       time.Millisecond,
		RedialBudgetWindow: time.Minute,
		SwitchWindow:       80 * time.Millisecond,
		SwitchWindowMax:    time.Minute,
		CommandPoll:        5 * time.Millisecond,
		// Open a manual window only once a refusal has actually been published,
		// so the two really do overlap rather than racing.
		PollCommand: func() (command.Command, bool) {
			mu.Lock()
			defer mu.Unlock()
			if sent {
				return command.Command{}, false
			}
			for _, s := range snaps {
				if s.Redial != nil {
					sent = true
					return command.Command{Op: command.OpOpenSwitchWindow}, true
				}
			}
			return command.Command{}, false
		},
		Publish: func(s state.Snapshot) {
			mu.Lock()
			snaps = append(snaps, s)
			mu.Unlock()
		},
	}
	if err := Run(ctx, o); err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	defer mu.Unlock()
	var refusals, both int
	for _, s := range snaps {
		if s.Redial == nil {
			continue
		}
		refusals++
		if s.Switch != nil {
			both++
			t.Logf("posture=%q switch.trigger=%q redial.reason=%q",
				s.Posture, s.Switch.Trigger, s.Redial.Reason)
		}
	}
	if refusals == 0 {
		t.Fatal("no refusal was ever published, so the overlap could not occur; nothing is proven")
	}
	if both > 0 {
		t.Errorf("%d snapshot(s) carry BOTH state.switch and state.redial — a script matching "+
			"on .redial.reason sees the guard holding while a window is open", both)
	}
}

// redialScriptWatcher replays a fixed up/down script, one entry per sample,
// holding the last entry forever. Unlike edgeWatcher it can produce a tunnel
// that comes back and drops AGAIN, which is what it takes to exhaust the redial
// budget; unlike recovery_test.go's scriptedWatcher it needs no test goroutine
// driving it, because the behaviour under test happens on its own with no
// further input. Each run is at least as long as netdetect's down debounce
// (2 samples) so every edge is actually emitted.
func redialScriptWatcher(script []bool) *netdetect.Watcher {
	n := 0
	return &netdetect.Watcher{
		Interval: time.Millisecond,
		Sample: func([]string) netdetect.TunnelState {
			up := script[len(script)-1]
			if n < len(script) {
				up = script[n]
			}
			n++
			if up {
				return netdetect.TunnelState{Up: true, Name: "utun4", Names: []string{"utun4"}}
			}
			return netdetect.TunnelState{}
		},
	}
}

// A refusal names an instant the guard can relax again; this is the test that it
// is a time the guard ACTS on rather than one it merely reports.
//
// The decision used to be retaken only on the next tunnel-down edge, so a tunnel
// that could not come back by itself — the rotated-server-address case the window
// exists for — produced no further edge, and the refusal stood until an operator
// ran `dezhban switch`. The budget refilling changed nothing on its own.
//
// The script drops twice: the first drop spends the budget, the second is refused
// against it. Then the tunnel stays DOWN, so any window that opens after that can
// only have come from the retry — there is no second up edge to trigger one.
func TestARefusedRedialRetriesWhenTheBudgetRefills(t *testing.T) {
	be := &fakeBackend{}
	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()

	// up ~15ms, down (drop 1), up again ~15ms, then down forever (drop 2).
	script := make([]bool, 0, 60)
	for i := 0; i < 15; i++ {
		script = append(script, true)
	}
	for i := 0; i < 15; i++ {
		script = append(script, false)
	}
	for i := 0; i < 15; i++ {
		script = append(script, true)
	}
	script = append(script, false)

	var (
		mu    sync.Mutex
		snaps []state.Snapshot
	)
	o := Options{
		// The lookup always fails, so no confirmed exit ever closes a window
		// early — every window costs its full grant, which is what makes the
		// budget reachable inside a test's lifetime.
		Monitor:      steadyFailMonitor{},
		Decider:      decision.New([]string{"IR"}, 1),
		Backend:      be,
		Log:          discardLog(),
		Interval:     time.Millisecond,
		Tunnels:      []string{"utun4"},
		Endpoints:    []netip.Addr{netip.MustParseAddr("203.0.113.7")},
		Watcher:      redialScriptWatcher(script),
		RedialWindow: 20 * time.Millisecond,
		// Room for one full window and no more, refilling 120ms after the first
		// window opened.
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

	// A refusal must have been published, or the test proved nothing.
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

	// After the refusal, a window must open while the tunnel is still DOWN. With
	// no further up edge in the script, the retry is the only thing that could
	// have opened it.
	reopened := false
	for _, s := range snaps[refusedAt:] {
		if s.Switch == nil || !s.Switch.Open {
			continue
		}
		if s.Switch.Trigger != state.TriggerAuto {
			t.Errorf("window after the refusal has trigger %q, want %q — the retry must stay trigger 2",
				s.Switch.Trigger, state.TriggerAuto)
		}
		if anyTunnelUp(s.Tunnels) {
			continue // a window with a tunnel up cannot be attributed to the retry
		}
		reopened = true
		// The refusal must be gone: a window is open, so nothing is being held.
		if s.Redial != nil {
			t.Errorf("a window is open but state.redial still reports %q — "+
				"exactly one of the two may be present", s.Redial.Reason)
		}
		break
	}
	if !reopened {
		t.Error("the refused drop never got a window once the budget refilled — " +
			"nextEligible is being published as a time nothing acts on")
	}
}

// A refusal explains a wait against a bound. Turning the automatic window off
// makes that explanation VOID, not merely unanswered: nothing governs the wait
// any more and nextEligible names an instant nothing will act on.
//
// The failure this pins is the published promise, not the missing window. With
// vpn.redialWindow reloaded to "0" the guard correctly holds — that is the
// setting doing its job. What must not happen is `status --json` and the app
// going on reporting "dezhban tries again at 3:15PM" for the rest of the cut,
// for a window that has been switched off entirely. The retry cannot clear it
// on its own: disabling the window is exactly what makes autoWindowPossible
// false, so the re-decision returns before reaching the ledger.
//
// Same fixture as TestARefusedRedialRetriesWhenTheBudgetRefills, which is the
// control: without the reload, that drop gets a window while the tunnel is down.
func TestDisablingTheRedialWindowDropsAStandingRefusal(t *testing.T) {
	be := &fakeBackend{}
	ctx, cancel := context.WithTimeout(context.Background(), 600*time.Millisecond)
	defer cancel()

	// up, down (drop 1 spends the budget), up, then down for the rest of the run.
	script := make([]bool, 0, 60)
	for i := 0; i < 15; i++ {
		script = append(script, true)
	}
	for i := 0; i < 15; i++ {
		script = append(script, false)
	}
	for i := 0; i < 15; i++ {
		script = append(script, true)
	}
	script = append(script, false)

	reloadC := make(chan LiveSettings, 1)
	var (
		mu       sync.Mutex
		snaps    []state.Snapshot
		disabled bool
	)
	o := Options{
		Monitor:            steadyFailMonitor{},
		Decider:            decision.New([]string{"IR"}, 1),
		Backend:            be,
		Log:                discardLog(),
		Interval:           time.Millisecond,
		Tunnels:            []string{"utun4"},
		Endpoints:          []netip.Addr{netip.MustParseAddr("203.0.113.7")},
		Watcher:            redialScriptWatcher(script),
		RedialWindow:       20 * time.Millisecond,
		RedialBudget:       25 * time.Millisecond,
		RedialBudgetWindow: 120 * time.Millisecond,
		ReloadC:            reloadC,
	}
	o.Publish = func(s state.Snapshot) {
		mu.Lock()
		defer mu.Unlock()
		snaps = append(snaps, s)
		// The moment a refusal is published, turn the automatic window off.
		if s.Redial != nil && !disabled {
			disabled = true
			ls := o.Live()
			ls.RedialWindow = -1 // the config.Disabled sentinel
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
		t.Fatal("no refusal was ever published; the budget never ran out and this fixture tests nothing")
	}
	// The LAST LIVE snapshot, not the last one: shutdown publishes a terminal
	// posture:"stopped" record that carries no refusal, and asserting on that
	// would pass whether or not the refusal was ever dropped. This test failed to
	// fail for exactly that reason before the distinction was made.
	var last *state.Snapshot
	for i := len(snaps) - 1; i >= 0; i-- {
		if snaps[i].Posture != "stopped" {
			last = &snaps[i]
			break
		}
	}
	if last == nil {
		t.Fatal("every snapshot was the terminal stopped record; fixture proved nothing")
	}
	// The run is 600ms against a 120ms rolling period, so the bound the refusal
	// named lifted long before the end. A refusal still standing at that point is
	// one nothing will ever act on.
	if last.Redial != nil {
		t.Errorf("the automatic window is off but state.redial still reports %q with nextEligible %v — "+
			"a refusal outliving the setting that justified it publishes a time nothing will honour",
			last.Redial.Reason, last.Redial.NextEligible)
	}
	// And the window really is off: the setting must still be doing its job.
	if last.Switch != nil && last.Switch.Open {
		t.Error("a window is open after vpn.redialWindow was set to \"0\"")
	}
}

// The retry must not multiply windows: one automatic window per drop is the
// standing rule, and a retry that re-armed after its own window closed would
// turn a single drop into a repeating relaxation.
func TestTheRetryStillOpensAtMostOneWindowPerDrop(t *testing.T) {
	be := &fakeBackend{}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	// Up briefly, then down for the rest of the run: exactly one drop.
	script := []bool{true, true, true, true, true, false}

	o := Options{
		Monitor:            steadyFailMonitor{},
		Decider:            decision.New([]string{"IR"}, 1),
		Backend:            be,
		Log:                discardLog(),
		Interval:           time.Millisecond,
		Tunnels:            []string{"utun4"},
		Endpoints:          []netip.Addr{netip.MustParseAddr("203.0.113.7")},
		Watcher:            redialScriptWatcher(script),
		RedialWindow:       20 * time.Millisecond,
		RedialBudget:       25 * time.Millisecond,
		RedialBudgetWindow: 60 * time.Millisecond,
	}
	if err := Run(ctx, o); err != nil {
		t.Fatal(err)
	}

	windows := 0
	for _, c := range be.calls {
		if c == "apply-switch" {
			windows++
		}
	}
	// One drop, one window. The budget refills repeatedly inside this run, so a
	// retry that re-armed after its own window closed would show up here as many.
	if windows != 1 {
		t.Errorf("one drop opened %d automatic windows, want exactly 1 — "+
			"the retry must not re-arm once a window has been granted", windows)
	}
}

// Hold the line suppresses the RETRY, not just the drop edge. An operator who
// arms it while already cut is saying "keep me cut", and a rule that may only
// subtract a relaxation has to be able to subtract this one too — otherwise the
// window the operator just refused arrives anyway a few seconds later, which
// reads as the daemon overruling them.
//
// The flag is deliberately NOT spent here: it names the next drop, and a cut
// already in progress is not one.
func TestHoldArmedMidCutSuppressesTheRetry(t *testing.T) {
	be := &fakeBackend{}
	ctx, cancel := context.WithTimeout(context.Background(), 600*time.Millisecond)
	defer cancel()

	// Same fixture as TestARefusedRedialRetriesWhenTheBudgetRefills, whose pass
	// is the control: without the hold, this drop DOES get a window from the
	// retry while the tunnel is still down.
	script := redialTwoDropScript()

	var (
		mu      sync.Mutex
		refused bool
		armed   bool
	)
	o := Options{
		Monitor:            steadyFailMonitor{},
		Decider:            decision.New([]string{"IR"}, 1),
		Backend:            be,
		Log:                discardLog(),
		Interval:           time.Millisecond,
		Tunnels:            []string{"utun4"},
		Endpoints:          []netip.Addr{netip.MustParseAddr("203.0.113.7")},
		Watcher:            redialScriptWatcher(script),
		RedialWindow:       20 * time.Millisecond,
		RedialBudget:       25 * time.Millisecond,
		RedialBudgetWindow: 120 * time.Millisecond,
		CommandPoll:        time.Millisecond,
		Publish: func(s state.Snapshot) {
			mu.Lock()
			defer mu.Unlock()
			if s.Redial != nil {
				refused = true
			}
		},
	}
	// Arm the moment a refusal stands, which is well before the retry deadline.
	o.PollCommand = func() (command.Command, bool) {
		mu.Lock()
		defer mu.Unlock()
		if refused && !armed {
			armed = true
			return command.Command{Op: command.OpHoldArm, IssuedAt: time.Now(), Nonce: "arm"}, true
		}
		return command.Command{}, false
	}
	if err := Run(ctx, o); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if !refused || !armed {
		t.Fatalf("fixture never reached the state under test (refused=%v armed=%v)", refused, armed)
	}
	// The first drop's window is expected; the retry's second one is not.
	windows := 0
	for _, c := range be.calls {
		if c == "apply-switch" {
			windows++
		}
	}
	if windows != 1 {
		t.Errorf("got %d automatic windows, want 1 — the retry opened one despite "+
			"hold the line being armed against the cut it was going to relax", windows)
	}
}

// Cancelling hold the line gives the pending re-decision back. Hold only ever
// SUBTRACTS a relaxation, so cancelling it must be able to restore what it took:
// the drop already qualified as trigger 2 at its own edge, and the only thing
// that had said no since was the operator, who has now changed their mind.
//
// Without this the retry fires once, is skipped by the hold, disarms itself, and
// nothing re-arms it — so the drop stays cut until the next tunnel-down edge,
// which cannot arrive while the tunnel is already down. That is the wall
// ADR-0009's retry exists to remove, reachable by using the feature that is
// meant to be the CAUTIOUS choice, and with both surfaces claiming throughout
// that dezhban re-checks on its own.
func TestCancellingHoldRestoresTheRetry(t *testing.T) {
	be := &fakeBackend{}
	ctx, cancel := context.WithTimeout(context.Background(), 700*time.Millisecond)
	defer cancel()

	script := redialTwoDropScript()

	var (
		mu       sync.Mutex
		snaps    []state.Snapshot
		refused  bool
		armed    bool
		canceled bool
		start    = time.Now()
	)
	o := Options{
		Monitor:            steadyFailMonitor{},
		Decider:            decision.New([]string{"IR"}, 1),
		Backend:            be,
		Log:                discardLog(),
		Interval:           time.Millisecond,
		Tunnels:            []string{"utun4"},
		Endpoints:          []netip.Addr{netip.MustParseAddr("203.0.113.7")},
		Watcher:            redialScriptWatcher(script),
		RedialWindow:       20 * time.Millisecond,
		RedialBudget:       25 * time.Millisecond,
		RedialBudgetWindow: 120 * time.Millisecond,
		CommandPoll:        time.Millisecond,
		Publish: func(s state.Snapshot) {
			mu.Lock()
			defer mu.Unlock()
			snaps = append(snaps, s)
			if s.Redial != nil {
				refused = true
			}
		},
	}
	o.PollCommand = func() (command.Command, bool) {
		mu.Lock()
		defer mu.Unlock()
		if refused && !armed {
			armed = true
			return command.Command{Op: command.OpHoldArm, IssuedAt: time.Now(), Nonce: "arm"}, true
		}
		// Cancel well AFTER the retry deadline (~120ms past the first window), so
		// the retry has already fired and been consumed by the hold. That is the
		// case with nothing left armed, and the one this test exists for.
		if armed && !canceled && time.Since(start) > 300*time.Millisecond {
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
	if !refused || !armed || !canceled {
		t.Fatalf("fixture never reached the state under test (refused=%v armed=%v canceled=%v)",
			refused, armed, canceled)
	}

	// Find where hold went from armed to cancelled, then require a window after
	// it — opened while the tunnel is still DOWN, so only the restored retry can
	// account for it.
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
		if s.Switch == nil || !s.Switch.Open || anyTunnelUp(s.Tunnels) {
			continue
		}
		if s.Switch.Trigger != state.TriggerAuto {
			t.Errorf("window after the cancel has trigger %q, want %q — a restored retry "+
				"is still trigger 2", s.Switch.Trigger, state.TriggerAuto)
		}
		return
	}
	t.Error("after hold the line was cancelled the refused drop never got a window: " +
		"the retry the hold consumed is never restored, so nothing re-decides until the " +
		"next tunnel-down edge, which cannot arrive while the tunnel is down")
}

// The drop shape both hold/retry tests above share with
// TestARefusedRedialRetriesWhenTheBudgetRefills: up, down (drop 1 spends the
// budget), up, then down for the rest of the run. Factored out so the control
// and the two hold cases cannot drift into testing different fixtures.
func redialTwoDropScript() []bool {
	script := make([]bool, 0, 60)
	for i := 0; i < 15; i++ {
		script = append(script, true)
	}
	for i := 0; i < 15; i++ {
		script = append(script, false)
	}
	for i := 0; i < 15; i++ {
		script = append(script, true)
	}
	return append(script, false)
}
