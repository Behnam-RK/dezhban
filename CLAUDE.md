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
- The `guard` / `fullblock` / `legacy` mode strings and the state-file JSON keys
  are stable identifiers (used by `print-rules --mode` and `status --json`) — do
  not rename them. "Primary" / "fallback" are documentation words only.

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
