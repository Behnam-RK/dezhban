package runner

import (
	"context"
	"errors"
	"log/slog"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/behnam-rk/dezhban/internal/command"
	"github.com/behnam-rk/dezhban/internal/control"
	"github.com/behnam-rk/dezhban/internal/decision"
	"github.com/behnam-rk/dezhban/internal/firewall"
	"github.com/behnam-rk/dezhban/internal/state"
)

// pollUntil polls cond every 5ms until it returns true or timeout elapses, at
// which point it fails the test with msg.
func pollUntil(t *testing.T, timeout time.Duration, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal(msg)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// countingHandler counts slog records whose message contains substr, so a
// test can poll for a specific number of log-visible events instead of
// sleeping over a fixed wall-clock window.
// atomic.Int64, not a *int64 driven by atomic.AddInt64: the same as
// countingMonitor and scriptedWatcher in this package, and the typed form is
// the one that cannot be read non-atomically by accident.
type countingHandler struct {
	substr string
	count  *atomic.Int64
}

func (h countingHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h countingHandler) Handle(_ context.Context, r slog.Record) error {
	if strings.Contains(r.Message, h.substr) {
		h.count.Add(1)
	}
	return nil
}

func (h countingHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h countingHandler) WithGroup(string) slog.Handler      { return h }

// controlSocket returns a short socket path (see internal/control: t.TempDir()
// overruns the platform sun_path limit).
func controlSocket(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "dzr")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return filepath.Join(dir, "c.sock")
}

// startControlled runs the loop with a live control socket and returns the socket
// path. The loop is cancelled and drained on cleanup.
func startControlled(t *testing.T, o Options) string {
	return startControlledWith(t, o, nil)
}

// startControlledWith is startControlled with a hook to configure the server
// before it starts — used by the token-gated ops, which need a verifier wired.
func startControlledWith(t *testing.T, o Options, tune func(*control.Server)) string {
	t.Helper()
	path := controlSocket(t)
	srv, err := control.New(path, "", discardLog())
	if err != nil {
		t.Fatalf("control.New: %v", err)
	}
	if tune != nil {
		tune(srv)
	}
	o.Control = srv
	if o.Log == nil {
		o.Log = discardLog()
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, o) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("Run: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("run loop did not exit")
		}
	})

	// Wait for the loop to install its startup posture and start serving.
	pollUntil(t, 3*time.Second, func() bool { return control.Ping(path) }, "control socket never became reachable")
	return path
}

func do(t *testing.T, path string, req control.Request) control.Response {
	t.Helper()
	resp, err := control.Do(path, req)
	if err != nil {
		t.Fatalf("control.Do(%s): %v", req.Op, err)
	}
	return resp
}

// vpnOpts is a VPN-mode loop that never moves on its own: a steady allowed
// country and a long poll interval, so any posture change in the test is
// attributable to the control socket alone.
func vpnOpts(be Backend) Options {
	return Options{
		Monitor:         steadyMonitor{cc: "US"},
		Decider:         decision.New([]string{"IR"}, 1),
		Backend:         be,
		Interval:        time.Hour,
		Tunnels:         []string{"utun4"},
		Endpoints:       []netip.Addr{netip.MustParseAddr("203.0.113.7")},
		SwitchWindow:    time.Minute,
		SwitchWindowMax: 5 * time.Minute,
		AllowSwitchOps:  true,
		PauseMax:        10 * time.Minute,
		AllowPauseOps:   true,
		// A switch window (or a pause) needs a command poller wired; the socket
		// path is what the test drives, but switchEnabled/pauseEnabled gate on
		// this being non-nil.
		PollCommand: func() (command.Command, bool) { return command.Command{}, false },
		CommandPoll: time.Hour,
	}
}

// The core promise: block/unblock over the socket drive the Backend, and they do
// it from the run-loop goroutine (the only one allowed to).
func TestControlBlockUnblockVPN(t *testing.T) {
	be := &fakeBackend{}
	path := startControlled(t, vpnOpts(be))

	resp := do(t, path, control.Request{Op: control.OpBlock})
	if !resp.OK || resp.Posture != "full-block" || !resp.Blocked {
		t.Fatalf("block response = %+v, want an OK full-block", resp)
	}

	resp = do(t, path, control.Request{Op: control.OpUnblock})
	if !resp.OK || resp.Posture != "guard" || resp.Blocked {
		t.Fatalf("unblock response = %+v, want an OK guard", resp)
	}

	want := []string{"apply-guard", "apply-fullblock", "apply-guard"}
	if !equal(be.calls, want) {
		t.Fatalf("calls = %v, want %v (startup guard, manual full block, manual guard restore)", be.calls, want)
	}
}

// A manual block must survive an allowed geo reading — otherwise the daemon would
// quietly undo what the operator asked for, and the recovery probe would open
// egress to observe a country nobody is acting on.
func TestControlManualBlockHeldAcrossGeoTicks(t *testing.T) {
	be := &fakeBackend{}
	o := vpnOpts(be)
	o.Interval = 5 * time.Millisecond // geo ticks fire continuously
	var skipped atomic.Int64
	o.Log = slog.New(countingHandler{substr: "manual block held", count: &skipped})
	path := startControlled(t, o)

	if resp := do(t, path, control.Request{Op: control.OpBlock}); !resp.OK {
		t.Fatalf("block refused: %+v", resp)
	}
	callsAfterBlock := len(be.calls)

	// Wait for many geo ticks to actually be suspended by the manual block — not
	// merely idle. A running state machine would probe (apply-guard +
	// apply-fullblock) and then lift the block on the steady "US" reading.
	pollUntil(t, 3*time.Second, func() bool { return skipped.Load() >= 10 },
		"geo ticks were not suspended by the manual block in time")

	resp := do(t, path, control.Request{Op: control.OpStatus})
	if !resp.Blocked || resp.Posture != "full-block" {
		t.Fatalf("manual block was lifted by the geo loop: %+v", resp)
	}
	if n := len(be.calls); n != callsAfterBlock {
		t.Fatalf("backend was driven %d extra time(s) while a manual block was held (calls=%v); the geo state machine must be suspended", n-callsAfterBlock, be.calls)
	}

	// Unblock hands the wheel back: geo resumes and the steady allowed reading is
	// now free to act (it has nothing to do — we are already at guard).
	if resp := do(t, path, control.Request{Op: control.OpUnblock}); !resp.OK || resp.Blocked {
		t.Fatalf("unblock response = %+v", resp)
	}
}

// A switch window relaxes the guard; block/unblock must not silently tear it down
// behind the operator's back.
func TestControlBlockRefusedDuringSwitchWindow(t *testing.T) {
	be := &fakeBackend{}
	path := startControlled(t, vpnOpts(be))

	if resp := do(t, path, control.Request{Op: control.OpOpenSwitch, Duration: "30s"}); !resp.OK {
		t.Fatalf("switch-open refused: %+v", resp)
	}
	resp := do(t, path, control.Request{Op: control.OpBlock})
	if resp.OK {
		t.Fatal("block accepted while a switch window was open; it must refuse rather than tear the window down")
	}
	if !contains(be.calls, "apply-switch") {
		t.Fatalf("calls = %v, want a switch-window policy applied", be.calls)
	}
}

// Switch ops are passwordless by DEFAULT (AllowSwitchOps true), and open a bounded
// window; cancelling reverts to the prior posture.
func TestControlSwitchOpenAndCancel(t *testing.T) {
	be := &fakeBackend{}
	path := startControlled(t, vpnOpts(be))

	resp := do(t, path, control.Request{Op: control.OpOpenSwitch, Duration: "30s", Profile: "home"})
	if !resp.OK || resp.Posture != "switch-window" {
		t.Fatalf("switch-open response = %+v, want an OK switch-window", resp)
	}
	resp = do(t, path, control.Request{Op: control.OpCancelSwitch})
	if !resp.OK || resp.Posture != "guard" {
		t.Fatalf("switch-cancel response = %+v, want an OK revert to guard", resp)
	}
	want := []string{"apply-guard", "apply-switch", "apply-guard"}
	if !equal(be.calls, want) {
		t.Fatalf("calls = %v, want %v", be.calls, want)
	}
}

// Pause is a third, independently-gated trigger on the same window machinery:
// open/resume drive the Backend exactly like a switch window does, and a
// switch-cancel must refuse to touch an open pause (must use resume instead).
func TestControlPauseOpenAndResume(t *testing.T) {
	be := &fakeBackend{}
	path := startControlled(t, vpnOpts(be))

	resp := do(t, path, control.Request{Op: control.OpPause, Duration: "30s"})
	if !resp.OK || resp.Posture != "switch-window" {
		t.Fatalf("pause response = %+v, want an OK switch-window", resp)
	}

	// A plain switch-cancel must not be able to end a pause.
	resp = do(t, path, control.Request{Op: control.OpCancelSwitch})
	if resp.OK {
		t.Fatalf("switch-cancel unexpectedly ended an open pause: %+v", resp)
	}

	// Nor may a switch-open hijack one: openWindow's manual-takeover path would
	// rebrand the pause as a switch window, after which resume no-ops ("already
	// closed") while egress stays open.
	resp = do(t, path, control.Request{Op: control.OpOpenSwitch, Duration: "30s"})
	if resp.OK {
		t.Fatalf("switch-open unexpectedly took over an open pause: %+v", resp)
	}

	resp = do(t, path, control.Request{Op: control.OpResume})
	if !resp.OK || resp.Posture != "guard" {
		t.Fatalf("resume response = %+v, want an OK revert to guard", resp)
	}
	want := []string{"apply-guard", "apply-switch", "apply-guard"}
	if !equal(be.calls, want) {
		t.Fatalf("calls = %v, want %v", be.calls, want)
	}
}

// The opt-out: with allowPauseOps false, the socket refuses to relax the guard
// and the operator is pushed back to the root-owned command file — independent
// of allowSwitchOps, which stays on.
func TestControlPauseOpsDisabled(t *testing.T) {
	be := &fakeBackend{}
	o := vpnOpts(be)
	o.AllowPauseOps = false
	path := startControlled(t, o)

	resp := do(t, path, control.Request{Op: control.OpPause, Duration: "30s"})
	if resp.OK {
		t.Fatalf("pause succeeded with allowPauseOps=false: %+v", resp)
	}
	if !equal(be.calls, []string{"apply-guard"}) {
		t.Fatalf("calls = %v, want only the startup guard", be.calls)
	}
}

// The opt-out: with allowSwitchOps false, the socket refuses to relax the guard and
// the operator is pushed back to the root-owned command file.
func TestControlSwitchOpsDisabled(t *testing.T) {
	be := &fakeBackend{}
	o := vpnOpts(be)
	o.AllowSwitchOps = false
	path := startControlled(t, o)

	resp := do(t, path, control.Request{Op: control.OpOpenSwitch, Duration: "30s"})
	if resp.OK {
		t.Fatal("switch-open accepted with control.allowSwitchOps=false")
	}
	if resp = do(t, path, control.Request{Op: control.OpCancelSwitch}); resp.OK {
		t.Fatal("switch-cancel accepted with control.allowSwitchOps=false")
	}
	// Block/unblock stay available — only the guard-relaxing op is gated.
	if resp := do(t, path, control.Request{Op: control.OpBlock}); !resp.OK {
		t.Fatalf("block refused with switch ops disabled: %+v", resp)
	}
	if contains(be.calls, "apply-switch") {
		t.Fatalf("a switch-window policy was applied despite allowSwitchOps=false: %v", be.calls)
	}
}

// A failed Apply must be reported to the caller, not swallowed into a false OK.
func TestControlBlockFailureReported(t *testing.T) {
	// Fail only the full block: the startup guard must still succeed, or the loop
	// aborts before it ever serves the socket.
	be := &fullBlockFailsBackend{}
	path := startControlled(t, vpnOpts(be))

	resp := do(t, path, control.Request{Op: control.OpBlock})
	if resp.OK {
		t.Fatal("block reported OK despite the Backend failing")
	}
	if resp.Error == "" {
		t.Fatal("block failure carried no error message")
	}
	// The posture must still read as guard — a failed block must not be published
	// as if it took effect.
	if resp.Blocked {
		t.Fatalf("a failed block was reported as blocked: %+v", resp)
	}
}

// fullBlockFailsBackend applies everything except a full block, which always fails.
type fullBlockFailsBackend struct{ fakeBackend }

func (b *fullBlockFailsBackend) Apply(p firewall.Policy) error {
	_ = b.fakeBackend.Apply(p)
	if p.Mode == firewall.ModeFullBlock {
		return errFake
	}
	return nil
}

func TestControlSocketRemovedOnShutdown(t *testing.T) {
	be := &fakeBackend{}
	path := controlSocket(t)
	srv, err := control.New(path, "", discardLog())
	if err != nil {
		t.Fatalf("control.New: %v", err)
	}
	o := vpnOpts(be)
	o.Control = srv
	o.Log = discardLog()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, o) }()
	pollUntil(t, 3*time.Second, func() bool { return control.Ping(path) }, "control socket never became reachable")
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("control socket survived daemon shutdown: %v", err)
	}
}

// A control-driven unblock into standby (vpn.autoArm, tunnel down) must clear
// any stale enforcement-verification finding left over from the armed state
// that just ended. dg.verify is otherwise only ever touched by the verifyC
// tick, which is (correctly) skipped in standby — so without an explicit
// reset at the transition, a "rules missing" finding from before the drop
// would keep being republished forever, even though nothing is installed in
// standby by design.
func TestVerifyFindingClearedOnStandbyEntry(t *testing.T) {
	be := &fakeBackend{isBlockedFn: func() (bool, error) { return false, nil }} // rules always "missing"
	o := vpnOpts(be)
	o.AutoArm = true
	o.Watcher = downWatcher()
	o.VerifyInterval = 5 * time.Millisecond
	var downEdges atomic.Int64
	o.Log = slog.New(countingHandler{substr: "vpn tunnel down — guard holds the line", count: &downEdges})
	var last atomic.Pointer[state.Snapshot]
	o.Publish = func(s state.Snapshot) { last.Store(&s) }
	path := startControlled(t, o)

	// Wait for the watcher's down edge to actually reach the run loop (tunnelUp
	// = false), not just for the watcher to start — the log fires on the same
	// goroutine right after the assignment, so seeing it guarantees the
	// unblock below observes tunnelUp already false.
	pollUntil(t, 2*time.Second, func() bool { return downEdges.Load() >= 1 },
		"tunnel-down edge was never observed by the run loop")
	pollUntil(t, 2*time.Second, func() bool {
		s := last.Load()
		return s != nil && s.Verify != nil && s.Verify.Missing
	}, "enforcement verification never reported the rules missing")

	resp := do(t, path, control.Request{Op: control.OpUnblock})
	if !resp.OK || resp.Posture != "standby" {
		t.Fatalf("unblock response = %+v, want an OK standby", resp)
	}

	if s := last.Load(); s.Verify != nil {
		t.Fatalf("a stale verify finding survived the standby transition: %+v", s.Verify)
	}
}

func contains(calls []string, want string) bool {
	for _, c := range calls {
		if c == want {
			return true
		}
	}
	return false
}

var errFake = errors.New("backend refused")
