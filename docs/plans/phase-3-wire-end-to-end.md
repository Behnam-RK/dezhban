# Phase 3 — Wire Monitor → Decision → Enforcement

## Goal
Tie the three layers into a working daemon on macOS. The `run` command polls the
country, decides block/allow, and drives the firewall — with clean shutdown.

## Scope
- `Decision` layer (blocklist membership → verdict)
- `run` loop: Monitor → Decision → Backend
- Signal handling: always `Cleanup()` on exit

## Design

### Decision (`internal/decision/decision.go`)
```go
type Verdict int
const ( Allow Verdict = iota; Block )

type Decider struct {
    blocked map[string]bool   // country codes, set
    // Phase 4 adds: failClosed, hysteresis counters
}

// Evaluate maps a monitor result to a verdict.
// Phase 3 (simple): in-blocklist → Block; else Allow.
//   On lookup error → Allow for now (Phase 4 flips to fail-closed).
func (d *Decider) Evaluate(r monitor.Result) Verdict
```
Keep `Evaluate` pure and table-testable. Phase 4 layers fail-mode + hysteresis
on top without changing the call site.

### run loop (`cmd/dezhban/main.go` / a small `internal/runner`)

The loop is a **state machine**. Its shape depends on `vpn.enabled`:

```
build Monitor, Decider, Backend
resolve tunnel iface(s) + VPN endpoint(s)   # config, or netdetect autodetect
defer Backend.Cleanup()
ctx, cancel = signal.NotifyContext(SIGINT, SIGTERM)

if vpn.enabled:                              # ── VPN guard mode ──
    backend.Apply(GUARD policy)              # always-on from the start
for result := range monitor.Poll(ctx):
    verdict = decider.Evaluate(result)
    if vpn.enabled:
      Block: ensure FULL BLOCK   { backend.Apply(FullBlock); log "FULL BLOCK (country=X)" }
      Allow: ensure GUARD        { backend.Apply(Guard);     log "GUARD     (country=X)" }
    else:                                    # ── legacy direct mode ──
      Block: if !blocked { backend.Block(allowlist); log "BLOCKING (country=X)" }
      Allow: if blocked  { backend.Unblock();        log "ALLOWING (country=X)" }
on ctx done: backend.Cleanup(); exit 0
```

- **VPN mode**: GUARD is installed immediately at startup (always-on, so a VPN
  drop is cut even before the first poll), then the verdict toggles GUARD ↔ FULL
  BLOCK. Recovery out of FULL BLOCK (probe) is detailed in Phase 4.
- **Legacy mode** (`vpn.enabled=false`): unchanged Block/Unblock on the dst-IP
  allowlist.
- Track current applied state in-process to avoid redundant calls (the backend is
  idempotent, but we want clean transition logs).
- `Block`/`Unblock`/`Apply` are idempotent + surgical from Phase 2.
- Requires root (reuse Phase 0 privilege check).

### Allowlist assembly
Resolve geo-API provider hostnames → IPs at startup and feed them into the
`Allowlist` so the monitor can still reach providers while blocked (critical for
detecting when we leave the blocked country). Re-resolve on each `Block` in case
IPs rotated.

In **VPN mode** the allowlist is only used during the FULL-BLOCK recovery-probe
window (Phase 4); the standing guarantee is interface-based. The daemon also
resolves the **tunnel interface(s)** and **VPN endpoint(s)** at startup — from
the `vpn` config block, or via `internal/netdetect` autodetect — and feeds them
into the `Policy` passed to `backend.Apply`.

## Files touched
- `internal/decision/decision.go`
- `internal/runner/runner.go` (or expand `run` in main)
- `cmd/dezhban/main.go`
- `internal/netdetect/*` (VPN mode: resolve tunnel iface + endpoint at startup)
- `internal/config/config.go` (`vpn` block)

## Dependencies
None new.

## Acceptance / verification
On macOS with `sudo`:
1. Set `blockedCountries` to **your own current country** → start `sudo dezhban run`
   → within N ticks egress is cut; logs show `BLOCKING (country=XX)`.
2. Edit config to remove your country + restart (or, if no hot-reload yet, this is
   simplest) → `ALLOWING` and connectivity returns.
3. `Ctrl-C` while blocked → `Cleanup()` runs, connectivity restored, exit 0.
4. `go test ./internal/decision` — table tests: in-list→Block, out→Allow,
   error→Allow (Phase 3 semantics).

## Out of scope
Fail-closed, hysteresis, multi-provider quorum (Phase 4). Config hot-reload
(optional, later). Cross-platform (Phase 5).
