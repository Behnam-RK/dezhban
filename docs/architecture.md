# Architecture

Three layers; only the firewall layer is platform-specific.

```
Monitor    internal/monitor    polls public IP, resolves country              (platform-independent)
Decision   internal/decision   blocklist + hysteresis + fail-mode → Block/Allow  (platform-independent)
Firewall   internal/firewall   FirewallBackend per OS                         (ONLY platform-specific part)
```

The **`FirewallBackend` interface** (`internal/firewall/backend.go`) is the seam
that keeps ~90% of the code shared across operating systems. Each backend shells
out to the OS's own firewall tooling (no netlink/WFP libraries are linked) and
tags every rule with the unique name `dezhban`, so teardown is surgical and never
touches unrelated rules:

- **macOS** → `pfctl`, dedicated `dezhban` pf anchor (`pf_darwin.go`)
- **Linux** → `nft`, dedicated `dezhban` nftables table (`nft_linux.go`)
- **Windows** → WFP via `netsh`/PowerShell, tagged sublayer (`wfp_windows.go`)

Backends are selected by build tags (`//go:build darwin|linux|windows`), so each
target compiles only its own backend. The two enforcement modes the backend
applies (interface guard vs destination allowlist) are described in
[modes.md](modes.md).

## State export

The live `run` daemon publishes its posture (IP, country, verdict, mode, tunnels)
to a world-readable JSON **state file** via an injected `Publish` callback in
`internal/runner` (writer: `internal/state`). It is best-effort observability and
**never affects enforcement**. `dezhban status --json` reads it, and the macOS
menubar app drives its icon from it. Full schema, location, and staleness
contract: [state.md](state.md).

## Control channels

Two one-way channels carry operator commands *into* the running daemon. They are
complementary, not alternatives — the file always works, the socket removes the
password prompt from the operations you perform every day.

| | **Command file** (`internal/command`) | **Control socket** (`internal/control`) |
|---|---|---|
| Path | `/var/db/dezhban/command.json` | `/var/db/dezhban/control.sock` |
| Who may write | root only (0600, root-owned dir) | the `control.group` (macOS: `admin`), via mode 0660 root:group |
| Shape | consume-once file, polled on a tick | unix socket, one JSON request per connection |
| Carries | switch open/cancel, forget-learned | ping, status, block, unblock, switch open/cancel |
| Works with no daemon | n/a (daemon consumes it) | no — the CLI falls back to acting on the firewall directly |

**The socket's trust boundary is filesystem permissions, and nothing else.**
dezhban is stdlib-only, so there is no `SO_PEERCRED` peer-credential check: whoever
can `open(2)` the socket is authorized. That is a deliberate trade, and it is
bounded by what the ops can actually do:

- `block` / `unblock` only move between postures the daemon's own state machine
  already sanctions (GUARD ↔ FULL BLOCK). They can never open egress *past* the
  guard, so the worst an unwanted caller achieves is cutting their own network.
- `switch-open` **can** relax the guard, bounded by the same clamp and 5-minute cap
  as always. It is the one genuinely-privileged op on the socket, which is why it
  has its own flag: `control.allowSwitchOps: false` forces it back to root-only.
- `panic` is deliberately **absent**. The lockout escape hatch must not depend on a
  daemon being alive, so it stays a direct, root-only firewall teardown.
- Service lifecycle (`install`/`uninstall`/`start`/`stop`) is absent for a simpler
  reason: a daemon cannot install, start, or stop itself.

**Say the cost out loud: "an admin could sudo anyway" is not the whole answer.**
`sudo` demands a password; the socket does not. So the group is not really "the
humans who administer this machine" — it is *every process running as one of them*.
A malicious binary the admin user runs, with no elevation and no prompt, can now
open a switch window and relax the guard for up to five minutes. Before the socket,
that required a password the malware did not have.

We ship it on anyway, because the window is bounded (clamped, ≤ 5m, auto-reverting
to the prior fail-closed posture) and because the alternative — a password prompt on
every routine block/unblock — is the kind of friction that gets a kill switch turned
off entirely. But an operator who does not want that trade has three ways out, in
increasing order of severity: `control.allowSwitchOps: false` (keeps passwordless
block/unblock, forces the guard-relaxing op back to root), `control.group: ""`
(root-only socket), `control.enabled: false` (no socket at all).

If the socket can't be created with the intended ownership, the daemon **fails
closed on the feature** — it logs a warning, runs without it, and routine ops go
back to asking for a password. Enforcement never depends on the socket.

### What the state directory exposes

`/var/db/dezhban` is `0755` and `state.json` / `learned.json` are `0644` — both
deliberate, and both a real disclosure worth naming. The menubar app runs as the
logged-in user and must read `state.json`, so a tighter mode is not available to us:
`0700` on the directory is precisely the bug that made the app report "stopped"
while the daemon was enforcing, and `0640 root:admin` would reintroduce it for any
*standard* (non-admin) user.

The price is that **any local user can read your public IP, resolved country, tunnel
interface names, and VPN server endpoint address**. That is posture metadata, not
credentials — there are no keys or secrets in the state directory — but on a
multi-user host it is readable by everyone on it. The one file in there that is a
capability rather than a report, `command.json`, stays `0600` root-owned, and the
daemon re-verifies its ownership and mode on every read (`internal/command`,
`Consume`) rather than trusting the directory to have kept it safe.

## Rules that must not be broken

These invariants are load-bearing — the whole design depends on them:

- **Never call `pfctl`/`nft`/WFP directly from `run` or `cmd/`** — go through
  `FirewallBackend`. That seam is what keeps the code cross-platform.
- Every firewall rule carries the unique tag/anchor/table name **`dezhban`**, so
  teardown (`Unblock`/`Cleanup`) is surgical and never touches unrelated rules.
- `Block` must be **idempotent** — re-block must not stack duplicate rules.
- `Cleanup()` must always be safe to call and is wired to run on shutdown
  (`defer` + `signal.NotifyContext`). A stale block-all rule can lock the user out
  of their own network — `panic` removes rules even with no daemon running.
- **An undeterminable country HOLDS the current posture — it never escalates.**
  The standing guard rule **is** the fail-closed block for physical leaks, so only
  a *successful* blocked-country reading escalates to FULL BLOCK, and only a
  successful allowed reading restores GUARD. Escalating on an unknown would cut
  the tunnel's own egress and livelock the reconnect that could fix the lookup.
  This lives in `decision.Evaluate`, which short-circuits on `r.Err != nil`
  without touching the hysteresis streak — so a blip neither commits a flip nor
  cancels one that real readings were counting toward. There is no `failClosed`
  switch; it belonged to the retired fallback model, where the firewall was open
  at rest and an unknown country was the only reason to cut anything.
- **One goroutine applies rules.** Every `Backend.Apply` call comes from the single
  run-loop goroutine in `internal/runner`. The window timer, command poll, tunnel
  watcher, geo ticks, and control-socket requests are all *select cases* in that one
  loop. The socket's accept goroutine parses and forwards over a channel; it never
  touches the Backend. Adding a new control path means adding a select case, not a
  goroutine that applies rules.

## Dependency strategy

Dependencies are deliberate. Stdlib for CLI (`flag`), config (JSON), logging
(`log/slog`), HTTP, and firewall control (shell out to the OS tooling). There are
three third-party modules:
[`kardianos/service`](https://github.com/kardianos/service) (cross-platform
service manager), [`charmbracelet/huh`](https://github.com/charmbracelet/huh) (the
interactive `setup` wizard only), and
[`charmbracelet/x/term`](https://github.com/charmbracelet/x) (TTY detection for the
sudo auto-elevation guard). The charm code stays off the daemon/enforcement path.
The Linux/Windows backends shell out to `nft` and `netsh`/PowerShell rather than
linking `google/nftables` / `tailscale/wf` — one consistent shell-out model. Don't
add `cobra`/`viper`/etc.; the deliverable is a dependency-light standalone binary.

Config is JSON with string durations; the on-disk shape is the `fileConfig` DTO in
`internal/config`, converted to a validated `Config`. Module path
`github.com/behnam-rk/dezhban`.

## Design decisions

The choices below were locked early and the codebase still rests on them. They are
recorded here because the rationale, not the choice, is the part that is expensive
to reconstruct.

Later decisions — and the alternatives examined for each — live as
**[architecture decision records](adr/README.md)**. Read those before reversing
anything they describe: several record choices that look wrong until you know the
failure they were built to prevent.

| Decision | Choice | Rationale |
|---|---|---|
| Language | **Go** | One static binary per OS, `go build` cross-compiles, no runtime to install |
| Platform order | **macOS first**, then Linux, then Windows | Prove one backend end-to-end, then port behind the `FirewallBackend` interface |
| Detection | **API-based**, offline IP-range hybrid deferred | Simple to start; robustness can be added once the loop is proven |
| Fail mode | **Fail-closed by construction** | The standing guard rules *are* the block, so there is no undeterminable-country decision to get wrong — an unknown holds the posture (see above). The old `failClosed` switch belonged to the retired fallback model ([ADR-0001](adr/0001-single-guard-mode.md)) |
| Enforcement primitive | **Interface-aware** — pass on tunnel + endpoint handshake, block physical | A destination-IP allowlist is meaningless under a full tunnel: pf/nft see only the outer packets to the VPN endpoint |
| Guard model | **Always-on interface guard — the only model** | A VPN drop is cut instantly, with a zero leak window. A reactive poller leaks for one poll interval, which is why the country-blocklist fallback was removed rather than kept as a peer ([ADR-0001](adr/0001-single-guard-mode.md)) |
| Resting posture | **STANDBY — no rules until a tunnel is observed** | A guard with no tunnel blocks everything, which is a blackout rather than security. This is the safety job `vpn.enabled: false` was quietly doing ([ADR-0002](adr/0002-standby-no-tunnel-posture.md)) |
| Recovery | **Wait for the VPN to return to an allowed country** | While full-blocked, observe the exit through a time-windowed probe and restore the guard once the exit is allowed again |

Two of these were revisited during the build and are worth naming as *deviations*,
since the reasoning is not obvious from the code:

- **The Linux and Windows backends shell out** to `nft` and `netsh`/PowerShell
  instead of linking [`google/nftables`](https://github.com/google/nftables) and
  [`tailscale/wf`](https://github.com/tailscale/wf), as originally planned. Shelling
  out mirrors the macOS `pfctl` backend, giving one consistent model across all three
  OSes and zero added dependencies. Those libraries remain the documented alternative
  if pure-Go enforcement is ever needed.
- **The control socket relaxes "root-triggered"** for routine ops. The full cost of
  that trade is spelled out under [Control channels](#control-channels) — it is a
  deliberate concession to usability, not an oversight.
- **The switch window gained a second sanctioned trigger** (2026-07): the
  [automatic reconnect window](modes.md#automatic-reconnect-window)
  (`vpn.reconnectWindow`, default 30s, `"0"` restores the original
  operator-only behavior). Field testing with rotating-pool anti-censorship
  VPNs (fresh Cloudflare-fronted server IP on nearly every connect) showed
  that "keep known endpoints open" can never cover a reconnect, making every
  drop a manual `switch` — an operator burden that pushed users toward running
  with the guard off entirely. The trade is explicit and bounded: a tunnel
  drop from healthy GUARD may expose the real IP for up to `reconnectWindow`
  seconds while the client redials; in exchange, reconnects and VPN switches
  are zero-interaction, and the guard still fail-closes on expiry. The
  alternatives were examined and rejected: standing port/protocol allows
  (443-fronted VPNs make any filter that admits the VPN admit the leak),
  per-app allows (not expressible across pf/nft/WFP), and provider IP feeds
  (Cloudflare-fronted means allowlisting a whole CDN). Zero-leak purists set
  `"0"` and lose nothing.
