package runner

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/behnam-rk/dezhban/internal/control"
	"github.com/behnam-rk/dezhban/internal/state"
)

// writeRecorder stands in for the daemon's real config writer. Tests assert on
// what it was asked to write, and on whether it was asked at all — a refusal
// that still wrote the file would be the worst possible outcome of a gate.
type writeRecorder struct {
	got  []map[string]string
	fail error
}

func (w *writeRecorder) write(pairs map[string]string) error {
	if w.fail != nil {
		return w.fail
	}
	w.got = append(w.got, pairs)
	return nil
}

// configWriteOpts is a controlled loop with config-write enabled and a writer
// wired, plus a reload that hands back whatever the test wants adopted.
func configWriteOpts(be Backend, w *writeRecorder, next func() LiveSettings, report ReloadReport) Options {
	o := vpnOpts(be)
	o.AllowConfigOps = true
	o.WriteConfig = w.write
	o.ReloadConfig = func() (LiveSettings, ReloadReport, error) {
		return next(), report, nil
	}
	return o
}

// withToken starts a socket whose verifier accepts exactly one token, mirroring
// the daemon's wiring.
func withToken(tok string) func(*control.Server) {
	return func(s *control.Server) {
		s.VerifyToken = func(presented string) bool { return presented == tok }
	}
}

// The whole point of the op: an authorised client changes a setting and the
// daemon is enforcing the new value when it answers — not after a restart, not
// after the next poll. Proven behaviourally: allowSwitchOps is written false and
// the very next switch-open is refused.
func TestConfigWriteAppliesWithoutARestart(t *testing.T) {
	be := &fakeBackend{}
	w := &writeRecorder{}

	var o Options
	next := func() LiveSettings {
		ls := o.Live()
		ls.AllowSwitchOps = false // what the freshly-written file now says
		return ls
	}
	o = configWriteOpts(be, w, next, ReloadReport{Applied: []string{"control.allowSwitchOps"}})
	path := startControlledWith(t, o, withToken("good"))

	resp := do(t, path, control.Request{
		Op:     control.OpConfigWrite,
		Token:  "good",
		Config: map[string]string{"control.allowSwitchOps": "false"},
	})
	if !resp.OK {
		t.Fatalf("config-write refused: %+v", resp)
	}
	if len(w.got) != 1 || w.got[0]["control.allowSwitchOps"] != "false" {
		t.Fatalf("writer saw %v, want one write of control.allowSwitchOps=false", w.got)
	}
	if !equal(resp.Applied, []string{"control.allowSwitchOps"}) {
		t.Fatalf("Applied = %v, want the written key reported back", resp.Applied)
	}

	// The behavioural half: the loop is running on the new value already.
	if resp := do(t, path, control.Request{Op: control.OpOpenSwitch, Duration: "30s"}); resp.OK {
		t.Fatal("switch-open succeeded after allowSwitchOps was written false; the write was saved but not adopted")
	}
}

// The policy switch has to be a real off switch: a client holding a valid token
// is still refused when the host has said no. Otherwise control.allowConfigOps
// would be advisory, which is the class of bug this project treats as the worst
// one it can have.
func TestConfigWriteRefusedWhenPolicyDisabled(t *testing.T) {
	be := &fakeBackend{}
	w := &writeRecorder{}
	o := configWriteOpts(be, w, func() LiveSettings { return LiveSettings{} }, ReloadReport{})
	o.AllowConfigOps = false
	path := startControlledWith(t, o, withToken("good"))

	resp := do(t, path, control.Request{
		Op:     control.OpConfigWrite,
		Token:  "good",
		Config: map[string]string{"pollInterval": "5s"},
	})
	if resp.OK {
		t.Fatal("config-write accepted with control.allowConfigOps false")
	}
	if !strings.Contains(resp.Error, "allowConfigOps") {
		t.Errorf("error = %q, want it to name the setting that refused", resp.Error)
	}
	if len(w.got) != 0 {
		t.Fatalf("a refused config-write still wrote %v", w.got)
	}
}

// A rejected value must leave the daemon exactly as it was — and must not be
// followed by a reload, which would report success for a write that never
// happened.
func TestConfigWriteRejectionIsReportedAndAdoptsNothing(t *testing.T) {
	be := &fakeBackend{}
	w := &writeRecorder{fail: errors.New("invalid value for pollInterval: bad duration")}
	reloads := 0
	o := configWriteOpts(be, w, func() LiveSettings { reloads++; return LiveSettings{} }, ReloadReport{})
	path := startControlledWith(t, o, withToken("good"))

	resp := do(t, path, control.Request{
		Op:     control.OpConfigWrite,
		Token:  "good",
		Config: map[string]string{"pollInterval": "banana"},
	})
	if resp.OK {
		t.Fatal("config-write reported success for a value the writer rejected")
	}
	if !strings.Contains(resp.Error, "pollInterval") {
		t.Errorf("error = %q, want the writer's own message naming the key", resp.Error)
	}
	if reloads != 0 {
		t.Errorf("reloaded %d time(s) after a failed write; there was nothing new to adopt", reloads)
	}
	// The loop is still serving and still enforcing.
	if resp := do(t, path, control.Request{Op: control.OpStatus}); !resp.OK || resp.Posture != "guard" {
		t.Fatalf("status after a rejected write = %+v, want the guard still held", resp)
	}
}

// An empty request is a client bug, not a no-op success: answering OK would let
// a GUI report "saved" for a batch it failed to assemble.
func TestConfigWriteWithNoKeysIsRefused(t *testing.T) {
	be := &fakeBackend{}
	w := &writeRecorder{}
	o := configWriteOpts(be, w, func() LiveSettings { return LiveSettings{} }, ReloadReport{})
	path := startControlledWith(t, o, withToken("good"))

	resp := do(t, path, control.Request{Op: control.OpConfigWrite, Token: "good"})
	if resp.OK {
		t.Fatal("an empty config-write was accepted")
	}
	if len(w.got) != 0 {
		t.Fatalf("an empty config-write still wrote %v", w.got)
	}
}

// A daemon running on built-in defaults has no file to write. It must say so by
// name rather than fail obscurely — the client's next move (fall back to sudo,
// or tell the user to create a config) depends on knowing which it is.
func TestConfigWriteUnavailableWithoutAWriter(t *testing.T) {
	be := &fakeBackend{}
	o := vpnOpts(be)
	o.AllowConfigOps = true // policy allows it; the capability is simply absent
	path := startControlledWith(t, o, withToken("good"))

	resp := do(t, path, control.Request{
		Op:     control.OpConfigWrite,
		Token:  "good",
		Config: map[string]string{"pollInterval": "5s"},
	})
	if resp.OK {
		t.Fatal("config-write accepted by a daemon with no config file to write")
	}
	if !strings.Contains(resp.Error, "cannot write") {
		t.Errorf("error = %q, want it to say the daemon cannot write its configuration", resp.Error)
	}
}

// Lowering vpn.pauseMax has to bind the very next pause. The cap is a security
// setting: a reload that reported it applied while the loop kept clamping to the
// old, larger value would be exactly the silent-discard failure the config layer
// goes to such lengths to prevent.
func TestReloadLowersThePauseCapImmediately(t *testing.T) {
	be := &fakeBackend{}
	w := &writeRecorder{}

	snaps := make(chan state.Snapshot, 32)
	var o Options
	next := func() LiveSettings {
		ls := o.Live()
		ls.PauseMax = time.Minute // was 10m
		return ls
	}
	o = configWriteOpts(be, w, next, ReloadReport{Applied: []string{"vpn.pauseMax"}})
	o.Publish = func(s state.Snapshot) {
		select {
		case snaps <- s:
		default:
		}
	}
	path := startControlledWith(t, o, withToken("good"))

	if resp := do(t, path, control.Request{
		Op:     control.OpConfigWrite,
		Token:  "good",
		Config: map[string]string{"vpn.pauseMax": "1m"},
	}); !resp.OK {
		t.Fatalf("config-write refused: %+v", resp)
	}

	// Ask for far more than the new cap allows.
	before := time.Now()
	if resp := do(t, path, control.Request{Op: control.OpPause, Duration: "9m"}); !resp.OK {
		t.Fatalf("pause refused: %+v", resp)
	}

	var until time.Time
	deadline := time.Now().Add(3 * time.Second)
	for until.IsZero() && time.Now().Before(deadline) {
		select {
		case s := <-snaps:
			if s.Switch != nil && s.Switch.Open && s.Switch.Trigger == state.TriggerPause {
				until = s.Switch.Until
			}
		case <-time.After(50 * time.Millisecond):
		}
	}
	if until.IsZero() {
		t.Fatal("no snapshot reported an open pause")
	}
	if got := until.Sub(before); got > 2*time.Minute {
		t.Fatalf("pause runs for %s, want it clamped to the reloaded 1m cap (the old 10m cap was still in force)", got.Round(time.Second))
	}
}
