package decision

import (
	"errors"
	"testing"

	"github.com/behnam-rk/dezhban/internal/monitor"
)

func ok(cc string) monitor.Result {
	return monitor.Result{Reading: monitor.Reading{CountryCode: cc}}
}

func fail() monitor.Result {
	return monitor.Result{Err: errors.New("all providers failed")}
}

// feed runs a sequence of readings through one Decider and returns the verdict
// after each reading, so a test can assert exactly when state flips.
func feed(d *Decider, in []monitor.Result) []Verdict {
	out := make([]Verdict, len(in))
	for i, r := range in {
		out[i] = d.Evaluate(r)
	}
	return out
}

func assertSeq(t *testing.T, got, want []Verdict) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("length mismatch: got %v want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("step %d: got %v, want %v (full: got %v want %v)", i, got[i], want[i], got, want)
		}
	}
}

// hysteresis=1 → behaves like the pure Phase-3 mapping (immediate toggle).
func TestNoHysteresisImmediateToggle(t *testing.T) {
	d := New([]string{"IR", "ru"}, true, 1) // mixed case → normalized
	got := feed(d, []monitor.Result{
		ok("US"), // allow
		ok("IR"), // block immediately
		ok("ir"), // lowercased input still blocked
		ok("RU"), // second config entry, case-folded
		ok("US"), // allow immediately
		ok(""),   // empty country → allow
	})
	assertSeq(t, got, []Verdict{Allow, Block, Block, Block, Allow, Allow})
}

func TestHysteresisRequiresConsecutiveAgreement(t *testing.T) {
	d := New([]string{"IR"}, true, 3)
	got := feed(d, []monitor.Result{
		ok("IR"), // streak 1 → still Allow
		ok("IR"), // streak 2 → still Allow
		ok("IR"), // streak 3 → commit Block
		ok("IR"), // stays Block
		ok("US"), // streak 1 toward Allow → still Block
		ok("US"), // streak 2 → still Block
		ok("US"), // streak 3 → commit Allow
	})
	assertSeq(t, got, []Verdict{Allow, Allow, Block, Block, Block, Block, Allow})
}

// A reading that agrees with the committed state resets a pending flip, so an
// alternating sequence never flaps the firewall.
func TestFlapResetsStreak(t *testing.T) {
	d := New([]string{"IR"}, true, 3)
	got := feed(d, []monitor.Result{
		ok("IR"), // streak 1 toward Block
		ok("US"), // agrees with committed Allow → reset
		ok("IR"), // streak 1 again
		ok("US"), // reset
		ok("IR"), // streak 1
		ok("IR"), // streak 2
		ok("IR"), // streak 3 → commit Block
	})
	assertSeq(t, got, []Verdict{Allow, Allow, Allow, Allow, Allow, Allow, Block})
}

func TestFailClosedNeedsConsecutiveErrors(t *testing.T) {
	d := New([]string{"IR"}, true, 3)
	got := feed(d, []monitor.Result{
		fail(),   // error → raw Block, streak 1
		fail(),   // streak 2
		ok("US"), // a good allowed reading → reset, no block
		fail(),   // streak 1 again
		fail(),   // streak 2
		fail(),   // streak 3 → commit Block (fail-closed)
	})
	assertSeq(t, got, []Verdict{Allow, Allow, Allow, Allow, Allow, Block})
}

func TestFailOpenErrorsNeverBlock(t *testing.T) {
	d := New([]string{"IR"}, false, 1) // fail-open
	got := feed(d, []monitor.Result{fail(), fail(), fail()})
	assertSeq(t, got, []Verdict{Allow, Allow, Allow})
}

func TestEmptyBlocklistAllowsEverything(t *testing.T) {
	d := New(nil, true, 1)
	got := feed(d, []monitor.Result{ok("IR"), ok("KP"), fail()})
	// fail-closed still blocks on error even with an empty blocklist.
	assertSeq(t, got, []Verdict{Allow, Allow, Block})
}

func TestHysteresisFloorIsOne(t *testing.T) {
	d := New([]string{"IR"}, true, 0) // clamped to 1
	got := feed(d, []monitor.Result{ok("IR")})
	assertSeq(t, got, []Verdict{Block})
}
