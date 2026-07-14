# Changelog

All notable changes to **dezhban** are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

Releases are cut with the manually-dispatched `release` workflow, which rewrites
the `## [Unreleased]` section below into a versioned entry — see
[docs/releasing.md](docs/releasing.md). Keep `## [Unreleased]` current as you land
changes.

## [Unreleased]

### Added

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
  `uninstall`, `start`, `stop`, `detect-vpn`, `validate`, `print-rules`, `doctor`,
  `monitor`, `setup`, `version`, plus a global `-v`/`--verbose`.
- Read-only diagnostics that need no root: `validate`, `print-rules`, `doctor`,
  `monitor`.
- macOS **menubar GUI** (`Dezhban.app`, `make gui-macos`): a standalone Swift
  client that reads the daemon state file and drives the CLI.
- Cross-platform release build matrix (`make build-all`) producing five binaries:
  darwin/arm64, darwin/amd64, linux/amd64, linux/arm64, windows/amd64.

[Unreleased]: https://github.com/Behnam-RK/dezhban/commits/main
