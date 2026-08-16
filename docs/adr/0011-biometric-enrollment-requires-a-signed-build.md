# ADR-0011: Biometric token enrollment requires a signed build, so unsigned builds must refuse it

**Date**: 2026-08-15
**Status**: accepted, implemented
**Deciders**: Behnam RK

## Context

[ADR-0003](0003-biometric-token-over-existing-daemon.md) chose a keychain-held
control token specifically because it needed **no signing prerequisite** — that was
one of the two headline advantages over the SMAppService helper it rejected
("no new root component, no signing prerequisite"), and the reason the fix could
land without waiting on release infrastructure. That claim is false on macOS.

`ControlToken.store` creates the item under a `SecAccessControl` with
`.biometryCurrentSet`. A biometry-gated item exists **only** in the macOS data
protection keychain, and reaching that keychain requires a `keychain-access-groups`
entitlement. Every build this project ships is ad-hoc signed
(`gui/macos/build-app.sh`: `codesign -s -`), has no Team ID, and carries no
entitlements at all. So `SecItemAdd` returns `-34018`
(`errSecMissingEntitlement`) — on every Mac, every time, not as the
version-dependent flakiness ADR-0003's risk section anticipated.

The damage was not the failure but its **ordering**. `ConfigApply.enrollToken`
gated only on `ControlToken.biometryAvailable` — "does this Mac have Touch
ID?" — which is true on any modern MacBook. It then spent the privileged step
first: a root password prompt, `dezhban token enroll`, the daemon's hash written
to `/var/db/dezhban/control.token`. Only afterwards did it try the keychain and
discover the refusal. The host was left with a token enrolled on the daemon that
no client could ever present, recoverable only by the user reading the error and
running `sudo dezhban token forget` by hand. Meanwhile the About pane went on
advertising *"Password — turn on Touch ID in Settings"*, inviting a retry that
cost another password and re-stranded the enrollment.

**Adding the entitlement was tested and is not available to us.** An ad-hoc
signature that declares `keychain-access-groups` — empty array or an explicit
group — is not merely ignored: the kernel **SIGKILLs the process at exec**.
Verified directly, and reversibly, on macOS 15: the identical binary runs and
returns `-34018` when signed `codesign -s -`, and dies with signal 9 the moment
the same signature carries that entitlement. Shipping it would have replaced a
recoverable error with an app that cannot launch at all.

## Decision

Treat "can this build hold a control token?" as a **capability to be probed, not
assumed** — and probe it *before* spending anything privileged.

`ControlToken.capability` attempts a real `SecItemAdd` of a throwaway item under
a suffixed account that can never collide with a real token, deletes it, and
classifies the status. The add prompts for nothing, so the probe is silent.
(As shipped it uses the plain-item policy
[ADR-0012](0012-app-checked-biometrics-on-unsigned-builds.md) settled on, not the
`.biometryCurrentSet` one this record was written against — see that ADR's Risks
for why the post-store rollback stays as the backstop.) On an unsigned build
the app disables the toggle, says why, and never takes a password. Enrollment is
refused **before** `token enroll` runs; if a store somehow fails anyway, the
daemon's hash is rolled back automatically rather than left for the user.

The signing path stays deliberately untaken. The dormant seams in
`packaging/macos/build-pkg.sh` (`INSTALLER_SIGN_IDENTITY`, `NOTARIZE_PROFILE`)
remain the recorded future fix.

## Alternatives considered

### Alternative 1: Add a `keychain-access-groups` entitlement to the ad-hoc signature

- **Pros**: would restore the feature outright, with a two-line build change.
- **Cons**: does not work — the kernel kills the process at launch.
- **Why not**: tested and disproved, both with an empty array and with an
  explicit group. This is the alternative that looks obviously correct from the
  documentation, which is precisely why it is recorded here.

### Alternative 2: Get a Developer ID and notarize

- **Pros**: the real fix. Restores biometric enrollment as designed, and fixes
  Gatekeeper for the `.pkg` at the same time.
- **Cons**: requires a paid Apple Developer membership and reworks the release
  pipeline; gates a correctness fix on unrelated infrastructure — the exact
  coupling ADR-0003 refused.
- **Why not**: not available now. Recorded as the future fix; the seams already
  exist so adopting it stays a two-variable change. **This ADR does not need
  superseding when that happens** — the probe reports `available` on a signed
  build and the feature simply switches on.

### Alternative 3: Keep the keychain item but gate it with `LAContext.evaluatePolicy`

- **Pros**: works under ad-hoc signing, with no entitlement and no Team ID.
  Biometrics keep working for everyone today.
- **Cons**: gives up the property the design was chosen for. Today `securityd`
  refuses to hand over the token without a biometric check, so *reading it is the
  authentication* and there is no "are you allowed?" question for a tampered app
  to answer for itself. Moving the check in-process makes it a branch a patched
  binary skips, over a plain keychain item it can then read freely.
- **Why not**: it converts an enforced gate into an advisory one, in a security
  tool, to avoid an inconvenience whose honest fallback (a password prompt) is
  what the user already had. Convenience is not worth a fake gate.

### Alternative 4: Leave the ordering alone and just document the failure

- **Pros**: no code change.
- **Cons**: every attempt still costs a root password and still strands an
  enrollment; the About pane still invites the attempt.
- **Why not**: the ordering *is* the bug. The signing constraint is a fact of the
  platform, but half-performing a privileged operation and leaving the host to
  clean up is ours.

## Consequences

### Positive

- Turning the toggle on an unsigned build now costs **nothing** — no password
  prompt, no enrollment, no cleanup. It is disabled with a reason instead.
- The failure is named where it happens. "This build can't use the keychain" and
  "this Mac has no Touch ID" are different problems and no longer share copy.
- Enrollment is atomic in effect: either both copies exist or neither.
- A latent bug was closed on the way: `store` implicitly addressed the data
  protection keychain while `isStored`, `load` and `remove` addressed the legacy
  one. Invisible only because `store` never succeeded — and it would have
  surfaced as "saved, but `isStored` says no" on the first signed build. All four
  now build their query from one shared helper, so they cannot drift onto
  different stores again. **They all address the LEGACY keychain**, and after
  [ADR-0012](0012-app-checked-biometrics-on-unsigned-builds.md) they must: the
  `SecKeychainItemDelete` that makes `remove` work across code identities exists
  only there. Do not "fix" this by pinning `kSecUseDataProtectionKeychain`.
- The verdict mapping lives in `DezhbanCore` and is unit-tested, so the copy
  cannot regress into inviting an impossible retry.

### Negative

- macOS app users get no Touch ID for settings changes until the project ships a
  signed build. They keep the password path, which is what they had.
- One more capability probe — a keychain add plus delete. As shipped it runs when
  the main window first appears, not at launch (a keychain write on every login,
  for a feature a menubar-only session never opens, is an unexplained unlock
  dialog), and only a *permanent* verdict is cached for the process lifetime;
  a transient refusal is left uncached so the next pane can re-probe.
- ADR-0003's "no signing prerequisite" now reads as false without this record
  beside it. It is deliberately not edited: a shipped ADR is superseded, not
  rewritten, and its *decision* still stands — only that consequence was wrong.

### Risks

- **The probe could disagree with the real store.** It writes the same item shape
  the real store writes, but under a different account — and it cannot exercise
  the `remove()` of a *pre-existing* token that `store` does first, so an item
  left by an earlier build is a divergence the probe cannot see. That is why the
  post-store rollback is kept as a backstop rather than treated as unreachable.
- **A stale probe item could be left behind** by a crash between add and delete.
  Mitigated by deleting the probe account before adding, so a leftover is cleared
  on the next run; the account is suffixed and can never collide with a real token.
