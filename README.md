# dezhban

> Persian *dežbān* (دژبان) — "gatekeeper / garrison guard."

A standalone, cross-platform **network kill switch** written in Go. It polls the
machine's public IP, resolves its country, and when that country matches a
blocklist it drives the OS firewall to cut traffic — while keeping a minimal
allowlist open so recovery detection can still fire and the machine can recover
on its own.

> [!WARNING]
> dezhban deliberately cuts network access. A bad allowlist, a crash before
> teardown, or running it over a remote session can **lock you out of your own
> machine**. Read [Safety](#safety) before running `block` for real.

## Status

Built phase-by-phase. Each phase is an independently buildable unit with its own
acceptance checks — see [`docs/plans/`](docs/plans/) (`README.md` is the index).

| Phase | What | State |
|------:|------|-------|
| 0 | Project scaffold, CLI, config, logging, privilege checks | ✅ |
| 1 | Monitor layer — public IP → country, multi-provider polling | ✅ |
| 2 | macOS enforcement backend (`pfctl`) + manual `block`/`unblock`/`status` | ✅ |
| 3 | Wire monitor → decision → enforcement loop | ⏳ |
| 4 | Resilience (hysteresis, quorum, fail-modes) | ⏳ |
| 5 | Cross-platform backends (Linux nftables, Windows WFP) | ⏳ |
| 6 | Persistence / service install | ⏳ |
| 7 | Safety + packaging (`panic`, cross-compile) | ⏳ |

## How it works

Three layers; only the firewall layer is platform-specific.

```
Monitor    internal/monitor    polls public IP, resolves country   (platform-independent)
Decision   internal/decision   blocklist + hysteresis + fail-mode → Block/Allow  (platform-independent)
Firewall   internal/firewall   FirewallBackend per OS              (ONLY platform-specific part)
```

The `FirewallBackend` interface (`internal/firewall/backend.go`) is the seam that
keeps ~90% of the code shared across operating systems:

- **macOS** → shell out to `pfctl`, dedicated `dezhban` pf anchor (`pf_darwin.go`)
- **Linux** → `google/nftables` netlink, dedicated `dezhban` table (Phase 5)
- **Windows** → `tailscale/wf` WFP, tagged sublayer (Phase 5)

Backends are selected by build tags, so each target compiles only its own code.

## Install / build

Requires Go 1.26+.

```bash
go build ./cmd/dezhban           # build the binary
go install ./cmd/dezhban         # install to $GOBIN

# cross-compile
GOOS=linux GOARCH=amd64 go build ./cmd/dezhban
```

## Usage

```
dezhban <command> [flags]

Commands:
  run        Run the monitor→decision→enforcement loop      (Phase 3)
  block      Manually block network egress                  (root)
  unblock    Remove dezhban's firewall rules                (root)
  status     Show version, config, and current block state
  panic      Force-remove dezhban's rules even with no daemon (Phase 7, root)
  version    Print the version
```

Privileged commands (`run`, `block`, `unblock`, `panic`) require root/admin and
print a clear error otherwise.

```bash
go run ./cmd/dezhban status                       # inspect config + block state
go run ./cmd/dezhban run --dry-run                # poll & print country, no firewall
sudo dezhban block --config configs/dezhban.example.json
dezhban status                                    # → blocked: true
sudo dezhban unblock
```

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
  "providerQuorum": false,
  "logLevel": "info"
}
```

- `failClosed` — when the country can't be determined, block (security-first default).
- `allowlist.dns` / `allowlist.hosts` — kept reachable while blocking. The geo-API
  provider IPs are resolved and added automatically so recovery detection works.

## Safety

dezhban is a kill switch; treat every `block` as potentially self-inflicting
until teardown is proven on your machine.

- **Test on the local console**, not over SSH/VPN — a lock-out shouldn't also kill
  your way back in.
- Verify teardown *first*: `block` then immediately `unblock`, and confirm the
  anchor is empty: `sudo pfctl -a dezhban -s rules`.
- Keep a manual escape in another terminal: `sudo pfctl -a dezhban -F all` (flush
  our anchor) and, if pf was off before, `sudo pfctl -d` (disable pf).
- The allowlist must include loopback (implicit), DNS, and the geo-API egress IPs,
  or recovery can never fire and the block becomes permanent.

On macOS, `block` appends one `anchor "dezhban"` line to `/etc/pf.conf` (backed up
to `/etc/pf.conf.dezhban.bak` first) and loads rules into the kernel `dezhban`
anchor; `unblock` flushes the anchor and restores the backup. Because the rules
live in the kernel anchor, teardown works even if the blocking process was killed.

## Development

```bash
go build ./...                          # build everything
go vet ./...                            # static checks
go test ./...                           # all tests
go test ./internal/config -run TestLoad # one package / test
```

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
