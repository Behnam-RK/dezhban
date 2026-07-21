# ADR-0004: The switch window must be fully disableable

**Date**: 2026-07-20
**Status**: accepted, implemented
**Deciders**: Behnam RK

## Context

The bounded switch window is the only sanctioned relaxation of the guard. An operator who
wants a strict zero-leak posture should be able to remove it entirely — but could not.

`Normalize` coerces `SwitchWindow <= 0` back to the 15s default, so `switchWindow: "0"`
is **silently ignored**. There was no way to disable it at any layer: not in config, not
in the CLI, not in the GUI.

Its sibling had already solved this. `ReconnectWindow` parses `"0"` into a `Disabled`
sentinel that survives `Normalize`, precisely so that zero-leak purists can opt out of
the automatic reconnect window. The same treatment was simply never applied to the manual
window.

## Decision

Give `SwitchWindow` the same `Disabled` sentinel as `ReconnectWindow`. The two windows
stay **independently** disableable, because they answer different questions.

| Setting | Off means |
|---|---|
| `switchWindow: "0"` | No manual `dezhban switch`. A brand-new VPN requires adding its endpoint to config by hand. |
| `reconnectWindow: "0"` | A drop is cut with a zero leak window; the VPN cannot redial to a server dezhban has never seen. |

## Alternatives considered

### Alternative 1: One combined "strict mode" switch that disables both

- **Pros**: a single, comprehensible control.
- **Cons**: conflates two independent trades.
- **Why not**: the two windows have genuinely different costs. Someone on a fixed-server
  VPN may want automatic reconnect off (their endpoint never changes, so a drop should
  just cut) while keeping manual switch available for the rare provider change. Collapsing
  them removes a valid configuration. The Strict/Balanced/Relaxed **presets** in the GUI
  give the simple control without removing the underlying independence.

### Alternative 2: Enforce a floor and refuse to disable

- **Pros**: guarantees a recovery path always exists.
- **Cons**: overrides an informed operator on their own security posture.
- **Why not**: this is a tightening, not a loosening. Refusing to let a user be *stricter*
  than the default is the wrong direction for a security tool, and `reconnectWindow`
  already sets the precedent that "0" is a legitimate answer.

## Consequences

### Positive

- A genuine zero-leak posture is expressible: with both windows off, a tunnel drop is cut
  instantly and nothing ever relaxes the guard.
- Removes a silent coercion — a config value that was accepted, ignored, and never
  reported. Those are the worst kind of bug in a security tool.
- Aligns the two window settings so they behave the same way, which makes the GUI presets
  honest rather than a leaky abstraction.

### Negative

- With `switchWindow` disabled, connecting a brand-new VPN requires editing config and
  restarting. That is the point, but it must be *explained* at the moment of disabling,
  not discovered later.
- One more state combination to test: four on/off permutations across the two windows.

### Risks

- **Accidental coupling through `switchEnabled`.** Disabling `switchWindow` sets
  `switchEnabled` false, which drops the command-file poll and refuses socket switch ops.
  The automatic reconnect window calls `openWindow` directly and gates only on
  `ReconnectWindow > 0`, so it *should* survive — but the two must not be accidentally
  coupled. A test asserting all four permutations is required, not optional.
- A user selects Strict for the reassuring name and then cannot reconnect to their
  rotating-pool VPN without manual intervention. Mitigated by presets that state their
  **cost** beside their benefit; Strict must never be presented as merely "safest".

## Related

The GUI surfaces both windows together as Strict / Balanced / Relaxed presets, with
Custom for arbitrary pairs. Balanced (`reconnectWindow: 30s`, `switchWindow: 15s`) remains
the default, per the 2026-07-19 defaults review.
