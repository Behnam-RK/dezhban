package runner

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/netip"
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
		vpn, blocked, window bool
		want                 string
	}{
		{false, false, false, "allow"},
		{false, true, false, "block"},
		{true, false, false, "guard"},
		{true, true, false, "full-block"},
		{true, false, true, "switch-window"},
		{true, true, true, "switch-window"}, // window overrides
	}
	for _, c := range cases {
		if got := postureName(c.vpn, c.blocked, c.window); got != c.want {
			t.Errorf("postureName(vpn=%v, blocked=%v, window=%v) = %q, want %q", c.vpn, c.blocked, c.window, got, c.want)
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

// In VPN guard mode an undeterminable country (lookup error) must HOLD the
// current posture, never escalate GUARD→FULL BLOCK. The standing guard is
// already the fail-closed block for physical leaks; escalating on an unknown
// would cut the tunnel's own egress and livelock the reconnect. hysteresis=1
// so that, without the hold, a single error would immediately FULL BLOCK.
func TestVPNHoldsGuardOnLookupError(t *testing.T) {
	be := &fakeBackend{}
	ctx, cancel := context.WithCancel(context.Background())
	o := Options{
		Monitor: &fakeMonitor{cancel: cancel, results: []monitor.Result{
			failResult(), // undeterminable → hold guard (must NOT full block)
			failResult(), // still undeterminable → still guard
		}},
		Decider:   decision.New([]string{"IR"}, true, 1), // failClosed, hysteresis 1
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
	// startup guard; IR → full block; each blocked tick probes (lift+re-cut)
	// but an error never restores guard.
	want := []string{
		"apply-guard",     // startup guard
		"apply-fullblock", // IR → FULL BLOCK
		"apply-guard",     // probe 1 lift
		"apply-fullblock", // probe 1 re-cut (error → hold block)
		"apply-guard",     // probe 2 lift
		"apply-fullblock", // probe 2 re-cut (error → hold block)
		"cleanup",
	}
	if !equal(be.calls, want) {
		t.Fatalf("calls = %v, want %v (a probe error must not lift FULL BLOCK)", be.calls, want)
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
		Decider:    decision.New([]string{"IR"}, true, 1),
		Backend:    be,
		Log:        discardLog(),
		Interval:   time.Hour, // suppress geoTick during the test
		VPN:        true,
		Autodetect: true,
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
		Decider:    decision.New([]string{"IR"}, true, 1),
		Backend:    be,
		Log:        discardLog(),
		Interval:   time.Millisecond, // geoTick would fire fast — must be suppressed
		VPN:        true,
		Autodetect: true,
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
// (GUARD) immediately. (Expiry uses the same closeWindowRevert path but the
// minimum window is 10s — too slow to assert in a unit test, so cancel exercises
// the revert.)
func TestSwitchWindowCancelRevertsToGuard(t *testing.T) {
	be := &fakeBackend{}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	o := Options{
		Monitor:         steadyMonitor{cc: "US"},
		Decider:         decision.New([]string{"IR"}, true, 1),
		Backend:         be,
		Log:             discardLog(),
		Interval:        time.Hour,
		VPN:             true,
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
		Decider:                 decision.New([]string{"IR"}, true, 1),
		Backend:                 be,
		Log:                     discardLog(),
		Interval:                time.Hour,
		VPN:                     true,
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
		Decider:                 decision.New([]string{"IR"}, true, 1),
		Backend:                 be,
		Log:                     discardLog(),
		Interval:                time.Hour,
		VPN:                     true,
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

func containsCall(calls []string, want string) bool {
	for _, c := range calls {
		if c == want {
			return true
		}
	}
	return false
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
		Decider:  decision.New([]string{"IR"}, true, 1),
		Backend:  be,
		Log:      discardLog(),
		Interval: time.Millisecond,
		VPN:      true,
		Tunnels:  []string{"utun4"}, // tunnel is up
		// Endpoints: none — discovery found nothing (WireGuard's unconnected UDP
		// socket never shows up as a connected flow).
		Autodetect: true, // "relaxed" — must not rescue this
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
		Decider:    decision.New([]string{"IR"}, true, 1),
		Backend:    be,
		Log:        discardLog(),
		Interval:   time.Millisecond,
		VPN:        true,
		Autodetect: true,
	}
	if err := Run(ctx, o); err != nil {
		t.Fatalf("refused to start with no tunnel and no endpoint; that is the legal standing-cut case: %v", err)
	}
	if len(be.calls) == 0 {
		t.Fatal("no standing posture was applied")
	}
}
