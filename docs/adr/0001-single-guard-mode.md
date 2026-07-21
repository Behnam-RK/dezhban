# ADR-0001: Collapse the two enforcement modes into one guard-only product

**Date**: 2026-07-20
**Status**: accepted, implemented
**Deciders**: Behnam RK

## Context

dezhban shipped two enforcement modes selected by `vpn.enabled`. They were never two
settings of one machine: the VPN guard blocks by **network interface** with rules
standing at all times, while the country-blocklist fallback blocks by **destination IP**
and installs no rules at all while the country looks fine. Different primitives,
different resting states, different run loops (`runVPN` vs `runLegacy`), different
Backend verbs (`Apply` vs `Block`/`Unblock`), different control-socket handlers.

The cost was not only implementation. `docs/modes.md` needed a comparison table, a
decision tree, and 219 lines to explain which mode a user wanted; the GUI foregrounded
a mode axis as its most prominent control; and the setup wizard opened by asking a
question most users could not answer.

Two facts made the split indefensible rather than merely expensive. First, the fallback
is, by its own documentation, "best-effort, not a zero-leak guarantee" and "only
meaningful if the country you block is your **real physical location**" — which is not
the situation dezhban's users are in. Second, guard mode already **contains** the
country check: observing a forbidden exit is exactly what escalates GUARD to FULL BLOCK.
The fallback was not a peer of the guard. It was a strictly weaker product that happened
to ship alongside it.

## Decision

Delete the country-blocklist mode. dezhban is a VPN kill switch: one state machine
(STANDBY → GUARD ⇄ FULL BLOCK, plus the bounded switch window). `vpn.enabled` is
removed, `vpn.*` is hoisted to the top level, and the exit-country check survives as
the GUARD→FULL BLOCK trigger it always was.

## Alternatives considered

### Alternative 1: Auto-select the primitive at runtime by tunnel presence

- **Pros**: no user-visible mode at all; adapts to whatever the host is doing.
- **Cons**: the selection signal is the same signal that indicates failure.
- **Why not**: a VPN drop reads as "no tunnel," which would downgrade an armed guard to
  the weak destination-allowlist mode and **open the network** — precisely the leak the
  product exists to prevent. Keying on *configured intent* rather than live presence
  avoids that, but then it is not really auto-selection, it is the guard with extra
  steps.

### Alternative 2: Superimpose both machines

- **Pros**: most defensive; both signals always evaluated.
- **Cons**: largest ruleset, hardest to preview or reason about.
- **Why not**: almost entirely redundant. The guard already blocks all non-tunnel
  egress, so the destination-allowlist terms would be dead rules in every posture that
  matters.

### Alternative 3: Keep both, flip the default to guard

- **Pros**: smallest change; preserves an escape hatch for non-VPN hosts.
- **Cons**: none of the complexity goes away.
- **Why not**: the simplification would be cosmetic. Every branch, both run loops, and
  the whole doc burden survive; only the default changes.

## Consequences

### Positive

- One state machine, one ruleset family, one setup path, one mental model.
- `runLegacy`, the second control-socket handler, and the `Block`/`Unblock` runner path
  are deleted outright.
- `docs/modes.md` collapses from a mode-selection guide to a single state machine.
- The `print-rules --mode guard` bug disappears: it currently renders a physical
  destination allowlist the runner would never install.
- Forces extraction of the shared policy constructor that `main.go` has carried a TODO
  for — one construction site instead of two that must be kept in sync.

### Negative

- **Hosts not behind a VPN are no longer supported.** This is deliberate; they were
  poorly served anyway.
- Breaking config change. Mitigated by silent migration on load plus a
  `dezhban config migrate` that writes the new shape back on demand.
- The `legacy` mode string was pinned as a stable identifier in `CLAUDE.md`. Retiring it
  is a deliberate thaw and must be recorded there in the same commit.

### Risks

- A user running the fallback intentionally on a non-VPN host loses protection on
  upgrade. Mitigated by the migration logging what it dropped, and by STANDBY
  (see [ADR-0002](0002-standby-no-tunnel-posture.md)) being loudly visible rather than
  silently unprotected.
- Removing `Snapshot.Mode` and `status --json`'s `vpnEnabled` breaks any external
  consumer. Accepted: a field with one possible value is noise, and the surface is
  small and local.
