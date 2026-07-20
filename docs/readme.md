# dezhban documentation

Start at the top-level [README](../README.md) for the overview and quick start.
Detailed docs live here:

| Doc | What's in it |
|---|---|
| [how-it-works.md](how-it-works.md) | The narrative walkthrough — startup, the standing guard, life of a VPN drop, exit-country policing, the switch window, and the escape hatches. Read this first to understand the machine. |
| [modes.md](modes.md) | The two enforcement modes — **VPN guard (primary)** vs country-blocklist (fallback): how each works, the rulesets, and which one you want. |
| [config.md](config.md) | Full JSON config field reference, the `vpn` block, validation rules, and sample configs. |
| [usage.md](usage.md) | Every CLI command and flag, safe read-only inspection, running as a service, and the macOS app. |
| [architecture.md](architecture.md) | The three-layer design, the `FirewallBackend` seam, the non-negotiable invariants, and the dependency strategy. |
| [safety.md](safety.md) | Kill-switch safety principles and firewall teardown mechanics. |
| [troubleshooting.md](troubleshooting.md) | Lockout recovery and VPN-guard failure runbook. |
| [development.md](development.md) | Build, cross-compile, the safe dev loop, CI, and the pre-commit hook. |
| [releasing.md](releasing.md) | Cutting a release: the dispatch workflow, CHANGELOG discipline, unsigned macOS GUI. |
| [state.md](state.md) | The `state.json` posture file: location, shape, and staleness contract. |
| [acceptance.md](acceptance.md) | The standing on-host verification checklist — the privileged checks CI cannot run. |
| [testing-macos-block.md](testing-macos-block.md) | Step-by-step live block and VPN-guard walkthrough on macOS, with expected output. |
