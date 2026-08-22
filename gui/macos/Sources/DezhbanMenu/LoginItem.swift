import Foundation
import ServiceManagement

/// Wraps the menubar app's own login-item registration via SMAppService
/// (macOS 13+). Requires a proper bundle with a bundle identifier (assembled by
/// build-app.sh), so it is a no-op / failure when run as a bare SwiftPM binary.
///
/// It registers an **agent** — `Contents/Library/LaunchAgents/…login.plist`,
/// installed by build-app.sh — rather than `SMAppService.mainApp`. The two are
/// equivalent as far as "start me at login" goes; the difference is that the
/// agent's `ProgramArguments` carry `--background`, which is the only reliable
/// way for the app to know that macOS started it rather than the user. See
/// `LaunchVisibility` and docs/adr/0014-login-item-launch-marker.md.
///
/// Registering an agent `exec`s the app immediately (`RunAtLoad`), so both
/// `set(enabled:)` and the migration below can spawn a second copy of a running
/// app.
/// That is caught at startup by the session lock in main.swift, not here — the
/// duplicate is a *process* problem and this type has no way to see it.
enum LoginItem {
    /// Must match `LoginAgent.plist`'s `Label` and the filename build-app.sh
    /// installs it under; launchd rejects a mismatch, and SMAppService reports
    /// it only as a `.notFound` status. build-app.sh greps this file for the
    /// label it installs, so the three cannot drift apart silently.
    private static let plistName = "com.behnam-rk.dezhban.app.login.plist"

    /// Whether the one-shot move off `SMAppService.mainApp` has been attempted.
    ///
    /// Persisted, and deliberately not re-derived from `mainApp.status`: that
    /// read is only a truthful "already migrated" signal when the unregister
    /// succeeded, and it can fail for real (a login item added by hand in System
    /// Settings was never registered through SMAppService; unregister is also
    /// known to fail after the bundle moves). Inferring it meant the migration
    /// ran again on every launch, which re-registered the agent after the user
    /// had switched login-at-launch off in Settings — login-at-launch back on,
    /// with no way to turn it off from the UI. Same UserDefaults-flag call as
    /// `FirstRun`: a fact about this app on this account, not daemon config.
    private static let migratedKey = "dezhban.loginItemMigratedToAgent"

    /// Set when the user switches login-at-launch off themselves.
    ///
    /// The migration is allowed to retry when it retracted the legacy item but
    /// could not register the agent — otherwise that upgrade silently ends with
    /// *nothing* starting the app at login and no retry, ever. This flag is what
    /// keeps that retry from becoming the bug it replaced: an explicit "off" must
    /// outlive it, so a retry never re-registers what the user turned off.
    private static let userDisabledKey = "dezhban.loginItemUserDisabled"

    /// Set the moment the legacy item is confirmed gone, before the agent is
    /// registered.
    ///
    /// Without it the retry the comment on `migratedKey` promises is dead code:
    /// on the next launch the legacy item is already retracted, so a migration
    /// that keys "is there anything to do" off `legacyEnabled` reads false, marks
    /// itself migrated and returns — never reaching `register()` again. The user
    /// is then left with nothing starting the app at login, permanently, which is
    /// the exact outcome the unmarked flag exists to prevent.
    private static let legacyRetractionAttemptedKey = "dezhban.loginItemLegacyRetractionAttempted"

    /// What a `set(enabled:)` actually achieved, so the UI can say something
    /// true.
    ///
    /// A plain `Bool` could not: `register()` reports "the user has to approve
    /// this in System Settings" as a *status* rather than an error, and the one
    /// path where the legacy registration cannot be retracted needs its own
    /// message because the app has no way out of it on its own.
    enum Outcome: Equatable {
        /// Login-at-launch is on, via the agent.
        case enabled
        /// Nothing will start the app at login.
        case disabled
        /// Registered, but macOS is holding it for the user's approval — most
        /// often because they switched this app off in System Settings before.
        case awaitingApproval
        /// The legacy LaunchServices login item is still **enabled** and macOS
        /// refuses to retract it, so the app still starts at login without the
        /// launch marker. Reached only from the disable direction.
        case legacyStuck
        /// Login-at-launch could not be turned on: an old registration survives
        /// that macOS will not remove, and registering the agent beside it would
        /// arm two launches at login. Nothing starts the app, so `isOn` is false.
        ///
        /// Its own case rather than `legacyStuck`, which has `isOn == true`.
        /// `enable()` is only entered when the switch read OFF — meaning
        /// `isEnabled` was false — so returning an on-ish outcome snapped the
        /// switch ON over a state where *nothing* was registered, and the next
        /// `seed()` flipped it back. That switch-versus-`isEnabled` disagreement is
        /// the one thing `isEnabled`'s docstring says must never exist.
        case blockedByLegacy
        /// The **agent** registration survived an unregister that failed, so the
        /// app still starts at login.
        ///
        /// Its own case, and `isOn == true`, because the alternative was the one
        /// direction of lie that matters: `unregister()` swallows its throw, so
        /// this used to come back as `.failed` — `isOn == false` — painting the
        /// switch OFF while the registration was live and the app kept starting
        /// at login. Exactly what `isEnabled`'s docstring says it exists to stop.
        case agentStuck
        /// This copy of the app is not somewhere it will stay, so it must not claim
        /// the login item.
        case unstableLocation(String)
        /// Registration failed outright, and nothing is registered.
        case failed(String)

        /// Whether anything starts the app at login — what the Settings switch
        /// shows.
        var isOn: Bool {
            switch self {
            case .enabled, .awaitingApproval, .legacyStuck, .agentStuck: return true
            case .disabled, .failed, .blockedByLegacy, .unstableLocation: return false
            }
        }

        /// One line for the Settings status area.
        var message: String {
            switch self {
            case .enabled: return "App will open at login."
            case .disabled: return "App will not open at login."
            case .awaitingApproval:
                return "macOS is holding this for your approval — enable Dezhban in "
                    + "System Settings → General → Login Items."
            case .legacyStuck:
                // The disable direction: the old item is still enabled, so it is
                // still starting the app. Worded for the click the user made — the
                // shared wording described switching it *on*, which is not what
                // they did.
                return "macOS would not remove the old login item, so Dezhban will still open "
                    + "at login. Logging out and back in usually clears it."
            case .blockedByLegacy:
                return "An old login-item registration is still on file and macOS will not "
                    + "remove it. Switching this on could start Dezhban twice at login, so it "
                    + "has been left off. Logging out and back in usually clears it."
            case .agentStuck:
                return "macOS would not remove the login item, so Dezhban will still open at "
                    + "login. Remove \"Dezhban\" under System Settings → General → Login Items."
            case .unstableLocation(let where_):
                return "Dezhban has to live in Applications to open at login. This copy is "
                    + "running from \(where_), and a login item pointing there would break the "
                    + "moment it moves."
            case .failed(let why):
                return "Could not change the login item: \(why)"
            }
        }
    }

    /// Every mutation runs here, one at a time.
    ///
    /// Both callers are off the main thread now — the Settings switch, so a click
    /// cannot beachball the window on six blocking XPC round-trips, and the
    /// migration, so a slow login is not held up by it — which made them able to
    /// interleave. The bad one: the migration passes its `userDisabledKey` check,
    /// the user switches login-at-launch off, `disable()` retracts everything, and
    /// the migration then registers the agent. Login-at-launch back on immediately
    /// after being turned off is the single thing that key exists to prevent, so
    /// the two paths cannot be allowed to overlap.
    private static let queue = DispatchQueue(label: "com.behnam-rk.dezhban.app.loginitem")

    private static var service: SMAppService { .agent(plistName: plistName) }

    private static var agentEnabled: Bool { service.status == .enabled }

    /// The legacy LaunchServices registration, *enabled* — as opposed to merely
    /// present.
    ///
    /// The distinction decides whether the migration runs at all, and it is not
    /// the same question the unregister guards ask. `.requiresApproval` is what
    /// `mainApp` reports once the user has switched Dezhban off under System
    /// Settings → General → Login Items — so a migration gated on "is there a
    /// registration" would treat a deliberate *off* as something to carry
    /// forward, retract it, and register the agent: login-at-launch back on
    /// behind the user's back, on upgrade, with `userDisabledKey` unable to help
    /// because a pre-upgrade user never set it.
    private static var legacyEnabled: Bool { SMAppService.mainApp.status == .enabled }

    /// Whether a registration exists at all, as opposed to one that will start
    /// the app *right now*.
    ///
    /// The difference is `.requiresApproval`: `register()` leaves the service
    /// there when the user has previously switched this app off in System
    /// Settings, and it is a live registration that starts the app the moment
    /// they approve it. Guarding the unregisters on `.enabled` alone meant an
    /// awaiting-approval registration could not be retracted by the Settings
    /// switch *or* by the uninstaller's errand — the bundle would be deleted with
    /// the registration still on file, which is the orphan the errand exists to
    /// remove.
    private static func registered(_ target: SMAppService) -> Bool {
        switch target.status {
        case .notRegistered, .notFound: return false
        default: return true
        }
    }

    /// Whether anything at all is set up to start this app at login — the agent
    /// or a legacy registration the migration could not retract.
    ///
    /// Both, not just the agent: on the failed-migration path the legacy item is
    /// still live, so the app still starts at login, and it starts *without*
    /// `--background`, which is the very bug this PR fixes. Reporting only the
    /// agent would show "off" while startup kept happening, and leave the user
    /// no control that reaches the thing launching them.
    ///
    /// This is the value the switch displays. It must agree with what
    /// `set(enabled:)` does, or the two answer different questions about the same
    /// switch: an awaiting-approval registration once painted the switch ON while
    /// this read "off", so a click meant to disable re-registered instead and
    /// there was no way to switch login-at-launch off at all.
    ///
    /// Asymmetric between the two, and deliberately. For the **agent**,
    /// `.requiresApproval` means we registered it and macOS wants the user's
    /// consent — on, pending. For the **legacy** item, which this app never
    /// registers any more, `.requiresApproval` is the signature of the user having
    /// switched Dezhban off under System Settings → General → Login Items — so it
    /// launches nothing, and reporting it as ON contradicted the migration's own
    /// reading of that exact state ("their 'off' is the answer") and showed a
    /// switch that was on while nothing started the app.
    static var isEnabled: Bool {
        // On the mutation queue, so a read can never observe a change half-applied.
        // `disable()` retracts the legacy item and then the agent; a read landing
        // between those two saw the agent still registered and reported ON, and if
        // its main-queue hop was enqueued after the mutation's own completion it
        // overwrote the correct answer with that one — the switch reading ON with
        // nothing starting the app at login, which is the failure the revision
        // stamp in SettingsView was added for and could not close on its own.
        queue.sync { registered(service) || legacyEnabled }
    }

    /// Sets login-at-launch to `enabled` and reports what actually happened.
    ///
    /// Takes the state the user asked for rather than deriving it from a live
    /// read, which inverted the click whenever the switch was stale: with the
    /// Settings pane open, removing the login item in System Settings and then
    /// clicking Dezhban's switch — visibly ON, so plainly an attempt to turn it
    /// off — read "currently off" and turned login-at-launch on.
    ///
    /// Turning it OFF retracts both registrations, for the reason `isEnabled`
    /// reports both. Turning it ON registers only the agent — the legacy one is
    /// never created again.
    /// Enqueued, with the result delivered on main.
    ///
    /// There is no synchronous form. There was, and it had no callers left — a
    /// `queue.sync` on a type whose whole job is serializing two mutation paths is
    /// an invitation to block the main thread on six XPC round-trips, which is the
    /// beachball this was moved off-main to avoid.
    ///
    /// `queue` is serial, so this preserves click order — which the synchronous
    /// form did not when each click was dispatched to a *concurrent* queue and the
    /// blocks then raced into `sync`. Two quick clicks could reach the serial queue
    /// in the wrong order, leaving the registration in click 1's state while the UI
    /// applied click 2's outcome: the switch showing OFF while the app still starts
    /// at login, which is the lie the whole `Outcome` type exists to prevent.
    static func set(enabled: Bool, completion: @escaping (Outcome) -> Void) {
        queue.async {
            let outcome = enabled ? enable() : disable()
            DispatchQueue.main.async { completion(outcome) }
        }
    }

    private static func enable() -> Outcome {
        // Gated on the install location, exactly as the migration is. The waiver
        // this used to carry — "an explicit toggle from Settings is the user's own
        // call" — ignored that the consequence is not the user's to undo:
        // `register()` records the *calling* bundle (`BundleProgram` is
        // bundle-relative), and only the registering bundle can ever call
        // `unregister()`. So toggling this on from `dist/Dezhban.app` — the
        // documented dev loop, which `SessionLock` is path-keyed specifically to
        // allow — leaves a launchd registration pointing into a bundle that the next
        // build deletes: it fails to load at every login, leaves an orphan row in
        // Login Items, and *nothing in the product can retract it*, since
        // uninstall.sh only searches the Applications directories. Same for a zip
        // copy run once from ~/Downloads, which is the case the migration's own gate
        // exists to avoid.
        guard isInStableInstallLocation else {
            return .unstableLocation(Bundle.main.bundleURL.deletingLastPathComponent().path)
        }
        // The agent must never be registered beside a live legacy item — that is
        // two launches at login, one with the marker and one without, and
        // whichever won the session lock would decide whether the window opened.
        // `disable()` and the migration both refuse it; this refused nothing, and
        // the stuck-migration path led straight here: switch reads ON, user clicks
        // it off, clicks it on again, and both are registered.
        // Retract any legacy registration, and refuse while one survives at all —
        // not merely while one is *enabled*.
        //
        // Guarding on enablement was an attempt to keep `.legacyStuck`'s advice
        // from being a dead end, on the reasoning that a `.requiresApproval` legacy
        // item launches nothing so cannot be half of "two launches at login". But
        // `AssociatedBundleIdentifiers` makes ONE "Dezhban" row in Login Items
        // govern both registrations, so approving that row arms both — and then the
        // agent and the legacy item both start the app, one with the marker and one
        // without, racing the session lock to decide whether the window opens.
        // That is the defect this whole branch exists to remove, so it cannot be
        // traded for a better error message.
        //
        // The dead end is real and is now stated honestly instead of being
        // engineered around: if macOS will not retract the old registration,
        // nothing the app can do will make enabling this safe. See
        // `Outcome.legacyStuck`.
        retractLegacy()
        if registered(.mainApp) {
            // Which outcome depends on what actually survived, not on the direction
            // clicked. A legacy item still *enabled* is starting the app at login,
            // so `.legacyStuck` (isOn true) is the true report; only a dormant
            // `.requiresApproval` leftover means nothing is starting it.
            //
            // `.blockedByLegacy` used to be returned for both, on the argument that
            // `enable()` is only entered when the switch read OFF so `isEnabled` must
            // have been false. That assumes a fresh switch — and the rest of this
            // code deliberately treats it as stale, which is exactly why
            // `set(enabled:)` takes the state the user asked for instead of
            // re-reading. Re-approving the "Dezhban" row in System Settings arms the
            // legacy registration behind a switch showing OFF, and clicking it then
            // reported "left off" over a live login launch.
            return legacyEnabled ? .legacyStuck : .blockedByLegacy
        }
        UserDefaults.standard.set(false, forKey: userDisabledKey)
        // Flushed, like every other write to these three coupled flags. This pane
        // can have its process killed by launchd mid-operation, and clearing the
        // explicit-off only in memory meant the next launch still read it as true —
        // marking the account migrated and permanently cancelling the register()
        // retry that exists so nobody is stranded with nothing starting the app.
        UserDefaults.standard.synchronize()
        do {
            try service.register()
        } catch {
            NSLog("DezhbanMenu: could not register the login agent: \(error)")
            // Re-read rather than assume the throw means nothing is registered.
            // This is the same asymmetry `.agentStuck` was added to close on the
            // other side: a throw reported as `.failed` has `isOn == false`, so a
            // registration that *is* live paints the switch OFF while the app keeps
            // starting at login. `kSMErrorAlreadyRegistered` is the obvious way in —
            // the switch stale-OFF, the user clicks it on, and the register throws
            // precisely because it is already registered.
            //
            // And the outcome comes from the status, not from the fact that a throw
            // happened. Mapping every throw-with-a-registration to `.agentStuck`
            // told a user who had just asked to turn login-at-launch ON to go and
            // remove the login item — the opposite of what they wanted, in the case
            // the comment above names as the main way here.
            switch service.status {
            case .enabled: return .enabled
            case .requiresApproval: return .awaitingApproval
            default: return .failed(error.localizedDescription)
            }
        }
        // Checked, not assumed: `register()` returns without throwing when macOS
        // is going to make the user approve it, and the switch snapping back with
        // no explanation is indistinguishable from a bug.
        if service.status == .requiresApproval { return .awaitingApproval }
        return agentEnabled ? .enabled : .failed(describe(service.status))
    }

    private static func disable() -> Outcome {
        UserDefaults.standard.set(true, forKey: userDisabledKey)
        // Flushed before the unregister below, because that unregister may get
        // this process killed: launchd terminates a loaded job's running process,
        // and in a login-started session that process is the app (recorded as an
        // open risk in docs/adr/0014-login-item-launch-marker.md). UserDefaults
        // does not write through synchronously, so losing this one would leave a
        // pending migration retry free to register the agent again on the next
        // launch — turning login-at-launch back on behind the user, which is the
        // single thing this key exists to prevent.
        UserDefaults.standard.synchronize()
        // Legacy FIRST, agent last, for the same reason the flush above exists:
        // the agent unregister is the call that may get this process killed by
        // launchd. Done the other way round, a stuck-legacy install in a
        // login-started session lost the app between the two lines and left the
        // legacy item registered — still starting the app at login, without the
        // marker, which is the state this function exists to clear.
        retractLegacy()
        if registered(service) { unregister(service, what: "login agent") }
        if registered(.mainApp) {
            // The stuck path. Reported rather than worked around: registering the
            // agent alongside it would mean two launches at login, one with the
            // marker and one without, and whichever won the race would decide
            // whether the window appeared. `Outcome.legacyStuck` tells the user
            // the one thing that does clear it.
            NSLog("DezhbanMenu: the legacy login item could not be retracted")
            // Same derivation as `enable()`: an *enabled* survivor is still starting
            // the app, a dormant one is not. Testing only `legacyEnabled` here while
            // `enable()` tested presence made the two directions disagree about one
            // state — a `.requiresApproval` leftover that would not retract reported
            // a clean "App will not open at login", and then every future click to
            // turn it on was refused, permanently, with nothing having warned them.
            return legacyEnabled ? .legacyStuck : .blockedByLegacy
        }
        return registered(service) ? .agentStuck : .disabled
    }

    /// Retracts everything that could start this app at login, best effort.
    ///
    /// Used by the `--unregister-login-item` errand the uninstaller runs (see
    /// main.swift). `SMAppService.unregister()` is the only thing that actually
    /// retracts an agent registration — `launchctl bootout` unloads the job for
    /// this boot and leaves the record that recreates it at the next login — and
    /// only the app can call it.
    @discardableResult
    static func retractAll() -> Bool {
        queue.sync {
            if registered(service) { unregister(service, what: "login agent") }
            if registered(.mainApp) { unregister(.mainApp, what: "legacy login item") }
            // Deliberately NOT through `retractLegacy`: this runs from the uninstall
            // errand, where recording "the agent still needs registering" would be a
            // lie about an app that is about to be deleted.
            //
            // Reported, not swallowed. `unregister` only logs its throw, and the
            // uninstaller discards this process's output — so a refusal left the
            // Login Items entry behind, pointing at a bundle about to be deleted and
            // unreachable afterwards, while the script printed "service
            // unregistered, files deleted". The orphan the errand exists to remove,
            // now silent. The exit status is what makes it visible.
            return !registered(service) && !registered(.mainApp)
        }
    }

    /// Moves an install that registered `SMAppService.mainApp` (every build
    /// before the agent existed) onto the agent, exactly once per account.
    ///
    /// Gated on the OLD registration being enabled: a user who had
    /// login-at-launch switched off must not have it switched on by an upgrade.
    /// The attempt is recorded either way, so this runs at most once whether it
    /// succeeded or not — see `migratedKey`.
    static func migrateFromMainAppRegistration() {
        queue.sync { migrateLocked() }
    }

    private static func migrateLocked() {
        guard !UserDefaults.standard.bool(forKey: migratedKey) else { return }
        // Only from a bundle that is going to stay put, and deliberately WITHOUT
        // marking migrated — so the installed copy still does this later.
        //
        // `register()` records the plist of the *calling* bundle (`BundleProgram`
        // is bundle-relative) while `migratedKey` is shared by every copy of the
        // app, so one launch from ~/Downloads or from dist/ — an upgrader trying
        // the app zip before moving it, or a dev build — would point the login
        // agent at a bundle that is about to move or be deleted, and mark the
        // account done forever. The symptom is an SMAppService status nobody
        // reads. This runs unattended, so it has to be the conservative one; an
        // explicit toggle from Settings is the user's own call and is not gated.
        guard isInStableInstallLocation else {
            NSLog("DezhbanMenu: not migrating the login item from a non-standard location "
                + "(\(Bundle.main.bundleURL.path)); the copy in /Applications will do it")
            return
        }
        // An explicit "off" outlives every retry below. Without this, a migration
        // allowed to retry would re-register what the user had switched off — the
        // bug the persisted flag was introduced to kill.
        guard !UserDefaults.standard.bool(forKey: userDisabledKey) else {
            markMigrated()
            return
        }

        if registered(.mainApp) {
            // Whether it was *enabled* decides what happens afterwards — that is
            // the user's own on/off — but the retraction attempt itself is
            // unconditional, and that is the correction here.
            //
            // The earlier shape left a present-but-not-enabled item alone, on the
            // reasoning that the user had switched it off in System Settings and
            // their "off" should not be undone behind their back. But
            // `.requiresApproval` is a *live* registration, not a dead one: if they
            // later re-approve "Dezhban" under Login Items, LaunchServices starts
            // the app with no `--background` and "Open minimized" is broken again —
            // permanently, because `migratedKey` is set and this never runs a second
            // time. And it was unreachable: `isEnabled` asks `legacyEnabled`, so the
            // switch read OFF and a click routed to `enable()`, never to the
            // `disable()` the old comment pointed at. Retracting it honours the same
            // "off" while removing the way back to the defect.
            let wasEnabled = legacyEnabled
            retractLegacy()
            if registered(.mainApp) {
                // Stuck. The agent is left unregistered rather than stacked on top
                // of a live legacy item — two launches at login, one with the marker
                // and one without. Only the user can clear it now.
                NSLog("DezhbanMenu: the legacy login item could not be retracted. "
                    + "Remove \"Dezhban\" under System Settings → General → Login Items; "
                    + "until then it may start Dezhban at login without the launch marker.")
                markMigrated()
                return
            }
            guard wasEnabled else {
                // It was off. Nothing is carried forward and the agent is not
                // registered; turning login-at-launch on is the user's call.
                markMigrated()
                return
            }
        } else if !UserDefaults.standard.bool(forKey: legacyRetractionAttemptedKey) {
            // Nothing was ever registered the old way on this account, and no
            // retraction was ever attempted, so there is nothing to move onto the
            // agent. Turning login-at-launch on is the user's call, via Settings.
            markMigrated()
            return
        }
        // Falling through means a retraction was attempted and the legacy item is
        // gone — this launch, or an earlier one that was killed by the unload before
        // it could finish.

        // Reached with the legacy item confirmed gone — now, or on an earlier
        // launch whose register() failed.
        if registered(service) {
            markMigrated()
            return
        }
        do {
            try service.register()
            markMigrated()
        } catch {
            // Deliberately NOT marked migrated. The legacy item is gone by now,
            // so leaving it here means *nothing* starts the app at login — and
            // with the flag set that would never be retried, silently costing the
            // user a setting they had switched on. The retry is safe because it is
            // gated on `userDisabledKey` above: it can only ever restore what was
            // already on, never override an explicit "off".
            NSLog("DezhbanMenu: could not register the login agent, will retry on next launch: \(error)")
        }
    }

    /// Retracts the legacy item and records the fact if it worked.
    ///
    /// The recording is the point. `legacyRetractionAttemptedKey` is what tells "this
    /// account had a login item and the agent is not up yet" from "this account
    /// never had one", and while only the migration wrote it, retracting through
    /// the Settings switch destroyed the fact without recording it — reopening the
    /// dead-retry hole from the other side. Switch off (legacy gone, nothing
    /// recorded), switch on, `register()` fails, and the next launch sees no
    /// legacy item and no flag, concludes there was never anything to migrate,
    /// and marks the account done with nothing starting the app at login.
    private static func retractLegacy() {
        guard registered(.mainApp) else { return }
        // Recorded and flushed BEFORE the unregister, not after. `SMAppService.mainApp`
        // is itself a launchd job, and the migration's main case is a pre-agent
        // install with login-at-launch ON — so the running app *is* that job's
        // process, and launchd may terminate it as the job is unloaded. Written
        // afterwards, that kill left the legacy item retracted with nothing recorded:
        // the next launch saw no legacy item and no flag, concluded there had never
        // been one, marked the account migrated and returned. Nothing starting the
        // app at login, permanently — the exact hole this flag was added to close.
        //
        // So it records the *attempt*, not the success. A retraction that fails is
        // then re-attempted on the next launch (the caller re-reads `registered`
        // either way), while one that succeeded without being recorded is no longer
        // mistaken for "there was never anything here". `disable()` already flushes
        // before this same call for the same reason.
        UserDefaults.standard.set(true, forKey: legacyRetractionAttemptedKey)
        UserDefaults.standard.synchronize()
        unregister(.mainApp, what: "legacy login item")
    }

    private static func markMigrated() {
        UserDefaults.standard.set(true, forKey: migratedKey)
        // Flushed, because `legacyRetractionAttemptedKey` is. Those two flags are read
        // together and one outliving the other inverts the decision they encode:
        // this runs seconds into a login, and if the session ended before cfprefsd
        // wrote it, a legacy item retracted for a user who had login-at-launch OFF
        // left `legacyRetractionAttemptedKey` durable and `migratedKey` gone — so the next
        // launch fell through to `register()` and turned it back on, which ADR-0014
        // says must never happen. The mirror loss strands the account with nothing
        // starting the app at login. `disable()` already flushes before the call
        // that can end the process; this path has the same obligation.
        UserDefaults.standard.synchronize()
    }

    /// Whether this bundle lives somewhere it is going to stay.
    ///
    /// `/Applications` is where every shipping path puts it (the `.pkg`, and the
    /// app zip, which unpacks straight into it); `~/Applications` is the
    /// system-sanctioned per-user equivalent. Anywhere else — `~/Downloads`,
    /// `dist/` — is a copy that is about to move or be deleted, and registering
    /// the login agent from it would point launchd at a bundle that stops
    /// existing.
    ///
    /// Symlinks are resolved for the same reason `SessionLock` resolves them: an
    /// install reached through a symlinked directory is still that install, and a
    /// literal string comparison silently answered "no" and left the migration
    /// undone forever, reported only in a log line.
    private static var isInStableInstallLocation: Bool {
        let bundle = Bundle.main.bundleURL
            .resolvingSymlinksInPath()
            .standardizedFileURL
            .deletingLastPathComponent()
            .path
        let roots = ([URL(fileURLWithPath: "/Applications")]
            + FileManager.default.urls(for: .applicationDirectory, in: .userDomainMask))
            .map { $0.resolvingSymlinksInPath().standardizedFileURL.path }
        // Anywhere *under* an Applications directory, not only directly in one.
        // Comparing just the immediate parent excluded
        // /Applications/Utilities/Dezhban.app — an ordinary thing for someone to do
        // with an app, and certainly a location it is going to stay in. That user
        // never migrated, so the legacy item kept starting the app with no marker
        // and "Open minimized" stayed broken for them permanently, reported only in
        // a log line nobody reads.
        return roots.contains { bundle == $0 || bundle.hasPrefix($0 + "/") }
    }

    /// Words, not a raw `SMAppService.Status`. It is an imported `NS_ENUM` with no
    /// `CustomStringConvertible`, so interpolating it put
    /// `SMAppService.Status(rawValue: 3)` in front of the user — in the very type
    /// that exists so the UI can say something true.
    private static func describe(_ status: SMAppService.Status) -> String {
        switch status {
        case .notRegistered: return "macOS did not keep the registration."
        case .enabled: return "the login item is enabled."
        case .requiresApproval:
            return "macOS needs you to approve Dezhban in System Settings → General → Login Items."
        case .notFound:
            return "macOS could not find the login item inside the app bundle — reinstall Dezhban."
        @unknown default: return "macOS reported an unrecognised login-item state."
        }
    }

    private static func unregister(_ target: SMAppService, what: String) {
        do {
            try target.unregister()
        } catch {
            NSLog("DezhbanMenu: could not unregister the \(what): \(error)")
        }
    }
}
