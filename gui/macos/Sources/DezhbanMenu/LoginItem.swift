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
/// `toggle()` and the migration below can spawn a second copy of a running app.
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

    /// What a `toggle()` actually achieved, so the UI can say something true.
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
        /// Registration failed outright.
        case failed(String)

        /// Whether anything starts the app at login — what the Settings switch
        /// shows.
        var isOn: Bool {
            switch self {
            case .enabled, .awaitingApproval, .legacyStuck: return true
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
                return "macOS would not remove the old login item. Remove \"Dezhban\" under "
                    + "System Settings → General → Login Items, then switch this on again."
            case .failed(let why):
                return "Could not change the login item: \(why)"
            }
        }
    }

    private static var service: SMAppService { .agent(plistName: plistName) }

    private static var agentEnabled: Bool { service.status == .enabled }

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
    /// This is the value the switch displays *and* the value `toggle()` branches
    /// on; they must be the same one. When they were not, an awaiting-approval
    /// registration painted the switch ON while `toggle()` still saw "off", so
    /// the user's next click re-registered instead of disabling and there was no
    /// way to switch login-at-launch off at all.
    static var isEnabled: Bool { registered(service) || registered(.mainApp) }

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
        enabled ? enable() : disable()
    }

    private static func enable() -> Outcome {
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
        if registered(service) { unregister(service, what: "login agent") }
        if registered(.mainApp) { unregister(.mainApp, what: "legacy login item") }
        if registered(.mainApp) {
            // The stuck path. Reported rather than worked around: registering the
            // agent alongside it would mean two launches at login, one with the
            // marker and one without, and whichever won the race would decide
            // whether the window appeared. `Outcome.legacyStuck` tells the user
            // the one thing that does clear it.
            NSLog("DezhbanMenu: the legacy login item could not be retracted")
            return .legacyStuck
        }
        return registered(service) ? .failed("the login agent is still registered") : .disabled
    }

    /// Retracts everything that could start this app at login, best effort.
    ///
    /// Used by the `--unregister-login-item` errand the uninstaller runs (see
    /// main.swift). `SMAppService.unregister()` is the only thing that actually
    /// retracts an agent registration — `launchctl bootout` unloads the job for
    /// this boot and leaves the record that recreates it at the next login — and
    /// only the app can call it.
    static func retractAll() {
        if registered(service) { unregister(service, what: "login agent") }
        if registered(.mainApp) { unregister(.mainApp, what: "legacy login item") }
    }

    /// Moves an install that registered `SMAppService.mainApp` (every build
    /// before the agent existed) onto the agent, exactly once per account.
    ///
    /// Gated on the OLD registration being enabled: a user who had
    /// login-at-launch switched off must not have it switched on by an upgrade.
    /// The attempt is recorded either way, so this runs at most once whether it
    /// succeeded or not — see `migratedKey`.
    static func migrateFromMainAppRegistration() {
        guard !UserDefaults.standard.bool(forKey: migratedKey) else { return }
        // An explicit "off" outlives every retry below. Without this, a migration
        // allowed to retry would re-register what the user had switched off — the
        // bug the persisted flag was introduced to kill.
        guard !UserDefaults.standard.bool(forKey: userDisabledKey) else {
            markMigrated()
            return
        }

        if registered(.mainApp) {
            unregister(.mainApp, what: "legacy login item")
            // Checked after the attempt rather than trusting it not to throw: what
            // matters is whether the old item is actually gone.
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
            // Recorded BEFORE the register below, and this is the whole point of
            // the flag: it is what a retry launch has to go on, since by then the
            // legacy item is gone and there is nothing else left to tell "this
            // account had a login item to migrate" from "this account never did".
            UserDefaults.standard.set(true, forKey: legacyRetractedKey)
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

    private static func markMigrated() {
        UserDefaults.standard.set(true, forKey: migratedKey)
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
