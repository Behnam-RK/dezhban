# ADR-0002: Standby is the resting posture when no tunnel exists

**Date**: 2026-07-20
**Status**: accepted, implemented
**Deciders**: Behnam RK

## Context

[ADR-0001](0001-single-guard-mode.md) deletes `vpn.enabled`. That flag was doing a
second job nobody had named: it was the **safety opt-in** that stopped dezhban from
locking a host out. `docs/modes.md` was explicit — "Don't run VPN mode *without* a VPN:
GUARD needs a tunnel to pass traffic through, or you have no connectivity" — and the
default of `false` existed for that reason, not because the fallback was the normal mode.

Remove the flag and that rail goes with it. Every fresh install, and every host whose
VPN was uninstalled, is now a host running a guard with no tunnel to guard.

The behaviour that existed for this case was inconsistent. With `autoArm` on (the
default since the 2026-07 defaults review) the daemon idled harmlessly but reported
healthy while enforcing nothing. With `autoArm` off and autodetect on, it installed a
total cut — the "mystery blackout" the `AutoArm` doc comment describes as the reason
the default was flipped. Same configuration intent, opposite outcomes.

## Decision

STANDBY is a declared, first-class posture: **no rules installed, network fully open,
and the UI states plainly that the guard is off.** The daemon arms only when a
tunnel is both configured **and** has been observed up at least once.

## Alternatives considered

### Alternative 1: Fail closed — block until a VPN exists

- **Pros**: consistent with "it is a kill switch, fail closed"; no window where a
  configured guard sits idle.
- **Cons**: every fresh install cuts the network immediately.
- **Why not**: it makes first-run a lockout event *by design*. A security tool that
  bricks connectivity before it has been configured is a tool users uninstall, and the
  recovery path (`dezhban panic`) requires a working machine to look up.

### Alternative 2: Arm on configuration alone, without waiting to observe the tunnel

- **Pros**: tighter — no gap between configuring and enforcing.
- **Cons**: arms a guard that may be incapable of passing traffic.
- **Why not**: a typo'd interface name or a wrong endpoint produces a guard that passes
  nothing. `docs/troubleshooting.md` names a wrong endpoint as the **#1 lockout cause**.
  Requiring one observed tunnel-up is cheap and turns a lockout into a no-op.

## Consequences

### Positive

- A fresh install cannot lock you out.
- The two contradictory zero-tunnel behaviours collapse into one defined state; the
  `autoArm: false` + relaxed total-cut path is deleted.
- The backends' existing refusal to build an empty-interface guard becomes the assertion
  that standby is working correctly, rather than an error path.
- The UI can be honest: standby is **grey**, not red, because nothing is being cut.

### Negative

- A user who abandons setup halfway is unprotected. This is the deliberate trade against
  lockout, and it is only acceptable because the UI shouts about it — see the risk below.
- "Configured and observed up once" is a slightly more complex arming rule than either
  pure alternative, and the "observed once" bit must persist across daemon restarts to
  avoid re-entering standby on every reboot.

### Risks

- **A user mistakes standby for protection.** This is the whole risk, and it is a UI
  risk rather than an enforcement one. Mitigated by the GUI truthfulness invariant: the
  icon and Overview must state "Guard off — standby" prominently, and the first-run wizard
  must not end while the host is still in standby without saying so.
- Standby is also the natural implementation for trusted-network handling should it ever
  be built — deliberately, because it keeps the UI honest where a silent guard relaxation
  would not.
