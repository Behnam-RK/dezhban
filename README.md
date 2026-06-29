# dezhban

> Persian *dežbān* (دژبان) — "gatekeeper / garrison guard."

A standalone, cross-platform **network kill switch** written in Go. It polls the
machine's public IP, resolves its country, and when that country matches a
blocklist it drives the OS firewall to cut traffic — while keeping a minimal
allowlist open so recovery detection can still fire and the machine can recover
on its own.

It is also **VPN-aware**: behind a full-tunnel VPN it runs an always-on interface
guard that cuts traffic the instant the tunnel drops (zero leak window) and
full-blocks when the VPN exit switches to a forbidden country. See
[VPN / full-tunnel mode](#vpn--full-tunnel-mode).

> [!WARNING]
> dezhban deliberately cuts network access. A bad allowlist, a crash before
> teardown, or running it over a remote session can **lock you out of your own
> machine**. Read [Safety](#safety) before running `block` for real. The escape
> hatch is `sudo dezhban panic`.

## How it works

Three layers; only the firewall layer is platform-specific.

```
Monitor    internal/monitor    polls public IP, resolves country   (platform-independent)
Decision   internal/decision   blocklist + hysteresis + fail-mode → Block/Allow  (platform-independent)
Firewall   internal/firewall   FirewallBackend per OS              (ONLY platform-specific part)
```

The `FirewallBackend` interface (`internal/firewall/backend.go`) is the seam that
keeps ~90% of the code shared across operating systems. Each backend shells out to
the OS's own firewall tooling and tags every rule with the unique name `dezhban`,
so teardown is surgical and never touches unrelated rules:

- **macOS** → `pfctl`, dedicated `dezhban` pf anchor (`pf_darwin.go`)
- **Linux** → `nft`, dedicated `dezhban` nftables table (`nft_linux.go`)
- **Windows** → WFP via `netsh`/PowerShell, tagged sublayer (`wfp_windows.go`)

Backends are selected by build tags, so each target compiles only its own code.
Dependencies are deliberately minimal — the only third-party module is
[`kardianos/service`](https://github.com/kardianos/service) (Phase 6); everything
else is stdlib.

## Install / build

Requires Go 1.26+.

```bash
go build ./cmd/dezhban           # build the binary
go install ./cmd/dezhban         # install to $GOBIN

make build           # host build, version-stamped, into ./dezhban
make build-all       # cross-compile all 5 targets into ./dist/
```

`make build-all` produces darwin arm64/amd64, linux amd64/arm64, and windows
amd64, each with the version stamped via `-ldflags -X main.version` (from
`git describe`). macOS still requires the system `pfctl` at runtime (shelled, not
linked).

## Usage

```
dezhban [-v] <command> [flags]

Commands:
  run          Run the monitor→decision→enforcement loop          (root)
  block        Manually block network egress                      (root)
  unblock      Remove dezhban's firewall rules                    (root)
  status       Show version, config, service, and block state
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

Privileged commands require root/admin and print a clear error otherwise.

```bash
dezhban status                                    # config + service + block state
dezhban run --dry-run                             # poll & print country, no firewall
sudo dezhban run --config /etc/dezhban/dezhban.json

# manual block / override
sudo dezhban block   --config configs/dezhban.example.json
sudo dezhban block   --force                      # cut ALL egress, ignore detection
sudo dezhban unblock
sudo dezhban panic                                # standalone teardown, no daemon needed
```

### Key flags

- `run --dry-run` — poll and print the country without touching the firewall.
- `block --force` — unconditional hard block of all egress (loopback + allowlist
  only), bypassing the VPN guard. The override when detection is wrong.
- `block --guard` — install the VPN interface guard (see below).
- `unblock --force` — accepted for symmetry (`unblock` is already unconditional).

### Diagnose & test safely (no root)

Inspect and validate before you risk a block — none of these touch the firewall:

```bash
dezhban validate    --config <config>                 # parse + validate, summarize
dezhban print-rules --mode guard --config <config>    # exact ruleset, not applied
dezhban doctor      --config <config>                 # tunnels, subnets, endpoint sanity
dezhban doctor --discover --config <config>           # macOS: find the VPN's real server IP
dezhban monitor     --config <config>                 # live: IP, country, tunnels, endpoints, verdict
```

`monitor` streams the live state the decision rests on; add `--once` for a single
snapshot. To drive detection from anywhere without a sanctioned IP, force the
verdict with `--simulate-country IR` (on `monitor` and `run`). `print-rules
--mode` takes `guard`, `fullblock`, or `legacy`. See
[docs/TROUBLESHOOTING.md](docs/TROUBLESHOOTING.md) for the lockout-recovery
runbook and [docs/CONFIG.md](docs/CONFIG.md) for the full config reference.

## Configuration

JSON, with durations as strings (e.g. `"30s"`). See
[`configs/dezhban.example.json`](configs/dezhban.example.json):

```json
{
  "pollInterval": "30s",
  "blockedCountries": ["RU", "IR"],
  "failClosed": true,
  "hysteresis": 3,
  "providers": [
    "https://ipinfo.io/json",
    "http://ip-api.com/json",
    "https://ifconfig.co/json"
  ],
  "allowlist": { "dns": ["1.1.1.1", "8.8.8.8"], "hosts": [] },
  "vpn": {
    "enabled": false,
    "tunnelInterfaces": ["utun4"],
    "endpoints": [],
    "autodetect": false
  },
  "providerQuorum": false,
  "logLevel": "info"
}
```

- `failClosed` — when the country can't be determined, block (security-first default).
- `hysteresis` — consecutive agreeing readings required before toggling state.
- `providerQuorum` — require a majority of providers to agree on the country.
- `allowlist.dns` / `allowlist.hosts` — kept reachable while blocking. The geo-API
  provider IPs are resolved and added automatically so recovery detection works.
- `vpn` — interface-guard config; see [VPN / full-tunnel mode](#vpn--full-tunnel-mode).

## Run as a service

dezhban can install itself as a boot-persistent background service using one
cross-platform API (launchd on macOS, systemd/upstart/sysv on Linux, the Windows
Service manager). The service wraps the `run` loop, restarts on crash, and routes
logs to the platform logger (syslog/journald/Event Log).

```bash
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

## VPN / full-tunnel mode

Under a full-tunnel VPN the firewall on the physical interface sees only encrypted
outer packets to one address — the VPN endpoint — so a destination-IP allowlist is
the wrong primitive. dezhban instead runs an **interface-aware guard** with two
states:

- **GUARD** (exit allowed) — pass loopback + tunnel egress + the endpoint
  handshake, block all other physical egress. Always on, so a tunnel drop is cut
  with zero leak window.
- **FULL BLOCK** (exit forbidden / country unknown) — cut the tunnel too. Recovery
  uses a time-windowed probe: each tick the guard is briefly lifted for one geo
  lookup, then re-cut.

Enable it in config (`vpn.enabled: true`) with the tunnel interface(s) and VPN
endpoint IP(s). Use the helper to find your tunnel interface:

```bash
dezhban detect-vpn          # prints detected tunnel iface(s) + a paste-ready vpn block
```

`detect-vpn` deliberately does **not** autodetect the endpoint — a wrong endpoint
would leak physical egress — so set `vpn.endpoints` from your VPN client's own
config. The endpoint and a stale guard can lock you out just like a stale block;
`panic` tears down both GUARD and FULL-BLOCK rules.

## Safety

dezhban is a kill switch; treat every `block` as potentially self-inflicting
until teardown is proven on your machine.

- **Test on the local console**, not over SSH/VPN — a lock-out shouldn't also kill
  your way back in.
- Verify teardown *first*: `block` then immediately `unblock` (or `panic`), and
  confirm rules are gone (macOS: `sudo pfctl -a dezhban -s rules`).
- `sudo dezhban panic` is the always-available escape hatch: a standalone teardown
  that removes dezhban's tagged rules and restores saved prior state, whether or
  not a daemon owns them. Idempotent — a no-op on a clean system.
- The allowlist must include loopback (implicit), DNS, and the geo-API egress IPs,
  or recovery can never fire and the block becomes permanent.
- The allowlist pins provider IPs **at block time**. A provider behind a rotating
  CDN may resolve to a different IP later that isn't allowed, breaking recovery.
  Prefer providers with stable IPs, or pin a wide-enough `allowlist.hosts` range.
  (The `run` loop refreshes the allowlist live; manual `block` is static.)

On macOS, `block` appends one `anchor "dezhban"` line to `/etc/pf.conf` (backed up
to `/etc/pf.conf.dezhban.bak` first) and loads rules into the kernel `dezhban`
anchor; `unblock`/`panic` flush the anchor and restore the backup. Because the
rules live in the kernel anchor, teardown works even if the blocking process was
killed. Linux and Windows teardown is equivalently surgical: the whole `dezhban`
nftables table / WFP sublayer is removed, nothing else touched.

## Development

```bash
go build ./...                          # build everything
go vet ./...                            # static checks
go test ./...                           # all tests
go test ./internal/config -run TestLoad # one package / test

# cross-compile a single target
GOOS=linux GOARCH=amd64 go build ./cmd/dezhban
```

### Safe dev loop (no root)

The `Makefile` and `scripts/` wrap the read-only inspect commands so you can
iterate on rules and config without root and without risking a lockout:

```bash
make validate CONFIG=configs/dezhban.dev.json    # parse + validate a config
make rules MODE=guard CONFIG=...                  # print the ruleset, don't apply it
make doctor CONFIG=... [ARGS=--discover]          # diagnose tunnels / lockout risks
make run-dry                                      # build + run the monitor, no firewall touch
```

The privileged flows have wrappers too — `make install-local` / `reinstall` /
`uninstall-local` / `panic`, mirrored by `scripts/*.sh`. Sample configs live in
`configs/` (`dezhban.dev.json`, `dezhban.vpn-guard.json`).

### CI

`.github/workflows/ci.yml` runs `go vet` + `go test` on macOS, Linux, and Windows
(with `-race` on Linux) and a `build-all` cross-compile, so the per-OS build-tag
backends can't silently break.

### Pre-commit hook

A native git hook (no extra dependencies) runs gofmt, `go vet`, `go build`, and
`go test` before each commit. Enable it once after cloning:

```bash
git config core.hooksPath .githooks
```

See [`CLAUDE.md`](CLAUDE.md) for the architecture invariants the design depends on
(the `FirewallBackend` seam, idempotent `Block`, always-safe `Cleanup`, fail-closed
defaults).

## License

[MIT](LICENSE) © 2026 Behnam RK
