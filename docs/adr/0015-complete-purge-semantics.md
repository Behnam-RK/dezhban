# ADR-0015: What a complete purge removes, and what it deliberately does not

**Date**: 2026-08-21
**Status**: accepted
**Deciders**: Behnam RK

## Context

`packaging/macos/uninstall.sh` runs as root and removes what root owns: the
binary, `Dezhban.app`, `/etc/dezhban`, `/var/db/dezhban`, the launchd plist, the
pkg receipts. Everything belonging to the logged-in user survived it — the app's
preference domain, the control token in the login keychain, and the login-item
registration.

That was not merely untidy. `FirstRunDecision.offer` is
`!isComplete && !vpnKnown`, where `isComplete` reads
`dezhban.firstRunCompleted` from the app's preference domain. Because uninstall
never cleared that domain, a machine that had been uninstalled and reinstalled —
with an empty `/etc/dezhban` and no VPN configured — still answered "the wizard
has been completed", so the setup flow never appeared. The reported bug ("setup
didn't auto-start on first run") and the missing feature ("I want a complete
uninstall") are the same defect from two ends. On the machine where this was
diagnosed, a second preference domain from a superseded bundle identifier
(`com.dezhban.DezhbanMenu`) was also still present.

Root cannot do this work. A login keychain item's ACL is bound to the user's
session, and a login item is registered per user through `SMAppService`. Both
require the user's own process.

## Decision

Purge is split by who can perform it, and the app owns the half root cannot.

**Dezhban.app** gains Settings → **Remove Dezhban…**. It removes, in this
account's session: the control token and its capability-probe item (via
`ControlToken.purge()`), the login-item registration (both the ADR-0014 agent
and any surviving `mainApp` registration), the saved-window-state directories,
and the preference domains — current and legacy. It then opens **Terminal.app**
running `sudo sh /usr/local/share/dezhban/uninstall.sh` and quits.

**`uninstall.sh`** additionally deletes both preference domains for `$SUDO_USER`
on a best-effort basis, and prints the two per-user items it cannot do
(keychain, login item) with the commands that finish the job.

Explicitly **not** removed: other user accounts' state, and notification
authorization.

## Alternatives considered

### Alternative 1: keep the first-run gate, make the config the only truth

Drop `isComplete` from `FirstRunDecision.offer`, so the wizard is offered
whenever no VPN is known.

- **Pros**: fixes the reported bug in one line, with no purge work at all.
- **Cons**: makes a modal wizard appear on every launch for anyone who
  deliberately runs without a configured VPN, and leaves the keychain token,
  login item and preference domains behind on every uninstall regardless.
- **Why not**: it treats the symptom. Settings already has "Run Setup Again…"
  as the manual route, so the gate is not the thing standing between a user and
  the wizard — a stale flag no uninstaller ever cleared is.

### Alternative 2: `uninstall.sh` loops over `/Users` and does everything

- **Pros**: one code path; works for a CLI-only install; reaches every account.
- **Cons**: `sudo -u` into an account that is not logged in cannot unlock its
  login keychain, so the keychain step fails or prompts unpredictably; and a
  script that deletes other people's data because root ran it is a scope no
  uninstaller should claim.
- **Why not**: it cannot actually do the keychain half, which is the half that
  needed a session in the first place.

### Alternative 3: a progress sheet in the app instead of Terminal

- **Pros**: feels native; no window the user has to close afterwards.
- **Cons**: `uninstall.sh` quits Dezhban and deletes its bundle partway
  through, so the sheet dies mid-teardown. The user could not distinguish a
  finished uninstall from one that stopped after `panic` removed the rules.
- **Why not**: a kill switch's removal has one step you must be able to watch
  succeed — the rule teardown — and this hides exactly that one.

## Consequences

### Positive

- A reinstall genuinely looks like a fresh machine, so the first-run wizard
  appears when it should.
- The keychain token and login item no longer outlive the app they belong to.
  A surviving login item makes macOS keep trying to launch a deleted bundle.
- The teardown is visible. The user watches `panic` remove the rules in a window
  that outlives the app.

### Negative

- Removal now spans two surfaces, and the app must run at least once for the
  per-user half to happen. Someone who deletes `Dezhban.app` in Finder and then
  runs `uninstall.sh` gets the preference domains (via `$SUDO_USER`) but keeps
  the keychain item and login item. The script prints both commands rather than
  leaving that silent.
- Terminal.app must exist and be scriptable. When it is not, the app says so and
  prints the command instead of quitting into a half-removed install.

### Risks

- **The confirmation is destructive and irreversible.** It is a critical-style
  alert that names every category removed, Cancel is the default button, and the
  return key cannot trigger removal. "Keep my dezhban configuration in
  /etc/dezhban" maps to the script's existing `KEEP_CONFIG=1`.
- **Preference deletion could be undone by the app's own shutdown**, since
  AppKit writes defaults as it winds down. Mitigated by clearing the domains
  last, immediately before `NSApp.terminate`, and by the script repeating the
  deletion for `$SUDO_USER`.
- **Scope creep.** This record exists so a later change cannot quietly widen
  purge to other accounts or to data dezhban did not create. Widening it needs a
  new ADR.
