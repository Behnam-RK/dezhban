# State file — `state.json`

The running daemon publishes its live posture to a JSON file so out-of-process
observers (the macOS menubar app, `dezhban status --json`, scripts) can read
**exactly what the daemon decided** without running their own IP/country poller.

## Location

| OS | Path |
|---|---|
| macOS / Linux | `/var/db/dezhban/state.json` |
| Windows | `%ProgramData%\dezhban\state.json` |

Written atomically (temp-file + rename) and `0644` (world-readable): the daemon
runs as root, but the reader is typically the unprivileged logged-in user. A
half-written file is never observed. Publishing is **best-effort** — a write
failure is logged at debug and never affects enforcement.

## When it updates

Only the live `run` daemon writes it — on every poll, verdict transition, tunnel
up/down edge, endpoint refresh, and at startup. `--dry-run` and the read-only
inspect commands (`validate`, `print-rules`, `doctor`, `monitor`) do not.

## Shape

Defined by `Snapshot` in `internal/state/state.go`. Keys are lowerCamelCase;
`time` is RFC3339. Fields marked *(vpn)* appear only in VPN guard mode.

```json
{
  "time": "2026-07-01T12:00:00Z",
  "mode": "legacy",                     // "vpn" | "legacy"
  "posture": "allow",                   // allow | block | guard | full-block | stopped
  "blocked": false,                     // egress currently cut
  "ip": "203.0.113.45",
  "countryCode": "US",
  "provider": "ipinfo.io",
  "lookupErr": "",                      // last lookup error, omitted when none
  "tunnels": [                          // (vpn)
    { "name": "utun4", "up": true, "detail": "utun4 up" }
  ],
  "endpoints": ["198.51.100.7"],        // (vpn) resolved VPN endpoints
  "blockedCountries": ["IR"],
  "pid": 4242
}
```

## Consuming it

- **Machine-readable status:** `dezhban status --json` reads this file and merges
  it with authoritative service state (from the OS service manager) and config
  summary — the stable contract for tooling.
- **Staleness:** treat the daemon as stopped/unknown when the file is missing or
  `time` is older than a few poll intervals (the menubar app uses 90 s). A clean
  shutdown does not guarantee a final "stopped" write (a crash can't write one),
  so rely on staleness rather than a sentinel.
