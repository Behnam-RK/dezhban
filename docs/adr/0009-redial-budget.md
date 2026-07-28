# ADR-0009: The automatic redial window spends from a bounded budget

**Date**: 2026-07-27
**Status**: accepted, implemented
**Deciders**: Behnam RK

## Context

[ADR-0008](0008-arm-at-boot.md) established the automatic redial window as the
second of three sanctioned relaxation triggers: a tunnel-down edge from a
healthy GUARD opens a bounded window (`vpn.redialWindow`, default `30s`) so the
VPN client can redial to a server dezhban has never seen. It is gated against
flapping by `vpn.advanced.redialMinUptime` (default `15s`): a tunnel that was up
for less than that, with no confirmed good exit, gets **no window at all**.

Both halves of that shape are wrong in the same way, and they are wrong in
opposite directions.

**Across drops, exposure is unbounded.** Every drop gets a fresh 30s. There is
no ceiling on how many drops, so a link dropping once a minute produces 30s of
relaxed guard every minute, indefinitely. Nothing in the design says how much
total exposure a redial policy may cost, so nothing enforces one.

**Within a flap, exposure is zero, on exactly the connection that needs help.**
The anti-flap gate fires precisely when a VPN is struggling — which is when a
redial window is most useful — and pushes the user onto the manual path. The
product principle this tool is built on is that a sustained real-IP leak must be
prevented **with the minimum possible interaction**; being told to run
`dezhban switch` by hand because the connection is poor is a product failure,
not a safety feature.

The gate's intent is nevertheless correct: chaining full-length windows on a
flapping tunnel would convert a bounded leak into standing exposure, which is
the one outcome that must never happen. What is wrong is the *shape* — one
fixed-length window per drop, all or nothing — not the instinct behind it.

## Decision

The automatic redial window draws from a **rolling budget of total open time**
(`vpn.advanced.redialBudget`, default `2m`, per
`vpn.advanced.redialBudgetWindow`, default `15m`) rather than being granted
whole on every qualifying drop. `vpn.advanced.redialMinUptime` stops suppressing
the window and instead **seeds a backoff**: a drop after a short uptime with no
confirmed exit still gets a window, shortened and cooled down for each
consecutive short drop, until the budget is spent — at which point the guard
holds and traffic stays cut.

Budget is debited when a window opens and **credited back when it closes early**,
so the ledger measures exposure actually taken, not exposure offered. A VPN that
reconnects in three seconds costs three seconds.

The backoff's cooldown is **cleared by evidence that the flap is over** — a
confirmed non-blocked exit through the tunnel, or an uptime past
`redialMinUptime` — and not merely by waiting it out. The same evidence already
decides whether a drop counts as fast, and it has to be read in both places: a
cooldown that outlives the flap refuses the drop of a tunnel that just
demonstrably worked, and because a refusal is only re-decided on the next
tunnel-down edge, that refusal stands until an operator opens a window by hand.
Rationing a link that recovered is the interaction this ADR exists to remove. The
rolling budget, not the cooldown, is the bound that must not be negotiable.

A refusal publishes **when a window could next open, answering for every bound at
once** — the later of the cooldown deadline and the instant enough budget has
rolled off. Reporting only whichever bound refused first would hand the user a
deadline that moves: told 3:00PM, they wait, and the drop at 3:00PM is refused
with 3:15PM instead. For the same reason an episode is retired *on* the boundary
of the rolling period rather than strictly past it, so the published instant is
one the ledger will actually honour.

A refused drop is **re-decided when that bound lifts**, from a timer in the run
loop, so the instant is one the guard acts on rather than one it merely reports.
Both surfaces therefore word it as an **attempt, not an outcome** ("dezhban tries
again at 3:15PM — no window opens before then"): the re-decision consults the
budget afresh and re-checks every precondition, so it may refuse again. Naming
the attempt is the strongest true claim available; "the guard relaxes at 3:15PM"
would not be.

Without the re-decision the instant was inert. The decision was retaken only on
the next tunnel-down edge, so a tunnel that could not come back on its own — a
rotated server address the endpoint pass does not cover, which is precisely the
case the automatic window exists for — produced no further edge, the refusal
stood indefinitely, and the budget refilling changed nothing. The user waited out
a time that was never going to be honoured and then had to run `dezhban switch`.
That is the manual interaction this ADR exists to remove, reintroduced by the
bound meant to be safe.

The copy still promises no more than the guard can deliver: when dezhban itself
tries again, that nothing opens before then, that a held guard keeps passing
known server addresses so the VPN's own redial is unaffected meanwhile, and that
a manual window is always available. Once the named instant has passed — the
retry ran and refused again without a new time, or could not be scheduled — the
sentence drops the instant rather than reprinting a moment that came and went.

This is still trigger 2. There is no fourth trigger.

The re-decision is **not** a new trigger, and the distinction is load-bearing. A
trigger is a *cause* for relaxing the guard; this admits none. The drop already
qualified at its own tunnel-down edge — healthy GUARD, a tunnel observed up, not
standby, not FULL BLOCK, no window open, hold not armed — and the only thing that
said no was the budget or the cooldown. Re-asking when that bound expires
completes a decision the drop had already earned. Concretely, every rail is
unchanged: the same `redial.Grant`, the same ledger debit and credit-on-close,
the same `TriggerAuto` episode capped by `redialWindowMax`.

The rails that keep it from becoming one:

- **At most one automatic window per drop, still.** A retry runs only while a
  refusal stands; a grant clears the refusal and disarms the timer, and nothing
  re-arms it. An expired window never re-opens.
- **The retry re-asks the same question.** The drop's uptime and confirmed-exit
  status are captured at its edge, because `now - tunnelUpSince` keeps growing
  while the tunnel is down — a retry deriving uptime fresh would report a fast
  drop as healthy and cancel the backoff at the moment it is working.
- **Every precondition is re-checked**, from the same predicate the drop edge
  uses. A retry into standby, FULL BLOCK, or an already-open window does nothing.
- **Hold the line still wins.** An operator who arms it during a cut is saying
  "keep me cut"; the retry honours that and does not spend the flag, which names
  the next drop. Hold may only ever subtract a relaxation, so it must be able to
  subtract this one.

  **And cancelling it gives the re-decision back**, which is the same rule read
  in the other direction: the subtraction is being taken back, so what it took
  must return. Without that, arming hold mid-cut and then changing your mind
  stranded the drop for good — the retry fires once, the hold consumes it, the
  timer disarms itself, and nothing re-arms it, so nothing re-decides until the
  next tunnel-down edge, which cannot arrive while the tunnel is already down.
  That is exactly the wall this ADR exists to remove, reachable by using the
  feature that is meant to be the *cautious* choice, and with both surfaces
  claiming throughout that dezhban re-checks on its own. Cancel therefore
  re-asks — through the same `retryAutoWindow`, so every rail above still
  applies — and only when nothing is armed, since a hold cancelled *before* the
  deadline leaves the original timer running and correct.
- **It is armed only for an instant in the future**, so a bound that has already
  lifted schedules nothing rather than spinning.

## Alternatives considered

### Alternative 1: Remove the anti-flap gate

- **Pros**: trivial; the flapping VPN gets its window immediately.
- **Cons**: restores exactly the failure the gate was built to prevent. A tunnel
  dropping every 20 seconds would chain 30s windows back to back, and a guard
  that is relaxed more often than it is armed is not a kill switch.
- **Why not**: it removes the bound without replacing it. The gate is the only
  thing standing between a lossy link and standing exposure today, and deleting
  a safety rail because its shape is wrong is not the same as fixing its shape.

### Alternative 2: Lengthen the single window

- **Pros**: no new machinery; a longer window survives more redial attempts.
- **Cons**: makes the worst case strictly worse. The problem with a flapping
  link is not that 30s is too short for one attempt, it is that the *attempts*
  are many; a longer window means each of the many is a longer leak. It also
  does nothing for the gate, which suppresses the window regardless of length.
- **Why not**: it optimises the wrong variable. Total exposure per interval is
  what matters, and lengthening the window raises it.

### Alternative 3: Cap the number of windows per interval instead of their total time

- **Pros**: simpler to reason about — "at most four windows per 15 minutes".
- **Cons**: a count is a poor proxy for exposure. Four windows that each closed
  in two seconds on a successful redial cost eight seconds and would exhaust the
  same allowance as four that ran the full 30s. It punishes the successful case,
  which is the one the feature exists to serve.
- **Why not**: the invariant worth holding is *time relaxed*, so time is what
  the ledger should count.

### Alternative 4: Share one budget with the manual switch window and pause

- **Pros**: one number to configure and to explain.
- **Cons**: identical to the shared-cap mistake CLAUDE.md already names for
  `switchWindowMax` / `redialWindowMax` / `pauseMax` — a shared allowance
  silently truncates whichever trigger has the larger intended budget, and an
  automatic mechanism spending an operator's deliberate allowance (or vice
  versa) is a surprise in a security tool.
- **Why not**: the three triggers keep separate caps on purpose. A budget is a
  cap; the same reasoning applies unchanged.

## Consequences

### Positive

- Total automatic exposure per interval is bounded for the first time. Today's
  behaviour has no such bound at all.
- A flapping VPN gets help instead of nothing, and the interaction that the
  gate used to force disappears in the common case.
- The successful case is nearly free: an early close credits the unspent
  remainder, so a healthy link that drops occasionally and redials fast will
  never approach the budget. The budget only bites a genuinely bad connection.
- Backoff means a pathological flap degrades gradually to "cut and holding"
  rather than either chaining windows or refusing from the first drop.

### Negative

- Two more advanced tunables. They are declared like every other
  (`internal/config/schema.go`), so every surface derives its hint, bound, and
  Off-availability from the same table, but the settings surface is two rows
  longer.
- A drop can now be refused for a reason the previous design had no vocabulary
  for — "the budget is spent" rather than "the tunnel was flapping". Both
  surfaces had to learn to say it (see the `redial` object in `status --json`),
  because a guard that silently declines to help is the failure mode this
  project treats as worst.

### Risks

- **A budget set too low reintroduces the old complaint**, since an exhausted
  budget behaves exactly like the old gate. Mitigated by the default being
  four full windows' worth (`2m` against a `30s` window) and by credit-on-close
  making successful redials almost free, so reaching the limit means the link
  really is failing.
- **A budget set too high weakens the bound.** It cannot weaken it past what
  ships today, which is no bound whatsoever, and `redialWindowMax` still caps
  any single window independently.
- **The ledger is in-memory and does not survive a restart.** A daemon restarted
  mid-flap starts with a full budget. This is deliberate and matches
  hold-the-line's reasoning: persisting a *restriction* across restarts means a
  later, unrelated drop inherits it, and the failure caused by an unexpectedly
  refused redial is worse than the failure caused by one extra window after a
  restart. Restarts are rare; flaps are not.

## What this does not change

Stated explicitly because each is an invariant something else depends on:

- **`vpn.redialWindow: "0"` still removes trigger 2 entirely**, and stays the
  *only* way to. The budget is consulted only after that gate, and neither new
  key takes the `Disabled` sentinel: a `0` on either is coerced back to the
  default like any ordinary duration. That is deliberate. On every other key a
  persisted `"0"` means "off", but these two are limits, so "off" would have to
  mean *no limit* — the opposite direction — and a security surface offering an
  **Off** switch that removes a bound rather than a feature is a misreading
  waiting to happen. Anyone who wants today's unbounded behaviour sets a large
  budget explicitly, which says what it does.
- **Hold the line still suppresses, and spends nothing.** It is checked before
  the budget, removes a relaxation rather than granting one, and must never
  consume an allowance the next accidental drop is entitled to.
- **`vpn.advanced.redialWindowMax` still caps any single window**, independently
  of the budget, exactly as `Options.RedialWindowMax` does today.
- **The preconditions are untouched**: never from standby, never from FULL
  BLOCK, never while a window is open, never for a tunnel not observed up.
- **The hold-on-unknown rule and the tunnel+destination-scoped geo pass are
  untouched.** This ADR changes when and for how long trigger 2 opens; it
  changes nothing about what a window does once open.
