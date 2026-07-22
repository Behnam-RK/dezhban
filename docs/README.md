# dezhban documentation

Start at the top-level [README](../README.md) for the overview and quick start.
Detailed docs are grouped by who they're for:

```
docs/
├── use/          end-user + operator — install it, configure it, run it, fix it
├── concepts/     the mental model — how the machine thinks, in one canonical place
├── contribute/   contributor + maintainer — build, test, release
└── adr/          the *why* behind hard-to-reverse decisions
```

## use/ — installing, configuring, running

| Doc | What's in it |
|---|---|
| [getting-started.md](use/getting-started.md) | **Start here if you're new.** Install, set up, check it won't lock you out, arm it, read the menubar icon. |
| [install.md](use/install.md) | Every install path (curl/PowerShell, `.pkg`, `.deb`/`.rpm`, bare binaries), why there's no Apple-signed installer, how to verify a download. |
| [config.md](use/config.md) | Full JSON config field reference, the `vpn` block, validation rules, sample configs. |
| [cli.md](use/cli.md) | Every CLI command and flag, safe read-only inspection, running as a service, the macOS app. |
| [troubleshooting.md](use/troubleshooting.md) | Lockout recovery and the VPN-guard failure runbook. |
| [upgrade.md](use/upgrade.md) | `dezhban upgrade`: the two-phase apply/activate split, the activation gate, rollback. |

## concepts/ — the mental model

| Doc | What's in it |
|---|---|
| [how-it-works.md](concepts/how-it-works.md) | The narrative walkthrough — startup, the standing guard, life of a VPN drop, exit-country policing, the switch window. Read this first to understand the machine. |
| [modes.md](concepts/modes.md) | **The canonical reference.** Every posture — STANDBY, GUARD, FULL BLOCK, SWITCH WINDOW — the exact ruleset each installs, and the state-machine diagram. |
| [glossary.md](concepts/glossary.md) | One term per concept, and the words we deliberately don't use. The authority for user-facing copy in the GUI, CLI, and docs. |

## contribute/ — building, testing, releasing

| Doc | What's in it |
|---|---|
| [architecture.md](contribute/architecture.md) | The three-layer design, the `FirewallBackend` seam, the state file contract, the non-negotiable invariants, the dependency strategy. |
| [development.md](contribute/development.md) | Build, cross-compile, the safe dev loop, CI, the pre-commit hook. |
| [testing.md](contribute/testing.md) | The standing on-host verification checklist — the privileged checks CI cannot run — plus a macOS `pf` worked example. |
| [releasing.md](contribute/releasing.md) | Cutting a release: the dispatch workflow, CHANGELOG discipline, unsigned artifacts / signed checksums. |

## adr/ — decision records

[adr/README.md](adr/README.md) — the *why* behind the choices, the alternatives examined, and the specific reason each was rejected. Read before reversing anything they describe.
