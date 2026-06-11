# Phase 5 — Cross-Platform Backends

## Goal
Port enforcement to Linux and Windows behind the existing `FirewallBackend`
interface, proving the abstraction holds. No changes to monitor/decision/run.

## Scope
- Confirm `FirewallBackend` seam needs no changes
- `nft_linux.go` — Linux nftables backend
- `wfp_windows.go` — Windows WFP backend
- Per-OS privilege check + `New()` build-tag dispatch

## Design

### Build tags
- `pf_darwin.go`   `//go:build darwin`
- `nft_linux.go`   `//go:build linux`
- `wfp_windows.go` `//go:build windows`

`New()` in each file returns that OS's backend. The `notimpl_others.go` stub from
Phase 2 is removed once all three exist.

> **Interface-aware parity (VPN mode).** macOS introduced interface-aware GUARD /
> FULL-BLOCK rules (see [VPN mode](./README.md#vpn--full-tunnel-mode-primary-use-case)
> and Phase 2). Linux and Windows backends must mirror the same two states via
> their native interface conditions — not just the destination-IP allowlist.

### Linux — nftables ([`google/nftables`](https://github.com/google/nftables))
- Pure-Go netlink, no shelling to `nft`.
- Create a **dedicated table** `inet dezhban` with an `output` chain (hook output,
  priority filter). Default policy drop; accept rules for loopback, DNS, geo-API
  egress, and established/related (ct state).
- **Interface-aware (VPN mode)**: GUARD = accept `oifname $tun` + accept output to
  `$endpoint` on `$phys` + drop other output (`meta oif`/`oifname` match); FULL
  BLOCK additionally drops `oifname $tun`. Maps the same `Policy{Mode,TunnelIfaces,
  VPNEndpoints}` shape as macOS.
- `Block`: add table+chain+rules. `Unblock`: delete the `dezhban` table (surgical —
  touches nothing else). `IsBlocked`: table exists with rules.
- ⚠️ `google/nftables` API is self-described as early-stage. Wrap every call behind
  our interface; if a needed rule shape (e.g. ct state expr) is awkward, **fall
  back to shelling `nft -f -`** with a self-contained ruleset. Document which path
  is used.
- Requires `CAP_NET_ADMIN` (root). Privilege check: `geteuid()==0` (Phase 0 covers).

### Windows — WFP ([`tailscale/wf`](https://github.com/tailscale/wf))
- Pure-Go WFP bindings (battle-tested in Tailscale client).
- Create a WFP **sublayer/provider tagged `dezhban`** so all rules can be removed
  as a group. Add block filters on the outbound connect layers, with permit
  filters (higher weight) for loopback, DNS, and geo-API egress.
- **Interface-aware (VPN mode)**: key filters on the interface LUID — GUARD =
  permit on the tunnel LUID + permit to `$endpoint` on the physical LUID + block
  other outbound; FULL BLOCK additionally blocks the tunnel LUID. Same `Policy`
  shape as the other backends.
- `Block`/`Unblock`/`IsBlocked`/`Cleanup` map to adding/removing the tagged
  sublayer's filters.
- Privilege: requires Administrator. Implement the real Windows admin check here
  (replace Phase 0 stub) — check process token elevation.
- Alternative if `wf` proves hard: shell `New-NetFirewallRule -Group dezhban` via
  PowerShell and tear down by group. Keep behind the interface either way.

### Cross-compilation note
`google/nftables` and `tailscale/wf` are OS-specific; build tags keep them out of
other targets. Verify `GOOS=darwin/linux/windows go build` each compile cleanly
(the foreign-OS backend file is simply excluded).

## Files touched
- `internal/firewall/nft_linux.go`
- `internal/firewall/wfp_windows.go`
- `internal/firewall/backend.go` (only if interface gaps surface — ideally none)
- `internal/privilege/privilege_windows.go` (real admin check)
- remove `internal/firewall/notimpl_others.go`

## Dependencies (new)
- `github.com/google/nftables` (linux build only)
- `github.com/tailscale/wf` (windows build only)

## Acceptance / verification
- **Linux** (root): `dezhban block` cuts egress except allowlist; `nft list ruleset`
  shows only the `dezhban` table; `unblock` deletes it cleanly; idempotent re-block.
- **Windows** (admin): `dezhban block` cuts egress except allowlist; rules visible
  under the `dezhban` group/sublayer; `unblock` removes them; idempotent.
- `GOOS=… go build ./...` succeeds for all three targets.
- Reuse the same `run` end-to-end test from Phase 3 on each OS.
- **Interface-aware (VPN mode)**: with GUARD active, simulate a tunnel-down
  (bring the tunnel iface down) → all egress is cut with no physical leak; bring
  it back → traffic resumes. Confirm rules honor the tunnel/endpoint interface
  conditions (`nft list ruleset` / WFP filter dump), not just dst-IPs.

## Out of scope
Service install (Phase 6). Packaging (Phase 7). Offline mmdb hybrid (deferred).
