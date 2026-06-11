# dezhban — Implementation Plans

**dezhban** (Persian: گزدزدزدبان / "gatekeeper") is a standalone, cross-platform
**network kill switch**. It watches the machine's public IP, resolves its
country, and when the country matches a blocklist it drives the OS firewall to
cut traffic — while keeping a minimal allowlist so recovery detection still works.

It is also **VPN-aware**: a primary deployment is running behind a full-tunnel
VPN, where dezhban must (a) cut traffic the instant the VPN drops unnoticed, and
(b) cut traffic when the VPN exit switches to a forbidden country. See
[VPN / full-tunnel mode](#vpn--full-tunnel-mode-primary-use-case).

## Locked decisions

| Decision | Choice | Rationale |
|---|---|---|
| Language | **Go** | Single static binary per OS, `go build` cross-compiles, no runtime |
| Platform order | **macOS first** → Linux → Windows | Build/verify one backend end-to-end, then port behind the interface |
| Detection | **API-based first**, offline IP-range hybrid later | Simple to start; add robustness once the loop works |
| Fail mode | **Fail-closed** | Block when country is undeterminable — safe default for a security tool |
| Enforcement primitive | **Interface-aware** (pass-on-tunnel + endpoint handshake, block physical) | A destination-IP allowlist is meaningless under a full tunnel — pf/nft see only outer packets to the VPN endpoint |
| Guard model | **Always-on interface guard** | VPN drop ⇒ instant cut, zero leak window. A reactive poller leaks for one poll interval |
| Recovery | **VPN returns to allowed country** | While full-blocked, observe the exit via a time-windowed probe; auto-restore the guard when the exit is allowed again |

## Architecture (3 layers)

```
Monitor  ── polls public IP, resolves country        (platform-independent)
   │
   ▼
Decision ── blocklist + hysteresis + fail-mode → Block/Allow   (platform-independent)
   │
   ▼
Enforcement ── FirewallBackend per OS                (only platform-specific part)
```

The `FirewallBackend` interface is the seam: ~90% of code is shared; one small
module differs per OS. Every firewall rule carries a unique tag/anchor (`dezhban`)
so teardown is surgical.

Enforcement is **interface-aware**: it consumes the tunnel interface(s) and VPN
endpoint(s) and runs in one of two states — **GUARD** (exit allowed: pass tunnel
egress + endpoint handshake, block all other physical egress) and **FULL BLOCK**
(exit forbidden / country unknown: cut the tunnel too). See below.

## VPN / full-tunnel mode (primary use-case)

Under a full-tunnel VPN the default route is the tunnel (e.g. `utun4`). The
firewall on the **physical** interface (e.g. `en0`) sees only the **encrypted
outer packets to one address — the VPN endpoint**; inner destinations (DNS,
geo-API) never appear on the wire. So the original destination-IP allowlist is
the **wrong primitive**: allow the endpoint ⇒ the whole tunnel passes (no kill
switch); block the endpoint ⇒ everything dies, including the polling that detects
recovery.

**The fix is interface-aware enforcement with two states:**

- **GUARD** (armed/normal, exit allowed) — continuous, always-on:
  `pass quick on lo0` · `pass out on $tun all` · `pass out on $phys to $endpoint`
  (handshake/keepalive) · `block drop out on $phys all`. Tunnel traffic flows
  normally; if the tunnel disappears, physical egress is already locked ⇒ **zero
  leak window**. Country detection polls *through* the tunnel and reflects the
  exit country.
- **FULL BLOCK** (exit forbidden, or unknown under fail-closed) — cut the tunnel
  too. pf can't allow *only* the geo-API inside a tunnel, so recovery uses a
  **time-windowed probe**: on each poll tick, briefly lift the block, run **one**
  geo-API lookup through the tunnel, re-apply. After `hysteresis` consecutive
  allowed probes ⇒ return to GUARD. Tradeoff: one lookup's worth of egress per
  interval while blocked (controlled minimal egress).

Config — a `vpn` block (guard is active only when `enabled`; with it off the
behavior is the legacy destination-IP model, unchanged):

```json
"vpn": {
  "enabled": true,            // opt-in; always-on guard can lock you out, default false
  "tunnelInterfaces": ["utun*"],
  "endpoints": ["203.0.113.5"],
  "autodetect": true          // assist iface/endpoint discovery; explicit values win
}
```

**Where each part is implemented:**

| Part | Phase |
|---|---|
| Interface-aware backend rules (macOS `pfctl`) | [Phase 2](./phase-2-macos-enforcement.md) |
| Guard state machine in the `run` daemon | [Phase 3](./phase-3-wire-end-to-end.md) |
| Recovery probe + fail-closed interplay | [Phase 4](./phase-4-resilience.md) |
| Interface-aware parity (Linux nft / Windows WFP) | [Phase 5](./phase-5-cross-platform.md) |
| `panic`/`Cleanup` of guard, `status`, `detect-vpn` | [Phase 7](./phase-7-safety-packaging.md) |

## Phases

| Phase | Doc | Status | Theme |
|---|---|---|---|
| 0 | [phase-0-scaffold.md](./phase-0-scaffold.md) | ✅ | Go module, CLI skeleton, config, logging, privilege check, CLAUDE.md |
| 1 | [phase-1-monitor.md](./phase-1-monitor.md) | ✅ | Public-IP fetch + country resolve + polling loop (prints country) |
| 2 | [phase-2-macos-enforcement.md](./phase-2-macos-enforcement.md) | ☐ | `pfctl` anchor backend + manual `block`/`unblock`/`status` |
| 3 | [phase-3-wire-end-to-end.md](./phase-3-wire-end-to-end.md) | ☐ | Decision layer + monitor→decision→enforcement daemon (macOS) |
| 4 | [phase-4-resilience.md](./phase-4-resilience.md) | ☐ | Fail-closed, hysteresis, allowlist hardening, multi-provider |
| 5 | [phase-5-cross-platform.md](./phase-5-cross-platform.md) | ☐ | Linux `nftables` + Windows WFP backends |
| 6 | [phase-6-persistence.md](./phase-6-persistence.md) | ☐ | Run as service: launchd / systemd / Windows Service |
| 7 | [phase-7-safety-packaging.md](./phase-7-safety-packaging.md) | ☐ | Panic-unblock, manual override, logging polish, cross-compile |

Each phase is independently buildable & verifiable. Implement one at a time;
verify before moving on.

## Dependency strategy (R&D summary)

Keep the binary lean — stdlib where it suffices; a dep only where it removes real
complexity. Only **3 real deps**, one per hard platform problem:

| Lib | Phase | Role |
|---|---|---|
| [`kardianos/service`](https://github.com/kardianos/service) | 6 | cross-platform service (launchd/systemd/winsvc), one API |
| [`google/nftables`](https://github.com/google/nftables) | 5 | pure-Go netlink nftables (Linux), no shelling |
| [`tailscale/wf`](https://github.com/tailscale/wf) | 5 | pure-Go Windows Filtering Platform bindings |
| [`oschwald/geoip2-golang/v2`](https://github.com/oschwald/geoip2-golang) | hybrid (deferred) | offline IP→country via GeoLite2 mmdb |

- **macOS enforcement** → shell out to `pfctl` (no maintained Go pf lib). Expected.
- **CLI / config / logging / HTTP** → stdlib (`flag`, `encoding/*`, `log/slog`,
  `net/http`). Avoid `viper`/`cobra` to keep the standalone promise.

## Project layout (created in Phase 0)

```
dezhban/
  go.mod                       # github.com/behnam-rk/dezhban (path adjustable)
  cmd/dezhban/main.go          # CLI: run, block, unblock, status, panic
  internal/
    config/config.go           # Config struct + load + defaults (+ vpn block)
    monitor/{monitor.go,provider.go}
    decision/decision.go
    firewall/{backend.go,pf_darwin.go,nft_linux.go,wfp_windows.go}
    netdetect/                 # tunnel-iface + VPN-endpoint auto-detect (VPN mode)
    logging/logging.go
  configs/dezhban.example.yaml
  CLAUDE.md
  docs/plans/                  # these files
```

The `vpn` config block (see [VPN mode](#vpn--full-tunnel-mode-primary-use-case))
adds `enabled`, `tunnelInterfaces`, `endpoints`, `autodetect`. The optional
`internal/netdetect` helper backs a `dezhban detect-vpn` command that prints the
detected tunnel interface + endpoint so users can fill the config and avoid
self-lockout.
