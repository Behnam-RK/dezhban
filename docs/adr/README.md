# Architecture decision records

Decisions whose **rationale** is expensive to reconstruct from the code.

[architecture.md](../architecture.md) has a "Design decisions" table — that is the
index of *what* was chosen. These records hold the *why*, the alternatives that were
examined, and the specific reason each was rejected. Read them before reversing
anything they describe: several record decisions that look wrong until you know the
failure they were built to prevent.

New records use [template.md](template.md) and take the next free number.

| # | Decision | Status |
|---|---|---|
| [0001](0001-single-guard-mode.md) | Collapse the two enforcement modes into one guard-only product | accepted, implemented |
| [0002](0002-standby-no-tunnel-posture.md) | Standby is the resting posture when no tunnel exists | accepted, implemented |
| [0003](0003-biometric-token-over-existing-daemon.md) | Biometric-gated token over the existing daemon, not an SMAppService helper | accepted, implementation pending |
| [0004](0004-switch-window-fully-disableable.md) | The switch window must be fully disableable | accepted, implemented |
| [0005](0005-allow-local-network-by-default.md) | Local network access is allowed by default | accepted, implemented |
| [0006](0006-geo-providers-tunnel-scoped.md) | Geo-provider passes are tunnel-scoped, never physical | accepted, implemented |
| [0007](0007-upgrade-disclosed-window-not-holding-block.md) | `dezhban upgrade` discloses the activation window instead of holding a block through it | accepted, implemented |

> **0006 is the one to read first if you are touching the geo lookup.** It records why
> the obvious implementation silently defeats the exit-country check, and it exists
> because that mistake has already been proposed once.
>
> **0007 is the one to read before "simplifying" `dezhban upgrade`'s
> apply/activate split**, or before adding a holding block around the restart
> window — it records why that gap is disclosed rather than covered, and why
> collapsing the two phases would quietly reopen the FULL BLOCK problem this
> design exists to prevent.
