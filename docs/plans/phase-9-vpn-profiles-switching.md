# Phase 9 — VPN profiles, dynamic tunnels, and the switch window

## Goal

Deliver the "connect to any VPN, switch freely, zero leak worry" experience for
someone inside a sanctioned country who juggles many VPNs (commercial + self-
hosted, TUN-mode). After a one-time `setup`, the user runs dezhban (or the
service) and connects/switches VPNs in each client's own app without touching
dezhban again — known VPNs work instantly, brand-new ones use a bounded switch
window.

## Prerequisite

The VPN-guard reconnect-livelock fix (`.claude/plans/vpn-guard-reconnect-livelock.md`)
lands first: an undeterminable country **holds** GUARD instead of escalating, and
`vpn.allowPhysicalDNS` opens port-53 egress so a hostname endpoint can re-resolve
while the tunnel is down. Phase 9 builds on hold-GUARD-on-unknown semantics.

## What shipped

1. **Config** (`internal/config`): `vpn.profiles[]`, `vpn.switchWindow`, and a
   `vpn.advanced` tunables block; autodetect implied when the guard is enabled
   with no pinned interfaces; endpoint requirement widened to the flat∪profiles∪
   autoDiscover union; `config.EffectiveEndpoints` as the shared union helper.
   Fully backward-compatible (no migration; legacy configs round-trip unchanged).
2. **Leaf packages**: `internal/learned` (daemon-owned `learned.json` store,
   atomic, capped, TTL-pruned, corrupt-tolerant) and `internal/command` (root-
   owned control file with ownership + freshness + consume-once validation).
3. **Firewall**: `ModeSwitchWindow` (all-outbound by default, optional proto/port
   restriction), `Policy.TunnelGroups` (pf group / nft wildcard for zero-reload
   coverage of new tunnels), and a zero-tunnel FULL-BLOCK standing shape.
4. **netdetect**: multi-tunnel watcher (`Names[]`, set-change events),
   `EndpointSource.ResolveWith(tunnels)`, and a learned-endpoint source tier.
5. **Runner**: mutable tunnel set with `reconcileTunnels` (grow immediate, prune
   debounced, pinned kept, never empty), zero-tunnel standing posture with geo
   suppression, and the switch-window state machine (open → fast discovery →
   early-close-on-verified-exit + learn, else revert on cancel/expiry keeping
   session endpoints). All `Apply` calls stay in the one run-loop goroutine.
6. **CLI**: `dezhban switch` (open/cancel/status/--for/--name/--no-wait) and
   `dezhban vpn list|add|remove|import|promote|forget`; endpoint-union wiring,
   learned store + command polling in `assembleOptions`; `internal/vpnimport`
   (WireGuard/OpenVPN/V2Ray endpoint extraction, endpoints-only); status /
   detect-vpn / `config set` updates.
7. **Setup wizard**: defaults to autodetect (no pinned `utunN`), profile import,
   `allowPhysicalDNS` prompt, and a closing install-and-start step.
8. **macOS GUI**: "Switching VPN…" / "Cancel VPN switch" menu item + profile /
   window status, via the existing admin-prompt path and new state fields.

## Deferred (documented, not built)

- Proxy-mode guard for SOCKS-only V2Ray/Xray (no TUN device) + `vmess://` share-
  link import — a natural Phase 10.
- Linux/Windows live socket discovery (parity with macOS `autoDiscoverEndpoints`).
- Optional Linux eBPF backend (cgroup per-process allowance, map-based endpoint
  updates) — nftables already covers most of it.
- Per-port endpoint pinning (`addr:port` holes) and a pf `table` for very large
  endpoint unions (>256).

## Acceptance checks

1. **Compat**: a pre-phase-9 config loads, validates, and renders identical rules;
   `configs/*` all pass `dezhban validate`. (`internal/config` round-trip tests.)
2. **Union**: with two profiles, both VPNs' endpoints appear in the guard rules
   (`make rules MODE=guard CONFIG=configs/dezhban.profiles.json`); switching
   between them needs no reconfiguration.
3. **Import**: `dezhban vpn import` extracts the expected hosts from WireGuard/
   OpenVPN/V2Ray fixtures, strips ports, drops private/loopback, rejects garbage.
   (`internal/vpnimport` tests.)
4. **Switch window**: `dezhban switch` opens a window (state.json posture
   `switch-window`), the daemon learns + pins a new endpoint into `learned.json`,
   and closes early on a verified exit; `--cancel` and expiry revert to the prior
   posture. (`internal/runner` tests + live macOS run.)
5. **Dynamic tunnels**: a newly-appeared tunnel is guarded within a watcher tick
   with no restart; zero tunnels up = endpoints-open standing posture, geo
   suppressed. (`internal/runner` tests.)
6. **Wizard**: a fresh `dezhban setup` on macOS yields an autodetect +
   auto-discovery config with zero concrete interface names and offers to
   install+start the service. (`applyWizard` tests + manual run.)
7. **Cross-platform**: `go build`/tests green for darwin/linux/windows; `go.mod`
   unchanged (stdlib only; huh stays wizard-only).

## Verification

```sh
go build ./... && go vet ./... && go test ./...
GOOS=linux go build ./... && GOOS=windows go build ./...
make rules MODE=guard   CONFIG=configs/dezhban.profiles.json
make rules MODE=switch  CONFIG=configs/dezhban.profiles.json   # (print-rules --mode switch)
# Live macOS: setup → connect VPN A (guarded) → disconnect → `dezhban switch` →
# connect self-hosted VPN B → window learns+closes → `vpn promote` → reconnect B
# with NO window → `--sim-country IR` still FULL-BLOCKs → `dezhban panic` restores.
```
