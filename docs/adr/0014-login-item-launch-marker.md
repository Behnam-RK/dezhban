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

- The bundle now has a mandatory `Contents/Library/LaunchAgents` payload, and
  `build-app.sh` asserts two things about it that no test can reach: that the
  plist's `Label` equals the filename it is installed under (launchd rejects a
  mismatch, and `SMAppService` reports that only as a status nobody reads), and
  that `ProgramArguments` still carry `--background`. Deleting the marker or
  renaming the label would otherwise pass `go test`, `swift test` and the build
  while silently restoring the original bug.
- **Registering the agent starts a second copy of the app.** This is the real
  cost of leaving `SMAppService.mainApp`. `mainApp` is a LaunchServices login
  item, and LaunchServices refuses to launch a bundle that is already running;
  an agent with `RunAtLoad` is not that — launchd `exec`s
  `Contents/MacOS/DezhbanMenu` directly and dedupes nothing. Both callers of
  `register()` run while the app is up (the Settings toggle, and the migration
  below), so each would leave two menubar items, two Dock tiles, two 1-second
  state-file timers and two update checkers — the duplicate carrying
  `--background`, so under the default `bootOnly` it opens no window and there
  is no way to tell the two icons apart. `RunAtLoad` has to stay true or the
  login launch never happens, so the duplicate is caught at startup instead:
  `acquireSessionOwnership()` in `main.swift` takes an exclusive `flock` before
  `NSApplication` exists, and a process that cannot get it exits.

  A lock rather than "which copy launched first", which is what this was first
  written as and which cannot work. Only a *newly started* process ever
  evaluates the question — the copy already serving the menubar never
  re-evaluates anything — so any rule under which the newcomer might decide it
  wins leaves both running, and any rule under which an undatable process yields
  can retire both and leave the Mac with no app at all.
  `NSRunningApplication.launchDate` is documented as optional, so both failures
  were reachable. The kernel has neither problem: exactly one open file
  description holds the lock, and it is released when that process dies however
  it dies, so a crashed predecessor cannot lock its successor out. The lock is
  keyed on the bundle's **path**, not its identifier, because
  `dist/Dezhban.app` run against an installed `/Applications/Dezhban.app` is the
  documented GUI dev loop and those two are not duplicates of each other.

  A launch the *user* performed must never become a silent no-op, so the copy
  that loses the lock focuses the winner and posts a distributed notification
  asking it to open its window — the incumbent may be a `--background` login
  launch with no window to be handed over to. A notification rather than
  re-opening the bundle through `NSWorkspace`: asking LaunchServices to open the
  app we are in the middle of quitting could spawn yet another copy, which would
  find the lock held and ask again.
- **Uninstalling has to retract the registration, and only the app can.** A
  LaunchServices login item disappears with its bundle; a per-user launchd agent
  does not. `launchctl bootout` is not the answer either — it unloads the job for
  the current boot and leaves the record that recreates it at the next login,
  pointing at a plist inside a bundle that has been deleted, which is exactly the
  orphan being avoided. `SMAppService.unregister()` is the only real retraction
  and it can only be called by the app, so `DezhbanMenu` takes a
  `--unregister-login-item` errand flag — handled before the instance lock, since
  it is not a second copy competing for the session — and
  `packaging/macos/uninstall.sh` runs it as the console user inside their GUI
  session before deleting the bundle. Root cannot reach another account's launchd
  session, so other users' entries are named in the closing message instead.

### Risks

- **A user who had login-at-launch enabled loses it on upgrade.**
  `migrateFromMainAppRegistration()` unregisters `mainApp` and registers the
  agent, gated on the old registration having been enabled — so an upgrade never
  switches the login item *on* for someone who had it off.

  The attempt is recorded in a persisted flag
  (`dezhban.loginItemMigratedToAgent`) and therefore happens at most once per
  account, whether or not it worked. Inferring "already migrated" from a live
  `mainApp.status` read instead — the first shape of this code — was only
  truthful when the unregister had succeeded, and it can fail for real: a login
  item added by hand in System Settings was never registered through
  `SMAppService`, and unregister is also known to fail after the bundle moves.
  The migration then re-ran on every launch and re-registered the agent after
  the user had switched login-at-launch off in Settings, leaving it on with no
  way to turn it off from the UI.

  When the legacy item survives the attempt — checked by reading its status
  after, not by trusting the call not to throw — the agent is deliberately left
  unregistered. Registering it anyway would mean two launches at login, the
  agent with `--background` and the legacy item without, and whichever won the
  race would decide whether the window appeared: worse than the behaviour it
  replaces. `LoginItem.isEnabled` reports the legacy registration too, so the
  Settings toggle tells the truth about whether anything starts the app at
  login, and switching it off retracts *both*.

  If macOS keeps refusing to retract it, the app has no way out on its own, and
  it must not pretend otherwise: "toggle it off and on again" was the first
  advice here and it was unreachable, because `toggle()` branches on `isEnabled`,
  which the stuck legacy item holds true — so every attempt took the *off* branch
  and could never reach `register()`. `LoginItem.toggle()` therefore returns an
  `Outcome` rather than a `Bool`, and the `legacyStuck` case tells the user the
  one thing that does work: remove "Dezhban" under System Settings → General →
  Login Items. Once they do, the toggle registers a clean agent. The same
  `Outcome` carries `awaitingApproval`, because `register()` reports "the user
  must approve this in System Settings" as a *status* rather than an error, and a
  switch that snaps back with no explanation is indistinguishable from a bug.
- **The marker could be passed by something other than the agent**, making a
  user launch look like a login launch. The only consequence is a window that
  does not open, and the Dock icon and "Open Dezhban…" both open it
  unconditionally in every mode — the setting can never make the window
  unreachable.
