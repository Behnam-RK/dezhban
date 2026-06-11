# Phase 7 — Safety Nets & Packaging

## Goal
Ship-readiness: a panic/manual-unblock that works even if the daemon is dead,
manual override, logging polish, and cross-compiled binaries for all three OSes.

## Scope
- `panic` command — standalone rule flush, no daemon needed
- Manual override (force allow/block regardless of detection)
- Logging & status polish
- Cross-compile build matrix

## Design

### `dezhban panic` (the most important safety net)
A self-contained teardown that removes dezhban's tagged firewall rules **without
talking to the running daemon** — because a stale block-all rule could lock the
user out of their own network if the daemon crashed.
- Implementation: call the OS backend's `Cleanup()` directly (each backend's
  teardown targets only the `dezhban` tag/anchor/table/sublayer, so it works
  whether or not a daemon owns it).
- Must run even when the service is stopped/crashed. Idempotent (no-op if already
  clean). Requires root/admin.
- macOS: flush `dezhban` anchor + restore saved pf state if a saved-state file
  exists. Linux: delete `dezhban` nft table. Windows: remove `dezhban` sublayer.
- Persist minimal restore state to a known file on `Block` (e.g.
  `/var/run/dezhban/state.json`) so `panic` from a cold process can restore prior
  firewall state correctly.

### Manual override
- `dezhban block --force` / `dezhban unblock --force` (Phase 2 commands; ensure
  they bypass detection/hysteresis).
- Optional config/flag `mode: auto|always-block|always-allow` for the daemon, so a
  user can pin state.

### Logging & status polish
- `status` reports: version, privileged?, blocked?, current detected country,
  last successful provider, service installed/running?, last N transitions.
- Consistent structured logs for every state transition (Phase 6 routes them to
  the platform logger).

### Idempotency & privilege (final audit)
- Confirm every backend's `Block` is idempotent (re-block ≠ duplicate rules) —
  re-test from Phases 2/5.
- Confirm clear privilege errors on all commands that need root/admin.

### Packaging — cross-compile matrix
```
GOOS=darwin  GOARCH=arm64  go build -o dist/dezhban-darwin-arm64  ./cmd/dezhban
GOOS=darwin  GOARCH=amd64  go build -o dist/dezhban-darwin-amd64  ./cmd/dezhban
GOOS=linux   GOARCH=amd64  go build -o dist/dezhban-linux-amd64   ./cmd/dezhban
GOOS=linux   GOARCH=arm64  go build -o dist/dezhban-linux-arm64   ./cmd/dezhban
GOOS=windows GOARCH=amd64  go build -o dist/dezhban-windows-amd64.exe ./cmd/dezhban
```
- Add a `Taskfile`/`Makefile` target `build-all`.
- `tailscale/wf` (windows) and `google/nftables` (linux) are build-tag-isolated, so
  each target compiles only its own backend — verify all five build clean.
- Embed version via `-ldflags "-X main.version=..."`.
- Note: macOS still requires the system `pfctl` at runtime (shelled, not linked).

## Files touched
- `cmd/dezhban/main.go` (`panic`, override flags, richer `status`)
- `internal/firewall/*` (state persistence for cold `panic`/restore)
- `Taskfile.yml` or `Makefile` (`build-all`)
- `internal/runner` / `internal/decision` (override `mode`)

## Dependencies
None new (state file = stdlib JSON).

## Acceptance / verification
1. Start `run`, let it block, **kill -9** the process → network still blocked →
   `sudo dezhban panic` → connectivity fully restored, rules gone, prior state
   restored. Repeat per OS.
2. `dezhban panic` on a clean system → no-op, no error (idempotent).
3. `dezhban block --force` / `unblock --force` ignore detection.
4. `build-all` produces all five binaries; spot-run each on its OS.
5. `status` shows accurate blocked/country/service fields.

## Out of scope
Offline mmdb hybrid detection (deferred enhancement). Code signing / notarization
(macOS) and installer packaging (.pkg/.deb/.msi) — future, if distribution needed.
