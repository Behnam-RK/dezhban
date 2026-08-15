# ADR-0011: App-checked biometrics on unsigned builds, rather than no biometrics

**Date**: 2026-08-15
**Status**: accepted, implemented
**Supersedes**: the rejection of "Alternative 3" in
[ADR-0010](0010-biometric-enrollment-requires-a-signed-build.md). Everything else
in 0010 — the signing constraint, the entitlement that SIGKILLs the app, the
probe-before-spending ordering — still holds and is unchanged.

## Context

[ADR-0010](0010-biometric-enrollment-requires-a-signed-build.md) established that
a keychain item whose *release* is gated on biometry needs an entitlement an
ad-hoc signature cannot carry, and made the app refuse enrollment cleanly rather
than half-perform it. Correct, but it left the actual outcome untouched: every
settings change still costs a password, which is precisely the complaint
[ADR-0003](0003-biometric-token-over-existing-daemon.md) existed to fix. The
feature was honest and absent.

0010 considered and rejected "Alternative 3" — `LAContext.evaluatePolicy` plus a
plain keychain item — on the grounds that it converts an enforced gate into an
advisory one. That reasoning was sound in isolation and **weighed the wrong
baseline**: it compared app-checked biometrics against keychain-enforced
biometrics, when the real choice on every build this project ships is
app-checked biometrics against *a password prompt every time*. The strong version
is not on the menu without a Developer ID.

Both halves of the alternative were then verified on an ad-hoc binary with no
entitlements: `canEvaluatePolicy(.deviceOwnerAuthenticationWithBiometrics)`
returns true, and a plain generic-password item adds and reads back with status
0. Neither needs an Apple Developer account.

## Decision

Store the control token as an **ordinary login-keychain item**, and gate reading
it on an explicit `LAContext.evaluatePolicy` biometric check performed by the app.

Say so, in the places a user would otherwise assume otherwise: the Settings
toggle's help text states that dezhban checks the fingerprint and then reads the
secret, and
[docs/usage/cli.md](../usage/cli.md#changing-settings-without-a-password) carries
the same in full. A gate that reads stronger than it is would be worse than no
gate, because it would be relied upon.

The check is deliberately `.deviceOwnerAuthenticationWithBiometrics` and not
`.deviceOwnerAuthentication`: the latter falls back to the login password, and a
login password that unlocks a settings change is exactly what the token exists to
avoid. A cancelled or failed prompt drops to the ordinary `sudo` path instead.

## Alternatives considered

### Alternative 1: Keep 0010's position — no biometrics until the build is signed

- **Pros**: preserves the strongest property; nothing to explain; no new risk.
- **Cons**: the feature stays absent indefinitely, since the signing work is not
  scheduled. Users keep paying a password per settings change.
- **Why not**: it is the status quo whose cost prompted ADR-0003 in the first
  place. Holding out for the ideal mechanism, with no date attached, is a
  decision to ship nothing.

### Alternative 2: `.deviceOwnerAuthentication` (biometrics with password fallback)

- **Pros**: never dead-ends; the user always has a way through the prompt.
- **Cons**: the way through is the login password — so the feature whose purpose
  is "stop asking me for a password" would ask for one, and a shoulder-surfed
  login password would authorise settings changes directly.
- **Why not**: the honest fallback is `sudo`, which the app already has and which
  at least announces itself as the privileged path.

### Alternative 3: Get a Developer ID and keep the keychain-enforced version

- **Pros**: strictly better; no trade to explain.
- **Cons**: paid membership and release-pipeline work, on no schedule.
- **Why not**: unchanged from ADR-0010. **This ADR does not block it** — the
  capability probe stays, and moving back to a keychain-enforced item is a change
  to `store`/`load` alone.

## Consequences

### Positive

- Touch ID for settings changes works on every build, today, with no Apple
  Developer account.
- The daily path costs one fingerprint and zero passwords, which is what ADR-0003
  set out to deliver and had not.
- Failing closed is preserved and is now load-bearing in a place it was not
  before: `load()` returns nil on every path that is not a confirmed biometric
  success, so an error can never be mistaken for permission.

### Negative

- **The gate is advisory, not enforced.** A modified copy of the app could skip
  the check and read the token. This is the cost, stated plainly, and it is why
  the UI copy no longer claims the keychain is holding the token back.
- `.biometryCurrentSet` is gone, so changing your enrolled fingerprints no longer
  invalidates the token. Someone who adds a finger to an unlocked Mac can use it
  to authorise settings changes — but adding a finger already requires the login
  password, which already grants `sudo`.
- One more claim that must stay true as the code changes: three separate places
  now describe where the check lives.

### Risks

- **The copy drifts back into over-claiming.** Mitigated by
  `TokenCapabilityTests`, which pins that only the available verdict points at
  the toggle, and by keeping the mechanism sentence next to the toggle rather
  than only in the docs.
- **Someone "restores" `.biometryCurrentSet` or adds the entitlement**, believing
  it strictly better. ADR-0010 records that the entitlement SIGKILLs the app;
  this record explains why the access-control flags were dropped rather than
  lost.
