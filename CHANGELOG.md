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

- **Standalone macOS installer** (`dezhban-<version>.pkg`, `make pkg-macos`):
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
