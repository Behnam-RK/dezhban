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
/// That is caught at startup by the instance lock in main.swift, not here — the
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
    private static let legacyRetractedKey = "dezhban.loginItemLegacyRetracted"

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
        /// The legacy LaunchServices login item is still live and macOS refuses
        /// to retract it, so the app still starts at login without the launch
        /// marker. Only the user can clear this, in System Settings.
        case legacyStuck
        /// The **agent** registration survived an unregister that failed, so the
        /// app still starts at login.
        ///
        /// Its own case, and `isOn == true`, because the alternative was the one
        /// direction of lie that matters: `unregister()` swallows its throw, so
        /// this used to come back as `.failed` — `isOn == false` — painting the
        /// switch OFF while the registration was live and the app kept starting
        /// at login. Exactly what `isEnabled`'s docstring says it exists to stop.
        case agentStuck
        /// Registration failed outright, and nothing is registered.
        case failed(String)

        /// Whether anything starts the app at login — what the Settings switch
        /// shows.
        var isOn: Bool {
            switch self {
            case .enabled, .awaitingApproval, .legacyStuck, .agentStuck: return true
            case .disabled, .failed: return false
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
                return "macOS would not remove the old login item, so Dezhban will still open "
                    + "at login. Remove \"Dezhban\" under System Settings → General → Login "
                    + "Items, then switch this on again to use the new one."
            case .agentStuck:
                return "macOS would not remove the login item, so Dezhban will still open at "
                    + "login. Remove \"Dezhban\" under System Settings → General → Login Items."
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
    @discardableResult
    static func set(enabled: Bool) -> Outcome {
        queue.sync { enabled ? enable() : disable() }
    }

    private static func enable() -> Outcome {
        // The agent must never be registered beside a live legacy item — that is
        // two launches at login, one with the marker and one without, and
        // whichever won the instance lock would decide whether the window opened.
        // `disable()` and the migration both refuse it; this refused nothing, and
        // the stuck-migration path led straight here: switch reads ON, user clicks
        // it off, clicks it on again, and both are registered.
        // Retract any legacy registration, but refuse only if one is still
        // *enabled*. The justification for refusing is "two launches at login,
        // one with the marker and one without" — and a `.requiresApproval` legacy
        // item launches nothing, so refusing on mere presence was stricter than
        // its own reason. It also made `Outcome.legacyStuck`'s advice a dead end:
        // removing the item under System Settings leaves `mainApp` at
        // `.requiresApproval`, so "then switch this on again" hit the same refusal
        // and the agent could never be registered.
        retractLegacy()
        if legacyEnabled { return .legacyStuck }
        UserDefaults.standard.set(false, forKey: userDisabledKey)
        do {
            try service.register()
        } catch {
            NSLog("DezhbanMenu: could not register the login agent: \(error)")
            return .failed(error.localizedDescription)
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
        if legacyEnabled {
            // The stuck path. Reported rather than worked around: registering the
            // agent alongside it would mean two launches at login, one with the
            // marker and one without, and whichever won the race would decide
            // whether the window appeared. `Outcome.legacyStuck` tells the user
            // the one thing that does clear it.
            NSLog("DezhbanMenu: the legacy login item could not be retracted")
            return .legacyStuck
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

        if legacyEnabled {
            // Checks after the attempt rather than trusting it not to throw, and
            // records the retraction — see `retractLegacy`. `registered`, not
            // `legacyEnabled`: a retraction that left it awaiting approval has not
            // retracted anything.
            retractLegacy()
            if registered(.mainApp) {
                // Same reasoning as `disable()`'s stuck path — the agent is left
                // unregistered rather than stacked on top of a live legacy item.
                // The Settings toggle reports the legacy registration, so the user
                // can see login-at-launch is on; clearing it is a System Settings
                // job, which `Outcome.legacyStuck` spells out when they try.
                NSLog("DezhbanMenu: the legacy login item could not be retracted; "
                    + "leaving login-at-launch as it was. Remove \"Dezhban\" under "
                    + "System Settings → General → Login Items to move onto the login agent.")
                markMigrated()
                return
            }
        } else if registered(.mainApp) {
            // Present but not enabled — the user switched it off in System
            // Settings. Their "off" is the answer: nothing is carried forward and
            // the agent is not registered. The stale registration is left alone
            // rather than retracted behind their back; `isEnabled` reports it, so
            // the switch shows it and `disable()` can clear it on request.
            markMigrated()
            return
        } else if !UserDefaults.standard.bool(forKey: legacyRetractedKey) {
            // Nothing was ever registered the old way on this account, so there is
            // nothing to move onto the agent. Turning login-at-launch on is the
            // user's call, via Settings.
            markMigrated()
            return
        }

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
    /// The recording is the point. `legacyRetractedKey` is what tells "this
    /// account had a login item and the agent is not up yet" from "this account
    /// never had one", and while only the migration wrote it, retracting through
    /// the Settings switch destroyed the fact without recording it — reopening the
    /// dead-retry hole from the other side. Switch off (legacy gone, nothing
    /// recorded), switch on, `register()` fails, and the next launch sees no
    /// legacy item and no flag, concludes there was never anything to migrate,
    /// and marks the account done with nothing starting the app at login.
    private static func retractLegacy() {
        guard registered(.mainApp) else { return }
        unregister(.mainApp, what: "legacy login item")
        guard !registered(.mainApp) else { return }
        UserDefaults.standard.set(true, forKey: legacyRetractedKey)
        UserDefaults.standard.synchronize()
    }

    private static func markMigrated() {
        UserDefaults.standard.set(true, forKey: migratedKey)
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
    /// Symlinks are resolved for the same reason `InstanceLock` resolves them: an
    /// install reached through a symlinked directory is still that install, and a
    /// literal string comparison silently answered "no" and left the migration
    /// undone forever, reported only in a log line.
    private static var isInStableInstallLocation: Bool {
        let parent = Bundle.main.bundleURL
            .resolvingSymlinksInPath()
            .standardizedFileURL
            .deletingLastPathComponent()
        let candidates = [URL(fileURLWithPath: "/Applications")]
            + FileManager.default
            .urls(for: .applicationDirectory, in: .userDomainMask)
        return candidates.contains {
            $0.resolvingSymlinksInPath().standardizedFileURL.path == parent.path
        }
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
