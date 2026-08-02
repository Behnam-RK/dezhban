# ADR-0010: Zombie-tunnel detection is unconditional; acting on it is opt-in

**Date**: 2026-08-02
**Status**: accepted, implemented
**Deciders**: Behnam RK

## Context

`isTunnelIface` (`internal/netdetect/netdetect.go`) asks the OS one question:
is this interface up, named like a tunnel, and carrying a global-unicast
address? None of that says packets are flowing. A tunnel can hang — the
interface object stays exactly as it looked when healthy, and no bytes make it
through — and dezhban has no event for that. `internal/netdetect/watch.go`'s
own comment names the shape of the problem for adapter-level watchers in
general: some failures never produce an interface event at all.

The consequence is not a leak. `decision.Evaluate` short-circuits on a failed
exit-country reading without touching the hysteresis streak — an unknown
country **HOLDS** the current posture, it never escalates — so a hung tunnel
that keeps failing its exit-country lookup keeps the guard exactly where it
was: traffic cut, physical egress blocked, endpoints open for redial. That part
of the design is correct and this ADR does not touch it.

What is missing is everything downstream of "correctly cut". Nothing tells the
operator the tunnel looks hung rather than merely dropped. Nothing tries to
recover automatically the way an ordinary tunnel-down edge does — trigger 2
(`vpn.redialWindow`) never fires, because the watcher never reports a down
edge for an interface that still looks up. A host can sit correctly blocked,
silently, for as long as the VPN client takes to notice its own tunnel died —
which, unlike a socket close, an OS interface object may never signal.

This has a real failure mode distinct from "hung": an exit that **censors the
geo providers** produces the identical symptom — the interface reports up, the
lookup keeps failing — on a tunnel that is working perfectly. `state.Snapshot`'s
`LookupErr` doc already names this by example ("an Iranian exit blocking them
looks exactly like this"). Any mechanism that reacts to a failing-lookup streak
by relaxing the guard has to reckon with the fact that it cannot tell a hung
tunnel from a working one behind a hostile exit.

## Decision

Split the feature into two halves with different defaults.

**Diagnosis is unconditional, on by default, and never changes.** The run loop
counts consecutive failed exit-country lookups while the tunnel interface
reports up, not standby, not in a window, and not already in FULL BLOCK. The
streak length reuses the Decider's own configured hysteresis (`o.Decider.Pending()`'s
`need`) rather than a new tunable, so it tracks the same "how many agreeing
readings before we act" tuning the rest of the state machine already uses. Once
the streak reaches that count, dezhban:

- publishes `state.Snapshot.Zombie` (`{Since, Checks}`) — an additive field,
  present only while the streak stands, cleared the instant a lookup succeeds,
  the tunnel reports down, or anything suspends the geo state machine (standby,
  a window, a manual block);
- logs one `Warn` line at the moment the streak crosses the threshold, not on
  every tick after (matching the existing "log the edge, not the level" style
  used for an ordinary tunnel-down transition);
- surfaces in `dezhban doctor` (the `liveness` check) and in the rendered
  posture sentence (`internal/render`'s `zombieNote`), alongside — never in
  place of — the existing `LookupErr` note.

This half carries no censoring-exit hazard: it changes nothing the guard
enforces. The guard is already holding; this only says so out loud.

**Acting on it is opt-in and off by default.** `vpn.advanced.livenessRedial`
(bool, default `false`) lets a confirmed streak call the **existing**
`maybeAutoWindow`, the same closure an ordinary tunnel-down edge calls. This is
trigger 2 (the automatic redial window) widening its own definition of "down"
to include "reports up but is not passing traffic" — not a fourth trigger.
Every rail that already governs trigger 2 applies completely unchanged:
`vpn.advanced.redialBudget` and `redialBudgetWindow`, the `redialMinUptime`
backoff, `dezhban hold`, `vpn.advanced.redialWindowMax`, and the one-window-
per-drop rule. `vpn.redialWindow: "0"` still removes trigger 2 outright,
`livenessRedial` or not — the streak calls the same gated closure, and
`autoWindowPossible()` still checks `RedialWindow > 0` first.

Deliberately **not** implemented: mutating the runner's `tunnelUp` variable to
pretend the tunnel went down. `internal/netdetect/watch.go`'s `Watcher` keeps
its own `emitted` state independent of the runner, so if the runner faked a
down edge the real interface coming back up later would never look like a
change to the watcher — no up edge would ever be emitted, and the daemon would
wedge. The zombie streak is tracked entirely in the run loop, `tunnelUp` is
never touched, and the streak clears itself on the loop's own next successful
lookup.

## Alternatives considered

### Alternative 1: A dedicated liveness probe instead of reusing the geo lookup

- **Pros**: distinguishes "the geo providers are unreachable" from "the tunnel
  is dead" — two different root causes currently produce one symptom.
- **Cons**: a new probe target through the tunnel is a new destination-scoped
  firewall pass, alongside the geo-provider pass ADR-0006 already scoped
  narrowly on purpose. A second such hole needs the same tunnel+destination
  double-scoping and the same scrutiny, for a diagnostic feature.
- **Why not**: the existing geo lookup already proves liveness on success —
  `runGuard`'s own startup-observation comment states this outright ("a
  confirmed allowed exit proves the tunnel is carrying traffic"). Reusing it
  costs no new I/O, no new pass, and no new attack surface. The
  cannot-distinguish-censorship-from-death limitation is real, but it is the
  same limitation `LookupErr` already lives with; a second signal would not
  remove it unless the new probe target were *also* uncensorable, which is not
  a property a probe target can promise.

### Alternative 2: Escalate to FULL BLOCK on a confirmed streak

- **Pros**: makes the "something is wrong" signal impossible to miss.
- **Cons**: FULL BLOCK is reserved for a *confirmed blocked country* — the one
  thing this tool exists to prevent physically. A hung tunnel is not that; the
  guard is already the correct response. Escalating would also cut the
  tunnel's own egress on a genuinely censoring exit, livelocking the very
  recovery a redial window is meant to offer — precisely the failure mode
  `decision.Evaluate`'s "undeterminable HOLDS" rule already exists to prevent
  for an ordinary unknown reading.
- **Why not**: it repeats a mistake this codebase has already reasoned its way
  out of once, for the same failure shape.

### Alternative 3: `livenessRedial` on by default

- **Pros**: better automatic recovery out of the box, matching CVG's own
  watchdog (which has no equivalent opt-out).
- **Cons**: a censoring exit is not a hypothetical for this project's stated
  threat model — the docs name Iran by example more than once. Defaulting to
  "trust a failing lookup enough to relax the guard" hands a censoring exit a
  way to trigger a relaxation window on a tunnel that was never actually down.
- **Why not**: the cost of getting this wrong (a brief real-IP exposure handed
  to an adversary who controls the exit) is categorically worse than the cost
  of getting it right by hand (`dezhban switch`). Default off, opt-in for
  operators who have judged their own exit trustworthy enough for the
  trade-off.

## Consequences

### Positive

- A hung tunnel finally explains itself — in the log, in `doctor`, and in the
  rendered posture — instead of sitting correctly cut with no signal to anyone.
- The diagnosis costs nothing: no new I/O, no new firewall pass, no new
  destination-scoped hole.
- Recovery is available for operators who want it, through the exact same
  budget/backoff/hold rails as an ordinary drop, with no new machinery to
  audit.

### Negative

- One more advanced tunable (`vpn.advanced.livenessRedial`), declared in
  `internal/config/schema.go` like every other, so every surface still derives
  its hint and default from the same table.
- `internal/runner/verify_test.go`/`liveness_test.go`-style coverage aside,
  this is a heuristic: a streak length tuned to the Decider's hysteresis can
  still misfire on a link that is merely slow, not dead. Mitigated by reusing
  the same hysteresis the rest of the state machine already trusts, rather than
  inventing a separate, unvalidated threshold.

### Risks

- **A user enables `livenessRedial` behind a censoring exit.** This is the
  hazard the whole split exists around. Mitigated by defaulting off, and by
  every relaxation rail (budget, backoff, `redialWindowMax`, hold) still
  applying — a censoring exit can trigger at most a budget's worth of exposure
  before the ledger holds, same as any other flapping link.
- **The diagnosis itself is noisy on a merely slow link.** Mitigated by gating
  the report on the Decider's own hysteresis count rather than a single failed
  reading, and by clearing it the moment a lookup succeeds.

## What this does not change

- **The switch window still has exactly THREE sanctioned triggers**
  (`docs/contribute/architecture.md`). A confirmed liveness streak reaches
  `maybeAutoWindow` — trigger 2's own entry point, alongside the ordinary
  tunnel-down edge and the bound-lifted re-decision (`retryAutoWindow`) — so
  this widens what trigger 2 recognises as "down"; it adds no fourth trigger.
- **`vpn.redialWindow: "0"` still removes trigger 2 entirely**, regardless of
  `livenessRedial`.
- **The undeterminable-country-HOLDS rule is untouched.** This ADR adds a
  second thing that HOLDS (a hung tunnel) rather than changing what holding
  means.
- **`internal/netdetect/watch.go` is untouched.** The watcher's own up/down
  edge detection, debounce, and `emitted` state carry no knowledge of the
  zombie streak.
