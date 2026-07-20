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
	d := New([]string{"IR", "ru"}, 1) // mixed case → normalized
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
	d := New([]string{"IR"}, 3)
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
	d := New([]string{"IR"}, 3)
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

// Errors never escalate on their own, however many arrive. Under the guard the
// standing rules ARE the fail-closed block for physical leaks, so a run of
// failed lookups must hold the posture rather than commit FULL BLOCK — which
// would cut the tunnel's own egress and livelock the reconnect that could fix
// the lookup in the first place.
func TestErrorsNeverCommitABlock(t *testing.T) {
	d := New([]string{"IR"}, 3)
	got := feed(d, []monitor.Result{
		fail(),   // neutral
		fail(),   // neutral
		ok("US"), // allowed country, agrees with committed Allow
		fail(),   // neutral
		fail(),   // neutral
		fail(),   // neutral — nothing ever commits off errors alone
	})
	assertSeq(t, got, []Verdict{Allow, Allow, Allow, Allow, Allow, Allow})
}

// An error mid-streak must not hand a blocked exit a free reprieve: the pending
// flip survives the blip and commits on the next agreeing reading. This is the
// difference between "errors are neutral" and "errors reset the streak" — the
// retired fail-open path did the latter, which let an exit that fails lookups
// every other tick postpone its block indefinitely.
func TestErrorMidStreakDoesNotCancelPendingFlip(t *testing.T) {
	d := New([]string{"IR"}, 3)
	got := feed(d, []monitor.Result{
		ok("IR"), // streak 1
		ok("IR"), // streak 2
		fail(),   // neutral — streak survives at 2
		ok("IR"), // streak 3 → commit Block
	})
	assertSeq(t, got, []Verdict{Allow, Allow, Allow, Block})
}

// The mirror case: once blocked, errors must not lift the block either.
func TestErrorsDoNotLiftACommittedBlock(t *testing.T) {
	d := New([]string{"IR"}, 1)
	got := feed(d, []monitor.Result{ok("IR"), fail(), fail()})
	assertSeq(t, got, []Verdict{Block, Block, Block})
}

func TestEmptyBlocklistAllowsEverything(t *testing.T) {
	d := New(nil, 1)
	got := feed(d, []monitor.Result{ok("IR"), ok("KP"), fail()})
	assertSeq(t, got, []Verdict{Allow, Allow, Allow})
}

func TestHysteresisFloorIsOne(t *testing.T) {
	d := New([]string{"IR"}, 0) // clamped to 1
	got := feed(d, []monitor.Result{ok("IR")})
	assertSeq(t, got, []Verdict{Block})
}
