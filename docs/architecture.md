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
- Default to **fail-closed**: when the country can't be determined, block. But the
  allowlist (loopback + DNS + geo-API egress) must stay open so recovery detection
  still works, or the machine can lock itself out.

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

See [plans/readme.md](plans/readme.md) for the phase-by-phase build history and the
locked design decisions behind these choices.
