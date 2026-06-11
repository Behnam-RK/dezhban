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
```
build Monitor, Decider, Backend
defer Backend.Cleanup()
ctx, cancel = signal.NotifyContext(SIGINT, SIGTERM)
for result := range monitor.Poll(ctx):
    verdict = decider.Evaluate(result)
    switch verdict:
      Block: if !blocked { backend.Block(allowlist); log "BLOCKING (country=X)" }
      Allow: if blocked  { backend.Unblock();        log "ALLOWING  (country=X)" }
on ctx done: backend.Cleanup(); exit 0
```
- Track current applied state in-process to avoid redundant Block/Unblock calls
  (the backend is idempotent, but we still want clean transition logs).
- `Block`/`Unblock` already idempotent + surgical from Phase 2.
- Requires root (reuse Phase 0 privilege check).

### Allowlist assembly
Resolve geo-API provider hostnames → IPs at startup and feed them into the
`Allowlist` so the monitor can still reach providers while blocked (critical for
detecting when we leave the blocked country). Re-resolve on each `Block` in case
IPs rotated.

## Files touched
- `internal/decision/decision.go`
- `internal/runner/runner.go` (or expand `run` in main)
- `cmd/dezhban/main.go`

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
