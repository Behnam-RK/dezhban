# CLAUDE.md

Guidance for Claude Code (claude.ai/code) when working in this repository.

## What this is

**dezhban** (Persian: "gatekeeper") is a standalone, cross-platform **network kill
switch** written in Go, for hosts behind a full-tunnel VPN. It has **one**
enforcement model: an **always-on interface guard**. Egress is allowed only
through the tunnel, so a tunnel drop is cut instantly — by default a bounded
reconnect window then follows so the VPN can redial (set
`vpn.reconnectWindow: "0"` for a strict zero-leak cut) — and it full-blocks when
the VPN exit lands in a forbidden country. See [docs/modes.md](docs/modes.md).

There used to be a second, `vpn.enabled: false` **country-blocklist fallback**
that cut traffic by destination IP. It is **gone**
([docs/adr/0001](docs/adr/0001-single-guard-mode.md)): it was not a peer of the
guard but a strictly weaker product, "best-effort, not a zero-leak guarantee" by
its own documentation, and only meaningful when the blocked country was the
user's real physical location. The guard already contains the country check —
that is what FULL BLOCK is.

`vpn.enabled` also did a second, unnamed job: it was the safety opt-in that kept
a misconfigured guard from locking a host out. That job now belongs to the
**STANDBY** posture ([docs/adr/0002](docs/adr/0002-standby-no-tunnel-posture.md)),
which installs no rules at all until a tunnel has been both configured *and*
observed up. Deleting the flag therefore removed a mode selector, not a guard rail.

`vpn.enabled`, `failClosed`, and `allowlist` are retired keys: still parsed, never
acted on, and reported by `dezhban validate` and at daemon start so nobody is left
believing a discarded security setting took effect.

The feature set is complete and the phase plans it was built from are retired (they
live in git history). What survives them is the verification they specified:
[docs/acceptance.md](docs/acceptance.md) is the standing checklist of privileged,
on-host checks that CI cannot run, and the design rationale is recorded under
"Design decisions" in [docs/architecture.md](docs/architecture.md).

## Commands

```sh
go build ./...                            # build everything
go vet ./...                              # static checks
go test ./...                             # all tests
go test ./internal/config -run TestLoad   # a single package / test

# safe, root-free dev loop — none of these touch the firewall
task validate CONFIG=configs/dezhban.dev.json    # parse + validate
task rules MODE=guard CONFIG=...                 # print the ruleset, don't apply
task doctor CONFIG=... -- --discover             # diagnose VPN guard / lockout risks

task build:all                            # all 5 targets into dist/, version-stamped (hidden task)
task gui:build                            # macOS menubar app → dist/Dezhban.app (macOS only, hidden task)
task dev                                  # fast roll: rebuild + swap CLI and app (macOS, sudo)
task install                              # full roll: build .pkg + install + launch (macOS, sudo)
```

Bare `task` on a TTY opens an interactive picker (`tools/taskmenu`, huh-based —
dev tooling only, never the daemon path); non-TTY prints the grouped menu
(`task help`). `task --list-all` shows hidden plumbing.

Subcommands: `run`, `block`, `unblock`, `status`, `panic`, `install`, `uninstall`,
`start`, `stop`, `restart`, `detect-vpn`, `validate`, `print-rules`, `doctor`, `monitor`,
`switch`, `vpn`, `setup`, `config`, `completion`, `upgrade`, `version`, plus a global
`-v`/`--verbose`. `validate`, `print-rules`, `doctor`, `monitor`, and `upgrade check`
are read-only (no root, no firewall effects); the rest of the privileged set
requires root/admin, including `upgrade download`/`upgrade apply` (macOS only —
`download`'s staging directory is root-owned so a local user can't swap the
verified `.pkg` before `apply` installs it). Full reference:
[docs/usage.md](docs/usage.md); the upgrade design in full:
[docs/upgrade.md](docs/upgrade.md).

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
- **An undeterminable country HOLDS the current posture — it never escalates.**
  The standing guard rule is itself the fail-closed block for physical leaks, so
  only a *successful* blocked-country reading may escalate to FULL BLOCK, and
  only a successful allowed reading may restore GUARD. Escalating on an unknown
  would cut the tunnel's own egress and livelock the very reconnect that could
  fix the lookup. This lives in `decision.Evaluate`, which short-circuits on
  `r.Err != nil` without touching the hysteresis streak — so a blip neither
  commits a flip nor cancels one that real readings were counting toward. There
  is **no `failClosed` switch**; it belonged to the retired fallback model, where
  the firewall was open at rest and an unknown country was the only reason to cut.
- **The FULL BLOCK geo-provider pass is scoped to the tunnel interface AND the
  provider addresses, and carries no DNS rule.** Never relax it to one half:
  destination-only (a pass on the *physical* link) would let the lookup succeed
  with the tunnel down and report the ISP's country — an allowed one — so FULL
  BLOCK would never fire and `finishCloseProbe` would close a window early on a
  bogus "good exit"; interface-only is just `ModeGuard`. And never re-add a
  `port 53` rule beside it: tunnel-scoped but destination-unscoped, it sends
  *every* application's DNS to the forbidden exit's resolver for as long as the
  block lasts. Providers are refreshed while the guard is healthy; a mid-block
  rotation correctly degrades to lift-and-probe, which heals it
  ([docs/adr/0006](docs/adr/0006-geo-providers-tunnel-scoped.md)).
- **`dezhban upgrade` never gets its own firewall pass, and the check that
  drives it never runs in the daemon.** The geo-provider pass above is
  already the only destination-scoped hole, tightly justified; a
  `pass to github.com` would be a second, weaker one — and unlike the geo
  pass, reachable even during FULL BLOCK. `upgrade check` therefore runs only
  in the GUI (user context, on launch + ~24h) or the CLI on demand, inherits
  the guard's tunnel-only routing for free, and simply fails if the tunnel is
  down — it does not open anything to succeed anyway. Applying is two
  phases: installing the `.pkg` opens no gap (the running daemon keeps
  enforcing on its old inode while the files land); only *activating* (the
  restart) is the exposure, and it is gated on `internal/update.CanActivate`
  — healthy `guard` or `standby` only, re-checked at the instant of restart,
  never `full-block` or an open switch window (restarting through FULL BLOCK
  would lift a block on a forbidden-country exit — the one thing this tool
  exists to prevent, caused by the updater). The upgrade path also never
  invokes `uninstall.sh` — that removes `/etc/dezhban` unless `KEEP_CONFIG=1`,
  and an upgrade must never touch config or learned state. See
  [docs/upgrade.md](docs/upgrade.md).
- **`vpn.allowLocalNetwork` passes destinations, never interfaces**, and only
  locally-scoped ones — an interface-scoped pass would carry internet traffic and
  silently disable the kill switch, and globally-routable multicast (`232/8`,
  `233/8`, `ff0e::/16`) has no place in a pass justified by "this traffic never
  leaves the building" ([docs/adr/0005](docs/adr/0005-allow-local-network-by-default.md)).
- The `guard` / `fullblock` / `switch` mode strings and the state-file JSON keys
  (including `switch-window`, `activeProfile`, `switch`) are stable identifiers
  (used by `print-rules --mode` and `status --json`) — do not rename them.
  **`legacy` was deliberately thawed and removed** with the fallback model
  ([docs/adr/0001](docs/adr/0001-single-guard-mode.md)); `print-rules --mode legacy`
  now errors by name rather than silently rendering something else. `Snapshot.Mode`
  and `status --json`'s `vpnEnabled` are gone too — a field with one possible
  value is noise, not compatibility.
- **The bounded switch window is the ONLY sanctioned relaxation of the guard,
  and it has exactly TWO sanctioned triggers** — nothing else may ever relax it:
  (1) an explicit operator command, via the **root-owned command file**
  (`internal/command`, always available, root-only) or the **control socket**
  (`internal/control`, admin-group, gated by `control.allowSwitchOps`, default
  true; `false` restores root-only); (2) the **automatic reconnect window**
  (`vpn.reconnectWindow`, default 30s, `"0"` disables — an explicit opt-out):
  a tunnel-down edge from *healthy GUARD only* — never from standby, FULL BLOCK,
  an already-open window, or a tunnel never observed up, and gated against
  flapping by `vpn.advanced.reconnectMinUptime`. Both triggers share the same
  machinery and rails — closes early on a confirmed good exit, auto-reverts to
  the prior fail-closed posture on cancel/expiry, one auto window per drop
  (expiry never re-opens) — but each has its OWN hard cap, deliberately never
  shared: the manual trigger is bounded by `switchWindowMax` (default 3m, no
  floor), the automatic one by `reconnectWindowMax` (default 10m, no floor).
  Collapsing these into one shared cap would silently truncate whichever
  trigger has the larger intended budget — the `Options.SwitchWindowMax` /
  `Options.ReconnectWindowMax` split in `internal/runner` and the per-episode
  `windowMax` selected by trigger at first open (`Run`'s `openWindow` closure)
  exist for exactly this reason. Never widen a window past its own cap, never
  add a trigger, never let either outlive its deadline.
- **Both windows are independently disableable, and "disabled" must survive
  `Normalize`.** `vpn.switchWindow: "0"` removes trigger (1);
  `vpn.reconnectWindow: "0"` removes trigger (2); both set to `"0"` is the strict
  zero-leak posture in which *nothing* can relax the guard. Each parses to the
  negative `config.Disabled` sentinel, because `Normalize` coerces a plain `0`
  back to the default — accepting a security setting and silently discarding it
  is the worst bug this tool can have. Disabling one must never disable the
  other: the manual path gates on `switchEnabled`, the automatic path gates only
  on `ReconnectWindow > 0`. `TestWindowDisableMatrix` pins all four permutations.
- The daemon owns all `Backend.Apply` calls from the **single run-loop goroutine** —
  keep it that way. Window timer, command poll, watcher, geo ticks, **and
  control-socket requests** are all select cases in that one loop; the socket's
  accept goroutine only forwards requests over a channel and never touches the
  Backend. No other goroutine applies rules.
- **`panic` must never depend on the daemon.** It is the lockout escape hatch, so it
  is deliberately NOT a control-socket op — it removes rules directly, as root, with
  no daemon running. Same for service lifecycle (`install`/`uninstall`/`start`/`stop`/`restart`):
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
- **Every PR that changes user-visible behavior updates [CHANGELOG.md](CHANGELOG.md)'s
  `## [Unreleased]` section, in the same PR** — not as a follow-up. `[Unreleased]`
  *is* the next release's notes (see [docs/releasing.md](docs/releasing.md)); a PR
  merged without an entry leaves it silently thin, and `task release:check` only
  catches the case where it's fully empty, not a partially-undocumented release.
  Use the existing `### Added` / `### Changed` / `### Fixed` / `### Removed`
  subsections (Keep a Changelog); skip only for changes with no user-facing effect
  (pure refactors, test-only changes, CI/tooling).
