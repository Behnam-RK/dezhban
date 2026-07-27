package redial

import (
	"testing"
	"time"
)

// Defaults, so the tests read as the shipped behaviour rather than as arbitrary
// numbers: a 30s window, 2m of it allowed per rolling 15m, backoff seeded at a
// 15s uptime.
func defaults() Settings {
	return Settings{
		Window:    30 * time.Second,
		Budget:    2 * time.Minute,
		Interval:  15 * time.Minute,
		MinUptime: 15 * time.Second,
	}
}

var t0 = time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)

// The ordinary drop: a tunnel that was up long enough gets the whole window and
// arms no cooldown. This is the case the budget must not make worse.
func TestHealthyDropGetsTheFullWindow(t *testing.T) {
	s := defaults()
	b := New()

	g := b.Grant(t0, 5*time.Minute, true, s)
	if !g.OK() || g.Duration != s.Window {
		t.Fatalf("duration = %v, want %v (grant %+v)", g.Duration, s.Window, g)
	}
	if g.Reason != ReasonFull {
		t.Errorf("reason = %q, want %q", g.Reason, ReasonFull)
	}
	if b.ShortRun() != 0 {
		t.Errorf("a healthy drop counted toward the backoff: %d", b.ShortRun())
	}
}

// A tunnel that came up briefly but PROVED itself — a confirmed non-blocked exit
// — is not flapping, however short the uptime. Treating it as a flap would back
// off from a VPN that is working.
func TestConfirmedExitIsNotAFlap(t *testing.T) {
	s := defaults()
	b := New()

	g := b.Grant(t0, 2*time.Second, true, s)
	if g.Duration != s.Window || g.Reason != ReasonFull {
		t.Errorf("a confirmed exit was treated as a flap: %+v", g)
	}
}

// The behaviour this whole change exists for: a fast drop still gets a window.
// The old anti-flap gate refused outright, which pushed the user onto the manual
// path at exactly the moment the automatic one was most useful.
func TestFastDropIsShortenedNotRefused(t *testing.T) {
	s := defaults()
	b := New()

	g := b.Grant(t0, 5*time.Second, false, s)
	if !g.OK() {
		t.Fatalf("a fast drop was refused outright: %+v", g)
	}
	if g.Duration != 15*time.Second {
		t.Errorf("duration = %v, want 15s (half of %v)", g.Duration, s.Window)
	}
	if g.Reason != ReasonBackoff {
		t.Errorf("reason = %q, want %q", g.Reason, ReasonBackoff)
	}
}

// Consecutive fast drops halve the window and lengthen the cooldown, so a
// pathological flap decays toward "cut and holding" instead of chaining windows.
func TestConsecutiveFastDropsBackOff(t *testing.T) {
	s := defaults()
	b := New()

	// Drop 1, healthy uptime: full window, no cooldown.
	if g := b.Grant(t0, time.Minute, true, s); g.Duration != 30*time.Second {
		t.Fatalf("drop 1: %v, want 30s", g.Duration)
	}
	b.Close(t0.Add(30 * time.Second))

	// Drop 2, fast: halved, and a 30s cooldown armed.
	at := t0.Add(time.Minute)
	g := b.Grant(at, 5*time.Second, false, s)
	if g.Duration != 15*time.Second {
		t.Fatalf("drop 2: %v, want 15s", g.Duration)
	}
	b.Close(at.Add(15 * time.Second))

	// Inside the cooldown, a further drop is refused and says when help returns.
	if g := b.Grant(at.Add(10*time.Second), 3*time.Second, false, s); g.OK() {
		t.Errorf("a drop inside the cooldown opened a window: %+v", g)
	} else if g.Reason != ReasonCooldown {
		t.Errorf("reason = %q, want %q", g.Reason, ReasonCooldown)
	} else if !g.NextEligible.After(at) {
		t.Errorf("NextEligible = %v, want after %v", g.NextEligible, at)
	}
	// A refused drop must not deepen the backoff — the cooldown is already the
	// response, and escalating would punish a drop that got no help.
	if b.ShortRun() != 1 {
		t.Errorf("a cooled-down drop deepened the backoff: ShortRun = %d, want 1", b.ShortRun())
	}

	// Drop 3, after the cooldown, still fast: quartered.
	at = at.Add(31 * time.Second)
	if g := b.Grant(at, 4*time.Second, false, s); g.Duration != 7500*time.Millisecond {
		t.Errorf("drop 3: %v, want 7.5s", g.Duration)
	}
}

// A healthy uptime clears the backoff. Without this a single bad afternoon would
// keep shortening windows for a VPN that had since recovered.
func TestHealthyUptimeResetsTheBackoff(t *testing.T) {
	s := defaults()
	b := New()

	b.Grant(t0, 2*time.Second, false, s)
	b.Close(t0.Add(15 * time.Second))
	if b.ShortRun() == 0 {
		t.Fatal("a fast drop did not register")
	}

	at := t0.Add(10 * time.Minute)
	if g := b.Grant(at, 5*time.Minute, true, s); g.Duration != s.Window {
		t.Errorf("duration = %v, want the full %v after a healthy uptime", g.Duration, s.Window)
	}
	if b.ShortRun() != 0 {
		t.Errorf("ShortRun = %d, want 0", b.ShortRun())
	}
}

// The bound the ADR exists to add: total open time inside the rolling interval
// cannot exceed the budget, however many drops occur.
func TestBudgetIsExhaustedAndHolds(t *testing.T) {
	s := defaults()
	s.MinUptime = 0 // isolate the budget from the backoff
	b := New()

	// Four full windows spend the whole 2m budget.
	at := t0
	for i := range 4 {
		g := b.Grant(at, time.Minute, true, s)
		if !g.OK() {
			t.Fatalf("window %d was refused with budget remaining: %+v", i+1, g)
		}
		at = at.Add(30 * time.Second)
		b.Close(at) // ran the full 30s
		at = at.Add(time.Minute)
	}
	if r := b.Remaining(at, s); r != 0 {
		t.Fatalf("remaining = %v, want 0 after four full windows", r)
	}

	g := b.Grant(at, time.Minute, true, s)
	if g.OK() {
		t.Fatalf("a fifth window opened past the budget: %+v", g)
	}
	if g.Reason != ReasonExhausted {
		t.Errorf("reason = %q, want %q", g.Reason, ReasonExhausted)
	}
	// "The guard is holding" is only half an answer; a refusal has to say when
	// help comes back or the user cannot tell it from a permanent failure.
	if !g.NextEligible.After(at) {
		t.Errorf("NextEligible = %v, want a real instant after %v", g.NextEligible, at)
	}
	if want := t0.Add(s.Interval); g.NextEligible != want {
		t.Errorf("NextEligible = %v, want %v (when the first episode rolls off)", g.NextEligible, want)
	}
}

// Credit-on-close is what keeps the budget from biting a healthy link: a window
// that closed in three seconds exposed you for three seconds, so that is what it
// costs. Charging the offer would punish the successful redial — the exact
// outcome the window exists to produce.
func TestEarlyCloseCreditsTheUnusedRemainder(t *testing.T) {
	s := defaults()
	s.MinUptime = 0
	b := New()

	at := t0
	for range 10 {
		g := b.Grant(at, time.Minute, true, s)
		if !g.OK() {
			t.Fatalf("a fast-redialling link exhausted its budget: remaining %v", b.Remaining(at, s))
		}
		at = at.Add(3 * time.Second) // reconnected almost at once
		b.Close(at)
		at = at.Add(time.Minute)
	}
	// Ten successful redials at 3s each cost 30s of the 2m budget.
	if got, want := s.Budget-b.Remaining(at, s), 30*time.Second; got != want {
		t.Errorf("spent = %v, want %v", got, want)
	}
}

// An open window is committed, not free. Reporting it as available would let a
// surface promise room that is already claimed.
func TestAnOpenWindowCountsAtItsFullGrant(t *testing.T) {
	s := defaults()
	s.MinUptime = 0
	b := New()

	b.Grant(t0, time.Minute, true, s)
	if got, want := b.Remaining(t0, s), 90*time.Second; got != want {
		t.Errorf("remaining = %v, want %v while a 30s window is open", got, want)
	}
}

// The ledger is rolling, and it refills one episode at a time rather than all at
// once — each frees its own cost as it falls out of the interval, so a link that
// is merely busy gets help back promptly instead of waiting for a full period of
// silence.
func TestBudgetRefillsPerEpisode(t *testing.T) {
	s := defaults()
	s.MinUptime = 0
	b := New()

	// Four full windows, one every minute, spending the whole budget.
	starts := []time.Time{}
	at := t0
	for range 4 {
		starts = append(starts, at)
		b.Grant(at, time.Minute, true, s)
		b.Close(at.Add(30 * time.Second))
		at = at.Add(time.Minute)
	}
	if r := b.Remaining(at, s); r != 0 {
		t.Fatalf("remaining = %v, want 0 after four full windows", r)
	}

	// Just after the FIRST episode ages out, exactly its 30s is back — not the
	// whole budget.
	afterFirst := starts[0].Add(s.Interval + time.Second)
	if r := b.Remaining(afterFirst, s); r != 30*time.Second {
		t.Errorf("remaining = %v, want 30s once the first episode rolled off", r)
	}
	if g := b.Grant(afterFirst, time.Minute, true, s); !g.OK() {
		t.Errorf("a partially refilled budget still refused: %+v", g)
	}
	b.Close(afterFirst.Add(30 * time.Second))

	// Once every original episode has aged out, only that newest one is charged.
	afterAll := starts[3].Add(s.Interval + time.Second)
	if r := b.Remaining(afterAll, s); r != s.Budget-30*time.Second {
		t.Errorf("remaining = %v, want %v", r, s.Budget-30*time.Second)
	}
}

// A sliver of a window is all cost and no benefit: it relaxes the guard without
// leaving a client time to hand-shake. Refusing keeps the remainder for a drop
// that can use it.
func TestASliverIsRefusedRatherThanSpent(t *testing.T) {
	s := Settings{Window: 30 * time.Second, Budget: 33 * time.Second, Interval: 15 * time.Minute}
	b := New()

	at := t0
	g := b.Grant(at, time.Minute, true, s)
	if g.Duration != 30*time.Second {
		t.Fatalf("first grant = %v, want the full window", g.Duration)
	}
	at = at.Add(30 * time.Second)
	b.Close(at)

	// 3s left, below MinGrant.
	if g := b.Grant(at, time.Minute, true, s); g.OK() {
		t.Errorf("a %v sliver was spent: %+v", g.Duration, g)
	}
}

// A budget that can afford something, but less than a full window, opens for
// what it has — truncation is honest, and the client may well redial inside it.
func TestAPartialBudgetTruncatesTheWindow(t *testing.T) {
	s := Settings{Window: 30 * time.Second, Budget: 50 * time.Second, Interval: 15 * time.Minute}
	b := New()

	at := t0
	b.Grant(at, time.Minute, true, s)
	at = at.Add(30 * time.Second)
	b.Close(at)

	g := b.Grant(at, time.Minute, true, s)
	if g.Duration != 20*time.Second {
		t.Errorf("duration = %v, want the remaining 20s", g.Duration)
	}
	if g.Reason != ReasonTruncated {
		t.Errorf("reason = %q, want %q", g.Reason, ReasonTruncated)
	}
}

// A deliberately short vpn.redialWindow is a decision. Opening for longer than
// asked — because MinGrant said so — would be the mirror image of silently
// discarding a security setting.
func TestAShortConfiguredWindowIsHonoured(t *testing.T) {
	s := Settings{Window: 2 * time.Second, Budget: time.Minute, Interval: 15 * time.Minute, MinUptime: 15 * time.Second}
	b := New()

	if g := b.Grant(t0, time.Minute, true, s); g.Duration != 2*time.Second {
		t.Errorf("duration = %v, want the configured 2s, never MinGrant's %v", g.Duration, MinGrant)
	}
}

// Backoff is off when MinUptime is: the anti-flap gate honours the Disabled
// sentinel, so "0" must mean every qualifying drop gets a full window until the
// budget itself runs out.
func TestBackoffDisabledByZeroMinUptime(t *testing.T) {
	s := defaults()
	s.MinUptime = 0
	b := New()

	for range 3 {
		if g := b.Grant(t0, time.Second, false, s); g.Duration != s.Window {
			t.Fatalf("duration = %v, want the full window with backoff disabled", g.Duration)
		}
	}
	if b.ShortRun() != 0 {
		t.Errorf("ShortRun = %d, want 0 with backoff disabled", b.ShortRun())
	}
}

// Close with nothing open is a no-op, so the run loop's close paths need no
// "was this an automatic window" bookkeeping of their own.
func TestCloseWithNothingOpenIsHarmless(t *testing.T) {
	s := defaults()
	b := New()
	b.Close(t0)
	if r := b.Remaining(t0, s); r != s.Budget {
		t.Errorf("remaining = %v, want the untouched %v", r, s.Budget)
	}
}

// A window still open when its interval elapses must not be aged out of the
// ledger: losing the debit would leave Close nothing to settle and quietly make
// the longest windows the cheapest. The interval here is deliberately shorter
// than the window, which is a misconfiguration rather than a shipped default —
// but it is the only way to reach the case, and "the longest windows are free"
// is not an acceptable answer to it.
func TestAnOpenEpisodeIsNeverAgedOut(t *testing.T) {
	s := Settings{Window: 30 * time.Second, Budget: 2 * time.Minute, Interval: 10 * time.Second}
	b := New()

	b.Grant(t0, time.Minute, true, s)
	at := t0.Add(25 * time.Second) // well past the interval, still open
	if r := b.Remaining(at, s); r != 90*time.Second {
		t.Errorf("remaining = %v, want the open window still charged", r)
	}

	// Settling it hands it to the ordinary rolling rule, which — the episode
	// being older than the interval — retires it at once. That is the rule
	// working, not the debit being lost: what must never happen is the charge
	// disappearing while the guard is still relaxed.
	b.Close(at)
	if r := b.Remaining(at, s); r != s.Budget {
		t.Errorf("remaining = %v, want %v once a settled episode ages out normally", r, s.Budget)
	}
}

// A refusal gave the drop no help, so it must not deepen the backoff or push the
// cooldown out. The cooldown path has always worked this way; this pins the same
// rule for the budget path, which used to escalate before it refused. Left
// unfixed, a run of refusals compounds into a wait no bound ever asked for: the
// ledger rolls over and the guard keeps holding on a cooldown built entirely out
// of drops it declined to assist with.
func TestARefusedDropDoesNotDeepenTheBackoff(t *testing.T) {
	s := defaults()
	b := New()

	// Spend the budget on healthy drops, so nothing is owed to the backoff yet.
	// 2m of budget against a 30s window is exactly four full windows.
	for i := range 4 {
		at := t0.Add(time.Duration(i) * time.Second)
		if g := b.Grant(at, 5*time.Minute, true, s); !g.OK() {
			t.Fatalf("setup grant %d refused: %+v", i, g)
		}
		b.Close(at.Add(s.Window)) // ran to expiry, so it cost the whole grant
	}
	if r := b.Remaining(t0, s); r != 0 {
		t.Fatalf("remaining = %v, want the budget fully spent before the real check", r)
	}

	// Now a run of FAST drops against an exhausted budget. Each is refused, and
	// each must leave the backoff exactly where it found it.
	for i := range 5 {
		at := t0.Add(time.Duration(i+1) * time.Minute)
		g := b.Grant(at, 2*time.Second, false, s)
		if g.OK() {
			t.Fatalf("drop %d opened a window against a spent budget: %+v", i, g)
		}
		if g.Reason != ReasonExhausted {
			t.Errorf("drop %d reason = %q, want %q — a cooldown here would be the "+
				"refusals scoring themselves", i, g.Reason, ReasonExhausted)
		}
		if b.ShortRun() != 0 {
			t.Fatalf("drop %d deepened the backoff to %d; a refused drop got no help "+
				"and must not be counted as one that did", i, b.ShortRun())
		}
	}
}

// The mirror: a GRANTED fast drop does commit the backoff. Without this the fix
// above would read as "the backoff never engages", which is the behaviour
// ADR-0009 replaced arriving by the back door.
func TestAGrantedFastDropStillCommitsTheBackoff(t *testing.T) {
	s := defaults()
	b := New()

	if g := b.Grant(t0, 2*time.Second, false, s); g.Reason != ReasonBackoff {
		t.Fatalf("reason = %q, want %q", g.Reason, ReasonBackoff)
	}
	if b.ShortRun() != 1 {
		t.Errorf("shortRun = %d, want 1", b.ShortRun())
	}
	// And the cooldown it armed refuses the very next drop.
	if g := b.Grant(t0.Add(time.Second), 2*time.Second, false, s); g.Reason != ReasonCooldown {
		t.Errorf("reason = %q, want %q — a granted fast drop must arm a cooldown", g.Reason, ReasonCooldown)
	}
}

// vpn.redialWindow: "0" is the one way to turn trigger 2 off, and the ledger has
// to refuse it outright rather than compute its way there. floorFor(0) is 0, so
// falling through would append a zero-length episode and claim openIdx — a
// refusal that looks like one while quietly holding a slot the next Close would
// settle in place of a real window.
func TestADisabledWindowIsRefusedWithoutTouchingTheLedger(t *testing.T) {
	s := defaults()
	s.Window = 0
	b := New()

	g := b.Grant(t0, 5*time.Minute, true, s)
	if g.OK() {
		t.Fatalf("a disabled window opened: %+v", g)
	}
	if g.Reason != ReasonDisabled {
		t.Errorf("reason = %q, want %q", g.Reason, ReasonDisabled)
	}
	// Nothing was spent, and nothing is open for a later Close to settle.
	if r := b.Remaining(t0, s); r != s.Budget {
		t.Errorf("remaining = %v, want the budget untouched at %v", r, s.Budget)
	}
	b.Close(t0.Add(time.Minute))
	if r := b.Remaining(t0.Add(time.Minute), s); r != s.Budget {
		t.Errorf("remaining = %v after a stray Close, want %v", r, s.Budget)
	}
}

// Granting with a window already open is a caller bug the run loop prevents, but
// the consequence is silent and permanent: expire never ages out an unsettled
// episode, so an orphan is charged its full grant for the life of the process
// and the budget shrinks by that much forever. Grant settles it instead, which
// keeps the guarantee inside this package rather than several hundred lines away
// in the caller.
func TestGrantSettlesAnOrphanedEpisode(t *testing.T) {
	s := defaults()
	b := New()

	b.Grant(t0, 5*time.Minute, true, s) // opened, never closed
	// Three seconds later a second drop arrives with the first still open.
	at := t0.Add(3 * time.Second)
	if g := b.Grant(at, 5*time.Minute, true, s); !g.OK() {
		t.Fatalf("second grant refused: %+v", g)
	}
	// The orphan settled at what it actually cost (3s), not at its 30s grant, so
	// the ledger holds 2m − 3s − 30s rather than 2m − 30s − 30s.
	want := s.Budget - 3*time.Second - s.Window
	if r := b.Remaining(at, s); r != want {
		t.Errorf("remaining = %v, want %v — the orphan was charged its full grant", r, want)
	}
	// And exactly one episode is open: closing once must settle the second, and
	// closing again must be the documented no-op.
	b.Close(at.Add(s.Window))
	if r := b.Remaining(at, s); r != want {
		t.Errorf("remaining = %v after closing the live window, want %v", r, want)
	}
}
