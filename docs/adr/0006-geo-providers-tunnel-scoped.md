# ADR-0006: Geo-provider passes are tunnel-scoped, never physical

**Date**: 2026-07-20
**Status**: accepted, implementation pending
**Deciders**: Behnam RK

> **Read this before changing anything about the geo lookup.** It records why the
> obvious implementation silently defeats the exit-country check. That implementation
> has already been proposed once, from a reasonable-looking symptom, and it will look
> reasonable again.

## Context

Users see errors about dezhban being unable to reach the geo-IP providers, most often
while in FULL BLOCK or during a switch window. The natural request follows: *add a
setting that always allows the providers, the way `allowPhysicalDNS` always allows DNS.*

The observation is correct. The diagnosis needs splitting, because it is **two problems
wearing one symptom**:

**(a) FULL BLOCK — a real gap.** The ruleset there is loopback + endpoint passes +
optional DNS + `block drop out all`. Providers are not passed, so lookups genuinely
cannot complete. This is why recovery uses a time-windowed probe that lifts the guard for
a `probeEgressBudget` of roughly eight seconds, runs one lookup, and re-cuts — a
recurring, deliberate leak whose only purpose is to make one measurement possible.

**(b) Switch and reconnect windows — not a gap at all.** During a window the tunnel is
usually down; that is why the window exists. A lookup then has no tunnel exit to measure.
**Failing is correct.** dezhban is reporting an expected condition as an error.

## Decision

Add a standing `oif = <tunnel>, dst = <provider IPs>` pass in both GUARD and FULL BLOCK.
The exit-country lookup keeps traversing the tunnel, so the measurement stays correct,
but no user traffic leaks while observing. **Never** allow provider addresses on the
physical interface.

Separately, classify lookup failures instead of surfacing them all identically.

## Alternatives considered

### Alternative 1: Allow provider IPs on the physical interface, like `allowPhysicalDNS`

- **Pros**: trivial to implement; mirrors an existing setting; makes the error messages
  stop.
- **Cons**: silently destroys the exit-country check.
- **Why not** — this is the important part:

  The measurement is **path-dependent**. When the tunnel is down, the OS falls back to
  the physical route. A provider lookup allowed on the physical link would then measure
  **your real ISP's country**, which is almost always an allowed one. The decider reads
  "exit is fine," and FULL BLOCK never fires. Half of what the guard does would be
  disabled, and nothing would appear broken.

  It is worse than merely useless, because the reading feeds `finishCloseProbe`, which
  decides whether to **close a switch window early and learn the new endpoint**. A
  physical-path lookup would succeed, report an allowed country, and cause the window to
  close early on a bogus "good exit" while pinning a wrong endpoint. A harmless error
  message becomes silent misbehaviour inside the machinery that protects the guard.

  The tunnel-cutting probe exists *because* of this path-dependence. It is not an
  oversight to be optimised away.

### Alternative 2: Keep the probe, shorten `probeEgressBudget`

- **Pros**: no new rule shape; smaller leak.
- **Cons**: shrinks the leak without removing it, and a too-short budget starts failing
  lookups on slow links.
- **Why not**: trades one failure mode for another. The tunnel-scoped pass removes the
  leak entirely rather than tuning it.

### Alternative 3: Make it a user-facing "allow providers" toggle

- **Pros**: user choice.
- **Cons**: the only variant worth having is unconditionally safe, and the unsafe variant
  must not exist.
- **Why not**: there is nothing to decide. The tunnel-scoped pass has no downside to opt
  out of, and the physical variant must never be offered.

## Consequences

### Positive

- **Deletes the `probeEgressBudget` leak entirely** — roughly eight seconds of full
  tunnel egress opened on every probe tick, gone. This is a real recurring leak removed,
  and a better outcome than the feature originally requested.
- Enables continuous observation with no posture toggling: no lift-and-re-cut cycle, so
  no window where the guard is briefly down.
- The exit-country measurement stays correct by construction rather than by timing.

### Negative

- Provider addresses are CDN-rotated, so they need resolve-and-refresh plus tunnel-scoped
  DNS to re-resolve them. The retired legacy allowlist already had this machinery —
  salvage it rather than rewriting.
- A new matched posture must be implemented across pf, nft, and WFP.

### Risks

- If the exit genuinely censors the providers (an Iranian exit, for example), lookups
  still fail. **No regression** — that is true today — and the guard-mode fail-closed
  scoping already handles it correctly by *holding* posture rather than escalating.
- **Someone re-proposes the physical allow.** Mitigated by this record, by a `CLAUDE.md`
  invariant stating that geo-provider passes are tunnel-scoped only, and by an acceptance
  check that drops the tunnel and asserts the lookup **fails** rather than reading the
  ISP's country. That check must never be "fixed" to pass.

## Error classification

Three distinct causes currently surface as one error. This is part of the same work, not
a follow-up — the errors that prompted the request were mostly the first row.

| Cause | Correct presentation |
|---|---|
| No tunnel up (window, standby, drop) | **Not an error.** "Exit country unknown — no tunnel." |
| Tunnel up, providers unreachable | Real warning; the exit may be censoring them. Posture **holds**. |
| Tunnel up, providers reachable, response malformed | Real error worth showing. |
