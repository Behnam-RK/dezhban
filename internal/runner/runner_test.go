package runner

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/netip"
	"sync"
	"testing"
	"time"

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

// fakeBackend records the sequence of calls made against it. blockErr/applyErr, when
// set, make the corresponding action fail (the call is still recorded) so tests can
// exercise enforcement-failure paths.
type fakeBackend struct {
	calls    []string
	policies []firewall.Policy
	blockErr error
	applyErr error
}

func (b *fakeBackend) Apply(p firewall.Policy) error {
	b.policies = append(b.policies, p)
	if p.Mode == firewall.ModeGuard {
		b.calls = append(b.calls, "apply-guard")
	} else {
		b.calls = append(b.calls, "apply-fullblock")
	}
	return b.applyErr
}
func (b *fakeBackend) Block(a firewall.Allowlist) error {
	b.calls = append(b.calls, "block")
	return b.blockErr
}
func (b *fakeBackend) Unblock() error {
	b.calls = append(b.calls, "unblock")
	return nil
}
func (b *fakeBackend) Cleanup() error {
	b.calls = append(b.calls, "cleanup")
	return nil
}

func reading(cc string) monitor.Result {
	return monitor.Result{Reading: monitor.Reading{CountryCode: cc}}
}

func failResult() monitor.Result {
	return monitor.Result{Err: errors.New("all providers failed")}
}

func discardLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// oneHostAL is a non-empty allowlist so the legacy mid-block refresh re-Blocks
// (an empty refresh is deliberately skipped — see TestLegacyRefreshSkipWhenEmpty).
func oneHostAL() firewall.Allowlist {
	return firewall.Allowlist{Hosts: []netip.Addr{netip.MustParseAddr("9.9.9.9")}}
}

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
		vpn, blocked bool
		want         string
	}{
		{false, false, "allow"},
		{false, true, "block"},
		{true, false, "guard"},
		{true, true, "full-block"},
	}
	for _, c := range cases {
		if got := postureName(c.vpn, c.blocked); got != c.want {
			t.Errorf("postureName(vpn=%v, blocked=%v) = %q, want %q", c.vpn, c.blocked, got, c.want)
		}
	}
}

// TestLegacyPublishesPostureTransitions asserts a snapshot fires on every poll
// with the correct posture as the daemon crosses allow→block→allow, then a
// terminal "stopped" snapshot on shutdown so observers flip immediately.
func TestLegacyPublishesPostureTransitions(t *testing.T) {
	var snaps []state.Snapshot
	be := &fakeBackend{}
	o := Options{
		Monitor: &fakeMonitor{results: []monitor.Result{
			reading("US"), // allow
			reading("IR"), // block (enter)
			reading("US"), // allow (unblock)
		}},
		Decider:          decision.New([]string{"IR"}, false, 1),
		Backend:          be,
		Log:              discardLog(),
		Allowlist:        oneHostAL,
		BlockedCountries: []string{"IR"},
		Publish:          func(s state.Snapshot) { snaps = append(snaps, s) },
	}
	if err := Run(context.Background(), o); err != nil {
		t.Fatal(err)
	}

	var postures []string
	for _, s := range snaps {
		postures = append(postures, s.Posture)
		if s.Mode != "legacy" {
			t.Errorf("mode = %q, want legacy", s.Mode)
		}
	}
	// Three poll transitions plus one terminal "stopped" on shutdown.
	if want := []string{"allow", "block", "allow", "stopped"}; !equal(postures, want) {
		t.Fatalf("postures = %v, want %v", postures, want)
	}
	// The poll snapshots carry the blocklist; the terminal stopped snapshot need not.
	for _, s := range snaps[:3] {
		if len(s.BlockedCountries) != 1 || s.BlockedCountries[0] != "IR" {
			t.Errorf("blockedCountries = %v, want [IR]", s.BlockedCountries)
		}
	}
	if snaps[0].Blocked || !snaps[1].Blocked || snaps[2].Blocked {
		t.Errorf("blocked flags = [%v %v %v], want [false true false]",
			snaps[0].Blocked, snaps[1].Blocked, snaps[2].Blocked)
	}
	if last := snaps[3]; last.Posture != "stopped" || last.Blocked {
		t.Errorf("terminal snapshot = {posture:%q blocked:%v}, want {stopped false}", last.Posture, last.Blocked)
	}
}

// TestLegacyBlockFailurePublishesEnforcementErr asserts that when the backend
// rejects a Block, the published snapshot surfaces the failure (EnforcementErr set)
// instead of a healthy-looking "allow" — otherwise the menubar would show a green
// shield during an active leak where the kill switch failed to engage.
func TestLegacyBlockFailurePublishesEnforcementErr(t *testing.T) {
	var snaps []state.Snapshot
	be := &fakeBackend{blockErr: errors.New("pfctl: cannot load rules")}
	o := Options{
		Monitor:   &fakeMonitor{results: []monitor.Result{reading("IR")}}, // decision Block → Block fails
		Decider:   decision.New([]string{"IR"}, false, 1),
		Backend:   be,
		Log:       discardLog(),
		Allowlist: oneHostAL,
		Publish:   func(s state.Snapshot) { snaps = append(snaps, s) },
	}
	if err := Run(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	if len(snaps) == 0 {
		t.Fatal("no snapshots published")
	}
	s := snaps[0] // the poll snapshot for the failed block (before the terminal stopped)
	if s.Posture != "allow" || s.Blocked {
		t.Errorf("posture=%q blocked=%v, want allow/false (block failed, nothing in force)", s.Posture, s.Blocked)
	}
	if s.EnforcementErr == "" {
		t.Error("EnforcementErr empty; want the block failure surfaced so the leak isn't shown as healthy")
	}
}

// TestPublishStallDoesNotBlockEnforcement asserts the core invariant behind the
// non-blocking publish: a wedged state-file writer (sink that never returns) must not
// stall the enforcement loop, and Cleanup must still run on shutdown despite it.
// (signalBackend is the concurrency-safe backend defined below.)
func TestPublishStallDoesNotBlockEnforcement(t *testing.T) {
	release := make(chan struct{})
	be := &signalBackend{blockCh: make(chan struct{}, 1)}
	o := Options{
		Monitor:   &fakeMonitor{results: []monitor.Result{reading("IR")}}, // triggers Block
		Decider:   decision.New([]string{"IR"}, false, 1),
		Backend:   be,
		Log:       discardLog(),
		Allowlist: oneHostAL,
		// Sink wedges on every publish until released — simulating a hung state-dir volume.
		Publish: func(s state.Snapshot) { <-release },
	}
	errc := make(chan error, 1)
	go func() { errc <- Run(context.Background(), o) }()

	// Enforcement must reach Block even though the publish sink is wedged.
	select {
	case <-be.blockCh:
	case <-time.After(2 * time.Second):
		close(release) // unwedge so the goroutine can exit
		t.Fatal("enforcement stalled: Block not called while publish sink was blocked")
	}

	// Let the wedged writer drain so Run's bounded flush returns promptly rather than
	// waiting out its 2s timeout.
	close(release)
	select {
	case err := <-errc:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not return after the sink was released")
	}
	// Cleanup must have run on shutdown regardless of the stalled writer.
	if !be.has("cleanup") {
		t.Error("Cleanup did not run despite the stalled publish writer")
	}
}

// --- legacy mode ---

func TestLegacyBlockRefreshThenUnblock(t *testing.T) {
	be := &fakeBackend{}
	o := Options{
		Monitor: &fakeMonitor{results: []monitor.Result{
			reading("US"), // allow, not blocked → no-op
			reading("IR"), // block (enter)
			reading("IR"), // still blocked → allowlist refresh (re-Block)
			reading("US"), // unblock
			reading("US"), // already allowed → no-op
		}},
		Decider:   decision.New([]string{"IR"}, false, 1),
		Backend:   be,
		Log:       discardLog(),
		Allowlist: oneHostAL,
	}
	if err := Run(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	// Two Blocks: one on entry, one mid-block refresh (idempotent), then Unblock.
	want := []string{"block", "block", "unblock", "cleanup"}
	if !equal(be.calls, want) {
		t.Fatalf("calls = %v, want %v", be.calls, want)
	}
}

func TestLegacyFailOpenReleasesOnError(t *testing.T) {
	be := &fakeBackend{}
	o := Options{
		Monitor: &fakeMonitor{results: []monitor.Result{
			reading("IR"), // block
			failResult(),  // fail-open: error → Allow → unblock
		}},
		Decider:   decision.New([]string{"IR"}, false, 1), // fail-open
		Backend:   be,
		Log:       discardLog(),
		Allowlist: oneHostAL,
	}
	if err := Run(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	want := []string{"block", "unblock", "cleanup"}
	if !equal(be.calls, want) {
		t.Fatalf("calls = %v, want %v", be.calls, want)
	}
}

func TestLegacyFailClosedHoldsOnError(t *testing.T) {
	be := &fakeBackend{}
	o := Options{
		Monitor: &fakeMonitor{results: []monitor.Result{
			reading("IR"), // block
			failResult(),  // fail-closed: error → Block → stays blocked (refresh)
		}},
		Decider:   decision.New([]string{"IR"}, true, 1), // fail-closed
		Backend:   be,
		Log:       discardLog(),
		Allowlist: oneHostAL,
	}
	if err := Run(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	// Never unblocks: a lookup error keeps the block (and refreshes the allowlist).
	want := []string{"block", "block", "cleanup"}
	if !equal(be.calls, want) {
		t.Fatalf("calls = %v, want %v", be.calls, want)
	}
}

// A mid-block allowlist refresh that resolves no provider IPs must not re-Block
// (which would narrow the rules to an empty list and strand recovery). The
// existing block is kept; only the entry Block fires.
func TestLegacyRefreshSkipWhenEmpty(t *testing.T) {
	be := &fakeBackend{}
	o := Options{
		Monitor: &fakeMonitor{results: []monitor.Result{
			reading("IR"), // enter block (empty allowlist allowed on entry)
			reading("IR"), // refresh resolves nothing → keep existing block, no re-Block
		}},
		Decider:   decision.New([]string{"IR"}, true, 1),
		Backend:   be,
		Log:       discardLog(),
		Allowlist: func() firewall.Allowlist { return firewall.Allowlist{} }, // always empty
	}
	if err := Run(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	want := []string{"block", "cleanup"}
	if !equal(be.calls, want) {
		t.Fatalf("calls = %v, want %v", be.calls, want)
	}
}

// --- VPN mode ---

func TestVPNGuardFullBlockAndProbeRecovery(t *testing.T) {
	be := &fakeBackend{}
	ctx, cancel := context.WithCancel(context.Background())
	o := Options{
		Monitor: &fakeMonitor{cancel: cancel, results: []monitor.Result{
			reading("US"), // allow, already guard → no-op
			reading("IR"), // full block (enter)
			reading("US"), // probe sees allowed country → recover to guard
		}},
		Decider:   decision.New([]string{"IR"}, true, 1),
		Backend:   be,
		Log:       discardLog(),
		Interval:  time.Millisecond,
		VPN:       true,
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
	if len(fb.Allowlist.DNS) != 0 || len(fb.Allowlist.Hosts) != 0 {
		t.Error("VPN full block must not carry a dst-IP allowlist")
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
		Decider:   decision.New([]string{"IR"}, true, 2),
		Backend:   be,
		Log:       discardLog(),
		Interval:  time.Millisecond,
		VPN:       true,
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

func TestVPNStartupGuardFailureAborts(t *testing.T) {
	be := &failingGuardBackend{}
	o := Options{
		Monitor:   &fakeMonitor{},
		Decider:   decision.New([]string{"IR"}, true, 1),
		Backend:   be,
		Log:       discardLog(),
		Interval:  time.Millisecond,
		VPN:       true,
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

func (b *failingGuardBackend) Apply(p firewall.Policy) error    { return errors.New("guard apply failed") }
func (b *failingGuardBackend) Block(a firewall.Allowlist) error { return nil }
func (b *failingGuardBackend) Unblock() error                   { return nil }
func (b *failingGuardBackend) Cleanup() error                   { b.cleanups++; return nil }

// --- tunnel watcher ---

// signalBackend is concurrency-safe (the watcher runs in its own goroutine) and
// signals on blockCh whenever Block is called, so a test can synchronize on it.
type signalBackend struct {
	mu      sync.Mutex
	calls   []string
	blockCh chan struct{}
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
func (b *signalBackend) Block(a firewall.Allowlist) error {
	b.record("block")
	select {
	case b.blockCh <- struct{}{}:
	default:
	}
	return nil
}
func (b *signalBackend) Unblock() error { b.record("unblock"); return nil }
func (b *signalBackend) Cleanup() error { b.record("cleanup"); return nil }
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

// In legacy mode a tunnel drop must block immediately, with no geo reading at
// all, and a still-down tunnel must not auto-unblock.
func TestLegacyTunnelDownBlocks(t *testing.T) {
	be := &signalBackend{blockCh: make(chan struct{}, 4)}
	ctx, cancel := context.WithCancel(context.Background())
	o := Options{
		Monitor:   idleMonitor{},
		Decider:   decision.New([]string{"IR"}, false, 1),
		Backend:   be,
		Log:       discardLog(),
		Allowlist: oneHostAL,
		Watcher:   downWatcher(),
	}
	done := make(chan error, 1)
	go func() { done <- Run(ctx, o) }()

	select {
	case <-be.blockCh:
	case <-time.After(2 * time.Second):
		cancel()
		t.Fatal("tunnel-down did not trigger an immediate block")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if be.has("unblock") {
		t.Error("a still-down tunnel must not auto-unblock")
	}
}

// In VPN mode the watcher is observability-only: a tunnel drop must NOT apply any
// firewall transition (the standing guard rule already cuts the leak). Only the
// startup guard should appear, plus cleanup.
func TestVPNWatcherObservabilityOnly(t *testing.T) {
	be := &fakeBackend{}
	// steadyMonitor always reports US (allowed), so the guard holds throughout and
	// the loop is bounded by the timeout, not by a fixed reading slice — the skip
	// added for a down tunnel would otherwise stop the geo ticks that drained it.
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	o := Options{
		Monitor:   steadyMonitor{cc: "US"},
		Decider:   decision.New([]string{"IR"}, true, 1),
		Backend:   be,
		Log:       discardLog(),
		Interval:  time.Millisecond,
		VPN:       true,
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
// endpoints the guard holds open for reconnect. So a failing monitor must NOT
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
		Decider:   decision.New([]string{"IR"}, true, 1), // fail-closed, no hysteresis
		Backend:   be,
		Log:       discardLog(),
		Interval:  100 * time.Millisecond, // geo ticks land long after the down edge (~1ms)
		VPN:       true,
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
