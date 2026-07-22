# Changelog

All notable changes to **dezhban** are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

Releases are cut with the manually-dispatched `release` workflow, which rewrites
the `## [Unreleased]` section below into a versioned entry — see
[docs/contribute/releasing.md](docs/contribute/releasing.md). Keep `## [Unreleased]`
current as you land changes.

## [Unreleased]

### Added

- **`vpn.armAtBoot`** (default **true**): arms the guard directly at startup,
  even before the VPN's tunnel interface exists, on any host that has
  connected successfully at least once and has a known endpoint. Closes a real
  gap — `internal/runner` decided STANDBY from a live interface probe taken
  fresh on every start, so a normal boot (this daemon starts before the VPN
  client) opened the network for however long the VPN took to reconnect, on
  every reboot, even on hosts that had run the guard for months. A fresh
  install, or a host whose VPN has never come up, still starts in STANDBY —
  this cannot turn a misconfiguration into a lockout. See
  [ADR-0008](docs/adr/0008-arm-at-boot.md).
- **`dezhban pause [duration]` / `dezhban resume`**: a bounded, deliberate drop
  to the real ISP IP (e.g. to reach a domestic-only service a VPN exit can't
  reach), auto-reverting at the deadline with no further action. A third
  sanctioned relaxation of the guard alongside the switch window and the
  automatic reconnect window, with its own cap (`vpn.pauseMax`, default 30m,
  `"0"` disables) and its own control-socket gate (`control.allowPauseOps`,
  default true, independent of `control.allowSwitchOps`).

### Changed

- **README now leads with the macOS app**, with the CLI presented as the
  headless option for Linux, servers, and terminal users — it previously
  mentioned the app only twice, both in passing, despite the app covering
  every everyday operation. Adds a platform-support table marking Windows
  **experimental** (no passwordless control path yet) rather than a peer
  install target.
- **`docs/` reorganized from a flat 17-file list into `usage/`, `concepts/`,
  and `contribute/`**, grouped by audience; `docs/adr/` is unchanged.
  Duplicated explanations of the same concepts (the country-check hold
  behavior, the switch/reconnect/pause windows) are consolidated into single
  canonical homes in `docs/concepts/modes.md`, which also gains two ASCII
  diagrams (the posture state machine, the window-trigger comparison).
- Five of seven ADRs were marked `implementation pending` in the decision-log
  index despite having shipped; statuses now match reality (`docs/adr/README.md`).
- `CLAUDE.md` corrected against a full verification pass: the dependency count
  (four third-party modules, not three), the read-only/root command split, the
  global-flags list, and the subcommand list, plus a new doc-maintenance
  convention routing config/CLI/behavior changes to their canonical doc.

### Fixed

- Removed `legacy` as an offered `--mode` value from the Taskfile description
  and all three shell completions (bash/zsh/fish) — `print-rules --mode legacy`
  has errored by name since ADR-0001, but the completions still suggested it.
  Also added the `--no-sudo`/`--no-daemon` global flags to all three
  completions, which were missing entirely.
- Retired "legacy direct model" language from `internal/firewall` comments
  (`backend.go`, `policyset.go`, the three per-OS renderers): the code path
  they describe is live — it's what `block --force` renders — not a leftover
  of the country-blocklist model ADR-0001 removed. One dead `docs/plans`
  reference in `backend.go` is also fixed. The same sweep now covers
  `cmd/dezhban/main.go`, whose comments still called the watcher's job "the
  legacy kill switch" and described `print-rules` populating a legacy
  allowlist it no longer builds.
- `print-rules --mode` help text still offered `legacy` alongside the live
  modes; it now lists `guard, fullblock, or switch` — matching what the
  command actually accepts and what the completions suggest.
- Every `docs/*.md` path cited from Go/Swift source and shell scripts now
  points at the reorganized hierarchy — including the user-visible ones:
  the paths `dezhban upgrade` prints in its guidance and the one shown in
  the app's **About → Updates** pane.
- **`dezhban config set vpn.switchWindow 0` now disables manual switch windows**
  instead of being silently coerced back to the 5s default by `Normalize` — the
  same explicit-opt-out sentinel `vpn.reconnectWindow` already used. `config get`
  now reports the disabled state as `0s` rather than a negative duration, and
  `dezhban status` prints `switch window: off` instead of the raw sentinel.

## [0.6.0] - 2026-07-22

### Fixed

- **GUI main-thread crash on launch/settings.** `DezhbanCLI.exec` spawned a
  `Process` and blocked on `waitUntilExit`, which spins the calling thread's
  run loop instead of simply blocking; on the main thread this re-entered
  AppKit's display cycle mid-SwiftUI-body-evaluation and corrupted state,
  crashing on a null PC. The CLI's config-path resolution is now memoized
  behind a lock and split into a non-blocking `displayConfigPath` (safe in a
  `body`) and a blocking, background-only `resolvedConfigPath()`; every
  remaining main-thread call site was moved off-main or reads the memoized
  value, and `exec` now asserts `!Thread.isMainThread` so a regression fails
  loudly in development.
- Sidebar toggle misalignment/relocation-on-click in the macOS app: the
  window hosted its SwiftUI root via a bare `NSHostingView` instead of an
  `NSHostingController`, so `NavigationSplitView`'s toolbar-based sidebar
  toggle couldn't install into the titlebar and SwiftUI drew its own inline
  toggle instead.
- Dock icon states now match what `PostureUI.dockState` actually reports
  (`on`/`blocked`); the unused intermediate asset states were removed rather
  than left to silently bit-rot.

## [0.5.0] - 2026-07-22

### Added

- **`dezhban upgrade check|download|apply`** — self-update for macOS. `check`
  is the only network call anywhere in the upgrade path and never runs in the
  root daemon (GUI on launch/~24h, or CLI on demand); `download` fetches and
  verifies the `.pkg` into a root-owned staging directory so a local user can't
  swap it before `apply` installs it. Installing opens no enforcement gap (the
  running daemon keeps enforcing on its old inode while the files land) —
  only *activating* (the restart) is the exposure, and it's gated on
  `internal/update.CanActivate` (healthy `guard`/`standby` only, re-checked at
  the instant of restart). The menubar app surfaces the same flow under
  **About → Updates**, with one confirmation and a self-relaunch. See
  [docs/upgrade.md](docs/usage/upgrade.md) and
  [ADR-0007](docs/adr/0007-upgrade-disclosed-window-not-holding-block.md).
- curl/PowerShell installers plus `.deb`/`.rpm` packaging, wired into CI
  alongside the existing macOS `.pkg`.
- `vpn.advanced.reconnectWindowMax` (default `10m`) — an independent hard cap
  for the automatic reconnect window, kept separate from
  `vpn.advanced.switchWindowMax` (see Changed below).
- The macOS app's Settings pane now also exposes `pollInterval` and
  `vpn.reconnectWindow`, previously config-file-only.
- **Reset to Defaults** in the macOS app's Settings pane. Runs
  `dezhban config reset --all` rather than carrying a second copy of the
  defaults in Swift, so `config.Default()` stays the only place they live, and
  inherits that command's identity carve-out: blocked countries, tunnel
  interfaces, endpoints, and saved profiles are preserved, so a reset can never
  silently unblock a country or forget your VPN.

### Changed

- **The curl/PowerShell installers now tell a first-time install from an
  upgrade.** Both read the outgoing version before anything is overwritten and
  branch their closing guidance on it: a fresh install still gets the
  `setup` + `start` walkthrough, while an upgrade or same-version reinstall
  drops `setup` entirely — it would have walked an existing user through
  replacing a config they already had — and instead reports `old -> new`,
  states that config and learned state were left untouched, and says whether
  the service was restarted or left stopped. A prior binary too old or broken
  to report its version classifies as an upgrade, never as fresh.

- **The VPN Guard and Settings sections of the macOS app are merged into one
  Settings pane.** The two sections split VPN keys along a seam that didn't
  match how they relate (`switchWindow`/`endpointGrace` lived in Settings,
  `endpointRefresh`/`tunnelWatch` in VPN Guard); merging removes it. The
  combined pane now awaits the restarted daemon's posture on Apply (as VPN
  Guard always did), since it carries guard-affecting keys.
- **Defaults retuned for a safer, less configuration-dependent out-of-box
  posture** (2026-07-22 defaults review):
  - `vpn.autodetect` and `vpn.autoDiscoverEndpoints` now default `true`
    (previously `false`, with `autodetect` only implied when no
    `tunnelInterfaces` were pinned). Explicit `tunnelInterfaces` still win;
    set either to `false` explicitly to opt out.
  - `blockedCountries` now defaults to `IR,RU,KP` **when the key is absent**.
    An explicit `blockedCountries: []` is a deliberate "block nothing" and is
    never overridden.
  - `vpn.switchWindow` default drops `15s` → `5s`, and its floor (`10s`) is
    removed entirely — any positive duration up to the cap now validates.
    `vpn.advanced.switchWindowMax` drops `5m` → `3m`.
  - `vpn.reconnectWindow`'s floor (`5s`) is also removed. It now has its own
    independent cap, `vpn.advanced.reconnectWindowMax` (default `10m`), no
    longer sharing `switchWindowMax` — sharing one cap between the two
    triggers would have silently truncated whichever trigger has the larger
    intended budget.
  - `vpn.advanced.windowDiscoveryInterval` drops `2s` → `1s`, so the new
    shorter `switchWindow` default still gets several discovery ticks.
- Upgrade stash lifecycle hardening: the upgrade stash is now classified
  against the *running* version rather than the one on disk, closing a gap
  where a stash could be misjudged after a partial or interrupted upgrade.
  Checksum/verify hardening and a CI seal check were added alongside it.

## [0.4.0] - 2026-07-21

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

- **`vpn.allowLocalNetwork` (default `true`) — LAN devices keep working while the
  guard is armed.** There was previously **no local-network handling anywhere**:
  none of the three backends contained a single reference to RFC1918, link-local
  or multicast, so arming the guard made printers, NAS, the router's admin page,
  AirPlay/Chromecast and local dev servers unreachable with no setting to get
  them back. The passes are **destination-scoped**, never interface-scoped, so
  they cannot become an internet path — packets to public addresses stay blocked
  whatever the next hop is — and they cost nothing against the threat model,
  since this traffic never leaves the building. Multicast is included because
  mDNS/SSDP is what actually makes discovery work; unicast alone would leave
  devices visible but undiscoverable. Set `false` on untrusted networks, where
  the real cost is that other devices there can reach you.
  See [ADR-0005](docs/adr/0005-allow-local-network-by-default.md).
- `dezhban status` now prints an `also reachable:` line naming exactly what stays
  open on the physical link (local network, DNS, or neither). These are the only
  standing exceptions to "only the tunnel may egress", so they should not have to
  be inferred from the config file.
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
  [glossary](docs/concepts/glossary.md) fixing the "guard"/"protection"/"kill switch"
  vocabulary drift. GUARD is the canonical term.

### Fixed

- **The recovery probe no longer lifts the guard — a recurring leak is gone.**
  While in FULL BLOCK, observing the exit country meant applying the **GUARD**
  ruleset (full tunnel egress) for up to 8 seconds on *every* probe tick, just to
  make one HTTP request, for as long as a forbidden exit persisted. FULL BLOCK
  now carries a standing pass scoped to the tunnel interface **and** the geo
  providers' addresses, so the lookup completes with no rule change and no leak.
  The double scoping is load-bearing: with the tunnel down the lookup fails and
  the posture holds — correct, since there is no exit to measure — whereas a pass
  on the *physical* link would succeed and report the ISP's country, silently
  defeating the check. Provider IPs refresh on the endpoint cadence, since
  CDN-fronted providers rotate. If none resolve, recovery falls back to the old
  lift-and-probe rather than losing the ability to recover at all.
  The provider rule deliberately carries **no DNS pass**: a tunnel-scoped but
  destination-unscoped `port 53` rule would send *every application's* DNS
  through the tunnel to the forbidden exit's resolver for as long as FULL BLOCK
  lasted, handing the exit whose country we are refusing a running log of every
  hostname the host looks up. The set is refreshed while the guard is healthy,
  and a mid-block rotation falls back to lift-and-probe, which heals it.
  See [ADR-0006](docs/adr/0006-geo-providers-tunnel-scoped.md).
- **The local-network pass no longer includes globally-routable multicast.**
  `224.0.0.0/4` and `ff00::/8` were shorthand for "multicast", but they contain
  scopes designed to cross the internet (`232/8` SSM, `233/8` GLOP, `ff0e::/16`
  global) — which a pass justified by "this traffic never leaves the building"
  must not contain. Narrowed to the local and administratively-scoped ranges
  that discovery actually uses: `224.0.0.0/24`, `239.0.0.0/8`, `ff02::/16`,
  `ff05::/16`. mDNS, Bonjour, SSDP, AirPlay and Chromecast are unaffected.
- Invalid addresses dropped at the policy seam are now logged. Dropping is
  correct — one `invalid IP` entry would make pf reject the whole ruleset — but
  a silent drop of a VPN endpoint presents as a tunnel that will not handshake,
  with nothing connecting the two.
- FULL BLOCK now carries tunnel **groups** as well as concrete interfaces, so a
  host that names only an interface class (`utun`) gets a scoped provider pass
  instead of silently degrading to lift-and-probe.
- **Failed exit-country lookups are now classified instead of all being reported
  as errors.** Three causes collapsed into one alarming message, and the most
  common was not a fault at all: during a switch or reconnect window the tunnel
  is *supposed* to be down — that is why the window exists — so there is no VPN
  exit to measure and the lookup failing is correct behaviour. That is now
  reported as a state (`exitUnknown`: "no tunnel is up, so there is no VPN exit
  to check") rather than an error. `lookupErr` is reserved for a failure with a
  tunnel **up**, where there really was an exit to measure and something went
  wrong — which may mean the exit itself is censoring the geo providers. The two
  fields are mutually exclusive; the macOS app and `status --json` render them
  differently. This is what made the geo providers look broken during every
  window.
- **IPv4-in-IPv6 addresses are now unmapped at the policy seam — a silent lockout.**
  pf does *not* reject `::ffff:1.2.3.4`; verified with `pfctl -nvf`, it accepts
  the rule and expands it to `pass out quick inet6 … to ::ffff:1.2.3.4`. Real
  IPv4 traffic never matches that, so the pass is effectively absent while
  looking perfectly present in `pfctl -sr` — and when the address is a VPN
  endpoint, the tunnel's own handshake is blocked and the VPN can never connect.
  Callers each remembered `.Unmap()` individually except the learned-endpoint
  reload, which is exactly how a per-caller convention fails. Now normalised once
  in `firewall.PolicyInput` so no backend or caller has to defend itself.
  Invalid addresses are dropped rather than rendered, since the zero
  `netip.Addr` stringifies to `invalid IP` — a ruleset pf genuinely does reject,
  turning one bad entry into a total failure to install any rules.

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
  `sudo` (`pam_tid`) — see [docs/usage.md](docs/usage/cli.md#touch-id).

### Changed

- **Makefile replaced by a [Taskfile](https://taskfile.dev)** (`task` lists everything).
  All targets carried over 1:1, plus two new update-roll loops for testing:
  `task dev:all` (fast: rebuild + swap CLI and app in place, restart daemon, relaunch)
  and `task pkg:cycle` (full: cross-compile, build the `.pkg`, install it, open the
  app), with `pkg:fresh`/`pkg:install`/`pkg:uninstall` piecewise variants. The
  `scripts/*.sh` escape hatches still run standalone without `task`. See
  [docs/development.md](docs/contribute/development.md).

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

[Unreleased]: https://github.com/Behnam-RK/dezhban/compare/v0.6.0...HEAD
[0.6.0]: https://github.com/Behnam-RK/dezhban/releases/tag/v0.6.0
[0.5.0]: https://github.com/Behnam-RK/dezhban/releases/tag/v0.5.0
[0.4.0]: https://github.com/Behnam-RK/dezhban/releases/tag/v0.4.0
[0.3.0]: https://github.com/Behnam-RK/dezhban/releases/tag/v0.3.0
[0.2.0]: https://github.com/Behnam-RK/dezhban/releases/tag/v0.2.0
[0.1.0]: https://github.com/Behnam-RK/dezhban/releases/tag/v0.1.0
