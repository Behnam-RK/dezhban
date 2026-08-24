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
`ControlToken.purge()`), the saved-window-state directories, and the preference
domains — current and legacy. It then opens **Terminal.app** running
`sudo sh /usr/local/share/dezhban/uninstall.sh` and quits.

The order is fixed by two facts that pull against each other. The purge runs
*before* the Terminal hand-off, because `uninstall.sh` quits this app in its
first few lines — anything left until after would race a `pkill` and lose on a
Mac where `sudo` does not stop to ask for a password. But retracting the login
item is *not* in the purge at all, because `SMAppService.unregister()` can end
this process: launchd terminates a loaded job's running process, and in a
login-started session that process is the app (ADR-0014's open risk). Doing it
before the hand-off would mean dying with the keychain and preferences gone,
the guard still enforcing, and nothing on screen. It does not need doing there
anyway: `uninstall.sh` already runs the app's own `--unregister-login-item`
errand as the console user. The app retracts the login item itself in exactly
one case — when the uninstaller is not installed *and* nothing root-owned is
left either, so no errand will ever run and the app is not the last surface on
a machine still enforcing — and does it last, after its report has been read.

**`uninstall.sh`** already ran that errand and deleted the console user's
current preference domain; it now deletes the legacy domain there too, covers
an invoking user who is *not* the console user through that account's own
launchd session, and prints the one per-user item nothing root-owned can ever
reach — the login-keychain token — with the command that finishes the job.

Sending `do script` to Terminal is a cross-app Apple event, so the bundle
carries `NSAppleEventsUsageDescription`. Without it macOS denies the event with
`errAEEventNotPermitted` and never offers the user a prompt, which would leave
the flow able only to report its own failure.

Explicitly **not** removed: another account's *own* state — its preferences, its
login-keychain items, its login-item registration — and notification
authorization. dezhban's machine-derived files are a different matter: the root
script already sweeps every account's session lock, and now its saved window
state, because those are dezhban's own artefacts rather than the user's settings
and nothing but root can reach them for an account nobody is logged into.

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
  keychain half to happen. Someone who deletes `Dezhban.app` in Finder and then
  runs `uninstall.sh` gets the preference domains, but keeps both the keychain
  token and the login item: the only thing that retracts a registration is the
  bundle's own `--unregister-login-item` errand, and they have just deleted the
  bundle. The script detects that (`no-app`) and prints both manual steps rather
  than leaving either silent.
- The four ways the hand-off can end are four different sentences, not one
  failure. Terminal running the script means quit. Terminal refusing means the
  guard is still enforcing while the keychain and preferences are already
  removed. And a missing script splits in two, because its absence proves
  nothing about the install: `scripts/install.sh` fetches the uninstaller over
  the network and treats a failure as non-fatal, and `install-local.sh` never
  fetches it at all — so if a CLI binary or the launchd job is still there, the
  answer is "still installed, still enforcing", and if neither is, this
  account's residue really was the whole job. Saying "nothing happened", or
  "it is all gone", would be false in exactly the cases where the user has to
  act.
- Terminal.app must exist and be scriptable. When it is not, the app says so and
  prints the command instead of quitting into a half-removed install.

### Risks

- **The confirmation is destructive and irreversible.** It is a critical-style
  alert that names every category removed. There is deliberately no default
  button, so the return key cannot trigger removal; Escape cancels. "Keep my
  dezhban configuration in /etc/dezhban" maps to the script's existing
  `KEEP_CONFIG=1`.
- **Preference deletion could be undone by the app's own shutdown**, since
  AppKit writes defaults as it winds down. Mitigated by clearing the domains
  last, immediately before the Terminal hand-off, and by the script repeating
  the deletion for the console user — which on this flow is the same account,
  since the app is what launched the script. The `$SUDO_USER` pass covers the
  other case: somebody who ran the script directly, from SSH or a second
  account. Saved window state does *not* have that exposure — the app's only
  window opts out of AppKit restoration, so nothing writes it back — but the
  script sweeps it for every account anyway, since it can reach accounts the app
  cannot. The one branch where no script will ever run — no uninstaller *and*
  nothing root-owned left — has to be its own repeat: it clears the domains a
  second time, sweeps this account's support directory, and quits, rather than
  staying open to write window frames back into a domain it has just told the
  user is empty.
- **Scope creep.** This record exists so a later change cannot quietly widen
  purge to other accounts or to data dezhban did not create. Widening it needs a
  new ADR.
