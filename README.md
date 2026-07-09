# dezhban

> Persian *dežbān* (دژبان) — "gatekeeper / garrison guard."

A standalone, cross-platform **network kill switch** written in Go, built for
running behind a full-tunnel VPN. Its primary mode is an **always-on interface
guard**: it lets traffic out only through the VPN tunnel, so the instant the
tunnel drops it cuts egress with a **zero leak window**, and it full-blocks when
the VPN exit switches to a forbidden country.

As a **fallback** for hosts *not* behind a VPN, it can instead poll the machine's
public IP, resolve its country, and cut traffic by destination when that country
matches a blocklist — best-effort, since a poller can only react after the next
poll. Both modes and how to choose are in [docs/modes.md](docs/modes.md).

> [!WARNING]
> dezhban deliberately cuts network access. A bad allowlist, a wrong VPN endpoint,
> a crash before teardown, or running it over a remote session can **lock you out
> of your own machine**. Read [docs/safety.md](docs/safety.md) before running
> `block` for real. The escape hatch is `sudo dezhban panic`.

## Install

Prebuilt binaries for macOS (arm64/amd64), Linux (amd64/arm64), and Windows
(amd64), plus a macOS menubar app, are published on the
[Releases page](https://github.com/Behnam-RK/dezhban/releases) — download
`dezhban-<os>-<arch>` (or `.exe` on Windows) and `Dezhban-macos.app.zip` for the
GUI. `SHA256SUMS` is attached to each release for verification.

> [!NOTE]
> `Dezhban.app` is **unsigned** (no Apple Developer certificate) — Gatekeeper
> blocks a plain double-click on first launch. Right-click → **Open** in
> Finder, or run `xattr -dr com.apple.quarantine Dezhban.app`.

See [docs/releasing.md](docs/releasing.md) for how releases are cut.

## Quick start

Requires Go 1.26+.

```sh
make build                        # host build → ./dezhban

sudo dezhban setup                # interactive wizard — build the config, no JSON by hand
dezhban validate                  # confirm it (--config is optional; see docs/config.md)
dezhban monitor                   # live IP/country/tunnel/verdict, no firewall touched

sudo dezhban run                  # run the daemon (root; drives the firewall)
sudo dezhban panic                # always-available teardown, no daemon needed
```

`--config` is optional — dezhban resolves it from `$DEZHBAN_CONFIG` or the system
path (`dezhban config path`). Tab-completion: `source <(dezhban completion zsh)`.
The binary can also install itself as a boot-persistent service and ships an
optional macOS menubar app. Full command reference: [docs/usage.md](docs/usage.md).

## Modes at a glance

- **VPN guard** (`vpn.enabled: true`) — **primary/recommended.** Interface-aware,
  always on, zero leak window. Use it whenever you're behind a full-tunnel VPN.
- **Country-blocklist** (`vpn.enabled: false`) — **fallback.** Destination-aware,
  reactive; only meaningful when you're not tunneled. Defaults to off — a
  misconfigured guard can lock a host out, so VPN mode is a deliberate opt-in.

Details, rulesets, and the deciding question: [docs/modes.md](docs/modes.md).

## Configuration

JSON, with durations as strings (e.g. `"30s"`). Sample configs live in `configs/`
(`dezhban.vpn-guard.json` for the guard, `dezhban.example.json` for the fallback).
Full field reference, the `vpn` block, and validation rules:
[docs/config.md](docs/config.md).

## Documentation

| Doc | What's in it |
|---|---|
| [docs/modes.md](docs/modes.md) | The two enforcement modes and which one you want. |
| [docs/config.md](docs/config.md) | Config field reference and sample configs. |
| [docs/usage.md](docs/usage.md) | CLI commands, flags, service install, menubar app. |
| [docs/architecture.md](docs/architecture.md) | Three-layer design and the invariants it rests on. |
| [docs/safety.md](docs/safety.md) | Kill-switch safety principles and teardown mechanics. |
| [docs/troubleshooting.md](docs/troubleshooting.md) | Lockout recovery and VPN-guard failure runbook. |
| [docs/development.md](docs/development.md) | Build, cross-compile, dev loop, CI, hooks. |
| [docs/releasing.md](docs/releasing.md) | Cutting a release, CHANGELOG discipline, unsigned macOS GUI. |
| [docs/state.md](docs/state.md) | The `state.json` posture file. |
| [docs/plans/readme.md](docs/plans/readme.md) | Phase-by-phase plans and locked decisions. |

## License

[MIT](LICENSE) © 2026 Behnam RK
