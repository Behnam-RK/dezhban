# Architecture

Three layers; only the firewall layer is platform-specific.

```
Monitor    internal/monitor    polls public IP, resolves country              (platform-independent)
Decision   internal/decision   blocklist + hysteresis + fail-mode → Block/Allow  (platform-independent)
Firewall   internal/firewall   FirewallBackend per OS                         (ONLY platform-specific part)
```

The **`FirewallBackend` interface** (`internal/firewall/backend.go`) is the seam
that keeps ~90% of the code shared across operating systems. Each backend shells
out to the OS's own firewall tooling (no netlink/WFP libraries are linked) and
tags every rule with the unique name `dezhban`, so teardown is surgical and never
touches unrelated rules:

- **macOS** → `pfctl`, dedicated `dezhban` pf anchor (`pf_darwin.go`)
- **Linux** → `nft`, dedicated `dezhban` nftables table (`nft_linux.go`)
- **Windows** → WFP via `netsh`/PowerShell, tagged sublayer (`wfp_windows.go`)

Backends are selected by build tags (`//go:build darwin|linux|windows`), so each
target compiles only its own backend. The postures the backend renders — STANDBY,
GUARD, FULL BLOCK, SWITCH WINDOW — are described in [modes.md](../concepts/modes.md), and all
four are built from one constructor (`firewall.PolicyInput`) so the daemon and
`print-rules` can never disagree about what a posture looks like.

## State export — `state.json`

The live `run` daemon publishes its posture to a JSON file via an injected
`Publish` callback in `internal/runner` (writer: `internal/state`), so
out-of-process observers (the macOS menubar app, `dezhban status --json`,
scripts) can read **exactly what the daemon decided** without running their own
IP/country poller. It is best-effort observability and **never affects
enforcement** — a write failure is logged at debug and changes nothing.

| OS | Path |
|---|---|
| macOS / Linux | `/var/db/dezhban/state.json` |
| Windows | `%ProgramData%\dezhban\state.json` |

Written atomically (temp-file + rename) and `0644` (world-readable — see below).
Only the live `run` daemon writes it: on every poll, verdict transition, tunnel
up/down edge, endpoint refresh, and at startup. `--dry-run` and the read-only
inspect commands (`validate`, `print-rules`, `doctor`, `monitor`) do not.

Defined by `Snapshot` in `internal/state/state.go`. Keys are lowerCamelCase;
`time` is RFC3339. Fields marked *(vpn)* are present only once the guard has
something to describe — absent in STANDBY, before any tunnel is known.

```json
{
  "time": "2026-07-01T12:00:00Z",
  "posture": "guard",                   // guard | full-block | switch-window | standby | stopped
  "version": "v0.5.0",                  // build version of the daemon process that wrote this
  "blocked": false,                     // egress currently cut
  "ip": "203.0.113.45",
  "countryCode": "US",
  "provider": "ipinfo.io",
  "lookupErr": "",                      // GENUINE failure: a tunnel was up and measuring its exit failed
  "exitUnknown": "",                    // EXPECTED: no tunnel up, so there is no exit to measure
  "enforcementErr": "",                 // last firewall-action failure, omitted when clear
  "tunnels": [                          // (vpn)
    { "name": "utun4", "up": true, "detail": "utun4 up" }
  ],
  "endpoints": ["198.51.100.7"],        // (vpn) resolved VPN endpoints
  "pollIntervalSeconds": 30,            // daemon poll cadence, for sizing staleness
  "blockedCountries": ["IR"],
  "pid": 4242,
  "activeProfile": "proton",            // (vpn) profile of the last completed switch window; omitted until one completes
  "switch": {                            // (vpn) present only while a switch window is open
    "open": true,
    "until": "2026-07-01T12:02:00Z",
    "profile": "newvpn",
    "trigger": "manual"                  // "manual" (operator command) | "auto" (reconnect window on a tunnel drop); absent from older daemons — treat as "manual"
  }
}
```

`lookupErr` and `exitUnknown` are mutually exclusive, and the split matters. A
lookup that fails because **no tunnel is up** is not a fault — it is the normal
state during a switch or reconnect window, in standby, and across any drop. That
sets `exitUnknown` with a plain-language reason. `lookupErr` is reserved for a
failure with a tunnel **up**, where there genuinely was an exit to measure —
which may mean the exit itself is censoring the geo providers. Observers should
render `exitUnknown` as a state and `lookupErr` as a problem; showing both alike
is what made the providers look broken during every window.

`version` is the build version of the daemon **process** that wrote the
snapshot, and it is the only surface that reports what is actually running. The
binary on disk is a different fact: `upgrade apply` replaces it while the daemon
keeps enforcing on its old inode, so disk and process legitimately disagree for
the whole window between applying an upgrade and activating it (see
[upgrade.md](../usage/upgrade.md)). `upgrade apply` reads this field to tell a
still-pending activation from one that already landed. Omitted by daemons
predating the field — consumers must treat an absent `version` as unknown, never
as a version.

`enforcementErr` is distinct from both: a geo-lookup failure holds the current
posture, but a non-empty `enforcementErr` means the daemon **tried to apply a
firewall change and the backend rejected it** — so `posture`/`blocked` describe
the data plane truthfully, but the *intended* posture was not achieved (e.g. a
failed escalation leaves `posture: "guard"` while the exit is forbidden, and a
failed VPN probe re-cut can leave egress open). Observers should surface it
prominently regardless of posture — the menubar app shows a red warning icon
whenever it is set.

On a terminal `posture: "stopped"` snapshot, `enforcementErr` carries **why the
daemon went down** when the exit was not a clean shutdown: a startup refusal
(e.g. the VPN guard's "refusing to start: the tunnel is up but no server address
is known") or a run-loop failure. A clean, operator-requested stop leaves it
empty — so `stopped` + `enforcementErr` reads as "the daemon would not run", not
"you stopped it".

### The rest of the state directory

`state.json` is one of four things the daemon keeps in `/var/db/dezhban`. They
are easy to confuse, and only one of them is a *capability*:

| File | Mode | What it is |
|---|---|---|
| `state.json` | `0644` | This file — a **report**. Read-only observability; never affects enforcement. |
| `learned.json` | `0644` | VPN endpoints learned during a switch window. Daemon-owned — **never** written back into your config. |
| `logs/dezhban.log` | `0644` | Persistent daemon log, captured on every run (interactive or service) in addition to stderr / the platform logger. Size-rotated: 5 MiB × 3 files (`.1`, `.2` archives), oldest dropped. |
| `command.json` | `0600` root | A **capability**: the root-only command channel (switch open/cancel, forget-learned). Consumed once, and the daemon re-verifies its owner and mode on every read. |
| `control.sock` | `0660` root:group | The control socket — passwordless `block`/`unblock`/`switch` for the `control.group`. |

The directory itself is `0755` — deliberate, and a real disclosure worth naming;
see [What the state directory exposes](#what-the-state-directory-exposes) below.

### Consuming it

- **Machine-readable status:** `dezhban status --json` reads this file and merges
  it with authoritative service state (from the OS service manager) and config
  summary — the stable contract for tooling.
- **Staleness:** treat the daemon as stopped/unknown when the file is missing or
  `time` is older than a few poll intervals. Size the threshold off
  `pollIntervalSeconds` (the menubar app uses `max(90 s, 3 × pollInterval)`)
  rather than a fixed constant, so a deliberately long `pollInterval` doesn't
  read as stopped. A clean shutdown publishes a final `posture: "stopped"`
  snapshot, but a crash cannot, so still rely on staleness rather than only the
  sentinel.

## Control channels

Two one-way channels carry operator commands *into* the running daemon. They are
complementary, not alternatives — the file always works, the socket removes the
password prompt from the operations you perform every day.

| | **Command file** (`internal/command`) | **Control socket** (`internal/control`) |
|---|---|---|
| Path | `/var/db/dezhban/command.json` | `/var/db/dezhban/control.sock` |
| Who may write | root only (0600, root-owned dir) | the `control.group` (macOS: `admin`), via mode 0660 root:group |
| Shape | consume-once file, polled on a tick | unix socket, one JSON request per connection |
| Carries | switch open/cancel, pause/resume, forget-learned | ping, status, block, unblock, switch open/cancel, pause/resume |
| Works with no daemon | n/a (daemon consumes it) | no — the CLI falls back to acting on the firewall directly |

**The socket's trust boundary is filesystem permissions, and nothing else.**
dezhban is stdlib-only, so there is no `SO_PEERCRED` peer-credential check: whoever
can `open(2)` the socket is authorized. That is a deliberate trade, and it is
bounded by what the ops can actually do:

- `block` / `unblock` only move between postures the daemon's own state machine
  already sanctions (GUARD ↔ FULL BLOCK). They can never open egress *past* the
  guard, so the worst an unwanted caller achieves is cutting their own network.
- `switch-open` **can** relax the guard, bounded by its trigger's own hard cap —
  3m for a manual switch, 10m for the automatic reconnect window, deliberately
  never shared. It is one of two genuinely-privileged ops on the socket, which is
  why it has its own flag: `control.allowSwitchOps: false` forces it back to root-only.
- `pause` **can** also relax the guard — a third, independently-capped trigger
  (`vpn.pauseMax`, default 30m) alongside the switch window, for deliberately
  using the real ISP IP rather than connecting a VPN. Its own flag,
  `control.allowPauseOps`, is independent of `allowSwitchOps`: turning off
  passwordless switching does not turn off passwordless pausing, or vice
  versa. See [ADR-0008](../adr/0008-arm-at-boot.md).
- `panic` is deliberately **absent**. The lockout escape hatch must not depend on a
  daemon being alive, so it stays a direct, root-only firewall teardown.
- Service lifecycle (`install`/`uninstall`/`start`/`stop`) is absent for a simpler
  reason: a daemon cannot install, start, or stop itself.

**Say the cost out loud: "an admin could sudo anyway" is not the whole answer.**
`sudo` demands a password; the socket does not. So the group is not really "the
humans who administer this machine" — it is *every process running as one of them*.
A malicious binary the admin user runs, with no elevation and no prompt, can now
open a switch window (or a pause) and relax the guard for up to their respective
caps. Before the socket, that required a password the malware did not have.

We ship it on anyway, because every relaxation is bounded (clamped, auto-reverting
to the prior fail-closed posture) and because the alternative — a password prompt on
every routine block/unblock — is the kind of friction that gets a kill switch turned
off entirely. But an operator who does not want that trade has four ways out, in
increasing order of severity: `control.allowSwitchOps: false` / `control.allowPauseOps: false`
(keeps passwordless block/unblock, forces the corresponding guard-relaxing op
back to root — independently of each other), `control.group: ""` (root-only
socket), `control.enabled: false` (no socket at all).

If the socket can't be created with the intended ownership, the daemon **fails
closed on the feature** — it logs a warning, runs without it, and routine ops go
back to asking for a password. Enforcement never depends on the socket.

### What the state directory exposes

`/var/db/dezhban` is `0755` and `state.json` / `learned.json` are `0644` — both
deliberate, and both a real disclosure worth naming. The menubar app runs as the
logged-in user and must read `state.json`, so a tighter mode is not available to us:
`0700` on the directory is precisely the bug that made the app report "stopped"
while the daemon was enforcing, and `0640 root:admin` would reintroduce it for any
*standard* (non-admin) user.

The price is that **any local user can read your public IP, resolved country, tunnel
interface names, and VPN server endpoint address**. That is posture metadata, not
credentials — there are no keys or secrets in the state directory — but on a
multi-user host it is readable by everyone on it. The one file in there that is a
capability rather than a report, `command.json`, stays `0600` root-owned, and the
daemon re-verifies its ownership and mode on every read (`internal/command`,
`Consume`) rather than trusting the directory to have kept it safe.

## Rules that must not be broken

These invariants are load-bearing — the whole design depends on them:

- **Never call `pfctl`/`nft`/WFP directly from `run` or `cmd/`** — go through
  `FirewallBackend`. That seam is what keeps the code cross-platform.
- Every firewall rule carries the unique tag/anchor/table name **`dezhban`**, so
  teardown (`Unblock`/`Cleanup`) is surgical and never touches unrelated rules.
- `Block` must be **idempotent** — re-block must not stack duplicate rules.
- `Cleanup()` must always be safe to call and is wired to run on shutdown
  (`defer` + `signal.NotifyContext`). A stale block-all rule can lock the user out
  of their own network — `panic` removes rules even with no daemon running.
- **An undeterminable country HOLDS the current posture — it never escalates.**
  The standing guard rule **is** the fail-closed block for physical leaks, so only
  a *successful* blocked-country reading escalates to FULL BLOCK, and only a
  successful allowed reading restores GUARD. Escalating on an unknown would cut
  the tunnel's own egress and livelock the reconnect that could fix the lookup.
  This lives in `decision.Evaluate`, which short-circuits on `r.Err != nil`
  without touching the hysteresis streak — so a blip neither commits a flip nor
  cancels one that real readings were counting toward. There is no `failClosed`
  switch; it belonged to the retired fallback model, where the firewall was open
  at rest and an unknown country was the only reason to cut anything.
- **One goroutine applies rules.** Every `Backend.Apply` call comes from the single
  run-loop goroutine in `internal/runner`. The window timer, command poll, tunnel
  watcher, geo ticks, and control-socket requests are all *select cases* in that one
  loop. The socket's accept goroutine parses and forwards over a channel; it never
  touches the Backend. Adding a new control path means adding a select case, not a
  goroutine that applies rules.

## Dependency strategy

Dependencies are deliberate. Stdlib for CLI (`flag`), config (JSON), logging
(`log/slog`), HTTP, and firewall control (shell out to the OS tooling). There are
four third-party modules:
[`kardianos/service`](https://github.com/kardianos/service) (cross-platform
service manager — the one real daemon-path dependency, for install/start/stop),
[`charmbracelet/huh`](https://github.com/charmbracelet/huh) (the interactive
`setup` wizard and the `tools/taskmenu` dev picker),
[`charmbracelet/x/term`](https://github.com/charmbracelet/x) (TTY detection —
the sudo auto-elevation guard and the wizard's own interactive check), and
[`charmbracelet/bubbles`](https://github.com/charmbracelet/bubbles) (also
`tools/taskmenu` — dev tooling only, never installed). The three charm modules
stay off the enforcement loop itself.
The Linux/Windows backends shell out to `nft` and `netsh`/PowerShell rather than
linking `google/nftables` / `tailscale/wf` — one consistent shell-out model. Don't
add `cobra`/`viper`/etc.; the deliverable is a dependency-light standalone binary.

Config is JSON with string durations; the on-disk shape is the `fileConfig` DTO in
`internal/config`, converted to a validated `Config`. Module path
`github.com/behnam-rk/dezhban`.

## Design decisions

The choices below were locked early and the codebase still rests on them. They are
recorded here because the rationale, not the choice, is the part that is expensive
to reconstruct.

Later decisions — and the alternatives examined for each — live as
**[architecture decision records](../adr/README.md)**. Read those before reversing
anything they describe: several record choices that look wrong until you know the
failure they were built to prevent.

| Decision | Choice | Rationale |
|---|---|---|
| Language | **Go** | One static binary per OS, `go build` cross-compiles, no runtime to install |
| Platform order | **macOS first**, then Linux, then Windows | Prove one backend end-to-end, then port behind the `FirewallBackend` interface |
| Detection | **API-based**, offline IP-range hybrid deferred | Simple to start; robustness can be added once the loop is proven |
| Fail mode | **Fail-closed by construction** | The standing guard rules *are* the block, so there is no undeterminable-country decision to get wrong — an unknown holds the posture (see above). The old `failClosed` switch belonged to the retired fallback model ([ADR-0001](../adr/0001-single-guard-mode.md)) |
| Enforcement primitive | **Interface-aware** — pass on tunnel + endpoint handshake, block physical | A destination-IP allowlist is meaningless under a full tunnel: pf/nft see only the outer packets to the VPN endpoint |
| Guard model | **Always-on interface guard — the only model** | A VPN drop is cut instantly, with a zero leak window. A reactive poller leaks for one poll interval, which is why the country-blocklist fallback was removed rather than kept as a peer ([ADR-0001](../adr/0001-single-guard-mode.md)) |
| Resting posture | **STANDBY — no rules until a tunnel is observed** | A guard with no tunnel blocks everything, which is a blackout rather than security. This is the safety job `vpn.enabled: false` was quietly doing ([ADR-0002](../adr/0002-standby-no-tunnel-posture.md)) |
| Recovery | **Wait for the VPN to return to an allowed country** | While full-blocked, observe the exit through a time-windowed probe and restore the guard once the exit is allowed again |

Two of these were revisited during the build and are worth naming as *deviations*,
since the reasoning is not obvious from the code:

- **The Linux and Windows backends shell out** to `nft` and `netsh`/PowerShell
  instead of linking [`google/nftables`](https://github.com/google/nftables) and
  [`tailscale/wf`](https://github.com/tailscale/wf), as originally planned. Shelling
  out mirrors the macOS `pfctl` backend, giving one consistent model across all three
  OSes and zero added dependencies. Those libraries remain the documented alternative
  if pure-Go enforcement is ever needed.
- **The control socket relaxes "root-triggered"** for routine ops. The full cost of
  that trade is spelled out under [Control channels](#control-channels) — it is a
  deliberate concession to usability, not an oversight.
- **The switch window gained a second sanctioned trigger** (2026-07): the
  [automatic reconnect window](../concepts/modes.md#automatic-reconnect-window)
  (`vpn.reconnectWindow`, default 30s, `"0"` restores the original
  operator-only behavior). Field testing with rotating-pool anti-censorship
  VPNs (fresh Cloudflare-fronted server IP on nearly every connect) showed
  that "keep known endpoints open" can never cover a reconnect, making every
  drop a manual `switch` — an operator burden that pushed users toward running
  with the guard off entirely. The trade is explicit and bounded: a tunnel
  drop from healthy GUARD may expose the real IP for up to `reconnectWindow`
  seconds while the client redials; in exchange, reconnects and VPN switches
  are zero-interaction, and the guard still fail-closes on expiry. The
  alternatives were examined and rejected: standing port/protocol allows
  (443-fronted VPNs make any filter that admits the VPN admit the leak),
  per-app allows (not expressible across pf/nft/WFP), and provider IP feeds
  (Cloudflare-fronted means allowlisting a whole CDN). Zero-leak purists set
  `"0"` and lose nothing.
- **STANDBY's own arming rail went unimplemented, and the switch window gained
  a third trigger** (2026-07-22): ADR-0002 required the "tunnel observed up at
  least once" fact to persist across restarts; it never did, so every boot
  where this daemon started before the VPN client's interface existed silently
  opened the network. `vpn.armAtBoot` (default true) closes that with a small
  persisted record (`internal/armed`) and arms at boot from it — never for a
  host that has never proven its VPN works, preserving ADR-0002's actual
  guarantee. Separately, `dezhban pause`/`resume` add a **third** sanctioned
  relaxation, `state.TriggerPause`, sharing the switch-window machinery but
  with its own cap (`vpn.pauseMax`) and its own control-socket gate
  (`control.allowPauseOps`) — for deliberately using the real ISP IP, not
  connecting a VPN. See [ADR-0008](../adr/0008-arm-at-boot.md).
