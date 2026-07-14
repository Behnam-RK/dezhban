# Configuration reference

dezhban reads a JSON config. Durations are strings (Go syntax, e.g. `"30s"`,
`"5m"`). A missing `--config` loads built-in defaults. Validate any file without
running it:

```sh
dezhban validate --config path/to/config.json
```

## Where the config lives

`--config` is optional. When omitted, dezhban resolves the path in this order:

1. the `--config` flag, if given
2. `$DEZHBAN_CONFIG`
3. the canonical **system path** — `/etc/dezhban/dezhban.json` (unix),
   `%ProgramData%\dezhban\dezhban.json` (windows) — if the file exists
4. built-in defaults (no file)

The system path is deliberate: both the root daemon (`sudo dezhban run`) and your
unprivileged inspect commands (`dezhban monitor`/`validate`) resolve the *same*
file. `dezhban config path` prints whichever won.

Author it without editing JSON:

```sh
sudo dezhban setup                              # interactive wizard
sudo dezhban config set blockedCountries IR,RU  # or targeted edits
dezhban config show                             # print the effective config
```

Writing to the system path needs root; on a permission error the CLI prints a
`sudo` hint. See [usage.md](usage.md#create--manage-the-config) for the full
command set.

## Fields

| Field | Type | Default | Notes |
|---|---|---|---|
| `pollInterval` | duration string | `"30s"` | How often the public IP / country is checked. Must be > 0. |
| `blockedCountries` | `[]string` | `[]` | ISO-3166 alpha-2 codes (e.g. `"RU"`, `"IR"`). Upper-cased on load; each must be exactly 2 letters. A match triggers a block. |
| `failClosed` | bool | `true` | **Fallback (non-VPN) mode:** when the country can't be determined, block anyway (security-first); the allowlist stays open so recovery still works. **In VPN guard mode this is a no-op** — the standing guard is itself the fail-closed block for physical leaks, so an undeterminable country *holds* the current posture rather than escalating to FULL BLOCK (escalating would cut the tunnel's own egress and livelock the reconnect). Only a *successful* reading of a blocked country triggers FULL BLOCK. |
| `hysteresis` | int | `3` | Consecutive agreeing readings required before toggling block/allow. Must be ≥ 1. Damps flapping. |
| `providers` | `[]string` | 3 geo-IP URLs | Geo-location endpoints, tried for redundancy. At least one required. |
| `allowlist.dns` | `[]string` | `[]` | Resolver IPs kept reachable while blocking, so hostname re-resolution works. |
| `allowlist.hosts` | `[]string` | `[]` | Extra host IPs always allowed. Provider IPs are added automatically at block time. |
| `providerQuorum` | bool | `false` | Require a majority of providers to agree on the country before acting. |
| `logLevel` | string | `"info"` | One of `debug`, `info`, `warn`, `error`. The `-v`/`--verbose` flag overrides this to `debug`. |
| `vpn` | object | disabled | VPN interface-guard config — see below. |
| `control` | object | enabled | Control socket — the reason routine ops don't ask for a password. See below. |

## `control` block

The daemon listens on a unix socket so `block`, `unblock` and `switch` reach the
running daemon instead of re-elevating to root every time. **This is why you are
not prompted for a password during normal use.** The CLI and the menubar app both
go through it; with no daemon listening they fall back to acting on the firewall
directly, which does need root.

`dezhban status` prints a `daemon control:` line telling you exactly which of the
two you are in.

| Field | Type | Default | Notes |
|---|---|---|---|
| `control.enabled` | bool | `true` | Turn the socket off to require root for every operation. |
| `control.socket` | string | `<state dir>/control.sock` | Socket path. Defaults to `/var/db/dezhban/control.sock` (unix). Its **parent directory is part of the trust boundary** — whoever may unlink the socket may bind their own in its place — so the daemon refuses to start the control feature if that directory is group/world-writable without the sticky bit, or is owned by neither root nor the daemon. Keep it in a root-owned directory. |
| `control.group` | string | `"admin"` on macOS, `""` elsewhere | The unix group allowed to drive the daemon. The socket is root-owned, mode `0660`, group-owned by this group. `""` means root-only (`0600`) — the passwordless path is off. |
| `control.allowSwitchOps` | bool | `true` | Whether opening/cancelling a **switch window** may go over the socket. This is the one op that *relaxes* the guard, so it has its own switch: set it `false` to force `switch` back to root-only (`sudo dezhban switch`). |

**What the trade actually is.** There are no peer credentials in the protocol
(dezhban is stdlib-only), so filesystem permissions are the whole authorization:
any process running as a member of `control.group` can issue these commands without
a password. On macOS `admin` is the group every administrator account is already in
— the same humans macOS would have prompted for a password anyway. What they can do
is bounded: `block`/`unblock` only move between the daemon's own fail-closed
postures, and `switch` is bounded by the same 5-minute cap as ever. `panic` is not
on the socket at all, so the lockout escape hatch never depends on a live daemon.

Tighten it if you want to: `control.group: ""` (root-only), or
`control.allowSwitchOps: false` (keep passwordless block/unblock, but make relaxing
the guard require root), or `control.enabled: false` (password for everything).

Off macOS the group defaults to empty — `wheel`, `sudo` and `adm` mean different
things across distros, and guessing wrong would hand the socket to the wrong people.
Name a group explicitly to opt in.

## `vpn` block

For hosts behind a full-tunnel VPN, the guard cuts the **physical** interface
while keeping the **tunnel** open, instead of the destination-IP allowlist (which
is meaningless under a tunnel). Opt-in — a misconfigured guard can lock you out.

| Field | Type | Default | Notes |
|---|---|---|---|
| `vpn.enabled` | bool | `false` | Turns on guard mode. |
| `vpn.tunnelInterfaces` | `[]string` | `[]` | Tunnel interface names (e.g. `["utun4"]`). Required unless `autodetect` is set. Run `dezhban detect-vpn` to find them. |
| `vpn.endpoints` | `[]string` | `[]` | VPN server addresses reachable on the physical interface — kept open so the tunnel can stay up and reconnect. Each entry may be an **IP or a hostname** (hostnames are re-resolved at runtime). Required when `enabled`, unless `autoDiscoverEndpoints` is set. |
| `vpn.autodetect` | bool | `false` | Discover the tunnel interface(s) at runtime via `netdetect`, growing/pruning the guard set as VPNs come and go. Explicit `tunnelInterfaces` always win (and are pinned — never pruned). **Implied `true`** when the guard is enabled with no `tunnelInterfaces`, so a config never pins a `utunN` that renumbers across reconnects. |
| `vpn.profiles` | `[]object` | `[]` | Named VPNs whose server endpoints are always kept reachable (the guard passes the **union** of all profiles' endpoints), so switching between known VPNs needs no reconfiguration. Each: `{name, endpoints[], ifaceHint?}`. `ifaceHint` is display-only. Manage with `dezhban vpn add/remove/import`, not `config set`. |
| `vpn.switchWindow` | duration | `2m` | Default length of a `dezhban switch` window — a bounded, explicitly-triggered relaxation for connecting a brand-new VPN whose server isn't known yet. Validated to `[10s, advanced.switchWindowMax]`. |
| `vpn.autoDiscoverEndpoints` | bool | `false` | Continuously learn the live VPN server IP from the active socket (**macOS only**; ignored elsewhere, where hostnames/IPs are used). Lets a rotating-pool VPN (NordVPN/ProtonVPN/…) run with no hand-typed endpoint. |
| `vpn.allowPhysicalDNS` | bool | `false` | Open plain DNS (port 53) egress on the **physical** link in GUARD and VPN FULL BLOCK, so a VPN client can re-resolve its server hostname and reconnect while the tunnel is down. Off by default — the residual leak is DNS-query metadata (which resolver you query, and that you're reconnecting) on the physical path; your actual traffic stays blocked. Recommended when any endpoint is a hostname. |
| `vpn.endpointRefresh` | duration | `5m` | How often hostnames are re-resolved and live discovery re-run. |
| `vpn.tunnelWatch` | duration | `1s` | How often the tunnel interface(s) are sampled for up/down. In guard mode this powers logging/`monitor`; in legacy (direct) mode a drop blocks immediately (kill switch). |

### Validation rules (enforced by `validate` and at load)

- `pollInterval` > 0
- `hysteresis` ≥ 1
- at least one `providers` entry
- every `blockedCountries` code is 2 letters
- when `vpn.enabled`: at least one endpoint across the **union** of `vpn.endpoints`, `vpn.profiles[].endpoints`, **or** `autoDiscoverEndpoints` (tunnel interfaces need not be set — autodetect is implied)
- `vpn.profiles`: unique names (`[A-Za-z0-9._-]`, ≤64), each with ≥1 valid endpoint
- `vpn.switchWindow` within `[10s, advanced.switchWindowMax]`

### Getting `vpn.endpoints` right

A wrong or tunnel-internal endpoint is the #1 lockout cause — see
[troubleshooting.md](troubleshooting.md). Endpoints may now be **hostnames** (handy
for self-hosted WireGuard/V2Ray with a stable name) and, on macOS,
`autoDiscoverEndpoints` learns the live server IP so you need not type one at all.
If your endpoints are hostnames, set `vpn.allowPhysicalDNS: true` so the client
can re-resolve them on reconnect while the tunnel is down (otherwise a
hostname-only config can wedge: the tunnel drops, DNS is cut, and the client
can't find its server to reconnect).

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

## VPN profiles and switching between many VPNs

The target workflow — one-time setup, then connect to **any** VPN and switch
freely — is served by two mechanisms:

- **Profiles** keep every known VPN's server reachable at once (the guard passes
  the union), so disconnecting VPN A and connecting VPN B just works with no
  reconfiguration. Add them from the client's own config file:

  ```sh
  dezhban vpn add proton --endpoint nl-01.protonvpn.net
  dezhban vpn import ~/wg0.conf            # WireGuard .conf / OpenVPN .ovpn / V2Ray JSON
  dezhban vpn list                          # profiles + learned endpoints + active state
  ```

- **Switch window** handles a *brand-new* VPN whose server isn't known yet. The
  guard is blocking everything, so its handshake to an unknown IP would be cut —
  open a bounded window, connect, and dezhban learns and pins the server, then
  snaps shut:

  ```sh
  sudo dezhban switch          # opens a ~2m window, watches for the new tunnel + server
  # …connect your VPN in its app…
  sudo dezhban vpn promote <name>   # make the learned endpoint permanent (see: dezhban vpn list)
  ```

  See [modes.md](modes.md) for the window's exact posture and the leak trade-off.

Learned endpoints live in a daemon-owned file (`/var/db/dezhban/learned.json` on
unix, `%ProgramData%\dezhban\learned.json` on Windows) — separate from your
config so the daemon never rewrites user intent. `dezhban vpn forget` clears them.

## Advanced tunables (`vpn.advanced`)

An optional block for behaviors that are otherwise recommended defaults. Omit it
entirely to keep the defaults; set only the knobs you need.

| Field | Default | What it controls |
|---|---|---|
| `switchWindowMax` | `5m` | Hard cap on any switch window (incl. `--for`). |
| `commandFreshness` | `30s` | How recent a control command must be to be acted on (replay guard). |
| `windowDiscoveryInterval` | `2s` | How often the new server is looked for while a window is open. |
| `tunnelPruneAfter` | `60s` | How long a dynamically-detected tunnel must be gone before it's dropped. |
| `learnedEndpointTTL` | `720h` | How long an unused learned endpoint is kept. |
| `learnedMaxPerProfile` | `16` | Cap on learned endpoints per profile (LRU). |
| `promoteAfterRefreshes` | `3` | Consecutive sightings before a discovered endpoint is learned under normal guard. |
| `endpointWarnThreshold` | `256` | Union size at which `doctor` warns about rule-list bloat. |
| `windowProtocols` / `windowPorts` | (empty = allow all) | Restrict the switch window to these protocols/ports instead of all outbound — only useful when every VPN you switch to uses a fixed port set (e.g. WireGuard on 51820). |

## Sample configs

- [`configs/dezhban.example.json`](../configs/dezhban.example.json) — reference, legacy (non-VPN) mode.
- [`configs/dezhban.vpn-guard.json`](../configs/dezhban.vpn-guard.json) — VPN guard mode.
- [`configs/dezhban.profiles.json`](../configs/dezhban.profiles.json) — autodetect + multiple VPN profiles + switch window.
- [`configs/dezhban.dev.json`](../configs/dezhban.dev.json) — debug logging, fast poll, no blocking; for local dry-runs.
- `configs/dezhban.local.json` — your private config (git-ignored; may hold a real endpoint IP).
