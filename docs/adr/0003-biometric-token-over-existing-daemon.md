# ADR-0003: Biometric-gated token over the existing daemon, not an SMAppService helper

**Date**: 2026-07-20
**Status**: accepted, implementation pending
**Deciders**: Behnam RK

## Context

The macOS app kept showing password-only prompts despite `pam_tid` being correctly
configured. The cause is in `Elevation.runViaSudo`: it launches `sudo` with
`standardInput = FileHandle.nullDevice`, deliberately, so sudo can never block waiting
on a password read. With `pam_tid` present, sudo tries Touch ID first — but the moment
that **first** read fails (clamshell or external display, an unrecognised or wet finger,
a few bad reads, or right after a reboot before the first password unlock), sudo falls
through to `pam_opendirectory`, tries to read a password from `/dev/null`, and fails
instantly. `runViaSudo` returns `nil` and the caller falls back to Authorization
Services, which the code's own comment concedes "is password-only in practice."

**There is no retry.** One Touch ID miss and you are on the password path.

The deeper problem is that the design authenticates by *getting root*, which forces a
choice between two macOS prompts that each have a disqualifying flaw: Authorization
Services cannot do biometrics, and the sudo path gives up after a single miss.

The reasoning that previously ruled out a privileged helper — "a permanently installed
root XPC service, a great deal more attack surface than this tool wants for what amounts
to running `dezhban start` occasionally" — is **stale**. dezhban already runs a permanent
root LaunchDaemon with a control socket. That surface exists.

## Decision

Stop elevating from the GUI for routine work. Store a random token in the login keychain
under `kSecAccessControlBiometryCurrentSet` — so *reading it* is the Touch ID prompt,
with macOS's native retry and a real "Use Password…" continuation — and send it over the
control socket the daemon already has. The daemon verifies it against a root-owned hash.
Add two socket ops: `config-write` and **`reload`** (not `restart`: a daemon cannot
restart itself, but it can re-read config and re-apply policy from its own run loop).

## Alternatives considered

### Alternative 1: Just fix the sudo retry

- **Pros**: minimal; no architecture change; lands fast.
- **Cons**: still shows a password field on a Touch ID miss; does nothing for the
  frequency of prompts.
- **Why not**: insufficient alone — but **adopted as a complement**, because service
  lifecycle and `panic` can never route through the daemon. See "What still elevates".

### Alternative 2: SMAppService privileged helper with LAContext

- **Pros**: the by-the-book Apple approach; most future-proof.
- **Cons**: a *second* root component beside the daemon; requires Developer ID signing
  and notarization, which the current build does not have.
- **Why not**: it adds a root component to solve a problem the existing root component
  can already solve. The signing requirement would also gate a fix on unrelated release
  infrastructure.

### Alternative 3: Reduce the number of prompts instead of making them biometric

- **Pros**: cheap; batching already exists.
- **Cons**: does not address the complaint.
- **Why not**: the ask was biometrics everywhere, not fewer passwords. Batching is kept
  regardless, for atomicity rather than prompt-count.

## Consequences

### Positive

- The daily path — edit config, apply, reload — costs **zero** password prompts and one
  native Touch ID HUD that behaves the way every other macOS biometric prompt does.
- **The socket gets stronger, not weaker.** Its trust boundary today is "filesystem
  permissions, and nothing else" (no `SO_PEERCRED`), so any process running as an admin
  user is authorised. A token beats filesystem permissions, so adding `config-write`
  behind it is a net tightening rather than the escalation it would otherwise be.
- `kSecAccessControlBiometryCurrentSet` invalidates the item if the enrolled fingerprint
  set changes, forcing re-enrollment — the correct security property, for free.
- No new root component, no signing prerequisite.

### Negative

- Enrollment needs root exactly once, to write the hash to `/var/db/dezhban`. That first
  prompt still goes through the existing elevation path.
- A new secret to manage, with a defined loss path: if the keychain item is gone,
  re-enroll; if the hash file is gone, the daemon rejects all token ops until re-enrolled.
- Gated behind `control.allowConfigOps` (default true), mirroring `allowSwitchOps` —
  another flag on an already flag-rich control block.

### Risks

- **The token becomes a bypass of the biometric gate if it leaks.** Bounded by what the
  ops can do: `config-write` and `reload` cannot open egress past the guard directly,
  and the existing `allowSwitchOps` rail still governs the one genuinely guard-relaxing
  op. Operators who reject the trade set `control.allowConfigOps: false`.
- Keychain access from a non-notarized app can behave differently across macOS versions;
  the sudo path must remain a working fallback, not be deleted.

## What still elevates

Non-negotiable per `CLAUDE.md`: `panic` never depends on the daemon (it is the lockout
escape hatch), and service lifecycle — `install` / `uninstall` / `start` / `stop` —
cannot go through a daemon that is being installed or stopped. **These keep needing real
root, so the sudo path must be fixed rather than merely bypassed**: replace
`standardInput = nullDevice` with a `SUDO_ASKPASS` helper and `sudo -A`, so a Touch ID
miss falls back to sudo's own askpass instead of defecting to a system password dialog.

Also: `AboutView` currently advertises "Authorization Services (Touch ID capable)". By
this project's own findings that is false, and it must be replaced with the true
enrollment state.

## Progress

Alternative 1 (the sudo-retry complement, not a substitute for the Decision above) has
shipped: `Elevation` now prefers `sudo` + `pam_tid` and falls back to Authorization
Services, then the legacy AppleScript dialog. **The Decision itself — the keychain token,
the `config-write`/`reload` control-socket ops, `control.allowConfigOps` — has not.**
`AboutView` still reports "Authorization Services (Touch ID capable)", the exact string
this ADR calls out as false; it has not yet been replaced with the true enrollment state.
Status stays `implementation pending` until the token path lands.
