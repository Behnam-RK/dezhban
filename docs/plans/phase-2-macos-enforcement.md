# Phase 2 — macOS Enforcement Backend

## Goal
First real firewall backend. Implement `FirewallBackend` for macOS via `pfctl`
and a dedicated `dezhban` pf anchor. Wire manual `block` / `unblock` / `status`
CLI so the firewall calls can be verified end-to-end before any automation.

## Scope
- `FirewallBackend` interface (`internal/firewall/backend.go`)
- `pf_darwin.go` — pfctl anchor backend
- Manual CLI: `dezhban block`, `dezhban unblock`, `dezhban status`
- Idempotent block, surgical teardown, allowlist for recovery

## Design

### Interface (`backend.go`)
```go
type Allowlist struct {
    DNS       []netip.Addr   // resolvers that must stay reachable
    Hosts     []netip.Addr   // geo-API egress IPs (so recovery detection works)
    // loopback is always implicitly allowed
}

type FirewallBackend interface {
    Block(a Allowlist) error   // idempotent: re-block must not stack rules
    Unblock() error            // remove ONLY dezhban's rules
    IsBlocked() (bool, error)
    Cleanup() error            // always-safe teardown (shutdown/panic)
}

func New() (FirewallBackend, error)   // build-tagged per OS
```
`pf_darwin.go` is `//go:build darwin`. Linux/Windows files return a "not
implemented" backend until Phase 5.

### pfctl backend
Block = default-deny outbound, pass the allowlist + loopback + established/related.
Rules live in a **dedicated anchor `dezhban`** so flushing never touches other rules.

Ruleset loaded into the anchor (stdin via `pfctl -a dezhban -f -`):
```
# loopback always
pass quick on lo0 all
# allow DNS to configured resolvers (out)
pass out quick proto { udp tcp } to <dns_addrs> port 53
# allow geo-API egress so we can detect leaving the blocked country
pass out quick to <allow_hosts>
# keep established connections alive
pass out quick flags any keep state    # (refine: rely on keep state for return traffic)
# default: drop everything else outbound
block drop out all
```

Operations:
- `Block(a)`: render template with allowlist → load into anchor → ensure pf is
  enabled. **Capture prior pf enable state first** (`pfctl -s info`) to restore
  on unblock. Idempotent: loading the anchor again just replaces its contents.
- `Unblock()`: flush the anchor (`pfctl -a dezhban -F all`); if we enabled pf and
  it was previously disabled, disable it again.
- `IsBlocked()`: `pfctl -a dezhban -s rules` non-empty → blocked.
- `Cleanup()`: same as Unblock but never errors fatally (best-effort, logged).

### ⚠️ Research item to resolve during implementation
macOS only **evaluates** an anchor if the main ruleset (`/etc/pf.conf`) contains
an `anchor "dezhban"` line. Two options — pick one and document it:
- **(A) Require a one-time setup**: `dezhban install-anchor` appends the anchor
  reference to `/etc/pf.conf` (backed up first). Clean, but mutates a system file.
- **(B) Load a complete main ruleset** that includes the anchor inline, replacing
  the active ruleset while blocked and restoring the saved original on unblock.
  Self-contained, but must carefully save/restore `/etc/pf.conf` state.

Default recommendation: **(B)** during a block window (save current ruleset via
`pfctl -s rules` / `-s nat`, load ours, restore on unblock) to avoid persistent
system-file edits. Validate return-traffic behavior with `keep state` carefully.

### CLI wiring
- `block` → load config allowlist (DNS from resolv/config, resolve geo-API
  hostnames to IPs), call `Block`. Requires root.
- `unblock` → `Unblock`. Requires root.
- `status` → extend Phase 0 status with `IsBlocked()` result.

All `pfctl` calls go through a small `exec.CommandContext` helper that captures
stderr and wraps errors with the command for debuggability.

## Files touched
- `internal/firewall/backend.go`
- `internal/firewall/pf_darwin.go`
- `internal/firewall/notimpl_others.go` (`//go:build !darwin` stub for now)
- `internal/firewall/pfctl.go` (exec helper, darwin)
- `cmd/dezhban/main.go` (wire block/unblock/status)

## Dependencies
stdlib only (`os/exec`, `text/template`, `net`). Shell out to system `pfctl`.

## Acceptance / verification
Run on macOS with `sudo`:
1. `sudo dezhban block` → confirm general egress dies (`curl https://example.com`
   hangs/fails) **but** loopback works, DNS resolves, and the configured geo-API
   host is still reachable.
2. `dezhban status` → reports `blocked: true`.
3. `sudo dezhban block` again → no duplicate rules (`sudo pfctl -a dezhban -s rules`
   shows the same set). Idempotent.
4. `sudo dezhban unblock` → full connectivity restored; `pfctl -a dezhban -s rules`
   empty; original pf state restored.
5. Kill the process mid-block, then `sudo dezhban unblock` → still cleans up.

## Out of scope
Automation loop (Phase 3), Linux/Windows (Phase 5), panic-without-daemon (Phase 7,
though Cleanup lays groundwork).
