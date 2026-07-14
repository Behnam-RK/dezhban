# Usage

```
dezhban [-v] <command> [flags]

Commands:
  run          Run the monitor→decision→enforcement loop          (root)
  block        Manually block network egress                      (root)
  unblock      Remove dezhban's firewall rules                    (root)
  status       Show version, config, service, and block state (--json for tooling)
  validate     Load + validate a config file (no root, no effects)
  print-rules  Print the ruleset a block/guard would apply, without applying it
  doctor       Diagnose VPN guard config (tunnels, endpoints, lockout risks)
  monitor      Live read-only view: IP, country, tunnel state, endpoints, verdict
  panic        Force-remove dezhban's rules even with no daemon   (root)
  install      Register dezhban as a boot-persistent OS service   (root)
  uninstall    Remove the OS service                              (root)
  start        Start the installed service                        (root)
  stop         Stop the installed service (removes firewall rules) (root)
  restart      Restart the installed service — apply a config change (root)
  detect-vpn   Print detected VPN tunnel interfaces for config
  switch       Open a bounded window to connect a brand-new VPN    (root)
  vpn          Manage VPN profiles and learned endpoints (list/add/remove/import/promote/forget)
  setup        Interactive wizard to create or update the config
  config       Inspect or change the config without hand-editing JSON
  completion   Print a shell completion script (bash|zsh|fish)
  version      Print the version

Global: -v / --verbose   override the configured log level to debug
```

`--config` is **optional**: when omitted, dezhban resolves the config from
`$DEZHBAN_CONFIG`, then the canonical system path (`dezhban config path` prints
it), then built-in defaults. So `dezhban run` / `monitor` / `validate` normally
need no path at all.

## Do I need a password?

Mostly, no. Once the daemon is running, the commands you use day to day go **to the
daemon** over its control socket and need no password at all:

| Command | Needs a password? |
|---|---|
| `block`, `unblock`, `switch` | **No** — the running daemon performs them (see [config.md](config.md#control-block)). Only if no daemon is listening do they fall back to acting on the firewall directly, which needs root. |
| `status`, `validate`, `print-rules`, `doctor`, `monitor`, `detect-vpn` | **No** — read-only, no root, no firewall effects. |
| `install`, `uninstall`, `start`, `stop`, `restart` | Yes — a daemon can't install, start, or stop itself. Rare (install-time). |
| `panic` | Yes — deliberately independent of the daemon, so the lockout escape hatch works when nothing else does. |
| `run` | Yes — it *is* the daemon. |
| `setup`, `config set`/`edit` | Yes, but only for the config write itself. |

`dezhban status` prints a `daemon control:` line saying which mode you're in.

### Why doesn't the password prompt take Touch ID?

The menubar app elevates through `do shell script … with administrator privileges`,
whose SecurityAgent dialog is **password-only** — it has never supported biometrics.
Touch ID there would require shipping a privileged helper (`SMAppService`) and going
through Authorization Services, which is a lot of attack surface to save a few
keystrokes on operations you should rarely perform. The prompts that remain are
install-time and emergency ones; the *routine* ops need no authentication at all.

For the CLI, Touch ID works with `sudo` once you enable it system-wide (macOS 14+):

```sh
sudo sh -c 'echo "auth       sufficient     pam_tid.so" > /etc/pam.d/sudo_local'
```

That's a change to your system's `sudo` configuration, not to dezhban — it applies
to every `sudo` you run, and it survives OS updates (unlike editing `/etc/pam.d/sudo`
directly). dezhban's auto-elevation goes through `sudo`, so `dezhban start` and
friends pick it up automatically.

When a command does need root and you're on an interactive terminal on unix,
dezhban **auto-re-runs itself under `sudo`** — so you rarely type `sudo` yourself.
Pass `--no-sudo` (or `DEZHBAN_NO_SUDO=1`) to opt out and get the plain "must run as
root" error; on Windows, and when there's no terminal (CI/pipes), it never
auto-elevates. Pass `--no-daemon` (or `DEZHBAN_NO_DAEMON=1`) to skip the control
socket and act on the firewall directly — the escape hatch for a wedged daemon.

A manual `block` **holds**: the daemon suspends its geo state machine until you
`unblock`, so an allowed country won't quietly undo what you asked for.

```sh
dezhban status                                    # config + service + block state
dezhban status --json                             # machine-readable (merges the state file)
dezhban run --dry-run                             # poll & print country, no firewall
sudo dezhban run --config /etc/dezhban/dezhban.json

# manual block / override
sudo dezhban block   --config configs/dezhban.example.json
sudo dezhban block   --force                      # cut ALL egress, ignore detection
sudo dezhban unblock
sudo dezhban panic                                # standalone teardown, no daemon needed
```

## Key flags

- `run --dry-run` — poll and print the country without touching the firewall.
- `block --guard` — install the VPN interface guard (see [modes.md](modes.md)).
- `block --force` — unconditional hard block of all egress (loopback + allowlist
  only), bypassing the VPN guard. The override when detection is wrong.
- `unblock --force` — accepted for symmetry (`unblock` is already unconditional).
- `--simulate-country IR` (on `monitor` and `run`) — force the verdict from
  anywhere, without a sanctioned IP.

## Diagnose & test safely (no root)

Inspect and validate before you risk a block — none of these touch the firewall:

```sh
dezhban validate    --config <config>                 # parse + validate, summarize
dezhban print-rules --mode guard --config <config>    # exact ruleset, not applied
dezhban doctor      --config <config>                 # tunnels, subnets, endpoint sanity
dezhban doctor --discover --config <config>           # macOS: find the VPN's real server IP
dezhban monitor     --config <config>                 # live: IP, country, tunnels, endpoints, verdict
```

`monitor` streams the live state the decision rests on; add `--once` for a single
snapshot. `print-rules --mode` takes `guard`, `fullblock`, or `legacy`. See
[config.md](config.md) for the full field reference and [troubleshooting.md](troubleshooting.md)
for the lockout-recovery runbook.

## Create & manage the config

You rarely need to touch JSON by hand. See [config.md](config.md#where-the-config-lives)
for where the file lives and the resolution order.

```sh
sudo dezhban setup                 # interactive wizard — builds/updates the config,
                                   # detects tunnels, previews the ruleset, then writes it
dezhban config path                # print the resolved config path
dezhban config show                # print the effective config as JSON
dezhban config get blockedCountries
sudo dezhban config set blockedCountries IR,RU   # set, validate, save
sudo dezhban config set vpn.enabled=true vpn.tunnelInterfaces=utun4 \
     vpn.autoDiscoverEndpoints=true                # several keys, one atomic write
sudo dezhban config edit           # open the config in $EDITOR, re-validated on save
```

`config set` takes either one `<key> <value>` pair or any number of `key=value`
pairs. The multi-pair form applies them all to one in-memory config, validates
**once**, and writes **once** — so there is no ordering to get right (a key that is
only legal alongside another, like `vpn.enabled`, can come first) and no
half-applied config if one value is rejected. It is also one privileged write, i.e.
one password prompt instead of one per key; the menubar app's VPN panel uses it for
exactly that reason.

`setup` needs an interactive terminal and reuses the same tunnel detection,
validation, and ruleset preview as `detect-vpn`/`validate`/`print-rules`. Writes to
the system path need root (hence `sudo`); a permission error prints a `sudo` hint.

## Connect & switch VPNs

After a one-time `setup`, run dezhban (or install the service) and connect any
VPN. Known VPNs need no ceremony; a brand-new one uses a switch window.

```sh
# Known VPNs — register once, then just connect/switch in the VPN's own app:
dezhban vpn add proton --endpoint nl-01.protonvpn.net
dezhban vpn import ~/wg0.conf          # WireGuard .conf / OpenVPN .ovpn / V2Ray JSON
dezhban vpn list                        # profiles + learned endpoints + active state

# A brand-new VPN whose server dezhban has never seen:
sudo dezhban switch                     # open a ~2m window; connect it in its app now
sudo dezhban switch --for 90s --name windscribe   # custom duration + attribution
sudo dezhban switch --cancel            # close the window early
dezhban switch --status                 # is a window open?
sudo dezhban vpn promote <name>         # make a learned endpoint permanent (see: vpn list)
sudo dezhban vpn forget <name>          # drop a learned endpoint
```

`switch` writes a root-owned control file the daemon consumes, then narrates the
window from the state file until it closes. See [modes.md](modes.md#switch-window--connecting-a-brand-new-vpn)
for the posture and the real-IP-exposure trade-off.

## Shell completion

```sh
source <(dezhban completion zsh)     # or bash; add to your ~/.zshrc / ~/.bashrc
dezhban completion fish | source     # fish
```

Completes subcommands, `--mode` values (`guard|fullblock|legacy`), the `config`
subcommands, and file paths for `--config`.

## Run as a service

On macOS the [installer](../README.md#install) (`dezhban-<version>.pkg`) does all of
this for you — it installs the CLI + app and registers the service in one step, with
one password prompt. It deliberately leaves enforcement stopped; run
`sudo dezhban setup` then `sudo dezhban start`. Everything below is the manual
equivalent, and the only path on Linux/Windows.

dezhban can install itself as a boot-persistent background service using one
cross-platform API (launchd on macOS, systemd/upstart/sysv on Linux, the Windows
Service manager). The service wraps the `run` loop, restarts on crash, and routes
logs to the platform logger (syslog/journald/Event Log).

```sh
sudo dezhban install --config /etc/dezhban/dezhban.json   # register (default path if omitted)
sudo dezhban start                                        # start now; also auto-starts on boot
dezhban status                                            # → service: installed, running
sudo dezhban stop                                         # stops AND removes firewall rules
sudo dezhban uninstall
```

`stop` cancels the run loop so its deferred `Cleanup()` removes every rule —
stopping the service never leaves a block-all rule behind. If the service crashes
while blocked, the rules persist by design (a kill switch must not fail open); use
`sudo dezhban panic` to flush them even with no daemon running.

## macOS menubar app

On macOS an optional native **menubar app** (`Dezhban.app`) shows the daemon's
live posture at a glance and offers click-to-control. It's a separate Swift/AppKit
target, so the Go binary keeps its zero-dependency, `CGO_ENABLED=0` promise. Build
it with `make gui-macos` (see [development.md](development.md)).

- **Status icon** — 🟢 allow/guard, 🔴 block/full-block, ⚪ stopped or stale;
  repainted about once a second.
- **Menu** — Start/Stop kill switch, Block now/Unblock, the VPN switch window
  (Switching VPN… / Cancel) when in VPN mode, **Run diagnostics…**, **Panic —
  force unblock…**, **Install/Uninstall service**, **VPN guard mode** (opens the
  validated in-app config panel), Open config file…, View logs, **About
  Dezhban…**, Launch at login (`SMAppService`), Quit. Items enable/disable from
  the current state.
- **Passwords** — Block, Unblock and the switch window go to the running daemon
  over its control socket and raise **no prompt at all**. Only the service lifecycle
  (Install/Uninstall/Start/Stop) and Panic raise the native admin prompt, because
  neither can be daemon-mediated. Each menu item's tooltip says which it will be.
- **Output & diagnostics** — Run diagnostics, panic, and install/uninstall
  capture their command output in a scrollable panel; View logs streams a scoped
  `log show`/`log stream` (or opens Console.app).

The app runs no IP/country poller of its own — it reads the daemon's state file
(see [state.md](state.md)), the single source of truth for what the daemon decided.
It is unsigned for local use (right-click → Open past Gatekeeper). The in-app
**VPN guard mode** panel edits `vpn.enabled` + tunnels/endpoints through the same
validation as `config set`, then restarts the daemon to apply. Design notes:
[plans/phase-8-macos-gui.md](plans/phase-8-macos-gui.md),
[plans/phase-10-gui-diagnostics.md](plans/phase-10-gui-diagnostics.md),
[plans/phase-11-gui-vpn-config.md](plans/phase-11-gui-vpn-config.md).
