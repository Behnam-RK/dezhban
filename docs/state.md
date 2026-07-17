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
  "posture": "allow",                   // allow | block | guard | full-block | switch-window | stopped
  "blocked": false,                     // egress currently cut
  "ip": "203.0.113.45",
  "countryCode": "US",
  "provider": "ipinfo.io",
  "lookupErr": "",                      // last geo-lookup error, omitted when none
  "enforcementErr": "",                 // last firewall-action failure, omitted when clear
  "tunnels": [                          // (vpn)
    { "name": "utun4", "up": true, "detail": "utun4 up" }
  ],
  "endpoints": ["198.51.100.7"],        // (vpn) resolved VPN endpoints
  "pollIntervalSeconds": 30,            // daemon poll cadence, for sizing staleness
  "blockedCountries": ["IR"],
  "pid": 4242,
  "activeProfile": "proton",            // (vpn) profile of the last completed switch window; omitted until one completes
  "switch": {                            // (vpn) present only while a switch window is open
    "open": true,
    "until": "2026-07-01T12:02:00Z",
    "profile": "newvpn"
  }
}
```

`enforcementErr` is distinct from `lookupErr`: a geo-lookup failure is expected and
handled by fail-closed, but a non-empty `enforcementErr` means the daemon **tried to
apply a firewall change and the backend rejected it** — so `posture`/`blocked`
describe the data plane truthfully, but the *intended* posture was not achieved (e.g.
a failed block leaves `posture: "allow"` during a live leak, and a failed VPN probe
re-cut can leave egress open). Observers should surface it prominently regardless of
posture — the menubar app shows a red warning icon whenever it is set.

On a terminal `posture: "stopped"` snapshot, `enforcementErr` carries **why the
daemon went down** when the exit was not a clean shutdown: a startup refusal (e.g.
the VPN guard's "refusing to start: the tunnel is up but no server address is
known") or a run-loop failure. A clean, operator-requested stop leaves it empty —
so `stopped` + `enforcementErr` reads as "the daemon would not run", not "you
stopped it".

## The rest of the state directory

`state.json` is one of four things the daemon keeps in `/var/db/dezhban`. They are
easy to confuse, and only one of them is a *capability*:

| File | Mode | What it is |
|---|---|---|
| `state.json` | `0644` | This file — a **report**. Read-only observability; never affects enforcement. |
| `learned.json` | `0644` | VPN endpoints learned during a switch window. Daemon-owned — **never** written back into your config. |
| `command.json` | `0600` root | A **capability**: the root-only command channel (switch open/cancel, forget-learned). Consumed once, and the daemon re-verifies its owner and mode on every read. |
| `control.sock` | `0660` root:group | The control socket — passwordless `block`/`unblock`/`switch` for the `control.group`. |

The directory itself is `0755`, which is deliberate: the menubar app runs as the
logged-in user and must be able to read `state.json`. The cost — any local user can
read your public IP, resolved country, tunnel names, and VPN endpoint — and the
reasoning behind accepting it are spelled out in
[architecture.md](architecture.md#what-the-state-directory-exposes).

## Consuming it

- **Machine-readable status:** `dezhban status --json` reads this file and merges
  it with authoritative service state (from the OS service manager) and config
  summary — the stable contract for tooling.
- **Staleness:** treat the daemon as stopped/unknown when the file is missing or
  `time` is older than a few poll intervals. Size the threshold off
  `pollIntervalSeconds` (the menubar app uses `max(90 s, 3 × pollInterval)`) rather
  than a fixed constant, so a deliberately long `pollInterval` doesn't read as
  stopped. A clean shutdown publishes a final `posture: "stopped"` snapshot, but a
  crash cannot, so still rely on staleness rather than only the sentinel.
