# Changelog

All notable changes to **dezhban** are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

Releases are cut with the manually-dispatched `release` workflow, which rewrites
the `## [Unreleased]` section below into a versioned entry — see
[docs/releasing.md](docs/releasing.md). Keep `## [Unreleased]` current as you land
changes.

## [Unreleased]

### Removed

- **BREAKING: the country-blocklist fallback mode is gone.** dezhban now has a
  single enforcement model — the always-on interface guard. The
  `vpn.enabled: false` mode watched your public IP and cut egress by destination;
  it applied **no rules at rest**, so it was "best-effort, not a zero-leak
  guarantee" by its own documentation, and it was only meaningful when the
  country you blocked was your *real physical location*. The guard already
  contains the country check — that is what FULL BLOCK is. See
  [ADR-0001](docs/adr/0001-single-guard-mode.md).
- `print-rules --mode legacy` now errors by name instead of rendering a posture
  that no longer exists. `guard` / `fullblock` / `switch` are unchanged.
- `status --json` drops `mode` (`"vpn"`/`"legacy"`) and `vpnEnabled`. Both had
  exactly one possible value after the merge; a constant field is noise, not
  compatibility. `posture` is unchanged and remains the field to read.
- There is no `failClosed` setting. Under the guard, the standing rules **are**
  the fail-closed block, so an undeterminable country holds the current posture
  rather than escalating — escalating would cut the tunnel's own egress and
  livelock the reconnect that could fix the lookup.

### Added

- **`vpn.switchWindow: "0"` now disables manual switch windows.** Previously it
  was silently coerced back to the 15s default, so the setting was accepted and
  discarded — the worst failure mode a security tool has. It now uses the same
  explicit-opt-out sentinel `vpn.reconnectWindow` already had. Setting both to
  `"0"` is the strict zero-leak posture in which nothing can relax the guard.
  Disabling one never disables the other. `dezhban switch` refuses by name,
  telling you which setting is responsible. See
  [ADR-0004](docs/adr/0004-switch-window-fully-disableable.md).
- **Retired keys are reported, not ignored.** `vpn.enabled`, `failClosed` and
  `allowlist` still parse without error, do nothing, and are named — with a
  reason — by `dezhban validate` and once at daemon start. They are never
  written back when dezhban saves your config.
- **Architecture decision records** under [`docs/adr/`](docs/adr/), plus a
  [glossary](docs/glossary.md) fixing the "guard"/"protection"/"kill switch"
  vocabulary drift. GUARD is the canonical term.

### Changed

- **Upgrade note — no config migration is required.** A pre-merge config loads,
  validates, and enforces identically; the three retired keys are reported and
  ignored. Two behavior changes to know about:
  - A config that had `vpn.enabled: false` was running the fallback mode. It now
    runs the guard, which means it rests in **STANDBY** (no rules, network fully
    open) until a tunnel is configured *and* observed up — rather than watching
    your public IP. If you were relying on the fallback, there is no longer an
    equivalent; the guard requires a VPN.
  - `vpn.endpoints` is no longer required at load time. A config with none is
    valid and rests in STANDBY. The check moved to where it can tell the
    difference: the runner refuses to *arm* a guard that has tunnels but no
    endpoints, and `doctor` reports it as a lockout risk beforehand.
- **STANDBY is a first-class posture**, not an emergent property of `autoArm`.
  It is the resting state before any tunnel has been observed: no rules, network
  fully open, and the UI says so — grey icon, never red. This is the job
  `vpn.enabled: false` was quietly doing as a safety opt-in, now done properly.
  See [ADR-0002](docs/adr/0002-standby-no-tunnel-posture.md).
- **One constructor builds every posture** (`firewall.PolicyInput`). The run loop
  and `print-rules` previously built them separately, and had already drifted —
  the preview dropped `TunnelGroups` entirely and degraded a zero-tunnel guard on
  a different condition than the daemon. A preview that can lie about what the
  daemon would install is a correctness bug, not untidiness.

## [0.3.0] - 2026-07-20

### Changed

- **Upgrade note — posture defaults change for existing configs.** The defaults
  review below makes `vpn.autoArm` and `vpn.allowPhysicalDNS` default **on**. A
  config that never set `vpn.autoArm` (the previous default was off, and it was
  omitted when false) will **arm on VPN connect / park in standby instead of
  arming from boot** under this release — a real posture change, not a no-op.
  Every tunnel drop now also opens a **30s reconnect window** by default (real IP
  may be exposed while the client redials) unless you set `vpn.reconnectWindow:
  "0"`. To keep the pre-upgrade strict posture, set `vpn.autoArm: false`,
  `vpn.allowPhysicalDNS: false`, and `vpn.reconnectWindow: "0"`. (An explicit
  `allowPhysicalDNS: false` already on disk is preserved; only omitted keys pick
  up the new default.)
- **Defaults review (2026-07-19)** — the shipped defaults now favor the
  smooth-operation posture; every previous value remains one config line away:
  `vpn.autoArm` **on** (standby until a VPN connects — no more mystery blackout
  when starting without a VPN), `vpn.allowPhysicalDNS` **on** (hostname redials
  work while the tunnel is down; explicit `false` still closes the DNS-metadata
  leak), `pollInterval` **15s** + `hysteresis` **2** (~30s worst-case
  forbidden-exit confirmation), `vpn.switchWindow` **15s** (windows close early
  on success; `--for` extends a one-off), `vpn.endpointRefresh` **1m**.
  `autoArm`/`allowPhysicalDNS` became pointer fields on disk so an explicit
  `false` survives normalization.
- **Five new geo-IP providers** (geojs.io, country.is, ipwho.is,
  freeipapi.com, ipapi.co) join the existing three, and the default provider
  list is now ordered by rate-limit headroom — the first reachable provider
  absorbs nearly all poll traffic, so unmetered endpoints go first and
  quota-limited ones (ipinfo, ipapi.co) become deep fallbacks. Provider-side
  failure shapes (ipwho.is `success:false`, ipapi.co `error:true`) fail closed.

### Added

- **Automatic reconnect window** (`vpn.reconnectWindow`, default `30s`, on by
  default; `"0"` disables): a tunnel drop from healthy GUARD now opens the
  bounded switch-window relaxation automatically, so a VPN client can redial
  *any* server — including a never-seen one (rotating-pool / 443-fronted
  anti-censorship VPNs) — or a different VPN app entirely, with zero operator
  interaction. Closes early and learns the new endpoint on a confirmed good
  exit; fail-closes and stays closed on expiry. Guarded by an anti-flap gate
  (`vpn.advanced.reconnectMinUptime`, default `15s`) and never opens from
  standby, FULL BLOCK, or for a tunnel never observed up. The manual
  `dezhban switch` window remains as the fallback for edge cases.
  `status --json` labels an open window's origin via the additive
  `switch.trigger` field (`"manual"`/`"auto"`); the menubar app shows a
  distinct "VPN dropped — reconnect window open" banner and notification.

- **`dezhban config reset <key> [key ...]` / `config reset --all`** — restore
  shipped defaults from the CLI. `--all` resets every tunable while preserving
  identity data (blockedCountries, allowlist, vpn.enabled / tunnelInterfaces /
  endpoints / profiles); deleting the config file remains the true wipe.
- **Persistent log capture, always on**: every daemon run — interactive or
  under the service manager — now also appends to
  `<state dir>/logs/dezhban.log` (0644, size-rotated at 5 MiB with two
  archives), so history survives shell exits and is readable without root;
  stderr and the platform logger keep working exactly as before.
- **Touch ID for the menubar app's privileged prompts**: elevation now prefers
  `sudo` + `pam_tid` (the system Touch ID HUD) when Touch ID for sudo is
  configured, falling back to Authorization Services and then the legacy
  osascript dialog — in practice the `system.privilege.admin` SecurityAgent
  prompt never offers biometrics, so the sudo path is the one that actually
  delivers them. `dezhban doctor` now surfaces the one-line
  `/etc/pam.d/sudo_local` opt-in when it is missing.

### Fixed

- Saving a config (`config set`, the setup wizard, GUI settings) silently
  dropped `vpn.endpointGrace` and `vpn.autoArm` — both now round-trip, and an
  absent `endpointGrace` normalizes to its effective `15m` default so
  observers see the real value instead of `0`.
- The menubar/Dock icon now shows the blocked (red) state in the zero-tunnel
  standing posture — VPN guard armed with no tunnel present is a total egress
  cut and no longer renders the calm green shield.

## [0.2.0] - 2026-07-18

### Added

- **`vpn.autoArm`** (default off): the daemon starts PASSIVE (new posture
  `standby`, nothing enforced) when no tunnel interface is present, and arms
  the guard automatically the moment a VPN connects — no more choosing between
  "always blocked without a VPN" and "kill switch off". Arming is one-way on
  tunnel loss (a drop is exactly the leak the kill switch exists for); an
  explicit `unblock` with the tunnel down releases back to standby. Arm-time
  endpoint checks preserve the no-endpoint blackout refusal, and switch
  windows are refused in standby (egress is already open). Toggle in the
  macOS app's VPN Guard pane.
- **macOS: notifications for essential events** — guard armed / auto-armed,
  egress blocked, warnings (enforcement error, switch window open), standby,
  stopped. Posted by the menubar app on posture transitions only (never at
  launch, never for routine updates); toggle in Settings.

- **Brand assets wired in end-to-end** (`gui/assets/`): full-color menubar and Dock
  state icons (teal on / gray off / red blocked / amber warning), a generated
  `AppIcon.icns`, and the README banner. The Dock tile mirrors the enforcement
  posture (the app is no longer an `LSUIElement` agent).
- **`vpn.endpointGrace`** (default `15m`): an autodiscovered endpoint now stays
  in the allowed set for a grace period after a refresh stops reporting it, so
  a dropped VPN can redial the same server without needing a switch window.
  Discovery could only see an endpoint while its socket lived — and the socket
  dies with the tunnel, which walled off exactly the reconnect the guard keeps
  endpoints open for.
- **macOS: Settings hub in the app**: startup controls (install/uninstall the
  boot service, open the app at login), blocked countries, switch-window
  duration, endpoint grace, and the config-file escape hatch. Replaces the
  scattered "VPN guard mode" / "Open config file…" / "Launch at login" menu
  items. About now also reports which elevation path privileged actions take
  (Authorization Services with Touch ID vs. the password-only fallback).

### Changed

- **Dev-task vocabulary overhauled** (developer-facing only; no runtime change).
  The Taskfile shrinks to ~15 intent-named commands in four groups — everyday
  (`build`, `check`, `dev`, `clean`), safe loop (`monitor`, `validate`, `rules`,
  `doctor`, `status`), real install (`pkg`, `install`, `uninstall`, `panic`),
  and the unchanged release trio. Renames: `dev:all`→`dev`, `pkg:build`→`pkg`,
  `pkg:cycle`→`install` (`pkg:fresh` is now `install FRESH=1`),
  `pkg:uninstall`→`uninstall`, `run-dry`→`monitor`. The source-install wrappers
  (`install-local`, `reinstall`, `uninstall-local`) are gone — the scripts
  remain standalone (`sh scripts/install-local.sh`). Bare `task` on a TTY now
  opens an interactive picker (`tools/taskmenu`, built on the huh dependency
  the setup wizard already uses); non-TTY prints a grouped menu. Privileged
  flows ask for sudo up front, destructive ones confirm first, and behavior
  vars are asked on-demand: unset on a TTY, `install` asks "wipe first?" and
  `uninstall` asks "keep config?", and the release tasks ask for the
  bump/version spec — passing `FRESH=`, `KEEP_CONFIG=`, `VERSION=`/`BUMP=`
  explicitly (or having no TTY) skips the question. Plumbing is hidden behind
  `task --list-all`.
- **macOS app overhauled: one main window, minimal menubar.** A SwiftUI main
  window (sidebar: Overview / VPN Guard / Settings / Logs & Diagnostics /
  About) is now the primary surface — opened via "Open Dezhban…" (⌘O) or the
  Dock icon, never at launch; closing it keeps the app running. The menubar
  dropdown shrinks to the safety core: status line, Block/Unblock, the switch
  window, Panic, Quit. Panic from the menubar still shows its transcript in a
  plain alert, so the escape hatch never depends on the window opening. The
  three separate panel windows (Settings, VPN guard, output) are gone; long
  command output lands in the Logs & Diagnostics pane. Repo layout moved with
  it: `macos-gui/` → `gui/macos/`, `assets/` → `gui/assets/`.

### Fixed

- **A daemon whose run loop ended on its own (startup refusal, run failure)
  lingered as a zombie**: the service manager still counted the process as
  running, so `start` was a silent no-op and only a kill recovered. The
  process now exits when the loop ends by itself, and `stop`'s teardown wait
  is bounded (30s, with a loud log pointing at `dezhban panic`).
- **`switch --cancel` could die with "daemon busy" while a window was open
  with a VPN mid-connect.** The early-close verification probe ran inline on
  the run loop (8s budget vs. the control socket's 2s hand-off), and the CLI
  treated the busy reply as a daemon refusal — which callers rightly never
  escalate. The probe now runs off-loop (verdict and every firewall Apply
  still on the loop), and transient server errors fall back to the durable
  root command-file path.
- **`stop` on a crash-looping (loaded-but-not-running) service reported
  "already stopped" without unloading it**, so KeepAlive kept respawning the
  daemon. The idempotence guard now consults the loaded state, not just
  running. (Post-merge review finding on #21.)
- **macOS: a guard posture with every tunnel down now shows the BLOCKED state
  icon** (menu bar + Dock) instead of the calm "on" — the guard cutting
  physical egress is a blocked state visually, even though the posture string
  legitimately stays `guard`.

- **macOS: start/stop/restart from the menubar app failed with "Expecting a
  LaunchAgents path … Load failed: 5".** The app's admin prompt runs commands as
  root but *inside the GUI login session*, and the legacy `launchctl load`/
  `unload`/`list` used by the service library infer the launchd domain from the
  session, not the uid — so loading the LaunchDaemons plist was rejected, and
  the service was misreported as stopped while running. Service start/stop and
  the root status query on macOS now use the domain-explicit subcommands
  (`launchctl bootstrap system …` / `bootout system/…` / `print system/…`),
  which behave identically under a terminal `sudo` and the app's elevation.
  (`uninstall` also boots the job out first, so it can no longer remove the
  plist while leaving the daemon resident.)
- **A startup refusal is now visible, not just logged.** When the run loop
  refuses to arm (e.g. the VPN guard's "refusing to start: the tunnel is up but
  no server address is known") or fails, the reason is published into the final
  `posture: "stopped"` snapshot as `enforcementErr` — so `status --json` and the
  menubar app can say *why* the daemon is down instead of showing a bare
  "stopped" indistinguishable from a deliberate shutdown.

## [0.1.0] - 2026-07-14

### Added

- **A release is now one command.** `task release BUMP=minor` (or
  `VERSION=0.2.0`) runs a preflight — on `main`, clean tree, synced with origin,
  `[Unreleased]` non-empty, CI green — prints what it is about to do, asks you to
  type the tag to confirm, then dispatches and streams the workflow.
  `task release:preview` shows the resolved version, the rendered notes and the
  CHANGELOG diff without touching anything, and the workflow's `dry_run` input
  does the same at full fidelity on a real runner: it cross-compiles everything
  and install/uninstall-tests the `.pkg`, then publishes nothing. All of it goes
  through one `scripts/release.sh`, which is the same code the workflow runs, so a
  local preview cannot drift from what CI does.
- **Release candidates** (`X.Y.Z-rc.N`). An rc is a pure snapshot: it tags only —
  no CHANGELOG roll — and publishes as a GitHub pre-release, so it never becomes
  "latest" and an abandoned rc line costs nothing to walk away from.
  `bump: patch|minor|major` always counts from the last *final* tag; `bump: rc`
  advances an open rc line.
- The release **never pushes to `main`**. It tags the exact commit it built and
  tested, publishes, and then opens a `chore(release)` PR carrying the rolled
  CHANGELOG — because `main`'s ruleset requires a pull request and the Actions bot
  cannot bypass it (GitHub only permits that on org-owned repos). The ruleset is
  left intact, and no long-lived admin token goes anywhere near CI.
- `dezhban -v version` now reports the commit, build date and Go version
  alongside the version, and `status --json` gained `commit` and `buildDate`. A
  binary built without the Taskfile (a plain `go build`) no longer reports itself
  as an anonymous `dev`: it falls back to the VCS stamps the Go toolchain embeds,
  so it still names the commit it came from and whether the tree was dirty.
- **Standalone macOS installer** (`dezhban-<version>.pkg`, `task pkg:build`):
  installs the CLI, the menubar app, and the launchd service in one step with a
  single password prompt. It registers the service but deliberately does **not**
  start enforcement — configure with `sudo dezhban setup`, then `sudo dezhban start`.
  Ships its own uninstaller (`sudo sh /usr/local/share/dezhban/uninstall.sh`), and
  the release workflow installs + uninstalls it on a runner before publishing.
  Unsigned (no Apple Developer certificate); `build-pkg.sh` has the signing seams.
- **Control socket** (`internal/control`, config `control` block): the daemon
  listens on a root-owned, admin-group unix socket, so `block`, `unblock` and
  `switch` are performed BY the running daemon and **need no password**. Both the
  CLI and the menubar app go through it, falling back to the previous root path when
  no daemon is listening. `panic` and the service lifecycle deliberately stay
  root-only. Tighten with `control.allowSwitchOps: false`, `control.group: ""`, or
  `control.enabled: false`; `dezhban status` reports which mode you're in.
- A manual `block` now **holds**: the geo state machine is suspended until you
  `unblock`, so an allowed reading can't quietly undo an operator's block.
- `config set` accepts several `key=value` pairs in one validated, atomic write
  (`dezhban config set vpn.enabled=true vpn.tunnelInterfaces=utun4`). One prompt,
  one write, and no ordering constraints between interdependent keys.

- `dezhban restart` — stop + start as one command, for applying a config change
  (there is no live reload). `start` and `stop` are now idempotent.

- **Touch ID for the menubar app's admin prompts.** It now elevates through
  Authorization Services (the API behind the System Settings padlock), whose prompt
  offers "Touch ID or password" — and caches the authorization, so a second privileged
  action a moment later is usually silent. The old `osascript` dialog was password-only
  and always had been; it remains as a fallback. For the CLI, enable Touch ID for
  `sudo` (`pam_tid`) — see [docs/usage.md](docs/usage.md#touch-id).

### Changed

- **Makefile replaced by a [Taskfile](https://taskfile.dev)** (`task` lists everything).
  All targets carried over 1:1, plus two new update-roll loops for testing:
  `task dev:all` (fast: rebuild + swap CLI and app in place, restart daemon, relaunch)
  and `task pkg:cycle` (full: cross-compile, build the `.pkg`, install it, open the
  app), with `pkg:fresh`/`pkg:install`/`pkg:uninstall` piecewise variants. The
  `scripts/*.sh` escape hatches still run standalone without `task`. See
  [docs/development.md](docs/development.md).

### Fixed

- **A failed release used to strand a tag.** The release tagged and pushed
  *before* it built anything, so a broken build or a failed installer smoke-test
  left a pushed tag and a `chore(release)` commit with no release behind them —
  and the workflow's own "tag already exists" guard then refused the retry. The
  order is now resolve → build → smoke-test → *only then* tag and publish, so a
  failed release leaves the repository untouched and re-dispatching is free.
  `publish` additionally refuses to run if `main` moved after the commit it built
  from, rather than tag a tree that was never tested.
- **The release never checked whether the code it was shipping worked.** It ran no
  tests and never looked at CI, so a red `main` released fine. It now requires
  `ci.yml` to be green on the exact commit being released, waiting out an in-flight
  run and aborting on a red or missing one. `force: true` overrides it for an
  emergency, loudly.
- **`task pkg:install` / `pkg:cycle` / `pkg:fresh` could never find the installer
  they had just built.** The Taskfile looked for `dezhban-v0.1-…​.pkg` while
  `build-pkg.sh` writes `dezhban-0.1-…​.pkg` — it strips the tag's leading `v` and
  the Taskfile did not. Every invocation failed the precondition with a misleading
  "run `task pkg:build` first". The `v` is now normalised in one place.
- **Every dev build of the menubar app claimed to be version `0.1.0`**, the
  hardcoded fallback in `Info.plist`, which only a tagged CI build ever overwrote.
  An unstamped build is now a visible `0.0.0`. A release candidate stamps its
  numeric core (`0.2.0-rc.1` → `0.2.0`) into the pkg receipt and bundle rather than
  collapsing to `0.0.0`, since those fields must be dotted numerics.
- **Endpoint auto-discovery reported unrelated hosts as VPN endpoints.** It accepted any
  socket bound to a physical interface IP with a public peer, on the premise that a
  full-tunnel VPN routes everything else through the tunnel. That premise is false: apps
  bind to the physical link all the time. In the wild it returned GitHub, Cloudflare and
  Google — and those addresses went straight into the guard's pass list, so the kill
  switch punched **permanent holes to arbitrary hosts** (a leak) while still blocking the
  real VPN server (a blackout). Discovery now requires the socket to be owned by a
  process that is plausibly a VPN transport; an unattributable socket is not an endpoint.
- **The guard could be armed in a state that cut the tunnel's own transport.** With a
  VPN connected but no known server address, the guard's `block drop out all` covers
  the physical interface — which is exactly what carries the VPN's encrypted transport
  — so arming it killed the tunnel and every packet with it, unrecoverably (the socket
  discovery would have learned the server from died too). `vpn.autodetect` was wrongly
  excusing this; that allowance exists for the *zero-tunnel* case, where a total cut is
  correct and a switch window recovers it. The daemon now refuses to start with a live
  tunnel and no endpoint, and says how to fix it. `doctor` reports it as a LOCKOUT RISK
  and exits non-zero (it also now exits non-zero on tunnel-internal endpoints, which it
  previously reported and then exited 0 on).
  Note: endpoint auto-discovery reads *connected* sockets, and WireGuard (like other
  NetworkExtension clients) sends from an *unconnected* UDP socket — it cannot be
  discovered, and must be named via `vpn import` / `vpn add` / `vpn.endpoints`.
- **The menubar icon is no longer tinted at all.** Both the stopped (gray) and the
  enforcing (green) shields were unreadable on a dark menu bar. It is now a plain
  template image drawn in the menu bar's own color, with the posture carried by the
  symbol — hollow shield (stopped), check shield (enforcing), slashed shield (blocked),
  exclamation shield (switch window open).
- **`stop` failed on a service that wasn't running**, because launchd's
  `launchctl unload` is an edge trigger and errors with a bare "Input/output error"
  when the job was never loaded. Being asked to reach a state you are already in is
  not an error; `start`/`stop` now report it and exit 0. This is what made the GUI's
  config-apply abort halfway — a failed `stop` (on an installed-but-stopped daemon)
  took the following `start` down with it.
- **The daemon's state directory (`/var/db/dezhban`) was created `0700`** by the
  macOS pf backend, which silently broke everything that reads out of it as the
  logged-in user: the menubar app could not read `state.json` (so it showed "Kill
  switch stopped" and "no posture reported" while the daemon was enforcing
  perfectly), and the control socket was unreachable through the directory (so every
  routine `block`/`unblock` fell back to a password prompt — the very thing the
  socket exists to prevent). The directory is now `0755` and `state.EnsureDir`
  repairs an existing too-tight one at daemon startup. Confidentiality was never in
  the directory bit: the sensitive files inside it are `0600`.
- **The menubar app asked for a password once per config field.** Applying the VPN
  panel meant seven separate elevations, plus two more for the restart. The panel now
  sends the whole change as one batched, privileged invocation — **one prompt**. The
  same batching makes "Install service…" one prompt instead of two and "Uninstall
  service…" one instead of three.
- **The menubar icon was invisible on a dark menu bar** when stopped: it was tinted a
  fixed gray. Resting states now draw in the menu bar's own adaptive color; only the
  states that carry a warning keep an explicit color.
- Always-on **VPN interface guard** (`vpn.enabled: true`): egress is allowed only
  through the tunnel, cutting a tunnel drop with a zero leak window, with a bounded
  **switch window** as the only sanctioned relaxation.
- **Country-blocklist fallback** (`vpn.enabled: false`): polls the public IP and
  cuts traffic by destination country for hosts not behind a VPN.
- Cross-platform `FirewallBackend` seam with build-tagged backends: `pfctl`
  (macOS), `nftables` (Linux), WFP/`netsh` (Windows).
- CLI subcommands: `run`, `block`, `unblock`, `status`, `panic`, `install`,
  `uninstall`, `start`, `stop`, `restart`, `detect-vpn`, `validate`, `print-rules`,
  `doctor`, `monitor`, `switch`, `vpn`, `setup`, `config`, `completion`, `version`,
  plus a global `-v`/`--verbose`.
- Read-only diagnostics that need no root: `validate`, `print-rules`, `doctor`,
  `monitor`.
- macOS **menubar GUI** (`Dezhban.app`, `task gui:build`): a standalone Swift
  client that reads the daemon state file and drives the CLI.
- Cross-platform release build matrix (`task build:all`) producing five binaries:
  darwin/arm64, darwin/amd64, linux/amd64, linux/arm64, windows/amd64.

[Unreleased]: https://github.com/Behnam-RK/dezhban/compare/v0.3.0...HEAD
[0.3.0]: https://github.com/Behnam-RK/dezhban/releases/tag/v0.3.0
[0.2.0]: https://github.com/Behnam-RK/dezhban/releases/tag/v0.2.0
[0.1.0]: https://github.com/Behnam-RK/dezhban/releases/tag/v0.1.0
