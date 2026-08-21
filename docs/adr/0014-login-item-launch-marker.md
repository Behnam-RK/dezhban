# ADR-0014: The login item carries an explicit launch marker

**Date**: 2026-08-21
**Status**: accepted
**Deciders**: Behnam RK

## Context

The macOS app's "Open minimized" setting (`LaunchVisibility`: never / always /
only at login) needs to know one thing: did macOS start this app at login, or
did the user start it? The `bootOnly` case — the default, and the behaviour the
app had before the setting existed — is defined entirely by that distinction.

Until now the app asked AppKit, reading
`NSApplication.launchIsDefaultUserInfoKey` from the
`applicationDidFinishLaunching` notification. That key is documented as false
for launches the system performed on the user's behalf, and it was the sole
input to the decision. In practice it read wrong in both directions: the window
appeared at login with the setting on, and failed to appear on a manual Finder
launch. Compounding it, `MainWindow`'s `NSWindow` left `isRestorable` at its
default `true`, so AppKit's state restoration could reopen the window at launch
without consulting the setting at all.

The app registers itself for login with `SMAppService.mainApp`, which relaunches
the bundle with no arguments and no marker of any kind — so there was nothing
else to consult. Any fix that stayed with `mainApp` would have had to replace
one heuristic with another.

## Decision

Register a **LaunchAgent** shipped inside the bundle
(`Contents/Library/LaunchAgents/com.behnam-rk.dezhban.app.login.plist`, via
`SMAppService.agent(plistName:)`) whose `ProgramArguments` end in
`--background`. `LaunchVisibility.isBackgroundLaunch(arguments:)` reads that
marker from `CommandLine.arguments`, and `opensWindow(backgroundLaunch:)`
replaces `opensWindow(deliberateLaunch:)`. `MainWindow` additionally sets
`isRestorable = false` so state restoration cannot reopen the window behind the
setting's back.

An absent marker reads as a user launch. Existing installs are migrated once, on
launch, by `LoginItem.migrateFromMainAppRegistration()`.

## Alternatives considered

### Alternative 1: keep `launchIsDefaultUserInfoKey`, add `NSApp.isActive` as a tiebreak

A login-item launch does not activate the app; a Finder or Dock launch does.

- **Pros**: no packaging change at all; ships in one file.
- **Cons**: still a heuristic, and now a compound one. Activation state at
  `applicationDidFinishLaunching` races anything else competing for focus at
  login, which is precisely the moment the machine is busiest.
- **Why not**: it trades a signal that is wrong sometimes for a signal that is
  wrong less often. The setting has a right answer and the app should know it,
  not estimate it.

### Alternative 2: drop the three-way setting for a plain on/off

"Open the window when Dezhban starts", yes or no. Nothing to detect.

- **Pros**: the bug becomes unreachable; the least code of any option.
- **Cons**: loses the default behaviour — quiet at boot, visible when you launch
  it yourself — which is what almost everyone wants and what the app did before
  the setting existed. Every user would have to pick one of two worse options.
- **Why not**: deleting a feature is not a fix for being unable to implement it.

### Alternative 3: a separate login-item helper application

A small helper app in `Contents/Library/LoginItems` that launches the main app
with an argument, the pre-`SMAppService` pattern.

- **Pros**: also deterministic; long-established.
- **Cons**: a second executable to build, sign, version and keep in step. The
  agent plist achieves the identical result with a file that has no code in it.
- **Why not**: strictly more machinery for the same guarantee.

## Consequences

### Positive

- The launch kind is a fact, not an inference. `LaunchVisibility` is testable
  against a literal argument list rather than an AppKit notification.
- State restoration can no longer reopen the window independently of the
  setting.
- Both directions of the original defect are fixed by the same change.

### Negative

- The bundle now has a mandatory `Contents/Library/LaunchAgents` payload.
  `build-app.sh` fails the build if it is missing, because a bundle without it
  registers nothing and the app silently stops starting at login.
- The plist's `Label`, its filename, and `LoginItem.plistName` must agree.
  launchd rejects a mismatch and `SMAppService` reports it only as a status, so
  the failure is quiet by nature; the plist comments say so at both ends.

### Risks

- **A user who had login-at-launch enabled loses it on upgrade.**
  `migrateFromMainAppRegistration()` unregisters `mainApp` and registers the
  agent, gated on the old registration having been enabled — so an upgrade never
  switches the login item *on* for someone who had it off. If the unregister
  fails, the register still runs: a duplicate entry in System Settings is
  cosmetic, whereas skipping it would leave a login launch that never sets
  `--background`, which is the bug being fixed.
- **The marker could be passed by something other than the agent**, making a
  user launch look like a login launch. The only consequence is a window that
  does not open, and the Dock icon and "Open Dezhban…" both open it
  unconditionally in every mode — the setting can never make the window
  unreachable.
