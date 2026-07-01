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
  detect-vpn   Print detected VPN tunnel interfaces for config
  version      Print the version

Global: -v / --verbose   override the configured log level to debug
```

Privileged commands (`run`, `block`, `unblock`, `panic`, `install`, `uninstall`,
`start`, `stop`) require root/admin and print a clear error otherwise. The
inspect commands (`validate`, `print-rules`, `doctor`, `monitor`) are read-only —
no root, no firewall effects.

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

## Run as a service

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
- **Menu** — Start/Stop, Block/Unblock, VPN-mode indicator + Open config, View
  logs, Launch at login (`SMAppService`), Quit. Items enable/disable from the
  current state; privileged actions raise a native admin prompt via `osascript`.

The app runs no IP/country poller of its own — it reads the daemon's state file
(see [state.md](state.md)), the single source of truth for what the daemon decided.
It is unsigned for local use (right-click → Open past Gatekeeper); an in-app
VPN-mode toggle is deferred (the menu routes to **Open config…**). Design notes:
[plans/phase-8-macos-gui.md](plans/phase-8-macos-gui.md).
