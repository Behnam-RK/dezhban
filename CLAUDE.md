# CLAUDE.md

Guidance for Claude Code (claude.ai/code) when working in this repository.

## What this is

**dezhban** (Persian: "gatekeeper") is a standalone, cross-platform **network kill
switch** written in Go, built primarily for hosts behind a full-tunnel VPN. Its
main mode is an **always-on interface guard** (`vpn.enabled: true`): egress is
allowed only through the tunnel, so a tunnel drop is cut with a zero leak window,
and it full-blocks when the VPN exit lands in a forbidden country. A
**country-blocklist fallback** (`vpn.enabled: false`) polls the public IP and cuts
traffic by destination when the country is blocklisted — for hosts not behind a
VPN. See [docs/modes.md](docs/modes.md).

`vpn.enabled` defaults to `false`: the always-on guard can lock a host out if
misconfigured, so it is a deliberate safety opt-in — not a statement that the
fallback is the normal mode.

Built phase-by-phase; see [docs/plans/readme.md](docs/plans/readme.md) (index) —
each `phase-N-*.md` is an independently buildable unit with its own acceptance
checks. Implement and verify one phase before the next.

## Commands

```sh
go build ./...                            # build everything
go vet ./...                              # static checks
go test ./...                             # all tests
go test ./internal/config -run TestLoad   # a single package / test

# safe, root-free dev loop — none of these touch the firewall
make validate CONFIG=configs/dezhban.dev.json    # parse + validate
make rules MODE=guard CONFIG=...                  # print the ruleset, don't apply
make doctor CONFIG=... [ARGS=--discover]          # diagnose VPN guard / lockout risks

make build-all                            # all 5 targets into dist/, version-stamped
make gui-macos                            # macOS menubar app → dist/Dezhban.app (macOS only)
```

Subcommands: `run`, `block`, `unblock`, `status`, `panic`, `install`, `uninstall`,
`start`, `stop`, `detect-vpn`, `validate`, `print-rules`, `doctor`, `monitor`,
`version`, plus a global `-v`/`--verbose`. `validate`, `print-rules`, `doctor`,
and `monitor` are read-only (no root, no firewall effects); the rest of the
privileged set requires root/admin. Full reference: [docs/usage.md](docs/usage.md).

## Rules that must not be broken

The design depends on these invariants (rationale in
[docs/architecture.md](docs/architecture.md)):

- **Never call `pfctl`/`nft`/WFP directly from `run` or `cmd/`** — go through the
  `FirewallBackend` interface (`internal/firewall/backend.go`). That seam keeps
  ~90% of the code shared across OSes; backends are chosen by build tags.
- Every firewall rule carries the unique tag/anchor/table name **`dezhban`**, so
  teardown (`Unblock`/`Cleanup`) is surgical and never touches unrelated rules.
- `Block` must be **idempotent** — re-block must not stack duplicate rules.
- `Cleanup()` must always be safe to call and is wired to run on shutdown
  (`defer` + `signal.NotifyContext`). A stale block-all rule can lock the user
  out — `panic` removes rules even with no daemon.
- Default to **fail-closed** *in the fallback/legacy model*: block when the
  country is undeterminable, but keep the allowlist (loopback + DNS + geo-API
  egress) open so recovery can fire. **In VPN guard mode this is scoped
  differently:** the standing guard rule is itself the fail-closed block for
  physical leaks, so an undeterminable country *holds* the current posture — only
  a *successful* blocked-country reading escalates to FULL BLOCK. Escalating on
  an unknown would cut the tunnel's own egress and livelock the reconnect.
- The `guard` / `fullblock` / `legacy` / `switch` mode strings and the state-file
  JSON keys (including `switch-window`, `activeProfile`, `switch`) are stable
  identifiers (used by `print-rules --mode` and `status --json`) — do not rename
  them. "Primary" / "fallback" are documentation words only.
- **The switch window is the ONLY sanctioned relaxation of the guard.** It is
  bounded (default 2m, capped 5m), never automatic — it opens only on an explicit
  operator command — closes early on a confirmed good exit, and auto-reverts to the
  prior fail-closed posture on cancel/expiry. Never widen it, never let it open
  without an explicit command, and never let it outlive its deadline.
  Two channels can carry that command: the **root-owned command file**
  (`internal/command`, always available, root-only) and the **control socket**
  (`internal/control`, admin-group, gated by `control.allowSwitchOps`, default
  true). The socket is a deliberate, documented relaxation of "root-triggered" —
  admins get a passwordless switch — and `control.allowSwitchOps: false` restores
  root-only. Everything else about the window is unchanged: same clamp, same cap,
  same auto-revert.
- The daemon owns all `Backend.Apply` calls from the **single run-loop goroutine** —
  keep it that way. Window timer, command poll, watcher, geo ticks, **and
  control-socket requests** are all select cases in that one loop; the socket's
  accept goroutine only forwards requests over a channel and never touches the
  Backend. No other goroutine applies rules.
- **`panic` must never depend on the daemon.** It is the lockout escape hatch, so it
  is deliberately NOT a control-socket op — it removes rules directly, as root, with
  no daemon running. Same for service lifecycle (`install`/`uninstall`/`start`/`stop`):
  a daemon cannot manage its own lifecycle, so those keep requiring root.
- The tunnel-interface set is runtime-mutable (autodetect grows/prunes it), but
  **explicit `vpn.tunnelInterfaces` are pinned and never auto-pruned**, and the
  set never narrows to empty. Learned endpoints live in a daemon-owned
  `learned.json`, never written into the user's config.

## Conventions

- **Dependencies are deliberate.** Stdlib for everything except three third-party
  modules: `kardianos/service` (cross-platform service manager), `charmbracelet/huh`
  (the interactive `setup` wizard only), and `charmbracelet/x/term` (TTY detection so
  auto-sudo elevation is skipped on non-interactive stdin). The huh-driven wizard code
  stays out of the daemon/enforcement path; `x/term` is touched only by the
  elevation-guard TTY check. Linux/Windows backends shell out to `nft` /
  `netsh`/PowerShell rather than linking libraries. Don't add `cobra`/`viper`/etc. —
  the deliverable is still a dependency-light standalone binary; weigh any new dep
  against that.
- Config is JSON with string durations; on-disk shape is the `fileConfig` DTO in
  `internal/config`, converted to a validated `Config`. Field reference:
  [docs/config.md](docs/config.md).
- Architecture & invariants: [docs/architecture.md](docs/architecture.md).
  Lockout recovery / VPN-guard runbook: [docs/troubleshooting.md](docs/troubleshooting.md).
- Module path `github.com/behnam-rk/dezhban` (adjust if the repo moves).
