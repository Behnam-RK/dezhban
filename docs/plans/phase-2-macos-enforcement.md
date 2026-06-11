# Phase 2 — macOS Enforcement Backend

> ## ⚠️ CAUTION — this phase can cut your own network
>
> This is the first phase that touches the live firewall. A default-deny
> `block` rule applied via `pfctl` will drop **all** outbound traffic except the
> allowlist — including the SSH/remote session you may be running it through, and
> the very geo-API calls the monitor needs to detect recovery. A bug in the
> allowlist, a crash before `Unblock`, or a botched pf-state restore can **lock
> you out of your own machine/network**.
>
> Before running `block` for real:
> - **Test on the local console**, not over SSH/VPN/remote — so a lock-out
>   doesn't also kill your way back in.
> - Verify `Unblock`/`Cleanup` works *first* (apply → immediately tear down) and
>   that the `dezhban` anchor is empty afterward (`sudo pfctl -a dezhban -s rules`).
> - Keep a manual escape ready in another terminal:
>   `sudo pfctl -a dezhban -F all` (flush our anchor) and, if pf was enabled by
>   us, `sudo pfctl -d` (disable pf). The Phase 7 `panic` command automates this,
>   but it does not exist yet during Phase 2.
> - Confirm the allowlist includes loopback, DNS, and the geo-API egress IPs, or
>   recovery detection can never fire and the block becomes permanent.
> - Snapshot/keep the original `/etc/pf.conf` and prior pf enable state so it can
>   be restored if a session dies mid-block.
>
> Treat every `block` as potentially self-inflicting until the tear-down path is
> proven reliable. Idempotency + surgical, always-safe `Cleanup()` are not nice-to-haves
> here — they are what stops a kill switch from killing the operator.

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

### Full-tunnel VPN limitation & interface-aware rules

The destination-IP ruleset above **only works on a direct connection**. Under a
full-tunnel VPN the default route is the tunnel (e.g. `utun4`), and pf on the
**physical** interface (`en0`) sees only the **encrypted outer packets to the VPN
endpoint** — the inner DNS/geo-API destinations never appear on the wire. So a
per-IP allowlist can't selectively pass them: allowing the endpoint opens the
whole tunnel (no kill switch); blocking it kills everything (no recovery poll).
See [VPN / full-tunnel mode](./README.md#vpn--full-tunnel-mode-primary-use-case).

When `vpn.enabled`, the backend emits **interface-aware** rules in one of two
modes (`$tun` = tunnel iface, `$phys` = physical iface, `$endpoint` = VPN server):

```
# GUARD (exit allowed) — always-on; VPN drop ⇒ instant cut, zero leak
pass quick on lo0 all
pass out on $tun all
pass out on $phys proto { udp tcp } to { $endpoint }    # handshake / keepalive
block drop out on $phys all

# FULL BLOCK (exit forbidden / country unknown) — cut the tunnel too
pass quick on lo0 all
block drop out on $phys all
# tunnel egress dropped; the DNS/geo-API allowlist is passed ONLY during the
# brief recovery-probe window (see Phase 4), never standing.
```

`renderRuleset` therefore gains **mode + tunnel iface + endpoint** inputs, and the
backend grows a `Policy`/`Apply` shape (`Apply(Policy{Mode, Allowlist, TunnelIfaces,
VPNEndpoints})`) that subsumes `Block`. The existing destination-IP path stays as
the `vpn.enabled=false` fallback so non-VPN hosts are unaffected.

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
