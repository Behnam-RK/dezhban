# Architecture decision records

Decisions whose **rationale** is expensive to reconstruct from the code.

[architecture.md](../contribute/architecture.md) has a "Design decisions" table — that is the
index of *what* was chosen. These records hold the *why*, the alternatives that were
examined, and the specific reason each was rejected. Read them before reversing
anything they describe: several record decisions that look wrong until you know the
failure they were built to prevent.

New records use [template.md](template.md) and take the next free number.

| # | Decision | Status |
|---|---|---|
| [0001](0001-single-guard-mode.md) | Collapse the two enforcement modes into one guard-only product | accepted, implemented |
| [0002](0002-standby-no-tunnel-posture.md) | Standby is the resting posture when no tunnel exists | accepted, implemented |
| [0003](0003-biometric-token-over-existing-daemon.md) | Biometric-gated token over the existing daemon, not an SMAppService helper | accepted, implemented |
| [0004](0004-switch-window-fully-disableable.md) | The switch window must be fully disableable | accepted, implemented |
| [0005](0005-allow-local-network-by-default.md) | Local network access is allowed by default | accepted, implemented |
| [0006](0006-geo-providers-tunnel-scoped.md) | Geo-provider passes are tunnel-scoped, never physical | accepted, implemented |
| [0007](0007-upgrade-disclosed-window-not-holding-block.md) | `dezhban upgrade` discloses the activation window instead of holding a block through it | accepted, implemented |
| [0008](0008-arm-at-boot.md) | Arm at boot from a persisted observation, plus a bounded pause | accepted, implemented |
| [0009](0009-redial-budget.md) | The automatic redial window spends from a bounded budget | accepted, implemented |
| [0010](0010-tunnel-liveness.md) | Zombie-tunnel detection is unconditional; acting on it is opt-in | accepted, implemented |
| [0011](0011-biometric-enrollment-requires-a-signed-build.md) | Biometric token enrollment requires a signed build, so unsigned builds must refuse it | accepted, implemented (Alternative 3 superseded by 0012) |
| [0012](0012-app-checked-biometrics-on-unsigned-builds.md) | App-checked biometrics on unsigned builds, rather than no biometrics | accepted, implemented |

> **0006 is the one to read first if you are touching the geo lookup.** It records why
> the obvious implementation silently defeats the exit-country check, and it exists
> because that mistake has already been proposed once.
>
> **0007 is the one to read before "simplifying" `dezhban upgrade`'s
> apply/activate split**, or before adding a holding block around the restart
> window — it records why that gap is disclosed rather than covered, and why
> collapsing the two phases would quietly reopen the FULL BLOCK problem this
> design exists to prevent.
>
> **0009 is the one to read before "simplifying" the redial window back to a
> fixed length per drop**, or before sharing its budget with the manual window
> or pause. It records why the obvious shape — one window per drop, suppressed
> outright on a flap — is simultaneously unbounded across drops and useless on
> the poor connection it was meant to serve.
>
> **0011 is the one to read before adding an entitlement to the macOS app.** It
> records that the obvious fix — declaring `keychain-access-groups` on the
> ad-hoc signature — makes the app unlaunchable rather than working, and that
> this was tested rather than assumed. **0012 is its other half**: read it before
> "hardening" the Touch ID check back into a keychain-enforced one, or before
> repeating anywhere that reading the token *is* the authentication. On an
> unsigned build it is not, deliberately, and the UI says so.
>
> **0008 is the one to read before adding a fourth relaxation trigger** (or
> before treating "the switch window is the only sanctioned relaxation" as
> still literally true) — it records why pause was added as a *third*, and
> why arming at boot needed the `TunnelEverUp` persistence rather than a
> plain unconditional fail-closed start.
>
> **0010 is the one to read before defaulting `vpn.advanced.livenessRedial`
> to on**, or before treating a failed exit-country lookup as evidence a
> tunnel is dead. It records why that exact symptom is indistinguishable from
> a censoring exit, and why the diagnosis (always on) is kept separate from
> the relaxation it may trigger (opt-in).
