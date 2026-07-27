// Package redial bounds the automatic redial window: how long it may be, how
// often, and when it must be refused outright. It is the decision half of
// trigger 2 (see docs/adr/0009-redial-budget.md); the run loop keeps the
// enforcement half.
//
// The shape it replaces was one fixed window per drop, suppressed entirely when
// the tunnel had been up for less than vpn.advanced.redialMinUptime. That is
// unbounded across drops — every drop gets a fresh window, forever — and zero
// within a flap, which is the connection most in need of help. A rolling budget
// of total open time inverts both: bounded across drops, non-zero on a flap.
//
// Pure and clock-injected, like internal/decision: every method takes `now`, and
// nothing here reads a clock, a file, or the network. That is what makes a
// pathological flap testable as a sequence of instants rather than as a wait.
package redial

import "time"

// Reason names why a grant came out the size it did. Exported because the run
// loop logs it and the renderer turns a refusal into a sentence — a guard that
// declines to help must be able to say which of these it was.
type Reason string

const (
	// ReasonFull is the ordinary case: a drop after a healthy uptime.
	ReasonFull Reason = "full"
	// ReasonBackoff is a shortened window after consecutive fast drops.
	ReasonBackoff Reason = "backoff"
	// ReasonTruncated is a window cut down to what the budget still holds.
	ReasonTruncated Reason = "truncated"
	// ReasonCooldown refuses because the backoff cooldown has not elapsed.
	ReasonCooldown Reason = "cooldown"
	// ReasonExhausted refuses because the rolling budget is spent.
	ReasonExhausted Reason = "exhausted"
	// ReasonDisabled refuses because the automatic window is off entirely
	// (vpn.redialWindow: "0"). The run loop gates on that before ever calling
	// Grant, so this is a direct caller's answer rather than one a user sees —
	// it exists so the disabled case cannot fall through to opening a
	// zero-length episode, which would look like a refusal while spending a
	// ledger slot.
	ReasonDisabled Reason = "disabled"
)

// MinGrant is the shortest window worth opening. Below this a window is all cost
// and no benefit: it relaxes the guard — the real IP is exposed the moment it
// opens — without leaving a VPN client enough time to complete a handshake. When
// the budget can only afford a sliver, refusing is strictly better than spending
// it, so the remainder stays available for a drop that can use it.
//
// It is a ceiling on the floor, never a floor on the window: a deliberately
// short vpn.redialWindow is honoured as-is (see floorFor).
const MinGrant = 5 * time.Second

// Settings are the live config values a grant depends on. Passed per call rather
// than stored because all four are live-appliable — a Budget that captured them
// at construction would keep enforcing the old numbers after a reload, which is
// the same class of bug as reporting a setting applied while the old one runs.
type Settings struct {
	// Window is vpn.redialWindow: the full grant, before backoff or truncation.
	Window time.Duration
	// Budget is vpn.advanced.redialBudget: total window-open time allowed per
	// Interval. Zero or negative disables the automatic window entirely.
	Budget time.Duration
	// Interval is vpn.advanced.redialBudgetWindow: the rolling period Budget
	// applies to.
	Interval time.Duration
	// MinUptime is vpn.advanced.redialMinUptime: an uptime below this, with no
	// confirmed exit, seeds the backoff. Zero disables backoff, so every
	// qualifying drop gets a full window until the budget runs out.
	MinUptime time.Duration
}

// A Grant is the answer to "may this drop open a window, and for how long".
type Grant struct {
	// Duration is how long to open for. Zero means refused.
	Duration time.Duration
	Reason   Reason
	// NextEligible is when a window could next open. Meaningful only on a
	// refusal, and it is what both surfaces show so "the guard is holding" comes
	// with "until when" rather than leaving the user to wonder whether help is
	// coming at all.
	NextEligible time.Time
}

// OK reports whether the grant opens a window.
func (g Grant) OK() bool { return g.Duration > 0 }

// episode is one window this budget paid for. granted is debited at open;
// actual replaces it once the window closes, so a redial that succeeded in three
// seconds costs three seconds rather than the whole grant. Charging the offer
// instead of the exposure would punish exactly the outcome the window exists to
// produce.
type episode struct {
	start   time.Time
	granted time.Duration
	actual  time.Duration
	settled bool
}

func (e episode) cost() time.Duration {
	if e.settled {
		return e.actual
	}
	return e.granted
}

// Budget is the rolling ledger plus the backoff state. Not safe for concurrent
// use, and it does not need to be: it lives in the run loop's single goroutine,
// the same one that owns every Backend.Apply.
//
// Deliberately in-memory. Persisting it would mean a daemon restart inherits a
// restriction earned by a flap that is over, and an unexpectedly refused redial
// is a worse failure than one extra window after a restart — the same reasoning
// that keeps hold-the-line un-persisted.
type Budget struct {
	episodes  []episode
	shortRun  int // consecutive drops that came too fast
	coolUntil time.Time
	openIdx   int // index of the open episode, or -1
}

// New returns an empty budget: nothing spent, no backoff, no window open.
func New() *Budget { return &Budget{openIdx: -1} }

// Grant decides whether this drop opens a window. uptime is how long the tunnel
// was up before it dropped (zero when unknown), and goodExit reports whether a
// confirmed non-blocked exit was seen during that uptime — a tunnel that proved
// itself is not flapping, however briefly it lasted.
//
// It does not re-check the run loop's own preconditions (standby, FULL BLOCK, a
// window already open, hold the line). Those are the loop's to enforce and are
// deliberately checked before this is ever called, so that a suppressed drop
// spends nothing.
func (b *Budget) Grant(now time.Time, uptime time.Duration, goodExit bool, s Settings) Grant {
	if s.Window <= 0 {
		// The automatic window is off (vpn.redialWindow: "0"). Refuse before
		// touching the ledger: floorFor(0) is 0, so falling through would append
		// a zero-length episode and claim openIdx — a slot the next Close would
		// settle in place of a real window.
		return Grant{Reason: ReasonDisabled}
	}
	// Defensive, and deliberately not a precondition the caller is trusted with.
	// The run loop checks windowActive before calling, but an episode left open
	// here is never aged out — expire keeps unsettled ones on purpose — so an
	// orphan would be charged its full grant for the rest of the process's life.
	// Settling it costs a line and keeps the invariant inside the package.
	if b.openIdx >= 0 {
		b.Close(now)
	}
	b.expire(now, s.Interval)

	// recovered is the evidence that the flap the cooldown is rationing is OVER:
	// a confirmed non-blocked exit through the tunnel, or an uptime that cleared
	// the health threshold. It is the same evidence that disqualifies `fast`
	// below, and it must be read here too — a cooldown that outlives the flap
	// refuses the drop of a tunnel that just demonstrably worked, and because a
	// refusal is only re-decided on the next tunnel-down edge, that refusal is
	// terminal until the operator opens a window by hand. Rationing a link that
	// recovered is the manual interaction ADR-0009 exists to remove.
	//
	// A zero uptime means "up since before we were watching" — unknowable, so it
	// is not evidence of anything and only goodExit can clear the cooldown then.
	recovered := goodExit || (s.MinUptime > 0 && uptime >= s.MinUptime)

	// A drop inside the cooldown does NOT deepen the backoff. The cooldown is
	// already the response to the flap, and escalating on drops that were given
	// no help would compound a punishment for something the guard declined to
	// assist with — the backoff exists to ration windows, not to score drops.
	if now.Before(b.coolUntil) && !recovered {
		return Grant{Reason: ReasonCooldown, NextEligible: b.coolUntil}
	}

	// Compute the backoff without committing it. Same principle as the cooldown
	// return above, applied to the refusal below: a drop the budget turns away
	// got no help either, so it must not deepen the backoff or push the cooldown
	// out. Otherwise refusals would compound into a wait longer than the ledger
	// alone ever asked for, and the guard would keep holding after the budget
	// had already rolled over.
	reason := ReasonFull
	want := s.Window
	shortRun := 0
	fast := s.MinUptime > 0 && !goodExit && uptime > 0 && uptime < s.MinUptime
	if fast {
		shortRun = b.shortRun + 1
		reason = ReasonBackoff
		// Halve per consecutive fast drop, and cool for one full window per step
		// so a pathological flap decays toward "cut and holding" rather than
		// chaining. Both are derived from Window so there is one number to tune.
		want = s.Window >> min(shortRun, backoffSteps)
	}

	floor := floorFor(s.Window)
	if want < floor {
		want = floor
	}

	remaining := s.Budget - b.spent()
	if remaining < floor {
		return Grant{Reason: ReasonExhausted, NextEligible: b.nextEligible(now, s, floor)}
	}
	if want > remaining {
		want, reason = remaining, ReasonTruncated
	}

	// Past every refusal, so a window is definitely opening: commit the backoff.
	b.shortRun = shortRun
	if fast {
		if cool := s.Window * time.Duration(shortRun); cool > 0 {
			// Never cool longer than a full refill — past that the budget has
			// recovered anyway and the wait buys nothing.
			b.coolUntil = now.Add(min(cool, s.Interval))
		}
	} else {
		// A drop that was not fast resets the backoff completely, cooldown
		// included. Clearing shortRun alone would leave a cooldown armed by a
		// flap this very drop disproved, and the next fast drop would then be
		// refused against a deadline nothing still justifies. Only reachable
		// with time left on the clock via `recovered` above — past the deadline
		// this is already a no-op.
		b.coolUntil = time.Time{}
	}
	b.episodes = append(b.episodes, episode{start: now, granted: want})
	b.openIdx = len(b.episodes) - 1
	return Grant{Duration: want, Reason: reason}
}

// Close settles the open episode at what it actually cost. Call it from every
// path that closes an automatic window — expiry, cancel, and the early close on
// a confirmed good exit — so the ledger measures exposure taken rather than
// exposure offered. Calling it with nothing open is a no-op, which keeps the
// call sites free of "was this window an automatic one" bookkeeping.
func (b *Budget) Close(now time.Time) {
	if b.openIdx < 0 || b.openIdx >= len(b.episodes) {
		return
	}
	e := &b.episodes[b.openIdx]
	// Clamp rather than trust the clock: a window taken over by a manual switch
	// keeps running under the operator's own cap, and the budget must not be
	// charged for time it did not grant.
	e.actual = min(max(0, now.Sub(e.start)), e.granted)
	e.settled = true
	b.openIdx = -1
}

// Remaining is how much of the budget is unspent as of now. An open window
// counts at its full grant: it is committed, and reporting it as free would let
// a surface promise room that is already claimed.
//
// It MUTATES: expiring the ledger is what makes the answer current, so this
// retires episodes that have rolled out of the interval. Harmless where it is
// called — the run loop's single goroutine, the same one that owns every
// Backend.Apply — but it is not the read-only accessor its name suggests, so do
// not reach for it from anywhere else without moving the expiry out first.
func (b *Budget) Remaining(now time.Time, s Settings) time.Duration {
	b.expire(now, s.Interval)
	return max(0, s.Budget-b.spent())
}

// ShortRun is the number of consecutive fast drops behind the current backoff.
// Zero when the last drop followed a healthy uptime.
func (b *Budget) ShortRun() int { return b.shortRun }

// backoffSteps caps the halving. Past it the grant is the floor anyway, and an
// unbounded shift on a long-running flap would reach zero and read as "refused"
// for a reason the budget never decided.
const backoffSteps = 4

// floorFor is the smallest window worth opening for a given configured length.
// MinGrant is the general answer, but it must never exceed Window itself — a
// deliberately short vpn.redialWindow is a decision, and silently opening for
// longer than asked would be the mirror of silently discarding the setting.
func floorFor(window time.Duration) time.Duration {
	if window < MinGrant {
		return window
	}
	return MinGrant
}

// spent totals the ledger. Callers expire first.
func (b *Budget) spent() time.Duration {
	var total time.Duration
	for _, e := range b.episodes {
		total += e.cost()
	}
	return total
}

// expire drops episodes that started more than one Interval ago. Inclusion is by
// START time, so an episode straddling the boundary leaves the ledger whole
// rather than being pro-rated — simpler, and it errs toward forgetting sooner,
// which is the direction that keeps the budget from over-refusing.
func (b *Budget) expire(now time.Time, interval time.Duration) {
	if interval <= 0 {
		return
	}
	cutoff := now.Add(-interval)
	keep := b.episodes[:0]
	openIdx := -1
	for _, e := range b.episodes {
		// An unsettled episode is a window that is open right now, so it is
		// never aged out however long it has been running — dropping it would
		// lose the debit and leave Close with nothing to settle, quietly making
		// the longest windows the cheapest ones.
		if e.settled && e.start.Before(cutoff) {
			continue
		}
		if !e.settled {
			openIdx = len(keep)
		}
		keep = append(keep, e)
	}
	b.episodes, b.openIdx = keep, openIdx
}

// nextEligible is the earliest time enough budget has rolled off to afford a
// window of at least `floor`, never earlier than the backoff cooldown. Episodes
// are walked oldest-first — each frees its cost when it falls out of the
// interval — so the answer is a real instant rather than "try again later".
func (b *Budget) nextEligible(now time.Time, s Settings, floor time.Duration) time.Time {
	best := b.coolUntil
	if best.Before(now) {
		best = now
	}
	if s.Interval <= 0 {
		return best
	}
	freed := s.Budget - b.spent()
	for _, e := range b.episodes {
		if freed >= floor {
			break
		}
		freed += e.cost()
		if t := e.start.Add(s.Interval); t.After(best) {
			best = t
		}
	}
	return best
}
