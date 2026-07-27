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

### Keys that apply live, and keys that need a restart

`dezhban config set` (and the macOS app's Settings pane) tells a running daemon to
re-read this file, so most edits take effect immediately. Some cannot: anything the
daemon *built* at startup — the logger, the geo monitor's provider list, the
control socket, the tunnel watcher, the endpoint resolver — is still in force at
its old value until a restart.

Nothing needs to be memorised, because the daemon reports which is which by name
for the keys you actually changed:

```
$ sudo dezhban config set pollInterval 30s logLevel debug
Saved and applied: pollInterval
Restart dezhban to apply: logLevel
```

That report is authoritative — it comes from the daemon, which is the only thing
that knows what it built — so treat it, not this page, as the answer. Nothing is
ever reported as applied while the old value is still being enforced; that would be
the same failure as silently discarding a setting.

Two consequences worth knowing:

- Re-enabling a disabled window (`vpn.switchWindow`, `vpn.pauseMax`) applies live,
  in both directions, including on a daemon that started with everything disabled.
- Changing `blockedCountries` or `hysteresis` restarts the agreement streak, since
  readings counted under the old list say nothing about the new one. Any other edit
  leaves an in-progress posture change counting undisturbed.

### Keys that do nothing

Any key the schema does not define — a typo, a key from an older version, a key
renamed by an upgrade — is **parsed without error**, has **no effect**, and is
**reported** by `dezhban validate` and once at daemon start. Renamed keys name
their replacement:

```
  note: "vpn.reconnectWindow" has no effect.
        renamed to vpn.redialWindow; the old name has no effect
```

This is deliberately a report and not a startup failure. Refusing to run would
leave the machine with no kill switch at all, which is worse than running with
one setting at its default — but silence would be worse still, because a
disabled window quietly returning to its default re-enables a relaxation of the
guard that someone deliberately turned off.

### Keys that do something, under the wrong name

One category is **not** inert, and is reported differently for that reason: a
key whose spelling differs from the real one only by **letter case**. JSON key
matching ignores case, so these are honored in full —

```
  note: "vpn.pausemax" is not the schema's spelling, but it TOOK EFFECT.
        the schema spells it "vpn.pauseMax"; JSON key matching ignores case, so this
        value IS in effect — rename it to match so the file says what it does
```

— and the report says so, because telling you a live setting has no effect is
the same failure as silently discarding one, pointed the other way: you would
stop looking while a 2-hour pause window was in force. Fix the casing so the
file says what it does; nothing changes about enforcement when you do.

The key on the left is spelled the way **your file** spells it, so you can find
the line; the one the note tells you to change *to* is always the schema's own
spelling at **every** level. Miscase a whole block and you get one note per
level — `"VPN"` → `"vpn"`, then `"VPN.Profiles"` → `"vpn.profiles"` — rather
than a single hybrid that matches neither.

If **both** spellings are present, which one wins is document order (the
decoder assigns each key as it reads it), which is why the report flags the
miscased one rather than trying to pick a winner. Delete it.

#### Renamed keys

| Old name | New name |
|---|---|
| `vpn.reconnectWindow` | `vpn.redialWindow` |
| `vpn.advanced.reconnectWindowMax` | `vpn.advanced.redialWindowMax` |
| `vpn.advanced.reconnectMinUptime` | `vpn.advanced.redialMinUptime` |
| `vpn.profiles[].ifaceHint` | `vpn.profiles[].tunnelHint` |

`vpn.autodetect` → `vpn.autoDetect` is **not** in this table: that rename
changed only casing, so the old spelling still takes effect and is reported as
a misspelling (above) rather than as a name with no effect.

#### Retired keys

These are recognised, deliberately inert, and never written back when dezhban
saves your config. Nothing you have to do — but nothing they do, either.

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
| `control.allowConfigOps` | bool | `true` | Whether **config changes** may go over the socket. Unlike every other op this changes state that outlives the daemon, so group membership is not enough on its own: the client must additionally prove it holds the enrolled **control token** (`dezhban token`). Both gates must pass — setting this `false` refuses config writes even from a client holding a valid token, forcing settings changes back to `sudo dezhban config set`. |

**What the trade actually is, and how to tighten it:**
[architecture.md § Control channels](../contribute/architecture.md#control-channels).
Short version: `control.group: ""` goes root-only, `control.allowSwitchOps: false`
keeps passwordless block/unblock but forces the guard-relaxing op back to root,
`control.allowConfigOps: false` forces settings changes back to root, and
`control.enabled: false` requires a password for everything.

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
| `vpn.tunnelInterfaces` | `[]string` | `[]` | Tunnel interface names (e.g. `["utun4"]`). Leave empty to let `autoDetect` find them. Run `dezhban detect-vpn` to see them. |
| `vpn.endpoints` | `[]string` | `[]` | VPN server addresses reachable on the physical interface — kept open so the tunnel can stay up and redial. Each entry may be an **IP or a hostname** (hostnames are re-resolved at runtime). Not required to load — a config with none is valid and rests in STANDBY — but the guard will not arm until it knows at least one, from here, a profile, or `autoDiscoverEndpoints`. |
| `vpn.autoDetect` | bool | `true` | Discover the tunnel interface(s) at runtime via `netdetect`, growing/pruning the guard set as VPNs come and go. Explicit `tunnelInterfaces` always win (and are pinned — never pruned). **On by default** (2026-07-22 defaults review) — set `false` explicitly to rely solely on `tunnelInterfaces`. |
| `vpn.profiles` | `[]object` | `[]` | Named VPNs whose server endpoints are always kept reachable (the guard passes the **union** of all profiles' endpoints), so switching between known VPNs needs no reconfiguration. Each: `{name, endpoints[], tunnelHint?}`. `tunnelHint` is display-only. Manage with `dezhban vpn add/remove/import`, not `config set`. |
| `vpn.switchWindow` | duration | `5s` | Default length of a `dezhban switch` window — a bounded, explicitly-triggered relaxation for connecting a brand-new VPN whose server isn't known yet (it closes early on a confirmed good exit, so the duration only bounds the slow case; pass `--for` for a longer one-off). Set `"0"` to disable manual switch windows entirely — a *tightening*, at the cost of having to add a new VPN's server to `vpn.endpoints` by hand. Independent of `redialWindow`. No floor; validated to `(0, advanced.switchWindowMax]`, or exactly `"0"`. |
| `vpn.redialWindow` | duration | `30s` | Length of the **automatic redial window**: a tunnel drop from healthy GUARD opens a switch-window relaxation for this long, so the VPN client can redial *any* server — including one dezhban has never seen — with zero interaction. Closes early (and learns the new endpoint) the moment a good exit is confirmed; on expiry the guard fail-closes and stays closed. Set `"0"` to disable and get the strict zero-relaxation behavior. No floor; validated to `(0, advanced.redialWindowMax]` — a cap kept **independent** of `advanced.switchWindowMax` so one trigger's budget can never silently truncate the other's. See [modes.md](../concepts/modes.md#automatic-redial-window). |
| `vpn.pauseMax` | duration | `30m` | Cap on a `dezhban pause` — a deliberate, timed drop to the real ISP IP (e.g. to reach a domestic-only service), sharing the switch-window machinery as a **third** trigger with its own cap, never shared with `switchWindowMax`/`redialWindowMax`. The requested duration comes from the `pause` call itself (`dezhban pause 15m`), not a separate default key. Set `"0"` to disable pausing entirely. See [modes.md](../concepts/modes.md#pause--deliberately-using-your-real-ip). |
| `vpn.armAtBoot` | bool | `true` | Arm the guard directly at startup even when no tunnel interface is present yet — instead of waiting in `autoArm`'s standby — **provided** a tunnel has been observed up at least once on this host and an endpoint is known. Closes the boot race where this daemon starts before the VPN client brings its interface up: without it, every such boot opens the network until the VPN connects. A fresh install, or a host whose VPN has never connected, still starts in STANDBY regardless — this cannot turn a misconfiguration into a lockout. See [ADR-0008](../adr/0008-arm-at-boot.md). |
| `vpn.autoDiscoverEndpoints` | bool | `true` | Continuously learn the live VPN server IP from the active socket (**macOS only**; ignored elsewhere, where hostnames/IPs are used — a global default-true still emits a startup warning there since the setting does nothing). Lets a rotating-pool VPN (NordVPN/ProtonVPN/…) run with no hand-typed endpoint. **On by default** (2026-07-22 defaults review); set `false` explicitly to require hand-typed endpoints/hostnames. |
| `vpn.allowPhysicalDNS` | bool | `true` | Open plain DNS (port 53) egress on the **physical** link in GUARD and VPN FULL BLOCK, so a VPN client can re-resolve its server hostname and redial while the tunnel is down. **On by default** (2026-07 defaults review: redialability wins for this project's users); set `false` to close the residual leak — DNS-query metadata (which resolver you query, and that you're redialing) on the physical path. Your actual traffic stays blocked either way. |
| `vpn.allowLocalNetwork` | bool | `true` | Keep **local** destinations reachable while the guard is armed: printers, NAS, the router's admin page, AirPlay/Chromecast, local dev servers, SSH to another machine on the desk. Without it, arming the guard makes every one of them unreachable with no way to get them back. **Destination-scoped** (RFC1918 + CGNAT + link-local + IPv6 ULA + multicast — see [modes.md](../concepts/modes.md#local-network-access)), never interface-scoped, so it cannot become an internet path: packets to public addresses stay blocked whatever the next hop is. Costs nothing against the threat model — this traffic never leaves the building, so it cannot expose your country to a foreign service. **The one real cost:** on an untrusted network (café, hotel) it lets you reach, and be reached by, the other devices there. Set `false` to close that. |
| `vpn.autoArm` | bool | `true` | Start PASSIVE (posture `standby`, nothing enforced) when no tunnel interface is present, and arm the guard automatically the moment a VPN connects (endpoints are re-checked at arm time; arming is held while none are known). Never disarms on tunnel loss — a drop is exactly the leak the kill switch exists for; an explicit `unblock` with the tunnel down returns to standby. **On by default** (2026-07 defaults review: a guard armed with no VPN is a mystery blackout for new users); set `false` for the stricter armed-from-startup posture. |
| `vpn.endpointRefresh` | duration | `1m` | How often hostnames are re-resolved and live discovery re-run. Local work only (DNS + a socket scan), so the fast cadence costs nothing against geo-API quotas and promotes roamed-to servers to learned within ~3 minutes. |
| `vpn.endpointGrace` | duration | `15m` | How long an autodiscovered endpoint stays in the allowed set after a refresh stops reporting it. Discovery can only see an endpoint while its socket lives, and the socket dies with the tunnel — the grace is the window in which a dropped VPN can redial the *same* server without a switch window. A genuinely rotated-away server ages out once unseen past the grace. |
| `vpn.tunnelWatch` | duration | `1s` | How often the tunnel interface(s) are sampled for up/down. Drives the tunnel-down edge that arms the guard out of STANDBY and opens the automatic redial window, plus logging and `monitor`. |

### Validation rules (enforced by `validate` and at load)

- `pollInterval` > 0
- `hysteresis` ≥ 1
- at least one `providers` entry
- every `blockedCountries` code is 2 letters
- `vpn.profiles`: unique names (`[A-Za-z0-9._-]`, ≤64), each with ≥1 valid endpoint
- `vpn.switchWindow` within `(0, advanced.switchWindowMax]` (no floor), or exactly `"0"` (disabled)
- `vpn.redialWindow` within `(0, advanced.redialWindowMax]` (no floor, independent cap), or exactly `"0"` (disabled)

**Endpoints are deliberately *not* a load-time requirement.** They used to be,
because `vpn.enabled: true` was a promise to enforce and a guard that can never
learn a server address can never let the tunnel redial. With one mode, every
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
can re-resolve them on redial while the tunnel is down (otherwise a
hostname-only config can wedge: the tunnel drops, DNS is cut, and the client
can't find its server to redial).

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
sudo dezhban run --simulate-tunnel-down 8s --config <config>   # exercise the tunnel-drop path (cut + redial window)
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
entirely to keep the defaults; set only the knobs you need. Every field below is
reachable with `dezhban config set vpn.advanced.<field>=<value>` — the same
validated write-and-reload path as any other key — not just by hand-editing the
file. `switchWindowMax`, `redialWindowMax`, `redialMinUptime`, `redialBudget`,
`redialBudgetWindow`, and
`windowDiscoveryInterval` apply live; the rest (built into something the run
loop constructs once at startup, or — for `windowProtocols`/`windowPorts` —
only re-read when a switch window opens) need `dezhban restart` to take
effect, which `config set` says so at the time.

**`0` is not "off" here.** Only the three windows and `redialMinUptime` treat a
`0` as an explicit opt-out; every other field in this table has no disabled
state, so a non-positive value is replaced with the default shown below. That
replacement is not silent — `config set` echoes the value actually stored and
adds a `note: <key> was normalised on write: <typed> → <stored>` line whenever
the two differ, on both write paths (elevated and `--token-stdin`).

`redialBudget` and `redialBudgetWindow` go one step further and **refuse** a
`0` by name rather than normalising it. They are limits, not features, so an
"off" would have to mean *no limit* — the opposite direction from every other
`0` in this file, and the wrong thing for a security surface to offer. Raise the
budget to relax the bound, or set `vpn.redialWindow` to `"0"` to turn the
automatic redial window off outright. Full rationale:
[ADR-0009](../adr/0009-redial-budget.md).

| Field | Default | What it controls |
|---|---|---|
| `switchWindowMax` | `3m` | Hard cap on any MANUAL switch window (incl. `--for`). |
| `redialWindowMax` | `10m` | Hard cap on the AUTOMATIC redial window — kept independent of `switchWindowMax` so one trigger's budget never truncates the other's. |
| `commandFreshness` | `30s` | How recent a control command must be to be acted on (replay guard). |
| `windowDiscoveryInterval` | `1s` | How often the new server is looked for while a window is open. |
| `tunnelPruneAfter` | `60s` | How long a dynamically-detected tunnel must be gone before it's dropped. |
| `learnedEndpointTTL` | `720h` | How long an unused learned endpoint is kept. |
| `learnedMaxPerProfile` | `16` | Cap on learned endpoints per profile (LRU). |
| `promoteAfterRefreshes` | `3` | Consecutive sightings before a discovered endpoint is learned under normal guard. |
| `redialMinUptime` | `15s` | Backoff seed for the automatic redial window: a tunnel that was up for less than this, with no good exit confirmed during that uptime, still gets a window — but a shorter one for each consecutive fast drop, with a growing wait between them. The first drop after startup is exempt — uptime before the daemon started is unknowable. `"0"` disables the backoff, so every qualifying drop gets a full window until the budget runs out. |
| `redialBudget` | `2m` | Total time automatic redial windows may leave the guard relaxed within `redialBudgetWindow`. Debited when a window opens and **credited back when it closes early**, so a redial that succeeded in three seconds costs three seconds — the budget measures the exposure actually taken, not the exposure offered. When it can no longer afford a window the guard simply holds and traffic stays cut. Not disablable (see below). |
| `redialBudgetWindow` | `15m` | The rolling period `redialBudget` is measured over. Each window's cost is returned as it falls out of the period, so a busy link recovers its allowance progressively rather than needing a full quiet stretch. Not disablable. |
| `endpointWarnThreshold` | `256` | Union size at which `doctor` warns about rule-list bloat. |
| `windowProtocols` | `[]` | Restrict a switch window to these protocols (e.g. `["udp"]`) instead of allowing all outbound. Empty allows all — only worth setting when every VPN you switch to uses a fixed protocol. |
| `windowPorts` | `[]` | Restrict a switch window to these ports (e.g. `[51820]`) instead of allowing all outbound. Empty allows all — only worth setting when every VPN you switch to uses a fixed port set (e.g. WireGuard on 51820). |

## Presets

A **preset** is a named bundle of values for the keys that answer "how strict am
I" — a write-time macro, not runtime state. Applying one writes those keys
through the ordinary config path (same validation, same live-reload/restart
reporting as `config set`); the daemon never knows a preset was applied, only the
resulting values. A config that has since drifted from all three shows as
**Custom** rather than silently keeping a stale label.

Presets never touch identity — blocked countries, tunnel interfaces, endpoints,
profiles — the same carve-out `config reset --all` uses, which is what keeps
`vpn.profiles` (VPN identities) cleanly distinct from presets (strictness
strategies).

| Key | Strict | Balanced (shipped default) | Relaxed |
|---|---|---|---|
| `vpn.switchWindow` | `0` (disabled) | `5s` | `30s` |
| `vpn.redialWindow` | `0` (disabled) | `30s` | `2m` |
| `vpn.pauseMax` | `0` (disabled) | `30m` | `2h` |
| `pollInterval` | `10s` | `15s` | `30s` |
| `hysteresis` | `1` | `2` | `3` |
| `vpn.allowLocalNetwork` | `false` | `true` | `true` |
| `vpn.allowPhysicalDNS` | `false` | `true` | `true` |
| `vpn.armAtBoot` | `true` | `true` | `false` |

Each preset states its cost in plain words, never a bare "safe"/"strict" label
(see [glossary.md](../concepts/glossary.md)'s "Words we do not use"):

- **Strict** — zero relaxation, fastest exit checks. Cost: connecting a new VPN
  or reconnecting after a drop needs the server's address in `vpn.endpoints`
  ahead of time (no window to redial or switch through); pausing to use your
  real IP is unavailable; a VPN endpoint given as a hostname can't re-resolve
  while the tunnel is down (`allowPhysicalDNS` off); faster polling means more
  geo-provider requests.
- **Balanced** — the shipped defaults. Cost: a brief, bounded exposure window
  whenever the VPN redials or a new one connects; local devices stay reachable,
  which also lets them reach you on an untrusted network.
- **Relaxed** — longer windows, slower checks, doesn't arm at boot. Cost: longer
  exposure per window; a forbidden exit takes longer to catch; a reboot before
  the VPN reconnects leaves the network open until you arm it by hand.

A preset never touches `vpn.advanced.switchWindowMax` or
`vpn.advanced.redialWindowMax`. Those are your own hard ceiling on two of the
three relaxations, and a strictness macro that raised one would relax the guard
past a limit you set by hand. The consequence is that lowering a cap can put a
preset out of reach — Relaxed's `30s` switch window cannot be written under a
`10s` `switchWindowMax`. `preset list` and `preset diff` mark such a preset
`cannot apply:` with both values, `preset apply` refuses before writing
anything, and the way forward is to raise the cap or pick another preset.

See [cli.md](cli.md#create--manage-the-config) for `dezhban config preset
list/show/diff/apply`.

## Sample configs

- [`configs/dezhban.example.json`](../../configs/dezhban.example.json) — reference: fully automatic (autoDetect + endpoint discovery).
- [`configs/dezhban.vpn-guard.json`](../../configs/dezhban.vpn-guard.json) — explicitly pinned tunnel interface and endpoints.
- [`configs/dezhban.profiles.json`](../../configs/dezhban.profiles.json) — autoDetect + multiple VPN profiles + switch window.
- [`configs/dezhban.dev.json`](../../configs/dezhban.dev.json) — debug logging, fast poll, no blocking; for local dry-runs.
- `configs/dezhban.local.json` — your private config (git-ignored; may hold a real endpoint IP).
