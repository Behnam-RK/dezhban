# Dezhban

> Persian *dežbān* (دژبان) — "gatekeeper / garrison guard."

![dezhban — system-wide network kill switch](gui/assets/png/banner-1280x640.png)

A standalone, system-wide and cross-platform **network kill switch** written in Go, built for
running behind full-tunnel VPNs. Its primary mode is an **always-on interface
guard**: it lets traffic out only through the VPN tunnel, so the instant the
tunnel drops it cuts egress **instantly**, and it full-blocks when the VPN exit
switches to a forbidden country. On a drop it then opens a bounded, self-closing
[reconnect window](docs/modes.md#automatic-reconnect-window) (default 30s) so
your VPN can redial any server with zero interaction — set
`vpn.reconnectWindow: "0"` for the strict zero-leak-window behavior instead.

As a **fallback** for hosts *not* behind a VPN, it can instead poll the machine's
public IP, resolve its country, and cut traffic by destination when that country
matches a blocklist — best-effort, since a poller can only react after the next
poll. Both modes and how to choose are in [docs/modes.md](docs/modes.md); for
the full story of what happens from launch to teardown, read
[docs/how-it-works.md](docs/how-it-works.md).

> [!WARNING]
> dezhban deliberately cuts network access. A bad allowlist, a wrong VPN endpoint,
> a crash before teardown, or running it over a remote session can **lock you out
> of your own machine**. Read [docs/safety.md](docs/safety.md) before running
> `block` for real. The escape hatch is `sudo dezhban panic`.

## Install

### macOS — the installer (recommended)

Download **`dezhban-<version>.pkg`** from the
[Releases page](https://github.com/Behnam-RK/dezhban/releases). It installs the CLI
(`/usr/local/bin/dezhban`), the menubar app (`/Applications/Dezhban.app`), and
registers the background service — **asking for your password exactly once**.

After that, the everyday operations (**block**, **unblock**, **switching VPNs**)
never ask again: the background service performs them for you over a local control
socket. See [docs/config.md](docs/config.md#control-block) for the security model
and how to tighten it.

The installer does **not** start enforcement — a kill switch configured by guesswork
is how you get locked out of your own machine. Two steps to finish:

```sh
sudo dezhban setup     # choose your settings
sudo dezhban start     # arm it
```

> [!NOTE]
> The `.pkg` is **unsigned** (no Apple Developer certificate), so Gatekeeper blocks
> a double-click. Either install from the terminal —
> `sudo installer -pkg dezhban-<version>.pkg -target /` — or double-click, dismiss
> the warning, and approve it in **System Settings → Privacy & Security → Open
> Anyway**. On macOS 14 and earlier, right-click → **Open** also works.

To remove everything: `sudo sh /usr/local/share/dezhban/uninstall.sh`.

### Other platforms

Prebuilt binaries for macOS (arm64/amd64), Linux (amd64/arm64), and Windows (amd64)
are on the same Releases page: download `dezhban-<os>-<arch>` (or `.exe` on
Windows). `Dezhban-macos.app.zip` is the menubar app on its own, for people who
already have the CLI. `SHA256SUMS` is attached to every release for verification.

> [!NOTE]
> `Dezhban.app` from the zip is unsigned too — right-click → **Open** in Finder, or
> `xattr -dr com.apple.quarantine Dezhban.app`.

See [docs/releasing.md](docs/releasing.md) for how releases are cut.

## Quick start

Requires Go 1.26+.

```sh
task build                        # host build → ./dezhban (go-task; or: go build ./cmd/dezhban)

sudo dezhban setup                # interactive wizard — build the config, no JSON by hand
dezhban validate                  # confirm it (--config is optional; see docs/config.md)
dezhban monitor                   # live IP/country/tunnel/verdict, no firewall touched

sudo dezhban run                  # run the daemon (root; drives the firewall)
sudo dezhban panic                # always-available teardown, no daemon needed
```

`--config` is optional — dezhban resolves it from `$DEZHBAN_CONFIG` or the system
path (`dezhban config path`). Tab-completion: `source <(dezhban completion zsh)`.
The binary can also install itself as a boot-persistent service and ships an
optional macOS app (menubar + main window). Full command reference: [docs/usage.md](docs/usage.md).

## Modes at a glance

- **VPN guard** (`vpn.enabled: true`) — **primary/recommended.** Interface-aware,
  always on, instant cut on a drop (zero leak window with
  `vpn.reconnectWindow: "0"`; by default a bounded reconnect window follows the
  cut so the VPN can redial). Use it whenever you're behind a full-tunnel VPN.
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
| [docs/usage.md](docs/usage.md) | CLI commands, flags, service install, the macOS app. |
| [docs/architecture.md](docs/architecture.md) | Three-layer design and the invariants it rests on. |
| [docs/safety.md](docs/safety.md) | Kill-switch safety principles and teardown mechanics. |
| [docs/troubleshooting.md](docs/troubleshooting.md) | Lockout recovery and VPN-guard failure runbook. |
| [docs/development.md](docs/development.md) | Build, cross-compile, dev loop, CI, hooks. |
| [docs/releasing.md](docs/releasing.md) | Cutting a release, CHANGELOG discipline, unsigned macOS GUI. |
| [docs/state.md](docs/state.md) | The `state.json` posture file. |
| [docs/acceptance.md](docs/acceptance.md) | The on-host verification checklist CI can't run. |

## License

[MIT](LICENSE) © 2026 Behnam RK
