# Configuration reference

dezhban reads a JSON config. Durations are strings (Go syntax, e.g. `"30s"`,
`"5m"`). A missing `--config` loads built-in defaults. Validate any file without
running it:

```sh
dezhban validate --config path/to/config.json
```

## Fields

| Field | Type | Default | Notes |
|---|---|---|---|
| `pollInterval` | duration string | `"30s"` | How often the public IP / country is checked. Must be > 0. |
| `blockedCountries` | `[]string` | `[]` | ISO-3166 alpha-2 codes (e.g. `"RU"`, `"IR"`). Upper-cased on load; each must be exactly 2 letters. A match triggers a block. |
| `failClosed` | bool | `true` | When the country can't be determined, block anyway (security-first). The allowlist stays open so recovery still works. |
| `hysteresis` | int | `3` | Consecutive agreeing readings required before toggling block/allow. Must be ≥ 1. Damps flapping. |
| `providers` | `[]string` | 3 geo-IP URLs | Geo-location endpoints, tried for redundancy. At least one required. |
| `allowlist.dns` | `[]string` | `[]` | Resolver IPs kept reachable while blocking, so hostname re-resolution works. |
| `allowlist.hosts` | `[]string` | `[]` | Extra host IPs always allowed. Provider IPs are added automatically at block time. |
| `providerQuorum` | bool | `false` | Require a majority of providers to agree on the country before acting. |
| `logLevel` | string | `"info"` | One of `debug`, `info`, `warn`, `error`. The `-v`/`--verbose` flag overrides this to `debug`. |
| `vpn` | object | disabled | VPN interface-guard config — see below. |

## `vpn` block

For hosts behind a full-tunnel VPN, the guard cuts the **physical** interface
while keeping the **tunnel** open, instead of the destination-IP allowlist (which
is meaningless under a tunnel). Opt-in — a misconfigured guard can lock you out.

| Field | Type | Default | Notes |
|---|---|---|---|
| `vpn.enabled` | bool | `false` | Turns on guard mode. |
| `vpn.tunnelInterfaces` | `[]string` | `[]` | Tunnel interface names (e.g. `["utun4"]`). Required unless `autodetect` is set. Run `dezhban detect-vpn` to find them. |
| `vpn.endpoints` | `[]string` | `[]` | VPN server addresses reachable on the physical interface — kept open so the tunnel can stay up and reconnect. Each entry may be an **IP or a hostname** (hostnames are re-resolved at runtime). Required when `enabled`, unless `autoDiscoverEndpoints` is set. |
| `vpn.autodetect` | bool | `false` | Discover the tunnel interface(s) at runtime via `netdetect`. Explicit `tunnelInterfaces` always win. |
| `vpn.autoDiscoverEndpoints` | bool | `false` | Continuously learn the live VPN server IP from the active socket (**macOS only**; ignored elsewhere, where hostnames/IPs are used). Lets a rotating-pool VPN (NordVPN/ProtonVPN/…) run with no hand-typed endpoint. |
| `vpn.endpointRefresh` | duration | `5m` | How often hostnames are re-resolved and live discovery re-run. |
| `vpn.tunnelWatch` | duration | `1s` | How often the tunnel interface(s) are sampled for up/down. In guard mode this powers logging/`monitor`; in legacy (direct) mode a drop blocks immediately (kill switch). |

### Validation rules (enforced by `validate` and at load)

- `pollInterval` > 0
- `hysteresis` ≥ 1
- at least one `providers` entry
- every `blockedCountries` code is 2 letters
- when `vpn.enabled`: `tunnelInterfaces` non-empty **or** `autodetect` true
- when `vpn.enabled`: at least one `endpoints` entry (IP **or** hostname) **or** `autoDiscoverEndpoints` true

### Getting `vpn.endpoints` right

A wrong or tunnel-internal endpoint is the #1 lockout cause — see
[troubleshooting.md](troubleshooting.md). Endpoints may now be **hostnames** (handy
for self-hosted WireGuard/V2Ray with a stable name) and, on macOS,
`autoDiscoverEndpoints` learns the live server IP so you need not type one at all.
Verify what will actually be opened before enabling:

```sh
dezhban monitor --config <config>           # live: IP, country, tunnels, resolved endpoints, verdict
dezhban doctor --config <config>            # flags endpoints inside a tunnel subnet
dezhban doctor --discover --config <config> # macOS: print the VPN's real server IP
```

### Testing without a real sanctioned IP

No root, no firewall changes:

```sh
dezhban monitor --simulate-country IR --config <config>   # force the verdict to BLOCK from anywhere
dezhban run --dry-run --simulate-country IR --config <config>
```

A real run (needs root — it drives the firewall) can be driven with simulated
inputs to watch enforcement actually fire:

```sh
sudo dezhban run --simulate-country IR --config <config>       # drive a real block from anywhere
sudo dezhban run --simulate-tunnel-down 8s --config <config>   # exercise the failover path (legacy kill switch)
```

## Sample configs

- [`configs/dezhban.example.json`](../configs/dezhban.example.json) — reference, legacy (non-VPN) mode.
- [`configs/dezhban.vpn-guard.json`](../configs/dezhban.vpn-guard.json) — VPN guard mode.
- [`configs/dezhban.dev.json`](../configs/dezhban.dev.json) — debug logging, fast poll, no blocking; for local dry-runs.
- `configs/dezhban.local.json` — your private config (git-ignored; may hold a real endpoint IP).
