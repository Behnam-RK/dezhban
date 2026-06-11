# Phase 4 — Resilience

## Goal
Make the decision layer robust: fail-closed on lookup failure, hysteresis to stop
flapping, allowlist hardening, and multi-provider quorum/fallback.

## Scope
- Fail-closed semantics in `Decider`
- Hysteresis (N consecutive readings before toggling)
- Allowlist hardening (geo-API egress guaranteed)
- Multi-provider fallback / quorum tuning

## Design

### Fail-closed (`internal/decision`)
Flip Phase 3's `error → Allow` to **`error → Block`**, gated by config `FailClosed`
(default true). When all providers fail, treat as "country unknown → block."
- Guard against lock-out: the allowlist (geo-API egress + DNS + loopback) MUST
  stay open even in the fail-closed block so the monitor can recover. This is why
  allowlist hardening lives here.

### Hysteresis (`Decider`)
Require `Hysteresis` (default 3) consecutive readings agreeing before flipping
state. Implementation: a counter that increments toward the candidate verdict and
resets on disagreement; only toggle when the counter reaches the threshold.
```go
type Decider struct {
    blocked    map[string]bool
    failClosed bool
    need       int          // hysteresis threshold
    current    Verdict      // last committed verdict
    streak     int          // consecutive readings for the candidate
    candidate  Verdict
}
func (d *Decider) Evaluate(r monitor.Result) Verdict  // returns committed verdict
```
- A single bad lookup or one-off wrong reading no longer flips the firewall.
- Fail-closed interacts with hysteresis: decide whether a lookup *error* counts
  toward a Block streak immediately or after N errors. Recommendation: errors
  feed the streak like any reading (N consecutive errors → block), so transient
  blips don't trigger.

### Recovery while in FULL BLOCK — VPN back to an allowed country
(VPN mode only — see [VPN mode](./README.md#vpn--full-tunnel-mode-primary-use-case).)

In FULL BLOCK the tunnel is cut, so the exit country can't be observed and pf
can't allow *only* the geo-API inside a tunnel. Recovery uses a **time-windowed
probe**:

```
each poll tick while FULL BLOCK:
    backend.Apply(GUARD)          # briefly lift — opens the tunnel
    reading = monitor.Once(ctx)   # ONE geo-API lookup through the tunnel
    backend.Apply(FULL BLOCK)     # re-cut immediately
    feed reading into the hysteresis streak
when `Hysteresis` consecutive probes report an allowed country ⇒ commit GUARD
```

- Tradeoff: one lookup's worth of egress per interval while blocked (controlled
  minimal egress — the accepted recovery semantics).
- Interaction with hysteresis: probe readings feed the same streak counter; a
  single allowed reading does not lift the block.
- Fallbacks if the probe never clears: `dezhban unblock --force` and `dezhban
  panic` (Phase 7) both tear the guard/block down without the daemon.

### Allowlist hardening (`internal/firewall` + assembly)
- Always include: loopback, configured DNS resolvers, and **all** geo-API
  provider IPs (resolve every provider host, not just the first).
- Re-resolve provider IPs periodically (IPs rotate); refresh the allowlist on each
  `Block` and on a slow timer. Document the risk: if a provider's IP changes while
  blocked and DNS is also blocked, recovery could stall — mitigate by always
  allowing DNS egress so re-resolution works.
- **VPN mode**: the allowlist matters only during the probe window above and for
  `vpn.enabled=false`; the standing guarantee is interface-based (GUARD), not the
  dst-IP list. Fail-closed (country unknown) maps to **FULL BLOCK**, not the
  legacy dst-IP block.

### Multi-provider (`internal/monitor`)
- Phase 1 already does ordered fallback. Add optional **quorum**: query K
  providers, require majority agreement on country (defends against a single
  spoofed/wrong provider). Config flag `providerQuorum` (default off = first-success).
- Log disagreements at warn.

## Files touched
- `internal/decision/decision.go` (fail-mode + hysteresis)
- `internal/monitor/monitor.go` (optional quorum)
- `internal/firewall/*` (allowlist always includes all provider IPs)
- `internal/config/config.go` (`providerQuorum`, ensure `failClosed`/`hysteresis` wired)

## Dependencies
None new.

## Acceptance / verification
1. **Fail-closed:** block all provider hosts via `/etc/hosts` while running →
   within N error-ticks the firewall blocks; loopback/DNS/provider-allowlist still
   intact; restore hosts → recovers.
2. **No flap:** inject an alternating country sequence (mock provider) → firewall
   does NOT toggle until `Hysteresis` consecutive readings agree.
3. **Quorum:** with 3 mock providers and 1 disagreeing → majority wins, warn logged.
4. `go test ./internal/decision` — hysteresis state machine: streak build, reset on
   disagreement, fail-closed-on-error paths.

## Out of scope
Cross-platform backends (Phase 5). Service mode (Phase 6). Panic command (Phase 7).
