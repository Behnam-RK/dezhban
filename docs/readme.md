# dezhban documentation

Start at the top-level [README](../README.md) for the overview and quick start.
Detailed docs live here:

| Doc | What's in it |
|---|---|
| [modes.md](modes.md) | The two enforcement modes — **VPN guard (primary)** vs country-blocklist (fallback): how each works, the rulesets, and which one you want. |
| [config.md](config.md) | Full JSON config field reference, the `vpn` block, validation rules, and sample configs. |
| [usage.md](usage.md) | Every CLI command and flag, safe read-only inspection, running as a service, and the macOS menubar app. |
| [architecture.md](architecture.md) | The three-layer design, the `FirewallBackend` seam, the non-negotiable invariants, and the dependency strategy. |
| [safety.md](safety.md) | Kill-switch safety principles and firewall teardown mechanics. |
| [troubleshooting.md](troubleshooting.md) | Lockout recovery and VPN-guard failure runbook. |
| [development.md](development.md) | Build, cross-compile, the safe dev loop, CI, and the pre-commit hook. |
| [state.md](state.md) | The `state.json` posture file: location, shape, and staleness contract. |
| [plans/readme.md](plans/readme.md) | Phase-by-phase implementation plans and locked design decisions. |
