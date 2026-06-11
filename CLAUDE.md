# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

**dezhban** (Persian: "gatekeeper") is a standalone, cross-platform **network
kill switch** written in Go. It polls the machine's public IP, resolves its
country, and when the country matches a blocklist it drives the OS firewall to
cut traffic — keeping a minimal allowlist so recovery detection keeps working.

Status: built phase-by-phase. See `docs/plans/` — `README.md` is the index;
each `phase-N-*.md` is an independently buildable unit with its own acceptance
checks. Implement and verify one phase before starting the next.

## Commands

```bash
go build ./...                      # build everything
go vet ./...                        # static checks
go test ./...                       # all tests
go test ./internal/config -run TestLoad   # a single package / test
go run ./cmd/dezhban status         # run a subcommand without installing

# cross-compile (Phase 7)
GOOS=linux GOARCH=amd64 go build ./cmd/dezhban
```

The binary's subcommands: `run`, `block`, `unblock`, `status`, `panic`, `version`.
Privileged commands (`run`, `block`, `unblock`, `panic`) require root/admin and
print a clear error otherwise.

## Architecture — three layers

```
Monitor    internal/monitor    polls public IP, resolves country   (platform-independent)
Decision   internal/decision   blocklist + hysteresis + fail-mode → Block/Allow  (platform-independent)
Firewall   internal/firewall   FirewallBackend per OS              (ONLY platform-specific part)
```

The **`FirewallBackend` interface** (`internal/firewall/backend.go`) is the seam
that keeps ~90% of the code shared. Rules per OS:

- **macOS** → shell out to `pfctl`, dedicated `dezhban` anchor (`pf_darwin.go`)
- **Linux** → `google/nftables` netlink, dedicated `dezhban` table (`nft_linux.go`)
- **Windows** → `tailscale/wf` WFP, tagged sublayer (`wfp_windows.go`)

Backends are selected by **build tags** (`//go:build darwin|linux|windows`), so
each target compiles only its own backend.

### Rules that must not be broken

- **Never call `pfctl`/`nft`/WFP directly from `run` or `cmd/`** — go through
  `FirewallBackend`. The whole design depends on that seam.
- Every firewall rule carries the unique tag/anchor/table name **`dezhban`** so
  teardown (`Unblock`/`Cleanup`) is surgical and never touches unrelated rules.
- `Block` must be **idempotent** (re-block must not stack duplicate rules).
- `Cleanup()` must always be safe to call and is wired to run on shutdown
  (`defer` + `signal.NotifyContext`). A stale block-all rule can lock the user
  out of their own network — `panic` (Phase 7) removes rules even with no daemon.
- Default to **fail-closed**: when the country can't be determined, block. But
  the allowlist (loopback + DNS + geo-API egress) must stay open so recovery
  detection still works, or the machine can lock itself out.

## Conventions

- **Dependencies are deliberate.** Stdlib for CLI (`flag`), config (JSON),
  logging (`log/slog`), HTTP. Only three real third-party deps, one per hard
  platform problem: `google/nftables`, `tailscale/wf`, `kardianos/service`. Don't
  add `cobra`/`viper`/etc. — the deliverable is a dependency-light standalone binary.
- Config is JSON with string durations (e.g. `"30s"`); on-disk shape is the
  `fileConfig` DTO in `internal/config`, converted to a validated `Config`.
- Module path `github.com/behnam-rk/dezhban` (adjust if the repo moves).
