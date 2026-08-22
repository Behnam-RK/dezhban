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

  The lock file lives under Application Support, not `~/Library/Caches`. `flock`
  is per-inode and macOS may purge a caches directory under disk pressure; a
  purged lock file while the incumbent holds its descriptor means the next launch
  creates a fresh inode, locks *that*, and runs a second copy undetectably.

  A launch the *user* performed must never become a silent no-op, so the copy
  that loses the lock focuses the winner and posts a distributed notification
  asking it to open its own, since the incumbent may be a `--background` login
  launch with none.

  Not gated on "Open minimized". It was, on the reasoning that "Always" has to mean
  always — but the preference governs the *launch*, and a user-initiated launch of
  an already-running app is not one: once the incumbent has finished starting,
  LaunchServices turns the same double-click into a reopen, which
  `applicationShouldHandleReopen` answers unconditionally in every mode, by design.
  So the gate bought no consistency — it gave one gesture opposite answers
  depending on whether the incumbent had finished starting — and cost the single
  launch with no other route to a window.
  Scoped by posting the bundle **path** as the notification object, since the
  name derives from the bundle id and two installs may legitimately run side by
  side. And a notification rather than re-opening the bundle through
  `NSWorkspace`: asking LaunchServices to open the app we are in the middle of
  quitting could spawn yet another copy, which would find the lock held and ask
  again. The observer is registered as the first statement of
  `applicationDidFinishLaunching` — distributed notifications are delivered
  immediately and never queued, so anything ahead of it is time in which the
  hand-off is dropped.

  That is not the whole window, though, and the rest of it needs a file. The lock
  is taken before `NSApplication` exists, so between acquiring it and installing
  the observer there is a stretch in which a hand-off is posted to nobody — short,
  but landing exactly at login, when someone impatient with a slow start
  double-clicks the app. So the losing copy writes a `HandoffRequest` beside the
  lock as well as posting, and the incumbent consumes it both when it installs the
  observer and for a few seconds after — bounded rather than on every tick,
  because a permanent per-tick stat on the main thread is the hazard
  `pollStateFile` was restructured to remove and this feature is cosmetic; the
  window it covers is a launch-time one, and once the observer exists the
  notification carries every later hand-off. Both signals describe the same
  request, so acting on it is a *claim* — whoever removes the file acts and the
  other stands down. Reading the timestamp and removing without checking, which is
  what it did first, let the notification handler and the backstop both conclude
  they had it and open the window twice: a second `NSApp.activate` half a second
  after the first, or a window reopening right after the user closed it.

  The claim settles ownership; it cannot settle everything, and trying to make it
  do so was the wrong instinct. The duplicate writes the file and *then* posts, so
  the two signals can pass each other in ways where both callers legitimately
  conclude they should act — and refusing to act on the ambiguous ones turns a
  hand-off into the silent no-op the mechanism exists to prevent, which is the
  worse failure of the two. So the notification acts on everything except `.lost`,
  the backstop acts only on `.fresh`, and the ambiguous signals' *effect* is
  debounced: an open within three seconds of a previous hand-off open is dropped.
  Debouncing what the user notices is cheaper and safer than making two
  asynchronous signals agree.

  Only the ambiguous ones, though. A claim of `.fresh` means the caller took a
  request nobody else had, so it is a distinct launch by definition — two
  double-clicks a second apart with the window closed in between are two requests
  and both must be answered. Debouncing on elapsed time alone swallowed the second,
  which is the silent no-op again, arrived at from the other direction.

  And accepting an ambiguous notification is bounded to the launch window, because
  `DistributedNotificationCenter` is a system-wide bus with no sender
  authentication and both the name and the object are derivable. Unbounded, any
  process running as this user could call `MainWindow.open()` — which activates the
  app — once per debounce interval indefinitely, reopening a window the moment it
  was closed. Requiring a file outside the launch window costs nothing real (the
  file is written before the post, so only the microsecond gap between the two
  needs the exemption) and removes the channel. The debounce is a rate limit, not a
  gate. Both consumers do their claim off the main thread, since it is a stat and an
  unlink and a network or relocated home would otherwise block the run loop on the
  one path meant to feel instant.

  A request the incumbent never got to must not be inherited by the *next* app to
  start and turned into a window nobody asked for. That is handled by the session
  owner discarding whatever it finds at the moment it takes the lock — exactly,
  where the first design guessed with a 30-second age cutoff. The cutoff only added
  a way to be wrong, and it was: a cold login where the incumbent takes longer than
  that to finish starting is precisely the impatient-double-click case this
  mechanism is written around, and the cutoff threw that request away. So a claimed
  file is honoured however old it is, because by construction it was written after
  this process took the lock.
- **macOS has a second way to start the app at login, and it carries no marker.**
  "Reopen windows when logging back in" relaunches whatever was running at
  logout, through LaunchServices, with no arguments. `SMAppService.mainApp` was
  reconciled with that path because it went through LaunchServices too; a launchd
  agent is not, so both would start at login and race for the session lock, and
  a resume copy that won made the window open at login under the default
  `bootOnly` — this very defect, intermittent instead of absent.
  `NSApp.disableRelaunchOnLogin()` is the API for "the login item is the only way
  I start at login", and the app calls it at launch. `MainWindow`'s
  `isRestorable = false` covers window restoration; this covers app relaunch,
  which is a different mechanism.
- **Uninstalling has to retract the registration, and only the app can.** A
  LaunchServices login item disappears with its bundle; a per-user launchd agent
  does not. `launchctl bootout` is not the answer either — it unloads the job for
  the current boot and leaves the record that recreates it at the next login,
  pointing at a plist inside a bundle that has been deleted, which is exactly the
  orphan being avoided. `SMAppService.unregister()` is the only real retraction
  and it can only be called by the app, so `DezhbanMenu` takes a
  `--unregister-login-item` errand flag — handled before the session lock, since
  it is not a second copy competing for the session — and
  `packaging/macos/uninstall.sh` runs it as the console user inside their GUI
  session before deleting the bundle. Root cannot reach another account's launchd
  session, so other users' entries are named in the closing message instead — as is
  the case where there is no logged-in user at all (run at the login window, or
  over ssh), where none of the per-user teardown can happen.

  The uninstaller also *looks* for the bundle rather than assuming
  `/Applications/Dezhban.app`. The migration is allowed to run from anywhere under
  an Applications directory, so a copy filed into `/Applications/Utilities` would
  otherwise be told its bundle was already gone — while it went on launching at
  every login and the script reported everything removed.

  The errand's exit status is load-bearing: `unregister()` only logs a refusal and
  the script discards the output, so without it a login item macOS would not
  retract stayed behind — pointing at a bundle deleted moments later, unreachable
  from then on — while the script reported everything removed. Uninstalling also
  clears the app's preference domain, which is not the cosmetic cleanup it looks
  like: the migration records that it has run, so a surviving flag means a *later*
  install is never migrated onto the agent, silently restoring the defect this ADR
  is about.

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

  The retraction attempt is unconditional, even for a legacy item that is present
  but not enabled. Leaving that one alone was the earlier choice, on the reasoning
  that the user had switched it off in System Settings and their "off" should not be
  undone behind their back — but `.requiresApproval` is a *live* registration, not a
  dead one. Re-approving "Dezhban" under Login Items later would have LaunchServices
  start the app with no `--background`, breaking "Open minimized" permanently, since
  the migration is marked done and never runs again. It was also unreachable:
  `isEnabled` asks whether the legacy item is *enabled*, so the switch read OFF and
  a click routed to `enable()`, never to the `disable()` that could have cleared it.
  Retracting it honours the same "off" and removes the way back to the defect;
  whether it *was* enabled still decides whether the agent is then registered.

  When the legacy item survives the attempt — checked by reading its status
  after, not by trusting the call not to throw — the agent is deliberately left
  unregistered. Registering it anyway would mean two launches at login, the
  agent with `--background` and the legacy item without, and whichever won the
  race would decide whether the window appeared: worse than the behaviour it
  replaces. `LoginItem.isEnabled` reports the legacy registration too, so the
  Settings toggle tells the truth about whether anything starts the app at
  login, and switching it off retracts *both*.

  The two stuck states are separate outcomes, because they are opposite facts.
  From the disable direction the old item is still *enabled*, so something is
  starting the app and the switch must stay on. From the enable direction nothing
  is registered at all — `enable()` is only entered when the switch read off — so
  an on-ish outcome snapped the switch on over a state where nothing was
  registered, and the next `seed()` flipped it back: the switch-versus-`isEnabled`
  disagreement that must never exist. Sharing one case also had it telling a user
  who clicked *off* what switching it *on* would do.

  `enable()` refuses while *any* legacy registration survives, not merely an
  enabled one. Guarding on enablement looked like a way to keep the advice below
  from being a dead end — a `.requiresApproval` item launches nothing, so it cannot
  be half of "two launches at login" — but `AssociatedBundleIdentifiers` makes one
  "Dezhban" row in Login Items govern both registrations, so approving that row arms
  both. The dead end is real and is stated rather than engineered around: a defect
  this branch exists to remove cannot be traded for a better error message.

  If macOS keeps refusing to retract it, the app has no way out on its own, and
  it must not pretend otherwise: "toggle it off and on again" was the first
  advice here and it was unreachable. The control was a `toggle()` that derived
  the direction to move in from `isEnabled`, which the stuck legacy item holds
  true — so every attempt took the *off* branch and could never reach
  `register()`. The control is `LoginItem.set(enabled:)` now, taking the state the
  user asked for, and it returns an `Outcome` rather than a `Bool`; the
  `legacyStuck` case tells the user the
  one thing that does work: remove "Dezhban" under System Settings → General →
  Login Items. Once they do, the toggle registers a clean agent.

  The same `Outcome` carries `awaitingApproval`, because `register()` reports
  "the user must approve this in System Settings" as a *status* rather than an
  error, and a switch that snaps back with no explanation is indistinguishable
  from a bug. `.requiresApproval` is why "is there a registration" and "will this
  start the app" have to be separate questions: it is a live registration that
  starts the app the moment approval lands, so the unregisters are guarded on the
  former. Guarding them on `.enabled` meant an awaiting-approval registration
  could not be retracted by the Settings switch *or* by the uninstaller's errand
  — the bundle would be deleted with the registration still on file, which is the
  orphan the errand exists to remove. `isEnabled` reports that same question, so
  what the switch shows agrees with what `set(enabled:)` does; when they
  disagreed, an awaiting-approval registration painted the switch ON while
  `isEnabled` read "off", and the user's attempt to switch it off re-registered
  instead. Taking the requested state rather than re-deriving it also fixed the
  stale-switch inversion: the login item is removable in System Settings too, so
  a switch left showing ON while that happened turned login-at-launch *on* when
  clicked.

  One more thing the persisted flag may not swallow: a migration that retracted
  the legacy item and then *failed* to register the agent leaves nothing starting
  the app at login. Marking that migrated would cost the user a setting they had
  switched on, with no retry ever, so it is deliberately left unmarked and
  retried on the next launch. What makes the retry safe rather than a return of
  the every-launch re-registration bug is a second flag, set whenever the user
  switches login-at-launch off themselves: an explicit "off" outlives every
  retry, so a retry can only restore what was already on.

  That retry needs a third flag to exist at all, which is not obvious and was
  got wrong first: by the time `register()` is reached the legacy item is already
  confirmed gone, so a retry launch that asks "is there a legacy item to
  migrate?" reads *no*, marks itself migrated and returns — never reaching
  `register()` again. The promised retry was dead code. A flag recorded at the
  moment the legacy item is confirmed retracted, before the register is
  attempted, is what distinguishes "this account had a login item and the agent
  is not up yet" from "this account never had one".

  The migration decides on `.enabled`, not on "is there a registration". These
  are different questions and using one predicate for both was a bug in the
  user's favour nowhere: `.requiresApproval` is what `mainApp` reports once the
  user has switched Dezhban *off* under System Settings → General → Login Items,
  so a migration gated on mere presence treated a deliberate off as something to
  carry forward — retracting it and registering the agent, turning
  login-at-launch back on during an upgrade, with `userDisabledKey` unable to
  help because a pre-upgrade user never set it. The unregister *guards* keep
  asking the presence question, because a `.requiresApproval` registration is
  still a registration to retract.

  And the migration runs only from `/Applications`, without marking the account
  migrated otherwise. `register()` records the plist of the *calling* bundle
  (`BundleProgram` is bundle-relative) while the flag is shared by every copy of
  the app, so one launch from `~/Downloads` — an upgrader trying the app zip
  before moving it — or from `dist/` would point the login agent at a bundle
  about to move or be deleted and mark the account done forever. It runs
  unattended, so it takes the conservative branch; an explicit toggle from
  Settings is the user's own call and is not gated.

  Two smaller versions of the same "the switch must not lie" rule.
  `LoginItem.enable()` refuses to register the agent while a legacy item is live,
  which `disable()` and the migration already refused — without it the stuck path
  led straight to both being registered, which is the two-launch race. And a
  failed *agent* unregister has its own outcome rather than reusing `.failed`,
  whose `isOn` is false: `unregister()` swallows its throw, so that combination
  painted the switch OFF while the registration was live and the app kept
  starting at login.
- **The first logout after upgrading is not covered, once.**
  `NSApp.disableRelaunchOnLogin()` is asserted by the *running* app, and the
  build being replaced never called it — so at that one logout the system has
  already registered the app for a LaunchServices resume relaunch. At the next
  login both copies start: the agent's with `--background`, the resume's without.
  Either way the window opens under the default `bootOnly` — if the agent wins the
  lock, the resume copy loses it, reads as a user launch and hands off, which the
  incumbent honours; if the resume copy wins, it opens the window itself. So the
  defect this ADR fixes reappears exactly once, on the first login after the
  upgrade, and never again.
  Not worked around, because every workaround guesses. Suppressing a hand-off for
  the first N seconds after login would break the case the hand-off exists for —
  an impatient double-click at login is *precisely* that window — and it would
  keep doing so on every subsequent login to fix one event that has already
  passed. A one-off wrong window beats a permanent rule that throws away real
  launches.
- **Switching login-at-launch off may terminate the app.** Unverified, and
  listed here rather than worked around because the workarounds are worse than
  the symptom. `SMAppService.unregister()` unloads the job from the launchd
  domain, and launchd terminates a loaded job's running process — which, in a
  session the agent started, is the app itself. So switching the Settings toggle
  off from a login-started session may quit the app before it can show the
  result. It is a nuisance rather than a risk: the daemon is what enforces, the
  GUI is a status and control surface, and relaunching restores it. Routing the
  agent through `/usr/bin/open` instead would sidestep it and hand the dedupe
  back to LaunchServices, but it resolves the bundle by identifier and could
  launch a *different* copy of the app than the one that registered — trading a
  known nuisance for a silent wrong-bundle launch. There is a manual check for
  this in [docs/contribute/testing.md](../contribute/testing.md); if it
  reproduces, that trade is worth reopening with measurements rather than
  guesses.
- **The marker could be passed by something other than the agent**, making a
  user launch look like a login launch. The only consequence is a window that
  does not open, and the Dock icon and "Open Dezhban…" both open it
  unconditionally in every mode — the setting can never make the window
  unreachable.
