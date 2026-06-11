# dezhban — Implementation Plans

**dezhban** (Persian: گزدزدزدبان / "gatekeeper") is a standalone, cross-platform
**network kill switch**. It watches the machine's public IP, resolves its
country, and when the country matches a blocklist it drives the OS firewall to
cut traffic — while keeping a minimal allowlist so recovery detection still works.

## Locked decisions

| Decision | Choice | Rationale |
|---|---|---|
| Language | **Go** | Single static binary per OS, `go build` cross-compiles, no runtime |
| Platform order | **macOS first** → Linux → Windows | Build/verify one backend end-to-end, then port behind the interface |
| Detection | **API-based first**, offline IP-range hybrid later | Simple to start; add robustness once the loop works |
| Fail mode | **Fail-closed** | Block when country is undeterminable — safe default for a security tool |

## Architecture (3 layers)

```
Monitor  ── polls public IP, resolves country        (platform-independent)
   │
   ▼
Decision ── blocklist + hysteresis + fail-mode → Block/Allow   (platform-independent)
   │
   ▼
Enforcement ── FirewallBackend per OS                (only platform-specific part)
```

The `FirewallBackend` interface is the seam: ~90% of code is shared; one small
module differs per OS. Every firewall rule carries a unique tag/anchor (`dezhban`)
so teardown is surgical.

## Phases

| Phase | Doc | Status | Theme |
|---|---|---|---|
| 0 | [phase-0-scaffold.md](./phase-0-scaffold.md) | ✅ | Go module, CLI skeleton, config, logging, privilege check, CLAUDE.md |
| 1 | [phase-1-monitor.md](./phase-1-monitor.md) | ✅ | Public-IP fetch + country resolve + polling loop (prints country) |
| 2 | [phase-2-macos-enforcement.md](./phase-2-macos-enforcement.md) | ☐ | `pfctl` anchor backend + manual `block`/`unblock`/`status` |
| 3 | [phase-3-wire-end-to-end.md](./phase-3-wire-end-to-end.md) | ☐ | Decision layer + monitor→decision→enforcement daemon (macOS) |
| 4 | [phase-4-resilience.md](./phase-4-resilience.md) | ☐ | Fail-closed, hysteresis, allowlist hardening, multi-provider |
| 5 | [phase-5-cross-platform.md](./phase-5-cross-platform.md) | ☐ | Linux `nftables` + Windows WFP backends |
| 6 | [phase-6-persistence.md](./phase-6-persistence.md) | ☐ | Run as service: launchd / systemd / Windows Service |
| 7 | [phase-7-safety-packaging.md](./phase-7-safety-packaging.md) | ☐ | Panic-unblock, manual override, logging polish, cross-compile |

Each phase is independently buildable & verifiable. Implement one at a time;
verify before moving on.

## Dependency strategy (R&D summary)

Keep the binary lean — stdlib where it suffices; a dep only where it removes real
complexity. Only **3 real deps**, one per hard platform problem:

| Lib | Phase | Role |
|---|---|---|
| [`kardianos/service`](https://github.com/kardianos/service) | 6 | cross-platform service (launchd/systemd/winsvc), one API |
| [`google/nftables`](https://github.com/google/nftables) | 5 | pure-Go netlink nftables (Linux), no shelling |
| [`tailscale/wf`](https://github.com/tailscale/wf) | 5 | pure-Go Windows Filtering Platform bindings |
| [`oschwald/geoip2-golang/v2`](https://github.com/oschwald/geoip2-golang) | hybrid (deferred) | offline IP→country via GeoLite2 mmdb |

- **macOS enforcement** → shell out to `pfctl` (no maintained Go pf lib). Expected.
- **CLI / config / logging / HTTP** → stdlib (`flag`, `encoding/*`, `log/slog`,
  `net/http`). Avoid `viper`/`cobra` to keep the standalone promise.

## Project layout (created in Phase 0)

```
dezhban/
  go.mod                       # github.com/behnam-rk/dezhban (path adjustable)
  cmd/dezhban/main.go          # CLI: run, block, unblock, status, panic
  internal/
    config/config.go           # Config struct + load + defaults
    monitor/{monitor.go,provider.go}
    decision/decision.go
    firewall/{backend.go,pf_darwin.go,nft_linux.go,wfp_windows.go}
    logging/logging.go
  configs/dezhban.example.yaml
  CLAUDE.md
  docs/plans/                  # these files
```
