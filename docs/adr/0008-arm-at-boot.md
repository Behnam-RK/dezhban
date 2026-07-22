# ADR-0008: Arm at boot from a persisted observation, plus a bounded pause

**Date**: 2026-07-22
**Status**: accepted, implemented
**Deciders**: Behnam RK

## Context

[ADR-0002](0002-standby-no-tunnel-posture.md) decided the daemon arms only
when a tunnel is "configured **and** has been observed up at least once," and
flagged in its own Consequences that the "observed once" bit "must persist
across daemon restarts to avoid re-entering standby on every reboot." That
persistence was never built. `runGuard` decides standby from a live interface
probe (`netdetect.TunnelInterfaces()`) taken fresh on every process start —
not from any record of past observation.

dezhban is a launchd/systemd daemon; the VPN client is typically a
later-starting user-session service. On an ordinary boot the probe runs before
the tunnel interface exists, finds nothing, and — with `vpn.autoArm` on (the
default) — the daemon enters STANDBY and calls `Backend.Unblock()`, actively
clearing whatever rules were standing from the previous session. A host that
has run this kill switch successfully for months boots with the network fully
open, for as long as the VPN client takes to connect.

For a user behind a VPN in a sanctioned country, this is the opposite of the
intended guarantee: the machine should be unreachable except through the
tunnel from the moment it boots, not from the moment the VPN happens to win a
race with the daemon.

A related but separate need surfaced at the same time: the same user
sometimes needs the real, ISP-assigned IP on purpose — reaching a domestic
service (e.g. online banking) that refuses foreign exit IPs — without leaving
the guard down afterward by forgetting to turn it back on.

## Decision

**Arm at boot.** Persist one fact, `TunnelEverUp`, the first time a configured
tunnel is observed up (`internal/armed`, modelled on `internal/learned`'s
atomic-write convention). At startup, if `vpn.armAtBoot` is on (default true)
and the live probe would otherwise select STANDBY, override it and arm
directly — **provided both** an endpoint is already known **and**
`TunnelEverUp` is true. Both conditions gate the override; either one being
false leaves today's behavior in place. No new firewall mode: with no tunnel
interface present yet, `PolicyInput.Guard()` already degrades to the
`FullBlock()` rule shape (endpoint + DNS + local-network passes, no
tunnel-egress pass) — arming at boot renders that same, already-tested shape.

**Bounded pause, as a third window trigger.** `dezhban pause [duration]` /
`resume`, capped by `vpn.pauseMax` (default 30m, `"0"` disables), gated over
the control socket by `control.allowPauseOps` (default true, independent of
`control.allowSwitchOps`). Implemented as `state.TriggerPause` on the existing
switch-window machinery (`openWindow`/`closeWindowRevert`/timers) rather than
a parallel state machine — the rule shape ("all outbound, bounded by a timer,
auto-reverting") is identical to a switch window; only the purpose and the cap
differ.

## Alternatives considered

### Alternative 1: Block on the source/exit country instead of tunnel presence

- **Pros**: matches the literal original ask ("block if source geo IP is a
  blocked country").
- **Cons**: with no tunnel, the source country is always the ISP's — for this
  user's threat model that condition is constant-true and decides nothing.
  Deciding requires a network lookup, so either (a) egress must be briefly
  open to run it — precisely when every app on the machine fires at once on
  boot — or (b) the lookup runs over the physical link, which
  [ADR-0006](0006-geo-providers-tunnel-scoped.md) already rejected: a
  destination-only pass reports the ISP's (allowed) country and the block
  never fires.
- **Why not**: it makes a spoofable network reading the thing that *opens*
  the kill switch — the exact shape of failure
  [ADR-0001](0001-single-guard-mode.md) Alternative 1 was rejected for, just
  relocated to a different trigger. Arming from a persisted local fact needs
  no network round-trip to decide, so it cannot be fooled by one.

### Alternative 2: Arm at boot unconditionally (no `TunnelEverUp` gate)

- **Pros**: simpler; no new persisted state.
- **Cons**: this is exactly
  [ADR-0002](0002-standby-no-tunnel-posture.md) Alternative 1, "fail closed —
  block until a VPN exists," which that ADR rejected because it makes
  first-run a lockout event by design.
- **Why not**: `TunnelEverUp` is the entire reason this decision is different
  from the one already rejected. A fresh install, or a host whose VPN has
  never successfully connected, still boots into STANDBY — arm-at-boot only
  changes behavior for a host that has already proven its VPN works.

### Alternative 3: Persist the pause deadline so a restart resumes it

- **Pros**: closer to a literal "the pause survives everything."
- **Cons**: neither the manual switch window nor the automatic reconnect
  window survives a daemon restart today — both are pure in-memory episodes
  on the run loop's own timers, and a restart simply re-enters via the normal
  startup posture. Making pause the one relaxation that *does* survive a
  crash would be a new, asymmetric precedent.
- **Why not**: a daemon restart is exactly the moment to fail toward
  protection, not to remember "I was deliberately exposed" and stay that way.
  A pause in progress during a restart is simply gone; the daemon re-arms per
  its normal startup logic (arm-at-boot if `TunnelEverUp`, standby otherwise).

## Consequences

### Positive

- A host that has successfully run the guard before boots protected, closing
  the exact gap [ADR-0002](0002-standby-no-tunnel-posture.md) left open.
- A fresh install, or any host whose VPN has never come up, is still
  impossible to lock out at boot — the safety rail that ADR was built around
  is preserved, not just inherited by assumption.
- The bounded pause gives a real, bounded escape hatch for the
  domestic-service case without a silent, indefinite "protection off."
- No new firewall mode, no new posture string, no new rule-rendering code
  path: arm-at-boot's rule shape and pause's rule shape both already existed
  and were already tested.

### Negative

- `vpn.armAtBoot` defaulting to true is a behavior change on upgrade — but it
  only reaches hosts that have already had a working tunnel (the intended
  population), never a fresh install.
- Pause is a **third** sanctioned relaxation, alongside the switch window and
  the automatic reconnect window — CLAUDE.md's invariant that the switch
  window is "the ONLY sanctioned relaxation" is deliberately superseded by
  this ADR to "three sanctioned relaxations, each with its own cap, never
  shared." The two-trigger invariant was never a value in itself; the value is
  that every relaxation is bounded, independently capped, and single-purpose —
  pause satisfies all three, it is simply a third one.
- A pause in progress at the instant a daemon restart happens is lost — see
  Alternative 3.

### Risks

- **Windows**: the WFP renderer's `-InterfaceAlias` rules are not verified to
  accept an interface name that does not exist yet, so arm-at-boot may not
  render correctly there. Mitigated by forcing `vpn.armAtBoot` off on Windows
  in `cmd/dezhban/main.go` (with a logged warning) until this is verified —
  pf and nft accept a not-yet-present interface name as a plain string match.
- **A confirmed good exit during an open pause can close it early.** The
  switch-window machinery's discovery/probe logic (`maybeStartCloseProbe` /
  `finishCloseProbe`) is shared by all three triggers and does not distinguish
  "a VPN reconnected, so close early" (correct for a switch window) from "the
  operator is deliberately paused" (where an early close ends the pause
  sooner than requested). This is a tightening, not a leak — the guard
  re-arming early is always safe — so it is accepted as a known nuance rather
  than built around.
