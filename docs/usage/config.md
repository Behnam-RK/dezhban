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
sudo dezhban config reset vpn.switchWindow      # back to the shipped default (--all: every tunable)
dezhban config show                             # print the effective config
```

Writing to the system path needs root; on a permission error the CLI prints a
`sudo` hint. See [cli.md](cli.md#create--manage-the-config) for the full
command set.

## Fields

| Field | Type | Default | Notes |
|---|---|---|---|
| `pollInterval` | duration string | `"15s"` | How often the public IP / country is checked. Must be > 0. With the default `hysteresis: 2`, a forbidden exit is confirmed in ~30s worst-case; the default provider order keeps this volume on unmetered endpoints. |
| `blockedCountries` | `[]string` | `["IR","RU","KP"]` | ISO-3166 alpha-2 codes (e.g. `"RU"`, `"IR"`). Upper-cased on load; each must be exactly 2 letters. A match triggers a block. **The default applies only when the key is absent** — an explicit `[]` is a deliberate "block nothing" and is never overridden (2026-07-22 defaults review). |
| `hysteresis` | int | `2` | Consecutive agreeing readings required before toggling block/allow. Must be ≥ 1. Damps flapping. A *failed* lookup is neutral — it neither commits a pending flip nor cancels one. |
| `providers` | `[]string` | 8 geo-IP URLs | Geo-location endpoints, tried **in order** for redundancy — the first reachable one absorbs nearly all poll traffic, so the default list is ordered by rate-limit headroom: `get.geojs.io`, `api.country.is`, `ip-api.com`, `ipwho.is`, `freeipapi.com`, `ifconfig.co`, `ipinfo.io`, `ipapi.co`. Only these known URLs are usable (each needs a response parser); unknown URLs are skipped with a warning. At least one required. |
| `providerQuorum` | bool | `false` | Require a majority of providers to agree on the country before acting. |
| `logLevel` | string | `"info"` | One of `debug`, `info`, `warn`, `error`. The `-v`/`--verbose` flag overrides this to `debug`. |
| `vpn` | object | — | VPN interface-guard config — see below. |
| `control` | object | enabled | Control socket — the reason routine ops don't ask for a password. See below. |

A `block --force` pins the resolved provider IPs at block time; the long-running
`run` loop re-resolves them live, but a one-shot `block --force` does not. A
provider behind a rotating CDN can later resolve to a different IP than the one
pinned, breaking recovery until the next `run` refresh — prefer providers with
stable IPs for hosts that rely on one-shot `block`.

### Retired keys

These are still **parsed without error**, have **no effect**, and are **reported**
by `dezhban validate` and once at daemon start. They are never written back when
dezhban saves your config. Nothing you have to do — but nothing they do, either.

| Key | Why it's gone |
|---|---|
| `vpn.enabled` | There is one enforcement model now. Its second job — the safety opt-in that stopped a misconfigured guard locking a host out — is done properly by the STANDBY posture, which installs no rules until a tunnel is observed up. [ADR-0001](../adr/0001-single-guard-mode.md), [ADR-0002](../adr/0002-standby-no-tunnel-posture.md) |
| `failClosed` | Belonged to the retired country-blocklist model, where the firewall was open at rest and an undeterminable country was the only reason to cut. Under the guard, the standing rules *are* the fail-closed block, so an unknown country holds the posture instead of escalating. [ADR-0001](../adr/0001-single-guard-mode.md) |
| `allowlist` | Belonged to the same model. A VPN posture opens the tunnel **endpoint**, not a destination allowlist — against a tunnel's encrypted outer packets a dst-IP list means nothing. Geo-provider IPs are still resolved automatically where they're needed. [ADR-0001](../adr/0001-single-guard-mode.md) |

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
| `control.allowPauseOps` | bool | `true` | Whether opening/ending a **pause** may go over the socket, independently of `allowSwitchOps` — turning off passwordless switching does not turn off passwordless pausing, or vice versa. Set `false` to force `pause`/`resume` back to root-only. |

**What the trade actually is, and how to tighten it:**
[architecture.md § Control channels](../contribute/architecture.md#control-channels).
Short version: `control.group: ""` goes root-only, `control.allowSwitchOps: false`
keeps passwordless block/unblock but forces the guard-relaxing op back to root,
and `control.enabled: false` requires a password for everything.

Off macOS the group defaults to empty — `wheel`, `sudo` and `adm` mean different
things across distros, and guessing wrong would hand the socket to the wrong people.
Name a group explicitly to opt in.

## `vpn` block

The guard cuts the **physical** interface while keeping the **tunnel** open. This
is the only enforcement model dezhban has; there is no flag to turn it on,
because with no tunnel configured or observed the daemon simply rests in STANDBY
and installs no rules at all.

| Field | Type | Default | Notes |
|---|---|---|---|
| `vpn.tunnelInterfaces` | `[]string` | `[]` | Tunnel interface names (e.g. `["utun4"]`). Leave empty to let `autodetect` find them. Run `dezhban detect-vpn` to see them. |
| `vpn.endpoints` | `[]string` | `[]` | VPN server addresses reachable on the physical interface — kept open so the tunnel can stay up and reconnect. Each entry may be an **IP or a hostname** (hostnames are re-resolved at runtime). Not required to load — a config with none is valid and rests in STANDBY — but the guard will not arm until it knows at least one, from here, a profile, or `autoDiscoverEndpoints`. |
| `vpn.autodetect` | bool | `true` | Discover the tunnel interface(s) at runtime via `netdetect`, growing/pruning the guard set as VPNs come and go. Explicit `tunnelInterfaces` always win (and are pinned — never pruned). **On by default** (2026-07-22 defaults review) — set `false` explicitly to rely solely on `tunnelInterfaces`. |
| `vpn.profiles` | `[]object` | `[]` | Named VPNs whose server endpoints are always kept reachable (the guard passes the **union** of all profiles' endpoints), so switching between known VPNs needs no reconfiguration. Each: `{name, endpoints[], ifaceHint?}`. `ifaceHint` is display-only. Manage with `dezhban vpn add/remove/import`, not `config set`. |
| `vpn.switchWindow` | duration | `5s` | Default length of a `dezhban switch` window — a bounded, explicitly-triggered relaxation for connecting a brand-new VPN whose server isn't known yet (it closes early on a confirmed good exit, so the duration only bounds the slow case; pass `--for` for a longer one-off). Set `"0"` to disable manual switch windows entirely — a *tightening*, at the cost of having to add a new VPN's server to `vpn.endpoints` by hand. Independent of `reconnectWindow`. No floor; validated to `(0, advanced.switchWindowMax]`, or exactly `"0"`. |
| `vpn.reconnectWindow` | duration | `30s` | Length of the **automatic reconnect window**: a tunnel drop from healthy GUARD opens a switch-window relaxation for this long, so the VPN client can redial *any* server — including one dezhban has never seen — with zero interaction. Closes early (and learns the new endpoint) the moment a good exit is confirmed; on expiry the guard fail-closes and stays closed. Set `"0"` to disable and get the strict zero-relaxation behavior. No floor; validated to `(0, advanced.reconnectWindowMax]` — a cap kept **independent** of `advanced.switchWindowMax` so one trigger's budget can never silently truncate the other's. See [modes.md](../concepts/modes.md#automatic-reconnect-window). |
| `vpn.pauseMax` | duration | `30m` | Cap on a `dezhban pause` — a deliberate, timed drop to the real ISP IP (e.g. to reach a domestic-only service), sharing the switch-window machinery as a **third** trigger with its own cap, never shared with `switchWindowMax`/`reconnectWindowMax`. The requested duration comes from the `pause` call itself (`dezhban pause 15m`), not a separate default key. Set `"0"` to disable pausing entirely. See [modes.md](../concepts/modes.md#pause--deliberately-using-your-real-ip). |
| `vpn.armAtBoot` | bool | `true` | Arm the guard directly at startup even when no tunnel interface is present yet — instead of waiting in `autoArm`'s standby — **provided** a tunnel has been observed up at least once on this host and an endpoint is known. Closes the boot race where this daemon starts before the VPN client brings its interface up: without it, every such boot opens the network until the VPN connects. A fresh install, or a host whose VPN has never connected, still starts in STANDBY regardless — this cannot turn a misconfiguration into a lockout. See [ADR-0008](../adr/0008-arm-at-boot.md). |
| `vpn.autoDiscoverEndpoints` | bool | `true` | Continuously learn the live VPN server IP from the active socket (**macOS only**; ignored elsewhere, where hostnames/IPs are used — a global default-true still emits a startup warning there since the setting does nothing). Lets a rotating-pool VPN (NordVPN/ProtonVPN/…) run with no hand-typed endpoint. **On by default** (2026-07-22 defaults review); set `false` explicitly to require hand-typed endpoints/hostnames. |
| `vpn.allowPhysicalDNS` | bool | `true` | Open plain DNS (port 53) egress on the **physical** link in GUARD and VPN FULL BLOCK, so a VPN client can re-resolve its server hostname and reconnect while the tunnel is down. **On by default** (2026-07 defaults review: reconnectability wins for this project's users); set `false` to close the residual leak — DNS-query metadata (which resolver you query, and that you're reconnecting) on the physical path. Your actual traffic stays blocked either way. |
| `vpn.allowLocalNetwork` | bool | `true` | Keep **local** destinations reachable while the guard is armed: printers, NAS, the router's admin page, AirPlay/Chromecast, local dev servers, SSH to another machine on the desk. Without it, arming the guard makes every one of them unreachable with no way to get them back. **Destination-scoped** (RFC1918 + CGNAT + link-local + IPv6 ULA + multicast — see [modes.md](../concepts/modes.md#local-network-access)), never interface-scoped, so it cannot become an internet path: packets to public addresses stay blocked whatever the next hop is. Costs nothing against the threat model — this traffic never leaves the building, so it cannot expose your country to a foreign service. **The one real cost:** on an untrusted network (café, hotel) it lets you reach, and be reached by, the other devices there. Set `false` to close that. |
| `vpn.autoArm` | bool | `true` | Start PASSIVE (posture `standby`, nothing enforced) when no tunnel interface is present, and arm the guard automatically the moment a VPN connects (endpoints are re-checked at arm time; arming is held while none are known). Never disarms on tunnel loss — a drop is exactly the leak the kill switch exists for; an explicit `unblock` with the tunnel down returns to standby. **On by default** (2026-07 defaults review: a guard armed with no VPN is a mystery blackout for new users); set `false` for the stricter armed-from-startup posture. |
| `vpn.endpointRefresh` | duration | `1m` | How often hostnames are re-resolved and live discovery re-run. Local work only (DNS + a socket scan), so the fast cadence costs nothing against geo-API quotas and promotes roamed-to servers to learned within ~3 minutes. |
| `vpn.endpointGrace` | duration | `15m` | How long an autodiscovered endpoint stays in the allowed set after a refresh stops reporting it. Discovery can only see an endpoint while its socket lives, and the socket dies with the tunnel — the grace is the window in which a dropped VPN can redial the *same* server without a switch window. A genuinely rotated-away server ages out once unseen past the grace. |
| `vpn.tunnelWatch` | duration | `1s` | How often the tunnel interface(s) are sampled for up/down. Drives the tunnel-down edge that arms the guard out of STANDBY and opens the automatic reconnect window, plus logging and `monitor`. |

### Validation rules (enforced by `validate` and at load)

- `pollInterval` > 0
- `hysteresis` ≥ 1
- at least one `providers` entry
- every `blockedCountries` code is 2 letters
- `vpn.profiles`: unique names (`[A-Za-z0-9._-]`, ≤64), each with ≥1 valid endpoint
- `vpn.switchWindow` within `(0, advanced.switchWindowMax]` (no floor), or exactly `"0"` (disabled)
- `vpn.reconnectWindow` within `(0, advanced.reconnectWindowMax]` (no floor, independent cap), or exactly `"0"` (disabled)

**Endpoints are deliberately *not* a load-time requirement.** They used to be,
because `vpn.enabled: true` was a promise to enforce and a guard that can never
learn a server address can never let the tunnel reconnect. With one mode, every
config is a guard config, so rejecting here would make a fresh install — which
legitimately knows no endpoints yet — fail to load at all. The check moved to
where it can tell the difference: the runner refuses to *arm* a guard that has
tunnels but no endpoints (that specific pair is the unrecoverable blackout), and
`dezhban doctor` reports the same condition as a lockout risk before you hit it.
Knowing no endpoints *and* no tunnel is simply STANDBY, which is safe.

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
sudo dezhban run --simulate-tunnel-down 8s --config <config>   # exercise the tunnel-drop path (cut + reconnect window)
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
  sudo dezhban switch          # opens a window (5s default), watches for the new tunnel + server
  # …connect your VPN in its app…
  sudo dezhban vpn promote <name>   # make the learned endpoint permanent (see: dezhban vpn list)
  ```

  See [modes.md](../concepts/modes.md#switching-between-vpns) for the window's
  exact posture and the leak trade-off.

Learned endpoints live in a daemon-owned file (`/var/db/dezhban/learned.json` on
unix, `%ProgramData%\dezhban\learned.json` on Windows) — separate from your
config so the daemon never rewrites user intent. `dezhban vpn forget` clears them.

## Advanced tunables (`vpn.advanced`)

An optional block for behaviors that are otherwise recommended defaults. Omit it
entirely to keep the defaults; set only the knobs you need.

| Field | Default | What it controls |
|---|---|---|
| `switchWindowMax` | `3m` | Hard cap on any MANUAL switch window (incl. `--for`). |
| `reconnectWindowMax` | `10m` | Hard cap on the AUTOMATIC reconnect window — kept independent of `switchWindowMax` so one trigger's budget never truncates the other's. |
| `commandFreshness` | `30s` | How recent a control command must be to be acted on (replay guard). |
| `windowDiscoveryInterval` | `1s` | How often the new server is looked for while a window is open. |
| `tunnelPruneAfter` | `60s` | How long a dynamically-detected tunnel must be gone before it's dropped. |
| `learnedEndpointTTL` | `720h` | How long an unused learned endpoint is kept. |
| `learnedMaxPerProfile` | `16` | Cap on learned endpoints per profile (LRU). |
| `promoteAfterRefreshes` | `3` | Consecutive sightings before a discovered endpoint is learned under normal guard. |
| `reconnectMinUptime` | `15s` | Anti-flap gate on the automatic reconnect window: an auto-window opens only if the tunnel had been up at least this long (or a good exit was confirmed during that uptime). The first drop after startup is exempt — uptime before the daemon started is unknowable. `"0"` disables the gate. |
| `endpointWarnThreshold` | `256` | Union size at which `doctor` warns about rule-list bloat. |
| `windowProtocols` / `windowPorts` | (empty = allow all) | Restrict the switch window to these protocols/ports instead of all outbound — only useful when every VPN you switch to uses a fixed port set (e.g. WireGuard on 51820). |

## Sample configs

- [`configs/dezhban.example.json`](../../configs/dezhban.example.json) — reference: fully automatic (autodetect + endpoint discovery).
- [`configs/dezhban.vpn-guard.json`](../../configs/dezhban.vpn-guard.json) — explicitly pinned tunnel interface and endpoints.
- [`configs/dezhban.profiles.json`](../../configs/dezhban.profiles.json) — autodetect + multiple VPN profiles + switch window.
- [`configs/dezhban.dev.json`](../../configs/dezhban.dev.json) — debug logging, fast poll, no blocking; for local dry-runs.
- `configs/dezhban.local.json` — your private config (git-ignored; may hold a real endpoint IP).
