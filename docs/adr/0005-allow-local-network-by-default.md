# ADR-0005: Local network access is allowed by default

**Date**: 2026-07-20
**Status**: accepted, implemented
**Deciders**: Behnam RK

## Context

There is **no LAN handling anywhere in dezhban.** None of the three backends contains a
single reference to RFC1918 ranges, link-local addresses, or multicast.

The consequence is that the moment the guard arms, every local device becomes
unreachable: printers, NAS, the router's admin page, AirPlay and Chromecast targets,
local development servers, SSH to another machine on the same desk. There is no setting
to restore any of it. The only escape is turning the guard off.

This is a significant daily-usability defect for a tool whose value depends on being left
running. Every comparable product — Mullvad, ProtonVPN, Windscribe — ships local network
access as a top-level toggle.

## Decision

Add a top-level `allowLocalNetwork` boolean, **defaulting to true**, which adds
destination-scoped passes to GUARD, FULL BLOCK, and the switch window alike:

```
IPv4    10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16, 169.254.0.0/16
IPv6    fc00::/7 (ULA), fe80::/10 (link-local)
mcast   224.0.0.0/4, ff00::/8      — mDNS/Bonjour needs 224.0.0.251 / ff02::fb
```

Implemented once in the shared policy constructor, so all three backends and
`print-rules` inherit it from a single definition.

## Alternatives considered

### Alternative 1: Default to off, opt in

- **Pros**: strictly the more conservative default; matches Mullvad's choice.
- **Cons**: every user hits the breakage first and has to discover the setting.
- **Why not**: the default should reflect the threat model, and this costs nothing
  against it (see below). Making everyone experience a confusing failure in order to
  protect against a risk they do not have is the wrong trade for this tool's users.

### Alternative 2: Allow the local subnet only, discovered from the physical interface

- **Pros**: narrower than all of RFC1918.
- **Cons**: requires runtime discovery and re-derivation on every network change; breaks
  multi-subnet homes and VLANs; a new failure mode when discovery is wrong.
- **Why not**: materially more complexity for a marginal reduction in scope. The addresses
  excluded are ones the host has no route to anyway.

### Alternative 3: Per-application local access

- **Pros**: finest granularity.
- **Cons**: not expressible across pf, nft, and WFP.
- **Why not**: the same reason per-app rules were rejected elsewhere in this project — the
  primitive does not exist portably.

## Consequences

### Positive

- Printers, NAS, router administration, casting, and local development work with the
  guard armed. Removes the most common reason to turn the guard off.
- **Costs nothing against the threat model.** dezhban exists to prevent a standing direct
  connection exposing a sanctioned-country IP to a *foreign service*. RFC1918 traffic
  never leaves the building, so it cannot carry that exposure.
- Destination-scoped, so it cannot become an internet path: packets to public addresses
  remain blocked regardless of next hop.

### Negative

- More rules in every posture, including FULL BLOCK — which is intentional (a full block
  should not sever local access either) but does widen the ruleset in the most
  safety-critical state.
- Another config key, though it is one users expect to find.

### Risks

- **On untrusted Wi-Fi this permits traffic to other devices on that network.** Real but
  modest: the rules are outbound-only, so this enables reaching others, not being reached.
  Mitigated by stating it plainly in the setting's hint text rather than hiding it —
  users on café networks can turn it off.
- The passes must be IPv6-aware from the first commit rather than retrofitted; a v4-only
  implementation would silently fail on v6-capable LANs. This lands alongside the
  address-family normalisation work, which fixes a latent pf bug: addresses are rendered
  with a bare `.String()`, so a 4-in-6 mapped address emits `::ffff:1.2.3.4`, which pf
  rejects — a ruleset that fails to load. Linux already unmaps; pf does not. Normalise in
  the shared policy constructor so no backend has to defend itself.
