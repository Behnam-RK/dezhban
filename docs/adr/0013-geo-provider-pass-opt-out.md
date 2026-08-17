# ADR-0013: The geo-provider pass gets an opt-out, not a redesign

**Date**: 2026-08-16
**Status**: accepted
**Deciders**: Behnam RK

## Context

FULL BLOCK carries exactly one destination-scoped hole: the tunnel-**and**-
destination-scoped geo-provider pass from
[ADR-0006](0006-geo-providers-tunnel-scoped.md), which lets the exit-country
lookup keep running while everything else is cut, so a block heals itself the
moment the exit is allowed again. Some operators want the strictest possible
statement — *no* standing destination-scoped rule in FULL BLOCK, however tightly
scoped — and the GUI settings work made that preference expressible for the
first time. The tension: removing the pass does not remove the lookups; the
runner already has a documented degraded path (lift-and-probe) that briefly
lifts the guard to observe the exit whenever no provider address is available.

## Decision

Add `vpn.allowGeoProviders` (bool, default `true`, restart required). When
`false`, the daemon is assembled with no provider resolver, so FULL BLOCK
renders without the ADR-0006 pass and recovery uses the existing lift-and-probe
fallback. ADR-0006's double scoping is untouched when the key is on; no rule
shapes change on either setting.

## Alternatives considered

### Alternative 1: no key — the pass is always on

- **Pros**: one less knob; the pass is the better trade for almost everyone.
- **Cons**: an operator who audits the ruleset finds a standing
  destination-scoped rule they cannot remove without emptying `providers`,
  which disables country checking entirely — a much worse tool for the job.
- **Why not**: the strict preference is legitimate and cheaply expressible on
  an existing, tested degraded path.

### Alternative 2: `false` also stops the lookups (no lift-and-probe)

- **Pros**: matches a "stop talking to geo providers" reading of the key.
- **Cons**: a FULL BLOCK could then never observe its way out — the block
  becomes permanent until an operator intervenes, which is a lockout by design.
- **Why not**: that behavior already exists honestly as emptying `providers`
  (no country checking at all). A key that silently created unrecoverable
  blocks would be the worst failure mode this tool can have.

### Alternative 3: make it live-appliable

- **Pros**: `Saved and applied` instead of a restart prompt.
- **Cons**: the provider resolver is a closure wired into `runner.Options` at
  startup, same as `providers`/`providerQuorum`; a live key the loop never
  re-reads would report "applied" while the old behavior kept enforcing —
  the exact failure `liveKeys` exists to prevent.
- **Why not**: honesty over convenience; restart-required is the truth.

## Consequences

### Positive

- The strictest posture ("no destination-scoped holes in FULL BLOCK") is
  expressible without giving up country checking.
- No new rule shapes; ADR-0006 stands unmodified for the default case.

### Negative

- With the key off, recovery from FULL BLOCK **briefly lifts the guard on every
  probe tick** to observe the exit — a bounded, repeated leak the pass exists
  to avoid — and accelerated recovery probing is disabled. The key trades a
  scoped standing hole for a periodic full lift; it does **not** reduce
  geo-provider traffic.

### Risks

- An operator reads the key as "stop geo-provider traffic". Mitigated by the
  schema help text, the config.md row, and the startup warning, all of which
  state the lift-and-probe cost in plain words.
